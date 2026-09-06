package relay

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	klingprovider "github.com/tokenrouter/tokenrouter/relay/channel/kling"
	doubaoprovider "github.com/tokenrouter/tokenrouter/relay/channel/task/doubao"
	"github.com/tokenrouter/tokenrouter/service"
)

var (
	klingTaskManualReviewRetryMu  sync.Mutex
	doubaoTaskManualReviewRetryMu sync.Mutex
)

type HeldAsyncTaskManualReviewRetryResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

type heldAsyncTaskPollRetrySpec struct {
	name                string
	platforms           []string
	reviewReason        string
	mu                  *sync.Mutex
	consumeAuditEventID func(string) string
	validate            func(*model.RelayQuotaReservationRecord, *model.Task, *model.TaskOperation) error
}

// RetryKlingTaskManualReview resumes only a known Kling provider task. The
// usage-metered reservation remains dispatched until an authoritative terminal
// poll settles or refunds it.
func RetryKlingTaskManualReview(reservationID string, operatorUserID int) (*HeldAsyncTaskManualReviewRetryResult, error) {
	return retryHeldAsyncTaskPollReview(reservationID, operatorUserID, heldAsyncTaskPollRetrySpec{
		name: "Kling", platforms: model.KlingTaskOperationPlatforms(),
		reviewReason: klingPollingManualReviewReason, mu: &klingTaskManualReviewRetryMu,
		consumeAuditEventID: klingAuditEventID, validate: validateActiveKlingTaskPollReview,
	})
}

// RetryDoubaoTaskManualReview resumes only a known Doubao provider task. The
// selected platform must be one of the exact durable Doubao channel values;
// model names are never used to infer provenance.
func RetryDoubaoTaskManualReview(reservationID string, operatorUserID int) (*HeldAsyncTaskManualReviewRetryResult, error) {
	return retryHeldAsyncTaskPollReview(reservationID, operatorUserID, heldAsyncTaskPollRetrySpec{
		name: "Doubao", platforms: model.DoubaoVideoTaskOperationPlatforms(),
		reviewReason: doubaoPollingManualReviewReason, mu: &doubaoTaskManualReviewRetryMu,
		consumeAuditEventID: doubaoAuditEventID, validate: validateActiveDoubaoTaskPollReview,
	})
}

func retryHeldAsyncTaskPollReview(
	reservationID string,
	operatorUserID int,
	spec heldAsyncTaskPollRetrySpec,
) (*HeldAsyncTaskManualReviewRetryResult, error) {
	if !validVideoReviewReservationID(reservationID) || operatorUserID <= 0 || spec.name == "" ||
		len(spec.platforms) == 0 || spec.mu == nil || spec.consumeAuditEventID == nil ||
		spec.validate == nil || spec.reviewReason == "" {
		return nil, service.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, fmt.Errorf("%s review database is unavailable", spec.name)
	}
	spec.mu.Lock()
	defer spec.mu.Unlock()
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
			Where("reservation_id = ? AND platform IN ?", reservationID, spec.platforms).
			Limit(2).Find(&operations).Error; err != nil {
			return err
		}
		if len(operations) == 0 {
			return service.ErrRelayQuotaReviewNotFound
		}
		if len(operations) != 1 {
			return fmt.Errorf("%w: %s operation identity is ambiguous", service.ErrRelayQuotaReviewUnsafeRetry, spec.name)
		}
		operation := &operations[0]
		var tasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND platform = ?", operation.TaskID, operation.Platform).
			Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return fmt.Errorf("%w: %s task identity is unavailable or ambiguous", service.ErrRelayQuotaReviewUnsafeRetry, spec.name)
		}
		task := &tasks[0]
		prior, found, err := service.FindRelayQuotaTaskPollRetryEventTx(tx, reservationID)
		if err != nil {
			return err
		}
		if record.Status != model.RelayQuotaReservationStatusDispatched ||
			operation.State != model.TaskOperationManualReview {
			if found && videoTaskPollRetryEventMatches(prior, &record, task, operation) {
				eventID = prior.EventID
				return nil
			}
			return fmt.Errorf("%w: %s task is not awaiting a polling retry",
				service.ErrRelayQuotaReviewInvalidState, spec.name)
		}
		if err := spec.validate(&record, task, operation); err != nil {
			return err
		}
		var consumeAuditCount int64
		if err := tx.Model(&model.AuditLogOutbox{}).
			Where("event_id = ?", spec.consumeAuditEventID(record.ReservationID)).
			Count(&consumeAuditCount).Error; err != nil {
			return err
		}
		if consumeAuditCount != 0 {
			return fmt.Errorf("%w: %s accounting audit already exists",
				service.ErrRelayQuotaReviewUnsafeRetry, spec.name)
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
				task.ID, task.TaskID, operation.Platform, task.UserId, task.ChannelId, task.Quota).
			Where("status = ? AND fail_reason = ? AND private_data = ? AND properties = ? AND finish_time = ?",
				model.TaskStatusUnknown, spec.reviewReason, task.PrivateData, task.Properties, task.FinishTime).
			Updates(map[string]any{"status": model.TaskStatusSubmitted, "fail_reason": "",
				"finish_time": 0, "updated_at": now})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return errors.Join(taskResult.Error,
				fmt.Errorf("%w: %s task snapshot changed", service.ErrRelayQuotaReservationBusy, spec.name))
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ?", operation.ID,
				operation.TaskID, operation.ReservationID, operation.Platform).
			Where("user_id = ? AND channel_id = ? AND state = ? AND settlement_pending = ?",
				operation.UserID, operation.ChannelID, model.TaskOperationManualReview, true).
			Where("encrypted_provider_task_id = ? AND attempts = ? AND next_attempt_at = ?",
				operation.EncryptedProviderTaskID, operation.Attempts, operation.NextAttemptAt).
			Where("lease_owner = ? AND lease_expires_at = ? AND last_error = ?",
				"", int64(0), spec.reviewReason).
			Updates(map[string]any{"state": model.TaskOperationSubmitted, "attempts": 0,
				"next_attempt_at": now, "created_at": now, "completed_at": 0, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0})
		if opResult.Error != nil || opResult.RowsAffected != 1 {
			return errors.Join(opResult.Error,
				fmt.Errorf("%w: %s operation snapshot changed", service.ErrRelayQuotaReservationBusy, spec.name))
		}
		eventID = event.EventID
		changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	result, err := verifyHeldAsyncTaskPollRetry(reservationID, eventID, spec)
	if err != nil || result == nil {
		if err == nil {
			err = service.ErrRelayQuotaReservationBusy
		}
		return nil, err
	}
	result.Changed = changed
	return result, nil
}

func validateActiveKlingTaskPollReview(record *model.RelayQuotaReservationRecord, task *model.Task,
	operation *model.TaskOperation) error {
	if err := validateHeldTaskPollReviewTuple(record, task, operation, klingTaskPlatform,
		klingPollingManualReviewReason); err != nil {
		return fmt.Errorf("%w: Kling %v", service.ErrRelayQuotaReviewUnsafeRetry, err)
	}
	if !klingTaskOperationIdentityMatches(task, operation, record.ReservationID) {
		return fmt.Errorf("%w: Kling task identity is inconsistent", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil || task.Action != string(properties.Action) {
		return fmt.Errorf("%w: Kling task provenance is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID || !privateData.SettlementPending ||
		privateData.EncryptedUpstreamTaskID != operation.EncryptedProviderTaskID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID {
		return fmt.Errorf("%w: Kling recovery metadata does not match accounting", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	preconsume, err := privateData.Pricing.PreConsumeQuota()
	if err != nil || preconsume != record.RequestedQuota || privateData.Pricing.ModelName != properties.OriginModelName {
		return fmt.Errorf("%w: Kling pricing snapshot does not match accounting", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		klingChannelCredentialBinding(task.TaskID, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") {
		return fmt.Errorf("%w: Kling channel credential is not recoverable", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	baseURL, _, err := klingprovider.EffectiveBaseURL(privateData.ChannelBaseURL, key)
	if err != nil || baseURL != privateData.ChannelBaseURL {
		return fmt.Errorf("%w: Kling provider endpoint is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	binding := klingProviderTaskBinding(task.TaskID, record.ReservationID, task.UserId, task.ChannelId, properties.Action)
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedUpstreamTaskID} {
		providerID, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || !validKlingRecoveryOpaqueID(providerID, klingprovider.MaxProviderTaskIDBytes) {
			return fmt.Errorf("%w: Kling provider id is not recoverable", service.ErrRelayQuotaReviewUnsafeRetry)
		}
	}
	if _, err := decodeKlingStoredTaskData(task.Data); err != nil {
		return fmt.Errorf("%w: Kling provider state is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	return nil
}

func validateActiveDoubaoTaskPollReview(record *model.RelayQuotaReservationRecord, task *model.Task,
	operation *model.TaskOperation) error {
	if !model.IsDoubaoVideoTaskOperationPlatform(operation.Platform) || operation.Platform != task.Platform {
		return fmt.Errorf("%w: Doubao platform is unsupported", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if err := validateHeldTaskPollReviewTuple(record, task, operation, operation.Platform,
		doubaoPollingManualReviewReason); err != nil {
		return fmt.Errorf("%w: Doubao %v", service.ErrRelayQuotaReviewUnsafeRetry, err)
	}
	if !doubaoTaskOperationIdentityMatches(task, operation, record.ReservationID) {
		return fmt.Errorf("%w: Doubao task identity is inconsistent", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || task.Action != string(properties.Action) {
		return fmt.Errorf("%w: Doubao task provenance is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID || !privateData.SettlementPending ||
		privateData.EncryptedUpstreamTaskID != operation.EncryptedProviderTaskID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID {
		return fmt.Errorf("%w: Doubao recovery metadata does not match accounting", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	preconsume, err := privateData.Pricing.PreConsumeQuota()
	if err != nil || preconsume != record.RequestedQuota || privateData.Pricing.ModelName != properties.OriginModelName {
		return fmt.Errorf("%w: Doubao pricing snapshot does not match accounting", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	baseURL, err := doubaoprovider.EffectiveBaseURL(privateData.ChannelBaseURL)
	if err != nil || baseURL != privateData.ChannelBaseURL {
		return fmt.Errorf("%w: Doubao provider endpoint is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	key, err := asyncTaskDecryptBound(privateData.EncryptedChannelKey,
		doubaoChannelCredentialBinding(task.TaskID, task.Platform, task.UserId, task.ChannelId, privateData.ChannelBaseURL))
	if err != nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") {
		return fmt.Errorf("%w: Doubao channel credential is not recoverable", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	binding := doubaoProviderTaskBinding(task.TaskID, record.ReservationID, task.Platform,
		task.UserId, task.ChannelId, properties.Action)
	for _, encrypted := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedUpstreamTaskID} {
		providerID, err := asyncTaskDecryptBound(encrypted, binding)
		if err != nil || !validDoubaoRecoveryOpaqueID(providerID, doubaoprovider.MaxProviderTaskIDBytes) {
			return fmt.Errorf("%w: Doubao provider id is not recoverable", service.ErrRelayQuotaReviewUnsafeRetry)
		}
	}
	if _, err := decodeDoubaoStoredTaskData(task.Data); err != nil {
		return fmt.Errorf("%w: Doubao provider state is invalid", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	return nil
}

func validateHeldTaskPollReviewTuple(record *model.RelayQuotaReservationRecord, task *model.Task,
	operation *model.TaskOperation, platform, reviewReason string) error {
	if record == nil || task == nil || operation == nil ||
		record.Status != model.RelayQuotaReservationStatusDispatched ||
		record.Operation != model.RelayQuotaReservationOperationSettle || record.ChannelID != 0 ||
		record.ActualQuota != record.ReservedQuota || record.CompletedAt != 0 || record.DispatchedAt <= 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 ||
		task.Platform != platform || task.TaskID != operation.TaskID || task.UserId != record.UserID ||
		task.Status != model.TaskStatusUnknown || task.FailReason != reviewReason ||
		task.Quota != record.RequestedQuota || task.FinishTime <= 0 ||
		operation.Platform != platform || operation.ReservationID != record.ReservationID ||
		operation.UserID != task.UserId || operation.ChannelID != task.ChannelId ||
		operation.State != model.TaskOperationManualReview || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID == "" || operation.LastError != reviewReason ||
		operation.NextAttemptAt != 0 || operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.CompletedAt <= 0 {
		return errors.New("polling review tuple is inconsistent")
	}
	return nil
}

func verifyHeldAsyncTaskPollRetry(reservationID, eventID string,
	spec heldAsyncTaskPollRetrySpec) (*HeldAsyncTaskManualReviewRetryResult, error) {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	var operations []model.TaskOperation
	if err := model.DB.Where("reservation_id = ? AND platform IN ?", reservationID, spec.platforms).
		Limit(2).Find(&operations).Error; err != nil {
		return nil, err
	}
	if len(operations) != 1 {
		return nil, service.ErrRelayQuotaReviewUnsafeRetry
	}
	operation := &operations[0]
	var tasks []model.Task
	if err := model.DB.Where("task_id = ? AND platform = ?", operation.TaskID, operation.Platform).
		Limit(2).Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) != 1 {
		return nil, service.ErrRelayQuotaReviewUnsafeRetry
	}
	task := &tasks[0]
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
