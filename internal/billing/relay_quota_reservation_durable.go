package billing

import (
	"context"
	"errors"
	"fmt"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrRelayQuotaReservationBusy = errors.New("relay quota reservation reconciliation is leased")
	ErrRelayQuotaManualReview    = errors.New("relay quota reservation requires manual review")
	ErrRelayQuotaOperation       = errors.New("relay quota reservation operation conflict")
)

const (
	defaultRelayReservationHoldSeconds  = int64(24 * time.Hour / time.Second)
	defaultRelayReservationLeaseSeconds = int64(30 * time.Second / time.Second)
	defaultRelayReservationMaxAttempts  = 8
	defaultRelayReservationBatchSize    = 100
	maxRelayReservationBackoffSeconds   = int64(5 * time.Minute / time.Second)
	maxRelayReservationHoldSeconds      = int64(30 * 24 * time.Hour / time.Second)
	maxRelayReservationLeaseSeconds     = int64(10 * time.Minute / time.Second)
	maxRelayReservationAttempts         = 100
	maxRelayReservationErrorBytes       = 4096
)

func reconcileRelayQuotaSettlement(reservation *RelayQuotaReservation, worker bool) error {
	return reconcileRelayQuotaSettlementAt(reservation, worker, wallclock.NowTimestamp())
}

func reconcileRelayQuotaSettlementAt(reservation *RelayQuotaReservation, worker bool, now int64) error {
	return reconcileRelayQuotaSettlementAtContext(context.Background(), reservation, worker, now)
}

func reconcileRelayQuotaSettlementAtContext(ctx context.Context, reservation *RelayQuotaReservation, worker bool, now int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return err
	}
	owner := "relay-settle-" + ownerID
	record, claimed, err := claimRelayQuotaReservationContext(
		ctx,
		reservation.reservationId,
		model.RelayQuotaReservationStatusPendingSettlement,
		owner,
		now,
		worker,
	)
	if err != nil {
		return err
	}
	if !claimed {
		if record.Status == model.RelayQuotaReservationStatusSettled {
			markFundingSessionSettled(reservation.funding, record.ActualQuota)
			return nil
		}
		if worker {
			return nil
		}
		return ErrRelayQuotaReservationBusy
	}
	durable, validationErr := relayReservationFromRecord(record)
	if validationErr != nil {
		return markInvalidRelayQuotaReservationForReviewContext(ctx, record, validationErr, now)
	}
	actual := record.ActualQuota
	err = durable.funding.commitReservedUsageWithTransaction(
		actual,
		record.TokenID,
		record.TokenReserved,
		record.TokenUnlimited,
		record.ChannelID,
		func(tx *gorm.DB) (bool, error) {
			return markRelayQuotaReservationTerminalTx(
				tx, record.ReservationID, owner,
				model.RelayQuotaReservationStatusPendingSettlement,
				model.RelayQuotaReservationStatusSettled,
				actual,
			)
		},
		false,
		reservation.transactionRunnerContext(ctx),
	)
	if err == nil {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	terminal, verifyErr := relayQuotaReservationHasStatusContext(ctx, record.ReservationID, model.RelayQuotaReservationStatusSettled)
	if verifyErr == nil && terminal {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	return recordRelayQuotaReconciliationFailureContext(ctx, record.ReservationID, owner, err, verifyErr)
}

func reconcileRelayQuotaRefund(reservation *RelayQuotaReservation, worker bool) error {
	return reconcileRelayQuotaRefundAt(reservation, worker, wallclock.NowTimestamp())
}

func reconcileRelayQuotaRefundAt(reservation *RelayQuotaReservation, worker bool, now int64) error {
	return reconcileRelayQuotaRefundAtContext(context.Background(), reservation, worker, now)
}

func reconcileRelayQuotaRefundAtContext(ctx context.Context, reservation *RelayQuotaReservation, worker bool, now int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return err
	}
	owner := "relay-refund-" + ownerID
	record, claimed, err := claimRelayQuotaReservationContext(
		ctx,
		reservation.reservationId,
		model.RelayQuotaReservationStatusPendingRefund,
		owner,
		now,
		worker,
	)
	if err != nil {
		return err
	}
	if !claimed {
		if record.Status == model.RelayQuotaReservationStatusRefunded {
			markFundingSessionRefunded(reservation.funding)
			return nil
		}
		if worker {
			return nil
		}
		return ErrRelayQuotaReservationBusy
	}
	if _, validationErr := relayReservationFromRecord(record); validationErr != nil {
		return markInvalidRelayQuotaReservationForReviewContext(ctx, record, validationErr, now)
	}

	err = reservation.transactionRunnerContext(ctx)(func(tx *gorm.DB) error {
		if err := refundRelayFundingTx(tx, record); err != nil {
			return err
		}
		if err := refundRelayTokenTx(tx, record); err != nil {
			return err
		}
		_, err := markRelayQuotaReservationTerminalTx(
			tx, record.ReservationID, owner,
			model.RelayQuotaReservationStatusPendingRefund,
			model.RelayQuotaReservationStatusRefunded,
			0,
		)
		return err
	})
	if err == nil {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	terminal, verifyErr := relayQuotaReservationHasStatusContext(ctx, record.ReservationID, model.RelayQuotaReservationStatusRefunded)
	if verifyErr == nil && terminal {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	return recordRelayQuotaReconciliationFailureContext(ctx, record.ReservationID, owner, err, verifyErr)
}

func claimRelayQuotaReservation(
	reservationId, pendingStatus, owner string,
	now int64,
	dueOnly bool,
) (*model.RelayQuotaReservationRecord, bool, error) {
	return claimRelayQuotaReservationContext(context.Background(), reservationId, pendingStatus, owner, now, dueOnly)
}

func claimRelayQuotaReservationContext(
	ctx context.Context,
	reservationId, pendingStatus, owner string,
	now int64,
	dueOnly bool,
) (*model.RelayQuotaReservationRecord, bool, error) {
	leaseUntil := now + relayReservationLeaseSeconds()
	db := model.DB.WithContext(ctx)
	query := db.Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ? AND status = ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			reservationId, pendingStatus, now).
		Where("attempts >= 0 AND attempts < ?", relayReservationMaxAttempts())
	if dueOnly {
		query = query.Where("next_attempt_at <= ?", now)
	}
	result := query.
		Updates(map[string]any{
			"lease_owner": owner, "lease_expires_at": leaseUntil, "updated_at": now,
			"attempts": gorm.Expr("attempts + ?", 1),
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	var record model.RelayQuotaReservationRecord
	if err := db.Where("reservation_id = ?", reservationId).First(&record).Error; err != nil {
		return nil, false, err
	}
	return &record, result.RowsAffected == 1 && record.LeaseOwner == owner, nil
}

func markRelayQuotaReservationTerminalTx(
	tx *gorm.DB,
	reservationId, owner, pendingStatus, terminalStatus string,
	actual int,
) (bool, error) {
	var record model.RelayQuotaReservationRecord
	if err := locking.SubscriptionLockForUpdate(tx).
		Where("reservation_id = ?", reservationId).First(&record).Error; err != nil {
		return false, err
	}
	if record.Status == terminalStatus {
		return true, nil
	}
	if record.Status != pendingStatus || record.LeaseOwner != owner {
		return false, ErrRelayQuotaReservationBusy
	}
	if pendingStatus == model.RelayQuotaReservationStatusPendingSettlement && record.ActualQuota != actual {
		return false, ErrRelayQuotaOperation
	}
	now := wallclock.NowTimestamp()
	result := tx.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ? AND lease_owner = ?", record.ID, pendingStatus, owner).
		Updates(map[string]any{
			"status": terminalStatus, "completed_at": now, "updated_at": now,
			"lease_owner": "", "lease_expires_at": 0, "next_attempt_at": 0, "last_error": "",
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, ErrRelayQuotaReservationBusy
	}
	return false, nil
}

func relayQuotaReservationHasStatus(reservationId, status string) (bool, error) {
	return relayQuotaReservationHasStatusContext(context.Background(), reservationId, status)
}

func relayQuotaReservationHasStatusContext(ctx context.Context, reservationId, status string) (bool, error) {
	var count int64
	err := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("reservation_id = ? AND status = ?", reservationId, status).Count(&count).Error
	return count == 1, err
}

func recordRelayQuotaReconciliationFailure(reservationId, owner string, operationErr, verifyErr error) error {
	return recordRelayQuotaReconciliationFailureContext(context.Background(), reservationId, owner, operationErr, verifyErr)
}

func recordRelayQuotaReconciliationFailureContext(ctx context.Context, reservationId, owner string, operationErr, verifyErr error) error {
	combined := errors.Join(operationErr, wrapRelayReservationError("verify terminal marker", verifyErr))
	if combined == nil {
		return nil
	}
	now := wallclock.NowTimestamp()
	var record model.RelayQuotaReservationRecord
	db := model.DB.WithContext(ctx)
	loadErr := db.Where("reservation_id = ?", reservationId).First(&record).Error
	if loadErr != nil {
		result := errors.Join(combined, fmt.Errorf("load failed reservation: %w", loadErr))
		logging.SysError("relay quota reconciliation failed: " + result.Error())
		return result
	}
	maxAttempts := relayReservationMaxAttempts()
	attempts := record.Attempts
	if attempts < 1 || attempts > maxAttempts {
		attempts = maxAttempts
	}
	status := record.Status
	nextAttempt := now + relayReservationBackoffSeconds(attempts)
	if attempts >= maxAttempts {
		status = model.RelayQuotaReservationStatusManualReview
		nextAttempt = 0
	}
	persistedError := truncateRelayReservationError(combined.Error())
	updates := map[string]any{
		"status": status, "attempts": attempts, "last_error": persistedError,
		"next_attempt_at": nextAttempt, "lease_owner": "", "lease_expires_at": 0,
		"updated_at": now,
	}
	if status == model.RelayQuotaReservationStatusManualReview {
		updates["completed_at"] = now
	}
	result := db.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ? AND lease_owner = ?", record.ID, record.Status, owner).
		Updates(updates)
	if result.Error != nil {
		combined = errors.Join(combined, fmt.Errorf("record reconciliation failure: %w", result.Error))
	} else if result.RowsAffected != 1 {
		combined = errors.Join(combined, errors.New("record reconciliation failure: lease lost"))
	}
	logging.SysError(fmt.Sprintf(
		"relay quota reservation %s reconciliation failed (attempt=%d status=%s): %s",
		reservationId, attempts, status, persistedError,
	))
	return combined
}

func wrapRelayReservationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func truncateRelayReservationError(message string) string {
	message = strings.ToValidUTF8(message, "�")
	if len(message) <= maxRelayReservationErrorBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := maxRelayReservationErrorBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

func relayReservationHoldSeconds() int64 {
	seconds := int64(env.GetEnvInt("RELAY_RESERVATION_HOLD_SECONDS", int(defaultRelayReservationHoldSeconds)))
	if seconds < 60 || seconds > maxRelayReservationHoldSeconds {
		return defaultRelayReservationHoldSeconds
	}
	return seconds
}

func relayReservationLeaseSeconds() int64 {
	seconds := int64(env.GetEnvInt("RELAY_RESERVATION_LEASE_SECONDS", int(defaultRelayReservationLeaseSeconds)))
	if seconds < 1 || seconds > maxRelayReservationLeaseSeconds {
		return defaultRelayReservationLeaseSeconds
	}
	return seconds
}

func relayReservationMaxAttempts() int {
	attempts := env.GetEnvInt("RELAY_RESERVATION_MAX_ATTEMPTS", defaultRelayReservationMaxAttempts)
	if attempts < 1 || attempts > maxRelayReservationAttempts {
		return defaultRelayReservationMaxAttempts
	}
	return attempts
}

func relayReservationBackoffSeconds(attempt int) int64 {
	if attempt < 1 {
		return 1
	}
	shift := attempt - 1
	if shift > 8 {
		shift = 8
	}
	seconds := int64(1) << shift
	if seconds > maxRelayReservationBackoffSeconds {
		return maxRelayReservationBackoffSeconds
	}
	return seconds
}
