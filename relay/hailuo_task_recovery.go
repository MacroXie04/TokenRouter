package relay

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/task/hailuo"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	hailuoOperationLeaseSeconds     = int64(90)
	hailuoOperationBatchSize        = 100
	hailuoOperationMaxAttempts      = 20_000
	hailuoOperationMaxPollDays      = 7
	hailuoPollingManualReviewReason = "provider task exceeded automatic polling horizon"
	hailuoRecoveryErrorMaxBytes     = 255
)

type claimedHailuoTask struct {
	Task       model.Task
	Operation  model.TaskOperation
	Private    hailuoTaskPrivateData
	ProviderID string
	BaseURL    string
	APIKey     string
}

func reconcileAsyncHailuoTasks(ctx context.Context) error {
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
	if err := quarantineExpiredHailuoOperations(ctx, now); err != nil {
		all = append(all, err)
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			hailuoTaskPlatform, []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, hailuoOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(hailuoOperationBatchSize).Find(&candidates).Error; err != nil {
		return errors.Join(append(all, err)...)
	}
	for i := range candidates {
		if ctx.Err() != nil {
			all = append(all, ctx.Err())
			break
		}
		op, ok, err := claimHailuoTaskOperation(ctx, &candidates[i], now)
		if err != nil {
			all = append(all, err)
			continue
		}
		if !ok {
			continue
		}
		claimed, ready, err := prepareClaimedHailuoTask(ctx, op, now)
		if err != nil {
			if releaseErr := releaseHailuoTaskOperation(op, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if !ready {
			continue
		}
		pollAt, err := renewHailuoTaskLease(ctx, claimed)
		if err != nil {
			all = append(all, err)
			continue
		}
		provider, _, err := newHailuoProvider().Fetch(ctx, claimed.BaseURL, claimed.APIKey, claimed.ProviderID)
		if err != nil {
			if releaseErr := releaseHailuoTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if err := persistHailuoPollResult(claimed, provider, pollAt); err != nil {
			if releaseErr := releaseHailuoTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}

func claimHailuoTaskOperation(ctx context.Context, candidate *model.TaskOperation, now int64) (*model.TaskOperation, bool, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.Platform != hailuoTaskPlatform {
		return nil, false, errors.New("invalid Hailuo operation candidate")
	}
	id, err := common.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "hailuo-task-" + id
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, hailuoTaskPlatform, candidate.State, now, hailuoOperationMaxAttempts, now).
		Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + hailuoOperationLeaseSeconds,
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

func prepareClaimedHailuoTask(ctx context.Context, operation *model.TaskOperation, now int64) (*claimedHailuoTask, bool, error) {
	if operation == nil || operation.LeaseOwner == "" || operation.Platform != hailuoTaskPlatform {
		return nil, false, errors.New("Hailuo operation lease is missing")
	}
	var task model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
		operation.TaskID, hailuoTaskPlatform, operation.UserID, operation.ChannelID).First(&task).Error; err != nil {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo task row is unavailable", now)
	}
	if !hailuoTaskOperationIdentityMatches(&task, operation) {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo task identity mismatch", now)
	}
	reservation, err := service.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return nil, false, err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return nil, false, refundPreparedHailuoTaskWithLease(&task, reservation, operation, "submission stopped before provider dispatch")
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID == "" {
			return nil, false, markHailuoAmbiguousDispatchWithLease(&task, operation.ReservationID,
				hailuoUnknownDispatchReason, operation.LeaseOwner)
		}
		return nil, false, settlePendingHailuoOperation(&task, reservation, operation)
	case model.TaskOperationSubmitted:
		if operation.SettlementPending {
			return nil, false, settlePendingHailuoOperation(&task, reservation, operation)
		}
		if task.Status == model.TaskStatusFailure {
			return nil, false, reverseFailedHailuoTask(&task, operation, task.FailReason, task.Data)
		}
		if task.Status == model.TaskStatusSuccess {
			return nil, false, markSuccessfulHailuoOperation(operation, now)
		}
		if task.Status == model.TaskStatusUnknown {
			return nil, false, markHailuoOperationManualReview(operation, "Hailuo task state is unknown", now)
		}
		return buildClaimedHailuoTask(&task, operation, now)
	default:
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo operation state is invalid", now)
	}
}

func settlePendingHailuoOperation(task *model.Task, reservation *service.RelayQuotaReservation, operation *model.TaskOperation) error {
	if operation.EncryptedProviderTaskID == "" {
		return errors.New("accepted Hailuo operation is missing its provider id")
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		hailuoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return markHailuoOperationManualReview(operation, "Hailuo provider id cannot be decrypted", common.NowTimestamp())
	}
	return settleAcceptedHailuoTask(task, reservation, providerID, operation.State, operation.LeaseOwner)
}

func buildClaimedHailuoTask(task *model.Task, operation *model.TaskOperation, now int64) (*claimedHailuoTask, bool, error) {
	privateData, err := decodeHailuoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo recovery metadata is invalid", now)
	}
	if _, err := validateHailuoTaskPricingSnapshot(task, privateData, task.Quota); err != nil {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo pricing snapshot is inconsistent", now)
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ? AND user_id = ? AND channel_id = ?", operation.ReservationID,
		operation.UserID, operation.ChannelID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != task.Quota || record.CompletedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo settled accounting snapshot is inconsistent", now)
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		hailuoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo provider id cannot be decrypted", now)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		hailuoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo channel key cannot be decrypted", now)
	}
	baseURL, err := hailuo.EffectiveBaseURL(privateData.ChannelBaseURL)
	if err != nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, false, markHailuoOperationManualReview(operation, "Hailuo channel snapshot is invalid", now)
	}
	return &claimedHailuoTask{Task: *task, Operation: *operation, Private: privateData,
		ProviderID: providerID, BaseURL: baseURL, APIKey: key}, true, nil
}

func renewHailuoTaskLease(ctx context.Context, claimed *claimedHailuoTask) (int64, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ? AND lease_expires_at > ?",
			claimed.Operation.ID, hailuoTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner, now).
		Updates(map[string]any{"lease_expires_at": now + hailuoOperationLeaseSeconds, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return 0, errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
	}
	return now, nil
}

func persistHailuoPollResult(claimed *claimedHailuoTask, provider *hailuo.Task, now int64) error {
	if claimed == nil || provider == nil || claimed.Operation.LeaseOwner == "" || provider.ProviderTaskID != claimed.ProviderID {
		return errors.New("invalid Hailuo poll transition")
	}
	normalized, err := normalizedHailuoTaskData(provider)
	if err != nil {
		return err
	}
	data, err := marshalHailuoStoredTaskData(normalized)
	if err != nil {
		return err
	}
	if provider.Status == hailuo.StatusFailed {
		reason := "Hailuo task failed"
		return reverseFailedHailuoTask(&claimed.Task, &claimed.Operation, reason, data)
	}
	status, progress, operationState := model.TaskStatusSubmitted, "10%", model.TaskOperationSubmitted
	nextAttemptAt, completedAt := now+hailuoOperationRetrySeconds, int64(0)
	switch provider.Status {
	case hailuo.StatusSubmitted:
	case hailuo.StatusProcessing:
		status, progress = model.TaskStatusRunning, "50%"
	case hailuo.StatusSucceeded:
		if provider.ResultURL == "" {
			return errors.New("successful Hailuo task has no result URL")
		}
		status, progress, operationState = model.TaskStatusSuccess, "100%", model.TaskOperationTerminal
		nextAttemptAt, completedAt = 0, now
	default:
		return errors.New("unknown Hailuo task status")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", claimed.Task.ID,
			claimed.Task.TaskID, hailuoTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if isHailuoTerminalStatus(current.Status) {
			return service.ErrRelayQuotaReservationBusy
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
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", claimed.Operation.ID,
				hailuoTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner).
			Updates(map[string]any{"state": operationState, "next_attempt_at": nextAttemptAt,
				"completed_at": completedAt, "updated_at": now, "last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func refundPreparedHailuoTaskWithLease(task *model.Task, reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation, reason string) error {
	reason = boundedHailuoFailReason(reason)
	return reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
			hailuoTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if !hailuoTaskOperationIdentityMatches(&current, operation) || current.Status != model.TaskStatusNotStart {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status = ?", current.ID, model.TaskStatusNotStart).
			Updates(map[string]any{"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).Where("id = ? AND state = ? AND lease_owner = ?",
			operation.ID, model.TaskOperationPrepared, operation.LeaseOwner).
			Updates(map[string]any{"state": model.TaskOperationRefunded, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func markSuccessfulHailuoOperation(operation *model.TaskOperation, now int64) error {
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, hailuoTaskPlatform, model.TaskOperationSubmitted, operation.LeaseOwner).
		Updates(map[string]any{"state": model.TaskOperationTerminal, "settlement_pending": false,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now, "last_error": "",
			"lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func releaseHailuoTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * hailuoOperationRetrySeconds
	if backoff > 300 {
		backoff = 300
	}
	message := "Hailuo recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "Hailuo recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "Hailuo provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, hailuoTaskPlatform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{"next_attempt_at": now + backoff, "updated_at": now,
			"last_error": message, "lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func markHailuoOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid Hailuo manual-review transition")
	}
	reason = boundedHailuoRecoveryError(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
			operation.ID, hailuoTaskPlatform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{"state": model.TaskOperationManualReview, "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": reason, "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		taskResult := tx.Model(&model.Task{}).
			Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND status NOT IN ?",
				operation.TaskID, hailuoTaskPlatform, operation.UserID, operation.ChannelID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func quarantineExpiredHailuoOperations(ctx context.Context, now int64) error {
	cutoff := now - int64(hailuoOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			hailuoTaskPlatform, []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			hailuoOperationMaxAttempts, cutoff, now).Order("id asc").Limit(hailuoOperationBatchSize).Find(&operations).Error; err != nil {
		return err
	}
	var all []error
	for i := range operations {
		op := &operations[i]
		id, err := common.SecureRandomUUID()
		if err != nil {
			all = append(all, err)
			continue
		}
		owner := "hailuo-review-" + id
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				op.ID, hailuoTaskPlatform, op.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + hailuoOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				all = append(all, claim.Error)
			}
			continue
		}
		op.LeaseOwner = owner
		_, ready, processErr := prepareClaimedHailuoTask(ctx, op, now)
		if processErr != nil {
			all = append(all, processErr)
		} else if ready {
			all = append(all, markHailuoOperationManualReview(op, hailuoPollingManualReviewReason, now))
		}
	}
	return errors.Join(all...)
}

func boundedHailuoRecoveryError(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		return "Hailuo recovery failed"
	}
	if len(message) <= hailuoRecoveryErrorMaxBytes {
		return message
	}
	cut := hailuoRecoveryErrorMaxBytes - len("... [truncated]")
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "... [truncated]"
}
