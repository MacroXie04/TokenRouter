package relay

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	sunoOperationRetrySeconds    = int64(45)
	sunoDispatchRecoverySeconds  = int64(120)
	sunoReservationSafetySeconds = int64(8 * 24 * time.Hour / time.Second)
	sunoUnknownDispatchReason    = "provider submission outcome requires manual review"
)

func createSunoReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *sunoTaskPrivateData,
) (*service.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 ||
		task.ChannelId <= 0 || task.Platform != sunoTaskPlatform || task.Quota < 0 {
		return nil, errors.New("invalid Suno task reservation")
	}
	properties, err := decodeSunoTaskProperties(task.Properties)
	if err != nil || properties.Pricing.ModelName == "" {
		return nil, errors.New("invalid Suno task pricing")
	}
	return service.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, properties.Pricing.FreeModel,
		func(tx *gorm.DB, creation service.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalSunoTaskPrivateData(*privateData)
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
			minimumExpiry := now + sunoReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: sunoTaskPlatform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + sunoOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markSunoTaskDispatching(task *model.Task, reservation *service.RelayQuotaReservation) error {
	if task == nil || reservation == nil || task.ID <= 0 {
		return errors.New("invalid Suno dispatch transition")
	}
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + sunoReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		operation := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), sunoTaskPlatform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + sunoDispatchRecoverySeconds, "updated_at": now, "last_error": "",
			})
		if operation.Error != nil {
			return operation.Error
		}
		if operation.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				sunoTaskPlatform, model.TaskStatusNotStart).
			Update("updated_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		task.UpdatedAt = now
		return nil
	})
	if err == nil || sunoOperationStateMatches(task.TaskID, reservation.ReservationID(), model.TaskOperationDispatching) {
		return nil
	}
	return err
}

func refundRejectedSunoTask(task *model.Task, reservation *service.RelayQuotaReservation, reason string) error {
	if task == nil || reservation == nil {
		return errors.New("invalid rejected Suno task refund")
	}
	reason = boundedSunoFailReason(reason)
	err := reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, sunoTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), sunoTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !sunoTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("rejected Suno task identity mismatch")
		}
		privateData, err := decodeSunoTaskPrivateData(currentTask.PrivateData)
		if err != nil || operation.EncryptedProviderTaskID != "" || privateData.EncryptedProviderTaskID != "" {
			return errors.New("rejected Suno task conflicts with an accepted provider identity")
		}
		if operation.State == model.TaskOperationRefunded && currentTask.Status == model.TaskStatusFailure && currentTask.Quota == 0 {
			return nil
		}
		if operation.LeaseOwner != "" || currentTask.Status != model.TaskStatusNotStart ||
			(operation.State != model.TaskOperationPrepared && operation.State != model.TaskOperationDispatching) {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", currentTask.ID, currentTask.Status).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, operation.State, "").
			Updates(map[string]any{
				"state": model.TaskOperationRefunded, "settlement_pending": false,
				"encrypted_provider_task_id": "", "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || sunoOperationStateMatches(task.TaskID, reservation.ReservationID(), model.TaskOperationRefunded) {
		return model.DB.Where("id = ?", task.ID).First(task).Error
	}
	return err
}

func settleAcceptedSunoTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	providerTaskID string,
	expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted Suno task settlement")
	}
	properties, err := decodeSunoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	actualQuota, err := properties.Pricing.SettlementQuota(0)
	if err != nil || actualQuota < 0 {
		return errors.New("invalid Suno settlement quota")
	}
	encryptedProviderID, err := asyncTaskEncryptBound(
		providerTaskID,
		sunoProviderTaskBinding(task.TaskID, reservation.ReservationID(), task.UserId, task.ChannelId),
	)
	if err != nil {
		return err
	}
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	privateData.EncryptedProviderTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateJSON, err := marshalSunoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	err = reservation.SettleWithChannelAndPersistence(actualQuota, task.ChannelId, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, sunoTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), sunoTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !sunoTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("accepted Suno task identity mismatch")
		}
		if operation.State == model.TaskOperationSubmitted && !operation.SettlementPending &&
			operation.EncryptedProviderTaskID != "" && currentTask.Status == model.TaskStatusSubmitted {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				sunoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("Suno provider task id conflicts with durable state"), decryptErr)
			}
			return nil
		}
		if operation.State != expectedState || operation.LeaseOwner != leaseOwner ||
			(currentTask.Status != model.TaskStatusNotStart && currentTask.Status != model.TaskStatusSubmitted) {
			return service.ErrRelayQuotaReservationBusy
		}
		if operation.EncryptedProviderTaskID != "" {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				sunoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("Suno provider task id conflicts with durable state"), decryptErr)
			}
			encryptedProviderID = operation.EncryptedProviderTaskID
			privateData.EncryptedProviderTaskID = encryptedProviderID
			privateJSON, err = marshalSunoTaskPrivateData(privateData)
			if err != nil {
				return err
			}
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status = ?", currentTask.ID, currentTask.Status).
			Updates(map[string]any{
				"private_data": privateJSON, "status": model.TaskStatusSubmitted,
				"quota": actualQuota, "progress": "0%", "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": false,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            now + sunoOperationRetrySeconds, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || sunoAcceptedSettlementMatches(task.TaskID, reservation.ReservationID(), providerTaskID) {
		return model.DB.Where("id = ?", task.ID).First(task).Error
	}
	return err
}

func persistAcceptedSunoFallback(task *model.Task, reservationID, providerTaskID string) error {
	if task == nil || reservationID == "" || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted Suno fallback")
	}
	encryptedProviderID, err := asyncTaskEncryptBound(providerTaskID,
		sunoProviderTaskBinding(task.TaskID, reservationID, task.UserId, task.ChannelId))
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, sunoTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, sunoTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !sunoTaskOperationIdentityMatches(&currentTask, &operation) || operation.LeaseOwner != "" ||
			(operation.State != model.TaskOperationDispatching && operation.State != model.TaskOperationSubmitted &&
				operation.State != model.TaskOperationUnknown && operation.State != model.TaskOperationManualReview) {
			return service.ErrRelayQuotaReservationBusy
		}
		if operation.EncryptedProviderTaskID != "" {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				sunoProviderTaskBinding(task.TaskID, reservationID, task.UserId, task.ChannelId))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("Suno provider task id conflicts with durable state"), decryptErr)
			}
			encryptedProviderID = operation.EncryptedProviderTaskID
		}
		privateData, err := decodeSunoTaskPrivateData(currentTask.PrivateData)
		if err != nil {
			return err
		}
		privateData.EncryptedProviderTaskID = encryptedProviderID
		privateData.SettlementPending = true
		privateJSON, err := marshalSunoTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status IN ?", currentTask.ID,
			[]string{model.TaskStatusNotStart, model.TaskStatusSubmitted, model.TaskStatusUnknown}).
			Updates(map[string]any{
				"private_data": privateJSON, "status": model.TaskStatusSubmitted,
				"fail_reason": "", "progress": "0%", "finish_time": 0, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, operation.State, "").
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": true,
				"encrypted_provider_task_id": encryptedProviderID, "next_attempt_at": now,
				"completed_at": 0, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || sunoAcceptedFallbackMatches(task.TaskID, reservationID, providerTaskID) {
		return nil
	}
	return err
}

func settleUnknownSunoDispatch(task *model.Task, reservation *service.RelayQuotaReservation, reason, leaseOwner string) error {
	if task == nil || reservation == nil {
		return errors.New("invalid unknown Suno dispatch")
	}
	properties, err := decodeSunoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	actualQuota, err := properties.Pricing.SettlementQuota(0)
	if err != nil || actualQuota < 0 {
		return errors.New("invalid Suno settlement quota")
	}
	reason = boundedSunoFailReason(reason)
	err = reservation.SettleWithChannelAndPersistence(actualQuota, task.ChannelId, func(tx *gorm.DB) error {
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), sunoTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if operation.State == model.TaskOperationUnknown {
			return nil
		}
		if operation.State != model.TaskOperationDispatching || operation.LeaseOwner != leaseOwner || operation.EncryptedProviderTaskID != "" {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				sunoTaskPlatform, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "fail_reason": reason, "quota": actualQuota,
				"progress": "100%", "finish_time": now, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, model.TaskOperationDispatching, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationUnknown, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now,
				"last_error": reason, "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || sunoOperationStateMatches(task.TaskID, reservation.ReservationID(), model.TaskOperationUnknown) {
		return nil
	}
	return err
}

func reverseFailedSunoTask(task *model.Task, operation *model.TaskOperation, reason string, data string) error {
	if task == nil || operation == nil || operation.ReservationID == "" {
		return errors.New("invalid failed Suno task reversal")
	}
	reason = boundedSunoFailReason(reason)
	data, err := normalizeSunoTaskData(data)
	if err != nil {
		return err
	}
	return service.ReverseSettledRelayQuotaReservationWithPersistence(operation.ReservationID, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, sunoTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND reservation_id = ? AND platform = ?", operation.ID,
			operation.ReservationID, sunoTaskPlatform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !sunoTaskOperationIdentityMatches(&currentTask, &currentOperation) {
			return errors.New("failed Suno task identity mismatch")
		}
		if currentOperation.State == model.TaskOperationReversed && currentTask.Status == model.TaskStatusFailure && currentTask.Quota == 0 {
			return nil
		}
		if currentOperation.State != model.TaskOperationSubmitted || currentOperation.LeaseOwner != operation.LeaseOwner ||
			isSunoTerminalStatus(currentTask.Status) {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).Where("id = ? AND status = ?", currentTask.ID, currentTask.Status).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now, "data": data,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID,
				model.TaskOperationSubmitted, operation.LeaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationReversed, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

// reverseRejectedSunoTask repairs the narrow case where an authoritative
// rejection journal arrives after another node has already conservatively
// settled the dispatch as unknown. No provider identity may exist, and the
// reversal and terminal task transition are committed atomically.
func reverseRejectedSunoTask(task *model.Task, operation *model.TaskOperation, reason string) error {
	if task == nil || operation == nil || operation.ReservationID == "" {
		return errors.New("invalid rejected Suno task reversal")
	}
	reason = boundedSunoFailReason(reason)
	return service.ReverseSettledRelayQuotaReservationWithPersistence(operation.ReservationID, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, sunoTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND reservation_id = ? AND platform = ?", operation.ID,
			operation.ReservationID, sunoTaskPlatform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !sunoTaskOperationIdentityMatches(&currentTask, &currentOperation) {
			return errors.New("rejected Suno reversal identity mismatch")
		}
		privateData, err := decodeSunoTaskPrivateData(currentTask.PrivateData)
		if err != nil || currentOperation.EncryptedProviderTaskID != "" || privateData.EncryptedProviderTaskID != "" {
			return errors.New("rejected Suno reversal conflicts with an accepted provider identity")
		}
		if currentOperation.State == model.TaskOperationReversed &&
			currentTask.Status == model.TaskStatusFailure && currentTask.Quota == 0 {
			return nil
		}
		if (currentOperation.State != model.TaskOperationUnknown &&
			currentOperation.State != model.TaskOperationManualReview) ||
			currentOperation.LeaseOwner != "" || currentTask.Status != model.TaskStatusUnknown {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", currentTask.ID, model.TaskStatusUnknown).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID,
				currentOperation.State, "").
			Updates(map[string]any{
				"state": model.TaskOperationReversed, "settlement_pending": false,
				"next_attempt_at": 0, "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

func sunoTaskOperationIdentityMatches(task *model.Task, operation *model.TaskOperation) bool {
	if !isSunoTask(task) || operation == nil || operation.Platform != sunoTaskPlatform ||
		operation.TaskID != task.TaskID || operation.UserID != task.UserId || operation.ChannelID != task.ChannelId {
		return false
	}
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	return err == nil && privateData.RelayReservationID == operation.ReservationID
}

func sunoOperationStateMatches(taskID, reservationID, state string) bool {
	var operation model.TaskOperation
	return model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, sunoTaskPlatform, state).First(&operation).Error == nil
}

func sunoAcceptedSettlementMatches(taskID, reservationID, providerID string) bool {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, sunoTaskPlatform).First(&operation).Error; err != nil ||
		operation.State != model.TaskOperationSubmitted || operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" {
		return false
	}
	stored, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		sunoProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID))
	return err == nil && stored == providerID
}

func sunoAcceptedFallbackMatches(taskID, reservationID, providerID string) bool {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, sunoTaskPlatform).First(&operation).Error; err != nil ||
		operation.State != model.TaskOperationSubmitted || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" {
		return false
	}
	stored, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		sunoProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID))
	return err == nil && stored == providerID
}

func isSunoTerminalStatus(status string) bool {
	return status == model.TaskStatusSuccess || status == model.TaskStatusFailure || status == model.TaskStatusUnknown
}

func loadSunoTaskOperation(taskID string) (*model.TaskOperation, error) {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, sunoTaskPlatform).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func ensureSunoPrivateProviderCipher(task *model.Task, operation *model.TaskOperation) error {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" {
		return errors.New("Suno provider task id is unavailable")
	}
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	if privateData.EncryptedProviderTaskID == "" {
		privateData.EncryptedProviderTaskID = operation.EncryptedProviderTaskID
	}
	if privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return fmt.Errorf("Suno provider task ciphertext mismatch")
	}
	return nil
}
