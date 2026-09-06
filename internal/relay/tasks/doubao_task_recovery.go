package tasks

import (
	"context"
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/doubao"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
)

func reconcileAsyncDoubaoTasks(ctx context.Context) error {
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
	if err := quarantineExpiredDoubaoOperations(now); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("quarantine expired Doubao operations: %w", err))
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform IN ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			model.DoubaoVideoTaskOperationPlatforms(),
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, doubaoOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(doubaoOperationBatchSize).
		Find(&candidates).Error; err != nil {
		return errors.Join(append(reconcileErrors, err)...)
	}
	for index := range candidates {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		claimed, ok, claimedAt, err := claimDoubaoTaskOperation(ctx, &candidates[index])
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("claim Doubao task %s: %w", candidates[index].TaskID, err))
			continue
		}
		if !ok {
			continue
		}
		if err := reconcileClaimedDoubaoTask(ctx, claimed, claimedAt); err != nil {
			releaseAt, clockErr := model.DatabaseUnixTimestamp(model.DB)
			if clockErr != nil {
				err = errors.Join(err, fmt.Errorf("read Doubao release clock: %w", clockErr))
			} else if releaseErr := releaseDoubaoTaskOperation(claimed, releaseAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile Doubao task %s: %w", claimed.TaskID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func claimDoubaoTaskOperation(
	ctx context.Context,
	candidate *model.TaskOperation,
) (*model.TaskOperation, bool, int64, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.TaskID == "" ||
		!model.IsDoubaoVideoTaskOperationPlatform(candidate.Platform) {
		return nil, false, 0, errors.New("invalid Doubao operation candidate")
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, 0, err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, false, 0, err
	}
	owner := "doubao-task-" + ownerID
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, candidate.Platform, candidate.State, now, doubaoOperationMaxAttempts, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + doubaoOperationLeaseSeconds,
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
		candidate.ID, candidate.Platform, owner).First(&claimed).Error; err != nil {
		return nil, false, 0, err
	}
	return &claimed, true, now, nil
}

func reconcileClaimedDoubaoTask(ctx context.Context, operation *model.TaskOperation, now int64) error {
	if operation == nil || !model.IsDoubaoVideoTaskOperationPlatform(operation.Platform) || operation.LeaseOwner == "" {
		return errors.New("Doubao operation lease is missing")
	}
	task, err := loadUniqueDoubaoRecoveryTask(ctx, operation)
	if err != nil {
		return markDoubaoOperationManualReview(operation, "Doubao task row is unavailable or ambiguous", now)
	}
	reservation, err := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return refundDoubaoTask(task, reservation, nil, "Doubao submission stopped before provider dispatch",
			model.TaskOperationPrepared, operation.LeaseOwner)
	case model.TaskOperationDispatching:
		privateData, decodeErr := decodeDoubaoTaskPrivateData(task.PrivateData)
		if decodeErr != nil {
			return markDoubaoOperationManualReview(operation, "Doubao recovery metadata is invalid", now)
		}
		if operation.EncryptedProviderTaskID != "" || privateData.EncryptedUpstreamTaskID != "" {
			return promoteClaimedDoubaoDispatch(task, operation, privateData, now)
		}
		return markDoubaoOperationManualReview(operation, doubaoUnknownDispatchReason, now)
	case model.TaskOperationSubmitted:
		provider, providerErr := doubaoProviderTaskFromStored(task, operation)
		if providerErr != nil {
			return markDoubaoOperationManualReview(operation, providerErr.Error(), now)
		}
		if provider != nil && (provider.Status == doubao.StatusSucceeded || provider.Status == doubao.StatusFailed) {
			return finishPolledDoubaoTask(task, reservation, operation, provider)
		}
		return pollSubmittedDoubaoTask(ctx, task, reservation, operation, now)
	default:
		return markDoubaoOperationManualReview(operation, "Doubao operation state is invalid", now)
	}
}

func loadUniqueDoubaoRecoveryTask(ctx context.Context, operation *model.TaskOperation) (*model.Task, error) {
	var tasks []model.Task
	result := model.DB.WithContext(ctx).
		Where("task_id = ? AND user_id = ? AND channel_id = ? AND platform = ?", operation.TaskID,
			operation.UserID, operation.ChannelID, operation.Platform).Limit(2).Find(&tasks)
	if result.Error != nil {
		return nil, result.Error
	}
	if len(tasks) != 1 || !doubaoTaskOperationIdentityMatches(&tasks[0], operation, operation.ReservationID) {
		return nil, errors.New("Doubao task identity is unavailable or ambiguous")
	}
	properties, err := decodeDoubaoTaskProperties(tasks[0].Properties)
	if err != nil || string(properties.Action) != tasks[0].Action {
		return nil, errors.New("Doubao task provenance is invalid")
	}
	return &tasks[0], nil
}

func promoteClaimedDoubaoDispatch(
	task *model.Task,
	operation *model.TaskOperation,
	privateData doubaoTaskPrivateData,
	now int64,
) error {
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	binding := doubaoProviderTaskBinding(task.TaskID, operation.ReservationID, task.Platform, task.UserId, task.ChannelId, properties.Action)
	providerID := ""
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedUpstreamTaskID} {
		if encoded == "" {
			continue
		}
		decoded, decryptErr := asyncTaskDecryptBound(encoded, binding)
		if decryptErr != nil {
			return markDoubaoOperationManualReview(operation, "Doubao provider id cannot be decrypted", now)
		}
		if providerID != "" && providerID != decoded {
			return markDoubaoOperationManualReview(operation, "Doubao provider identity conflicts with durable state", now)
		}
		providerID = decoded
	}
	if providerID == "" {
		return markDoubaoOperationManualReview(operation, doubaoUnknownDispatchReason, now)
	}
	provider := &doubao.Task{ProviderTaskID: providerID, Status: doubao.StatusSubmitted}
	return persistDoubaoProviderStatePending(task, operation.ReservationID, provider,
		model.TaskOperationDispatching, operation.LeaseOwner)
}

func pollSubmittedDoubaoTask(
	ctx context.Context,
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	operation *model.TaskOperation,
	now int64,
) error {
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil {
		return markDoubaoOperationManualReview(operation, "Doubao recovery metadata is invalid", now)
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || string(properties.Action) != task.Action {
		return markDoubaoOperationManualReview(operation, "Doubao task provenance is invalid", now)
	}
	providerTaskID, err := decryptDoubaoProviderTaskID(task, operation, privateData, properties.Action)
	if err != nil {
		return markDoubaoOperationManualReview(operation, "Doubao provider id cannot be decrypted", now)
	}
	channelKey, err := asyncTaskDecryptBound(
		privateData.EncryptedChannelKey,
		doubaoChannelCredentialBinding(task.TaskID, task.Platform, task.UserId, task.ChannelId, privateData.ChannelBaseURL),
	)
	if err != nil || channelKey == "" {
		return markDoubaoOperationManualReview(operation, "Doubao channel key cannot be decrypted", now)
	}
	provider, _, err := newDoubaoTaskClient().Fetch(ctx, privateData.ChannelBaseURL, channelKey, providerTaskID)
	if err != nil {
		return err
	}
	if provider.ProviderTaskID != providerTaskID {
		return markDoubaoOperationManualReview(operation, "Doubao provider identity changed during polling", now)
	}
	if provider.Status == doubao.StatusSucceeded || provider.Status == doubao.StatusFailed {
		return finishPolledDoubaoTask(task, reservation, operation, provider)
	}
	return persistAcceptedDoubaoTask(task, operation.ReservationID, provider,
		model.TaskOperationSubmitted, operation.LeaseOwner)
}

func finishPolledDoubaoTask(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	operation *model.TaskOperation,
	provider *doubao.Task,
) error {
	var err error
	if provider.Status == doubao.StatusSucceeded {
		err = settleSuccessfulDoubaoTask(task, reservation, provider,
			model.TaskOperationSubmitted, operation.LeaseOwner)
	} else {
		err = refundDoubaoTask(task, reservation, provider, provider.StatusMessage,
			model.TaskOperationSubmitted, operation.LeaseOwner)
	}
	if err == nil {
		if provider.Status == doubao.StatusSucceeded {
			if deliverErr := billingsvc.DeliverAuditLogOutboxEvent(doubaoAuditEventID(operation.ReservationID)); deliverErr != nil {
				logging.SysError("deliver recovered Doubao consume audit: " + deliverErr.Error())
			}
		}
		return nil
	}
	// The accounting transaction is atomic. Preserve terminal provider truth
	// and release this lease so the exact same settlement can be retried.
	if fallbackErr := persistDoubaoProviderStatePending(task, operation.ReservationID, provider,
		model.TaskOperationSubmitted, operation.LeaseOwner); fallbackErr == nil {
		logging.SysError("Doubao terminal accounting remains pending for " + task.TaskID + ": " + err.Error())
		return nil
	} else {
		return errors.Join(err, fallbackErr)
	}
}

func doubaoProviderTaskFromStored(task *model.Task, operation *model.TaskOperation) (*doubao.Task, error) {
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil {
		return nil, errors.New("Doubao recovery metadata is invalid")
	}
	data, err := decodeDoubaoStoredTaskData(task.Data)
	if err != nil {
		return nil, errors.New("Doubao stored provider state is invalid")
	}
	if !validDoubaoStoredStatus(data.Status) ||
		(data.Status != doubao.StatusSucceeded && data.Status != doubao.StatusFailed) {
		return nil, nil
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return nil, errors.New("Doubao task provenance is invalid")
	}
	providerID, err := decryptDoubaoProviderTaskID(task, operation, privateData, properties.Action)
	if err != nil {
		return nil, errors.New("Doubao provider id cannot be decrypted")
	}
	return &doubao.Task{
		ProviderTaskID: providerID, Status: data.Status, StatusMessage: data.StatusMessage,
		ErrorCode: data.ErrorCode,
		ResultURL: data.ResultURL, CompletionUnits: privateData.CompletionUnits,
		CreatedAt: data.ProviderCreatedAt, UpdatedAt: data.ProviderUpdatedAt,
	}, nil
}

func decryptDoubaoProviderTaskID(
	task *model.Task,
	operation *model.TaskOperation,
	privateData doubaoTaskPrivateData,
	action doubao.Action,
) (string, error) {
	binding := doubaoProviderTaskBinding(task.TaskID, operation.ReservationID, task.Platform, task.UserId, task.ChannelId, action)
	providerID := ""
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedUpstreamTaskID} {
		if encrypted == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || strings.TrimSpace(decoded) == "" {
			return "", errors.New("invalid encrypted Doubao provider identity")
		}
		if providerID != "" && providerID != decoded {
			return "", errors.New("conflicting encrypted Doubao provider identities")
		}
		providerID = decoded
	}
	if providerID == "" {
		return "", errors.New("Doubao provider identity is unavailable")
	}
	return providerID, nil
}

func releaseDoubaoTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * doubaoOperationRetrySeconds
	if backoff > int64(5*time.Minute/time.Second) {
		backoff = int64(5 * time.Minute / time.Second)
	}
	message := "Doubao recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "Doubao recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "Doubao provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
			operation.Platform, operation.State, operation.LeaseOwner).
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
		return billingsvc.ErrRelayQuotaReservationBusy
	}
	return nil
}

func markDoubaoOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || !model.IsDoubaoVideoTaskOperationPlatform(operation.Platform) || operation.LeaseOwner == "" {
		return errors.New("invalid Doubao manual-review transition")
	}
	reason = boundedDoubaoFailReason(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
				operation.Platform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "settlement_pending": operation.SettlementPending,
				"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
				"last_error": reason, "lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		return tx.Model(&model.Task{}).
			Where("task_id = ? AND user_id = ? AND channel_id = ? AND platform = ? AND status NOT IN ?",
				operation.TaskID, operation.UserID, operation.ChannelID, operation.Platform,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason,
				"updated_at": now, "finish_time": now, "progress": "100%",
			}).Error
	})
}

func quarantineExpiredDoubaoOperations(now int64) error {
	cutoff := now - int64(doubaoOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.Where("platform IN ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
		model.DoubaoVideoTaskOperationPlatforms(),
		[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
		doubaoOperationMaxAttempts, cutoff, now).
		Order("id asc").Limit(doubaoOperationBatchSize).Find(&operations).Error; err != nil {
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
		owner := "doubao-review-" + ownerID
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				operation.ID, operation.Platform, operation.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + doubaoOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				quarantineErrors = append(quarantineErrors, claim.Error)
			}
			continue
		}
		operation.LeaseOwner = owner
		task, err := loadUniqueDoubaoRecoveryTask(context.Background(), operation)
		if err != nil {
			if reviewErr := markDoubaoOperationManualReview(operation, "Doubao task identity is unavailable", now); reviewErr != nil {
				quarantineErrors = append(quarantineErrors, errors.Join(err, reviewErr))
			}
			continue
		}
		reservation, restoreErr := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
		if restoreErr != nil {
			quarantineErrors = append(quarantineErrors, restoreErr)
			continue
		}
		if operation.State == model.TaskOperationPrepared {
			if err := refundDoubaoTask(task, reservation, nil, "Doubao submission stopped before provider dispatch",
				model.TaskOperationPrepared, operation.LeaseOwner); err != nil {
				quarantineErrors = append(quarantineErrors, err)
			}
			continue
		}
		if operation.State == model.TaskOperationSubmitted {
			provider, providerErr := doubaoProviderTaskFromStored(task, operation)
			if providerErr == nil && provider != nil {
				if err := finishPolledDoubaoTask(task, reservation, operation, provider); err != nil {
					quarantineErrors = append(quarantineErrors, err)
				}
				continue
			}
		}
		reason := doubaoPollingManualReviewReason
		if operation.State == model.TaskOperationDispatching {
			reason = doubaoUnknownDispatchReason
		}
		if err := markDoubaoOperationManualReview(operation, reason, now); err != nil {
			quarantineErrors = append(quarantineErrors, err)
		}
	}
	return errors.Join(quarantineErrors...)
}
