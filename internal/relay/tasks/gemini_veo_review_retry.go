package tasks

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sync"
)

var geminiVeoTaskManualReviewRetryMu sync.Mutex

type GeminiVeoTaskManualReviewRetryResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

// RetryGeminiVeoTaskManualReview reopens only authenticated, already-settled GeminiVeo
// polling. It never changes accounting and cannot replay a submission.
func RetryGeminiVeoTaskManualReview(reservationID string, operatorUserID int) (*GeminiVeoTaskManualReviewRetryResult, error) {
	if !validGeminiVeoReviewReservationID(reservationID) || operatorUserID <= 0 {
		return nil, billingsvc.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("GeminiVeo review database is unavailable")
	}
	geminiVeoTaskManualReviewRetryMu.Lock()
	defer geminiVeoTaskManualReviewRetryMu.Unlock()
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
		var operations []model.TaskOperation
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ? AND platform IN ?", reservationID, model.VeoTaskOperationPlatforms()).Limit(2).Find(&operations)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || len(operations) != 1 {
			return billingsvc.ErrRelayQuotaReviewNotFound
		}
		operation := operations[0]
		var tasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND platform = ?", operation.TaskID, operation.Platform).Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return fmt.Errorf("%w: GeminiVeo task identity is unavailable or ambiguous", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
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
			return fmt.Errorf("%w: GeminiVeo task is not awaiting a polling retry", billingsvc.ErrRelayQuotaReviewInvalidState)
		}
		if err := validateActiveGeminiVeoTaskPollReview(&record, task, &operation); err != nil {
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
				task.ID, task.TaskID, task.Platform, task.UserId, task.ChannelId, task.Quota).
			Where("status = ? AND private_data = ? AND properties = ? AND finish_time = ?",
				model.TaskStatusUnknown, task.PrivateData, task.Properties, task.FinishTime).
			Updates(map[string]any{"status": model.TaskStatusSubmitted, "fail_reason": "",
				"finish_time": 0, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error, billingsvc.ErrRelayQuotaReservationBusy)
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ?", operation.ID,
				operation.TaskID, operation.ReservationID, operation.Platform).
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
	result, err := verifyGeminiVeoTaskManualReviewRetry(reservationID, eventID)
	if err != nil || result == nil {
		if err == nil {
			err = billingsvc.ErrRelayQuotaReservationBusy
		}
		return nil, err
	}
	result.Changed = changed
	return result, nil
}

func validateActiveGeminiVeoTaskPollReview(record *model.RelayQuotaReservationRecord, task *model.Task,
	operation *model.TaskOperation) error {
	if record == nil || task == nil || operation == nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle || record.ChannelID != task.ChannelId ||
		record.CompletedAt <= 0 || record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
		!model.IsVeoTaskOperationPlatform(task.Platform) || task.TaskID != operation.TaskID || task.UserId != record.UserID ||
		task.Status != model.TaskStatusUnknown || task.Quota != record.ActualQuota || task.FinishTime <= 0 ||
		operation.Platform != task.Platform || operation.ReservationID != record.ReservationID ||
		operation.UserID != task.UserId || operation.ChannelID != task.ChannelId ||
		operation.State != model.TaskOperationManualReview || operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.NextAttemptAt != 0 ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.CompletedAt <= 0 {
		return fmt.Errorf("%w: GeminiVeo polling review tuple is inconsistent", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID || privateData.SettlementPending ||
		privateData.EncryptedProviderTaskID != operation.EncryptedProviderTaskID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID {
		return fmt.Errorf("%w: GeminiVeo recovery metadata does not match accounting", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := validateGeminiVeoTaskPricingSnapshot(task, privateData, record.ActualQuota); err != nil {
		return fmt.Errorf("%w: GeminiVeo pricing snapshots do not match accounting", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	properties, propertiesErr := decodeGeminiVeoTaskProperties(task.Properties)
	if propertiesErr != nil {
		return fmt.Errorf("%w: Gemini Veo pricing properties are invalid", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	settled, err := privateData.Pricing.quota(properties.UpstreamModelName, properties.Resolution)
	if err != nil || settled != record.ActualQuota {
		return fmt.Errorf("%w: GeminiVeo pricing snapshot does not match accounting", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	descriptor, ok := veoTaskProviderByPlatform(task.Platform)
	if !ok || descriptor.Family != properties.Family ||
		descriptor.ValidateRoutingSnapshot(privateData.RoutingSnapshot, properties.UpstreamModelName) != nil {
		return fmt.Errorf("%w: GeminiVeo provider endpoint is invalid", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	credential, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		geminiVeoChannelCredentialBinding(task.TaskID, task.Platform, task.UserId, task.ChannelId,
			privateData.RoutingSnapshot))
	if err != nil || credential == "" || len(credential) > descriptor.MaxCredentialBytes {
		return fmt.Errorf("%w: GeminiVeo channel credential is not recoverable", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	binding := geminiVeoProviderTaskBinding(task.TaskID, operation.ReservationID, task.Platform, task.UserId, task.ChannelId)
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || descriptor.ValidateProviderTaskID(privateData.RoutingSnapshot,
			properties.UpstreamModelName, providerID) != nil {
			return fmt.Errorf("%w: GeminiVeo provider id is not recoverable", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
	}
	return nil
}

func verifyGeminiVeoTaskManualReviewRetry(reservationID, eventID string) (*GeminiVeoTaskManualReviewRetryResult, error) {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	var operations []model.TaskOperation
	if err := model.DB.Where("reservation_id = ? AND platform IN ?", reservationID, model.VeoTaskOperationPlatforms()).
		Limit(2).Find(&operations).Error; err != nil {
		return nil, err
	}
	if len(operations) != 1 {
		return nil, billingsvc.ErrRelayQuotaReviewInvalidState
	}
	operation := operations[0]
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", operation.TaskID, operation.Platform).
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
	return &GeminiVeoTaskManualReviewRetryResult{ReservationID: reservationID, TaskID: task.TaskID,
		ReservationStatus: record.Status, TaskStatus: task.Status, OperationState: operation.State,
		AuditEventID: event.EventID}, nil
}
