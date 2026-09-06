package relay

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/midjourney"
	"github.com/tokenrouter/tokenrouter/service"
)

var midjourneyTaskManualReviewRetryMu sync.Mutex

// RetryMidjourneyTaskManualReview reopens only a known provider poll after
// atomically validating and transitioning both the generic task row and the
// legacy Midjourney mirror. The unresolved accounting hold is not changed.
func RetryMidjourneyTaskManualReview(reservationID string, operatorUserID int) (*HeldAsyncTaskManualReviewRetryResult, error) {
	if !validVideoReviewReservationID(reservationID) || operatorUserID <= 0 {
		return nil, service.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("Midjourney review database is unavailable")
	}
	midjourneyTaskManualReviewRetryMu.Lock()
	defer midjourneyTaskManualReviewRetryMu.Unlock()
	eventID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	changed := false
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("reservation_id = ?", reservationID).
			First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return service.ErrRelayQuotaReviewNotFound
			}
			return err
		}
		var operations []model.TaskOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ? AND platform = ?", reservationID, midjourneyTaskPlatform).
			Limit(2).Find(&operations).Error; err != nil {
			return err
		}
		if len(operations) == 0 {
			return service.ErrRelayQuotaReviewNotFound
		}
		if len(operations) != 1 {
			return fmt.Errorf("%w: Midjourney operation identity is ambiguous", service.ErrRelayQuotaReviewUnsafeRetry)
		}
		operation := &operations[0]
		var tasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND platform = ?", operation.TaskID, midjourneyTaskPlatform).
			Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return fmt.Errorf("%w: Midjourney task identity is unavailable or ambiguous", service.ErrRelayQuotaReviewUnsafeRetry)
		}
		task := &tasks[0]
		var mirrors []model.Midjourney
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("mj_id = ?", task.TaskID).
			Limit(2).Find(&mirrors).Error; err != nil {
			return err
		}
		if len(mirrors) != 1 {
			return fmt.Errorf("%w: Midjourney mirror identity is unavailable or ambiguous", service.ErrRelayQuotaReviewUnsafeRetry)
		}
		mirror := &mirrors[0]
		prior, found, err := service.FindRelayQuotaTaskPollRetryEventTx(tx, reservationID)
		if err != nil {
			return err
		}
		if record.Status != model.RelayQuotaReservationStatusDispatched ||
			operation.State != model.TaskOperationManualReview {
			if found && videoTaskPollRetryEventMatches(prior, &record, task, operation) &&
				validRetriedMidjourneyMirror(task, mirror) {
				eventID = prior.EventID
				return nil
			}
			return fmt.Errorf("%w: Midjourney task is not awaiting a polling retry",
				service.ErrRelayQuotaReviewInvalidState)
		}
		if err := validateActiveMidjourneyTaskPollReview(&record, task, mirror, operation); err != nil {
			return err
		}
		var consumeAuditCount int64
		if err := tx.Model(&model.AuditLogOutbox{}).Where("event_id = ?", "mj:"+record.ReservationID).
			Count(&consumeAuditCount).Error; err != nil {
			return err
		}
		if consumeAuditCount != 0 {
			return fmt.Errorf("%w: Midjourney accounting audit already exists", service.ErrRelayQuotaReviewUnsafeRetry)
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		event, err := service.AppendRelayQuotaTaskPollRetryEventTx(
			tx, &record, task, operation, operatorUserID, eventID, now,
		)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
				task.ID, task.TaskID, midjourneyTaskPlatform, task.UserId, task.ChannelId, task.Quota).
			Where("status = ? AND fail_reason = ? AND private_data = ? AND properties = ? AND finish_time = ?",
				model.TaskStatusUnknown, midjourneyPollingManualReviewReason,
				task.PrivateData, task.Properties, task.FinishTime).
			Updates(map[string]any{"status": model.TaskStatusSubmitted, "fail_reason": "",
				"progress": "0%", "finish_time": 0, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error,
				fmt.Errorf("%w: Midjourney task snapshot changed", service.ErrRelayQuotaReservationBusy))
		}
		mirrorResult := tx.Model(&model.Midjourney{}).
			Where("id = ? AND mj_id = ? AND user_id = ? AND channel_id = ? AND quota = ?",
				mirror.Id, mirror.MjId, mirror.UserId, mirror.ChannelId, mirror.Quota).
			Where("status = ? AND fail_reason = ? AND progress = ? AND finish_time = ?",
				model.TaskStatusUnknown, midjourneyPollingManualReviewReason, "100%", mirror.FinishTime).
			Updates(map[string]any{"status": model.TaskStatusSubmitted, "fail_reason": "",
				"progress": "0%", "finish_time": 0})
		if mirrorResult.Error != nil || mirrorResult.RowsAffected != 1 {
			return errors.Join(mirrorResult.Error,
				fmt.Errorf("%w: Midjourney mirror snapshot changed", service.ErrRelayQuotaReservationBusy))
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ?", operation.ID,
				operation.TaskID, operation.ReservationID, midjourneyTaskPlatform).
			Where("user_id = ? AND channel_id = ? AND state = ? AND settlement_pending = ?",
				operation.UserID, operation.ChannelID, model.TaskOperationManualReview, true).
			Where("encrypted_provider_task_id = ? AND attempts = ? AND next_attempt_at = ?",
				operation.EncryptedProviderTaskID, operation.Attempts, operation.NextAttemptAt).
			Where("lease_owner = ? AND lease_expires_at = ? AND last_error = ?",
				"", int64(0), midjourneyPollingManualReviewReason).
			Updates(map[string]any{"state": model.TaskOperationSubmitted, "attempts": 0,
				"next_attempt_at": now, "created_at": now, "completed_at": 0, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if opResult.Error != nil || opResult.RowsAffected != 1 {
			return errors.Join(opResult.Error,
				fmt.Errorf("%w: Midjourney operation snapshot changed", service.ErrRelayQuotaReservationBusy))
		}
		eventID = event.EventID
		changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	result, err := verifyMidjourneyTaskManualReviewRetry(reservationID, eventID)
	if err != nil || result == nil {
		if err == nil {
			err = service.ErrRelayQuotaReservationBusy
		}
		return nil, err
	}
	result.Changed = changed
	return result, nil
}

func validateActiveMidjourneyTaskPollReview(record *model.RelayQuotaReservationRecord, task *model.Task,
	mirror *model.Midjourney, operation *model.TaskOperation) error {
	if err := validateHeldTaskPollReviewTuple(record, task, operation, midjourneyTaskPlatform,
		midjourneyPollingManualReviewReason); err != nil {
		return fmt.Errorf("%w: Midjourney %v", service.ErrRelayQuotaReviewUnsafeRetry, err)
	}
	if mirror == nil || !midjourneyTaskOperationIdentityMatches(task, mirror, operation, record.ReservationID) ||
		mirror.Status != model.TaskStatusUnknown || mirror.FailReason != midjourneyPollingManualReviewReason ||
		mirror.Progress != "100%" || mirror.FinishTime <= 0 || mirror.Quota != task.Quota ||
		task.FinishTime > math.MaxInt64/1000 || mirror.FinishTime != task.FinishTime*1000 {
		return fmt.Errorf("%w: Midjourney mirror does not match the polling review",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	properties, err := decodeMidjourneyTaskProperties(task.Properties)
	if err != nil || task.Action != string(properties.Action) || mirror.Action != task.Action {
		return fmt.Errorf("%w: Midjourney task provenance is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID || !privateData.SettlementPending ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID {
		return fmt.Errorf("%w: Midjourney recovery metadata does not match accounting",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	preconsume, preconsumeErr := properties.Pricing.PreConsumeQuota()
	settled, settleErr := properties.Pricing.SettlementQuota(0)
	if preconsumeErr != nil || settleErr != nil || preconsume != record.RequestedQuota || settled != record.RequestedQuota {
		return fmt.Errorf("%w: Midjourney pricing snapshot does not match accounting",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	baseURL, err := midjourney.ValidateBaseURL(privateData.ChannelBaseURL)
	if err != nil || baseURL != privateData.ChannelBaseURL {
		return fmt.Errorf("%w: Midjourney provider endpoint is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		midjourneyChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") {
		return fmt.Errorf("%w: Midjourney channel credential is not recoverable",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	binding := midjourneyProviderTaskBinding(task.TaskID, record.ReservationID,
		task.UserId, task.ChannelId, properties.Action)
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || !validMidjourneyRecoveryOpaqueID(providerID, midjourney.MaxTaskIDBytes) {
			return fmt.Errorf("%w: Midjourney provider id is not recoverable",
				service.ErrRelayQuotaReviewUnsafeRetry)
		}
	}
	if _, err := decodeMidjourneyStoredTaskData(task.Data); err != nil {
		return fmt.Errorf("%w: Midjourney provider state is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := midjourneyTaskDTOFromModel(mirror, task); err != nil {
		return fmt.Errorf("%w: Midjourney mirror state is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	return nil
}

func validRetriedMidjourneyMirror(task *model.Task, mirror *model.Midjourney) bool {
	return task != nil && mirror != nil && mirror.MjId == task.TaskID && mirror.UserId == task.UserId &&
		mirror.ChannelId == task.ChannelId && mirror.Status == model.TaskStatusSubmitted &&
		mirror.FailReason == "" && mirror.Progress == "0%" && mirror.FinishTime == 0
}

func verifyMidjourneyTaskManualReviewRetry(reservationID, eventID string) (*HeldAsyncTaskManualReviewRetryResult, error) {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	var operations []model.TaskOperation
	if err := model.DB.Where("reservation_id = ? AND platform = ?", reservationID, midjourneyTaskPlatform).
		Limit(2).Find(&operations).Error; err != nil {
		return nil, err
	}
	if len(operations) != 1 {
		return nil, service.ErrRelayQuotaReviewUnsafeRetry
	}
	operation := &operations[0]
	var tasks []model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", operation.TaskID, midjourneyTaskPlatform).
		Limit(2).Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) != 1 {
		return nil, service.ErrRelayQuotaReviewUnsafeRetry
	}
	task := &tasks[0]
	var mirrors []model.Midjourney
	if err := model.DB.Where("mj_id = ?", task.TaskID).
		Limit(2).Find(&mirrors).Error; err != nil {
		return nil, err
	}
	if len(mirrors) != 1 || !validRetriedMidjourneyMirror(task, &mirrors[0]) {
		return nil, service.ErrRelayQuotaReviewUnsafeRetry
	}
	var event model.RelayQuotaReservationReviewEvent
	query := model.DB.Where("reservation_id = ? AND action = ?", reservationID,
		model.RelayQuotaReservationReviewActionRetryTaskPoll)
	if eventID != "" {
		query = query.Where("event_id = ?", eventID)
	}
	result := query.Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil || result.RowsAffected != 1 ||
		!videoTaskPollRetryEventMatches(&event, &record, task, operation) {
		return nil, result.Error
	}
	return &HeldAsyncTaskManualReviewRetryResult{ReservationID: reservationID, TaskID: task.TaskID,
		ReservationStatus: record.Status, TaskStatus: task.Status, OperationState: operation.State,
		AuditEventID: event.EventID}, nil
}
