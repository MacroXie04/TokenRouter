package tasks

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/hailuo"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"sync"
)

var hailuoTaskManualReviewRetryMu sync.Mutex

type HailuoTaskManualReviewRetryResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

// RetryHailuoTaskManualReview reopens only authenticated, already-settled Hailuo
// polling. It never changes accounting and cannot replay a submission.
func RetryHailuoTaskManualReview(reservationID string, operatorUserID int) (*HailuoTaskManualReviewRetryResult, error) {
	if !validHailuoReviewReservationID(reservationID) || operatorUserID <= 0 {
		return nil, billingsvc.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("Hailuo review database is unavailable")
	}
	hailuoTaskManualReviewRetryMu.Lock()
	defer hailuoTaskManualReviewRetryMu.Unlock()
	eventID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	changed := false
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("reservation_id = ?", reservationID).
			First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return billingsvc.ErrRelayQuotaReviewNotFound
			}
			return err
		}
		var operation model.TaskOperation
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ? AND platform = ?", reservationID, hailuoTaskPlatform).Limit(1).Find(&operation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReviewNotFound
		}
		var tasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND platform = ?", operation.TaskID, hailuoTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return fmt.Errorf("%w: Hailuo task identity is unavailable or ambiguous", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
		task := &tasks[0]
		prior, found, err := billingsvc.FindRelayQuotaTaskPollRetryEventTx(tx, reservationID)
		if err != nil {
			return err
		}
		if record.Status != model.RelayQuotaReservationStatusSettled ||
			operation.State != model.TaskOperationManualReview {
			if found && videoTaskPollRetryEventMatches(prior, &record, task, &operation) {
				eventID = prior.EventID
				return nil
			}
			return fmt.Errorf("%w: Hailuo task is not awaiting a polling retry", billingsvc.ErrRelayQuotaReviewInvalidState)
		}
		if err := validateActiveHailuoTaskPollReview(&record, task, &operation); err != nil {
			return err
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		event, err := billingsvc.AppendRelayQuotaTaskPollRetryEventTx(
			tx, &record, task, &operation, operatorUserID, eventID, now,
		)
		if err != nil {
			return err
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
				task.ID, task.TaskID, hailuoTaskPlatform, task.UserId, task.ChannelId, task.Quota).
			Where("status = ? AND private_data = ? AND properties = ? AND finish_time = ?",
				model.TaskStatusUnknown, task.PrivateData, task.Properties, task.FinishTime).
			Updates(map[string]any{"status": model.TaskStatusSubmitted, "fail_reason": "",
				"finish_time": 0, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ?", operation.ID,
				operation.TaskID, operation.ReservationID, hailuoTaskPlatform).
			Where("user_id = ? AND channel_id = ? AND state = ? AND settlement_pending = ?",
				operation.UserID, operation.ChannelID, model.TaskOperationManualReview, false).
			Where("encrypted_provider_task_id = ? AND attempts = ? AND next_attempt_at = ?",
				operation.EncryptedProviderTaskID, operation.Attempts, operation.NextAttemptAt).
			Where("lease_owner = ? AND lease_expires_at = ? AND last_error = ?",
				"", int64(0), operation.LastError).
			Updates(map[string]any{"state": model.TaskOperationSubmitted, "attempts": 0,
				"next_attempt_at": now, "created_at": now, "completed_at": 0, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if opResult.Error != nil || opResult.RowsAffected != 1 {
			return errors.Join(opResult.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		eventID = event.EventID
		changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	result, err := verifyHailuoTaskManualReviewRetry(reservationID, eventID)
	if err != nil || result == nil {
		if err == nil {
			err = billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil, err
	}
	result.Changed = changed
	return result, nil
}

func validateActiveHailuoTaskPollReview(record *model.RelayQuotaReservationRecord, task *model.Task,
	operation *model.TaskOperation) error {
	if record == nil || task == nil || operation == nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle || record.ChannelID != task.ChannelId ||
		record.CompletedAt <= 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
		task.Platform != hailuoTaskPlatform || task.TaskID != operation.TaskID || task.UserId != record.UserID ||
		task.Status != model.TaskStatusUnknown || task.Quota != record.ActualQuota || task.FinishTime <= 0 ||
		operation.Platform != hailuoTaskPlatform || operation.ReservationID != record.ReservationID ||
		operation.UserID != task.UserId || operation.ChannelID != task.ChannelId ||
		operation.State != model.TaskOperationManualReview || operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.NextAttemptAt != 0 ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.CompletedAt <= 0 {
		return fmt.Errorf("%w: Hailuo polling review tuple is inconsistent", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeHailuoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID || privateData.SettlementPending ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID {
		return fmt.Errorf("%w: Hailuo recovery metadata does not match accounting", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := validateHailuoTaskPricingSnapshot(task, privateData, record.ActualQuota); err != nil {
		return fmt.Errorf("%w: Hailuo pricing snapshots do not match accounting", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	settled, err := privateData.Pricing.SettlementQuota(0)
	if err != nil || settled != record.ActualQuota {
		return fmt.Errorf("%w: Hailuo pricing snapshot does not match accounting", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	baseURL, err := hailuo.EffectiveBaseURL(privateData.ChannelBaseURL)
	if err != nil || baseURL != privateData.ChannelBaseURL {
		return fmt.Errorf("%w: Hailuo provider endpoint is invalid", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		hailuoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("%w: Hailuo channel credential is not recoverable", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	binding := hailuoProviderTaskBinding(task.TaskID, operation.ReservationID, task.UserId, task.ChannelId)
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || !validHailuoRecoveryOpaqueID(providerID, hailuo.MaxProviderTaskIDBytes) {
			return fmt.Errorf("%w: Hailuo provider id is not recoverable", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
	}
	return nil
}

func verifyHailuoTaskManualReviewRetry(reservationID, eventID string) (*HailuoTaskManualReviewRetryResult, error) {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	var operation model.TaskOperation
	if err := model.DB.Where("reservation_id = ? AND platform = ?", reservationID, hailuoTaskPlatform).
		First(&operation).Error; err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", operation.TaskID, hailuoTaskPlatform).
		First(&task).Error; err != nil {
		return nil, err
	}
	var event model.RelayQuotaReservationReviewEvent
	query := model.DB.Where("reservation_id = ? AND action = ?", reservationID,
		model.RelayQuotaReservationReviewActionRetryTaskPoll)
	if eventID != "" {
		query = query.Where("event_id = ?", eventID)
	}
	result := query.Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil || result.RowsAffected != 1 ||
		!videoTaskPollRetryEventMatches(&event, &record, &task, &operation) {
		return nil, result.Error
	}
	return &HailuoTaskManualReviewRetryResult{ReservationID: reservationID, TaskID: task.TaskID,
		ReservationStatus: record.Status, TaskStatus: task.Status, OperationState: operation.State,
		AuditEventID: event.EventID}, nil
}
