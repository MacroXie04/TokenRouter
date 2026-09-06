package relay

import (
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

const (
	klingOperationLeaseSeconds    = int64(90)
	klingOperationRetrySeconds    = int64(45)
	klingDispatchRecoverySeconds  = int64(120)
	klingReservationSafetySeconds = int64(8 * 24 * time.Hour / time.Second)
	klingOperationBatchSize       = 100
	klingOperationMaxAttempts     = 20_000
	klingOperationMaxPollDays     = 7

	klingUnknownDispatchReason     = "Kling provider submission outcome requires manual review"
	klingPollingManualReviewReason = "Kling task exceeded automatic polling horizon"
)

func createKlingReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *klingTaskPrivateData,
) (*service.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 || task.ChannelId <= 0 ||
		task.Platform != klingTaskPlatform {
		return nil, errors.New("invalid Kling task reservation")
	}
	return service.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, privateData.Pricing.FreeModel,
		func(tx *gorm.DB, creation service.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalKlingTaskPrivateData(*privateData)
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
			minimumExpiry := now + klingReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: klingTaskPlatform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + klingOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markKlingTaskDispatching(task *model.Task, reservation *service.RelayQuotaReservation) error {
	if task == nil || reservation == nil || task.ID <= 0 || !isKlingTask(task) {
		return errors.New("invalid Kling dispatch transition")
	}
	var now int64
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		var err error
		now, err = model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + klingReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				klingTaskPlatform, model.TaskStatusNotStart).
			Update("updated_at", now)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), klingTaskPlatform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + klingDispatchRecoverySeconds,
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
	if err != nil && !klingDispatchTransitionMatches(task.TaskID, reservation.ReservationID()) {
		return err
	}
	task.UpdatedAt = now
	return nil
}

// persistAcceptedKlingTask records provider identity before returning an
// accepted non-terminal task. Its reservation deliberately stays dispatched:
// the provider's final units do not exist yet.
func persistAcceptedKlingTask(
	task *model.Task,
	reservationID string,
	provider *kling.Task,
	expectedState, leaseOwner string,
) error {
	if provider == nil || (provider.Status != kling.StatusSubmitted && provider.Status != kling.StatusProcessing) {
		return errors.New("invalid non-terminal Kling provider state")
	}
	return persistKlingProviderStatePending(task, reservationID, provider, expectedState, leaseOwner)
}

// persistKlingProviderStatePending is also the post-acceptance fallback for a
// terminal response whose accounting transaction could not commit. Terminal
// provider truth is stored, but Task remains non-terminal until settlement or
// refund commits atomically.
func persistKlingProviderStatePending(
	task *model.Task,
	reservationID string,
	provider *kling.Task,
	expectedState, leaseOwner string,
) error {
	if task == nil || provider == nil || strings.TrimSpace(provider.ProviderTaskID) == "" ||
		!validKlingStoredStatus(provider.Status) ||
		(expectedState != model.TaskOperationDispatching && expectedState != model.TaskOperationSubmitted &&
			expectedState != model.TaskOperationManualReview) {
		return errors.New("invalid accepted Kling fallback")
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservationID {
		return errors.New("invalid Kling recovery metadata")
	}
	encryptedProviderID, err := klingProviderCipher(
		task, privateData, nil, provider.ProviderTaskID, properties.Action,
	)
	if err != nil {
		return err
	}
	privateData.EncryptedUpstreamTaskID = encryptedProviderID
	privateData.SettlementPending = true
	privateData.CompletionUnits = provider.CompletionUnits
	privateJSON, err := marshalKlingTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	stored, err := normalizedKlingTaskData(provider)
	if err != nil {
		return err
	}
	dataJSON, err := marshalKlingStoredTaskData(stored)
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	visibleStatus := klingTaskStatus(provider.Status)
	if provider.Status == kling.StatusSucceeded || provider.Status == kling.StatusFailed {
		visibleStatus = model.TaskStatusSubmitted
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, klingTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !klingTaskOperationIdentityMatches(&currentTask, &operation, reservationID) {
			return errors.New("accepted Kling identity mismatch")
		}
		if operation.State == model.TaskOperationTerminal || operation.State == model.TaskOperationRefunded {
			return nil
		}
		if operation.State != expectedState || operation.LeaseOwner != leaseOwner {
			return service.ErrRelayQuotaReservationBusy
		}
		if err := verifyKlingProviderCipherConsistency(&currentTask, &operation, provider.ProviderTaskID, properties.Action); err != nil {
			return err
		}
		taskUpdates := map[string]any{
			"status": visibleStatus, "private_data": privateJSON, "data": dataJSON,
			"progress": klingTaskProgress(visibleStatus), "fail_reason": "", "updated_at": now,
		}
		if visibleStatus == model.TaskStatusRunning && currentTask.StartTime == 0 {
			taskUpdates["start_time"] = now
		}
		if currentTask.Status == model.TaskStatusUnknown {
			taskUpdates["finish_time"] = 0
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, klingTaskPlatform).
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
				"next_attempt_at":            now + klingOperationRetrySeconds,
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

func settleSuccessfulKlingTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	provider *kling.Task,
	expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil || provider == nil || provider.Status != kling.StatusSucceeded ||
		strings.TrimSpace(provider.ProviderTaskID) == "" {
		return errors.New("invalid successful Kling settlement")
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservation.ReservationID() {
		return errors.New("invalid successful Kling accounting metadata")
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
		reservation.ReservationID(), klingTaskPlatform).First(&operation).Error; err != nil {
		return err
	}
	encryptedProviderID, err := klingProviderCipher(
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
	privateJSON, err := marshalKlingTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	stored, err := normalizedKlingTaskData(provider)
	if err != nil {
		return err
	}
	dataJSON, err := marshalKlingStoredTaskData(stored)
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
			reservation.ReservationID(), klingTaskPlatform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !klingTaskOperationIdentityMatches(&currentTask, &currentOperation, reservation.ReservationID()) {
			return errors.New("successful Kling settlement identity mismatch")
		}
		if err := verifyKlingProviderCipherConsistency(&currentTask, &currentOperation,
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
			audit, err := newKlingConsumeAudit(currentTask, privateData, reservation.ReservationID(), actualQuota)
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
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, klingTaskPlatform).
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
		audit, err := newKlingConsumeAudit(updated, privateData, reservation.ReservationID(), actualQuota)
		if err != nil {
			return err
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
	if err != nil && !klingSettlementTransitionMatches(task.TaskID, reservation.ReservationID(), actualQuota) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func refundKlingTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	provider *kling.Task,
	reason, expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid Kling refund")
	}
	reason = boundedKlingFailReason(reason)
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservation.ReservationID() {
		return errors.New("invalid Kling refund metadata")
	}
	stored := klingStoredTaskData{Status: kling.StatusFailed, StatusMessage: reason}
	encryptedProviderID := privateData.EncryptedUpstreamTaskID
	completionUnits := privateData.CompletionUnits
	if provider != nil {
		if provider.Status != kling.StatusFailed {
			return errors.New("invalid Kling failure state")
		}
		stored, err = normalizedKlingTaskData(provider)
		if err != nil {
			return err
		}
		completionUnits = provider.CompletionUnits
		if strings.TrimSpace(provider.ProviderTaskID) != "" {
			var operation model.TaskOperation
			_ = model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
				reservation.ReservationID(), klingTaskPlatform).First(&operation).Error
			encryptedProviderID, err = klingProviderCipher(task, privateData, &operation,
				provider.ProviderTaskID, properties.Action)
			if err != nil {
				return err
			}
		}
	}
	dataJSON, err := marshalKlingStoredTaskData(stored)
	if err != nil {
		return err
	}
	privateData.EncryptedUpstreamTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateData.CompletionUnits = completionUnits
	privateJSON, err := marshalKlingTaskPrivateData(privateData)
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
			reservation.ReservationID(), klingTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !klingTaskOperationIdentityMatches(&currentTask, &operation, reservation.ReservationID()) {
			return errors.New("Kling refund identity mismatch")
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
			if err := verifyKlingProviderCipherConsistency(&currentTask, &operation,
				provider.ProviderTaskID, properties.Action); err != nil {
				return err
			}
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, klingTaskPlatform).
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
	if err != nil && !klingRefundTransitionMatches(task.TaskID, reservation.ReservationID()) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func markKlingAmbiguousDispatch(task *model.Task, reservationID, reason string) error {
	if task == nil || reservationID == "" {
		return errors.New("invalid ambiguous Kling dispatch")
	}
	reason = boundedKlingFailReason(reason)
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
			reservationID, klingTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !klingTaskOperationIdentityMatches(&currentTask, &operation, reservationID) ||
			operation.State != model.TaskOperationDispatching || operation.LeaseOwner != "" {
			return service.ErrRelayQuotaReservationBusy
		}
		privateData, err := decodeKlingTaskPrivateData(currentTask.PrivateData)
		if err != nil || privateData.EncryptedUpstreamTaskID != "" {
			return errors.New("ambiguous Kling dispatch metadata is invalid")
		}
		privateData.SettlementPending = true
		privateJSON, err := marshalKlingTaskPrivateData(privateData)
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
	if err != nil && klingManualReviewTransitionMatches(task.TaskID, reservationID) {
		return nil
	}
	return err
}

func klingProviderCipher(
	task *model.Task,
	privateData klingTaskPrivateData,
	operation *model.TaskOperation,
	providerTaskID string,
	action kling.Action,
) (string, error) {
	binding := klingProviderTaskBinding(task.TaskID, privateData.RelayReservationID, task.UserId, task.ChannelId, action)
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
			return "", errors.New("Kling provider identity conflicts with durable state")
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

func verifyKlingProviderCipherConsistency(
	task *model.Task,
	operation *model.TaskOperation,
	providerTaskID string,
	action kling.Action,
) error {
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	binding := klingProviderTaskBinding(task.TaskID, operation.ReservationID, task.UserId, task.ChannelId, action)
	for _, encoded := range []string{privateData.EncryptedUpstreamTaskID, operation.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || decoded != providerTaskID {
			return errors.New("Kling provider identity conflicts with durable state")
		}
	}
	return nil
}

func newKlingConsumeAudit(
	task model.Task,
	privateData klingTaskPrivateData,
	reservationID string,
	actualQuota int,
) (*model.Log, error) {
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil || task.TaskID == "" || reservationID == "" {
		return nil, errors.New("invalid Kling consume audit")
	}
	eventID := klingAuditEventID(reservationID)
	if len(eventID) > 64 {
		return nil, errors.New("invalid Kling consume audit identity")
	}
	other := privateData.Pricing.BillingLogFields()
	for key, value := range map[string]any{
		"billing_source": privateData.BillingSource, "relay_reservation_id": reservationID,
		"task_id": task.TaskID, "task_platform": klingTaskPlatform, "action": string(properties.Action),
		"completion_units": privateData.CompletionUnits, "subscription_id": privateData.SubscriptionID,
		"subscription_usage_epoch": privateData.FundingUsageEpoch,
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

func klingDispatchTransitionMatches(taskID, reservationID string) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusDispatched {
		return false
	}
	var operation model.TaskOperation
	return model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, klingTaskPlatform, model.TaskOperationDispatching).First(&operation).Error == nil
}

func klingSettlementTransitionMatches(taskID, reservationID string, actual int) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled || record.ActualQuota != actual {
		return false
	}
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, klingTaskPlatform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationTerminal || operation.SettlementPending {
		return false
	}
	var task model.Task
	return model.DB.Where("task_id = ? AND platform = ? AND status = ? AND quota = ?", taskID,
		klingTaskPlatform, model.TaskStatusSuccess, actual).First(&task).Error == nil &&
		klingTaskOperationIdentityMatches(&task, &operation, reservationID)
}

func klingRefundTransitionMatches(taskID, reservationID string) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusRefunded {
		return false
	}
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, klingTaskPlatform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationRefunded || operation.SettlementPending {
		return false
	}
	var task model.Task
	return model.DB.Where("task_id = ? AND platform = ? AND status = ? AND quota = 0", taskID,
		klingTaskPlatform, model.TaskStatusFailure).First(&task).Error == nil &&
		klingTaskOperationIdentityMatches(&task, &operation, reservationID)
}

func klingManualReviewTransitionMatches(taskID, reservationID string) bool {
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, klingTaskPlatform).First(&operation).Error != nil {
		return false
	}
	return operation.State == model.TaskOperationManualReview && operation.SettlementPending &&
		operation.EncryptedProviderTaskID == ""
}

func loadKlingTaskOperation(taskID string) (*model.TaskOperation, error) {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, klingTaskPlatform).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func reloadKlingTask(task *model.Task) error {
	if task == nil || task.ID <= 0 {
		return fmt.Errorf("invalid Kling task reload")
	}
	return model.DB.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
		klingTaskPlatform).First(task).Error
}
