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
	aliWanManualSettlementReason = "unresolved AliWan submission was settled by operator review"
	aliWanManualRefundReason     = "unresolved AliWan submission was refunded by operator review"
)

// SQLite cannot reliably upgrade concurrent read transactions into writers.
// Cross-process races remain fenced by the durable reservation and task CAS
// predicates used below.
var aliWanManualReviewResolveMu sync.Mutex

// AliWanManualReviewResolutionResult deliberately contains no channel
// credential or provider identifier. The audit identity proves that the
// accounting decision and task transition committed together.
type AliWanManualReviewResolutionResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	Resolution        string `json:"resolution"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

type aliWanManualReviewTxOutcome struct {
	taskID       string
	auditEventID string
}

// ResolveAliWanRelayQuotaReservationReview terminally resolves only the exact
// no-provider-ID ambiguous-submit tuple. The caller chooses settle or refund;
// the amount, funding coordinates, and provider channel are read from the
// immutable reservation and task snapshot and cannot be supplied or changed.
func ResolveAliWanRelayQuotaReservationReview(
	reservationID string,
	operatorUserID int,
	resolution string,
) (*AliWanManualReviewResolutionResult, error) {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if !validAliWanReviewReservationID(reservationID) || operatorUserID <= 0 ||
		(resolution != service.RelayQuotaReviewResolutionSettle &&
			resolution != service.RelayQuotaReviewResolutionRefund) {
		return nil, service.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("AliWan review database is unavailable")
	}

	aliWanManualReviewResolveMu.Lock()
	defer aliWanManualReviewResolveMu.Unlock()

	var source model.RelayQuotaReservationRecord
	result := model.DB.Where("reservation_id = ?", reservationID).Limit(1).Find(&source)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, service.ErrRelayQuotaReviewNotFound
	}
	var ownership model.TaskOperation
	result = model.DB.Where("reservation_id = ? AND platform = ?", reservationID, aliWanTaskPlatform).
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
			verified, err := verifyAliWanManualReviewResolution(reservationID, resolution)
			if err != nil {
				return nil, err
			}
			verified.Changed = false
			return verified, nil
		}
		return nil, fmt.Errorf("%w: reservation is %s", service.ErrRelayQuotaReviewInvalidState, source.Status)
	}

	operation, task, _, err := loadActiveAliWanManualReview(model.DB, &source)
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
	outcome := aliWanManualReviewTxOutcome{}
	persist := func(tx *gorm.DB) error {
		var persistErr error
		outcome, persistErr = resolveAliWanManualReviewTx(
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
		return nil, classifyAliWanManualReviewResolutionError(err)
	}
	verified, err := verifyAliWanManualReviewResolution(reservationID, resolution)
	if err != nil {
		return nil, err
	}
	verified.Changed = outcome.taskID == verified.TaskID && outcome.auditEventID == verified.AuditEventID
	_ = removeAliWanRecoveryJournal(verified.TaskID)
	return verified, nil
}

func resolveAliWanManualReviewTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	expectedTaskID string,
	expectedOperationID int64,
	operatorUserID int,
	resolution, eventID string,
) (aliWanManualReviewTxOutcome, error) {
	var record model.RelayQuotaReservationRecord
	if err := tx.Where("reservation_id = ?", source.ReservationID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return aliWanManualReviewTxOutcome{}, service.ErrRelayQuotaReviewNotFound
		}
		return aliWanManualReviewTxOutcome{}, err
	}
	if !aliWanReviewSourceRecordMatches(source, &record) {
		return aliWanManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	operation, task, privateData, err := loadActiveAliWanManualReview(tx, &record)
	if err != nil {
		return aliWanManualReviewTxOutcome{}, err
	}
	if task.TaskID != expectedTaskID || operation.ID != expectedOperationID {
		return aliWanManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	var existingAuditCount int64
	if err := tx.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", aliWanAuditEventID(record.ReservationID)).
		Count(&existingAuditCount).Error; err != nil {
		return aliWanManualReviewTxOutcome{}, err
	}
	if existingAuditCount != 0 {
		return aliWanManualReviewTxOutcome{}, fmt.Errorf("%w: consume audit already exists for unresolved AliWan hold",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}

	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return aliWanManualReviewTxOutcome{}, err
	}
	privateData.SettlementPending = false
	privateJSON, err := marshalAliWanTaskPrivateData(privateData)
	if err != nil {
		return aliWanManualReviewTxOutcome{}, fmt.Errorf("%w: AliWan terminal metadata is invalid",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	taskStatus := model.TaskStatusUnknown
	operationState := model.TaskOperationUnknown
	failReason := aliWanManualSettlementReason
	actualQuota := task.Quota
	if resolution == service.RelayQuotaReviewResolutionRefund {
		taskStatus = model.TaskStatusFailure
		operationState = model.TaskOperationRefunded
		failReason = aliWanManualRefundReason
		actualQuota = 0
	}
	taskResult := tx.Model(&model.Task{}).
		Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
			task.ID, task.TaskID, aliWanTaskPlatform, record.UserID, task.ChannelId, record.RequestedQuota).
		Where("status = ? AND fail_reason = ? AND private_data = ? AND data = ?",
			model.TaskStatusUnknown, aliWanUnknownDispatchReason, task.PrivateData, "null").
		Updates(map[string]any{
			"status": taskStatus, "quota": actualQuota, "fail_reason": failReason,
			"private_data": privateJSON, "progress": "100%", "finish_time": now, "updated_at": now,
		})
	if taskResult.Error != nil {
		return aliWanManualReviewTxOutcome{}, taskResult.Error
	}
	if taskResult.RowsAffected != 1 {
		return aliWanManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	opResult := tx.Model(&model.TaskOperation{}).
		Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ? AND user_id = ? AND channel_id = ?",
			operation.ID, task.TaskID, record.ReservationID, aliWanTaskPlatform, record.UserID, task.ChannelId).
		Where("state = ? AND settlement_pending = ? AND encrypted_provider_task_id = ?",
			model.TaskOperationManualReview, true, "").
		Where("last_error = ? AND next_attempt_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			aliWanUnknownDispatchReason, int64(0), "", int64(0)).
		Updates(map[string]any{
			"state": operationState, "settlement_pending": false,
			"next_attempt_at": int64(0), "completed_at": now, "updated_at": now,
			"last_error": "", "lease_owner": "", "lease_expires_at": int64(0),
		})
	if opResult.Error != nil {
		return aliWanManualReviewTxOutcome{}, opResult.Error
	}
	if opResult.RowsAffected != 1 {
		return aliWanManualReviewTxOutcome{}, service.ErrRelayQuotaReservationBusy
	}
	if resolution == service.RelayQuotaReviewResolutionSettle {
		auditTask := *task
		auditTask.Status = taskStatus
		auditTask.FailReason = failReason
		auditTask.PrivateData = privateJSON
		auditTask.FinishTime = now
		auditTask.UpdatedAt = now
		audit, err := newAliWanConsumeAudit(auditTask, privateData, record.ReservationID, actualQuota)
		if err != nil {
			return aliWanManualReviewTxOutcome{}, err
		}
		if err := service.EnqueueAuditLogTx(tx, audit); err != nil {
			return aliWanManualReviewTxOutcome{}, err
		}
	}
	reviewEvent, err := service.AppendRelayQuotaReservationResolutionEventTx(
		tx, &record, operatorUserID, resolution, actualQuota, task.ChannelId, eventID,
	)
	if err != nil {
		return aliWanManualReviewTxOutcome{}, err
	}
	return aliWanManualReviewTxOutcome{taskID: task.TaskID, auditEventID: reviewEvent.EventID}, nil
}

func loadActiveAliWanManualReview(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
) (*model.TaskOperation, *model.Task, aliWanTaskPrivateData, error) {
	var operation model.TaskOperation
	result := tx.Where("reservation_id = ? AND platform = ?", record.ReservationID, aliWanTaskPlatform).
		Limit(1).Find(&operation)
	if result.Error != nil {
		return nil, nil, aliWanTaskPrivateData{}, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, aliWanTaskPrivateData{}, fmt.Errorf("%w: AliWan recovery operation is unavailable",
			service.ErrRelayQuotaReviewNotFound)
	}
	var task model.Task
	result = tx.Where("task_id = ? AND platform = ?", operation.TaskID, aliWanTaskPlatform).Limit(1).Find(&task)
	if result.Error != nil {
		return nil, nil, aliWanTaskPrivateData{}, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, nil, aliWanTaskPrivateData{}, fmt.Errorf("%w: AliWan task is unavailable",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := validateActiveAliWanManualReview(record, &task, &operation)
	if err != nil {
		return nil, nil, aliWanTaskPrivateData{}, err
	}
	return &operation, &task, privateData, nil
}

func validateActiveAliWanManualReview(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (aliWanTaskPrivateData, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		record.Status != model.RelayQuotaReservationStatusDispatched ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != record.ReservedQuota || record.ChannelID != 0 ||
		record.DispatchedAt <= 0 || record.CompletedAt != 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return aliWanTaskPrivateData{}, fmt.Errorf("%w: reservation is not the retained AliWan dispatched tuple",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if task.Platform != aliWanTaskPlatform || task.TaskID == "" || task.CreatedAt <= 0 ||
		task.Status != model.TaskStatusUnknown || task.FailReason != aliWanUnknownDispatchReason ||
		task.UserId != record.UserID || task.ChannelId <= 0 || task.Quota != record.RequestedQuota ||
		task.Data != "null" || task.FinishTime <= 0 {
		return aliWanTaskPrivateData{}, fmt.Errorf("%w: AliWan task is not the unresolved review tuple",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		operation.Platform != aliWanTaskPlatform || operation.Platform != task.Platform ||
		operation.UserID != record.UserID || operation.ChannelID != task.ChannelId ||
		operation.State != model.TaskOperationManualReview || !operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != aliWanUnknownDispatchReason ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.NextAttemptAt != 0 ||
		operation.CompletedAt <= 0 {
		return aliWanTaskPrivateData{}, fmt.Errorf("%w: AliWan recovery operation is not the unresolved review tuple",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID ||
		!privateData.SettlementPending || privateData.EncryptedProviderTaskID != "" {
		return aliWanTaskPrivateData{}, fmt.Errorf("%w: AliWan metadata does not match the reservation",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, err := validateAliWanTaskPricingSnapshot(task, privateData, record.RequestedQuota); err != nil {
		return aliWanTaskPrivateData{}, fmt.Errorf("%w: AliWan pricing does not match the reservation",
			service.ErrRelayQuotaReviewUnsafeRetry)
	}
	return privateData, nil
}

func verifyAliWanManualReviewResolution(
	reservationID, resolution string,
) (*AliWanManualReviewResolutionResult, error) {
	var outcome *AliWanManualReviewResolutionResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			return err
		}
		var operation model.TaskOperation
		if err := tx.Where("reservation_id = ? AND platform = ?", reservationID, aliWanTaskPlatform).
			First(&operation).Error; err != nil {
			return err
		}
		var task model.Task
		if err := tx.Where("task_id = ? AND platform = ?", operation.TaskID, aliWanTaskPlatform).
			First(&task).Error; err != nil {
			return err
		}
		event, err := validateTerminalAliWanManualReviewTx(tx, &record, &task, &operation, resolution)
		if err != nil {
			return err
		}
		outcome = &AliWanManualReviewResolutionResult{
			ReservationID: record.ReservationID, TaskID: task.TaskID, Resolution: resolution,
			ReservationStatus: record.Status, TaskStatus: task.Status,
			OperationState: operation.State, AuditEventID: event.EventID,
		}
		return nil
	})
	return outcome, err
}

func validateTerminalAliWanManualReviewTx(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
	resolution string,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		task.Platform != aliWanTaskPlatform || operation.Platform != aliWanTaskPlatform ||
		task.TaskID == "" || operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		task.UserId != record.UserID || operation.UserID != record.UserID ||
		task.ChannelId <= 0 || operation.ChannelID != task.ChannelId || task.FinishTime <= 0 ||
		record.CompletedAt <= 0 || operation.CompletedAt <= 0 || operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != "" ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.NextAttemptAt != 0 {
		return nil, fmt.Errorf("%w: terminal AliWan resolution tuple is invalid",
			service.ErrRelayQuotaReviewInvalidState)
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource || privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch || privateData.TokenID != record.TokenID ||
		privateData.SettlementPending || privateData.EncryptedProviderTaskID != "" {
		return nil, fmt.Errorf("%w: terminal AliWan metadata is invalid", service.ErrRelayQuotaReviewInvalidState)
	}
	if _, err := validateAliWanTaskPricingSnapshot(task, privateData, record.RequestedQuota); err != nil {
		return nil, fmt.Errorf("%w: terminal AliWan pricing is invalid", service.ErrRelayQuotaReviewInvalidState)
	}

	expectedActual := 0
	expectedTaskQuota := 0
	expectedTaskStatus := model.TaskStatusFailure
	expectedTaskReason := aliWanManualRefundReason
	expectedOperationState := model.TaskOperationRefunded
	expectedReservationStatus := model.RelayQuotaReservationStatusRefunded
	expectedReservationOperation := model.RelayQuotaReservationOperationRefund
	if resolution == service.RelayQuotaReviewResolutionSettle {
		expectedActual = record.RequestedQuota
		expectedTaskQuota = record.RequestedQuota
		expectedTaskStatus = model.TaskStatusUnknown
		expectedTaskReason = aliWanManualSettlementReason
		expectedOperationState = model.TaskOperationUnknown
		expectedReservationStatus = model.RelayQuotaReservationStatusSettled
		expectedReservationOperation = model.RelayQuotaReservationOperationSettle
	}
	if record.Status != expectedReservationStatus || record.Operation != expectedReservationOperation ||
		record.ActualQuota != expectedActual || task.Quota != expectedTaskQuota || task.Status != expectedTaskStatus ||
		task.FailReason != expectedTaskReason || task.Data != "null" || operation.State != expectedOperationState {
		return nil, fmt.Errorf("%w: terminal AliWan disposition conflicts with requested resolution",
			service.ErrRelayQuotaReviewInvalidState)
	}
	if resolution == service.RelayQuotaReviewResolutionSettle {
		if record.ChannelID != task.ChannelId {
			return nil, fmt.Errorf("%w: terminal AliWan settlement channel mismatch",
				service.ErrRelayQuotaReviewInvalidState)
		}
		var auditCount int64
		if err := tx.Model(&model.AuditLogOutbox{}).
			Where("event_id = ?", aliWanAuditEventID(record.ReservationID)).Count(&auditCount).Error; err != nil {
			return nil, err
		}
		if auditCount != 1 {
			return nil, fmt.Errorf("%w: terminal AliWan settlement audit is missing",
				service.ErrRelayQuotaReviewInvalidState)
		}
	} else if record.ChannelID != 0 {
		return nil, fmt.Errorf("%w: terminal AliWan refund retained a settlement channel",
			service.ErrRelayQuotaReviewInvalidState)
	}
	event, found, err := service.FindRelayQuotaReservationResolutionEventTx(tx, record.ReservationID, resolution)
	if err != nil {
		return nil, err
	}
	if !found || !aliWanResolutionEventMatches(event, record, task, resolution, expectedActual) {
		return nil, fmt.Errorf("%w: terminal AliWan resolution audit is missing or inconsistent",
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
		return nil, fmt.Errorf("%w: conflicting AliWan terminal resolution audits exist",
			service.ErrRelayQuotaReviewInvalidState)
	}
	return event, nil
}

func aliWanResolutionEventMatches(
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

func aliWanReviewSourceRecordMatches(expected, current *model.RelayQuotaReservationRecord) bool {
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

func classifyAliWanManualReviewResolutionError(err error) error {
	if err == nil || errors.Is(err, service.ErrRelayQuotaReviewQueryInvalid) ||
		errors.Is(err, service.ErrRelayQuotaReviewNotFound) ||
		errors.Is(err, service.ErrRelayQuotaReviewInvalidState) ||
		errors.Is(err, service.ErrRelayQuotaReviewUnsafeRetry) ||
		errors.Is(err, service.ErrRelayQuotaReservationBusy) {
		return err
	}
	if errors.Is(err, service.ErrRelayQuotaOperation) || errors.Is(err, service.ErrRelayQuotaManualReview) {
		return fmt.Errorf("%w: durable AliWan reservation changed", service.ErrRelayQuotaReviewInvalidState)
	}
	return err
}

func aliWanAuditEventID(reservationID string) string {
	return "ali-video:" + reservationID
}

func validAliWanReviewReservationID(value string) bool {
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
