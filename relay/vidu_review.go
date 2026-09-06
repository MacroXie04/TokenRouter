package relay

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	viduManualSettlementReason = "unresolved Vidu submission was settled by operator review"
	viduManualRefundReason     = "unresolved Vidu submission was refunded by operator review"
)

// SQLite cannot reliably upgrade concurrent read transactions into writers.
// Cross-process races remain fenced by the durable reservation and task CAS
// predicates used below.
var viduManualReviewResolveMu sync.Mutex

// ViduManualReviewResolutionResult deliberately contains no channel
// credential or provider identifier. The audit identity proves that the
// accounting decision and task transition committed together.
type ViduManualReviewResolutionResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	Resolution        string `json:"resolution"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

type viduManualReviewTxOutcome struct {
	taskID       string
	auditEventID string
}

// ResolveViduRelayQuotaReservationReview terminally resolves only the exact
// no-provider-ID ambiguous-submit tuple. The caller chooses settle or refund;
// the amount, funding coordinates, and provider channel are read from the
// immutable reservation and task snapshot and cannot be supplied or changed.
func ResolveViduRelayQuotaReservationReview(
	reservationID string,
	operatorUserID int,
	resolution string,
) (*ViduManualReviewResolutionResult, error) {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if !validViduReviewReservationID(reservationID) || operatorUserID <= 0 ||
		(resolution != service.RelayQuotaReviewResolutionSettle &&
			resolution != service.RelayQuotaReviewResolutionRefund) {
		return nil, service.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("Vidu review database is unavailable")
	}

	viduManualReviewResolveMu.Lock()
	defer viduManualReviewResolveMu.Unlock()

	var source model.RelayQuotaReservationRecord
	result := model.DB.Where("reservation_id = ?", reservationID).Limit(1).Find(&source)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, service.ErrRelayQuotaReviewNotFound
	}
	var ownership model.TaskOperation
	result = model.DB.Where("reservation_id = ? AND platform = ?", reservationID, viduTaskPlatform).
		Limit(1).Find(&ownership)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, service.ErrRelayQuotaReviewNotFound
	}
	if source.Status != model.RelayQuotaReservationStatusDispatched {
		if (source.Status == model.RelayQuotaReservationStatusSettled &&
			resolution == service.RelayQuotaReviewResolutionSettle) ||
			(source.Status == model.RelayQuotaReservationStatusRefunded &&
				resolution == service.RelayQuotaReviewResolutionRefund) {
			verified, err := verifyViduManualReviewResolution(reservationID, resolution)
			if err != nil {
				return nil, err
			}
			verified.Changed = false
			return verified, nil
		}
		return nil, fmt.Errorf("%w: reservation is %s", service.ErrRelayQuotaReviewInvalidState, source.Status)
	}

	operation, task, _, err := loadActiveViduManualReview(model.DB, &source)
	if err != nil {
		return nil, err
	}
	reservation, err := service.RestoreRelayQuotaReservation(reservationID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid durable reservation", service.ErrRelayQuotaReviewUnsafeRetry)
	}
	eventID, err := common.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	outcome := viduManualReviewTxOutcome{}
	persist := func(tx *gorm.DB) error {
		var persistErr error
		outcome, persistErr = resolveViduManualReviewTx(
			tx, &source, task.TaskID, operation.ID, operatorUserID, resolution, eventID,
		)
		return persistErr
	}
	if resolution == service.RelayQuotaReviewResolutionSettle {
		err = reservation.SettleWithChannelAndPersistence(source.RequestedQuota, task.ChannelId, persist)
	} else {
		err = reservation.RefundWithPersistence(persist)
	}
	if err != nil {
		return nil, classifyViduManualReviewResolutionError(err)
	}
	verified, err := verifyViduManualReviewResolution(reservationID, resolution)
	if err != nil {
		return nil, err
	}
	verified.Changed = outcome.taskID == verified.TaskID && outcome.auditEventID == verified.AuditEventID
	_ = removeViduRecoveryJournal(verified.TaskID)
	return verified, nil
}

func resolveViduManualReviewTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	expectedTaskID string,
	expectedOperationID int64,
	operatorUserID int,
	resolution, eventID string,
) (viduManualReviewTxOutcome, error) {
	var record model.RelayQuotaReservationRecord
	if err := tx.Where("reservation_id = ?", source.ReservationID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return viduManualReviewTxOutcome{}, service.ErrRelayQuotaReviewNotFound
		}
		return viduManualReviewTxOutcome{}, err
	}
	if !viduReviewSourceRecordMatches(source, &record) {
		return viduManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	operation, task, privateData, err := loadActiveViduManualReview(tx, &record)
	if err != nil {
		return viduManualReviewTxOutcome{}, err
	}
	if task.TaskID != expectedTaskID || operation.ID != expectedOperationID {
		return viduManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	var existingAuditCount int64
	if err := tx.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", viduAuditEventID(record.ReservationID)).
		Count(&existingAuditCount).Error; err != nil {
		return viduManualReviewTxOutcome{}, err
	}
	if existingAuditCount != 0 {
		return viduManualReviewTxOutcome{}, fmt.Errorf("%w: consume audit already exists for unresolved Vidu hold",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}

	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return viduManualReviewTxOutcome{}, err
	}
	privateData.SettlementPending = false
	privateJSON, err := marshalViduTaskPrivateData(privateData)
	if err != nil {
		return viduManualReviewTxOutcome{}, fmt.Errorf("%w: Vidu terminal metadata is invalid",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	taskStatus := model.TaskStatusUnknown
	operationState := model.TaskOperationUnknown
	failReason := viduManualSettlementReason
	actualQuota := task.Quota
	if resolution == service.RelayQuotaReviewResolutionRefund {
		taskStatus = model.TaskStatusFailure
		operationState = model.TaskOperationRefunded
		failReason = viduManualRefundReason
		actualQuota = 0
	}
	taskResult := tx.Model(&model.Task{}).
		Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
			task.ID, task.TaskID, viduTaskPlatform, record.UserID, task.ChannelId, record.RequestedQuota).
		Where("status = ? AND fail_reason = ? AND private_data = ? AND data = ?",
			model.TaskStatusUnknown, viduUnknownDispatchReason, task.PrivateData, "null").
		Updates(map[string]any{
			"status": taskStatus, "quota": actualQuota, "fail_reason": failReason,
			"private_data": privateJSON, "progress": "100%", "finish_time": now, "updated_at": now,
		})
	if taskResult.Error != nil {
		return viduManualReviewTxOutcome{}, taskResult.Error
	}
	if taskResult.RowsAffected != 1 {
		return viduManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	opResult := tx.Model(&model.TaskOperation{}).
		Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
			operation.ID, task.TaskID, record.ReservationID, viduTaskPlatform, record.UserID, task.ChannelId).
		Where("state = ? AND settlement_pending = ? AND encrypted_provider_task_id = ?",
			model.TaskOperationManualReview, true, "").
		Where("last_error = ? AND next_attempt_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			viduUnknownDispatchReason, int64(0), "", int64(0)).
		Updates(map[string]any{
			"state": operationState, "settlement_pending": false,
			"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
			"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
		})
	if opResult.Error != nil {
		return viduManualReviewTxOutcome{}, opResult.Error
	}
	if opResult.RowsAffected != 1 {
		return viduManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	if resolution == service.RelayQuotaReviewResolutionSettle {
		auditTask := *task
		auditTask.Status = taskStatus
		auditTask.FailReason = failReason
		auditTask.PrivateData = privateJSON
		auditTask.FinishTime = now
		auditTask.UpdatedAt = now
		audit, err := newViduConsumeAudit(auditTask, privateData, record.ReservationID, actualQuota)
		if err != nil {
			return viduManualReviewTxOutcome{}, err
		}
		if err := service.EnqueueAuditLogTx(tx, audit); err != nil {
			return viduManualReviewTxOutcome{}, err
		}
	}
	reviewEvent, err := service.AppendRelayQuotaReservationResolutionEventTx(
		tx, &record, operatorUserID, resolution, actualQuota, task.ChannelId, eventID,
	)
	if err != nil {
		return viduManualReviewTxOutcome{}, err
	}
	return viduManualReviewTxOutcome{taskID: task.TaskID, auditEventID: reviewEvent.EventID}, nil
}

func loadActiveViduManualReview(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
) (*model.TaskOperation, *model.Task, viduTaskPrivateData, error) {
	var operation model.TaskOperation
	result := tx.Where("reservation_id = ? AND platform = ?", record.ReservationID, viduTaskPlatform).
		Limit(1).Find(&operation)
	if result.Error != nil {
		return nil, nil, viduTaskPrivateData{}, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, viduTaskPrivateData{}, fmt.Errorf("%w: Vidu recovery operation is unavailable",
			service.ErrRelayQuotaReviewNotFound)
	}
	var task model.Task
	result = tx.Where("task_id = ? AND platform = ?", operation.TaskID, viduTaskPlatform).Limit(1).Find(&task)
	if result.Error != nil {
		return nil, nil, viduTaskPrivateData{}, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, viduTaskPrivateData{}, fmt.Errorf("%w: Vidu task is unavailable",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := validateActiveViduManualReview(record, &task, &operation)
	if err != nil {
		return nil, nil, viduTaskPrivateData{}, err
	}
	return &operation, &task, privateData, nil
}

func validateActiveViduManualReview(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (viduTaskPrivateData, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		record.Status != model.RelayQuotaReservationStatusDispatched ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != record.ReservedQuota || record.ChannelID != 0 ||
		record.DispatchedAt <= 0 || record.CompletedAt != 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return viduTaskPrivateData{}, fmt.Errorf("%w: reservation is not the retained Vidu dispatched tuple",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if task.Platform != viduTaskPlatform || task.TaskID == "" || task.CreatedAt <= 0 ||
		task.Status != model.TaskStatusUnknown || task.FailReason != viduUnknownDispatchReason ||
		task.UserId != record.UserID || task.ChannelId <= 0 || task.Quota != record.RequestedQuota ||
		task.Data != "null" || task.FinishTime <= 0 {
		return viduTaskPrivateData{}, fmt.Errorf("%w: Vidu task is not the unresolved review tuple",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		operation.Platform != viduTaskPlatform || operation.Platform != task.Platform ||
		operation.UserID != record.UserID || operation.ChannelID != task.ChannelId ||
		operation.State != model.TaskOperationManualReview || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != viduUnknownDispatchReason ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.NextAttemptAt != 0 ||
		operation.CompletedAt <= 0 {
		return viduTaskPrivateData{}, fmt.Errorf("%w: Vidu recovery operation is not the unresolved review tuple",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeViduTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID ||
		!privateData.SettlementPending || privateData.EncryptedProviderTaskID != "" {
		return viduTaskPrivateData{}, fmt.Errorf("%w: Vidu metadata does not match the reservation",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := validateViduTaskPricingSnapshot(task, privateData, record.RequestedQuota); err != nil {
		return viduTaskPrivateData{}, fmt.Errorf("%w: Vidu pricing does not match the reservation",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	return privateData, nil
}

func verifyViduManualReviewResolution(
	reservationID, resolution string,
) (*ViduManualReviewResolutionResult, error) {
	var outcome *ViduManualReviewResolutionResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("reservation_id = ? AND platform = ?", reservationID, viduTaskPlatform).
			First(&operation).Error; err != nil {
			return err
		}
		var task model.Task
		if err := tx.Where("task_id = ? AND platform = ?", operation.TaskID, viduTaskPlatform).
			First(&task).Error; err != nil {
			return err
		}
		event, err := validateTerminalViduManualReviewTx(tx, &record, &task, &operation, resolution)
		if err != nil {
			return err
		}
		outcome = &ViduManualReviewResolutionResult{
			ReservationID: record.ReservationID, TaskID: task.TaskID, Resolution: resolution,
			ReservationStatus: record.Status, TaskStatus: task.Status,
			OperationState: operation.State, AuditEventID: event.EventID,
		}
		return nil
	})
	return outcome, err
}

func validateTerminalViduManualReviewTx(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
	resolution string,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		task.Platform != viduTaskPlatform || operation.Platform != viduTaskPlatform ||
		task.TaskID == "" || operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		task.UserId != record.UserID || operation.UserID != record.UserID ||
		task.ChannelId <= 0 || operation.ChannelID != task.ChannelId || task.FinishTime <= 0 ||
		record.CompletedAt <= 0 || operation.CompletedAt <= 0 || operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != "" ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.NextAttemptAt != 0 {
		return nil, fmt.Errorf("%w: terminal Vidu resolution tuple is invalid",
			service.ErrRelayQuotaReviewInvalidState)
	}
	privateData, err := decodeViduTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID ||
		privateData.SettlementPending || privateData.EncryptedProviderTaskID != "" {
		return nil, fmt.Errorf("%w: terminal Vidu metadata is invalid", service.ErrRelayQuotaReviewInvalidState)
	}
	if _, err := validateViduTaskPricingSnapshot(task, privateData, record.RequestedQuota); err != nil {
		return nil, fmt.Errorf("%w: terminal Vidu pricing is invalid", service.ErrRelayQuotaReviewInvalidState)
	}

	expectedActual := 0
	expectedTaskQuota := 0
	expectedTaskStatus := model.TaskStatusFailure
	expectedTaskReason := viduManualRefundReason
	expectedOperationState := model.TaskOperationRefunded
	expectedReservationStatus := model.RelayQuotaReservationStatusRefunded
	expectedReservationOperation := model.RelayQuotaReservationOperationRefund
	if resolution == service.RelayQuotaReviewResolutionSettle {
		expectedActual = record.RequestedQuota
		expectedTaskQuota = record.RequestedQuota
		expectedTaskStatus = model.TaskStatusUnknown
		expectedTaskReason = viduManualSettlementReason
		expectedOperationState = model.TaskOperationUnknown
		expectedReservationStatus = model.RelayQuotaReservationStatusSettled
		expectedReservationOperation = model.RelayQuotaReservationOperationSettle
	}
	if record.Status != expectedReservationStatus || record.Operation != expectedReservationOperation ||
		record.ActualQuota != expectedActual || task.Quota != expectedTaskQuota || task.Status != expectedTaskStatus ||
		task.FailReason != expectedTaskReason || task.Data != "null" || operation.State != expectedOperationState {
		return nil, fmt.Errorf("%w: terminal Vidu disposition conflicts with requested resolution",
			service.ErrRelayQuotaReviewInvalidState)
	}
	if resolution == service.RelayQuotaReviewResolutionSettle {
		if record.ChannelID != task.ChannelId {
			return nil, fmt.Errorf("%w: terminal Vidu settlement channel mismatch",
				service.ErrRelayQuotaReviewInvalidState)
		}
		var auditCount int64
		if err := tx.Model(&model.AuditLogOutbox{}).
			Where("event_id = ?", viduAuditEventID(record.ReservationID)).Count(&auditCount).Error; err != nil {
			return nil, err
		}
		if auditCount != 1 {
			return nil, fmt.Errorf("%w: terminal Vidu settlement audit is missing",
				service.ErrRelayQuotaReviewInvalidState)
		}
	} else if record.ChannelID != 0 {
		return nil, fmt.Errorf("%w: terminal Vidu refund retained a settlement channel",
			service.ErrRelayQuotaReviewInvalidState)
	}
	event, found, err := service.FindRelayQuotaReservationResolutionEventTx(tx, record.ReservationID, resolution)
	if err != nil {
		return nil, err
	}
	if !found || !viduResolutionEventMatches(event, record, task, resolution, expectedActual) {
		return nil, fmt.Errorf("%w: terminal Vidu resolution audit is missing or inconsistent",
			service.ErrRelayQuotaReviewInvalidState)
	}
	var resolutionEventCount int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", record.ReservationID,
			[]string{service.RelayQuotaReviewActionResolveSettle, service.RelayQuotaReviewActionResolveRefund}).
		Count(&resolutionEventCount).Error; err != nil {
		return nil, err
	}
	if resolutionEventCount != 1 {
		return nil, fmt.Errorf("%w: conflicting Vidu terminal resolution audits exist",
			service.ErrRelayQuotaReviewInvalidState)
	}
	return event, nil
}

func viduResolutionEventMatches(
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
	if resolution == service.RelayQuotaReviewResolutionSettle {
		return event.Action == service.RelayQuotaReviewActionResolveSettle
	}
	return event.Action == service.RelayQuotaReviewActionResolveRefund
}

func viduReviewSourceRecordMatches(expected, current *model.RelayQuotaReservationRecord) bool {
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

func classifyViduManualReviewResolutionError(err error) error {
	if err == nil || errors.Is(err, service.ErrRelayQuotaReviewQueryInvalid) ||
		errors.Is(err, service.ErrRelayQuotaReviewNotFound) ||
		errors.Is(err, service.ErrRelayQuotaReviewInvalidState) ||
		errors.Is(err, service.ErrRelayQuotaReviewUnsafeRetry) ||
		errors.Is(err, service.ErrRelayQuotaReservationBusy) {
		return err
	}
	if errors.Is(err, service.ErrRelayQuotaOperation) || errors.Is(err, service.ErrRelayQuotaManualReview) {
		return fmt.Errorf("%w: durable Vidu reservation changed", service.ErrRelayQuotaReviewInvalidState)
	}
	return err
}

func viduAuditEventID(reservationID string) string {
	return "vidu:" + reservationID
}

func validViduReviewReservationID(value string) bool {
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
