package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/midjourney"
	"github.com/tokenrouter/tokenrouter/service"
)

type claimedMidjourneyTask struct {
	Task        model.Task
	Mirror      model.Midjourney
	Operation   model.TaskOperation
	Properties  midjourneyTaskProperties
	Private     midjourneyTaskPrivateData
	Reservation *service.RelayQuotaReservation
	ProviderID  string
	APIKey      string
}

type midjourneyPollGroup struct {
	BaseURL string
	APIKey  string
	Tasks   []*claimedMidjourneyTask
}

func reconcileAsyncMidjourneyTasks(ctx context.Context) error {
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
	if err := quarantineExpiredMidjourneyOperations(ctx, now); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("quarantine expired Midjourney operations: %w", err))
	}
	var candidates []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			midjourneyTaskPlatform,
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			now, midjourneyOperationMaxAttempts, now).
		Order("next_attempt_at asc, id asc").Limit(midjourneyOperationBatchSize).
		Find(&candidates).Error; err != nil {
		return errors.Join(append(reconcileErrors, err)...)
	}
	ready := make([]*claimedMidjourneyTask, 0, len(candidates))
	for index := range candidates {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		operation, ok, err := claimMidjourneyTaskOperation(ctx, &candidates[index], now)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if !ok {
			continue
		}
		claimed, poll, err := prepareClaimedMidjourneyTask(ctx, operation, now)
		if err != nil {
			if releaseErr := releaseMidjourneyTaskOperation(operation, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if poll {
			ready = append(ready, claimed)
		}
	}
	for _, group := range groupClaimedMidjourneyTasks(ready) {
		if ctx.Err() != nil {
			reconcileErrors = append(reconcileErrors, ctx.Err())
			break
		}
		if err := pollClaimedMidjourneyGroup(ctx, group, now); err != nil {
			reconcileErrors = append(reconcileErrors, err)
		}
	}
	return errors.Join(reconcileErrors...)
}

func claimMidjourneyTaskOperation(ctx context.Context, candidate *model.TaskOperation,
	now int64) (*model.TaskOperation, bool, error) {
	if candidate == nil || candidate.ID <= 0 || candidate.TaskID == "" || candidate.Platform != midjourneyTaskPlatform {
		return nil, false, errors.New("invalid Midjourney operation candidate")
	}
	ownerID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	owner := "midjourney-task-" + ownerID
	result := model.DB.WithContext(ctx).Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND next_attempt_at <= ? AND attempts >= 0 AND attempts < ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			candidate.ID, midjourneyTaskPlatform, candidate.State, now,
			midjourneyOperationMaxAttempts, now).
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": now + midjourneyOperationLeaseSeconds,
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, false, nil
	}
	var claimed model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("id = ? AND platform = ? AND lease_owner = ?",
		candidate.ID, midjourneyTaskPlatform, owner).First(&claimed).Error; err != nil {
		return nil, false, err
	}
	return &claimed, true, nil
}

func prepareClaimedMidjourneyTask(ctx context.Context, operation *model.TaskOperation,
	now int64) (*claimedMidjourneyTask, bool, error) {
	if operation == nil || operation.Platform != midjourneyTaskPlatform || operation.LeaseOwner == "" {
		return nil, false, errors.New("Midjourney operation lease is missing")
	}
	claimed, err := loadClaimedMidjourneyTask(ctx, operation)
	if err != nil {
		return nil, false, markMidjourneyOperationManualReview(operation,
			"Midjourney task row is unavailable or ambiguous", now)
	}
	switch operation.State {
	case model.TaskOperationPrepared:
		return nil, false, refundMidjourneyTask(&claimed.Task, claimed.Reservation, nil, 4,
			"Midjourney submission stopped before provider dispatch", model.TaskOperationPrepared, operation.LeaseOwner)
	case model.TaskOperationDispatching:
		if operation.EncryptedProviderTaskID == "" && claimed.Private.EncryptedProviderTaskID == "" {
			return nil, false, markMidjourneyOperationManualReview(operation, midjourneyUnknownDispatchReason, now)
		}
		providerID, err := decryptMidjourneyProviderTaskID(&claimed.Task, operation,
			claimed.Private, claimed.Properties.Action)
		if err != nil {
			return nil, false, markMidjourneyOperationManualReview(operation,
				"Midjourney provider id cannot be decrypted", now)
		}
		provider := &midjourney.TaskResult{
			ProviderTaskID: providerID, Action: string(claimed.Properties.Action),
			Status: model.TaskStatusSubmitted, Progress: "0%",
		}
		return nil, false, persistAcceptedMidjourneyTask(&claimed.Task, operation.ReservationID,
			provider, 1, model.TaskOperationDispatching, operation.LeaseOwner)
	case model.TaskOperationSubmitted:
		if terminal, ok, err := terminalMidjourneyProviderState(&claimed.Task, operation, claimed.ProviderID); err != nil {
			return nil, false, markMidjourneyOperationManualReview(operation,
				"Midjourney stored provider state is invalid", now)
		} else if ok {
			return nil, false, finishClaimedMidjourneyTask(claimed, terminal)
		}
		return claimed, true, nil
	default:
		return nil, false, markMidjourneyOperationManualReview(operation,
			"Midjourney operation state is invalid", now)
	}
}

func loadClaimedMidjourneyTask(ctx context.Context, operation *model.TaskOperation) (*claimedMidjourneyTask, error) {
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND platform = ? AND user_id = ? AND channel_id = ?", operation.TaskID,
			midjourneyTaskPlatform, operation.UserID, operation.ChannelID).Limit(2).Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) != 1 {
		return nil, errors.New("Midjourney task is unavailable or ambiguous")
	}
	var mirrors []model.Midjourney
	if err := model.DB.WithContext(ctx).
		Where("mj_id = ? AND user_id = ? AND channel_id = ?", operation.TaskID,
			operation.UserID, operation.ChannelID).Limit(2).Find(&mirrors).Error; err != nil {
		return nil, err
	}
	if len(mirrors) != 1 || !midjourneyTaskOperationIdentityMatches(&tasks[0], &mirrors[0], operation,
		operation.ReservationID) {
		return nil, errors.New("Midjourney task identity is unavailable or ambiguous")
	}
	properties, err := decodeMidjourneyTaskProperties(tasks[0].Properties)
	if err != nil || string(properties.Action) != tasks[0].Action {
		return nil, errors.New("Midjourney task provenance is invalid")
	}
	privateData, err := decodeMidjourneyTaskPrivateData(tasks[0].PrivateData)
	if err != nil {
		return nil, err
	}
	reservation, err := service.RestoreRelayQuotaReservation(operation.ReservationID)
	if err != nil {
		return nil, err
	}
	claimed := &claimedMidjourneyTask{
		Task: tasks[0], Mirror: mirrors[0], Operation: *operation,
		Properties: properties, Private: privateData, Reservation: reservation,
	}
	if operation.State != model.TaskOperationSubmitted {
		return claimed, nil
	}
	providerID, err := decryptMidjourneyProviderTaskID(&claimed.Task, operation, privateData, properties.Action)
	if err != nil {
		return nil, err
	}
	apiKey, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		midjourneyChannelCredentialBinding(claimed.Task.TaskID, claimed.Task.UserId,
			claimed.Task.ChannelId, privateData.ChannelBaseURL))
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Midjourney channel key cannot be decrypted")
	}
	if _, err := midjourney.ValidateBaseURL(privateData.ChannelBaseURL); err != nil {
		return nil, err
	}
	claimed.ProviderID, claimed.APIKey = providerID, apiKey
	return claimed, nil
}

func groupClaimedMidjourneyTasks(tasks []*claimedMidjourneyTask) []*midjourneyPollGroup {
	groups := make(map[[32]byte]*midjourneyPollGroup)
	order := make([][32]byte, 0)
	for _, task := range tasks {
		key := sha256.Sum256([]byte(task.Private.ChannelBaseURL + "\x00" + task.APIKey))
		group := groups[key]
		if group == nil {
			group = &midjourneyPollGroup{BaseURL: task.Private.ChannelBaseURL, APIKey: task.APIKey}
			groups[key] = group
			order = append(order, key)
		}
		if len(group.Tasks) >= midjourney.MaxBatchTasks {
			// Create a deterministic continuation bucket without retaining the key.
			continuation := sha256.Sum256(append(key[:], byte(len(order))))
			group = &midjourneyPollGroup{BaseURL: task.Private.ChannelBaseURL, APIKey: task.APIKey}
			groups[continuation] = group
			order = append(order, continuation)
		}
		group.Tasks = append(group.Tasks, task)
	}
	result := make([]*midjourneyPollGroup, 0, len(order))
	for _, key := range order {
		result = append(result, groups[key])
	}
	return result
}

func pollClaimedMidjourneyGroup(ctx context.Context, group *midjourneyPollGroup, now int64) error {
	if group == nil || len(group.Tasks) == 0 || len(group.Tasks) > midjourney.MaxBatchTasks {
		return errors.New("invalid Midjourney polling group")
	}
	ids := make([]string, 0, len(group.Tasks))
	byProviderID := make(map[string]*claimedMidjourneyTask, len(group.Tasks))
	for _, task := range group.Tasks {
		if _, duplicate := byProviderID[task.ProviderID]; duplicate {
			_ = markMidjourneyOperationManualReview(&task.Operation,
				"Midjourney provider identity is duplicated", now)
			continue
		}
		ids = append(ids, task.ProviderID)
		byProviderID[task.ProviderID] = task
	}
	if len(ids) == 0 {
		return nil
	}
	results, err := newMidjourneyProvider().Fetch(ctx, group.BaseURL, group.APIKey, ids)
	if err != nil {
		var releaseErrors []error
		for _, task := range group.Tasks {
			if releaseErr := releaseMidjourneyTaskOperation(&task.Operation, now, err); releaseErr != nil {
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
			resultErrors = append(resultErrors, errors.New("Midjourney provider returned an unrequested task"))
			continue
		}
		seen[result.ProviderTaskID] = struct{}{}
		if midjourneyTaskResultContainsSecret(result, group.APIKey) {
			resultErrors = append(resultErrors, releaseMidjourneyTaskOperation(&claimed.Operation, now,
				errors.New("Midjourney provider returned unsafe task data")))
			continue
		}
		if result.Action != "" && strings.ToUpper(result.Action) != string(claimed.Properties.Action) {
			resultErrors = append(resultErrors, releaseMidjourneyTaskOperation(&claimed.Operation, now,
				errors.New("Midjourney provider returned a conflicting task action")))
			continue
		}
		if err := finishClaimedMidjourneyTask(claimed, result); err != nil {
			if releaseErr := releaseMidjourneyTaskOperation(&claimed.Operation, now, err); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			}
			resultErrors = append(resultErrors, err)
		}
	}
	for _, claimed := range group.Tasks {
		if _, found := seen[claimed.ProviderID]; found {
			continue
		}
		if err := releaseMidjourneyTaskOperation(&claimed.Operation, now,
			errors.New("Midjourney provider omitted a requested task")); err != nil {
			resultErrors = append(resultErrors, err)
		}
	}
	return errors.Join(resultErrors...)
}

func finishClaimedMidjourneyTask(claimed *claimedMidjourneyTask, result *midjourney.TaskResult) error {
	if claimed == nil || result == nil {
		return errors.New("invalid Midjourney poll result")
	}
	status := normalizeMidjourneyTaskStatus(result.Status)
	var err error
	switch status {
	case model.TaskStatusSuccess:
		err = settleSuccessfulMidjourneyTask(&claimed.Task, claimed.Reservation, result, 1,
			model.TaskOperationSubmitted, claimed.Operation.LeaseOwner)
		if err == nil {
			_ = service.DeliverAuditLogOutboxEvent("mj:" + claimed.Operation.ReservationID)
		}
	case model.TaskStatusFailure:
		reason := result.FailReason
		if reason == "" {
			reason = "Midjourney task failed"
		}
		err = refundMidjourneyTask(&claimed.Task, claimed.Reservation, result, 1,
			reason, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner)
	case model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusNotStart:
		err = persistAcceptedMidjourneyTask(&claimed.Task, claimed.Operation.ReservationID,
			result, 1, model.TaskOperationSubmitted, claimed.Operation.LeaseOwner)
	default:
		err = errors.New("Midjourney provider returned an unknown task status")
	}
	return err
}

func terminalMidjourneyProviderState(task *model.Task, operation *model.TaskOperation,
	providerID string) (*midjourney.TaskResult, bool, error) {
	if task == nil || operation == nil || providerID == "" {
		return nil, false, nil
	}
	status := normalizeMidjourneyTaskStatus(task.Status)
	if status != model.TaskStatusSuccess && status != model.TaskStatusFailure {
		return nil, false, nil
	}
	data, err := decodeMidjourneyStoredTaskData(task.Data)
	if err != nil {
		return nil, false, err
	}
	return &midjourney.TaskResult{
		ProviderTaskID: providerID, Action: task.Action, Status: status, Progress: task.Progress,
		FailReason: task.FailReason, VideoURLs: data.VideoURLs, CustomID: data.CustomID,
		BotType: data.BotType, MaskBase64: data.MaskBase64,
	}, true, nil
}

func decryptMidjourneyProviderTaskID(task *model.Task, operation *model.TaskOperation,
	privateData midjourneyTaskPrivateData, action midjourney.Action) (string, error) {
	binding := midjourneyProviderTaskBinding(task.TaskID, operation.ReservationID,
		task.UserId, task.ChannelId, action)
	providerID := ""
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encrypted == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || strings.TrimSpace(decoded) == "" {
			return "", errors.New("invalid encrypted Midjourney provider identity")
		}
		if providerID != "" && providerID != decoded {
			return "", errors.New("conflicting encrypted Midjourney provider identities")
		}
		providerID = decoded
	}
	if providerID == "" {
		return "", errors.New("Midjourney provider identity is unavailable")
	}
	return providerID, nil
}

func releaseMidjourneyTaskOperation(operation *model.TaskOperation, now int64, operationErr error) error {
	if operation == nil || operation.LeaseOwner == "" {
		return nil
	}
	backoff := int64(operation.Attempts+1) * midjourneyOperationRetrySeconds
	if backoff > int64(5*time.Minute/time.Second) {
		backoff = int64(5 * time.Minute / time.Second)
	}
	message := "Midjourney recovery attempt failed"
	if errors.Is(operationErr, context.Canceled) {
		message = "Midjourney recovery was canceled"
	} else if errors.Is(operationErr, context.DeadlineExceeded) {
		message = "Midjourney provider recovery timed out"
	}
	result := model.DB.Model(&model.TaskOperation{}).
		Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
			midjourneyTaskPlatform, operation.State, operation.LeaseOwner).
		Updates(map[string]any{
			"next_attempt_at": now + backoff, "updated_at": now, "last_error": message,
			"lease_owner": "", "lease_expires_at": int64(0),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var current model.TaskOperation
		if model.DB.Where("id = ?", operation.ID).First(&current).Error == nil &&
			current.LeaseOwner == "" && current.State != operation.State {
			return nil
		}
		return service.ErrRelayQuotaReservationBusy
	}
	return nil
}

func markMidjourneyOperationManualReview(operation *model.TaskOperation, reason string, now int64) error {
	if operation == nil || operation.LeaseOwner == "" {
		return errors.New("invalid Midjourney manual-review transition")
	}
	reason = boundedMidjourneyFailReason(reason)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND lease_owner = ?", operation.ID,
				midjourneyTaskPlatform, operation.State, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "next_attempt_at": int64(0),
				"completed_at": now, "updated_at": now, "last_error": reason,
				"lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		if result := tx.Model(&model.Task{}).
			Where("task_id = ? AND platform = ? AND user_id = ? AND status NOT IN ?", operation.TaskID,
				midjourneyTaskPlatform, operation.UserID, []string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now, "updated_at": now,
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		if result := tx.Model(&model.Midjourney{}).
			Where("mj_id = ? AND user_id = ? AND channel_id = ?", operation.TaskID,
				operation.UserID, operation.ChannelID).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason,
				"progress": "100%", "finish_time": now * 1000,
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func quarantineExpiredMidjourneyOperations(ctx context.Context, now int64) error {
	cutoff := now - int64(midjourneyOperationMaxPollDays*24*60*60)
	var operations []model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("platform = ? AND state IN ? AND (attempts < 0 OR attempts >= ? OR created_at <= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			midjourneyTaskPlatform,
			[]string{model.TaskOperationPrepared, model.TaskOperationDispatching, model.TaskOperationSubmitted},
			midjourneyOperationMaxAttempts, cutoff, now).
		Order("id asc").Limit(midjourneyOperationBatchSize).Find(&operations).Error; err != nil {
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
		owner := "midjourney-review-" + ownerID
		claim := model.DB.Model(&model.TaskOperation{}).
			Where("id = ? AND platform = ? AND state = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
				operation.ID, midjourneyTaskPlatform, operation.State, now).
			Updates(map[string]any{"lease_owner": owner, "lease_expires_at": now + midjourneyOperationLeaseSeconds})
		if claim.Error != nil || claim.RowsAffected != 1 {
			if claim.Error != nil {
				quarantineErrors = append(quarantineErrors, claim.Error)
			}
			continue
		}
		operation.LeaseOwner = owner
		claimed, err := loadClaimedMidjourneyTask(ctx, operation)
		if err != nil {
			quarantineErrors = append(quarantineErrors,
				markMidjourneyOperationManualReview(operation, "Midjourney task state is invalid", now))
			continue
		}
		if operation.State == model.TaskOperationPrepared {
			err = refundMidjourneyTask(&claimed.Task, claimed.Reservation, nil, 4,
				"Midjourney submission stopped before provider dispatch", model.TaskOperationPrepared, owner)
		} else {
			err = markMidjourneyOperationManualReview(operation, midjourneyPollingManualReviewReason, now)
		}
		if err != nil {
			quarantineErrors = append(quarantineErrors, err)
		}
	}
	return errors.Join(quarantineErrors...)
}

func midjourneyTaskResultContainsSecret(result *midjourney.TaskResult, secret string) bool {
	if result == nil || secret == "" {
		return false
	}
	values := []string{
		result.ProviderTaskID, result.Action, result.CustomID, result.BotType, result.Prompt,
		result.PromptEn, result.Description, result.State, result.ImageURL, result.VideoURL,
		result.Status, result.Progress, result.FailReason, result.MaskBase64,
	}
	for _, video := range result.VideoURLs {
		values = append(values, video.URL)
	}
	for _, value := range values {
		if strings.Contains(value, secret) {
			return true
		}
	}
	return bytes.Contains(result.Buttons, []byte(secret)) || bytes.Contains(result.Properties, []byte(secret))
}
