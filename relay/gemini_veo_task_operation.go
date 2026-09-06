package relay

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	geminiVeoOperationRetrySeconds    = int64(45)
	geminiVeoDispatchRecoverySeconds  = int64(120)
	geminiVeoReservationSafetySeconds = int64(8 * 24 * time.Hour / time.Second)
	geminiVeoUnknownDispatchReason    = "provider submission outcome requires manual review"
)

func createGeminiVeoReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *geminiVeoTaskPrivateData,
) (*service.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 ||
		task.ChannelId <= 0 || !model.IsVeoTaskOperationPlatform(task.Platform) || task.Quota < 0 {
		return nil, errors.New("invalid GeminiVeo task reservation")
	}
	if privateData.RelayReservationID != "" || privateData.EncryptedProviderTaskID != "" ||
		privateData.SettlementPending || privateData.BillingSource != "" ||
		privateData.SubscriptionID != 0 || privateData.FundingUsageEpoch != 0 || privateData.TokenID != 0 {
		return nil, errors.New("GeminiVeo task reservation metadata is not pristine")
	}
	if _, err := validateGeminiVeoTaskPricingSnapshot(task, *privateData, task.Quota); err != nil {
		return nil, err
	}
	return service.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, privateData.Pricing.Plan.FreeModel,
		func(tx *gorm.DB, creation service.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalGeminiVeoTaskPrivateData(*privateData)
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
			minimumExpiry := now + geminiVeoReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: task.Platform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + geminiVeoOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markGeminiVeoTaskDispatching(task *model.Task, reservation *service.RelayQuotaReservation) error {
	if task == nil || reservation == nil || task.ID <= 0 {
		return errors.New("invalid GeminiVeo dispatch transition")
	}
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + geminiVeoReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		operation := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), task.Platform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + geminiVeoDispatchRecoverySeconds, "updated_at": now, "last_error": "",
			})
		if operation.Error != nil {
			return operation.Error
		}
		if operation.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				task.Platform, model.TaskStatusNotStart).
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
	if err == nil || geminiVeoOperationStateMatches(task.TaskID, reservation.ReservationID(), task.Platform, model.TaskOperationDispatching) {
		return nil
	}
	return err
}

func refundRejectedGeminiVeoTask(task *model.Task, reservation *service.RelayQuotaReservation, reason string) error {
	if task == nil || reservation == nil {
		return errors.New("invalid rejected GeminiVeo task refund")
	}
	reason = boundedGeminiVeoFailReason(reason)
	err := reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, task.Platform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !geminiVeoTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("rejected GeminiVeo task identity mismatch")
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
	if err == nil || geminiVeoOperationStateMatches(task.TaskID, reservation.ReservationID(), task.Platform, model.TaskOperationRefunded) {
		return model.DB.Where("id = ?", task.ID).First(task).Error
	}
	return err
}

func settleAcceptedGeminiVeoTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	providerTaskID string,
	expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted GeminiVeo task settlement")
	}
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	actualQuota, err := properties.Pricing.quota(properties.UpstreamModelName, properties.Resolution)
	if err != nil || actualQuota < 0 {
		return errors.New("invalid GeminiVeo settlement quota")
	}
	encryptedProviderID, err := asyncTaskEncryptBound(
		providerTaskID,
		geminiVeoProviderTaskBinding(task.TaskID, reservation.ReservationID(), task.Platform, task.UserId, task.ChannelId),
	)
	if err != nil {
		return err
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	descriptor, ok := veoTaskProviderByPlatform(task.Platform)
	if !ok || descriptor.Family != properties.Family ||
		descriptor.ValidateRoutingSnapshot(privateData.RoutingSnapshot, properties.UpstreamModelName) != nil ||
		descriptor.ValidateProviderTaskID(privateData.RoutingSnapshot, properties.UpstreamModelName, providerTaskID) != nil {
		return errors.New("invalid Veo provider identity snapshot")
	}
	if _, err := validateGeminiVeoTaskPricingSnapshot(task, privateData, actualQuota); err != nil {
		return err
	}
	privateData.EncryptedProviderTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateJSON, err := marshalGeminiVeoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	err = reservation.SettleWithChannelAndPersistence(actualQuota, task.ChannelId, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, task.Platform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservation.ReservationID(), task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !geminiVeoTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("accepted GeminiVeo task identity mismatch")
		}
		currentPrivate, currentPrivateErr := decodeGeminiVeoTaskPrivateData(currentTask.PrivateData)
		if currentPrivateErr != nil {
			return currentPrivateErr
		}
		if _, snapshotErr := validateGeminiVeoTaskPricingSnapshot(&currentTask, currentPrivate, actualQuota); snapshotErr != nil {
			return snapshotErr
		}
		if operation.State == model.TaskOperationSubmitted && !operation.SettlementPending &&
			operation.EncryptedProviderTaskID != "" && currentTask.Status == model.TaskStatusSubmitted {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				geminiVeoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.Platform,
					operation.UserID, operation.ChannelID))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("GeminiVeo provider task id conflicts with durable state"), decryptErr)
			}
			audit, auditErr := newGeminiVeoConsumeAudit(currentTask, privateData,
				reservation.ReservationID(), actualQuota)
			if auditErr != nil {
				return auditErr
			}
			return service.EnqueueAuditLogTx(tx, audit)
		}
		if operation.State != expectedState || operation.LeaseOwner != leaseOwner ||
			(currentTask.Status != model.TaskStatusNotStart && currentTask.Status != model.TaskStatusSubmitted) {
			return service.ErrRelayQuotaReservationBusy
		}
		if operation.EncryptedProviderTaskID != "" {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				geminiVeoProviderTaskBinding(task.TaskID, operation.ReservationID, operation.Platform,
					operation.UserID, operation.ChannelID))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("GeminiVeo provider task id conflicts with durable state"), decryptErr)
			}
			encryptedProviderID = operation.EncryptedProviderTaskID
			privateData.EncryptedProviderTaskID = encryptedProviderID
			privateJSON, err = marshalGeminiVeoTaskPrivateData(privateData)
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
				"next_attempt_at":            now + geminiVeoOperationRetrySeconds, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		auditTask := currentTask
		auditTask.Quota = actualQuota
		audit, auditErr := newGeminiVeoConsumeAudit(auditTask, privateData,
			reservation.ReservationID(), actualQuota)
		if auditErr != nil {
			return auditErr
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
	if err == nil || geminiVeoAcceptedSettlementMatches(task.TaskID, reservation.ReservationID(), task.Platform, providerTaskID) {
		return model.DB.Where("id = ?", task.ID).First(task).Error
	}
	return err
}

func persistAcceptedGeminiVeoFallback(task *model.Task, reservationID, providerTaskID string) error {
	if task == nil || reservationID == "" || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted GeminiVeo fallback")
	}
	encryptedProviderID, err := asyncTaskEncryptBound(providerTaskID,
		geminiVeoProviderTaskBinding(task.TaskID, reservationID, task.Platform, task.UserId, task.ChannelId))
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, task.Platform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !geminiVeoTaskOperationIdentityMatches(&currentTask, &operation) || operation.LeaseOwner != "" ||
			(operation.State != model.TaskOperationDispatching && operation.State != model.TaskOperationSubmitted &&
				operation.State != model.TaskOperationUnknown && operation.State != model.TaskOperationManualReview) {
			return service.ErrRelayQuotaReservationBusy
		}
		if operation.EncryptedProviderTaskID != "" {
			stored, decryptErr := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
				geminiVeoProviderTaskBinding(task.TaskID, reservationID, task.Platform, task.UserId, task.ChannelId))
			if decryptErr != nil || stored != providerTaskID {
				return errors.Join(errors.New("GeminiVeo provider task id conflicts with durable state"), decryptErr)
			}
			encryptedProviderID = operation.EncryptedProviderTaskID
		}
		privateData, err := decodeGeminiVeoTaskPrivateData(currentTask.PrivateData)
		if err != nil {
			return err
		}
		properties, err := validateGeminiVeoTaskPricingSnapshot(&currentTask, privateData, currentTask.Quota)
		if err != nil {
			return err
		}
		descriptor, ok := veoTaskProviderByPlatform(currentTask.Platform)
		if !ok || descriptor.Family != properties.Family ||
			descriptor.ValidateProviderTaskID(privateData.RoutingSnapshot,
				properties.UpstreamModelName, providerTaskID) != nil {
			return errors.New("invalid Veo fallback provider identity")
		}
		privateData.EncryptedProviderTaskID = encryptedProviderID
		privateData.SettlementPending = true
		privateJSON, err := marshalGeminiVeoTaskPrivateData(privateData)
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
	if err == nil || geminiVeoAcceptedFallbackMatches(task.TaskID, reservationID, task.Platform, providerTaskID) {
		return nil
	}
	return err
}

func markGeminiVeoAmbiguousDispatch(task *model.Task, reservationID, reason string) error {
	return markGeminiVeoAmbiguousDispatchWithLease(task, reservationID, reason, "")
}

// markGeminiVeoAmbiguousDispatch fences a request that may have reached GeminiVeo but
// returned no durable provider id. The dispatched hold is deliberately kept:
// replaying could duplicate a paid task, while settling would charge an
// outcome that cannot be verified. An operator must explicitly settle/refund.
func markGeminiVeoAmbiguousDispatchWithLease(task *model.Task, reservationID, reason, leaseOwner string) error {
	if task == nil || task.ID <= 0 || reservationID == "" {
		return errors.New("invalid unknown GeminiVeo dispatch")
	}
	reason = boundedGeminiVeoFailReason(reason)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, task.Platform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
			reservationID, task.Platform).First(&operation).Error; err != nil {
			return err
		}
		if !geminiVeoTaskOperationIdentityMatches(&currentTask, &operation) {
			return errors.New("unknown GeminiVeo task identity mismatch")
		}
		if operation.State == model.TaskOperationManualReview && operation.SettlementPending &&
			currentTask.Status == model.TaskStatusUnknown {
			return nil
		}
		if operation.State != model.TaskOperationDispatching || operation.LeaseOwner != leaseOwner ||
			operation.EncryptedProviderTaskID != "" || currentTask.Status != model.TaskStatusNotStart {
			return service.ErrRelayQuotaReservationBusy
		}
		var reservation model.RelayQuotaReservationRecord
		if err := tx.Where("reservation_id = ? AND user_id = ?", reservationID, task.UserId).
			First(&reservation).Error; err != nil {
			return err
		}
		if reservation.Status != model.RelayQuotaReservationStatusDispatched {
			return service.ErrRelayQuotaReservationBusy
		}
		privateData, err := decodeGeminiVeoTaskPrivateData(currentTask.PrivateData)
		if err != nil {
			return err
		}
		privateData.SettlementPending = true
		privateJSON, err := marshalGeminiVeoTaskPrivateData(privateData)
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
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		result = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID,
				model.TaskOperationDispatching, leaseOwner).
			Updates(map[string]any{"state": model.TaskOperationManualReview,
				"settlement_pending": true, "next_attempt_at": 0, "completed_at": now,
				"updated_at": now, "last_error": reason, "lease_owner": "", "lease_expires_at": 0})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, service.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || geminiVeoOperationStateMatches(task.TaskID, reservationID, task.Platform, model.TaskOperationManualReview) {
		return nil
	}
	return err
}

func reverseFailedGeminiVeoTask(task *model.Task, operation *model.TaskOperation, reason string, data string) error {
	if task == nil || operation == nil || operation.ReservationID == "" {
		return errors.New("invalid failed GeminiVeo task reversal")
	}
	reason = boundedGeminiVeoFailReason(reason)
	return service.ReverseSettledRelayQuotaReservationWithPersistence(operation.ReservationID, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID, task.Platform).
			First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND reservation_id = ? AND platform = ?", operation.ID,
			operation.ReservationID, operation.Platform).First(&currentOperation).Error; err != nil {
			return err
		}
		if !geminiVeoTaskOperationIdentityMatches(&currentTask, &currentOperation) {
			return errors.New("failed GeminiVeo task identity mismatch")
		}
		if currentOperation.State == model.TaskOperationReversed && currentTask.Status == model.TaskStatusFailure && currentTask.Quota == 0 {
			audit, auditErr := newGeminiVeoRefundAudit(tx, currentTask, operation.ReservationID,
				currentTask.FailReason, currentTask.FinishTime)
			if auditErr != nil {
				return auditErr
			}
			return service.EnqueueAuditLogTx(tx, audit)
		}
		if currentOperation.State != model.TaskOperationSubmitted || currentOperation.LeaseOwner != operation.LeaseOwner {
			return service.ErrRelayQuotaReservationBusy
		}
		switch currentTask.Status {
		case model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusFailure:
		default:
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
		audit, auditErr := newGeminiVeoRefundAudit(tx, currentTask, operation.ReservationID, reason, now)
		if auditErr != nil {
			return auditErr
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
}

func geminiVeoTaskOperationIdentityMatches(task *model.Task, operation *model.TaskOperation) bool {
	if !isGeminiVeoTask(task) || operation == nil || operation.Platform != task.Platform ||
		operation.TaskID != task.TaskID || operation.UserID != task.UserId || operation.ChannelID != task.ChannelId {
		return false
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	return err == nil && privateData.RelayReservationID == operation.ReservationID
}

func geminiVeoOperationStateMatches(taskID, reservationID, platform, state string) bool {
	var operation model.TaskOperation
	return model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, platform, state).First(&operation).Error == nil
}

func geminiVeoAcceptedSettlementMatches(taskID, reservationID, platform, providerID string) bool {
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, platform).
		First(&task).Error; err != nil || task.Status != model.TaskStatusSubmitted {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, platform).First(&operation).Error; err != nil ||
		operation.State != model.TaskOperationSubmitted || operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" ||
		!geminiVeoTaskOperationIdentityMatches(&task, &operation) {
		return false
	}
	stored, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		geminiVeoProviderTaskBinding(taskID, reservationID, operation.Platform, operation.UserID, operation.ChannelID))
	if err != nil || stored != providerID {
		return false
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.SettlementPending ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return false
	}
	if _, err := validateGeminiVeoTaskPricingSnapshot(&task, privateData, task.Quota); err != nil {
		return false
	}
	properties, propertiesErr := decodeGeminiVeoTaskProperties(task.Properties)
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
		Where("event_id = ?", geminiVeoAuditEventID(reservationID)).Count(&auditCount).Error; err != nil {
		return false
	}
	return auditCount == 1
}

func geminiVeoAcceptedFallbackMatches(taskID, reservationID, platform, providerID string) bool {
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", taskID, platform).
		First(&task).Error; err != nil || task.Status != model.TaskStatusSubmitted {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, platform).First(&operation).Error; err != nil ||
		operation.State != model.TaskOperationSubmitted || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" ||
		!geminiVeoTaskOperationIdentityMatches(&task, &operation) {
		return false
	}
	stored, err := asyncTaskDecryptBound(operation.EncryptedProviderTaskID,
		geminiVeoProviderTaskBinding(taskID, reservationID, operation.Platform, operation.UserID, operation.ChannelID))
	if err != nil || stored != providerID {
		return false
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
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

func isGeminiVeoTerminalStatus(status string) bool {
	return status == model.TaskStatusSuccess || status == model.TaskStatusFailure || status == model.TaskStatusUnknown
}

func loadGeminiVeoTaskOperation(taskID string) (*model.TaskOperation, error) {
	var operations []model.TaskOperation
	if err := model.DB.Where("task_id = ? AND platform IN ?", taskID, model.VeoTaskOperationPlatforms()).
		Limit(2).Find(&operations).Error; err != nil {
		return nil, err
	}
	if len(operations) != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return &operations[0], nil
}

func ensureGeminiVeoPrivateProviderCipher(task *model.Task, operation *model.TaskOperation) error {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" {
		return errors.New("GeminiVeo provider task id is unavailable")
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	if privateData.EncryptedProviderTaskID == "" {
		privateData.EncryptedProviderTaskID = operation.EncryptedProviderTaskID
	}
	if privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID {
		return fmt.Errorf("GeminiVeo provider task ciphertext mismatch")
	}
	return nil
}

func newGeminiVeoConsumeAudit(task model.Task, privateData geminiVeoTaskPrivateData,
	reservationID string, quota int) (*model.Log, error) {
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil || reservationID == "" || quota < 0 {
		return nil, errors.New("invalid GeminiVeo consume audit")
	}
	eventID := geminiVeoAuditEventID(reservationID)
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
	encoded, err := common.Marshal(other)
	if err != nil || len(eventID) > 64 {
		return nil, errors.New("invalid GeminiVeo consume audit")
	}
	return &model.Log{AuditEventId: &eventID, UserId: task.UserId, CreatedAt: task.CreatedAt,
		Type: service.LogTypeConsume, ModelName: properties.OriginModelName, Quota: quota,
		ChannelId: task.ChannelId, TokenId: privateData.TokenID, Group: task.Group,
		Other: string(encoded)}, nil
}

func newGeminiVeoRefundAudit(tx *gorm.DB, task model.Task, reservationID, reason string,
	createdAt int64) (*model.Log, error) {
	if tx == nil || reservationID == "" || createdAt <= 0 {
		return nil, errors.New("invalid GeminiVeo refund audit")
	}
	properties, err := decodeGeminiVeoTaskProperties(task.Properties)
	if err != nil {
		return nil, err
	}
	var record model.RelayQuotaReservationRecord
	if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	eventID := "geminiVeo-refund:" + reservationID
	other, err := common.Marshal(map[string]any{"relay_reservation_id": reservationID,
		"task_id": task.TaskID, "task_platform": task.Platform,
		"reason": boundedGeminiVeoFailReason(reason)})
	if err != nil || len(eventID) > 64 {
		return nil, errors.New("invalid GeminiVeo refund audit")
	}
	return &model.Log{AuditEventId: &eventID, UserId: task.UserId, CreatedAt: createdAt,
		Type: service.LogTypeRefund, ModelName: properties.OriginModelName, Quota: record.ActualQuota,
		ChannelId: task.ChannelId, TokenId: record.TokenID, Group: task.Group,
		Other: string(other)}, nil
}
