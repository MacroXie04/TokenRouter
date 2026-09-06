package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/midjourney"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
)

const (
	midjourneyOperationLeaseSeconds    = int64(90)
	midjourneyOperationRetrySeconds    = int64(45)
	midjourneyDispatchRecoverySeconds  = int64(120)
	midjourneyReservationSafetySeconds = int64(8 * 24 * time.Hour / time.Second)
	midjourneyOperationBatchSize       = 100
	midjourneyOperationMaxAttempts     = 20_000
	midjourneyOperationMaxPollDays     = 7

	midjourneyUnknownDispatchReason     = "Midjourney provider submission outcome requires manual review"
	midjourneyPollingManualReviewReason = "Midjourney task exceeded automatic polling horizon"
)

func createMidjourneyReservedTask(
	task *model.Task,
	mirror *model.Midjourney,
	token *model.Token,
	privateData *midjourneyTaskPrivateData,
) (*billingsvc.RelayQuotaReservation, error) {
	if task == nil || mirror == nil || privateData == nil || task.TaskID == "" || task.UserId <= 0 ||
		task.ChannelId <= 0 || task.Platform != midjourneyTaskPlatform || mirror.MjId != task.TaskID ||
		mirror.UserId != task.UserId || mirror.ChannelId != task.ChannelId || task.Quota < 0 {
		return nil, errors.New("invalid Midjourney task reservation")
	}
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil || properties.Pricing.ModelName == "" {
		return nil, errors.New("invalid Midjourney task pricing")
	}
	return billingsvc.NewRelayQuotaReservationWithFreeModelAndPersistence(
		task.UserId, token, task.Quota, properties.Pricing.FreeModel,
		func(tx *gorm.DB, creation billingsvc.RelayQuotaReservationCreation) error {
			privateData.RelayReservationID = creation.ReservationID
			privateData.BillingSource = creation.Funding.Source
			privateData.SubscriptionID = creation.Funding.SubscriptionId
			privateData.FundingUsageEpoch = creation.Funding.UsageEpoch
			privateData.TokenID = creation.TokenID
			encoded, err := marshalMidjourneyTaskPrivateData(*privateData)
			if err != nil {
				return err
			}
			task.PrivateData = encoded
			if err := tx.Create(task).Error; err != nil {
				return err
			}
			if err := tx.Create(mirror).Error; err != nil {
				return err
			}
			now, err := model.DatabaseUnixTimestamp(tx)
			if err != nil {
				return err
			}
			minimumExpiry := now + midjourneyReservationSafetySeconds
			if err := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("reservation_id = ? AND status = ? AND expires_at < ?", creation.ReservationID,
					model.RelayQuotaReservationStatusHeld, minimumExpiry).
				UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
				return err
			}
			return tx.Create(&model.TaskOperation{
				TaskID: task.TaskID, ReservationID: creation.ReservationID,
				Platform: midjourneyTaskPlatform, UserID: task.UserId, ChannelID: task.ChannelId,
				State: model.TaskOperationPrepared, NextAttemptAt: now + midjourneyOperationRetrySeconds,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
	)
}

func markMidjourneyTaskDispatching(task *model.Task, reservation *billingsvc.RelayQuotaReservation) error {
	if task == nil || reservation == nil || task.ID <= 0 || task.Platform != midjourneyTaskPlatform {
		return errors.New("invalid Midjourney dispatch transition")
	}
	var now int64
	err := reservation.MarkDispatchedWithPersistence(func(tx *gorm.DB) error {
		var err error
		now, err = model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		minimumExpiry := now + midjourneyReservationSafetySeconds
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("reservation_id = ? AND status = ? AND expires_at < ?", reservation.ReservationID(),
				model.RelayQuotaReservationStatusDispatched, minimumExpiry).
			UpdateColumn("expires_at", minimumExpiry).Error; err != nil {
			return err
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status = ?", task.ID, task.TaskID,
				midjourneyTaskPlatform, model.TaskStatusNotStart).
			Update("updated_at", now); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ? AND lease_owner = ?",
				task.TaskID, reservation.ReservationID(), midjourneyTaskPlatform, model.TaskOperationPrepared, "").
			Updates(map[string]any{
				"state": model.TaskOperationDispatching, "settlement_pending": true,
				"next_attempt_at": now + midjourneyDispatchRecoverySeconds,
				"updated_at":      now, "last_error": "",
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err != nil && !midjourneyDispatchTransitionMatches(task.TaskID, reservation.ReservationID()) {
		return err
	}
	task.UpdatedAt = now
	return nil
}

func persistAcceptedMidjourneyTask(
	task *model.Task,
	reservationID string,
	provider *midjourney.TaskResult,
	providerCode int,
	expectedState, leaseOwner string,
) error {
	if task == nil || provider == nil || strings.TrimSpace(provider.ProviderTaskID) == "" ||
		(provider.Status == model.TaskStatusSuccess || provider.Status == model.TaskStatusFailure) ||
		(expectedState != model.TaskOperationDispatching && expectedState != model.TaskOperationSubmitted) {
		return errors.New("invalid accepted Midjourney provider state")
	}
	properties, privateData, operation, err := loadMidjourneyOperationMetadata(task, reservationID)
	if err != nil {
		return err
	}
	encryptedProviderID, err := midjourneyProviderCipher(task, privateData, &operation, provider.ProviderTaskID, properties.Action)
	if err != nil {
		return err
	}
	privateData.EncryptedProviderTaskID = encryptedProviderID
	privateData.SettlementPending = true
	privateJSON, err := marshalMidjourneyTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	dataJSON, err := marshalMidjourneyProviderData(provider)
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	visibleStatus := normalizeMidjourneyTaskStatus(provider.Status)
	if visibleStatus == "" {
		visibleStatus = model.TaskStatusSubmitted
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		currentTask, currentMirror, currentOperation, err := loadMidjourneyIdentityTx(tx, task, reservationID)
		if err != nil {
			return err
		}
		if currentOperation.State == model.TaskOperationTerminal || currentOperation.State == model.TaskOperationRefunded {
			return nil
		}
		if currentOperation.State != expectedState || currentOperation.LeaseOwner != leaseOwner ||
			currentTask.Status == model.TaskStatusSuccess || currentTask.Status == model.TaskStatusFailure {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		if err := verifyMidjourneyProviderCipherConsistency(currentTask, currentOperation,
			provider.ProviderTaskID, properties.Action); err != nil {
			return err
		}
		taskUpdates := map[string]any{
			"status": visibleStatus, "private_data": privateJSON, "data": dataJSON,
			"progress": provider.Progress, "fail_reason": "", "updated_at": now,
		}
		if visibleStatus == model.TaskStatusRunning && currentTask.StartTime == 0 {
			taskUpdates["start_time"] = now
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, midjourneyTaskPlatform).
			Updates(taskUpdates); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		if err := updateMidjourneyMirrorTx(tx, currentMirror, provider, providerCode, visibleStatus, now); err != nil {
			return err
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": true,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            now + midjourneyOperationRetrySeconds,
				"completed_at":               int64(0), "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
}

// persistAcceptedMidjourneyFallback records the provider identity without
// settling accounting. It is deliberately tolerant of a prior ambiguous or
// partially committed state so an encrypted node-local recovery journal can
// safely promote the task after a database outage.
func persistAcceptedMidjourneyFallback(task *model.Task, reservationID, providerTaskID string) error {
	if task == nil || reservationID == "" || strings.TrimSpace(providerTaskID) == "" {
		return errors.New("invalid accepted Midjourney fallback")
	}
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	provider := &midjourney.TaskResult{
		ProviderTaskID: providerTaskID, Action: string(properties.Action),
		Status: model.TaskStatusSubmitted, Progress: "0%",
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		currentTask, currentMirror, operation, err := loadMidjourneyIdentityTx(tx, task, reservationID)
		if err != nil {
			return err
		}
		if operation.LeaseOwner != "" ||
			(operation.State != model.TaskOperationDispatching && operation.State != model.TaskOperationSubmitted &&
				operation.State != model.TaskOperationManualReview) {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		privateData, err := decodeMidjourneyTaskPrivateData(currentTask.PrivateData)
		if err != nil || privateData.RelayReservationID != reservationID {
			return errors.New("invalid Midjourney fallback metadata")
		}
		encryptedProviderID, err := midjourneyProviderCipher(currentTask, privateData, operation,
			providerTaskID, properties.Action)
		if err != nil {
			return err
		}
		privateData.EncryptedProviderTaskID = encryptedProviderID
		privateData.SettlementPending = true
		privateJSON, err := marshalMidjourneyTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		dataJSON, err := marshalMidjourneyProviderData(provider)
		if err != nil {
			return err
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND status NOT IN ?", currentTask.ID,
				currentTask.TaskID, midjourneyTaskPlatform,
				[]string{model.TaskStatusSuccess, model.TaskStatusFailure}).
			Updates(map[string]any{
				"status": model.TaskStatusSubmitted, "private_data": privateJSON, "data": dataJSON,
				"progress": "0%", "fail_reason": "", "finish_time": int64(0), "updated_at": now,
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		if err := updateMidjourneyMirrorTx(tx, currentMirror, provider, 1,
			model.TaskStatusSubmitted, now); err != nil {
			return err
		}
		if result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, operation.State, "").
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": true,
				"encrypted_provider_task_id": encryptedProviderID, "next_attempt_at": now,
				"completed_at": int64(0), "updated_at": now, "last_error": "",
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err == nil || midjourneyAcceptedFallbackMatches(task.TaskID, reservationID, providerTaskID) {
		return nil
	}
	return err
}

func settleSuccessfulMidjourneyTask(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	provider *midjourney.TaskResult,
	providerCode int,
	expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil || provider == nil ||
		normalizeMidjourneyTaskStatus(provider.Status) != model.TaskStatusSuccess || provider.ProviderTaskID == "" {
		return errors.New("invalid successful Midjourney settlement")
	}
	properties, privateData, operation, err := loadMidjourneyOperationMetadata(task, reservation.ReservationID())
	if err != nil {
		return err
	}
	encryptedProviderID, err := midjourneyProviderCipher(task, privateData, &operation,
		provider.ProviderTaskID, properties.Action)
	if err != nil {
		return err
	}
	actualQuota, err := properties.Pricing.SettlementQuota(0)
	if err != nil {
		return err
	}
	privateData.EncryptedProviderTaskID = encryptedProviderID
	privateData.SettlementPending = false
	privateJSON, err := marshalMidjourneyTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	dataJSON, err := marshalMidjourneyProviderData(provider)
	if err != nil {
		return err
	}
	var committedAt int64
	err = reservation.SettleWithChannelAndPersistence(actualQuota, task.ChannelId, func(tx *gorm.DB) error {
		currentTask, currentMirror, currentOperation, err := loadMidjourneyIdentityTx(tx, task, reservation.ReservationID())
		if err != nil {
			return err
		}
		if err := verifyMidjourneyProviderCipherConsistency(currentTask, currentOperation,
			provider.ProviderTaskID, properties.Action); err != nil {
			return err
		}
		committedAt, err = model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if currentOperation.State == model.TaskOperationTerminal {
			if currentTask.Status != model.TaskStatusSuccess || currentTask.Quota != actualQuota ||
				currentOperation.SettlementPending {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
			audit, err := newMidjourneyConsumeAudit(*currentTask, privateData, reservation.ReservationID(), actualQuota)
			if err != nil {
				return err
			}
			return billingsvc.EnqueueAuditLogTx(tx, audit)
		}
		if currentOperation.State != expectedState || currentOperation.LeaseOwner != leaseOwner ||
			currentTask.Status == model.TaskStatusSuccess || currentTask.Status == model.TaskStatusFailure {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		taskUpdates := map[string]any{
			"status": model.TaskStatusSuccess, "quota": actualQuota,
			"private_data": privateJSON, "data": dataJSON, "progress": "100%",
			"fail_reason": "", "finish_time": committedAt, "updated_at": committedAt,
		}
		if currentTask.StartTime == 0 {
			taskUpdates["start_time"] = committedAt
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, midjourneyTaskPlatform).
			Updates(taskUpdates); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		if err := updateMidjourneyMirrorTx(tx, currentMirror, provider, providerCode,
			model.TaskStatusSuccess, committedAt); err != nil {
			return err
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationTerminal, "settlement_pending": false,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            int64(0), "completed_at": committedAt, "updated_at": committedAt,
				"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		updated := *currentTask
		updated.Status, updated.Quota, updated.PrivateData = model.TaskStatusSuccess, actualQuota, privateJSON
		audit, err := newMidjourneyConsumeAudit(updated, privateData, reservation.ReservationID(), actualQuota)
		if err != nil {
			return err
		}
		return billingsvc.EnqueueAuditLogTx(tx, audit)
	})
	if err != nil && !midjourneySettlementTransitionMatches(task.TaskID, reservation.ReservationID(), actualQuota) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func refundMidjourneyTask(
	task *model.Task,
	reservation *billingsvc.RelayQuotaReservation,
	provider *midjourney.TaskResult,
	providerCode int,
	reason, expectedState, leaseOwner string,
) error {
	if task == nil || reservation == nil {
		return errors.New("invalid Midjourney refund")
	}
	reason = boundedMidjourneyFailReason(reason)
	properties, privateData, operation, err := loadMidjourneyOperationMetadata(task, reservation.ReservationID())
	if err != nil {
		return err
	}
	privateData.SettlementPending = false
	if provider != nil && provider.ProviderTaskID != "" {
		privateData.EncryptedProviderTaskID, err = midjourneyProviderCipher(task, privateData, &operation,
			provider.ProviderTaskID, properties.Action)
		if err != nil {
			return err
		}
	}
	privateJSON, err := marshalMidjourneyTaskPrivateData(privateData)
	if err != nil {
		return err
	}
	failed := provider
	if failed == nil {
		failed = &midjourney.TaskResult{ProviderTaskID: "unavailable", Action: string(properties.Action),
			Status: model.TaskStatusFailure, Progress: "100%", FailReason: reason}
	}
	failed.Status, failed.Progress, failed.FailReason = model.TaskStatusFailure, "100%", reason
	dataJSON, err := marshalMidjourneyProviderData(failed)
	if err != nil {
		return err
	}
	err = reservation.RefundWithPersistence(func(tx *gorm.DB) error {
		currentTask, currentMirror, currentOperation, err := loadMidjourneyIdentityTx(tx, task, reservation.ReservationID())
		if err != nil {
			return err
		}
		if currentOperation.State == model.TaskOperationRefunded {
			if currentTask.Status != model.TaskStatusFailure || currentTask.Quota != 0 || currentOperation.SettlementPending {
				return billingsvc.ErrRelayQuotaReservationBusy
			}
			return nil
		}
		if currentOperation.State != expectedState || currentOperation.LeaseOwner != leaseOwner {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		if provider != nil && provider.ProviderTaskID != "" {
			if err := verifyMidjourneyProviderCipherConsistency(currentTask, currentOperation,
				provider.ProviderTaskID, properties.Action); err != nil {
				return err
			}
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ?", currentTask.ID, currentTask.TaskID, midjourneyTaskPlatform).
			Updates(map[string]any{
				"status": model.TaskStatusFailure, "quota": 0, "private_data": privateJSON,
				"data": dataJSON, "progress": "100%", "fail_reason": reason,
				"finish_time": now, "updated_at": now,
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		if err := updateMidjourneyMirrorTx(tx, currentMirror, failed, providerCode,
			model.TaskStatusFailure, now); err != nil {
			return err
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", currentOperation.ID, expectedState, leaseOwner).
			Updates(map[string]any{
				"state": model.TaskOperationRefunded, "settlement_pending": false,
				"encrypted_provider_task_id": privateData.EncryptedProviderTaskID,
				"next_attempt_at":            int64(0), "completed_at": now, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err != nil && !midjourneyRefundTransitionMatches(task.TaskID, reservation.ReservationID()) {
		return err
	}
	return model.DB.Where("id = ? AND task_id = ?", task.ID, task.TaskID).First(task).Error
}

func markMidjourneyAmbiguousDispatch(task *model.Task, reservationID, reason string) error {
	if task == nil || reservationID == "" {
		return errors.New("invalid ambiguous Midjourney dispatch")
	}
	reason = boundedMidjourneyFailReason(reason)
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		currentTask, currentMirror, operation, err := loadMidjourneyIdentityTx(tx, task, reservationID)
		if err != nil {
			return err
		}
		if operation.State != model.TaskOperationDispatching || operation.LeaseOwner != "" ||
			operation.EncryptedProviderTaskID != "" || currentTask.Status != model.TaskStatusNotStart {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		privateData, err := decodeMidjourneyTaskPrivateData(currentTask.PrivateData)
		if err != nil || privateData.EncryptedProviderTaskID != "" {
			return errors.New("ambiguous Midjourney metadata is invalid")
		}
		privateData.SettlementPending = true
		privateJSON, err := marshalMidjourneyTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		if result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", currentTask.ID, model.TaskStatusNotStart).
			Updates(map[string]any{
				"status": model.TaskStatusUnknown, "private_data": privateJSON,
				"progress": "100%", "fail_reason": reason, "finish_time": now, "updated_at": now,
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		if result := tx.Model(&model.Midjourney{}).Where("id = ?", currentMirror.Id).
			Updates(map[string]any{
				"code": 5, "status": model.TaskStatusUnknown, "progress": "100%",
				"fail_reason": reason, "finish_time": now * 1000,
			}); result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		result := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, model.TaskOperationDispatching, "").
			Updates(map[string]any{
				"state": model.TaskOperationManualReview, "settlement_pending": true,
				"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
				"last_error": reason, "lease_owner": "", "lease_expires_at": int64(0),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		return nil
	})
	if err != nil && midjourneyManualReviewTransitionMatches(task.TaskID, reservationID) {
		return nil
	}
	return err
}

func loadMidjourneyOperationMetadata(task *model.Task, reservationID string) (
	midjourneyTaskProperties, midjourneyTaskPrivateData, model.TaskOperation, error,
) {
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil {
		return midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, model.TaskOperation{}, err
	}
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != reservationID {
		return midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, model.TaskOperation{}, errors.New("invalid Midjourney recovery metadata")
	}
	var operation model.TaskOperation
	if err := model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
		reservationID, midjourneyTaskPlatform).First(&operation).Error; err != nil {
		return midjourneyTaskProperties{}, midjourneyTaskPrivateData{}, model.TaskOperation{}, err
	}
	return properties, privateData, operation, nil
}

func loadMidjourneyIdentityTx(tx *gorm.DB, task *model.Task, reservationID string) (
	*model.Task, *model.Midjourney, *model.TaskOperation, error,
) {
	var currentTask model.Task
	if err := tx.Where("id = ? AND task_id = ? AND platform = ?", task.ID, task.TaskID,
		midjourneyTaskPlatform).First(&currentTask).Error; err != nil {
		return nil, nil, nil, err
	}
	var mirror model.Midjourney
	if err := tx.Where("mj_id = ? AND user_id = ? AND channel_id = ?", task.TaskID,
		task.UserId, task.ChannelId).First(&mirror).Error; err != nil {
		return nil, nil, nil, err
	}
	var operation model.TaskOperation
	if err := tx.Where("task_id = ? AND reservation_id = ? AND platform = ?", task.TaskID,
		reservationID, midjourneyTaskPlatform).First(&operation).Error; err != nil {
		return nil, nil, nil, err
	}
	if !midjourneyTaskOperationIdentityMatches(&currentTask, &mirror, &operation, reservationID) {
		return nil, nil, nil, errors.New("Midjourney task identity mismatch")
	}
	return &currentTask, &mirror, &operation, nil
}

func midjourneyTaskOperationIdentityMatches(task *model.Task, mirror *model.Midjourney,
	operation *model.TaskOperation, reservationID string) bool {
	if task == nil || mirror == nil || operation == nil || task.ID <= 0 || mirror.Id <= 0 || operation.ID <= 0 {
		return false
	}
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	return err == nil && task.Platform == midjourneyTaskPlatform && operation.Platform == midjourneyTaskPlatform &&
		task.TaskID == mirror.MjId && task.TaskID == operation.TaskID && task.UserId == mirror.UserId &&
		task.UserId == operation.UserID && task.ChannelId == mirror.ChannelId && task.ChannelId == operation.ChannelID &&
		reservationID != "" && reservationID == operation.ReservationID && reservationID == privateData.RelayReservationID
}

func midjourneyProviderCipher(task *model.Task, privateData midjourneyTaskPrivateData,
	operation *model.TaskOperation, providerTaskID string, action midjourney.Action) (string, error) {
	binding := midjourneyProviderTaskBinding(task.TaskID, privateData.RelayReservationID,
		task.UserId, task.ChannelId, action)
	reusable := ""
	values := []string{privateData.EncryptedProviderTaskID}
	if operation != nil {
		values = append(values, operation.EncryptedProviderTaskID)
	}
	for _, encoded := range values {
		if encoded == "" {
			continue
		}
		decoded, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || decoded != providerTaskID {
			return "", errors.New("Midjourney provider identity conflicts with durable state")
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

func midjourneyAcceptedFallbackMatches(taskID, reservationID, providerTaskID string) bool {
	var task model.Task
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND platform = ?", taskID, midjourneyTaskPlatform).First(&task).Error != nil ||
		model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
			reservationID, midjourneyTaskPlatform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationSubmitted || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LeaseOwner != "" {
		return false
	}
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil {
		return false
	}
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" || !privateData.SettlementPending {
		return false
	}
	binding := midjourneyProviderTaskBinding(taskID, reservationID, operation.UserID,
		operation.ChannelID, properties.Action)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		stored, decryptErr := asyncTaskDecryptBound(encoded, binding)
		if decryptErr != nil || stored != providerTaskID {
			return false
		}
	}
	return true
}

func verifyMidjourneyProviderCipherConsistency(task *model.Task, operation *model.TaskOperation,
	providerTaskID string, action midjourney.Action) error {
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil {
		return err
	}
	_, err = midjourneyProviderCipher(task, privateData, operation, providerTaskID, action)
	return err
}

func marshalMidjourneyProviderData(provider *midjourney.TaskResult) (string, error) {
	if provider == nil {
		return "", errors.New("Midjourney provider state is nil")
	}
	data := midjourneyStoredTaskData{
		CustomID: provider.CustomID, BotType: provider.BotType, MaskBase64: provider.MaskBase64,
		VideoURLs: append([]midjourney.VideoURL(nil), provider.VideoURLs...),
	}
	return marshalMidjourneyStoredTaskData(data)
}

func updateMidjourneyMirrorTx(tx *gorm.DB, mirror *model.Midjourney, provider *midjourney.TaskResult,
	providerCode int, status string, now int64) error {
	if tx == nil || mirror == nil || provider == nil {
		return errors.New("invalid Midjourney mirror update")
	}
	videoURLs, err := json.Marshal(provider.VideoURLs)
	if err != nil || len(videoURLs) > midjourney.MaxProviderMetadataBytes {
		return errors.New("invalid Midjourney video URLs")
	}
	buttons := normalizeMidjourneyRawJSON(provider.Buttons)
	properties := normalizeMidjourneyRawJSON(provider.Properties)
	progress := provider.Progress
	if progress == "" {
		progress = "0%"
	}
	updates := map[string]any{
		"code": providerCode, "description": boundedMidjourneyFailReason(provider.Description),
		"prompt_en": provider.PromptEn, "state": provider.State,
		"image_url": provider.ImageURL, "video_url": provider.VideoURL, "video_urls": string(videoURLs),
		"status": status, "progress": progress, "fail_reason": boundedMidjourneyFailReason(provider.FailReason),
		"buttons": buttons, "properties": properties,
	}
	if provider.SubmitTime > 0 {
		updates["submit_time"] = provider.SubmitTime
	}
	if provider.StartTime > 0 {
		updates["start_time"] = provider.StartTime
	} else if status == model.TaskStatusRunning && mirror.StartTime == 0 {
		updates["start_time"] = now * 1000
	}
	if provider.FinishTime > 0 {
		updates["finish_time"] = provider.FinishTime
	} else if status == model.TaskStatusSuccess || status == model.TaskStatusFailure {
		updates["finish_time"] = now * 1000
	}
	result := tx.Model(&model.Midjourney{}).Where("id = ? AND mj_id = ? AND user_id = ?",
		mirror.Id, mirror.MjId, mirror.UserId).Updates(updates)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(result.Error, billingsvc.ErrRelayQuotaReservationBusy)
	}
	return nil
}

func normalizeMidjourneyRawJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func normalizeMidjourneyTaskStatus(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "NOT_START":
		return model.TaskStatusNotStart
	case "SUBMITTED":
		return model.TaskStatusSubmitted
	case "QUEUED":
		return model.TaskStatusQueued
	case "RUNNING", "PROCESSING", "IN_PROGRESS":
		return model.TaskStatusRunning
	case "SUCCESS", "SUCCEEDED", "COMPLETED":
		return model.TaskStatusSuccess
	case "FAILURE", "FAILED", "FAIL":
		return model.TaskStatusFailure
	default:
		return ""
	}
}

func newMidjourneyConsumeAudit(task model.Task, privateData midjourneyTaskPrivateData,
	reservationID string, actualQuota int) (*model.Log, error) {
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil || task.TaskID == "" || reservationID == "" {
		return nil, errors.New("invalid Midjourney consume audit")
	}
	eventID := "mj:" + reservationID
	if len(eventID) > 64 {
		return nil, errors.New("invalid Midjourney consume audit identity")
	}
	other := properties.Pricing.BillingLogFields()
	for key, value := range map[string]any{
		"billing_source": privateData.BillingSource, "relay_reservation_id": reservationID,
		"task_id": task.TaskID, "task_platform": midjourneyTaskPlatform, "action": string(properties.Action),
		"subscription_id": privateData.SubscriptionID, "subscription_usage_epoch": privateData.FundingUsageEpoch,
	} {
		other[key] = value
	}
	encoded, err := jsonutil.Marshal(other)
	if err != nil {
		return nil, err
	}
	return &model.Log{
		AuditEventId: &eventID, UserId: task.UserId, CreatedAt: task.CreatedAt,
		Type: billingsvc.LogTypeConsume, ModelName: properties.OriginModelName,
		Quota: actualQuota, ChannelId: task.ChannelId, TokenId: privateData.TokenID,
		Group: task.Group, Other: string(encoded),
	}, nil
}

func midjourneyDispatchTransitionMatches(taskID, reservationID string) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusDispatched {
		return false
	}
	var operation model.TaskOperation
	return model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, midjourneyTaskPlatform, model.TaskOperationDispatching).First(&operation).Error == nil
}

func midjourneySettlementTransitionMatches(taskID, reservationID string, actual int) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusSettled || record.ActualQuota != actual {
		return false
	}
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, midjourneyTaskPlatform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationTerminal || operation.SettlementPending {
		return false
	}
	var task model.Task
	var mirror model.Midjourney
	return model.DB.Where("task_id = ? AND platform = ? AND status = ? AND quota = ?", taskID,
		midjourneyTaskPlatform, model.TaskStatusSuccess, actual).First(&task).Error == nil &&
		model.DB.Where("mj_id = ? AND user_id = ? AND status = ?", taskID, task.UserId,
			model.TaskStatusSuccess).First(&mirror).Error == nil &&
		midjourneyTaskOperationIdentityMatches(&task, &mirror, &operation, reservationID)
}

func midjourneyRefundTransitionMatches(taskID, reservationID string) bool {
	var record model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", reservationID).First(&record).Error != nil ||
		record.Status != model.RelayQuotaReservationStatusRefunded {
		return false
	}
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ?", taskID,
		reservationID, midjourneyTaskPlatform).First(&operation).Error != nil ||
		operation.State != model.TaskOperationRefunded || operation.SettlementPending {
		return false
	}
	var task model.Task
	var mirror model.Midjourney
	return model.DB.Where("task_id = ? AND platform = ? AND status = ? AND quota = 0", taskID,
		midjourneyTaskPlatform, model.TaskStatusFailure).First(&task).Error == nil &&
		model.DB.Where("mj_id = ? AND user_id = ? AND status = ?", taskID, task.UserId,
			model.TaskStatusFailure).First(&mirror).Error == nil &&
		midjourneyTaskOperationIdentityMatches(&task, &mirror, &operation, reservationID)
}

func midjourneyManualReviewTransitionMatches(taskID, reservationID string) bool {
	var operation model.TaskOperation
	if model.DB.Where("task_id = ? AND reservation_id = ? AND platform = ? AND state = ?",
		taskID, reservationID, midjourneyTaskPlatform, model.TaskOperationManualReview).First(&operation).Error != nil {
		return false
	}
	var task model.Task
	return model.DB.Where("task_id = ? AND platform = ? AND status = ?", taskID,
		midjourneyTaskPlatform, model.TaskStatusUnknown).First(&task).Error == nil
}

func formatMidjourneyOperationError(operation string) string {
	return fmt.Sprintf("Midjourney %s operation failed", operation)
}
