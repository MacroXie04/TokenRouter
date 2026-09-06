package relay

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	aliWan "github.com/tokenrouter/tokenrouter/relay/channel/task/ali"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	aliWanOperationLeaseSeconds     = int64(90)
	aliWanOperationBatchSize        = 100
	aliWanOperationMaxAttempts      = 20_000
	aliWanOperationMaxPollDays      = 7
	aliWanPollingManualReviewReason = "provider task exceeded automatic polling horizon"
	aliWanRecoveryErrorMaxBytes     = 255
)

type claimedAliWanTask struct {
	Task       model.Task
	Operation  model.TaskOperation
	Private    aliWanTaskPrivateData
	ProviderID string
	BaseURL    string
	APIKey     string
}

func reconcileAsyncAliWanTasks(ctx context.Context) error {
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
	if err := quarantineExpiredAliWanOperations(ctx, now); err != nil {
		all = append(all, err)
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			aliWanTaskPlatform, []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, aliWanOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(aliWanOperationBatchSize).Find(&candidates).Error; err != nil {
		return errors.Join(append(all, err)...)
	}
	for i := range candidates {
		if ctx.Err() != nil {
			all = append(all, ctx.Err())
			break
		}
		op, ok, err := claimAliWanTaskOperation(ctx, &candidates[i], now)
		if err != nil {
			all = append(all, err)
			continue
		}
		if !ok {
			continue
		}
		claimed, ready, err := prepareClaimedAliWanTask(ctx, op, now)
		if err != nil {
			if releaseErr := releaseAliWanTaskOperation(op, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if !ready {
			continue
		}
		pollAt, err := renewAliWanTaskLease(ctx, claimed)
		if err != nil {
			all = append(all, err)
			continue
		}
		provider, _, err := newAliWanProvider().Fetch(ctx, claimed.BaseURL, claimed.APIKey, claimed.ProviderID)
		if err != nil {
			if releaseErr := releaseAliWanTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				all = append(all, releaseErr)
			}
			all = append(all, sanitizedAliWanProviderPollError(err))
			continue
		}
		if err := persistAliWanPollResult(claimed, provider, pollAt); err != nil {
			if releaseErr := releaseAliWanTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}

func sanitizedAliWanProviderPollError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("Alibaba Wan provider polling failed")
	}
}

func claimAliWanTaskOperation(ctx context.Context, candidate *model.TaskOperation, now int64) (*model.TaskOperation, bool, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.Platform != aliWanTaskPlatform {
		return nil, false, errors.New("invalid AliWan operation candidate")
	}
	id, err := common.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "ali-wan-task-" + id
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, aliWanTaskPlatform, candidate.State, now, aliWanOperationMaxAttempts, now).
		Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + aliWanOperationLeaseSeconds,
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

func prepareClaimedAliWanTask(ctx context.Context, operation *model.TaskOperation, now int64) (*claimedAliWanTask, bool, error) {
	if operation == nil || operation.LeaseOwner == "" || operation.Platform != aliWanTaskPlatform {
		return nil, false, errors.New("AliWan operation lease is missing")
	}
	var task model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
		operation.TaskID, aliWanTaskPlatform, operation.UserID, operation.ChannelID).First(&task).Error; err != nil {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan task row is unavailable", now)
	}
	if !aliWanTaskOperationIdentityMatches(&task, operation) {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan task identity mismatch", now)
	}
	reservation, err := service.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return nil, false, err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return nil, false, refundPreparedAliWanTaskWithLease(&task, reservation, operation, "submission stopped before provider dispatch")
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID == "" {
			return nil, false, markAliWanAmbiguousDispatchWithLease(&task, operation.ReservationID,
				aliWanUnknownDispatchReason, operation.LeaseOwner)
		}
		return nil, false, settlePendingAliWanOperation(&task, reservation, operation, now)
	case model.TaskOperationSubmitted:
		if operation.SettlementPending {
			return nil, false, settlePendingAliWanOperation(&task, reservation, operation, now)
		}
		if task.Status == model.TaskStatusFailure {
			return nil, false, reverseFailedAliWanTask(&task, operation, task.FailReason, task.Data)
		}
		if task.Status == model.TaskStatusSuccess {
			return nil, false, markSuccessfulAliWanOperation(operation, now)
		}
		if task.Status == model.TaskStatusUnknown {
			return nil, false, markAliWanOperationManualReview(operation, "AliWan task state is unknown", now)
		}
		return buildClaimedAliWanTask(&task, operation, now)
	default:
		return nil, false, markAliWanOperationManualReview(operation, "AliWan operation state is invalid", now)
	}
}

func settlePendingAliWanOperation(task *model.Task, reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation, now int64,
) error {
	if operation.EncryptedProviderTaskID == "" {
		return errors.New("accepted AliWan operation is missing its provider id")
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		aliWanProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return markAliWanOperationManualReview(operation, "AliWan provider id cannot be decrypted", now)
	}
	return settleAcceptedAliWanTask(task, reservation, providerID, operation.State, operation.LeaseOwner)
}

func buildClaimedAliWanTask(task *model.Task, operation *model.TaskOperation, now int64) (*claimedAliWanTask, bool, error) {
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan recovery metadata is invalid", now)
	}
	if _, err := validateAliWanTaskPricingSnapshot(task, privateData, task.Quota); err != nil {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan pricing snapshot is inconsistent", now)
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ? AND user_id = ? AND channel_id = ?", operation.ReservationID,
		operation.UserID, operation.ChannelID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != task.Quota || record.CompletedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan settled accounting snapshot is inconsistent", now)
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		aliWanProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan provider id cannot be decrypted", now)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		aliWanChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan channel key cannot be decrypted", now)
	}
	baseURL, err := aliWan.EffectiveBaseURL(privateData.ChannelBaseURL)
	if err != nil || strings.TrimSpace(key) == "" || len(key) > 8<<10 || strings.ContainsAny(key, "\r\n\x00") {
		return nil, false, markAliWanOperationManualReview(operation, "AliWan channel snapshot is invalid", now)
	}
	return &claimedAliWanTask{Task: *task, Operation: *operation, Private: privateData,
		ProviderID: providerID, BaseURL: baseURL, APIKey: key}, true, nil
}

func renewAliWanTaskLease(ctx context.Context, claimed *claimedAliWanTask) (int64, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ? AND lease_expires_at > ?",
			claimed.Operation.ID, aliWanTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner, now).
		Updates(map[string]any{"lease_expires_at": now + aliWanOperationLeaseSeconds, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return 0, errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
	}
	return now, nil
}

func persistAliWanPollResult(claimed *claimedAliWanTask, provider *aliWan.Task, now int64) error {
	if claimed == nil || provider == nil || claimed.Operation.LeaseOwner == "" || provider.ProviderTaskID != claimed.ProviderID {
		return errors.New("invalid AliWan poll transition")
	}
	normalized, err := normalizedAliWanTaskData(provider)
	if err != nil {
		return err
	}
	data, err := marshalAliWanStoredTaskData(normalized)
	if err != nil {
		return err
	}
	if provider.Status == aliWan.StatusFailed {
		reason := normalized.ErrorMessage
		if reason == "" {
			reason = "Alibaba Wan task failed"
		}
		return reverseFailedAliWanTask(&claimed.Task, &claimed.Operation, reason, data)
	}
	status, progress, operationState := model.TaskStatusSubmitted, "10%", model.TaskOperationSubmitted
	nextAttemptAt, completedAt := now+aliWanOperationRetrySeconds, int64(0)
	switch provider.Status {
	case aliWan.StatusSubmitted:
	case aliWan.StatusProcessing:
		status, progress = model.TaskStatusRunning, "50%"
	case aliWan.StatusSucceeded:
		if provider.ResultURL == "" {
			return errors.New("successful AliWan task has no result URL")
		}
		status, progress, operationState = model.TaskStatusSuccess, "100%", model.TaskOperationTerminal
		nextAttemptAt, completedAt = 0, now
	default:
		return errors.New("unknown AliWan task status")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", claimed.Task.ID,
			claimed.Task.TaskID, aliWanTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if isAliWanTerminalStatus(current.Status) {
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
				aliWanTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner).
			Updates(map[string]any{"state": operationState, "next_attempt_at": nextAttemptAt,
				"completed_at": completedAt, "updated_at": now, "last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func refundPreparedAliWanTaskWithLease(task *model.Task, reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation, reason string) error {
	reason = boundedAliWanFailReason(reason)
	return reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
			aliWanTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if !aliWanTaskOperationIdentityMatches(&current, operation) || current.Status != model.TaskStatusNotStart {
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

func markSuccessfulAliWanOperation(operation *model.TaskOperation, now int64) error {
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, aliWanTaskPlatform, model.TaskOperationSubmitted, operation.LeaseOwner).
		Updates(map[string]any{"state": model.TaskOperationTerminal, "settlement_pending": false,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now, "last_error": "",
			"lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func releaseAliWanTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * aliWanOperationRetrySeconds
	if backoff > 300 {
		backoff = 300
	}
	message := "AliWan recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "AliWan recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "AliWan provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, aliWanTaskPlatform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{"next_attempt_at": now + backoff, "updated_at": now,
			"last_error": message, "lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func markAliWanOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid AliWan manual-review transition")
	}
	reason = boundedAliWanRecoveryError(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
			operation.ID, aliWanTaskPlatform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{"state": model.TaskOperationManualReview, "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": reason, "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		taskResult := tx.Model(&model.Task{}).
			Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND status NOT IN ?",
				operation.TaskID, aliWanTaskPlatform, operation.UserID, operation.ChannelID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func quarantineExpiredAliWanOperations(ctx context.Context, now int64) error {
	cutoff := now - int64(aliWanOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			aliWanTaskPlatform, []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			aliWanOperationMaxAttempts, cutoff, now).Order("id asc").Limit(aliWanOperationBatchSize).Find(&operations).Error; err != nil {
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
		owner := "aliWan-review-" + id
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				op.ID, aliWanTaskPlatform, op.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + aliWanOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				all = append(all, claim.Error)
			}
			continue
		}
		op.LeaseOwner = owner
		_, ready, processErr := prepareClaimedAliWanTask(ctx, op, now)
		if processErr != nil {
			all = append(all, processErr)
		} else if ready {
			all = append(all, markAliWanOperationManualReview(op, aliWanPollingManualReviewReason, now))
		}
	}
	return errors.Join(all...)
}

func boundedAliWanRecoveryError(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		return "AliWan recovery failed"
	}
	if len(message) <= aliWanRecoveryErrorMaxBytes {
		return message
	}
	cut := aliWanRecoveryErrorMaxBytes - len("... [truncated]")
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "... [truncated]"
}
