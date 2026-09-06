package tasks

import (
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"sync"
)

const (
	klingManualSettlementReason = "unresolved Kling submission was settled by operator review"
	klingManualRefundReason     = "unresolved Kling submission was refunded by operator review"
)

// SQLite cannot reliably upgrade concurrent read transactions into writers.
// Cross-process races remain fenced by the durable reservation and task CAS
// predicates used below.
var klingManualReviewResolveMu sync.Mutex

// KlingManualReviewResolutionResult deliberately contains no channel
// credential or provider identifier. The audit identity proves that the
// accounting decision and task transition committed together.
type KlingManualReviewResolutionResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	Resolution        string `json:"resolution"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

type klingManualReviewTxOutcome struct {
	taskID       string
	auditEventID string
}

// ResolveKlingRelayQuotaReservationReview terminally resolves only the exact
// no-provider-ID ambiguous-submit tuple. The caller chooses settle or refund;
// the amount, funding coordinates, and provider channel are read from the
// immutable reservation and task snapshot and cannot be supplied or changed.
func ResolveKlingRelayQuotaReservationReview(
	reservationID string,
	operatorUserID int,
	resolution string,
) (*KlingManualReviewResolutionResult, error) {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if !validKlingReviewReservationID(reservationID) || operatorUserID <= 0 ||
		(resolution != billingsvc.RelayQuotaReviewResolutionSettle &&
			resolution != billingsvc.RelayQuotaReviewResolutionRefund) {
		return nil, billingsvc.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("Kling review database is unavailable")
	}

	klingManualReviewResolveMu.Lock()
	defer klingManualReviewResolveMu.Unlock()

	var source model.RelayQuotaReservationRecord
	result := model.DB.Where("reservation_id = ?", reservationID).Limit(1).Find(&source)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, billingsvc.ErrRelayQuotaReviewNotFound
	}
	var ownership model.TaskOperation
	result = model.DB.Where("reservation_id = ? AND platform = ?", reservationID, klingTaskPlatform).
		Limit(1).Find(&ownership)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, billingsvc.ErrRelayQuotaReviewNotFound
	}
	if source.Status != model.RelayQuotaReservationStatusDispatched {
		if (source.Status == model.RelayQuotaReservationStatusSettled &&
			resolution == billingsvc.RelayQuotaReviewResolutionSettle) ||
			(source.Status == model.RelayQuotaReservationStatusRefunded &&
				resolution == billingsvc.RelayQuotaReviewResolutionRefund) {
			verified, err := verifyKlingManualReviewResolution(reservationID, resolution)
			if err != nil {
				return nil, err
			}
			verified.Changed = false
			return verified, nil
		}
		return nil, fmt.Errorf("%w: reservation is %s", billingsvc.ErrRelayQuotaReviewInvalidState, source.Status)
	}

	operation, task, _, err := loadActiveKlingManualReview(model.DB, &source)
	if err != nil {
		return nil, err
	}
	reservation, err := billingsvc.RestoreRelayQuotaReservation(reservationID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid durable reservation", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	eventID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	outcome := klingManualReviewTxOutcome{}
	persist := func(tx *gorm.DB) error {
		var persistErr error
		outcome, persistErr = resolveKlingManualReviewTx(
			tx, &source, task.TaskID, operation.ID, operatorUserID, resolution, eventID,
		)
		return persistErr
	}
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		err = reservation.SettleWithChannelAndPersistence(source.RequestedQuota, task.ChannelId, persist)
	} else {
		err = reservation.RefundWithPersistence(persist)
	}
	if err != nil {
		return nil, classifyKlingManualReviewResolutionError(err)
	}
	verified, err := verifyKlingManualReviewResolution(reservationID, resolution)
	if err != nil {
		return nil, err
	}
	verified.Changed = outcome.taskID == verified.TaskID && outcome.auditEventID == verified.AuditEventID
	_ = removeKlingRecoveryJournal(verified.TaskID)
	return verified, nil
}

func resolveKlingManualReviewTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	expectedTaskID string,
	expectedOperationID int64,
	operatorUserID int,
	resolution, eventID string,
) (klingManualReviewTxOutcome, error) {
	var record model.RelayQuotaReservationRecord
	if err := tx.Where("reservation_id = ?", source.ReservationID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return klingManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReviewNotFound
		}
		return klingManualReviewTxOutcome{}, err
	}
	if !klingReviewSourceRecordMatches(source, &record) {
		return klingManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}
	operation, task, privateData, err := loadActiveKlingManualReview(tx, &record)
	if err != nil {
		return klingManualReviewTxOutcome{}, err
	}
	if task.TaskID != expectedTaskID || operation.ID != expectedOperationID {
		return klingManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}
	var existingAuditCount int64
	if err := tx.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", klingAuditEventID(record.ReservationID)).
		Count(&existingAuditCount).Error; err != nil {
		return klingManualReviewTxOutcome{}, err
	}
	if existingAuditCount != 0 {
		return klingManualReviewTxOutcome{}, fmt.Errorf("%w: consume audit already exists for unresolved Kling hold",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}

	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return klingManualReviewTxOutcome{}, err
	}
	privateData.SettlementPending = false
	privateJSON, err := marshalKlingTaskPrivateData(privateData)
	if err != nil {
		return klingManualReviewTxOutcome{}, fmt.Errorf("%w: Kling terminal metadata is invalid",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	taskStatus := model.TaskStatusUnknown
	operationState := model.TaskOperationUnknown
	failReason := klingManualSettlementReason
	actualQuota := task.Quota
	if resolution == billingsvc.RelayQuotaReviewResolutionRefund {
		taskStatus = model.TaskStatusFailure
		operationState = model.TaskOperationRefunded
		failReason = klingManualRefundReason
		actualQuota = 0
	}
	taskResult := tx.Model(&model.Task{}).
		Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
			task.ID, task.TaskID, klingTaskPlatform, record.UserID, task.ChannelId, record.RequestedQuota).
		Where("status = ? AND fail_reason = ? AND private_data = ? AND data = ?",
			model.TaskStatusUnknown, klingUnknownDispatchReason, task.PrivateData, "null").
		Updates(map[string]any{
			"status": taskStatus, "quota": actualQuota, "fail_reason": failReason,
			"private_data": privateJSON, "progress": "100%", "finish_time": now, "updated_at": now,
		})
	if taskResult.Error != nil {
		return klingManualReviewTxOutcome{}, taskResult.Error
	}
	if taskResult.RowsAffected != 1 {
		return klingManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}
	opResult := tx.Model(&model.TaskOperation{}).
		Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
			operation.ID, task.TaskID, record.ReservationID, klingTaskPlatform, record.UserID, task.ChannelId).
		Where("state = ? AND settlement_pending = ? AND encrypted_provider_task_id = ?",
			model.TaskOperationManualReview, true, "").
		Where("last_error = ? AND next_attempt_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			klingUnknownDispatchReason, int64(0), "", int64(0)).
		Updates(map[string]any{
			"state": operationState, "settlement_pending": false,
			"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
			"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
		})
	if opResult.Error != nil {
		return klingManualReviewTxOutcome{}, opResult.Error
	}
	if opResult.RowsAffected != 1 {
		return klingManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		auditTask := *task
		auditTask.Status = taskStatus
		auditTask.FailReason = failReason
		auditTask.PrivateData = privateJSON
		auditTask.FinishTime = now
		auditTask.UpdatedAt = now
		audit, err := newKlingConsumeAudit(auditTask, privateData, record.ReservationID, actualQuota)
		if err != nil {
			return klingManualReviewTxOutcome{}, err
		}
		if err := billingsvc.EnqueueAuditLogTx(tx, audit); err != nil {
			return klingManualReviewTxOutcome{}, err
		}
	}
	reviewEvent, err := billingsvc.AppendRelayQuotaReservationResolutionEventTx(
		tx, &record, operatorUserID, resolution, actualQuota, task.ChannelId, eventID,
	)
	if err != nil {
		return klingManualReviewTxOutcome{}, err
	}
	return klingManualReviewTxOutcome{taskID: task.TaskID, auditEventID: reviewEvent.EventID}, nil
}

func loadActiveKlingManualReview(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
) (*model.TaskOperation, *model.Task, klingTaskPrivateData, error) {
	var operation model.TaskOperation
	result := tx.Where("reservation_id = ? AND platform = ?", record.ReservationID, klingTaskPlatform).
		Limit(1).Find(&operation)
	if result.Error != nil {
		return nil, nil, klingTaskPrivateData{}, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, klingTaskPrivateData{}, fmt.Errorf("%w: Kling recovery operation is unavailable",
			billingsvc.ErrRelayQuotaReviewNotFound)
	}
	var task model.Task
	result = tx.Where("task_id = ? AND platform = ?", operation.TaskID, klingTaskPlatform).Limit(1).Find(&task)
	if result.Error != nil {
		return nil, nil, klingTaskPrivateData{}, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, klingTaskPrivateData{}, fmt.Errorf("%w: Kling task is unavailable",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := validateActiveKlingManualReview(record, &task, &operation)
	if err != nil {
		return nil, nil, klingTaskPrivateData{}, err
	}
	return &operation, &task, privateData, nil
}

func validateActiveKlingManualReview(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (klingTaskPrivateData, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		record.Status != model.RelayQuotaReservationStatusDispatched ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != record.ReservedQuota || record.ChannelID != 0 ||
		record.DispatchedAt <= 0 || record.CompletedAt != 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return klingTaskPrivateData{}, fmt.Errorf("%w: reservation is not the retained Kling dispatched tuple",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if task.Platform != klingTaskPlatform || task.TaskID == "" || task.CreatedAt <= 0 ||
		task.Status != model.TaskStatusUnknown || task.FailReason != klingUnknownDispatchReason ||
		task.UserId != record.UserID || task.ChannelId <= 0 || task.Quota != record.RequestedQuota ||
		task.Data != "null" || task.FinishTime <= 0 {
		return klingTaskPrivateData{}, fmt.Errorf("%w: Kling task is not the unresolved review tuple",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		operation.Platform != klingTaskPlatform || operation.Platform != task.Platform ||
		operation.UserID != record.UserID || operation.ChannelID != task.ChannelId ||
		operation.State != model.TaskOperationManualReview || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != klingUnknownDispatchReason ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.NextAttemptAt != 0 ||
		operation.CompletedAt <= 0 {
		return klingTaskPrivateData{}, fmt.Errorf("%w: Kling recovery operation is not the unresolved review tuple",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID ||
		!privateData.SettlementPending || privateData.EncryptedUpstreamTaskID != "" ||
		privateData.CompletionUnits != 0 {
		return klingTaskPrivateData{}, fmt.Errorf("%w: Kling metadata does not match the reservation",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	precharge, err := privateData.Pricing.PreConsumeQuota()
	if err != nil || precharge != record.RequestedQuota {
		return klingTaskPrivateData{}, fmt.Errorf("%w: Kling pricing does not match the reservation",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := decodeKlingTaskProperties(task.Properties); err != nil {
		return klingTaskPrivateData{}, fmt.Errorf("%w: Kling task properties are invalid",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	return privateData, nil
}

func verifyKlingManualReviewResolution(
	reservationID, resolution string,
) (*KlingManualReviewResolutionResult, error) {
	var outcome *KlingManualReviewResolutionResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("reservation_id = ? AND platform = ?", reservationID, klingTaskPlatform).
			First(&operation).Error; err != nil {
			return err
		}
		var task model.Task
		if err := tx.Where("task_id = ? AND platform = ?", operation.TaskID, klingTaskPlatform).
			First(&task).Error; err != nil {
			return err
		}
		event, err := validateTerminalKlingManualReviewTx(tx, &record, &task, &operation, resolution)
		if err != nil {
			return err
		}
		outcome = &KlingManualReviewResolutionResult{
			ReservationID: record.ReservationID, TaskID: task.TaskID, Resolution: resolution,
			ReservationStatus: record.Status, TaskStatus: task.Status,
			OperationState: operation.State, AuditEventID: event.EventID,
		}
		return nil
	})
	return outcome, err
}

func validateTerminalKlingManualReviewTx(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
	resolution string,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		task.Platform != klingTaskPlatform || operation.Platform != klingTaskPlatform ||
		task.TaskID == "" || operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		task.UserId != record.UserID || operation.UserID != record.UserID ||
		task.ChannelId <= 0 || operation.ChannelID != task.ChannelId || task.FinishTime <= 0 ||
		record.CompletedAt <= 0 || operation.CompletedAt <= 0 || operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != "" ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.NextAttemptAt != 0 {
		return nil, fmt.Errorf("%w: terminal Kling resolution tuple is invalid",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID ||
		privateData.SettlementPending || privateData.EncryptedUpstreamTaskID != "" ||
		privateData.CompletionUnits != 0 {
		return nil, fmt.Errorf("%w: terminal Kling metadata is invalid", billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	precharge, err := privateData.Pricing.PreConsumeQuota()
	if err != nil || precharge != record.RequestedQuota {
		return nil, fmt.Errorf("%w: terminal Kling pricing is invalid", billingsvc.ErrRelayQuotaReviewInvalidState)
	}

	expectedActual := 0
	expectedTaskQuota := 0
	expectedTaskStatus := model.TaskStatusFailure
	expectedTaskReason := klingManualRefundReason
	expectedOperationState := model.TaskOperationRefunded
	expectedReservationStatus := model.RelayQuotaReservationStatusRefunded
	expectedReservationOperation := model.RelayQuotaReservationOperationRefund
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		expectedActual = record.RequestedQuota
		expectedTaskQuota = record.RequestedQuota
		expectedTaskStatus = model.TaskStatusUnknown
		expectedTaskReason = klingManualSettlementReason
		expectedOperationState = model.TaskOperationUnknown
		expectedReservationStatus = model.RelayQuotaReservationStatusSettled
		expectedReservationOperation = model.RelayQuotaReservationOperationSettle
	}
	if record.Status != expectedReservationStatus || record.Operation != expectedReservationOperation ||
		record.ActualQuota != expectedActual || task.Quota != expectedTaskQuota || task.Status != expectedTaskStatus ||
		task.FailReason != expectedTaskReason || task.Data != "null" || operation.State != expectedOperationState {
		return nil, fmt.Errorf("%w: terminal Kling disposition conflicts with requested resolution",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		if record.ChannelID != task.ChannelId {
			return nil, fmt.Errorf("%w: terminal Kling settlement channel mismatch",
				billingsvc.ErrRelayQuotaReviewInvalidState)
		}
		var auditCount int64
		if err := tx.Model(&model.AuditLogOutbox{}).
			Where("event_id = ?", klingAuditEventID(record.ReservationID)).Count(&auditCount).Error; err != nil {
			return nil, err
		}
		if auditCount != 1 {
			return nil, fmt.Errorf("%w: terminal Kling settlement audit is missing",
				billingsvc.ErrRelayQuotaReviewInvalidState)
		}
	} else if record.ChannelID != 0 {
		return nil, fmt.Errorf("%w: terminal Kling refund retained a settlement channel",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	event, found, err := billingsvc.FindRelayQuotaReservationResolutionEventTx(tx, record.ReservationID, resolution)
	if err != nil {
		return nil, err
	}
	if !found || !klingResolutionEventMatches(event, record, task, resolution, expectedActual) {
		return nil, fmt.Errorf("%w: terminal Kling resolution audit is missing or inconsistent",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	var resolutionEventCount int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", record.ReservationID,
			[]string{billingsvc.RelayQuotaReviewActionResolveSettle, billingsvc.RelayQuotaReviewActionResolveRefund}).
		Count(&resolutionEventCount).Error; err != nil {
		return nil, err
	}
	if resolutionEventCount != 1 {
		return nil, fmt.Errorf("%w: conflicting Kling terminal resolution audits exist",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	return event, nil
}

func klingResolutionEventMatches(
	event *model.RelayQuotaReservationReviewEvent,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	resolution string,
	expectedActual int,
) bool {
	if event == nil || record == nil || task == nil || event.ReservationID != record.ReservationID ||
		event.FromStatus != model.RelayQuotaReservationStatusDispatched || event.ToStatus != record.Status ||
		event.Operation != resolution || event.UserID != record.UserID || event.TokenID != record.TokenID ||
		event.TokenUnlimited != record.TokenUnlimited || event.TrustQuotaBypassed != record.TrustQuotaBypassed ||
		event.ChannelID != task.ChannelId || event.FundingSource != record.FundingSource ||
		event.RequestedQuota != record.RequestedQuota || event.ReservedQuota != record.ReservedQuota ||
		event.TokenReserved != record.TokenReserved || event.SubscriptionID != record.SubscriptionID ||
		event.UsageEpoch != record.UsageEpoch || event.ActualQuota != expectedActual ||
		event.OperatorUserID <= 0 || event.EventID == "" {
		return false
	}
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		return event.Action == billingsvc.RelayQuotaReviewActionResolveSettle
	}
	return event.Action == billingsvc.RelayQuotaReviewActionResolveRefund
}

func klingReviewSourceRecordMatches(expected, current *model.RelayQuotaReservationRecord) bool {
	return expected != nil && current != nil && expected.ID == current.ID &&
		expected.ReservationID == current.ReservationID && expected.UserID == current.UserID &&
		expected.TokenID == current.TokenID && expected.TokenUnlimited == current.TokenUnlimited &&
		expected.TrustQuotaBypassed == current.TrustQuotaBypassed && expected.ChannelID == current.ChannelID &&
		expected.FundingSource == current.FundingSource && expected.RequestedQuota == current.RequestedQuota &&
		expected.ReservedQuota == current.ReservedQuota && expected.TokenReserved == current.TokenReserved &&
		expected.SubscriptionID == current.SubscriptionID && expected.UsageEpoch == current.UsageEpoch &&
		expected.Status == current.Status && expected.Operation == current.Operation &&
		expected.ActualQuota == current.ActualQuota && expected.DispatchedAt == current.DispatchedAt &&
		expected.CompletedAt == current.CompletedAt && expected.LeaseOwner == current.LeaseOwner &&
		expected.LeaseExpiresAt == current.LeaseExpiresAt
}

func classifyKlingManualReviewResolutionError(err error) error {
	if err == nil || errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReviewInvalidState) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReviewUnsafeRetry) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReservationBusy) {
		return err
	}
	if errors.Is(err, billingsvc.ErrRelayQuotaOperation) || errors.Is(err, billingsvc.ErrRelayQuotaManualReview) {
		return fmt.Errorf("%w: durable Kling reservation changed", billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	return err
}

func klingAuditEventID(reservationID string) string {
	return "kling:" + reservationID
}

func validKlingReviewReservationID(value string) bool {
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
