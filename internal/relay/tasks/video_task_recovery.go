package tasks

import (
	"context"
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/sora"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
	"unicode/utf8"
)

func reconcileAsyncVideoTasks(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if model.DB == nil || !model.DB.Migrator().HasTable(&model.TaskOperation{}) {
		return nil
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	var reconcileErrors []error
	if err := quarantineExpiredVideoOperations(now); err != nil {
		// A damaged or temporarily unavailable expired operation must not starve
		// unrelated due work. Preserve the error for observability and continue
		// draining the bounded recovery batch.
		reconcileErrors = append(reconcileErrors, fmt.Errorf("quarantine expired video operations: %w", err))
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform IN ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			videoTaskPlatforms(),
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, videoOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(videoOperationBatchSize).
		Find(&candidates).Error; err != nil {
		return errors.Join(append(reconcileErrors, err)...)
	}
	for index := range candidates {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		claimed, ok, claimedAt, err := claimVideoTaskOperation(ctx, &candidates[index])
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("claim video task %s: %w", candidates[index].TaskID, err))
			continue
		}
		if !ok {
			continue
		}
		if err := reconcileClaimedVideoTask(ctx, claimed, claimedAt); err != nil {
			releaseAt, clockErr := model.DatabaseUnixTimestamp(model.DB)
			if clockErr != nil {
				err = errors.Join(err, fmt.Errorf("read video release clock: %w", clockErr))
			} else if releaseErr := releaseVideoTaskOperation(claimed, releaseAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile video task %s: %w", claimed.TaskID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func claimVideoTaskOperation(
	ctx context.Context,
	candidate *model.TaskOperation,
) (*model.TaskOperation, bool, int64, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.TaskID == "" {
		return nil, false, 0, errors.New("invalid video operation candidate")
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, 0, err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, false, 0, err
	}
	owner := "video-task-" + ownerID
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, candidate.State, now, videoOperationMaxAttempts, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + videoOperationLeaseSeconds,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, 0, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, false, 0, nil
	}
	var claimed model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("id = ? AND lease_owner = ?", candidate.ID, owner).
		First(&claimed).Error; err != nil {
		return nil, false, 0, err
	}
	return &claimed, true, now, nil
}

func reconcileClaimedVideoTask(ctx context.Context, operation *model.TaskOperation, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("video operation lease is missing")
	}
	var task model.Task
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND user_id = ? AND channel_id = ?", operation.TaskID, operation.UserID, operation.ChannelID).
		First(&task).Error; err != nil {
		return markVideoOperationManualReview(operation, "video task row is unavailable", now)
	}
	if !isVideoTaskPlatform(&task) || operation.Platform != task.Platform {
		return markVideoOperationManualReview(operation, "video task identity mismatch", now)
	}
	reservation, err := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return refundPreparedVideoTaskWithLease(&task, reservation, operation, "submission stopped before provider dispatch")
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID != "" {
			return settlePendingVideoOperation(ctx, &task, reservation, operation)
		}
		return settleUnknownVideoDispatch(
			&task, reservation, videoUnknownDispatchReason, model.TaskOperationDispatching, operation.LeaseOwner,
		)
	case model.TaskOperationSubmitted:
		if operation.SettlementPending {
			if err := settlePendingVideoOperation(ctx, &task, reservation, operation); err != nil {
				return err
			}
			if err := model.DB.WithContext(ctx).Where("task_id = ?", operation.TaskID).First(&task).Error; err != nil {
				return err
			}
			fresh, err := loadVideoTaskOperation(operation.TaskID)
			if err != nil {
				return err
			}
			operation = fresh
			if operation.State != model.TaskOperationSubmitted {
				return nil
			}
			// Settlement releases the old lease. Claiming again is deferred to the
			// next bounded scheduler pass rather than polling without fencing.
			return nil
		}
		if task.Status == model.TaskStatusFailure {
			provider := videoProviderResponseFromTask(&task)
			return reverseFailedVideoTask(&task, operation, provider, task.FailReason)
		}
		if task.Status == model.TaskStatusSuccess {
			completedAt, err := model.DatabaseUnixTimestamp(model.DB)
			if err != nil {
				return err
			}
			return markSuccessfulVideoOperation(operation, completedAt)
		}
		return pollSubmittedVideoTask(ctx, &task, operation, now)
	default:
		return markVideoOperationManualReview(operation, "video operation state is invalid", now)
	}
}

func settlePendingVideoOperation(
	ctx context.Context,
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	operation *model.TaskOperation,
) error {
	if operation.EncryptedProviderTaskID == "" {
		return errors.New("accepted video operation is missing its provider id")
	}
	providerTaskID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		videoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	if err != nil {
		return err
	}
	provider := videoProviderResponseFromTask(task)
	if provider == nil {
		provider = &sora.Response{Status: "queued"}
	}
	provider.ID = providerTaskID
	provider.TaskID = providerTaskID
	return settleAcceptedVideoTask(
		task, reservation, provider, providerTaskID, operation.State, operation.LeaseOwner,
	)
}

func pollSubmittedVideoTask(
	ctx context.Context,
	task *model.Task,
	operation *model.TaskOperation,
	now int64,
) error {
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil {
		return markVideoOperationManualReview(operation, "video recovery metadata is invalid", now)
	}
	encryptedProviderID := operation.EncryptedProviderTaskID
	if encryptedProviderID == "" {
		encryptedProviderID = privateData.EncryptedUpstreamTaskID
	}
	providerTaskID, err := asyncTaskDecryptBound(
		encryptedProviderID,
		videoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID),
	)
	if err != nil {
		return markVideoOperationManualReview(operation, "video provider id cannot be decrypted", now)
	}
	channelKey, err := asyncTaskDecryptBound(
		privateData.EncryptedChannelKey,
		videoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL),
	)
	if err != nil {
		return markVideoOperationManualReview(operation, "video channel key cannot be decrypted", now)
	}
	provider, _, err := (&sora.Client{}).Fetch(ctx, privateData.ChannelBaseURL, channelKey, providerTaskID)
	if err != nil {
		return err
	}
	transitionAt, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	status, recognized := providerVideoTaskStatus(provider.Status)
	if status == model.TaskStatusFailure {
		reason := "video task failed"
		if provider.Error != nil {
			reason = provider.Error.Message
		}
		return reverseFailedVideoTask(task, operation, provider, reason)
	}
	if !recognized {
		status = task.Status
		if status != model.TaskStatusSubmitted && status != model.TaskStatusQueued && status != model.TaskStatusRunning {
			status = model.TaskStatusSubmitted
		}
	}
	return persistVideoPollResult(task, operation, provider, status, transitionAt)
}

func persistVideoPollResult(
	task *model.Task,
	operation *model.TaskOperation,
	provider *sora.Response,
	status string,
	now int64,
) error {
	if task == nil || operation == nil || provider == nil || operation.LeaseOwner == "" {
		return errors.New("invalid video poll transition")
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	response := sanitizedVideoResponse(
		task.TaskID, properties, provider, status, "", task.CreatedAt, now,
	)
	data, err := jsonutil.Marshal(response)
	if err != nil || len(data) > videoTaskProviderPayloadMaxBytes {
		return errors.New("video provider response is too large")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		taskUpdates := map[string]any{
			"status": status, "data": string(data), "progress": videoTaskProgress(status, provider.Progress),
			"fail_reason": "", "updated_at": now,
		}
		if status == model.TaskStatusRunning && task.StartTime == 0 {
			taskUpdates["start_time"] = now
		}
		operationState := model.TaskOperationSubmitted
		nextAttemptAt := now + videoOperationRetrySeconds
		completedAt := int64(0)
		if status == model.TaskStatusSuccess {
			taskUpdates["finish_time"] = now
			operationState = model.TaskOperationTerminal
			nextAttemptAt = 0
			completedAt = now
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status IN ?", task.ID, task.TaskID,
				[]string{model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning, status}).
			Updates(taskUpdates)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			return errors.New("video task changed during provider poll")
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID,
				model.TaskOperationSubmitted, operation.LeaseOwner).
			Updates(map[string]any{
				"state": operationState, "next_attempt_at": nextAttemptAt,
				"completed_at": completedAt, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil
	})
}

func refundPreparedVideoTaskWithLease(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	operation *model.TaskOperation,
	reason string,
) error {
	reason = boundedVideoFailReason(reason)
	err := reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND task_id = ? AND reservation_id = ?", operation.ID,
			operation.TaskID, operation.ReservationID).First(&currentOperation).Error; err != nil {
			return err
		}
		if !videoTaskOperationIdentityMatches(&currentTask, &currentOperation, operation.ReservationID) {
			return errors.New("prepared video refund identity mismatch")
		}
		if currentOperation.State == model.TaskOperationRefunded {
			if currentTask.Status != model.TaskStatusFailure || currentTask.Quota != 0 ||
				currentTask.Progress != "100%" || currentTask.FinishTime <= 0 ||
				currentOperation.SettlementPending || currentOperation.EncryptedProviderTaskID != "" ||
				currentOperation.LeaseOwner != "" {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
			return nil
		}
		if currentOperation.State != model.TaskOperationPrepared ||
			currentOperation.LeaseOwner != operation.LeaseOwner || currentTask.Status != model.TaskStatusNotStart {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND user_id = ? AND channel_id = ? AND status = ?",
				task.ID, task.TaskID, operation.UserID, operation.ChannelID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0, "progress": "100%",
				"finish_time": now, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?", operation.ID,
				operation.TaskID, operation.ReservationID, model.TaskOperationPrepared, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationRefunded, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil && !videoRefundTransitionMatches(task.TaskID, operation.ReservationID) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func markSuccessfulVideoOperation(operation *model.TaskOperation, now int64) error {
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND state = ? AND lease_owner = ?", operation.ID,
			model.TaskOperationSubmitted, operation.LeaseOwner).
		Updates(map[string]any{
			"state": model.TaskOperationTerminal, "settlement_pending": false,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now,
			"last_error": "", "lease_owner": "", "lease_expires_at": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return billingsvc.ErrRelayQuotaReservationBusy
	}
	return nil
}

func releaseVideoTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * videoOperationRetrySeconds
	if backoff > int64(5*time.Minute/time.Second) {
		backoff = int64(5 * time.Minute / time.Second)
	}
	message := "video recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "video recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "video provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, operation.State, operation.LeaseOwner).
		Updates(map[string]any{
			"next_attempt_at": now + backoff, "updated_at": now, "last_error": message,
			"lease_owner": "", "lease_expires_at": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return billingsvc.ErrRelayQuotaReservationBusy
	}
	return nil
}

func markVideoOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	reason = boundedVideoOperationError(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, operation.State, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "settlement_pending": operation.SettlementPending,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now,
				"last_error": reason, "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return tx.Model(&model.Task{}).
			Where("task_id = ? AND user_id = ? AND status NOT IN ?", operation.TaskID, operation.UserID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason,
				"updated_at": now, "finish_time": now,
			}).Error
	})
}

func quarantineExpiredVideoOperations(now int64) error {
	cutoff := now - int64(videoOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.Where("platform IN ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
		videoTaskPlatforms(),
		[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
		videoOperationMaxAttempts, cutoff, now).
		Order("id asc").Limit(videoOperationBatchSize).Find(&operations).Error; err != nil {
		return err
	}
	var quarantineErrors []error
	for index := range operations {
		operation := &operations[index]
		ownerID, err := cryptoutil.SecureRandomUUID()
		if err != nil {
			quarantineErrors = append(quarantineErrors, err)
			continue
		}
		owner := "video-review-" + ownerID
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)", operation.ID, operation.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + videoOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				quarantineErrors = append(quarantineErrors, claim.Error)
			}
			continue
		}
		operation.LeaseOwner = owner
		if operation.State == model.TaskOperationPrepared {
			var task model.Task
			if err := model.DB.Where("task_id = ? AND user_id = ? AND channel_id = ?", operation.TaskID,
				operation.UserID, operation.ChannelID).First(&task).Error; err != nil ||
				!isVideoTaskPlatform(&task) || operation.Platform != task.Platform {
				if reviewErr := markVideoOperationManualReview(operation, "prepared video task identity is unavailable", now); reviewErr != nil {
					quarantineErrors = append(quarantineErrors, errors.Join(err, reviewErr))
				}
				continue
			}
			reservation, err := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
			if err != nil {
				quarantineErrors = append(quarantineErrors, err)
				continue
			}
			if err := refundPreparedVideoTaskWithLease(
				&task, reservation, operation, "submission stopped before provider dispatch",
			); err != nil {
				quarantineErrors = append(quarantineErrors, err)
			}
			continue
		}
		if operation.State == model.TaskOperationSubmitted {
			var task model.Task
			if err := model.DB.Where("task_id = ? AND user_id = ? AND channel_id = ?", operation.TaskID,
				operation.UserID, operation.ChannelID).First(&task).Error; err != nil ||
				!isVideoTaskPlatform(&task) || operation.Platform != task.Platform {
				if reviewErr := markVideoOperationManualReview(operation, "video task identity is unavailable", now); reviewErr != nil {
					quarantineErrors = append(quarantineErrors, errors.Join(err, reviewErr))
				}
				continue
			}
			if (task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusSuccess) &&
				operation.SettlementPending {
				reservation, restoreErr := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
				if restoreErr != nil {
					quarantineErrors = append(quarantineErrors, restoreErr)
					continue
				}
				if settleErr := settlePendingVideoOperation(context.Background(), &task, reservation, operation); settleErr != nil {
					quarantineErrors = append(quarantineErrors, settleErr)
					continue
				}
				if err := model.DB.Where("task_id = ?", operation.TaskID).First(&task).Error; err != nil {
					quarantineErrors = append(quarantineErrors, err)
					continue
				}
				fresh, loadErr := loadVideoTaskOperation(operation.TaskID)
				if loadErr != nil {
					quarantineErrors = append(quarantineErrors, loadErr)
					continue
				}
				operation = fresh
			}
			if task.Status == model.TaskStatusFailure {
				if err := reverseFailedVideoTask(
					&task, operation, videoProviderResponseFromTask(&task), task.FailReason,
				); err != nil {
					quarantineErrors = append(quarantineErrors, err)
				}
				continue
			}
			if task.Status == model.TaskStatusSuccess {
				if operation.State == model.TaskOperationSubmitted {
					if err := markSuccessfulVideoOperation(operation, now); err != nil {
						quarantineErrors = append(quarantineErrors, err)
					}
				}
				continue
			}
		}
		reason := videoPollingManualReviewReason
		if operation.State == model.TaskOperationDispatching {
			reason = videoUnknownDispatchReason
		}
		if err := markVideoOperationManualReview(operation, reason, now); err != nil {
			quarantineErrors = append(quarantineErrors, err)
		}
	}
	return errors.Join(quarantineErrors...)
}

func videoProviderResponseFromTask(task *model.Task) *sora.Response {
	if task == nil || strings.TrimSpace(task.Data) == "" || task.Data == "null" ||
		len(task.Data) > videoTaskProviderPayloadMaxBytes {
		return nil
	}
	var response sora.Response
	if jsonutil.UnmarshalJsonStr(task.Data, &response) != nil {
		return nil
	}
	return &response
}

func boundedVideoOperationError(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		return "video recovery failed"
	}
	if len(message) <= 255 {
		return message
	}
	cut := 240
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "... [truncated]"
}
