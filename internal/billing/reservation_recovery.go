package billing

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
)

// ReconcileRelayQuotaReservations retries due durable operations and expires
// unused reservations. It is safe for concurrent nodes: every operation is
// claimed with a short lease and accounting commits with its terminal marker.
func ReconcileRelayQuotaReservations() error {
	return ReconcileRelayQuotaReservationsContext(context.Background())
}

// ReconcileRelayQuotaReservationsContext is the cancellable maintenance form.
// Every scan/update and production accounting transaction inherits ctx, and
// cancellation is checked between independently leased records.
func ReconcileRelayQuotaReservationsContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("relay quota reconciliation context is nil")
	}
	db := model.DB.WithContext(ctx)
	now, err := model.DatabaseUnixTimestamp(db)
	if err != nil {
		return err
	}
	return reconcileRelayQuotaReservationsAtContext(ctx, now, defaultRelayReservationBatchSize)
}

func reconcileRelayQuotaReservationsAt(now int64, limit int) error {
	return reconcileRelayQuotaReservationsAtContext(context.Background(), now, limit)
}

func reconcileRelayQuotaReservationsAtContext(ctx context.Context, now int64, limit int) error {
	if ctx == nil {
		return errors.New("relay quota reconciliation context is nil")
	}
	if limit <= 0 {
		limit = defaultRelayReservationBatchSize
	}
	if err := expireHeldRelayQuotaReservationsContext(ctx, now); err != nil {
		return err
	}
	if err := markExhaustedRelayQuotaReservationsContext(ctx, now); err != nil {
		return err
	}
	var records []model.RelayQuotaReservationRecord
	if err := model.DB.WithContext(ctx).Where(
		"status IN ? AND next_attempt_at <= ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
		[]string{
			model.RelayQuotaReservationStatusPendingSettlement,
			model.RelayQuotaReservationStatusPendingRefund,
		},
		now,
		now,
	).Order("next_attempt_at asc, id asc").Limit(limit).Find(&records).Error; err != nil {
		return err
	}
	errs := make([]error, 0)
	for index := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		record := records[index]
		reservation, err := relayReservationFromRecord(&record)
		if err != nil {
			err = markInvalidRelayQuotaReservationForReviewContext(ctx, &record, err, now)
		} else {
			switch record.Status {
			case model.RelayQuotaReservationStatusPendingSettlement:
				err = reconcileRelayQuotaSettlementAtContext(ctx, reservation, true, now)
			case model.RelayQuotaReservationStatusPendingRefund:
				err = reconcileRelayQuotaRefundAtContext(ctx, reservation, true, now)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("reconcile reservation %s: %w", record.ReservationID, err))
		}
	}
	return errors.Join(errs...)
}

func markExhaustedRelayQuotaReservations(now int64) error {
	return markExhaustedRelayQuotaReservationsContext(context.Background(), now)
}

func markExhaustedRelayQuotaReservationsContext(ctx context.Context, now int64) error {
	maxAttempts := relayReservationMaxAttempts()
	reason := "automatic relay quota reconciliation attempt limit reached; reservation state and any holds retained for manual review"
	result := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("status IN ? AND (attempts < 0 OR attempts >= ?) AND (lease_expires_at = 0 OR lease_expires_at <= ?)", []string{
			model.RelayQuotaReservationStatusPendingSettlement,
			model.RelayQuotaReservationStatusPendingRefund,
		}, maxAttempts, now).
		Updates(map[string]any{
			"status":          model.RelayQuotaReservationStatusManualReview,
			"next_attempt_at": 0, "lease_owner": "", "lease_expires_at": 0,
			"last_error": reason, "updated_at": now, "completed_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		logging.SysError(fmt.Sprintf(
			"moved %d exhausted relay quota reservations to manual review",
			result.RowsAffected,
		))
	}
	return nil
}

func markInvalidRelayQuotaReservationForReview(
	record *model.RelayQuotaReservationRecord,
	validationErr error,
	now int64,
) error {
	return markInvalidRelayQuotaReservationForReviewContext(context.Background(), record, validationErr, now)
}

func markInvalidRelayQuotaReservationForReviewContext(
	ctx context.Context,
	record *model.RelayQuotaReservationRecord,
	validationErr error,
	now int64,
) error {
	message := truncateRelayReservationError("invalid durable reservation: " + validationErr.Error())
	result := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND status = ?", record.ID, record.Status).
		Updates(map[string]any{
			"status":     model.RelayQuotaReservationStatusManualReview,
			"last_error": message, "next_attempt_at": 0,
			"lease_owner": "", "lease_expires_at": 0, "updated_at": now, "completed_at": now,
		})
	if result.Error != nil {
		return errors.Join(validationErr, result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.Join(validationErr, ErrRelayQuotaReservationBusy)
	}
	logging.SysError(fmt.Sprintf("relay quota reservation %s requires manual review: %s", record.ReservationID, message))
	return validationErr
}

func expireHeldRelayQuotaReservations(now int64) error {
	return expireHeldRelayQuotaReservationsContext(context.Background(), now)
}

func expireHeldRelayQuotaReservationsContext(ctx context.Context, now int64) error {
	refundReason := "reservation expired before upstream dispatch; queued for automatic refund"
	heldQuery := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("status = ? AND expires_at > 0 AND expires_at <= ?", model.RelayQuotaReservationStatusHeld, now)
	heldQuery = excludeActiveJimengReservations(heldQuery, now)
	heldQuery = excludeActiveTaskReservations(heldQuery, now)
	refundResult := heldQuery.
		Updates(map[string]any{
			"status":          model.RelayQuotaReservationStatusPendingRefund,
			"operation":       model.RelayQuotaReservationOperationRefund,
			"actual_quota":    0,
			"next_attempt_at": now,
			"last_error":      refundReason,
			"updated_at":      now,
		})
	var errs []error
	if refundResult.Error != nil {
		errs = append(errs, fmt.Errorf("expire undispatched relay reservations: %w", refundResult.Error))
	}
	if refundResult.RowsAffected > 0 {
		logging.SysError(fmt.Sprintf("queued %d expired undispatched relay quota reservations for refund", refundResult.RowsAffected))
	}

	// A dispatched request may have been accepted or partially streamed before
	// the process died. Its authoritative usage is unknowable, so automatic
	// refund would create free work and automatic settlement could charge a
	// still-live long request. Retain the reservation state and any holds, and
	// make the condition explicit for an operator. ActualQuota already contains
	// the conservative dispatch fallback: the hold for normal rows or the
	// requested estimate for an explicitly trusted zero-hold row.
	manualReason := "dispatched reservation expired without authoritative usage; accounting state and any holds retained for manual upstream audit and settlement or refund"
	dispatchedQuery := model.DB.WithContext(ctx).Model(&model.RelayQuotaReservationRecord{}).
		Where("status = ? AND expires_at > 0 AND expires_at <= ?", model.RelayQuotaReservationStatusDispatched, now)
	dispatchedQuery = excludeActiveJimengReservations(dispatchedQuery, now)
	dispatchedQuery = excludeActiveTaskReservations(dispatchedQuery, now)
	manualResult := dispatchedQuery.
		Updates(map[string]any{
			"status":           model.RelayQuotaReservationStatusManualReview,
			"operation":        model.RelayQuotaReservationOperationSettle,
			"next_attempt_at":  0,
			"lease_owner":      "",
			"lease_expires_at": 0,
			"last_error":       manualReason,
			"updated_at":       now,
			"completed_at":     now,
		})
	if manualResult.Error != nil {
		errs = append(errs, fmt.Errorf("expire dispatched relay reservations: %w", manualResult.Error))
	}
	if manualResult.RowsAffected > 0 {
		logging.SysError(fmt.Sprintf(
			"moved %d expired dispatched relay quota reservations to manual review with accounting state retained",
			manualResult.RowsAffected,
		))
	}
	return errors.Join(errs...)
}

func excludeActiveJimengReservations(query *gorm.DB, now int64) *gorm.DB {
	if query == nil || !query.Migrator().HasTable(&model.JimengTaskOperation{}) {
		return query
	}
	retentionDays := env.GetEnvInt("JIMENG_RECOVERY_RETENTION_DAYS", 7)
	if retentionDays < 1 || retentionDays > 90 {
		retentionDays = 7
	}
	manualReviewCutoff := now - int64(retentionDays*24*60*60)
	return query.Where(
		"NOT EXISTS (SELECT 1 FROM jimeng_task_operations WHERE jimeng_task_operations.reservation_id = relay_quota_reservations.reservation_id AND (jimeng_task_operations.state IN ? OR (jimeng_task_operations.state = ? AND jimeng_task_operations.completed_at > ?)))",
		[]string{
			model.JimengTaskOperationPrepared,
			model.JimengTaskOperationDispatching,
			model.JimengTaskOperationSubmitted,
		},
		model.JimengTaskOperationManualReview,
		manualReviewCutoff,
	)
}

func excludeActiveTaskReservations(query *gorm.DB, now int64) *gorm.DB {
	if query == nil || !query.Migrator().HasTable(&model.TaskOperation{}) {
		return query
	}
	retentionDays := env.GetEnvInt("TASK_RECOVERY_RETENTION_DAYS", 7)
	if retentionDays < 1 || retentionDays > 90 {
		retentionDays = 7
	}
	manualReviewCutoff := now - int64(retentionDays*24*60*60)
	return query.Where(
		"NOT EXISTS (SELECT 1 FROM task_operations WHERE task_operations.reservation_id = relay_quota_reservations.reservation_id AND (task_operations.state IN ? OR (task_operations.state = ? AND task_operations.completed_at > ?)))",
		[]string{
			model.TaskOperationPrepared,
			model.TaskOperationDispatching,
			model.TaskOperationSubmitted,
		},
		model.TaskOperationManualReview,
		manualReviewCutoff,
	)
}
