package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/kling"
	"github.com/tokenrouter/tokenrouter/service"
)

func reconcileAsyncKlingTasks(ctx context.Context) error {
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
	if err := quarantineExpiredKlingOperations(now); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("quarantine expired Kling operations: %w", err))
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			klingTaskPlatform,
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, klingOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(klingOperationBatchSize).
		Find(&candidates).Error; err != nil {
		return errors.Join(append(reconcileErrors, err)...)
	}
	for index := range candidates {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		claimed, ok, claimedAt, err := claimKlingTaskOperation(ctx, &candidates[index])
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("claim Kling task %s: %w", candidates[index].TaskID, err))
			continue
		}
		if !ok {
			continue
		}
		if err := reconcileClaimedKlingTask(ctx, claimed, claimedAt); err != nil {
			releaseAt, clockErr := model.DatabaseUnixTimestamp(model.DB)
			if clockErr != nil {
				err = errors.Join(err, fmt.Errorf("read Kling release clock: %w", clockErr))
			} else if releaseErr := releaseKlingTaskOperation(claimed, releaseAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile Kling task %s: %w", claimed.TaskID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func claimKlingTaskOperation(
	ctx context.Context,
	candidate *model.TaskOperation,
) (*model.TaskOperation, bool, int64, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.TaskID == "" || candidate.Platform != klingTaskPlatform {
		return nil, false, 0, errors.New("invalid Kling operation candidate")
	}
	ownerID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, false, 0, err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, false, 0, err
	}
	owner := "kling-task-" + ownerID
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, klingTaskPlatform, candidate.State, now, klingOperationMaxAttempts, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + klingOperationLeaseSeconds,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, 0, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, false, 0, nil
	}
	var claimed model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("id = ? AND platform = ? AND lease_owner = ?",
		candidate.ID, klingTaskPlatform, owner).First(&claimed).Error; err != nil {
		return nil, false, 0, err
	}
	return &claimed, true, now, nil
}

func reconcileClaimedKlingTask(ctx context.Context, operation *model.TaskOperation, now int64) error {
	if operation == nil || operation.Platform != klingTaskPlatform || operation.LeaseOwner == "" {
		return errors.New("Kling operation lease is missing")
	}
	task, err := loadUniqueKlingRecoveryTask(ctx, operation)
	if err != nil {
		return markKlingOperationManualReview(operation, "Kling task row is unavailable or ambiguous", now)
	}
	reservation, err := service.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return refundKlingTask(task, reservation, nil, "Kling submission stopped before provider dispatch",
			model.TaskOperationPrepared, operation.LeaseOwner)
	case model.TaskOperationDispatching:
		privateData, decodeErr := decodeKlingTaskPrivateData(task.PrivateData)
		if decodeErr != nil {
			return markKlingOperationManualReview(operation, "Kling recovery metadata is invalid", now)
		}
		if operation.EncryptedProviderTaskID != "" || privateData.EncryptedUpstreamTaskID != "" {
			return promoteClaimedKlingDispatch(task, operation, privateData, now)
		}
		return markKlingOperationManualReview(operation, klingUnknownDispatchReason, now)
	case model.TaskOperationSubmitted:
		provider, providerErr := klingProviderTaskFromStored(task, operation)
		if providerErr != nil {
			return markKlingOperationManualReview(operation, providerErr.Error(), now)
		}
		if provider != nil && (provider.Status == kling.StatusSucceeded || provider.Status == kling.StatusFailed) {
			return finishPolledKlingTask(task, reservation, operation, provider)
		}
		return pollSubmittedKlingTask(ctx, task, reservation, operation, now)
	default:
		return markKlingOperationManualReview(operation, "Kling operation state is invalid", now)
	}
}

func loadUniqueKlingRecoveryTask(ctx context.Context, operation *model.TaskOperation) (*model.Task, error) {
	var tasks []model.Task
	result := model.DB.WithContext(ctx).
		Where("task_id = ? AND user_id = ? AND channel_id = ? AND platform = ?", operation.TaskID,
			operation.UserID, operation.ChannelID, klingTaskPlatform).Limit(2).Find(&tasks)
	if result.Error != nil {
		return nil, result.Error
	}
	if len(tasks) != 1 || !klingTaskOperationIdentityMatches(&tasks[0], operation, operation.ReservationID) {
		return nil, errors.New("Kling task identity is unavailable or ambiguous")
	}
	properties, err := decodeKlingTaskProperties(tasks[0].Properties)
	if err != nil || string(properties.Action) != tasks[0].Action {
		return nil, errors.New("Kling task provenance is invalid")
	}
	return &tasks[0], nil
}

func promoteClaimedKlingDispatch(
	task *model.Task,
	operation *model.TaskOperation,
	privateData klingTaskPrivateData,
	now int64,
) error {
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	binding := klingProviderTaskBinding(task.TaskID, operation.ReservationID, task.UserId, task.ChannelId, properties.Action)
	providerID := ""
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedUpstreamTaskID} {
		if encoded == "" {
			continue
		}
		decoded, decryptErr := asyncTaskDecryptBound(encoded, binding)
		if decryptErr != nil {
			return markKlingOperationManualReview(operation, "Kling provider id cannot be decrypted", now)
		}
		if providerID != "" && providerID != decoded {
			return markKlingOperationManualReview(operation, "Kling provider identity conflicts with durable state", now)
		}
		providerID = decoded
	}
	if providerID == "" {
		return markKlingOperationManualReview(operation, klingUnknownDispatchReason, now)
	}
	provider := &kling.Task{ProviderTaskID: providerID, Status: kling.StatusSubmitted}
	return persistKlingProviderStatePending(task, operation.ReservationID, provider,
		model.TaskOperationDispatching, operation.LeaseOwner)
}

func pollSubmittedKlingTask(
	ctx context.Context,
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation,
	now int64,
) error {
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil {
		return markKlingOperationManualReview(operation, "Kling recovery metadata is invalid", now)
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		return markKlingOperationManualReview(operation, "Kling task provenance is invalid", now)
	}
	providerTaskID, err := decryptKlingProviderTaskID(task, operation, privateData, properties.Action)
	if err != nil {
		return markKlingOperationManualReview(operation, "Kling provider id cannot be decrypted", now)
	}
	channelKey, err := asyncTaskDecryptBound(
		privateData.EncryptedChannelKey,
		klingChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL),
	)
	if err != nil || channelKey == "" {
		return markKlingOperationManualReview(operation, "Kling channel key cannot be decrypted", now)
	}
	provider, _, err := newKlingTaskClient().Fetch(ctx, privateData.ChannelBaseURL, channelKey,
		properties.Action, providerTaskID)
	if err != nil {
		return err
	}
	if provider.ProviderTaskID != providerTaskID {
		return markKlingOperationManualReview(operation, "Kling provider identity changed during polling", now)
	}
	if provider.Status == kling.StatusSucceeded || provider.Status == kling.StatusFailed {
		return finishPolledKlingTask(task, reservation, operation, provider)
	}
	return persistAcceptedKlingTask(task, operation.ReservationID, provider,
		model.TaskOperationSubmitted, operation.LeaseOwner)
}

func finishPolledKlingTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation,
	provider *kling.Task,
) error {
	var err error
	if provider.Status == kling.StatusSucceeded {
		err = settleSuccessfulKlingTask(task, reservation, provider,
			model.TaskOperationSubmitted, operation.LeaseOwner)
	} else {
		err = refundKlingTask(task, reservation, provider, provider.StatusMessage,
			model.TaskOperationSubmitted, operation.LeaseOwner)
	}
	if err == nil {
		if provider.Status == kling.StatusSucceeded {
			if deliverErr := service.DeliverAuditLogOutboxEvent(klingAuditEventID(operation.ReservationID)); deliverErr != nil {
				common.SysError("deliver recovered Kling consume audit: " + deliverErr.Error())
			}
		}
		return nil
	}
	// The accounting transaction is atomic. Preserve terminal provider truth
	// and release this lease so the exact same settlement can be retried.
	if fallbackErr := persistKlingProviderStatePending(task, operation.ReservationID, provider,
		model.TaskOperationSubmitted, operation.LeaseOwner); fallbackErr == nil {
		common.SysError("Kling terminal accounting remains pending for " + task.TaskID + ": " + err.Error())
		return nil
	} else {
		return errors.Join(err, fallbackErr)
	}
}

func klingProviderTaskFromStored(task *model.Task, operation *model.TaskOperation) (*kling.Task, error) {
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil {
		return nil, errors.New("Kling recovery metadata is invalid")
	}
	data, err := decodeKlingStoredTaskData(task.Data)
	if err != nil {
		return nil, errors.New("Kling stored provider state is invalid")
	}
	if !validKlingStoredStatus(data.Status) ||
		(data.Status != kling.StatusSucceeded && data.Status != kling.StatusFailed) {
		return nil, nil
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return nil, errors.New("Kling task provenance is invalid")
	}
	providerID, err := decryptKlingProviderTaskID(task, operation, privateData, properties.Action)
	if err != nil {
		return nil, errors.New("Kling provider id cannot be decrypted")
	}
	return &kling.Task{
		ProviderTaskID: providerID, Status: data.Status, StatusMessage: data.StatusMessage,
		ResultURL: data.ResultURL, CompletionUnits: privateData.CompletionUnits,
		CreatedAt: data.ProviderCreatedAt, UpdatedAt: data.ProviderUpdatedAt,
	}, nil
}

func decryptKlingProviderTaskID(
	task *model.Task,
	operation *model.TaskOperation,
	privateData klingTaskPrivateData,
	action kling.Action,
) (string, error) {
	binding := klingProviderTaskBinding(task.TaskID, operation.ReservationID, task.UserId, task.ChannelId, action)
	providerID := ""
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedUpstreamTaskID} {
		if encrypted == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || strings.TrimSpace(decoded) == "" {
			return "", errors.New("invalid encrypted Kling provider identity")
		}
		if providerID != "" && providerID != decoded {
			return "", errors.New("conflicting encrypted Kling provider identities")
		}
		providerID = decoded
	}
	if providerID == "" {
		return "", errors.New("Kling provider identity is unavailable")
	}
	return providerID, nil
}

func releaseKlingTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * klingOperationRetrySeconds
	if backoff > int64(5*time.Minute/time.Second) {
		backoff = int64(5 * time.Minute / time.Second)
	}
	message := "Kling recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "Kling recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "Kling provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
			klingTaskPlatform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{
			"next_attempt_at": now + backoff, "updated_at": now, "last_error": message,
			"lease_owner": "", "lease_expires_at": int64(0),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		// A successful transition clears the lease itself. Do not turn that into
		// a false failure when a caller returned an observational error later.
		var current model.TaskOperation
		if model.DB.Where("id = ?", operation.ID).First(&current).Error == nil && current.LeaseOwner == "" &&
			current.State != operation.State {
			return nil
		}
		return service.ErrRelayQuotaReservationBusy
	}
	return nil
}

func markKlingOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.Platform != klingTaskPlatform || operation.LeaseOwner == "" {
		return errors.New("invalid Kling manual-review transition")
	}
	reason = boundedKlingFailReason(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
				klingTaskPlatform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "settlement_pending": operation.SettlementPending,
				"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
				"last_error": reason, "lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return tx.Model(&model.Task{}).
			Where("task_id = ? AND user_id = ? AND channel_id = ? AND platform = ? AND status NOT IN ?",
				operation.TaskID, operation.UserID, operation.ChannelID, klingTaskPlatform,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason,
				"updated_at": now, "finish_time": now, "progress": "100%",
			}).Error
	})
}

func quarantineExpiredKlingOperations(now int64) error {
	cutoff := now - int64(klingOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.Where("platform = ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
		klingTaskPlatform,
		[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
		klingOperationMaxAttempts, cutoff, now).
		Order("id asc").Limit(klingOperationBatchSize).Find(&operations).Error; err != nil {
		return err
	}
	var quarantineErrors []error
	for index := range operations {
		operation := &operations[index]
		ownerID, err := common.SecureRandomUUID()
		if err != nil {
			quarantineErrors = append(quarantineErrors, err)
			continue
		}
		owner := "kling-review-" + ownerID
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				operation.ID, klingTaskPlatform, operation.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + klingOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				quarantineErrors = append(quarantineErrors, claim.Error)
			}
			continue
		}
		operation.LeaseOwner = owner
		task, err := loadUniqueKlingRecoveryTask(context.Background(), operation)
		if err != nil {
			if reviewErr := markKlingOperationManualReview(operation, "Kling task identity is unavailable", now); reviewErr != nil {
				quarantineErrors = append(quarantineErrors, errors.Join(err, reviewErr))
			}
			continue
		}
		reservation, restoreErr := service.RestoreRelayQuotaReservation(operation.ReservationID)
		if restoreErr != nil {
			quarantineErrors = append(quarantineErrors, restoreErr)
			continue
		}
		if operation.State == model.TaskOperationPrepared {
			if err := refundKlingTask(task, reservation, nil, "Kling submission stopped before provider dispatch",
				model.TaskOperationPrepared, operation.LeaseOwner); err != nil {
				quarantineErrors = append(quarantineErrors, err)
			}
			continue
		}
		if operation.State == model.TaskOperationSubmitted {
			provider, providerErr := klingProviderTaskFromStored(task, operation)
			if providerErr == nil && provider != nil {
				if err := finishPolledKlingTask(task, reservation, operation, provider); err != nil {
					quarantineErrors = append(quarantineErrors, err)
				}
				continue
			}
		}
		reason := klingPollingManualReviewReason
		if operation.State == model.TaskOperationDispatching {
			reason = klingUnknownDispatchReason
		}
		if err := markKlingOperationManualReview(operation, reason, now); err != nil {
			quarantineErrors = append(quarantineErrors, err)
		}
	}
	return errors.Join(quarantineErrors...)
}
