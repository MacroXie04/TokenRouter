package tasks

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/sora"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"sync"
	"unicode/utf8"
)

// SQLite cannot reliably upgrade simultaneous read transactions to writers.
// Production databases additionally serialize this action on the reservation
// row and exact task/journal compare-and-swap predicates.
var videoTaskManualReviewRetryMu sync.Mutex

// VideoTaskManualReviewRetryResult is a secret-free receipt for an operator
// reopening provider polling. The reservation status remains terminal: this
// action never charges, refunds, or otherwise rewrites accounting.
type VideoTaskManualReviewRetryResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

// RetryVideoTaskManualReview atomically returns a settled OpenAI video task
// and its provider poll journal to the submitted queue. Both authenticated
// copies of the provider id (when present), the snapshotted channel credential,
// task/accounting coordinates, and recovery lease are validated first.
func RetryVideoTaskManualReview(
	reservationID string,
	operatorUserID int,
) (*VideoTaskManualReviewRetryResult, error) {
	if !validVideoReviewReservationID(reservationID) || operatorUserID <= 0 {
		return nil, billingsvc.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("video review database is unavailable")
	}

	videoTaskManualReviewRetryMu.Lock()
	defer videoTaskManualReviewRetryMu.Unlock()

	eventID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	changed := false
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return billingsvc.ErrRelayQuotaReviewNotFound
			}
			return err
		}

		var operation model.TaskOperation
		operationResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ? AND platform IN ?", reservationID,
				model.OpenAIVideoTaskOperationPlatforms()).Limit(1).Find(&operation)
		if operationResult.Error != nil {
			return operationResult.Error
		}
		if operationResult.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReviewNotFound
		}
		// A reservation-level accounting review must continue through the
		// pre-existing retry path before provider polling can be considered.
		if record.Status == model.RelayQuotaReservationStatusManualReview {
			return billingsvc.ErrRelayQuotaReviewNotFound
		}

		var tasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND platform = ?", operation.TaskID,
				operation.Platform).Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return fmt.Errorf("%w: video task identity is unavailable or ambiguous",
				billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
		task := tasks[0]

		priorEvent, priorFound, err := billingsvc.FindRelayQuotaTaskPollRetryEventTx(tx, reservationID)
		if err != nil {
			return err
		}
		if record.Status != model.RelayQuotaReservationStatusSettled ||
			operation.State != model.TaskOperationManualReview {
			if priorFound && videoTaskPollRetryEventMatches(priorEvent, &record, &task, &operation) {
				eventID = priorEvent.EventID
				changed = false
				return nil
			}
			return fmt.Errorf("%w: video task is not awaiting a polling retry",
				billingsvc.ErrRelayQuotaReviewInvalidState)
		}

		if err := validateActiveVideoTaskPollReview(&record, &task, &operation); err != nil {
			return err
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		event, err := billingsvc.AppendRelayQuotaTaskPollRetryEventTx(
			tx, &record, &task, &operation, operatorUserID, eventID, now,
		)
		if err != nil {
			return err
		}

		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
				task.ID, task.TaskID, task.Platform, task.UserId, task.ChannelId, task.Quota).
			Where("status = ? AND private_data = ? AND properties = ? AND finish_time = ?",
				model.TaskStatusUnknown, task.PrivateData, task.Properties, task.FinishTime).
			Updates(map[string]any{
				"status": model.TaskStatusSubmitted, "fail_reason": "", "finish_time": 0,
				"updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}

		operationResult = tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ?",
				operation.ID, operation.TaskID, operation.ReservationID, operation.Platform).
			Where("user_id = ? AND channel_id = ? AND state = ? AND settlement_pending = ?",
				operation.UserID, operation.ChannelID, model.TaskOperationManualReview, false).
			Where("encrypted_provider_task_id = ? AND attempts = ? AND next_attempt_at = ?",
				operation.EncryptedProviderTaskID, operation.Attempts, operation.NextAttemptAt).
			Where("lease_owner = ? AND lease_expires_at = ? AND last_error = ?",
				"", int64(0), operation.LastError).
			Where("created_at = ? AND completed_at = ?", operation.CreatedAt, operation.CompletedAt).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": false,
				"attempts": 0, "next_attempt_at": now, "created_at": now,
				"completed_at": 0, "updated_at": now, "last_error": "",
				"lease_owner": "", "lease_expires_at": 0,
			})
		if operationResult.Error != nil {
			return operationResult.Error
		}
		if operationResult.RowsAffected != 1 {
			return billingsvc.ErrRelayQuotaReservationBusy
		}
		eventID = event.EventID
		changed = true
		return nil
	})
	if err != nil {
		if errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid) ||
			errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) ||
			errors.Is(err, billingsvc.ErrRelayQuotaReviewInvalidState) ||
			errors.Is(err, billingsvc.ErrRelayQuotaReviewUnsafeRetry) {
			return nil, err
		}
		if verified, verifyErr := verifyVideoTaskManualReviewRetry(reservationID, eventID); verifyErr == nil && verified != nil {
			verified.Changed = true
			return verified, nil
		}
		// Another node may have won the exact compare-and-swap with a different
		// random event id. Its latest consistent audit proves an idempotent
		// replay; an unsafe row still cannot match while it remains in review.
		if verified, verifyErr := verifyVideoTaskManualReviewRetry(reservationID, ""); verifyErr == nil && verified != nil {
			verified.Changed = false
			return verified, nil
		}
		return nil, err
	}
	verified, err := verifyVideoTaskManualReviewRetry(reservationID, eventID)
	if err != nil {
		return nil, err
	}
	if verified == nil {
		return nil, billingsvc.ErrRelayQuotaReservationBusy
	}
	verified.Changed = changed
	return verified, nil
}

func validateActiveVideoTaskPollReview(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	if record == nil || task == nil || operation == nil ||
		record.Status != model.RelayQuotaReservationStatusSettled ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ChannelID <= 0 || record.CompletedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
		task.TaskID == "" || task.TaskID != operation.TaskID ||
		!model.IsOpenAIVideoTaskOperationPlatform(task.Platform) ||
		task.UserId != record.UserID || task.ChannelId != record.ChannelID ||
		task.Quota != record.ActualQuota || task.Status != model.TaskStatusUnknown ||
		task.FinishTime <= 0 || operation.ReservationID != record.ReservationID ||
		operation.Platform != task.Platform || operation.UserID != task.UserId ||
		operation.ChannelID != task.ChannelId || operation.State != model.TaskOperationManualReview ||
		operation.SettlementPending || operation.Attempts < 0 || operation.NextAttemptAt != 0 ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.CreatedAt <= 0 || operation.CompletedAt <= 0 {
		return fmt.Errorf("%w: video task review tuple is inconsistent",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := decodeVideoTaskProperties(task.Properties); err != nil {
		return fmt.Errorf("%w: video task properties are invalid",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource ||
		privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch ||
		privateData.TokenID != record.TokenID || privateData.SettlementPending {
		return fmt.Errorf("%w: video recovery metadata does not match accounting",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	baseURL, err := sora.EffectiveBaseURL(privateData.ChannelBaseURL, -1)
	if err != nil || baseURL != privateData.ChannelBaseURL {
		return fmt.Errorf("%w: video provider endpoint is invalid",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if len(privateData.EncryptedChannelKey) > videoTaskEncryptedSecretMaxBytes {
		return fmt.Errorf("%w: video channel credential is invalid",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	channelKey, err := asyncTaskDecryptBound(
		privateData.EncryptedChannelKey,
		videoChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL),
	)
	if err != nil || strings.TrimSpace(channelKey) == "" {
		return fmt.Errorf("%w: video channel credential is not recoverable",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}

	binding := videoProviderTaskBinding(
		operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID,
	)
	providerIDs := make([]string, 0, 2)
	for _, encrypted := range []string{
		operation.EncryptedProviderTaskID,
		privateData.EncryptedUpstreamTaskID,
	} {
		if strings.TrimSpace(encrypted) == "" {
			continue
		}
		if len(encrypted) > videoTaskEncryptedSecretMaxBytes {
			return fmt.Errorf("%w: video provider id is not recoverable",
				billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
		providerID, decryptErr := asyncTaskDecryptBound(encrypted, binding)
		if decryptErr != nil || !validVideoReviewProviderTaskID(providerID) {
			return fmt.Errorf("%w: video provider id is not recoverable",
				billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
		providerIDs = append(providerIDs, providerID)
	}
	if len(providerIDs) == 0 {
		return fmt.Errorf("%w: video provider id is unavailable",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if len(providerIDs) == 2 && providerIDs[0] != providerIDs[1] {
		return fmt.Errorf("%w: durable video provider ids conflict",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	return nil
}

func verifyVideoTaskManualReviewRetry(
	reservationID string,
	eventID string,
) (*VideoTaskManualReviewRetryResult, error) {
	if model.DB == nil {
		return nil, errors.New("video review database is unavailable")
	}
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	var operation model.TaskOperation
	if err := model.DB.Where("reservation_id = ? AND platform IN ?", reservationID,
		model.OpenAIVideoTaskOperationPlatforms()).First(&operation).Error; err != nil {
		return nil, err
	}
	var task model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", operation.TaskID,
		operation.Platform).First(&task).Error; err != nil {
		return nil, err
	}
	var event model.RelayQuotaReservationReviewEvent
	query := model.DB.Where("reservation_id = ? AND action = ?", reservationID,
		model.RelayQuotaReservationReviewActionRetryTaskPoll)
	if eventID != "" {
		query = query.Where("event_id = ?", eventID)
	}
	result := query.Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 || !videoTaskPollRetryEventMatches(&event, &record, &task, &operation) {
		return nil, nil
	}
	return &VideoTaskManualReviewRetryResult{
		ReservationID: record.ReservationID, TaskID: task.TaskID,
		ReservationStatus: record.Status, TaskStatus: task.Status,
		OperationState: operation.State, AuditEventID: event.EventID,
	}, nil
}

func videoTaskPollRetryEventMatches(
	event *model.RelayQuotaReservationReviewEvent,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if event == nil || record == nil || task == nil || operation == nil ||
		event.Action != model.RelayQuotaReservationReviewActionRetryTaskPoll ||
		event.ReservationID != record.ReservationID || event.OperatorUserID <= 0 ||
		event.FromStatus != record.Status || event.ToStatus != record.Status ||
		event.Operation != model.RelayQuotaReservationOperationSettle ||
		event.TaskID != task.TaskID || event.TaskPlatform != task.Platform ||
		event.TaskFromStatus != model.TaskStatusUnknown ||
		event.TaskToStatus != model.TaskStatusSubmitted ||
		event.TaskOperationFromState != model.TaskOperationManualReview ||
		event.TaskOperationToState != model.TaskOperationSubmitted ||
		event.TaskOperationCreatedAt <= 0 || event.TaskOperationAttempts < 0 ||
		event.UserID != record.UserID || event.TokenID != record.TokenID ||
		event.TokenUnlimited != record.TokenUnlimited ||
		event.TrustQuotaBypassed != record.TrustQuotaBypassed ||
		event.ChannelID != record.ChannelID || event.FundingSource != record.FundingSource ||
		event.RequestedQuota != record.RequestedQuota || event.ReservedQuota != record.ReservedQuota ||
		event.TokenReserved != record.TokenReserved || event.SubscriptionID != record.SubscriptionID ||
		event.UsageEpoch != record.UsageEpoch || event.ActualQuota != record.ActualQuota ||
		operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		operation.Platform != event.TaskPlatform || operation.UserID != record.UserID ||
		operation.ChannelID != task.ChannelId {
		return false
	}
	if record.Operation != model.RelayQuotaReservationOperationSettle {
		return false
	}
	switch record.Status {
	case model.RelayQuotaReservationStatusSettled:
		if record.CompletedAt <= 0 || record.ChannelID != task.ChannelId ||
			event.ChannelID != operation.ChannelID || operation.SettlementPending {
			return false
		}
	case model.RelayQuotaReservationStatusDispatched:
		if record.CompletedAt != 0 || record.ChannelID != 0 || event.ChannelID != 0 || !operation.SettlementPending {
			return false
		}
	default:
		return false
	}
	switch operation.State {
	case model.TaskOperationSubmitted:
		return task.Status == model.TaskStatusSubmitted || task.Status == model.TaskStatusQueued ||
			task.Status == model.TaskStatusRunning
	case model.TaskOperationTerminal:
		return task.Status == model.TaskStatusSuccess
	case model.TaskOperationReversed:
		return task.Status == model.TaskStatusFailure
	default:
		return false
	}
}

func validVideoReviewReservationID(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validVideoReviewProviderTaskID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 191 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}
