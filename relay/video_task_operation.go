package relay

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/sora"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	videoOperationLeaseSeconds     = int64(90)
	videoOperationRetrySeconds     = int64(45)
	videoDispatchRecoverySeconds   = int64(120)
	videoReservationSafetySeconds  = int64(8 * 24 * time.Hour / time.Second)
	videoOperationBatchSize        = 100
	videoOperationMaxAttempts      = 20_000
	videoOperationMaxPollDays      = 7
	videoUnknownDispatchReason     = "provider submission outcome requires manual review"
	videoPollingManualReviewReason = "provider task exceeded automatic polling horizon"
)

func createVideoReservedTask(
	task *model.Task,
	token *model.Token,
	privateData *videoTaskPrivateData,
) (*service.RelayQuotaReservation, error) {
	if task == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 || task.ChannelId <= 0 {
		return nil, errors.New("invalid video task reservation")
	}
	if !validVideoTaskPlatform(task.Platform) {
		return nil, errors.New("invalid video task platform")
	}
	if privateData.FreeModel && task.Quota != 0 {
		return nil, errors.New("free-model video task requires zero quota")
	}
	return service.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, privateData.FreeModel,
		func(tx *gorm.DB, creation service.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalVideoTaskPrivateData(*privateData)
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
			minimumExpiry := now + videoReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: task.Platform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + videoOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markVideoTaskDispatching(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
) error {
	if task == nil || reservation == nil || task.ID <= 0 || task.ChannelId <= 0 {
		return errors.New("invalid video dispatch transition")
	}
	var now int64
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		var clockErr error
		now, clockErr = model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		minimumExpiry := now + videoReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, model.TaskStatusNotStart).
			Updates(map[string]any{"updated_at": now})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			var current model.Task
			if err := tx.First(&current, task.ID).Error; err != nil ||
				current.TaskID != task.TaskID || current.Status != model.TaskStatusNotStart {
				return errors.New("video task changed before dispatch")
			}
		}
		operationResult := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), task.Platform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + videoDispatchRecoverySeconds,
				"updated_at":      now, "last_error": "",
			})
		if operationResult.Error != nil {
			return operationResult.Error
		}
		if operationResult.RowsAffected != 1 {
			return errors.New("video recovery marker changed before dispatch")
		}
		return nil
	})
	if err != nil && !videoDispatchTransitionMatches(task.TaskID, reservation.ReservationID()) {
		return err
	}
	task.UpdatedAt = now
	return nil
}

func videoDispatchTransitionMatches(taskID, reservationID string) bool {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil {
		return false
	}
	if !validVideoTaskPlatform(operation.Platform) || operation.State != model.TaskOperationDispatching ||
		!operation.SettlementPending || operation.LeaseOwner != "" {
		return false
	}
	var task model.Task
	return model.DB.Where("task_id = ? AND platform = ?", taskID, operation.Platform).First(&task).Error == nil &&
		isVideoTaskPlatform(&task)
}

func refundRejectedVideoTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	reason string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid rejected video task refund")
	}
	reason = boundedVideoFailReason(reason)
	err := reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservation.ReservationID()).
			First(&currentOperation).Error; err != nil {
			return err
		}
		if !videoTaskOperationIdentityMatches(&currentTask, &currentOperation, reservation.ReservationID()) {
			return errors.New("accepted video settlement identity mismatch")
		}
		if !videoTaskOperationIdentityMatches(&currentTask, &currentOperation, reservation.ReservationID()) {
			return errors.New("rejected video refund identity mismatch")
		}
		if currentOperation.State == model.TaskOperationRefunded {
			if currentTask.Status != model.TaskStatusFailure || currentTask.Quota != 0 ||
				currentTask.Progress != "100%" || currentTask.FinishTime <= 0 ||
				currentOperation.SettlementPending || currentOperation.EncryptedProviderTaskID != "" ||
				currentOperation.LeaseOwner != "" {
				return service.ErrRelayQuotaReservationBusy
			}
			return nil
		}
		if (currentOperation.State != model.TaskOperationPrepared &&
			currentOperation.State != model.TaskOperationDispatching) || currentOperation.LeaseOwner != "" ||
			currentTask.Status != model.TaskStatusNotStart {
			return service.ErrRelayQuotaReservationBusy
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, currentTask.Status).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"progress": "100%", "finish_time": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?",
				currentOperation.ID, task.TaskID, reservation.ReservationID(), currentOperation.State, "").
			Updates(map[string]any{
				"state": model.TaskOperationRefunded, "settlement_pending": false,
				"encrypted_provider_task_id": "", "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil && !videoRefundTransitionMatches(task.TaskID, reservation.ReservationID()) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func videoRefundTransitionMatches(taskID, reservationID string) bool {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusRefunded {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil {
		return false
	}
	if operation.State != model.TaskOperationRefunded || operation.SettlementPending || operation.LeaseOwner != "" {
		return false
	}
	var task model.Task
	if model.DB.Where("task_id = ? AND status = ? AND quota = ?", taskID, model.TaskStatusFailure, 0).
		First(&task).Error != nil {
		return false
	}
	return videoTaskOperationIdentityMatches(&task, &operation, reservationID)
}

func settleAcceptedVideoTask(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	provider *sora.Response,
	providerTaskID string,
	expectedOperationState, leaseOwner string,
) error {
	if task == nil || reservation == nil || provider == nil || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted video task settlement")
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	encryptedProviderID, err := asyncTaskEncryptBound(
		providerTaskID,
		videoProviderTaskBinding(task.TaskID, reservation.ReservationID(), task.UserId, task.ChannelId),
	)
	if err != nil {
		return err
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	privateData.EncryptedUpstreamTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateJSON, err := marshalVideoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	status, recognized := providerVideoTaskStatus(provider.Status)
	if !recognized {
		status = model.TaskStatusSubmitted
	}
	failReason := ""
	if status == model.TaskStatusFailure {
		failReason = "video task failed"
		if provider.Error != nil {
			failReason = boundedVideoFailReason(provider.Error.Message)
		}
	}
	response := sanitizedVideoResponse(
		task.TaskID, properties, provider, status, failReason, task.CreatedAt, common.NowTimestamp(),
	)
	responseData, err := common.Marshal(response)
	if err != nil || len(responseData) > videoTaskProviderPayloadMaxBytes {
		return errors.New("video provider response is too large")
	}
	operationState := model.TaskOperationSubmitted
	completedAt := int64(0)
	nextAttemptAt := int64(0)
	if status == model.TaskStatusSuccess {
		operationState = model.TaskOperationTerminal
	}
	if expectedOperationState != model.TaskOperationDispatching && expectedOperationState != model.TaskOperationSubmitted &&
		expectedOperationState != model.TaskOperationManualReview {
		return errors.New("invalid expected video operation state")
	}
	var now int64
	err = reservation.SettleWithChannelAndPersistence(task.Quota, task.ChannelId, func(tx *gorm.DB) error {
		var clockErr error
		now, clockErr = model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		if operationState == model.TaskOperationTerminal {
			completedAt = now
		} else {
			nextAttemptAt = now
			if status != model.TaskStatusFailure {
				nextAttemptAt = now + videoOperationRetrySeconds
			}
		}
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservation.ReservationID()).
			First(&currentOperation).Error; err != nil {
			return err
		}
		if currentTask.Status == status && currentTask.PrivateData == privateJSON &&
			currentTask.Data == string(responseData) && currentOperation.State == operationState &&
			!currentOperation.SettlementPending &&
			currentOperation.EncryptedProviderTaskID == encryptedProviderID {
			audit, auditErr := newVideoConsumeAudit(currentTask, privateData, reservation.ReservationID(), "accepted")
			if auditErr != nil {
				return auditErr
			}
			return service.EnqueueAuditLogTx(tx, audit)
		}
		allowedTask := currentTask.Status == model.TaskStatusNotStart ||
			(currentOperation.SettlementPending &&
				(currentTask.Status == model.TaskStatusSubmitted || currentTask.Status == status))
		if !allowedTask {
			return fmt.Errorf("video task is already in status %s", currentTask.Status)
		}
		taskUpdates := map[string]any{
			"private_data": privateJSON, "data": string(responseData),
			"status": status, "fail_reason": failReason,
			"progress": videoTaskProgress(status, provider.Progress), "updated_at": now,
		}
		if status == model.TaskStatusRunning && currentTask.StartTime == 0 {
			taskUpdates["start_time"] = now
		}
		if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
			taskUpdates["finish_time"] = now
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, currentTask.Status).
			Updates(taskUpdates)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opQuery := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID, expectedOperationState, leaseOwner)
		opResult := opQuery.Updates(map[string]any{
			"state": operationState, "settlement_pending": false,
			"encrypted_provider_task_id": encryptedProviderID,
			"next_attempt_at":            nextAttemptAt, "completed_at": completedAt,
			"updated_at": now, "last_error": "",
			"lease_owner": "", "lease_expires_at": 0,
		})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		audit, auditErr := newVideoConsumeAudit(currentTask, privateData, reservation.ReservationID(), "accepted")
		if auditErr != nil {
			return auditErr
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
	if err != nil {
		if !videoAcceptedSettlementMatches(
			task.TaskID, reservation.ReservationID(), status, providerTaskID, task.Quota, task.ChannelId,
		) {
			return err
		}
		return model.DB.First(task, task.ID).Error
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func settleUnknownVideoDispatch(
	task *model.Task,
	reservation *service.RelayQuotaReservation,
	reason string,
	expectedOperationState, leaseOwner string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid unknown video dispatch settlement")
	}
	if expectedOperationState != model.TaskOperationDispatching {
		return errors.New("invalid unknown video operation state")
	}
	reason = boundedVideoFailReason(reason)
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	if privateData.EncryptedUpstreamTaskID != "" {
		return errors.New("unknown video dispatch already has a provider id")
	}
	privateData.SettlementPending = false
	privateJSON, err := marshalVideoTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	var now int64
	err = reservation.SettleWithChannelAndPersistence(task.Quota, task.ChannelId, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservation.ReservationID()).
			First(&currentOperation).Error; err != nil {
			return err
		}
		if !videoTaskOperationIdentityMatches(&currentTask, &currentOperation, reservation.ReservationID()) {
			return errors.New("unknown video settlement identity mismatch")
		}
		if currentOperation.State == model.TaskOperationUnknown {
			currentPrivateData, decodeErr := decodeVideoTaskPrivateData(currentTask.PrivateData)
			if decodeErr != nil || currentTask.Status != model.TaskStatusUnknown ||
				currentTask.Progress != "100%" || currentTask.FinishTime <= 0 ||
				currentOperation.SettlementPending || currentOperation.EncryptedProviderTaskID != "" ||
				currentOperation.LeaseOwner != "" || currentPrivateData.SettlementPending ||
				currentPrivateData.EncryptedUpstreamTaskID != "" {
				return errors.Join(service.ErrRelayQuotaReservationBusy, decodeErr)
			}
			audit, auditErr := newVideoConsumeAudit(
				currentTask, currentPrivateData, reservation.ReservationID(), "unknown",
			)
			if auditErr != nil {
				return auditErr
			}
			return service.EnqueueAuditLogTx(tx, audit)
		}
		if currentOperation.State != expectedOperationState || currentOperation.LeaseOwner != leaseOwner ||
			currentOperation.EncryptedProviderTaskID != "" || currentTask.Status != model.TaskStatusNotStart {
			return service.ErrRelayQuotaReservationBusy
		}
		var clockErr error
		now, clockErr = model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		response := sanitizedVideoResponse(
			task.TaskID, properties, nil, model.TaskStatusUnknown, reason, currentTask.CreatedAt, now,
		)
		response.Error = &sora.ResponseError{Message: reason, Code: "submit_outcome_unknown"}
		data, marshalErr := common.Marshal(response)
		if marshalErr != nil || len(data) > videoTaskProviderPayloadMaxBytes {
			return errors.New("video provider response is too large")
		}
		taskResult := tx.Model(&model.Task{}).Where("id = ? AND task_id = ? AND status = ?",
			task.ID, task.TaskID, currentTask.Status).
			Updates(map[string]any{
				"private_data": privateJSON, "data": string(data), "status": model.TaskStatusUnknown,
				"fail_reason": reason, "progress": "100%", "finish_time": now, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?",
				currentOperation.ID, task.TaskID, reservation.ReservationID(), expectedOperationState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationUnknown, "settlement_pending": false,
				"encrypted_provider_task_id": "", "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": reason,
				"lease_owner": "", "lease_expires_at": 0,
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		audit, auditErr := newVideoConsumeAudit(currentTask, privateData, reservation.ReservationID(), "unknown")
		if auditErr != nil {
			return auditErr
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
	if err != nil {
		if !videoUnknownSettlementMatches(
			task.TaskID, reservation.ReservationID(), task.Quota, task.ChannelId,
		) {
			return err
		}
		return model.DB.First(task, task.ID).Error
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func persistAcceptedVideoFallback(
	task *model.Task,
	reservationID, providerTaskID string,
	provider *sora.Response,
) error {
	if task == nil || reservationID == "" || provider == nil || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted video fallback")
	}
	providerTaskID = strings.TrimSpace(providerTaskID)
	encryptedProviderID, err := asyncTaskEncryptBound(
		providerTaskID,
		videoProviderTaskBinding(task.TaskID, reservationID, task.UserId, task.ChannelId),
	)
	if err != nil {
		return err
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	status, _ := providerVideoTaskStatus(provider.Status)
	if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
		// Accounting must commit before a terminal provider outcome can be acted on.
		status = model.TaskStatusSubmitted
	}
	response := sanitizedVideoResponse(
		task.TaskID, properties, provider, status, "", task.CreatedAt, common.NowTimestamp(),
	)
	data, err := common.Marshal(response)
	if err != nil || len(data) > videoTaskProviderPayloadMaxBytes {
		return errors.New("video provider response is too large")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	privateJSON := ""
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		if currentTask.Status != model.TaskStatusNotStart && currentTask.Status != model.TaskStatusSubmitted {
			return fmt.Errorf("video task is already in status %s", currentTask.Status)
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("task_id = ? AND reservation_id = ?", task.TaskID, reservationID).
			First(&currentOperation).Error; err != nil {
			return err
		}
		if !videoTaskOperationIdentityMatches(&currentTask, &currentOperation, reservationID) {
			return errors.New("accepted video fallback identity mismatch")
		}
		if currentOperation.LeaseOwner != "" ||
			(currentOperation.State != model.TaskOperationDispatching &&
				currentOperation.State != model.TaskOperationSubmitted) {
			return service.ErrRelayQuotaReservationBusy
		}
		if currentOperation.EncryptedProviderTaskID != "" {
			storedProviderID, decryptErr := asyncTaskDecryptBound(
				currentOperation.EncryptedProviderTaskID,
				videoProviderTaskBinding(task.TaskID, reservationID, currentOperation.UserID, currentOperation.ChannelID),
			)
			if decryptErr != nil || storedProviderID != providerTaskID {
				return errors.Join(errors.New("accepted video provider id conflicts with durable recovery state"), decryptErr)
			}
			// AES-GCM uses a random nonce. Reuse the committed ciphertext for an
			// idempotent replay instead of manufacturing a different durable value.
			encryptedProviderID = currentOperation.EncryptedProviderTaskID
		}
		privateData.EncryptedUpstreamTaskID = encryptedProviderID
		privateData.SettlementPending = true
		privateJSON, err = marshalVideoTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		progress := videoTaskProgress(model.TaskStatusSubmitted, provider.Progress)
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, currentTask.Status).
			Updates(map[string]any{
				"private_data": privateJSON, "data": string(data), "status": model.TaskStatusSubmitted,
				"progress": progress, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 &&
			(currentTask.PrivateData != privateJSON || currentTask.Data != string(data) ||
				currentTask.Status != model.TaskStatusSubmitted || currentTask.Progress != progress) {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND settlement_pending = ? AND encrypted_provider_task_id = ? AND lease_owner = ?",
				currentOperation.ID, currentOperation.State, currentOperation.SettlementPending,
				currentOperation.EncryptedProviderTaskID, "").
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": true,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            now, "updated_at": now, "last_error": "",
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected == 0 &&
			(currentOperation.State != model.TaskOperationSubmitted || !currentOperation.SettlementPending ||
				currentOperation.EncryptedProviderTaskID != encryptedProviderID) {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil {
		if !videoAcceptedFallbackMatches(task.TaskID, reservationID, providerTaskID) {
			return err
		}
		return model.DB.First(task, task.ID).Error
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func videoAcceptedFallbackMatches(taskID, reservationID, providerTaskID string) bool {
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil || operation.State != model.TaskOperationSubmitted ||
		!operation.SettlementPending || operation.LeaseOwner != "" || operation.EncryptedProviderTaskID == "" {
		return false
	}
	storedProviderID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		videoProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID),
	)
	if err != nil || storedProviderID != providerTaskID {
		return false
	}
	var task model.Task
	if err := model.DB.Where("task_id = ?", taskID).First(&task).Error; err != nil ||
		task.Status != model.TaskStatusSubmitted {
		return false
	}
	if !videoTaskOperationIdentityMatches(&task, &operation, reservationID) {
		return false
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || !privateData.SettlementPending || privateData.EncryptedUpstreamTaskID == "" {
		return false
	}
	privateProviderID, err := asyncTaskDecryptBound(
		privateData.EncryptedUpstreamTaskID,
		videoProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID),
	)
	return err == nil && privateProviderID == providerTaskID
}

func reverseFailedVideoTask(
	task *model.Task,
	operation *model.TaskOperation,
	provider *sora.Response,
	reason string,
) error {
	if task == nil || operation == nil || operation.ReservationID == "" {
		return errors.New("invalid failed video task reversal")
	}
	reason = boundedVideoFailReason(reason)
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	err = service.ReverseSettledRelayQuotaReservationWithPersistence(operation.ReservationID, func(tx *gorm.DB) error {
		var currentTask model.Task
		if err := tx.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(&currentTask).Error; err != nil {
			return err
		}
		var currentOperation model.TaskOperation
		if err := tx.Where("id = ? AND task_id = ? AND reservation_id = ?", operation.ID,
			task.TaskID, operation.ReservationID).First(&currentOperation).Error; err != nil {
			return err
		}
		if !videoTaskOperationIdentityMatches(&currentTask, &currentOperation, operation.ReservationID) {
			return errors.New("video reversal identity mismatch")
		}
		if currentOperation.State == model.TaskOperationReversed {
			if currentTask.Status != model.TaskStatusFailure || currentTask.Quota != 0 ||
				currentTask.Progress != "100%" || currentTask.FinishTime <= 0 ||
				currentOperation.SettlementPending || currentOperation.LeaseOwner != "" {
				return service.ErrRelayQuotaReservationBusy
			}
			audit, auditErr := newVideoRefundAudit(
				tx, currentTask, operation.ReservationID, currentTask.FailReason, currentTask.FinishTime,
			)
			if auditErr != nil {
				return auditErr
			}
			return service.EnqueueAuditLogTx(tx, audit)
		}
		if currentOperation.State != model.TaskOperationSubmitted ||
			(operation.LeaseOwner != "" && currentOperation.LeaseOwner != operation.LeaseOwner) {
			return service.ErrRelayQuotaReservationBusy
		}
		switch currentTask.Status {
		case model.TaskStatusSubmitted, model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusFailure:
		default:
			return fmt.Errorf("video task is already in status %s", currentTask.Status)
		}
		now, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		responseTime := task.FinishTime
		if responseTime <= 0 {
			responseTime = now
		}
		response := sanitizedVideoResponse(
			task.TaskID, properties, provider, model.TaskStatusFailure, reason, task.CreatedAt, responseTime,
		)
		encoded, marshalErr := common.Marshal(response)
		if marshalErr != nil || len(encoded) > videoTaskProviderPayloadMaxBytes {
			return errors.New("video provider response is too large")
		}
		data := encoded
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND status = ?", task.ID, task.TaskID, currentTask.Status).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "fail_reason": reason, "quota": 0,
				"data": string(data), "progress": "100%", "finish_time": now, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 0 {
			if currentTask.Status != model.TaskStatusFailure || currentTask.Quota != 0 ||
				currentTask.FailReason != reason || currentTask.Data != string(data) ||
				currentTask.Progress != "100%" || currentTask.FinishTime != now {
				return errors.New("video task changed before reversal")
			}
		}
		opQuery := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ?", operation.ID,
				task.TaskID, operation.ReservationID, model.TaskOperationSubmitted, currentOperation.LeaseOwner)
		opResult := opQuery.
			Updates(map[string]any{
				"state": model.TaskOperationReversed, "settlement_pending": false,
				"encrypted_provider_task_id": "", "next_attempt_at": 0,
				"completed_at": now, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		audit, auditErr := newVideoRefundAudit(tx, currentTask, operation.ReservationID, reason, now)
		if auditErr != nil {
			return auditErr
		}
		return service.EnqueueAuditLogTx(tx, audit)
	})
	if err != nil && !videoReverseTransitionMatches(task.TaskID, operation.ReservationID) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func videoAcceptedSettlementMatches(
	taskID, reservationID, status, providerTaskID string,
	quota, channelID int,
) bool {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != quota || record.ChannelID != channelID {
		return false
	}
	operationState := model.TaskOperationSubmitted
	if status == model.TaskStatusSuccess {
		operationState = model.TaskOperationTerminal
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil || operation.State != operationState ||
		operation.SettlementPending || operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" {
		return false
	}
	storedProviderID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		videoProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID),
	)
	if err != nil || storedProviderID != providerTaskID {
		return false
	}
	var task model.Task
	if err := model.DB.Where("task_id = ?", taskID).First(&task).Error; err != nil || task.Status != status {
		return false
	}
	if !videoTaskOperationIdentityMatches(&task, &operation, reservationID) {
		return false
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.SettlementPending || privateData.EncryptedUpstreamTaskID == "" {
		return false
	}
	privateProviderID, err := asyncTaskDecryptBound(
		privateData.EncryptedUpstreamTaskID,
		videoProviderTaskBinding(taskID, reservationID, operation.UserID, operation.ChannelID),
	)
	return err == nil && privateProviderID == providerTaskID && videoAuditEventExists("video:"+reservationID)
}

func videoUnknownSettlementMatches(taskID, reservationID string, quota, channelID int) bool {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != quota || record.ChannelID != channelID {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil || operation.State != model.TaskOperationUnknown ||
		operation.SettlementPending || operation.EncryptedProviderTaskID != "" || operation.LeaseOwner != "" {
		return false
	}
	var task model.Task
	if err := model.DB.Where("task_id = ?", taskID).First(&task).Error; err != nil ||
		task.Status != model.TaskStatusUnknown || task.Progress != "100%" || task.FinishTime <= 0 ||
		!videoTaskOperationIdentityMatches(&task, &operation, reservationID) {
		return false
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.SettlementPending || privateData.EncryptedUpstreamTaskID != "" {
		return false
	}
	return videoAuditEventExists("video:" + reservationID)
}

func videoReverseTransitionMatches(taskID, reservationID string) bool {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil ||
		record.Status != model.RelayQuotaReservationStatusReversed {
		return false
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ?", taskID, reservationID).
		First(&operation).Error; err != nil || operation.State != model.TaskOperationReversed ||
		operation.SettlementPending || operation.LeaseOwner != "" {
		return false
	}
	var task model.Task
	if model.DB.Where("task_id = ? AND status = ? AND quota = ?", taskID, model.TaskStatusFailure, 0).
		First(&task).Error != nil || !videoTaskOperationIdentityMatches(&task, &operation, reservationID) {
		return false
	}
	return videoAuditEventExists("video-refund:" + reservationID)
}

func videoAuditEventExists(eventID string) bool {
	var count int64
	return model.DB.Model(&model.AuditLogOutbox{}).Where("event_id = ?", eventID).Limit(1).
		Count(&count).Error == nil && count == 1
}

func newVideoConsumeAudit(
	task model.Task,
	privateData videoTaskPrivateData,
	reservationID string,
	providerOutcome string,
) (*model.Log, error) {
	if task.TaskID == "" || reservationID == "" {
		return nil, errors.New("invalid video consume audit identity")
	}
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil {
		return nil, err
	}
	eventID := "video:" + reservationID
	fields := map[string]any{
		"billing_source":           privateData.BillingSource,
		"provider_outcome":         providerOutcome,
		"relay_reservation_id":     reservationID,
		"task_id":                  task.TaskID,
		"task_platform":            task.Platform,
		"seconds":                  properties.Seconds,
		"size":                     properties.Size,
		"subscription_id":          privateData.SubscriptionID,
		"subscription_usage_epoch": privateData.FundingUsageEpoch,
	}
	if privateData.FreeModel {
		fields["free_model"] = true
	}
	other, err := common.Marshal(fields)
	if err != nil || len(eventID) > 64 {
		return nil, errors.New("invalid video consume audit")
	}
	return &model.Log{
		AuditEventId: &eventID, UserId: task.UserId, CreatedAt: task.CreatedAt,
		Type: service.LogTypeConsume, ModelName: properties.OriginModelName,
		Quota: task.Quota, ChannelId: task.ChannelId, TokenId: privateData.TokenID,
		Group: task.Group, Other: string(other),
	}, nil
}

func newVideoRefundAudit(
	tx *gorm.DB,
	task model.Task,
	reservationID, reason string,
	createdAt int64,
) (*model.Log, error) {
	eventID := "video-refund:" + reservationID
	properties, err := decodeVideoTaskProperties(task.Properties)
	if err != nil || len(eventID) > 64 || createdAt <= 0 {
		return nil, errors.New("invalid video refund audit")
	}
	var record model.RelayQuotaReservationRecord
	if tx == nil {
		return nil, errors.New("video refund audit transaction is nil")
	}
	if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	other, err := common.Marshal(map[string]any{
		"relay_reservation_id": reservationID,
		"task_id":              task.TaskID,
		"task_platform":        task.Platform,
		"reason":               boundedVideoFailReason(reason),
	})
	if err != nil {
		return nil, err
	}
	return &model.Log{
		AuditEventId: &eventID, UserId: task.UserId, CreatedAt: createdAt,
		Type: service.LogTypeRefund, ModelName: properties.OriginModelName,
		Quota: record.ActualQuota, ChannelId: task.ChannelId, TokenId: record.TokenID,
		Group: task.Group, Other: string(other),
	}, nil
}
