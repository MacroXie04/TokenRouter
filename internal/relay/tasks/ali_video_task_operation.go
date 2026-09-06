package tasks

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
)

const (
	aliWanOperationRetrySeconds    = int64(45)
	aliWanDispatchRecoverySeconds  = int64(120)
	aliWanReservationSafetySeconds = int64(8 * 24 * time.Hour / time.Second)
	aliWanUnknownDispatchReason    = "provider submission outcome requires manual review"
)

func createAliWanReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *aliWanTaskPrivateData,
) (*billingsvc.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 ||
		task.ChannelId <= 0 || task.Platform != aliWanTaskPlatform || task.Quota < 0 {
		return nil, errors.New("invalid AliWan task reservation")
	}
	if privateData.RelayReservationID != "" || privateData.EncryptedProviderTaskID != "" ||
		privateData.SettlementPending || privateData.BillingSource != "" ||
		privateData.SubscriptionID != 0 || privateData.FundingUsageEpoch != 0 || privateData.TokenID != 0 {
		return nil, errors.New("AliWan task reservation metadata is not pristine")
	}
	if _, err := validateAliWanTaskPricingSnapshot(task, *privateData, task.Quota); err != nil {
		return nil, err
	}
	return billingsvc.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, privateData.Pricing.Plan.FreeModel,
		func(tx *gorm.DB, creation billingsvc.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalAliWanTaskPrivateData(*privateData)
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
			minimumExpiry := now + aliWanReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: aliWanTaskPlatform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + aliWanOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markAliWanTaskDispatching(task *model.Task, reservation *billingsvc.RelayQuotaReservation) error {
	if task == nil || reservation == nil || task.ID <= 0 {
		return errors.New("invalid AliWan dispatch transition")
	}
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + aliWanReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		operation := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), aliWanTaskPlatform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + aliWanDispatchRecoverySeconds, "updated_at": now, "last_error": "",
			})
		if operation.Error != nil {
			return operation.Error
		}
		if operation.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				aliWanTaskPlatform, model.TaskStatusNotStart).
			Update("updated_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		task.UpdatedAt = now
		return nil
	})
	if err == nil || aliWanOperationStateMatches(task.TaskID, reservation.ReservationID(), model.TaskOperationDispatching) {
		return nil
	}
	return err
}

func refundRejectedAliWanTask(task *model.Task, reservation *billingsvc.RelayQuotaReservation, reason string) error {
	if task == nil || reservation == nil {
		return errors.New("invalid rejected AliWan task refund")
	}
	reason = boundedAliWanFailReason(reason)
	err := reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, aliWanTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), aliWanTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !aliWanTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("rejected AliWan task identity mismatch")
		}
		if operation.State == model.TaskOperationRefunded && currentTask.Status == model.TaskStatusFailure && currentTask.Quota == 0 {
			return nil
		}
		if operation.LeaseOwner != "" || currentTask.Status != model.TaskStatusNotStart ||
			(operation.State != model.TaskOperationPrepared && operation.State != model.TaskOperationDispatching) {
			return billingsvc.ErrRelayQuotaReservationBusy
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || aliWanOperationStateMatches(task.TaskID, reservation.ReservationID(), model.TaskOperationRefunded) {
		return model.DB.Where("id = ?", task.ID).First(task).Error
	}
	return err
}

func settleAcceptedAliWanTask(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	providerTaskID string,
	expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted AliWan task settlement")
	}
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	actualQuota, err := properties.Pricing.quota(properties.UpstreamModelName, properties.Resolution)
	if err != nil || actualQuota < 0 {
		return errors.New("invalid AliWan settlement quota")
	}
	encryptedProviderID, err := asyncTaskEncryptBound(
		providerTaskID,
		aliWanProviderTaskBinding(task.TaskID, reservation.ReservationID(), task.UserId, task.ChannelId),
	)
	if err != nil {
		return err
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	if _, err := validateAliWanTaskPricingSnapshot(task, privateData, actualQuota); err != nil {
		return err
	}
	privateData.EncryptedProviderTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateJSON, err := marshalAliWanTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	err = reservation.SettleWithChannelAndPersistence(actualQuota, task.ChannelId, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, aliWanTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), aliWanTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !aliWanTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("accepted AliWan task identity mismatch")
		}
		currentPrivate, currentPrivateErr := decodeAliWanTaskPrivateData(currentTask.PrivateData)
		if currentPrivateErr != nil {
			return currentPrivateErr
		}
		if _, snapshotErr := validateAliWanTaskPricingSnapshot(&currentTask, currentPrivate, actualQuota); snapshotErr != nil {
			return snapshotErr
		}
		if operation.State == model.TaskOperationSubmitted && !operation.SettlementPending &&
			operation.EncryptedProviderTaskID != "" && currentTask.Status == model.TaskStatusSubmitted {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				aliWanProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("AliWan provider task id conflicts with durable state"), decryptErr)
			}
			audit, auditErr := newAliWanConsumeAudit(currentTask, privateData,
				reservation.ReservationID(), actualQuota)
			if auditErr != nil {
				return auditErr
			}
			return billingsvc.EnqueueAuditLogTx(tx, audit)
		}
		if operation.State != expectedState || operation.LeaseOwner != leaseOwner ||
			(currentTask.Status != model.TaskStatusNotStart && currentTask.Status != model.TaskStatusSubmitted) {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		if operation.EncryptedProviderTaskID != "" {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				aliWanProviderTaskBinding(task.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("AliWan provider task id conflicts with durable state"), decryptErr)
			}
			encryptedProviderID = operation.EncryptedProviderTaskID
			privateData.EncryptedProviderTaskID = encryptedProviderID
			privateJSON, err = marshalAliWanTaskPrivateData(privateData)
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": false,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            now + aliWanOperationRetrySeconds, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		auditTask := currentTask
		auditTask.Quota = actualQuota
		audit, auditErr := newAliWanConsumeAudit(auditTask, privateData,
			reservation.ReservationID(), actualQuota)
		if auditErr != nil {
			return auditErr
		}
		return billingsvc.EnqueueAuditLogTx(tx, audit)
	})
	if err == nil || aliWanAcceptedSettlementMatches(task.TaskID, reservation.ReservationID(), providerTaskID) {
		return model.DB.Where("id = ?", task.ID).First(task).Error
	}
	return err
}

func persistAcceptedAliWanFallback(task *model.Task, reservationID, providerTaskID string) error {
	if task == nil || reservationID == "" || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted AliWan fallback")
	}
	encryptedProviderID, err := asyncTaskEncryptBound(providerTaskID,
		aliWanProviderTaskBinding(task.TaskID, reservationID, task.UserId, task.ChannelId))
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, aliWanTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, aliWanTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !aliWanTaskOperationIdentityMatches(&currentTask, &operation) || operation.LeaseOwner != "" ||
			(operation.State != model.TaskOperationDispatching && operation.State != model.TaskOperationSubmitted &&
				operation.State != model.TaskOperationUnknown && operation.State != model.TaskOperationManualReview) {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		if operation.EncryptedProviderTaskID != "" {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				aliWanProviderTaskBinding(task.TaskID, reservationID, task.UserId, task.ChannelId))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("AliWan provider task id conflicts with durable state"), decryptErr)
			}
			encryptedProviderID = operation.EncryptedProviderTaskID
		}
		privateData, err := decodeAliWanTaskPrivateData(currentTask.PrivateData)
		if err != nil {
			return err
		}
		if _, err := validateAliWanTaskPricingSnapshot(&currentTask, privateData, currentTask.Quota); err != nil {
			return err
		}
		privateData.EncryptedProviderTaskID = encryptedProviderID
		privateData.SettlementPending = true
		privateJSON, err := marshalAliWanTaskPrivateData(privateData)
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || aliWanAcceptedFallbackMatches(task.TaskID, reservationID, providerTaskID) {
		return nil
	}
	return err
}

func markAliWanAmbiguousDispatch(task *model.Task, reservationID, reason string) error {
	return markAliWanAmbiguousDispatchWithLease(task, reservationID, reason, "")
}

// markAliWanAmbiguousDispatch fences a request that may have reached AliWan but
// returned no durable provider id. The dispatched hold is deliberately kept:
// replaying could duplicate a paid task, while settling would charge an
// outcome that cannot be verified. An operator must explicitly settle/refund.
func markAliWanAmbiguousDispatchWithLease(task *model.Task, reservationID, reason, leaseOwner string) error {
	if task == nil || task.ID <= 0 || reservationID == "" {
		return errors.New("invalid unknown AliWan dispatch")
	}
	reason = boundedAliWanFailReason(reason)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, aliWanTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, aliWanTaskPlatform).First(&operation).Error; err != nil {
			return err
		}
		if !aliWanTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("unknown AliWan task identity mismatch")
		}
		if operation.State == model.TaskOperationManualReview && operation.SettlementPending &&
			currentTask.Status == model.TaskStatusUnknown {
			return nil
		}
		if operation.State != model.TaskOperationDispatching || operation.LeaseOwner != leaseOwner ||
			operation.EncryptedProviderTaskID != "" || currentTask.Status != model.TaskStatusNotStart {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		var reservation model.RelayQuotaReservationRecord
		if err := tx.Where("reservation_id = ? AND user_id = ?", reservationID, task.UserId).
			First(&reservation).Error; err != nil {
			return err
		}
		if reservation.Status != model.RelayQuotaReservationStatusDispatched {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		privateData, err := decodeAliWanTaskPrivateData(currentTask.PrivateData)
		if err != nil {
			return err
		}
		privateData.SettlementPending = true
		privateJSON, err := marshalAliWanTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", currentTask.ID, model.TaskStatusNotStart).
			Updates(map[string]any{"private_data": privateJSON, "status": model.TaskStatusUnknown,
				"fail_reason": reason, "progress": "100%", "finish_time": now, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID,
				model.TaskOperationDispatching, leaseOwner).
			Updates(map[string]any{"state": model.TaskOperationManualReview,
				"settlement_pending": true, "next_attempt_at": 0, "completed_at": now,
				"updated_at": now, "last_error": reason, "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || aliWanOperationStateMatches(task.TaskID, reservationID, model.TaskOperationManualReview) {
		return nil
	}
	return err
}

func reverseFailedAliWanTask(task *model.Task, operation *model.TaskOperation, reason string, data string) error {
	if task == nil || operation == nil || operation.ReservationID == "" {
		return errors.New("invalid failed AliWan task reversal")
	}
	reason = boundedAliWanFailReason(reason)
	return billingsvc.ReverseSettledRelayQuotaReservationWithPersistence(operation.ReservationID, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, aliWanTaskPlatform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND reservation_id = ? AND platform = ?", operation.ID,
			operation.ReservationID, aliWanTaskPlatform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !aliWanTaskOperationIdentityMatches(&currentTask, &currentOperation) {
			return errors.New("failed AliWan task identity mismatch")
		}
		if currentOperation.State == model.TaskOperationReversed && currentTask.Status == model.TaskStatusFailure && currentTask.Quota == 0 {
			audit, auditErr := newAliWanRefundAudit(tx, currentTask, operation.ReservationID,
				currentTask.FailReason, currentTask.FinishTime)
			if auditErr != nil {
				return auditErr
			}
			return billingsvc.EnqueueAuditLogTx(tx, audit)
		}
		if currentOperation.State != model.TaskOperationSubmitted || currentOperation.LeaseOwner != operation.LeaseOwner {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		switch currentTask.Status {
		case model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusFailure:
		default:
			return billingsvc.ErrRelayQuotaReservationBusy
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
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
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		audit, auditErr := newAliWanRefundAudit(tx, currentTask, operation.ReservationID, reason, now)
		if auditErr != nil {
			return auditErr
		}
		return billingsvc.EnqueueAuditLogTx(tx, audit)
	})
}

func aliWanTaskOperationIdentityMatches(task *model.Task, operation *model.TaskOperation) bool {
	if !isAliWanTask(task) || operation == nil || operation.Platform != aliWanTaskPlatform ||
		operation.TaskID != task.TaskID || operation.UserID != task.UserId || operation.ChannelID != task.ChannelId {
		return false
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	return err == nil && privateData.RelayReservationID == operation.ReservationID
}

func aliWanOperationStateMatches(taskID, reservationID, state string) bool {
	var operation model.TaskOperation
	return model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, aliWanTaskPlatform, state).First(&operation).Error == nil
}

func aliWanAcceptedSettlementMatches(taskID, reservationID, providerID string) bool {
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, aliWanTaskPlatform).
		First(&task).Error; err != nil || task.Status != model.TaskStatusSubmitted {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, aliWanTaskPlatform).First(&operation).Error; err != nil ||
		operation.State != model.TaskOperationSubmitted || operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" ||
		!aliWanTaskOperationIdentityMatches(&task, &operation) {
		return false
	}
	stored, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		aliWanProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID))
	if err != nil || stored != providerID {
		return false
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || privateData.SettlementPending ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return false
	}
	if _, err := validateAliWanTaskPricingSnapshot(&task, privateData, task.Quota); err != nil {
		return false
	}
	properties, propertiesErr := decodeAliWanTaskProperties(task.Properties)
	if propertiesErr != nil {
		return false
	}
	actualQuota, err := privateData.Pricing.quota(properties.UpstreamModelName, properties.Resolution)
	if err != nil || task.Quota != actualQuota {
		return false
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != actualQuota || record.ChannelID != task.ChannelId ||
		record.UserID != task.UserId {
		return false
	}
	var auditCount int64
	if err := model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", aliWanAuditEventID(reservationID)).Count(&auditCount).Error; err != nil {
		return false
	}
	return auditCount == 1
}

func aliWanAcceptedFallbackMatches(taskID, reservationID, providerID string) bool {
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, aliWanTaskPlatform).
		First(&task).Error; err != nil || task.Status != model.TaskStatusSubmitted {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, aliWanTaskPlatform).First(&operation).Error; err != nil ||
		operation.State != model.TaskOperationSubmitted || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" ||
		!aliWanTaskOperationIdentityMatches(&task, &operation) {
		return false
	}
	stored, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		aliWanProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID))
	if err != nil || stored != providerID {
		return false
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || !privateData.SettlementPending ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return false
	}
	var record model.RelayQuotaReservationRecord
	return model.DB.Where("reservation_id = ? AND user_id = ? AND status = ? AND operation = ?",
		reservationID, task.UserId, model.RelayQuotaReservationStatusDispatched,
		model.RelayQuotaReservationOperationSettle).First(&record).Error == nil &&
		record.ChannelID == 0 && record.ActualQuota == record.ReservedQuota &&
		task.Quota == record.RequestedQuota
}

func isAliWanTerminalStatus(status string) bool {
	return status == model.TaskStatusSuccess || status == model.TaskStatusFailure || status == model.TaskStatusUnknown
}

func loadAliWanTaskOperation(taskID string) (*model.TaskOperation, error) {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, aliWanTaskPlatform).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func ensureAliWanPrivateProviderCipher(task *model.Task, operation *model.TaskOperation) error {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" {
		return errors.New("AliWan provider task id is unavailable")
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	if privateData.EncryptedProviderTaskID == "" {
		privateData.EncryptedProviderTaskID = operation.EncryptedProviderTaskID
	}
	if privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return fmt.Errorf("AliWan provider task ciphertext mismatch")
	}
	return nil
}

func newAliWanConsumeAudit(task model.Task, privateData aliWanTaskPrivateData,
	reservationID string, quota int) (*model.Log, error) {
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil || reservationID == "" || quota < 0 {
		return nil, errors.New("invalid AliWan consume audit")
	}
	eventID := aliWanAuditEventID(reservationID)
	other := privateData.Pricing.Plan.BillingLogFields()
	other["duration"] = privateData.Pricing.Duration
	other["resolution_multiplier"] = privateData.Pricing.ResolutionMultiplier
	other["billing_source"] = privateData.BillingSource
	other["relay_reservation_id"] = reservationID
	other["task_id"] = task.TaskID
	other["task_platform"] = task.Platform
	other["provider_units_ignored"] = true
	other["subscription_id"] = privateData.SubscriptionID
	other["subscription_usage_epoch"] = privateData.FundingUsageEpoch
	encoded, err := jsonutil.Marshal(other)
	if err != nil || len(eventID) > 64 {
		return nil, errors.New("invalid AliWan consume audit")
	}
	return &model.Log{AuditEventId: &eventID, UserId: task.UserId, CreatedAt: task.CreatedAt,
		Type: billingsvc.LogTypeConsume, ModelName: properties.OriginModelName, Quota: quota,
		ChannelId: task.ChannelId, TokenId: privateData.TokenID, Group: task.Group,
		Other: string(encoded)}, nil
}

func newAliWanRefundAudit(tx *gorm.DB, task model.Task, reservationID, reason string,
	createdAt int64) (*model.Log, error) {
	if tx == nil || reservationID == "" || createdAt <= 0 {
		return nil, errors.New("invalid AliWan refund audit")
	}
	properties, err := decodeAliWanTaskProperties(task.Properties)
	if err != nil {
		return nil, err
	}
	var record model.RelayQuotaReservationRecord
	if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	eventID := "ali-wan-refund:" + reservationID
	other, err := jsonutil.Marshal(map[string]any{"relay_reservation_id": reservationID,
		"task_id": task.TaskID, "task_platform": task.Platform,
		"reason": boundedAliWanFailReason(reason)})
	if err != nil || len(eventID) > 64 {
		return nil, errors.New("invalid AliWan refund audit")
	}
	return &model.Log{AuditEventId: &eventID, UserId: task.UserId, CreatedAt: createdAt,
		Type: billingsvc.LogTypeRefund, ModelName: properties.OriginModelName, Quota: record.ActualQuota,
		ChannelId: task.ChannelId, TokenId: record.TokenID, Group: task.Group,
		Other: string(other)}, nil
}
