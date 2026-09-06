package tasks

import (
	"context"
	"errors"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	geminiVeoOperationLeaseSeconds     = int64(90)
	geminiVeoOperationBatchSize        = 100
	geminiVeoOperationMaxAttempts      = 20_000
	geminiVeoOperationMaxPollDays      = 7
	geminiVeoPollingManualReviewReason = "provider task exceeded automatic polling horizon"
	geminiVeoRecoveryErrorMaxBytes     = 255
)

type claimedGeminiVeoTask struct {
	Task          model.Task
	Operation     model.TaskOperation
	Private       geminiVeoTaskPrivateData
	Descriptor    VeoTaskProviderDescriptor
	ProviderID    string
	Routing       string
	Credential    string
	UpstreamModel string
}

func reconcileAsyncGeminiVeoTasks(ctx context.Context) error {
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
	if err := quarantineExpiredGeminiVeoOperations(ctx, now); err != nil {
		all = append(all, err)
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform IN ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			model.VeoTaskOperationPlatforms(), []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, geminiVeoOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(geminiVeoOperationBatchSize).Find(&candidates).Error; err != nil {
		return errors.Join(append(all, err)...)
	}
	for i := range candidates {
		if ctx.Err() != nil {
			all = append(all, ctx.Err())
			break
		}
		op, ok, err := claimGeminiVeoTaskOperation(ctx, &candidates[i], now)
		if err != nil {
			all = append(all, err)
			continue
		}
		if !ok {
			continue
		}
		claimed, ready, err := prepareClaimedGeminiVeoTask(ctx, op, now)
		if err != nil {
			if releaseErr := releaseGeminiVeoTaskOperation(op, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if !ready {
			continue
		}
		pollAt, err := renewGeminiVeoTaskLease(ctx, claimed)
		if err != nil {
			all = append(all, err)
			continue
		}
		provider, _, err := claimed.Descriptor.Fetch(ctx, claimed.Routing, claimed.Credential,
			claimed.UpstreamModel, claimed.ProviderID)
		if err != nil {
			if releaseErr := releaseGeminiVeoTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
			continue
		}
		if err := persistGeminiVeoPollResult(claimed, provider, pollAt); err != nil {
			if releaseErr := releaseGeminiVeoTaskOperation(&claimed.Operation, pollAt, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}

func claimGeminiVeoTaskOperation(ctx context.Context, candidate *model.TaskOperation, now int64) (*model.TaskOperation, bool, error) {
	if candidate == nil || candidate.ID <= 0 || !model.IsVeoTaskOperationPlatform(candidate.Platform) {
		return nil, false, errors.New("invalid GeminiVeo operation candidate")
	}
	id, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "veo-task-" + id
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, candidate.Platform, candidate.State, now, geminiVeoOperationMaxAttempts, now).
		Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + geminiVeoOperationLeaseSeconds,
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

func prepareClaimedGeminiVeoTask(ctx context.Context, operation *model.TaskOperation, now int64) (*claimedGeminiVeoTask, bool, error) {
	if operation == nil || operation.LeaseOwner == "" || !model.IsVeoTaskOperationPlatform(operation.Platform) {
		return nil, false, errors.New("GeminiVeo operation lease is missing")
	}
	var task model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
		operation.TaskID, operation.Platform, operation.UserID, operation.ChannelID).First(&task).Error; err != nil {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo task row is unavailable", now)
	}
	if !geminiVeoTaskOperationIdentityMatches(&task, operation) {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo task identity mismatch", now)
	}
	reservation, err := billingsvc.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return nil, false, err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return nil, false, refundPreparedGeminiVeoTaskWithLease(&task, reservation, operation, "submission stopped before provider dispatch")
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID == "" {
			return nil, false, markGeminiVeoAmbiguousDispatchWithLease(&task, operation.ReservationID,
				geminiVeoUnknownDispatchReason, operation.LeaseOwner)
		}
		return nil, false, settlePendingGeminiVeoOperation(&task, reservation, operation)
	case model.TaskOperationSubmitted:
		if operation.SettlementPending {
			return nil, false, settlePendingGeminiVeoOperation(&task, reservation, operation)
		}
		if task.Status == model.TaskStatusFailure {
			return nil, false, reverseFailedGeminiVeoTask(&task, operation, task.FailReason, task.Data)
		}
		if task.Status == model.TaskStatusSuccess {
			if err := validateDurableGeminiVeoSuccess(&task); err != nil {
				return nil, false, markGeminiVeoOperationManualReview(operation,
					"Veo terminal output is not durably recoverable", now)
			}
			return nil, false, markSuccessfulGeminiVeoOperation(operation, now)
		}
		if task.Status == model.TaskStatusUnknown {
			return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo task state is unknown", now)
		}
		return buildClaimedGeminiVeoTask(&task, operation, now)
	default:
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo operation state is invalid", now)
	}
}

func validateDurableGeminiVeoSuccess(task *model.Task) error {
	data, err := decodeGeminiVeoStoredTaskData(task.Data)
	if err != nil || data.State != geminiVeo.StatusSucceeded {
		return errors.New("invalid Veo terminal output")
	}
	if !data.HasInlineVideo {
		if data.ResultURL == "" {
			return errors.New("Veo terminal output is missing")
		}
		return nil
	}
	response, err := openGeminiVeoInlineArtifact(task, data)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	read, err := io.Copy(io.Discard, response.Body)
	if err != nil || read != data.InlineBytes {
		return errors.New("Veo inline output is not recoverable")
	}
	return nil
}

func settlePendingGeminiVeoOperation(task *model.Task, reservation *billingsvc.RelayQuotaReservation, operation *model.TaskOperation) error {
	if operation.EncryptedProviderTaskID == "" {
		return errors.New("accepted GeminiVeo operation is missing its provider id")
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		geminiVeoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.Platform,
			operation.UserID, operation.ChannelID))
	if err != nil {
		return markGeminiVeoOperationManualReview(operation, "GeminiVeo provider id cannot be decrypted", wallclock.NowTimestamp())
	}
	return settleAcceptedGeminiVeoTask(task, reservation, providerID, operation.State, operation.LeaseOwner)
}

func buildClaimedGeminiVeoTask(task *model.Task, operation *model.TaskOperation, now int64) (*claimedGeminiVeoTask, bool, error) {
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo recovery metadata is invalid", now)
	}
	properties, err := validateGeminiVeoTaskPricingSnapshot(task, privateData, task.Quota)
	if err != nil {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo pricing snapshot is inconsistent", now)
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ? AND user_id = ? AND channel_id = ?", operation.ReservationID,
		operation.UserID, operation.ChannelID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != task.Quota || record.CompletedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo settled accounting snapshot is inconsistent", now)
	}
	descriptor, ok := veoTaskProviderByPlatform(operation.Platform)
	if !ok || descriptor.Family != properties.Family ||
		descriptor.ValidateRoutingSnapshot(privateData.RoutingSnapshot, properties.UpstreamModelName) != nil {
		return nil, false, markGeminiVeoOperationManualReview(operation, "Veo provider routing snapshot is invalid", now)
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		geminiVeoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.Platform,
			operation.UserID, operation.ChannelID))
	if err != nil {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo provider id cannot be decrypted", now)
	}
	if descriptor.ValidateProviderTaskID(privateData.RoutingSnapshot, properties.UpstreamModelName, providerID) != nil {
		return nil, false, markGeminiVeoOperationManualReview(operation, "Veo provider id does not match its frozen route", now)
	}
	credential, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		geminiVeoChannelCredentialBinding(task.TaskID, task.Platform, task.UserId, task.ChannelId,
			privateData.RoutingSnapshot))
	if err != nil {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo channel key cannot be decrypted", now)
	}
	if credential == "" || len(credential) > descriptor.MaxCredentialBytes {
		return nil, false, markGeminiVeoOperationManualReview(operation, "GeminiVeo channel snapshot is invalid", now)
	}
	return &claimedGeminiVeoTask{Task: *task, Operation: *operation, Private: privateData,
		Descriptor: descriptor, ProviderID: providerID, Routing: privateData.RoutingSnapshot,
		Credential: credential, UpstreamModel: properties.UpstreamModelName}, true, nil
}

func renewGeminiVeoTaskLease(ctx context.Context, claimed *claimedGeminiVeoTask) (int64, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ? AND lease_expires_at > ?",
			claimed.Operation.ID, claimed.Operation.Platform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner, now).
		Updates(map[string]any{"lease_expires_at": now + geminiVeoOperationLeaseSeconds, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return 0, errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return now, nil
}

func persistGeminiVeoPollResult(claimed *claimedGeminiVeoTask, provider *VeoProviderTask, now int64) error {
	if claimed == nil || provider == nil || claimed.Operation.LeaseOwner == "" || provider.ProviderTaskID != claimed.ProviderID {
		return errors.New("invalid GeminiVeo poll transition")
	}
	normalized, err := durableGeminiVeoTaskData(&claimed.Task, provider)
	if err != nil {
		return err
	}
	data, err := marshalGeminiVeoStoredTaskData(normalized)
	if err != nil {
		return err
	}
	if provider.Status == geminiVeo.StatusFailed {
		reason := "GeminiVeo task failed"
		return reverseFailedGeminiVeoTask(&claimed.Task, &claimed.Operation, reason, data)
	}
	status, progress, operationState := model.TaskStatusRunning, "50%", model.TaskOperationSubmitted
	nextAttemptAt, completedAt := now+geminiVeoOperationRetrySeconds, int64(0)
	switch provider.Status {
	case geminiVeo.StatusProcessing:
	case geminiVeo.StatusSucceeded:
		status, progress, operationState = model.TaskStatusSuccess, "100%", model.TaskOperationTerminal
		nextAttemptAt, completedAt = 0, now
	default:
		return errors.New("unknown GeminiVeo task status")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", claimed.Task.ID,
			claimed.Task.TaskID, claimed.Task.Platform).First(&current).Error; err != nil {
			return err
		}
		if isGeminiVeoTerminalStatus(current.Status) {
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
				claimed.Operation.Platform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner).
			Updates(map[string]any{"state": operationState, "next_attempt_at": nextAttemptAt,
				"completed_at": completedAt, "updated_at": now, "last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func refundPreparedGeminiVeoTaskWithLease(task *model.Task, reservation *billingsvc.RelayQuotaReservation,
	operation *model.TaskOperation, reason string) error {
	reason = boundedGeminiVeoFailReason(reason)
	return reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
			task.Platform).First(&current).Error; err != nil {
			return err
		}
		if !geminiVeoTaskOperationIdentityMatches(&current, operation) || current.Status != model.TaskStatusNotStart {
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

func markSuccessfulGeminiVeoOperation(operation *model.TaskOperation, now int64) error {
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, operation.Platform, model.TaskOperationSubmitted, operation.LeaseOwner).
		Updates(map[string]any{"state": model.TaskOperationTerminal, "settlement_pending": false,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now, "last_error": "",
			"lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func releaseGeminiVeoTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * geminiVeoOperationRetrySeconds
	if backoff > 300 {
		backoff = 300
	}
	message := "GeminiVeo recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "GeminiVeo recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "GeminiVeo provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
		operation.ID, operation.Platform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{"next_attempt_at": now + backoff, "updated_at": now,
			"last_error": message, "lease_owner": "", "lease_expires_at": 0})
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func markGeminiVeoOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid GeminiVeo manual-review transition")
	}
	reason = boundedGeminiVeoRecoveryError(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?",
			operation.ID, operation.Platform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{"state": model.TaskOperationManualReview, "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": reason, "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		taskResult := tx.Model(&model.Task{}).
			Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND status NOT IN ?",
				operation.TaskID, operation.Platform, operation.UserID, operation.ChannelID,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func quarantineExpiredGeminiVeoOperations(ctx context.Context, now int64) error {
	cutoff := now - int64(geminiVeoOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform IN ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			model.VeoTaskOperationPlatforms(), []string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			geminiVeoOperationMaxAttempts, cutoff, now).Order("id asc").Limit(geminiVeoOperationBatchSize).Find(&operations).Error; err != nil {
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
		owner := "geminiVeo-review-" + id
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				op.ID, op.Platform, op.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + geminiVeoOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				all = append(all, claim.Error)
			}
			continue
		}
		op.LeaseOwner = owner
		_, ready, processErr := prepareClaimedGeminiVeoTask(ctx, op, now)
		if processErr != nil {
			all = append(all, processErr)
		} else if ready {
			all = append(all, markGeminiVeoOperationManualReview(op, geminiVeoPollingManualReviewReason, now))
		}
	}
	return errors.Join(all...)
}

func boundedGeminiVeoRecoveryError(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		return "GeminiVeo recovery failed"
	}
	if len(message) <= geminiVeoRecoveryErrorMaxBytes {
		return message
	}
	cut := geminiVeoRecoveryErrorMaxBytes - len("... [truncated]")
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "... [truncated]"
}
