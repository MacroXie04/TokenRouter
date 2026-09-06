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
	jimengManualSettlementReason = "unresolved provider submission was settled by operator review"
	jimengManualRefundReason     = "unresolved provider submission was refunded by operator review"
	jimengUnresolvedMarker       = "provider_outcome_unresolved"
)

// SQLite cannot upgrade concurrent read transactions into writers reliably.
// Production databases remain protected across nodes by the reservation row
// lock and the exact task/operation compare-and-swap predicates below.
var jimengManualReviewResolveMu sync.Mutex

// JimengManualReviewResolutionResult is deliberately secret-free. It reports
// only the terminal accounting and recovery states plus the append-only audit
// identity that proves the operator action committed with them.
type JimengManualReviewResolutionResult struct {
	ReservationID     string `json:"reservation_id"`
	TaskID            string `json:"task_id"`
	Resolution        string `json:"resolution"`
	ReservationStatus string `json:"reservation_status"`
	TaskStatus        string `json:"task_status"`
	OperationState    string `json:"operation_state"`
	AuditEventID      string `json:"audit_event_id"`
	Changed           bool   `json:"changed"`
}

type jimengManualReviewTxOutcome struct {
	taskID       string
	auditEventID string
	changed      bool
}

// ResolveJimengRelayQuotaReservationReview performs the only direct terminal
// resolution supported for a Jimeng provider-outcome hold. The caller chooses
// settle or refund, but every economic coordinate and the settlement amount
// come from the atomically-created reservation/task snapshot. The accounting
// transition, Jimeng task/operation transition, consume outbox (for settle),
// and root-operator review event have one primary-database commit boundary.
func ResolveJimengRelayQuotaReservationReview(
	reservationID string,
	operatorUserID int,
	resolution string,
) (*JimengManualReviewResolutionResult, error) {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if !validJimengReviewReservationID(reservationID) || operatorUserID <= 0 ||
		(resolution != billingsvc.RelayQuotaReviewResolutionSettle &&
			resolution != billingsvc.RelayQuotaReviewResolutionRefund) {
		return nil, billingsvc.ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("Jimeng review database is unavailable")
	}

	jimengManualReviewResolveMu.Lock()
	defer jimengManualReviewResolveMu.Unlock()

	var initial model.RelayQuotaReservationRecord
	result := model.DB.Where("reservation_id = ?", reservationID).Limit(1).Find(&initial)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, billingsvc.ErrRelayQuotaReviewNotFound
	}
	switch initial.Status {
	case model.RelayQuotaReservationStatusDispatched:
		// The complete source tuple is revalidated under the accounting row lock.
	case model.RelayQuotaReservationStatusSettled:
		if resolution != billingsvc.RelayQuotaReviewResolutionSettle {
			return nil, fmt.Errorf("%w: reservation was settled", billingsvc.ErrRelayQuotaReviewInvalidState)
		}
	case model.RelayQuotaReservationStatusRefunded:
		if resolution != billingsvc.RelayQuotaReviewResolutionRefund {
			return nil, fmt.Errorf("%w: reservation was refunded", billingsvc.ErrRelayQuotaReviewInvalidState)
		}
	default:
		return nil, fmt.Errorf("%w: reservation is %s", billingsvc.ErrRelayQuotaReviewInvalidState, initial.Status)
	}

	reservation, err := billingsvc.RestoreRelayQuotaReservation(reservationID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid durable reservation", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	eventID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	txOutcome := jimengManualReviewTxOutcome{}
	settlementChannelID := initial.ChannelID
	expectedTaskID := ""
	if initial.Status == model.RelayQuotaReservationStatusDispatched {
		var operation model.JimengTaskOperation
		if err := model.DB.Where("reservation_id = ?", reservationID).First(&operation).Error; err != nil {
			return nil, fmt.Errorf("%w: Jimeng recovery operation is unavailable",
				billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
		var task model.Task
		if err := model.DB.Where("task_id = ?", operation.TaskID).First(&task).Error; err != nil {
			return nil, fmt.Errorf("%w: Jimeng task is unavailable", billingsvc.ErrRelayQuotaReviewUnsafeRetry)
		}
		if _, err := validateActiveJimengManualReview(&initial, &task, &operation); err != nil {
			return nil, err
		}
		expectedTaskID = task.TaskID
		settlementChannelID = task.ChannelId
	}
	persist := func(tx *gorm.DB) error {
		outcome, persistErr := resolveJimengManualReviewTx(
			tx, reservationID, operatorUserID, resolution, eventID,
			&initial, expectedTaskID, settlementChannelID,
		)
		if persistErr == nil {
			txOutcome = outcome
		}
		return persistErr
	}

	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		err = reservation.SettleWithChannelAndPersistence(
			initial.RequestedQuota, settlementChannelID, persist,
		)
	} else {
		err = reservation.RefundWithPersistence(persist)
	}
	if err != nil {
		return nil, classifyJimengManualReviewResolutionError(err)
	}

	verified, verifyErr := verifyJimengManualReviewResolution(reservationID, resolution)
	if verifyErr != nil {
		return nil, verifyErr
	}
	verified.Changed = txOutcome.changed && txOutcome.auditEventID == verified.AuditEventID
	_ = removeJimengRecovery(verified.TaskID)
	return verified, nil
}

func resolveJimengManualReviewTx(
	tx *gorm.DB,
	reservationID string,
	operatorUserID int,
	resolution, eventID string,
	expectedSource *model.RelayQuotaReservationRecord,
	expectedTaskID string,
	expectedChannelID int,
) (jimengManualReviewTxOutcome, error) {
	var record model.RelayQuotaReservationRecord
	if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return jimengManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReviewNotFound
		}
		return jimengManualReviewTxOutcome{}, err
	}
	var operation model.JimengTaskOperation
	if err := tx.Where("reservation_id = ?", reservationID).First(&operation).Error; err != nil {
		return jimengManualReviewTxOutcome{}, fmt.Errorf("%w: Jimeng recovery operation is unavailable",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	var task model.Task
	if err := tx.Where("task_id = ?", operation.TaskID).First(&task).Error; err != nil {
		return jimengManualReviewTxOutcome{}, fmt.Errorf("%w: Jimeng task is unavailable",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}

	if record.Status != model.RelayQuotaReservationStatusDispatched {
		event, err := validateTerminalJimengManualReviewTx(tx, &record, &task, &operation, resolution)
		if err != nil {
			return jimengManualReviewTxOutcome{}, err
		}
		return jimengManualReviewTxOutcome{
			taskID: task.TaskID, auditEventID: event.EventID, changed: false,
		}, nil
	}
	if !jimengReviewSourceRecordMatches(expectedSource, &record) ||
		expectedTaskID == "" || operation.TaskID != expectedTaskID ||
		expectedChannelID <= 0 || task.ChannelId != expectedChannelID {
		return jimengManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}

	privateData, err := validateActiveJimengManualReview(&record, &task, &operation)
	if err != nil {
		return jimengManualReviewTxOutcome{}, err
	}
	var existingAuditCount int64
	if err := tx.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", jimengAuditEventID(record.ReservationID)).
		Count(&existingAuditCount).Error; err != nil {
		return jimengManualReviewTxOutcome{}, err
	}
	if existingAuditCount != 0 {
		return jimengManualReviewTxOutcome{}, fmt.Errorf("%w: consume audit already exists for an unresolved hold",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}

	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return jimengManualReviewTxOutcome{}, err
	}
	stripJimengTerminalRecoverySecrets(&privateData)
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	if err != nil {
		return jimengManualReviewTxOutcome{}, fmt.Errorf("%w: terminal metadata is invalid",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	taskStatus := model.TaskStatusUnknown
	operationState := model.JimengTaskOperationUnknown
	failReason := jimengManualSettlementReason
	actualQuota := task.Quota
	if resolution == billingsvc.RelayQuotaReviewResolutionRefund {
		taskStatus = model.TaskStatusFailure
		operationState = model.JimengTaskOperationRefunded
		failReason = jimengManualRefundReason
		actualQuota = 0
	}

	taskUpdate := tx.Model(&model.Task{}).
		Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
			task.ID, task.TaskID, jimengTaskPlatform, record.UserID, task.ChannelId, record.RequestedQuota).
		Where("status = ? AND fail_reason = ? AND private_data = ? AND properties = ?",
			model.TaskStatusUnknown, jimengManualReviewReason, task.PrivateData, task.Properties).
		Updates(map[string]any{
			"status": taskStatus, "fail_reason": failReason, "progress": "100%",
			"finish_time": now, "updated_at": now, "private_data": privateJSON,
		})
	if taskUpdate.Error != nil {
		return jimengManualReviewTxOutcome{}, taskUpdate.Error
	}
	if taskUpdate.RowsAffected != 1 {
		return jimengManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}
	opUpdate := tx.Model(&model.JimengTaskOperation{}).
		Where("id = ? AND task_id = ? AND reservation_id = ? AND user_id = ? AND channel_id = ?",
			operation.ID, task.TaskID, record.ReservationID, record.UserID, task.ChannelId).
		Where("state = ? AND settlement_pending = ? AND encrypted_provider_task_id = ?",
			model.JimengTaskOperationManualReview, false, "").
		Where("last_error = ? AND lease_owner = ? AND lease_expires_at = ?",
			jimengUnresolvedMarker, "", int64(0)).
		Updates(map[string]any{
			"state": operationState, "settlement_pending": false,
			"encrypted_provider_task_id": "", "attempts": operation.Attempts,
			"next_attempt_at": 0, "completed_at": now, "updated_at": now,
			"last_error": "", "lease_owner": "", "lease_expires_at": 0,
		})
	if opUpdate.Error != nil {
		return jimengManualReviewTxOutcome{}, opUpdate.Error
	}
	if opUpdate.RowsAffected != 1 {
		return jimengManualReviewTxOutcome{}, billingsvc.ErrRelayQuotaReservationBusy
	}

	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		auditTask := task
		auditTask.PrivateData = privateJSON
		auditTask.Status = taskStatus
		auditTask.FailReason = failReason
		auditTask.Progress = "100%"
		auditTask.FinishTime = now
		auditTask.UpdatedAt = now
		auditEntry, auditErr := newJimengConsumeAudit(
			auditTask, privateJSON, []byte(task.Data), model.TaskStatusUnknown, record.ReservationID,
		)
		if auditErr != nil {
			return jimengManualReviewTxOutcome{}, auditErr
		}
		if err := billingsvc.EnqueueAuditLogTx(tx, auditEntry); err != nil {
			return jimengManualReviewTxOutcome{}, err
		}
	}
	reviewEvent, err := billingsvc.AppendRelayQuotaReservationResolutionEventTx(
		tx, &record, operatorUserID, resolution, actualQuota, task.ChannelId, eventID,
	)
	if err != nil {
		return jimengManualReviewTxOutcome{}, err
	}
	return jimengManualReviewTxOutcome{
		taskID: task.TaskID, auditEventID: reviewEvent.EventID, changed: true,
	}, nil
}

func jimengReviewSourceRecordMatches(
	expected *model.RelayQuotaReservationRecord,
	current *model.RelayQuotaReservationRecord,
) bool {
	return expected != nil && current != nil &&
		expected.ID == current.ID && expected.ReservationID == current.ReservationID &&
		expected.UserID == current.UserID && expected.TokenID == current.TokenID &&
		expected.TokenUnlimited == current.TokenUnlimited &&
		expected.TrustQuotaBypassed == current.TrustQuotaBypassed && expected.ChannelID == current.ChannelID &&
		expected.FundingSource == current.FundingSource && expected.RequestedQuota == current.RequestedQuota &&
		expected.ReservedQuota == current.ReservedQuota && expected.TokenReserved == current.TokenReserved &&
		expected.SubscriptionID == current.SubscriptionID && expected.UsageEpoch == current.UsageEpoch &&
		expected.Status == current.Status && expected.Operation == current.Operation &&
		expected.ActualQuota == current.ActualQuota && expected.DispatchedAt == current.DispatchedAt &&
		expected.CompletedAt == current.CompletedAt && expected.LeaseOwner == current.LeaseOwner &&
		expected.LeaseExpiresAt == current.LeaseExpiresAt
}

func validateActiveJimengManualReview(
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.JimengTaskOperation,
) (jimengTaskPrivateData, error) {
	if record == nil || task == nil || operation == nil ||
		record.TrustQuotaBypassed ||
		record.Status != model.RelayQuotaReservationStatusDispatched ||
		record.Operation != model.RelayQuotaReservationOperationSettle ||
		record.ActualQuota != record.ReservedQuota || record.ChannelID != 0 ||
		record.DispatchedAt <= 0 || record.CompletedAt != 0 ||
		record.LeaseOwner != "" || record.LeaseExpiresAt != 0 {
		return jimengTaskPrivateData{}, fmt.Errorf("%w: reservation is not the retained dispatched tuple",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if task.Platform != jimengTaskPlatform || task.TaskID == "" || task.CreatedAt <= 0 ||
		task.Status != model.TaskStatusUnknown || task.FailReason != jimengManualReviewReason ||
		task.UserId != record.UserID || task.ChannelId <= 0 ||
		task.Quota != record.RequestedQuota || task.FinishTime <= 0 {
		return jimengTaskPrivateData{}, fmt.Errorf("%w: Jimeng task is not the unresolved review tuple",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	if operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		operation.UserID != record.UserID || operation.ChannelID != task.ChannelId ||
		operation.State != model.JimengTaskOperationManualReview || operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.LastError != jimengUnresolvedMarker ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.NextAttemptAt != 0 || operation.CompletedAt <= 0 {
		return jimengTaskPrivateData{}, fmt.Errorf("%w: Jimeng recovery operation is not the unresolved review tuple",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	privateData, err := decodeJimengTaskPrivateDataStored(task.PrivateData)
	if err != nil || !jimengReviewPrivateDataMatches(record, privateData) ||
		privateData.SettlementPending || privateData.UpstreamTaskID != "" ||
		privateData.EncryptedUpstreamTaskID != "" || privateData.ResultURL != "" {
		return jimengTaskPrivateData{}, fmt.Errorf("%w: Jimeng accounting metadata does not match the reservation",
			billingsvc.ErrRelayQuotaReviewUnsafeRetry)
	}
	return privateData, nil
}

func jimengReviewPrivateDataMatches(
	record *model.RelayQuotaReservationRecord,
	privateData jimengTaskPrivateData,
) bool {
	if record == nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.BillingSource != record.FundingSource ||
		privateData.SubscriptionID != record.SubscriptionID ||
		privateData.FundingUsageEpoch != record.UsageEpoch ||
		privateData.FundingReserved != record.ReservedQuota ||
		privateData.TokenID != record.TokenID ||
		privateData.TokenReserved != (record.TokenReserved > 0) ||
		privateData.TokenUnlimited != record.TokenUnlimited {
		return false
	}
	return privateData.FundingRequestID == "" || privateData.FundingRequestID == record.ReservationID
}

func validateTerminalJimengManualReviewTx(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.JimengTaskOperation,
	resolution string,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if record == nil || task == nil || operation == nil || record.TrustQuotaBypassed ||
		task.Platform != jimengTaskPlatform ||
		task.TaskID == "" || task.UserId != record.UserID || task.Quota != record.RequestedQuota ||
		task.ChannelId <= 0 || task.FinishTime <= 0 || record.CompletedAt <= 0 ||
		operation.TaskID != task.TaskID || operation.ReservationID != record.ReservationID ||
		operation.UserID != record.UserID || operation.ChannelID != task.ChannelId ||
		operation.SettlementPending || operation.EncryptedProviderTaskID != "" ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.LastError != "" ||
		operation.CompletedAt <= 0 {
		return nil, fmt.Errorf("%w: terminal Jimeng resolution tuple is invalid",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	privateData, err := decodeJimengTaskPrivateDataStored(task.PrivateData)
	if err != nil || !jimengReviewPrivateDataMatches(record, privateData) ||
		privateData.SettlementPending || privateData.UpstreamTaskID != "" ||
		privateData.EncryptedUpstreamTaskID != "" || privateData.ChannelBaseURL != "" ||
		privateData.EncryptedChannelKey != "" {
		return nil, fmt.Errorf("%w: terminal Jimeng metadata is invalid",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}

	expectedActual := 0
	expectedTaskStatus := model.TaskStatusFailure
	expectedTaskReason := jimengManualRefundReason
	expectedOperationState := model.JimengTaskOperationRefunded
	expectedReservationStatus := model.RelayQuotaReservationStatusRefunded
	expectedReservationOperation := model.RelayQuotaReservationOperationRefund
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		expectedActual = record.RequestedQuota
		expectedTaskStatus = model.TaskStatusUnknown
		expectedTaskReason = jimengManualSettlementReason
		expectedOperationState = model.JimengTaskOperationUnknown
		expectedReservationStatus = model.RelayQuotaReservationStatusSettled
		expectedReservationOperation = model.RelayQuotaReservationOperationSettle
	}
	if record.Status != expectedReservationStatus || record.Operation != expectedReservationOperation ||
		record.ActualQuota != expectedActual || task.Status != expectedTaskStatus ||
		task.FailReason != expectedTaskReason || operation.State != expectedOperationState {
		return nil, fmt.Errorf("%w: terminal disposition conflicts with the requested resolution",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		if record.ChannelID != task.ChannelId {
			return nil, fmt.Errorf("%w: terminal settlement channel mismatch",
				billingsvc.ErrRelayQuotaReviewInvalidState)
		}
		var auditCount int64
		if err := tx.Model(&model.AuditLogOutbox{}).
			Where("event_id = ?", jimengAuditEventID(record.ReservationID)).
			Count(&auditCount).Error; err != nil {
			return nil, err
		}
		if auditCount != 1 {
			return nil, fmt.Errorf("%w: terminal settlement consume audit is missing",
				billingsvc.ErrRelayQuotaReviewInvalidState)
		}
	} else if record.ChannelID != 0 {
		return nil, fmt.Errorf("%w: terminal refund retained a settlement channel",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}

	event, found, err := billingsvc.FindRelayQuotaReservationResolutionEventTx(
		tx, record.ReservationID, resolution,
	)
	if err != nil {
		return nil, err
	}
	if !found || !jimengResolutionEventMatches(event, record, task, resolution, expectedActual) {
		return nil, fmt.Errorf("%w: terminal resolution audit is missing or inconsistent",
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
		return nil, fmt.Errorf("%w: conflicting terminal resolution audits exist",
			billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	return event, nil
}

func jimengResolutionEventMatches(
	event *model.RelayQuotaReservationReviewEvent,
	record *model.RelayQuotaReservationRecord,
	task *model.Task,
	resolution string,
	expectedActual int,
) bool {
	if event == nil || record == nil || task == nil || event.ReservationID != record.ReservationID ||
		event.FromStatus != model.RelayQuotaReservationStatusDispatched ||
		event.ToStatus != record.Status || event.Operation != resolution ||
		event.UserID != record.UserID || event.TokenID != record.TokenID ||
		event.TokenUnlimited != record.TokenUnlimited || event.TrustQuotaBypassed != record.TrustQuotaBypassed ||
		event.ChannelID != task.ChannelId ||
		event.FundingSource != record.FundingSource || event.RequestedQuota != record.RequestedQuota ||
		event.ReservedQuota != record.ReservedQuota || event.TokenReserved != record.TokenReserved ||
		event.SubscriptionID != record.SubscriptionID || event.UsageEpoch != record.UsageEpoch ||
		event.ActualQuota != expectedActual || event.OperatorUserID <= 0 || event.EventID == "" {
		return false
	}
	if resolution == billingsvc.RelayQuotaReviewResolutionSettle {
		return event.Action == billingsvc.RelayQuotaReviewActionResolveSettle
	}
	return event.Action == billingsvc.RelayQuotaReviewActionResolveRefund
}

func verifyJimengManualReviewResolution(
	reservationID, resolution string,
) (*JimengManualReviewResolutionResult, error) {
	var outcome *JimengManualReviewResolutionResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := tx.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			return err
		}
		var operation model.JimengTaskOperation
		if err := tx.Where("reservation_id = ?", reservationID).First(&operation).Error; err != nil {
			return err
		}
		var task model.Task
		if err := tx.Where("task_id = ?", operation.TaskID).First(&task).Error; err != nil {
			return err
		}
		event, err := validateTerminalJimengManualReviewTx(tx, &record, &task, &operation, resolution)
		if err != nil {
			return err
		}
		outcome = &JimengManualReviewResolutionResult{
			ReservationID: record.ReservationID, TaskID: task.TaskID, Resolution: resolution,
			ReservationStatus: record.Status, TaskStatus: task.Status,
			OperationState: operation.State, AuditEventID: event.EventID,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func classifyJimengManualReviewResolutionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, billingsvc.ErrRelayQuotaReviewQueryInvalid) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReviewNotFound) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReviewInvalidState) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReviewUnsafeRetry) ||
		errors.Is(err, billingsvc.ErrRelayQuotaReservationBusy) {
		return err
	}
	if errors.Is(err, billingsvc.ErrRelayQuotaOperation) || errors.Is(err, billingsvc.ErrRelayQuotaManualReview) {
		return fmt.Errorf("%w: durable reservation changed", billingsvc.ErrRelayQuotaReviewInvalidState)
	}
	return err
}

func validJimengReviewReservationID(value string) bool {
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
