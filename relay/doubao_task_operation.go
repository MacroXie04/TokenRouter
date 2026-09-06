package relay

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/task/doubao"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	doubaoOperationLeaseSeconds    = int64(90)
	doubaoOperationRetrySeconds    = int64(45)
	doubaoDispatchRecoverySeconds  = int64(120)
	doubaoReservationSafetySeconds = int64(8 * 24 * time.Hour / time.Second)
	doubaoOperationBatchSize       = 100
	doubaoOperationMaxAttempts     = 20_000
	doubaoOperationMaxPollDays     = 7

	doubaoUnknownDispatchReason     = "Doubao provider submission outcome requires manual review"
	doubaoPollingManualReviewReason = "Doubao task exceeded automatic polling horizon"
)

func createDoubaoReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *doubaoTaskPrivateData,
) (*service.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 || task.ChannelId <= 0 ||
		!model.IsDoubaoVideoTaskOperationPlatform(task.Platform) {
		return nil, errors.New("invalid Doubao task reservation")
	}
	return service.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, privateData.Pricing.FreeModel,
		func(tx *gorm.DB, creation service.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalDoubaoTaskPrivateData(*privateData)
			if err != nil {
				return err
			}
			task.PrivateData = encoded
			if err := tx.Create(task).Error; err != nil {
				return err
			}
			now, err := model.DatabaseUnixTimestamp(tx)
			if err != nil {
				return err
			}
			minimumExpiry := now + doubaoReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: task.Platform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + doubaoOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markDoubaoTaskDispatching(task *model.Task, reservation *service.RelayQuotaReservation) error {
	if task == nil || reservation == nil || task.ID <= 0 || !isDoubaoTask(task) {
		return errors.New("invalid Doubao dispatch transition")
	}
	var now int64
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		var err error
		now, err = model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + doubaoReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				task.Platform, model.TaskStatusNotStart).
			Update("updated_at", now)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), task.Platform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + doubaoDispatchRecoverySeconds,
				"updated_at":      now, "last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil && !doubaoDispatchTransitionMatches(task.TaskID, reservation.ReservationID(), task.Platform) {
		return err
	}
	task.UpdatedAt = now
	return nil
}

// persistAcceptedDoubaoTask records provider identity before returning an
// accepted non-terminal task. Its reservation deliberately stays dispatched:
// the provider's final units do not exist yet.
func persistAcceptedDoubaoTask(
	task *model.Task,
	reservationID string,
	provider *doubao.Task,
	expectedState, leaseOwner string,
) error {
	if provider == nil || (provider.Status != doubao.StatusSubmitted && provider.Status != doubao.StatusProcessing) {
		return errors.New("invalid non-terminal Doubao provider state")
	}
	return persistDoubaoProviderStatePending(task, reservationID, provider, expectedState, leaseOwner)
}

// persistDoubaoProviderStatePending is also the post-acceptance fallback for a
// terminal response whose accounting transaction could not commit. Terminal
// provider truth is stored, but Task remains non-terminal until settlement or
// refund commits atomically.
func persistDoubaoProviderStatePending(
	task *model.Task,
	reservationID string,
	provider *doubao.Task,
	expectedState, leaseOwner string,
) error {
	if task == nil || provider == nil || strings.TrimSpace(provider.ProviderTaskID) == "" ||
		!validDoubaoStoredStatus(provider.Status) ||
		(expectedState != model.TaskOperationDispatching && expectedState != model.TaskOperationSubmitted &&
			expectedState != model.TaskOperationManualReview) {
		return errors.New("invalid accepted Doubao fallback")
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservationID {
		return errors.New("invalid Doubao recovery metadata")
	}
	encryptedProviderID, err := doubaoProviderCipher(
		task, privateData, nil, provider.ProviderTaskID, properties.Action,
	)
	if err != nil {
		return err
	}
	privateData.EncryptedUpstreamTaskID = encryptedProviderID
	privateData.SettlementPending = true
	privateData.CompletionUnits = provider.CompletionUnits
	privateJSON, err := marshalDoubaoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	stored, err := normalizedDoubaoTaskData(provider)
	if err != nil {
		return err
	}
	dataJSON, err := marshalDoubaoStoredTaskData(stored)
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	visibleStatus := doubaoTaskStatus(provider.Status)
	if provider.Status == doubao.StatusSucceeded || provider.Status == doubao.StatusFailed {
		visibleStatus = model.TaskStatusSubmitted
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !doubaoTaskOperationIdentityMatches(&currentTask, &operation, reservationID) {
			return errors.New("accepted Doubao identity mismatch")
		}
		if operation.State == model.TaskOperationTerminal || operation.State == model.TaskOperationRefunded {
			return nil
		}
		if operation.State != expectedState || operation.LeaseOwner != leaseOwner {
			return service.ErrRelayQuotaReservationBusy
		}
		if err := verifyDoubaoProviderCipherConsistency(&currentTask, &operation, provider.ProviderTaskID, properties.Action); err != nil {
			return err
		}
		taskUpdates := map[string]any{
			"status": visibleStatus, "private_data": privateJSON, "data": dataJSON,
			"progress": doubaoTaskProgress(visibleStatus), "fail_reason": "", "updated_at": now,
		}
		if visibleStatus == model.TaskStatusRunning && currentTask.StartTime == 0 {
			taskUpdates["start_time"] = now
		}
		if currentTask.Status == model.TaskStatusUnknown {
			taskUpdates["finish_time"] = 0
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, currentTask.Platform).
			Updates(taskUpdates)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": true,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            now + doubaoOperationRetrySeconds,
				"completed_at":               int64(0), "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": int64(0),
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
}

func settleSuccessfulDoubaoTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	provider *doubao.Task,
	expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil || provider == nil || provider.Status != doubao.StatusSucceeded ||
		strings.TrimSpace(provider.ProviderTaskID) == "" {
		return errors.New("invalid successful Doubao settlement")
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservation.ReservationID() {
		return errors.New("invalid successful Doubao accounting metadata")
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
		reservation.ReservationID(), task.Platform).First(&operation).Error; err != nil {
		return err
	}
	encryptedProviderID, err := doubaoProviderCipher(
		task, privateData, &operation, provider.ProviderTaskID, properties.Action,
	)
	if err != nil {
		return err
	}
	actualQuota, err := privateData.Pricing.SettlementQuota(provider.CompletionUnits)
	if err != nil {
		return err
	}
	privateData.EncryptedUpstreamTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateData.CompletionUnits = provider.CompletionUnits
	privateJSON, err := marshalDoubaoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	stored, err := normalizedDoubaoTaskData(provider)
	if err != nil {
		return err
	}
	dataJSON, err := marshalDoubaoStoredTaskData(stored)
	if err != nil {
		return err
	}
	var committedAt int64
	err = reservation.SettleWithChannelAndPersistence(actualQuota, task.ChannelId, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), task.Platform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !doubaoTaskOperationIdentityMatches(&currentTask, &currentOperation, reservation.ReservationID()) {
			return errors.New("successful Doubao settlement identity mismatch")
		}
		if err := verifyDoubaoProviderCipherConsistency(&currentTask, &currentOperation,
			provider.ProviderTaskID, properties.Action); err != nil {
			return err
		}
		committedAt, err = model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if currentOperation.State == model.TaskOperationTerminal {
			if currentTask.Status != model.TaskStatusSuccess || currentTask.Quota != actualQuota ||
				currentTask.PrivateData != privateJSON || currentTask.Data != dataJSON ||
				currentOperation.SettlementPending || currentOperation.EncryptedProviderTaskID != encryptedProviderID {
				return service.ErrRelayQuotaReservationBusy
			}
			audit, err := newDoubaoConsumeAudit(currentTask, privateData, reservation.ReservationID(), actualQuota)
			if err != nil {
				return err
			}
			return service.EnqueueAuditLogTx(tx, audit)
		}
		if currentOperation.State != expectedState || currentOperation.LeaseOwner != leaseOwner {
			return service.ErrRelayQuotaReservationBusy
		}
		if currentTask.Status == model.TaskStatusFailure || currentTask.Status == model.TaskStatusSuccess {
			return service.ErrRelayQuotaReservationBusy
		}
		taskUpdates := map[string]any{
			"status": model.TaskStatusSuccess, "quota": actualQuota,
			"private_data": privateJSON, "data": dataJSON, "progress": "100%",
			"fail_reason": "", "finish_time": committedAt, "updated_at": committedAt,
		}
		if currentTask.StartTime == 0 {
			taskUpdates["start_time"] = committedAt
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, currentTask.Platform).
			Updates(taskUpdates)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationTerminal, "settlement_pending": false,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            int64(0), "completed_at": committedAt, "updated_at": committedAt,
				"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		updated := currentTask
		updated.Status, updated.Quota = model.TaskStatusSuccess, actualQuota
		updated.PrivateData, updated.Data = privateJSON, dataJSON
		audit, err := newDoubaoConsumeAudit(updated, privateData, reservation.ReservationID(), actualQuota)
		if err != nil {
			return err
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
	if err != nil && !doubaoSettlementTransitionMatches(task.TaskID, reservation.ReservationID(), task.Platform, actualQuota) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func refundDoubaoTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	provider *doubao.Task,
	reason, expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid Doubao refund")
	}
	reason = boundedDoubaoFailReason(reason)
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservation.ReservationID() {
		return errors.New("invalid Doubao refund metadata")
	}
	stored := doubaoStoredTaskData{Status: doubao.StatusFailed, StatusMessage: reason}
	encryptedProviderID := privateData.EncryptedUpstreamTaskID
	completionUnits := privateData.CompletionUnits
	if provider != nil {
		if provider.Status != doubao.StatusFailed {
			return errors.New("invalid Doubao failure state")
		}
		stored, err = normalizedDoubaoTaskData(provider)
		if err != nil {
			return err
		}
		completionUnits = provider.CompletionUnits
		if strings.TrimSpace(provider.ProviderTaskID) != "" {
			var operation model.TaskOperation
			_ = model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
				reservation.ReservationID(), task.Platform).First(&operation).Error
			encryptedProviderID, err = doubaoProviderCipher(task, privateData, &operation,
				provider.ProviderTaskID, properties.Action)
			if err != nil {
				return err
			}
		}
	}
	dataJSON, err := marshalDoubaoStoredTaskData(stored)
	if err != nil {
		return err
	}
	privateData.EncryptedUpstreamTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateData.CompletionUnits = completionUnits
	privateJSON, err := marshalDoubaoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	err = reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !doubaoTaskOperationIdentityMatches(&currentTask, &operation, reservation.ReservationID()) {
			return errors.New("Doubao refund identity mismatch")
		}
		if operation.State == model.TaskOperationRefunded {
			if currentTask.Status != model.TaskStatusFailure || currentTask.Quota != 0 ||
				operation.SettlementPending || operation.LeaseOwner != "" {
				return service.ErrRelayQuotaReservationBusy
			}
			return nil
		}
		if operation.State != expectedState || operation.LeaseOwner != leaseOwner {
			return service.ErrRelayQuotaReservationBusy
		}
		if encryptedProviderID != "" && provider != nil {
			if err := verifyDoubaoProviderCipherConsistency(&currentTask, &operation,
				provider.ProviderTaskID, properties.Action); err != nil {
				return err
			}
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, currentTask.Platform).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "quota": 0, "private_data": privateJSON,
				"data": dataJSON, "progress": "100%", "fail_reason": reason,
				"finish_time": now, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationRefunded, "settlement_pending": false,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            int64(0), "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil && !doubaoRefundTransitionMatches(task.TaskID, reservation.ReservationID(), task.Platform) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func markDoubaoAmbiguousDispatch(task *model.Task, reservationID, reason string) error {
	if task == nil || reservationID == "" {
		return errors.New("invalid ambiguous Doubao dispatch")
	}
	reason = boundedDoubaoFailReason(reason)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !doubaoTaskOperationIdentityMatches(&currentTask, &operation, reservationID) ||
			operation.State != model.TaskOperationDispatching || operation.LeaseOwner != "" {
			return service.ErrRelayQuotaReservationBusy
		}
		privateData, err := decodeDoubaoTaskPrivateData(currentTask.PrivateData)
		if err != nil || privateData.EncryptedUpstreamTaskID != "" {
			return errors.New("ambiguous Doubao dispatch metadata is invalid")
		}
		privateData.SettlementPending = true
		privateJSON, err := marshalDoubaoTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", currentTask.ID, currentTask.TaskID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "private_data": privateJSON, "data": "null",
				"progress": "100%", "fail_reason": reason, "finish_time": now, "updated_at": now,
			}); result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return result.Error
			}
			return service.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, model.TaskOperationDispatching, "").
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "settlement_pending": true,
				"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
				"last_error": reason, "lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil && doubaoManualReviewTransitionMatches(task.TaskID, reservationID, task.Platform) {
		return nil
	}
	return err
}

func doubaoProviderCipher(
	task *model.Task,
	privateData doubaoTaskPrivateData,
	operation *model.TaskOperation,
	providerTaskID string,
	action doubao.Action,
) (string, error) {
	binding := doubaoProviderTaskBinding(task.TaskID, privateData.RelayReservationID, task.Platform, task.UserId, task.ChannelId, action)
	reusable := ""
	for _, encoded := range []string{privateData.EncryptedUpstreamTaskID, func() string {
		if operation == nil {
			return ""
		}
		return operation.EncryptedProviderTaskID
	}()} {
		if encoded == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || decoded != providerTaskID {
			return "", errors.New("Doubao provider identity conflicts with durable state")
		}
		if reusable == "" {
			reusable = encoded
		}
	}
	if reusable != "" {
		return reusable, nil
	}
	return asyncTaskEncryptBound(providerTaskID, binding)
}

func verifyDoubaoProviderCipherConsistency(
	task *model.Task,
	operation *model.TaskOperation,
	providerTaskID string,
	action doubao.Action,
) error {
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	binding := doubaoProviderTaskBinding(task.TaskID, operation.ReservationID, task.Platform, task.UserId, task.ChannelId, action)
	for _, encoded := range []string{privateData.EncryptedUpstreamTaskID, operation.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || decoded != providerTaskID {
			return errors.New("Doubao provider identity conflicts with durable state")
		}
	}
	return nil
}

func newDoubaoConsumeAudit(
	task model.Task,
	privateData doubaoTaskPrivateData,
	reservationID string,
	actualQuota int,
) (*model.Log, error) {
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || task.TaskID == "" || reservationID == "" {
		return nil, errors.New("invalid Doubao consume audit")
	}
	eventID := doubaoAuditEventID(reservationID)
	if len(eventID) > 64 {
		return nil, errors.New("invalid Doubao consume audit identity")
	}
	other := privateData.Pricing.BillingLogFields()
	for key, value := range map[string]any{
		"billing_source": privateData.BillingSource, "relay_reservation_id": reservationID,
		"task_id": task.TaskID, "task_platform": task.Platform, "action": string(properties.Action),
		"completion_units": privateData.CompletionUnits, "subscription_id": privateData.SubscriptionID,
		"subscription_usage_epoch": privateData.FundingUsageEpoch,
		"video_input":              properties.HasVideoInput, "video_input_ratio": properties.VideoInputRatio,
	} {
		other[key] = value
	}
	encoded, err := common.Marshal(other)
	if err != nil {
		return nil, err
	}
	return &model.Log{
		AuditEventId: &eventID, UserId: task.UserId, CreatedAt: task.CreatedAt,
		Type: service.LogTypeConsume, ModelName: properties.OriginModelName,
		Quota: actualQuota, ChannelId: task.ChannelId, TokenId: privateData.TokenID,
		Group: task.Group, Other: string(encoded),
	}, nil
}

func doubaoAuditEventID(reservationID string) string {
	return "doubao-video:" + reservationID
}

func doubaoDispatchTransitionMatches(taskID, reservationID, platform string) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusDispatched {
		return false
	}
	var operation model.TaskOperation
	return model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, platform, model.TaskOperationDispatching).First(&operation).Error == nil
}

func doubaoSettlementTransitionMatches(taskID, reservationID, platform string, actual int) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled || record.ActualQuota != actual {
		return false
	}
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, platform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationTerminal || operation.SettlementPending {
		return false
	}
	var task model.Task
	return model.DB.Where("task_id = ? AND platform = ? AND status = ? AND quota = ?", taskID,
		platform, model.TaskStatusSuccess, actual).First(&task).Error == nil &&
		doubaoTaskOperationIdentityMatches(&task, &operation, reservationID)
}

func doubaoRefundTransitionMatches(taskID, reservationID, platform string) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusRefunded {
		return false
	}
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, platform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationRefunded || operation.SettlementPending {
		return false
	}
	var task model.Task
	return model.DB.Where("task_id = ? AND platform = ? AND status = ? AND quota = 0", taskID,
		platform, model.TaskStatusFailure).First(&task).Error == nil &&
		doubaoTaskOperationIdentityMatches(&task, &operation, reservationID)
}

func doubaoManualReviewTransitionMatches(taskID, reservationID, platform string) bool {
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, platform).First(&operation).Error != nil {
		return false
	}
	return operation.State == model.TaskOperationManualReview && operation.SettlementPending &&
		operation.EncryptedProviderTaskID == ""
}

func loadDoubaoTaskOperation(taskID string) (*model.TaskOperation, error) {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND platform IN ?", taskID, model.DoubaoVideoTaskOperationPlatforms()).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func reloadDoubaoTask(task *model.Task) error {
	if task == nil || task.ID <= 0 {
		return fmt.Errorf("invalid Doubao task reload")
	}
	return model.DB.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
		task.Platform).First(task).Error
}
