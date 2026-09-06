package tasks

import (
	"context"
	"errors"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/vidu"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"unicode/utf8"
)

const (
	viduOperationLeaseSeconds     = int64(90)
	viduOperationBatchSize        = 100
	viduOperationMaxAttempts      = 20_000
	viduOperationMaxPollDays      = 7
	viduPollingManualReviewReason = "provider task exceeded automatic polling horizon"
	viduRecoveryErrorMaxBytes     = 255
)

type claimedViduTask struct {
	Task       model.Task
	Operation  model.TaskOperation
	Private    viduTaskPrivateData
	ProviderID string
	BaseURL    string
	APIKey     string
}

func reconcileAsyncViduTasks(ctx context.Context) error {
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
	var all []error
	if err := quarantineExpiredViduOperations(ctx, now); err != nil {
		all = append(all, err)
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			viduTaskPlatform, []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, viduOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(viduOperationBatchSize).Find(&candidates).Error; err != nil {
		return errors.Join(append(all, err)...)
	}
	for i := range candidates {
		if ctx.Err() != nil {
			all = append(all, ctx.Err())
			break
		}
		op, ok, err := claimViduTaskOperation(ctx, &candidates[i], now)
		if err != nil {
			all = append(all, err)
			continue
		}
		if !ok {
			continue
		}
		claimed, ready, err := prepareClaimedViduTask(ctx, op, now)
		if err != nil {
			if releaseErr := releaseViduTaskOperation(op, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if !ready {
			continue
		}
		pollAt, err := renewViduTaskLease(ctx, claimed)
		if err != nil {
			all = append(all, err)
			continue
		}
		provider, _, err := newViduProvider().Fetch(ctx, claimed.BaseURL, claimed.APIKey, claimed.ProviderID)
		if err != nil {
			if releaseErr := releaseViduTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if err := persistViduPollResult(claimed, provider, pollAt); err != nil {
			if releaseErr := releaseViduTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}

func claimViduTaskOperation(ctx context.Context, candidate *model.TaskOperation, now int64) (*model.TaskOperation, bool, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.Platform != viduTaskPlatform {
		return nil, false, errors.New("invalid Vidu operation candidate")
	}
	id, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "vidu-task-" + id
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, viduTaskPlatform, candidate.State, now, viduOperationMaxAttempts, now).
		Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + viduOperationLeaseSeconds,
			"attempts": gorm.Expr("attempts + 1"), "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return nil, false, result.Error
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("id = ? AND lease_owner = ?", candidate.ID, owner).First(&operation).Error; err != nil {
		return nil, false, err
	}
	return &operation, true, nil
}

func prepareClaimedViduTask(ctx context.Context, operation *model.TaskOperation, now int64) (*claimedViduTask, bool, error) {
	if operation == nil || operation.LeaseOwner == "" || operation.Platform != viduTaskPlatform {
		return nil, false, errors.New("Vidu operation lease is missing")
	}
	var task model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
		operation.TaskID, viduTaskPlatform, operation.UserID, operation.ChannelID).First(&task).Error; err != nil {
		return nil, false, markViduOperationManualReview(operation, "Vidu task row is unavailable", now)
	}
	if !viduTaskOperationIdentityMatches(&task, operation) {
		return nil, false, markViduOperationManualReview(operation, "Vidu task identity mismatch", now)
	}
	reservation, err := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return nil, false, err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return nil, false, refundPreparedViduTaskWithLease(&task, reservation, operation, "submission stopped before provider dispatch")
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID == "" {
			return nil, false, markViduAmbiguousDispatchWithLease(&task, operation.ReservationID,
				viduUnknownDispatchReason, operation.LeaseOwner)
		}
		return nil, false, settlePendingViduOperation(&task, reservation, operation)
	case model.TaskOperationSubmitted:
		if operation.SettlementPending {
			return nil, false, settlePendingViduOperation(&task, reservation, operation)
		}
		if task.Status == model.TaskStatusFailure {
			return nil, false, reverseFailedViduTask(&task, operation, task.FailReason, task.Data)
		}
		if task.Status == model.TaskStatusSuccess {
			return nil, false, markSuccessfulViduOperation(operation, now)
		}
		if task.Status == model.TaskStatusUnknown {
			return nil, false, markViduOperationManualReview(operation, "Vidu task state is unknown", now)
		}
		return buildClaimedViduTask(&task, operation, now)
	default:
		return nil, false, markViduOperationManualReview(operation, "Vidu operation state is invalid", now)
	}
}

func settlePendingViduOperation(task *model.Task, reservation *billingsvc.RelayQuotaReservation, operation *model.TaskOperation) error {
	if operation.EncryptedProviderTaskID == "" {
		return errors.New("accepted Vidu operation is missing its provider id")
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		viduProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return markViduOperationManualReview(operation, "Vidu provider id cannot be decrypted", wallclock.NowTimestamp())
	}
	return settleAcceptedViduTask(task, reservation, providerID, operation.State, operation.LeaseOwner)
}

func buildClaimedViduTask(task *model.Task, operation *model.TaskOperation, now int64) (*claimedViduTask, bool, error) {
	privateData, err := decodeViduTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return nil, false, markViduOperationManualReview(operation, "Vidu recovery metadata is invalid", now)
	}
	if _, err := validateViduTaskPricingSnapshot(task, privateData, task.Quota); err != nil {
		return nil, false, markViduOperationManualReview(operation, "Vidu pricing snapshot is inconsistent", now)
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ? AND user_id = ? AND channel_id = ?", operation.ReservationID,
		operation.UserID, operation.ChannelID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != task.Quota || record.CompletedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return nil, false, markViduOperationManualReview(operation, "Vidu settled accounting snapshot is inconsistent", now)
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		viduProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return nil, false, markViduOperationManualReview(operation, "Vidu provider id cannot be decrypted", now)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		viduChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil {
		return nil, false, markViduOperationManualReview(operation, "Vidu channel key cannot be decrypted", now)
	}
	baseURL, err := vidu.EffectiveBaseURL(privateData.ChannelBaseURL)
	if err != nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, false, markViduOperationManualReview(operation, "Vidu channel snapshot is invalid", now)
	}
	return &claimedViduTask{Task: *task, Operation: *operation, Private: privateData,
		ProviderID: providerID, BaseURL: baseURL, APIKey: key}, true, nil
}

func renewViduTaskLease(ctx context.Context, claimed *claimedViduTask) (int64, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ? AND lease_expires_at > ?",
			claimed.Operation.ID, viduTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner, now).
		Updates(map[string]any{"lease_expires_at": now + viduOperationLeaseSeconds, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return 0, errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return now, nil
}

func persistViduPollResult(claimed *claimedViduTask, provider *vidu.Task, now int64) error {
	if claimed == nil || provider == nil || claimed.Operation.LeaseOwner == "" || provider.ProviderTaskID != claimed.ProviderID {
		return errors.New("invalid Vidu poll transition")
	}
	normalized, err := normalizedViduTaskData(provider)
	if err != nil {
		return err
	}
	data, err := marshalViduStoredTaskData(normalized)
	if err != nil {
		return err
	}
	if provider.Status == vidu.StatusFailed {
		reason := boundedViduFailReason(provider.ErrorCode)
		if reason == "" {
			reason = "Vidu task failed"
		}
		return reverseFailedViduTask(&claimed.Task, &claimed.Operation, reason, data)
	}
	status, progress, operationState := model.TaskStatusSubmitted, "10%", model.TaskOperationSubmitted
	nextAttemptAt, completedAt := now+viduOperationRetrySeconds, int64(0)
	switch provider.Status {
	case vidu.StatusSubmitted:
	case vidu.StatusProcessing:
		status, progress = model.TaskStatusRunning, "50%"
	case vidu.StatusSucceeded:
		if provider.ResultURL == "" {
			return errors.New("successful Vidu task has no result URL")
		}
		status, progress, operationState = model.TaskStatusSuccess, "100%", model.TaskOperationTerminal
		nextAttemptAt, completedAt = 0, now
	default:
		return errors.New("unknown Vidu task status")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", claimed.Task.ID,
			claimed.Task.TaskID, viduTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if isViduTerminalStatus(current.Status) {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		updates := map[string]any{"status": status, "progress": progress, "fail_reason": "", "data": data, "updated_at": now}
		if status == model.TaskStatusRunning && current.StartTime == 0 {
			updates["start_time"] = now
		}
		if status == model.TaskStatusSuccess {
			updates["finish_time"] = now
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status IN ?", current.ID,
			[]string{model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning}).Updates(updates)
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", claimed.Operation.ID,
				viduTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner).
			Updates(map[string]any{"state": operationState, "next_attempt_at": nextAttemptAt,
				"completed_at": completedAt, "updated_at": now, "last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func refundPreparedViduTaskWithLease(task *model.Task, reservation *billingsvc.RelayQuotaReservation,
	operation *model.TaskOperation, reason string) error {
	reason = boundedViduFailReason(reason)
	return reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
			viduTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if !viduTaskOperationIdentityMatches(&current, operation) || current.Status != model.TaskStatusNotStart {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status = ?", current.ID, model.TaskStatusNotStart).
			Updates(map[string]any{"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).Where("id = ? AND state = ? AND lease_owner = ?",
			operation.ID, model.TaskOperationPrepared, operation.LeaseOwner).
			Updates(map[string]any{"state": model.TaskOperationRefunded, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func markSuccessfulViduOperation(operation *model.TaskOperation, now int64) error {
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, viduTaskPlatform, model.TaskOperationSubmitted, operation.LeaseOwner).
		Updates(map[string]any{"state": model.TaskOperationTerminal, "settlement_pending": false,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now, "last_error": "",
			"lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func releaseViduTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * viduOperationRetrySeconds
	if backoff > 300 {
		backoff = 300
	}
	message := "Vidu recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "Vidu recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "Vidu provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, viduTaskPlatform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{"next_attempt_at": now + backoff, "updated_at": now,
			"last_error": message, "lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func markViduOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid Vidu manual-review transition")
	}
	reason = boundedViduRecoveryError(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
			operation.ID, viduTaskPlatform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{"state": model.TaskOperationManualReview, "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": reason, "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		taskResult := tx.Model(&model.Task{}).
			Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND status NOT IN ?",
				operation.TaskID, viduTaskPlatform, operation.UserID, operation.ChannelID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func quarantineExpiredViduOperations(ctx context.Context, now int64) error {
	cutoff := now - int64(viduOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			viduTaskPlatform, []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			viduOperationMaxAttempts, cutoff, now).Order("id asc").Limit(viduOperationBatchSize).Find(&operations).Error; err != nil {
		return err
	}
	var all []error
	for i := range operations {
		op := &operations[i]
		id, err := cryptoutil.SecureRandomUUID()
		if err != nil {
			all = append(all, err)
			continue
		}
		owner := "vidu-review-" + id
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				op.ID, viduTaskPlatform, op.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + viduOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				all = append(all, claim.Error)
			}
			continue
		}
		op.LeaseOwner = owner
		_, ready, processErr := prepareClaimedViduTask(ctx, op, now)
		if processErr != nil {
			all = append(all, processErr)
		} else if ready {
			all = append(all, markViduOperationManualReview(op, viduPollingManualReviewReason, now))
		}
	}
	return errors.Join(all...)
}

func boundedViduRecoveryError(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		return "Vidu recovery failed"
	}
	if len(message) <= viduRecoveryErrorMaxBytes {
		return message
	}
	cut := viduRecoveryErrorMaxBytes - len("... [truncated]")
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "... [truncated]"
}
