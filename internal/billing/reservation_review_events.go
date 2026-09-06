package billing

import (
	"errors"
	"fmt"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"math"
)

// AppendRelayQuotaReservationResolutionEventTx appends the immutable operator
// audit for a direct dispatched -> terminal resolution. It must be called from
// the same transaction as the Jimeng task, recovery operation, accounting
// counters, and reservation transition. The complete economic source snapshot
// is compared in the database before the append, and the operator cannot
// replace the amount or channel.
func AppendRelayQuotaReservationResolutionEventTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	operatorUserID int,
	resolution string,
	actualQuota int,
	providerChannelID int,
	eventID string,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if tx == nil || source == nil || operatorUserID <= 0 || eventID == "" || len(eventID) > 64 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	action, targetStatus, expectedActual, err := relayQuotaReviewResolutionShape(source, resolution)
	if err != nil {
		return nil, err
	}
	if actualQuota != expectedActual {
		return nil, fmt.Errorf("%w: terminal quota does not match the immutable request", ErrRelayQuotaReviewUnsafeRetry)
	}
	if providerChannelID <= 0 {
		return nil, fmt.Errorf("%w: provider channel is missing", ErrRelayQuotaReviewUnsafeRetry)
	}

	var current model.RelayQuotaReservationRecord
	result := relayQuotaReviewSnapshotPredicateForStatus(
		locking.SubscriptionLockForUpdate(tx), source, model.RelayQuotaReservationStatusDispatched,
	).
		Where("dispatched_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			source.DispatchedAt, "", int64(0)).
		Limit(1).Find(&current)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrRelayQuotaReservationBusy
	}

	// A resolution event without its terminal transition is impossible through
	// this API because both share a transaction. Refuse to build on such a row
	// rather than treating database corruption as an idempotent replay.
	var existingCount int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", source.ReservationID,
			[]string{RelayQuotaReviewActionResolveSettle, RelayQuotaReviewActionResolveRefund}).
		Count(&existingCount).Error; err != nil {
		return nil, err
	}
	if existingCount != 0 {
		return nil, fmt.Errorf("%w: a terminal resolution audit already exists", ErrRelayQuotaReviewUnsafeRetry)
	}

	var maxRevision int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", source.ReservationID).
		Select("COALESCE(MAX(revision), 0)").Scan(&maxRevision).Error; err != nil {
		return nil, err
	}
	if maxRevision >= int64(math.MaxInt) {
		return nil, errors.New("relay quota review revision overflow")
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return nil, err
	}
	event := model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: source.ReservationID, Revision: int(maxRevision + 1),
		OperatorUserID: operatorUserID, Action: action,
		FromStatus: model.RelayQuotaReservationStatusDispatched, ToStatus: targetStatus,
		Operation: resolution, UserID: source.UserID, TokenID: source.TokenID,
		TokenUnlimited: source.TokenUnlimited, TrustQuotaBypassed: source.TrustQuotaBypassed,
		ChannelID:     providerChannelID,
		FundingSource: source.FundingSource, RequestedQuota: source.RequestedQuota,
		ReservedQuota: source.ReservedQuota, TokenReserved: source.TokenReserved,
		SubscriptionID: source.SubscriptionID, UsageEpoch: source.UsageEpoch,
		ActualQuota: actualQuota, CreatedAt: now,
	}
	if err := tx.Create(&event).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

// AppendRelayQuotaTaskPollRetryEventTx records an operator-authorized polling
// retry without changing accounting. A fixed-price task remains settled and a
// usage-metered task retains its dispatched hold. Callers must update the Task
// and TaskOperation in this same transaction. The event retains the previous
// polling horizon and attempt count because reopening a bounded recovery
// window intentionally resets both journal fields.
func AppendRelayQuotaTaskPollRetryEventTx(
	tx *gorm.DB,
	source *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
	operatorUserID int,
	eventID string,
	now int64,
) (*model.RelayQuotaReservationReviewEvent, error) {
	if tx == nil || source == nil || task == nil || operation == nil ||
		operatorUserID <= 0 || eventID == "" || len(eventID) > 64 || now <= 0 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	shapeErr := relayQuotaVideoReviewShape(source, task, operation)
	if model.IsKlingTaskOperationPlatform(operation.Platform) ||
		model.IsDoubaoVideoTaskOperationPlatform(operation.Platform) ||
		model.IsViduTaskOperationPlatform(operation.Platform) ||
		model.IsHailuoTaskOperationPlatform(operation.Platform) ||
		model.IsAliWanTaskOperationPlatform(operation.Platform) ||
		model.IsSunoTaskOperationPlatform(operation.Platform) ||
		model.IsMidjourneyTaskOperationPlatform(operation.Platform) ||
		model.IsVeoTaskOperationPlatform(operation.Platform) {
		shapeErr = relayQuotaAsyncReviewShape(source, task, operation)
	}
	if shapeErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, shapeErr)
	}
	sourceStatus := source.Status
	if sourceStatus != model.RelayQuotaReservationStatusSettled &&
		sourceStatus != model.RelayQuotaReservationStatusDispatched {
		return nil, fmt.Errorf("%w: task polling accounting state is unsupported", ErrRelayQuotaReviewUnsafeRetry)
	}

	var current model.RelayQuotaReservationRecord
	result := relayQuotaReviewSnapshotPredicateForStatus(
		locking.SubscriptionLockForUpdate(tx), source, sourceStatus,
	).
		Where("dispatched_at = ? AND completed_at = ? AND lease_owner = ? AND lease_expires_at = ?",
			source.DispatchedAt, source.CompletedAt, "", int64(0)).
		Limit(1).Find(&current)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("%w: polling retry accounting snapshot changed", ErrRelayQuotaReservationBusy)
	}
	var terminalReviewCount int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ? AND action IN ?", source.ReservationID,
			[]string{RelayQuotaReviewActionResolveSettle, RelayQuotaReviewActionResolveRefund}).
		Count(&terminalReviewCount).Error; err != nil {
		return nil, err
	}
	if terminalReviewCount != 0 {
		return nil, fmt.Errorf("%w: terminal resolution audit already exists", ErrRelayQuotaReviewUnsafeRetry)
	}

	var maxRevision int64
	if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
		Where("reservation_id = ?", source.ReservationID).
		Select("COALESCE(MAX(revision), 0)").Scan(&maxRevision).Error; err != nil {
		return nil, err
	}
	if maxRevision >= int64(math.MaxInt) {
		return nil, errors.New("relay quota review revision overflow")
	}
	event := model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: source.ReservationID, Revision: int(maxRevision + 1),
		OperatorUserID:         operatorUserID,
		Action:                 model.RelayQuotaReservationReviewActionRetryTaskPoll,
		FromStatus:             sourceStatus,
		ToStatus:               sourceStatus,
		Operation:              source.Operation,
		TaskID:                 task.TaskID,
		TaskPlatform:           task.Platform,
		TaskFromStatus:         model.TaskStatusUnknown,
		TaskToStatus:           model.TaskStatusSubmitted,
		TaskOperationFromState: model.TaskOperationManualReview,
		TaskOperationToState:   model.TaskOperationSubmitted,
		TaskOperationCreatedAt: operation.CreatedAt,
		TaskOperationAttempts:  operation.Attempts,
		UserID:                 source.UserID,
		TokenID:                source.TokenID,
		TokenUnlimited:         source.TokenUnlimited,
		TrustQuotaBypassed:     source.TrustQuotaBypassed,
		ChannelID:              source.ChannelID,
		FundingSource:          source.FundingSource,
		RequestedQuota:         source.RequestedQuota,
		ReservedQuota:          source.ReservedQuota,
		TokenReserved:          source.TokenReserved,
		SubscriptionID:         source.SubscriptionID,
		UsageEpoch:             source.UsageEpoch,
		ActualQuota:            source.ActualQuota,
		CreatedAt:              now,
	}
	if err := tx.Create(&event).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

// FindRelayQuotaTaskPollRetryEventTx returns the immutable proof for a prior
// polling retry. A settled task journal without this event is never treated as
// an idempotent operator replay.
func FindRelayQuotaTaskPollRetryEventTx(
	tx *gorm.DB,
	reservationID string,
) (*model.RelayQuotaReservationReviewEvent, bool, error) {
	if tx == nil || !validRelayQuotaReviewReservationID(reservationID) {
		return nil, false, ErrRelayQuotaReviewQueryInvalid
	}
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", reservationID,
		model.RelayQuotaReservationReviewActionRetryTaskPoll).
		Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, false, result.Error
	}
	return &event, result.RowsAffected == 1, nil
}

// FindRelayQuotaReservationResolutionEventTx returns the one terminal review
// event for a requested disposition. The boolean is false when no such event
// exists; callers must treat a terminal accounting row without this audit as
// a conflict, not as a successful replay.
func FindRelayQuotaReservationResolutionEventTx(
	tx *gorm.DB,
	reservationID, resolution string,
) (*model.RelayQuotaReservationReviewEvent, bool, error) {
	if tx == nil || !validRelayQuotaReviewReservationID(reservationID) {
		return nil, false, ErrRelayQuotaReviewQueryInvalid
	}
	action, _, err := relayQuotaReviewResolutionAction(resolution)
	if err != nil {
		return nil, false, err
	}
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", reservationID, action).
		Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, false, result.Error
	}
	return &event, result.RowsAffected == 1, nil
}

func relayQuotaReviewResolutionShape(
	source *model.RelayQuotaReservationRecord,
	resolution string,
) (action, targetStatus string, expectedActual int, err error) {
	if source == nil || source.Status != model.RelayQuotaReservationStatusDispatched ||
		source.Operation != model.RelayQuotaReservationOperationSettle ||
		source.ActualQuota != relayQuotaDispatchFallbackQuota(source) || source.ChannelID != 0 ||
		source.DispatchedAt <= 0 || source.LeaseOwner != "" || source.LeaseExpiresAt != 0 {
		return "", "", 0, fmt.Errorf("%w: reservation is not an unleased dispatched reservation", ErrRelayQuotaReviewUnsafeRetry)
	}
	if _, validationErr := relayReservationFromRecord(source); validationErr != nil {
		return "", "", 0, fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, validationErr)
	}
	action, targetStatus, err = relayQuotaReviewResolutionAction(resolution)
	if err != nil {
		return "", "", 0, err
	}
	if resolution == RelayQuotaReviewResolutionSettle {
		return action, targetStatus, source.RequestedQuota, nil
	}
	return action, targetStatus, 0, nil
}

func relayQuotaReviewResolutionAction(resolution string) (action, targetStatus string, err error) {
	switch resolution {
	case RelayQuotaReviewResolutionSettle:
		return RelayQuotaReviewActionResolveSettle, model.RelayQuotaReservationStatusSettled, nil
	case RelayQuotaReviewResolutionRefund:
		return RelayQuotaReviewActionResolveRefund, model.RelayQuotaReservationStatusRefunded, nil
	default:
		return "", "", fmt.Errorf("%w: unsupported terminal resolution", ErrRelayQuotaReviewQueryInvalid)
	}
}

func relayQuotaReviewEventFromRecord(eventID string, revision, operatorUserID int, targetStatus string, now int64, record *model.RelayQuotaReservationRecord) model.RelayQuotaReservationReviewEvent {
	return model.RelayQuotaReservationReviewEvent{
		EventID: eventID, ReservationID: record.ReservationID, Revision: revision,
		OperatorUserID: operatorUserID, Action: model.RelayQuotaReservationReviewActionRetry,
		FromStatus: model.RelayQuotaReservationStatusManualReview, ToStatus: targetStatus,
		Operation: record.Operation, UserID: record.UserID, TokenID: record.TokenID,
		TokenUnlimited: record.TokenUnlimited, TrustQuotaBypassed: record.TrustQuotaBypassed,
		ChannelID:     record.ChannelID,
		FundingSource: record.FundingSource, RequestedQuota: record.RequestedQuota,
		ReservedQuota: record.ReservedQuota, TokenReserved: record.TokenReserved,
		SubscriptionID: record.SubscriptionID, UsageEpoch: record.UsageEpoch,
		ActualQuota: record.ActualQuota, CreatedAt: now,
	}
}
