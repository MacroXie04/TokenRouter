package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/suno"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	sunoOperationLeaseSeconds     = int64(90)
	sunoOperationBatchSize        = 100
	sunoOperationMaxAttempts      = 20_000
	sunoOperationMaxPollDays      = 7
	sunoPollingManualReviewReason = "provider task exceeded automatic polling horizon"
	sunoRecoveryErrorMaxBytes     = 255
)

type claimedSunoTask struct {
	Task       model.Task
	Operation  model.TaskOperation
	Private    sunoTaskPrivateData
	ProviderID string
	BaseURL    string
	APIKey     string
}

type sunoPollGroup struct {
	BaseURL string
	APIKey  string
	Tasks   []*claimedSunoTask
}

func reconcileAsyncSunoTasks(ctx context.Context) error {
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
	if err := quarantineExpiredSunoOperations(ctx, now); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("quarantine expired Suno operations: %w", err))
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			sunoTaskPlatform,
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, sunoOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(sunoOperationBatchSize).
		Find(&candidates).Error; err != nil {
		return errors.Join(append(reconcileErrors, err)...)
	}

	claimedForPolling := make([]*claimedSunoTask, 0, len(candidates))
	for index := range candidates {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		operation, ok, err := claimSunoTaskOperation(ctx, &candidates[index], now)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if !ok {
			continue
		}
		claimed, ready, processErr := prepareClaimedSunoTask(ctx, operation, now)
		if processErr != nil {
			if releaseErr := releaseSunoTaskOperation(operation, now, processErr); releaseErr != nil {
				processErr = errors.Join(processErr, releaseErr)
			}
			reconcileErrors = append(reconcileErrors, processErr)
			continue
		}
		if ready {
			claimedForPolling = append(claimedForPolling, claimed)
		}
	}

	groups := groupClaimedSunoTasks(claimedForPolling)
	for _, group := range groups {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		renewed, renewedAt, renewErr := renewSunoPollGroupLeases(ctx, group)
		if renewErr != nil {
			reconcileErrors = append(reconcileErrors, renewErr)
		}
		if renewed == nil || len(renewed.Tasks) == 0 {
			continue
		}
		if err := pollClaimedSunoGroup(ctx, renewed, renewedAt); err != nil {
			reconcileErrors = append(reconcileErrors, err)
		}
	}
	return errors.Join(reconcileErrors...)
}

func claimSunoTaskOperation(
	ctx context.Context,
	candidate *model.TaskOperation,
	now int64,
) (*model.TaskOperation, bool, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.TaskID == "" || candidate.Platform != sunoTaskPlatform {
		return nil, false, errors.New("invalid Suno operation candidate")
	}
	ownerID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "suno-task-" + ownerID
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, sunoTaskPlatform, candidate.State, now, sunoOperationMaxAttempts, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + sunoOperationLeaseSeconds,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, false, nil
	}
	var claimed model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("id = ? AND lease_owner = ?", candidate.ID, owner).
		First(&claimed).Error; err != nil {
		return nil, false, err
	}
	return &claimed, true, nil
}

func prepareClaimedSunoTask(
	ctx context.Context,
	operation *model.TaskOperation,
	now int64,
) (*claimedSunoTask, bool, error) {
	if operation == nil || operation.LeaseOwner == "" || operation.Platform != sunoTaskPlatform {
		return nil, false, errors.New("Suno operation lease is missing")
	}
	var task model.Task
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ?", operation.TaskID,
			sunoTaskPlatform, operation.UserID, operation.ChannelID).First(&task).Error; err != nil {
		return nil, false, markSunoOperationManualReview(operation, "Suno task row is unavailable", now)
	}
	if !sunoTaskOperationIdentityMatches(&task, operation) {
		return nil, false, markSunoOperationManualReview(operation, "Suno task identity mismatch", now)
	}
	reservation, err := service.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return nil, false, err
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return nil, false, refundPreparedSunoTaskWithLease(&task, reservation, operation, "submission stopped before provider dispatch")
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID == "" {
			return nil, false, settleUnknownSunoDispatch(&task, reservation, sunoUnknownDispatchReason, operation.LeaseOwner)
		}
		if err := settlePendingSunoOperation(&task, reservation, operation); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	case model.TaskOperationSubmitted:
		if operation.SettlementPending {
			if err := settlePendingSunoOperation(&task, reservation, operation); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		}
		if task.Status == model.TaskStatusFailure {
			return nil, false, reverseFailedSunoTask(&task, operation, task.FailReason, task.Data)
		}
		if task.Status == model.TaskStatusSuccess {
			return nil, false, markSuccessfulSunoOperation(operation, now)
		}
		if task.Status == model.TaskStatusUnknown {
			return nil, false, markSunoOperationManualReview(operation, "Suno task state is unknown", now)
		}
		return buildClaimedSunoTask(&task, operation, now)
	default:
		return nil, false, markSunoOperationManualReview(operation, "Suno operation state is invalid", now)
	}
}

func settlePendingSunoOperation(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation,
) error {
	if operation.EncryptedProviderTaskID == "" {
		return errors.New("accepted Suno operation is missing its provider id")
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		sunoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return markSunoOperationManualReview(operation, "Suno provider id cannot be decrypted", common.NowTimestamp())
	}
	return settleAcceptedSunoTask(task, reservation, providerID, operation.State, operation.LeaseOwner)
}

func buildClaimedSunoTask(
	task *model.Task,
	operation *model.TaskOperation,
	now int64,
) (*claimedSunoTask, bool, error) {
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	if err != nil {
		return nil, false, markSunoOperationManualReview(operation, "Suno recovery metadata is invalid", now)
	}
	if privateData.EncryptedProviderTaskID != "" &&
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return nil, false, markSunoOperationManualReview(operation, "Suno provider task ciphertext mismatch", now)
	}
	providerID, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		sunoProviderTaskBinding(operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
	if err != nil {
		return nil, false, markSunoOperationManualReview(operation, "Suno provider id cannot be decrypted", now)
	}
	apiKey, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		sunoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil {
		return nil, false, markSunoOperationManualReview(operation, "Suno channel key cannot be decrypted", now)
	}
	baseURL, err := suno.ValidateBaseURL(privateData.ChannelBaseURL)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return nil, false, markSunoOperationManualReview(operation, "Suno channel snapshot is invalid", now)
	}
	return &claimedSunoTask{
		Task: *task, Operation: *operation, Private: privateData,
		ProviderID: providerID, BaseURL: baseURL, APIKey: apiKey,
	}, true, nil
}

func groupClaimedSunoTasks(tasks []*claimedSunoTask) []*sunoPollGroup {
	type groupKey struct {
		baseURL string
		apiKey  string
	}
	groups := make(map[groupKey]*sunoPollGroup)
	order := make([]groupKey, 0)
	for _, task := range tasks {
		key := groupKey{baseURL: task.BaseURL, apiKey: task.APIKey}
		group := groups[key]
		if group == nil {
			group = &sunoPollGroup{BaseURL: task.BaseURL, APIKey: task.APIKey}
			groups[key] = group
			order = append(order, key)
		}
		group.Tasks = append(group.Tasks, task)
	}
	result := make([]*sunoPollGroup, 0, len(order))
	for _, key := range order {
		result = append(result, groups[key])
	}
	return result
}

// renewSunoPollGroupLeases refreshes only still-owned, unexpired leases right
// before network I/O. Reconciliation may contain many credential groups; a
// lease claimed at the beginning of the pass must not be reused after earlier
// groups consume its lifetime.
func renewSunoPollGroupLeases(
	ctx context.Context,
	group *sunoPollGroup,
) (*sunoPollGroup, int64, error) {
	if group == nil || len(group.Tasks) == 0 || len(group.Tasks) > suno.MaxBatchTasks {
		return nil, 0, errors.New("invalid Suno polling group")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return nil, 0, err
	}
	renewed := &sunoPollGroup{BaseURL: group.BaseURL, APIKey: group.APIKey, Tasks: make([]*claimedSunoTask, 0, len(group.Tasks))}
	var renewErrors []error
	for _, task := range group.Tasks {
		if task == nil || task.Operation.LeaseOwner == "" {
			continue
		}
		targetExpiry := now + sunoOperationLeaseSeconds
		result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ? AND lease_expires_at > ?",
				task.Operation.ID, sunoTaskPlatform, model.TaskOperationSubmitted,
				task.Operation.LeaseOwner, now).
			Updates(map[string]any{
				"lease_expires_at": gorm.Expr(
					"CASE WHEN lease_expires_at >= ? THEN lease_expires_at + 1 ELSE ? END",
					targetExpiry, targetExpiry,
				),
				"updated_at": now,
			})
		if result.Error != nil {
			renewErrors = append(renewErrors, result.Error)
			continue
		}
		if result.RowsAffected != 1 {
			continue
		}
		task.Operation.LeaseExpiresAt = targetExpiry
		renewed.Tasks = append(renewed.Tasks, task)
	}
	return renewed, now, errors.Join(renewErrors...)
}

func pollClaimedSunoGroup(ctx context.Context, group *sunoPollGroup, now int64) error {
	if group == nil || len(group.Tasks) == 0 || len(group.Tasks) > suno.MaxBatchTasks {
		return errors.New("invalid Suno polling group")
	}
	ids := make([]string, 0, len(group.Tasks))
	byProviderID := make(map[string]*claimedSunoTask, len(group.Tasks))
	for _, task := range group.Tasks {
		ids = append(ids, task.ProviderID)
		byProviderID[task.ProviderID] = task
	}
	results, err := newSunoProvider().Fetch(ctx, group.BaseURL, group.APIKey, ids)
	if err != nil {
		var releaseErrors []error
		for _, task := range group.Tasks {
			if releaseErr := releaseSunoTaskOperation(&task.Operation, now, err); releaseErr != nil {
				releaseErrors = append(releaseErrors, releaseErr)
			}
		}
		return errors.Join(append(releaseErrors, err)...)
	}
	seen := make(map[string]struct{}, len(results))
	var resultErrors []error
	for index := range results {
		result := &results[index]
		claimed := byProviderID[result.ProviderTaskID]
		if claimed == nil {
			resultErrors = append(resultErrors, errors.New("Suno provider returned an unrequested task"))
			continue
		}
		seen[result.ProviderTaskID] = struct{}{}
		if result.Action != "" && strings.ToUpper(result.Action) != claimed.Task.Action {
			resultErrors = append(resultErrors, releaseSunoTaskOperation(&claimed.Operation, now,
				errors.New("Suno provider returned a conflicting task action")))
			continue
		}
		if err := persistSunoPollResult(claimed, result, now); err != nil {
			if releaseErr := releaseSunoTaskOperation(&claimed.Operation, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			resultErrors = append(resultErrors, err)
		}
	}
	for _, claimed := range group.Tasks {
		if _, found := seen[claimed.ProviderID]; found {
			continue
		}
		if err := releaseSunoTaskOperation(&claimed.Operation, now, errors.New("Suno provider omitted a requested task")); err != nil {
			resultErrors = append(resultErrors, err)
		}
	}
	return errors.Join(resultErrors...)
}

func persistSunoPollResult(claimed *claimedSunoTask, result *suno.TaskResult, now int64) error {
	if claimed == nil || result == nil || claimed.Operation.LeaseOwner == "" {
		return errors.New("invalid Suno poll transition")
	}
	data, err := normalizeSunoTaskData(string(result.Data))
	if err != nil {
		return errors.New("Suno provider task data is invalid or too large")
	}
	if result.Status == suno.StatusFailed {
		reason := result.FailReason
		if reason == "" {
			reason = "Suno task failed"
		}
		return reverseFailedSunoTask(&claimed.Task, &claimed.Operation, reason, data)
	}
	status, progress, operationState := model.TaskStatusSubmitted, "0%", model.TaskOperationSubmitted
	nextAttemptAt, completedAt := now+sunoOperationRetrySeconds, int64(0)
	switch result.Status {
	case suno.StatusSubmitted:
		status = model.TaskStatusSubmitted
	case suno.StatusQueueing:
		status = model.TaskStatusQueued
	case suno.StatusProcessing:
		status, progress = model.TaskStatusRunning, "50%"
	case suno.StatusSuccess:
		status, progress, operationState = model.TaskStatusSuccess, "100%", model.TaskOperationTerminal
		nextAttemptAt, completedAt = 0, now
	default:
		return errors.New("unknown Suno task status")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var current model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", claimed.Task.ID,
			claimed.Task.TaskID, sunoTaskPlatform).First(&current).Error; err != nil {
			return err
		}
		if isSunoTerminalStatus(current.Status) {
			return service.ErrRelayQuotaReservationBusy
		}
		updates := map[string]any{
			"status": status, "progress": progress, "fail_reason": "", "data": data, "updated_at": now,
		}
		if result.SubmitTime > 0 {
			updates["submit_time"] = result.SubmitTime
		}
		if result.StartTime > 0 {
			updates["start_time"] = result.StartTime
		} else if status == model.TaskStatusRunning && current.StartTime == 0 {
			updates["start_time"] = now
		}
		if result.FinishTime > 0 {
			updates["finish_time"] = result.FinishTime
		} else if status == model.TaskStatusSuccess {
			updates["finish_time"] = now
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND status IN ?", current.ID,
				[]string{model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning}).
			Updates(updates)
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, service.ErrRelayQuotaReservationBusy)
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", claimed.Operation.ID,
				sunoTaskPlatform, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner).
			Updates(map[string]any{
				"state": operationState, "next_attempt_at": nextAttemptAt, "completed_at": completedAt,
				"updated_at": now, "last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if opResult.Error != nil || opResult.RowsAffected != 1 {
			return errors.Join(opResult.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func refundPreparedSunoTaskWithLease(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	operation *model.TaskOperation,
	reason string,
) error {
	reason = boundedSunoFailReason(reason)
	return reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
			sunoTaskPlatform).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND reservation_id = ? AND platform = ?", operation.ID,
			operation.ReservationID, sunoTaskPlatform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !sunoTaskOperationIdentityMatches(&currentTask, &currentOperation) ||
			currentOperation.State != model.TaskOperationPrepared ||
			currentOperation.LeaseOwner != operation.LeaseOwner || currentTask.Status != model.TaskStatusNotStart {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status = ?", currentTask.ID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID,
				model.TaskOperationPrepared, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationRefunded, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func markSuccessfulSunoOperation(operation *model.TaskOperation, now int64) error {
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
			sunoTaskPlatform, model.TaskOperationSubmitted, operation.LeaseOwner).
		Updates(map[string]any{
			"state": model.TaskOperationTerminal, "settlement_pending": false,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now,
			"last_error": "", "lease_owner": "", "lease_expires_at": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return service.ErrRelayQuotaReservationBusy
	}
	return nil
}

func releaseSunoTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * sunoOperationRetrySeconds
	if backoff > int64(5*time.Minute/time.Second) {
		backoff = int64(5 * time.Minute / time.Second)
	}
	message := "Suno recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "Suno recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "Suno provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
			sunoTaskPlatform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{
			"next_attempt_at": now + backoff, "updated_at": now,
			"last_error": message, "lease_owner": "", "lease_expires_at": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return service.ErrRelayQuotaReservationBusy
	}
	return nil
}

func markSunoOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid Suno manual-review transition")
	}
	reason = boundedSunoRecoveryError(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
				sunoTaskPlatform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": reason,
				"lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return tx.Model(&model.Task{}).
			Where("task_id = ? AND platform = ? AND user_id = ? AND status NOT IN ?", operation.TaskID,
				sunoTaskPlatform, operation.UserID, []string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now, "updated_at": now,
			}).Error
	})
}

func quarantineExpiredSunoOperations(ctx context.Context, now int64) error {
	cutoff := now - int64(sunoOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			sunoTaskPlatform,
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			sunoOperationMaxAttempts, cutoff, now).
		Order("id asc").Limit(sunoOperationBatchSize).Find(&operations).Error; err != nil {
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
		owner := "suno-review-" + ownerID
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				operation.ID, sunoTaskPlatform, operation.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + sunoOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				quarantineErrors = append(quarantineErrors, claim.Error)
			}
			continue
		}
		operation.LeaseOwner = owner
		_, ready, processErr := prepareClaimedSunoTask(ctx, operation, now)
		if processErr != nil {
			quarantineErrors = append(quarantineErrors, processErr)
			continue
		}
		if ready {
			if err := markSunoOperationManualReview(operation, sunoPollingManualReviewReason, now); err != nil {
				quarantineErrors = append(quarantineErrors, err)
			}
		}
	}
	return errors.Join(quarantineErrors...)
}

func boundedSunoRecoveryError(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "�")
	if message == "" {
		return "Suno recovery failed"
	}
	if len(message) <= sunoRecoveryErrorMaxBytes {
		return message
	}
	cut := sunoRecoveryErrorMaxBytes - len("... [truncated]")
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "... [truncated]"
}
