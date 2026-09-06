package billing

import (
	"errors"
	"fmt"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"strings"
)

func markDurableRelayQuotaReservationDispatched(
	reservation *RelayQuotaReservation,
	persist RelayQuotaReservationPersistence,
) error {
	if reservation == nil || reservation.reservationId == "" {
		return errors.New("invalid durable relay quota reservation")
	}
	now := wallclock.NowTimestamp()
	alreadyDispatched := false
	err := reservation.transactionRunner()(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservation.reservationId).First(&record).Error; err != nil {
			return err
		}
		// Zero holds are valid only when the immutable trust decision is explicit.
		// Validate the durable tuple before permitting the first network dispatch,
		// so a missing/corrupt marker cannot be inferred from zero balances later.
		if _, validationErr := relayReservationFromRecord(&record); validationErr != nil {
			return validationErr
		}
		switch record.Status {
		case model.RelayQuotaReservationStatusDispatched:
			alreadyDispatched = true
			if persist != nil {
				return persist(tx)
			}
			return nil
		case model.RelayQuotaReservationStatusHeld:
			fallbackQuota := relayQuotaDispatchFallbackQuota(&record)
			result := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("id = ? AND status = ?", record.ID, model.RelayQuotaReservationStatusHeld).
				Updates(map[string]any{
					"status":          model.RelayQuotaReservationStatusDispatched,
					"operation":       model.RelayQuotaReservationOperationSettle,
					"actual_quota":    fallbackQuota,
					"dispatched_at":   now,
					"expires_at":      now + relayReservationHoldSeconds(),
					"updated_at":      now,
					"last_error":      "",
					"next_attempt_at": 0,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRelayQuotaReservationBusy
			}
			if persist != nil {
				return persist(tx)
			}
			return nil
		case model.RelayQuotaReservationStatusManualReview:
			return fmt.Errorf("%w: %s", ErrRelayQuotaManualReview, record.LastError)
		default:
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
	})
	if err == nil {
		return nil
	}
	if alreadyDispatched {
		return err
	}
	// Resolve the same post-COMMIT ambiguity as settlement/refund. Only the
	// dispatched state proves this transition; an unrelated terminal state is
	// deliberately not treated as success.
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservation.reservationId).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusDispatched &&
		record.Operation == model.RelayQuotaReservationOperationSettle &&
		record.ActualQuota == relayQuotaDispatchFallbackQuota(&record) && record.DispatchedAt > 0 {
		if _, validationErr := relayReservationFromRecord(&record); validationErr == nil {
			return nil
		} else {
			verifyErr = validationErr
		}
	}
	return errors.Join(err, wrapRelayReservationError("verify durable dispatch marker", verifyErr))
}

func settleDurableRelayQuotaReservation(reservation *RelayQuotaReservation, actual, channelId int) error {
	if reservation == nil || reservation.funding == nil || reservation.reservationId == "" {
		return errors.New("invalid durable relay quota reservation")
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	if channelId < 0 {
		return errors.New("invalid settlement channel")
	}
	if err := prepareRelayQuotaOperation(
		reservation.reservationId,
		model.RelayQuotaReservationOperationSettle,
		actual,
		channelId,
	); err != nil {
		return err
	}
	return reconcileRelayQuotaSettlement(reservation, false)
}

// settleDurableRelayQuotaReservationWithPersistence is the direct atomic path
// for asynchronous request types whose accepted-task marker must commit with
// accounting. Unlike the generic retry path, it never exposes a
// pending_settlement row that another reconciler could finish without the
// caller-owned persistence callback.
func settleDurableRelayQuotaReservationWithPersistence(
	reservation *RelayQuotaReservation,
	actual, channelId int,
	persist RelayQuotaReservationPersistence,
) error {
	if reservation == nil || reservation.reservationId == "" || persist == nil {
		return errors.New("invalid durable relay settlement persistence")
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	if channelId < 0 {
		return errors.New("invalid settlement channel")
	}
	now := wallclock.NowTimestamp()
	alreadySettled := false
	err := reservation.transactionRunner()(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservation.reservationId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == model.RelayQuotaReservationStatusSettled {
			alreadySettled = true
			if record.Operation != model.RelayQuotaReservationOperationSettle ||
				record.ActualQuota != actual || record.ChannelID != channelId {
				return ErrRelayQuotaOperation
			}
			return persist(tx)
		}
		if record.Status != model.RelayQuotaReservationStatusDispatched {
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
		durable, validationErr := relayReservationFromRecord(&record)
		if validationErr != nil {
			return validationErr
		}
		inlineTransaction := func(fn func(tx *gorm.DB) error) error { return fn(tx) }
		return durable.funding.commitReservedUsageWithTransaction(
			actual,
			record.TokenID,
			record.TokenReserved,
			record.TokenUnlimited,
			channelId,
			func(tx *gorm.DB) (bool, error) {
				if err := persist(tx); err != nil {
					return false, err
				}
				result := tx.Model(&model.RelayQuotaReservationRecord{}).
					Where("id = ? AND status = ?", record.ID, model.RelayQuotaReservationStatusDispatched).
					Updates(map[string]any{
						"status":       model.RelayQuotaReservationStatusSettled,
						"operation":    model.RelayQuotaReservationOperationSettle,
						"actual_quota": actual, "channel_id": channelId,
						"completed_at": now, "updated_at": now,
						"lease_owner": "", "lease_expires_at": 0,
						"next_attempt_at": 0, "last_error": "",
					})
				if result.Error != nil {
					return false, result.Error
				}
				if result.RowsAffected != 1 {
					return false, ErrRelayQuotaReservationBusy
				}
				return false, nil
			},
			false,
			inlineTransaction,
		)
	})
	if err == nil {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	if alreadySettled {
		return err
	}
	// A transaction driver can report an error after COMMIT. The terminal
	// accounting row proves that the persistence callback committed too.
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservation.reservationId).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusSettled &&
		record.Operation == model.RelayQuotaReservationOperationSettle &&
		record.ActualQuota == actual && record.ChannelID == channelId {
		markFundingSessionSettled(reservation.funding, actual)
		return nil
	}
	return errors.Join(err, wrapRelayReservationError("verify atomic durable settlement", verifyErr))
}

func refundDurableRelayQuotaReservation(reservation *RelayQuotaReservation) error {
	if reservation == nil || reservation.funding == nil || reservation.reservationId == "" {
		return errors.New("invalid durable relay quota reservation")
	}
	if err := prepareRelayQuotaOperation(
		reservation.reservationId,
		model.RelayQuotaReservationOperationRefund,
		0,
		0,
	); err != nil {
		return err
	}
	return reconcileRelayQuotaRefund(reservation, false)
}

func refundDurableRelayQuotaReservationWithPersistence(
	reservation *RelayQuotaReservation,
	persist RelayQuotaReservationPersistence,
) error {
	if reservation == nil || reservation.reservationId == "" || persist == nil {
		return errors.New("invalid durable relay refund persistence")
	}
	now := wallclock.NowTimestamp()
	alreadyRefunded := false
	err := reservation.transactionRunner()(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservation.reservationId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == model.RelayQuotaReservationStatusRefunded {
			alreadyRefunded = true
			return persist(tx)
		}
		if record.Status != model.RelayQuotaReservationStatusHeld &&
			record.Status != model.RelayQuotaReservationStatusDispatched {
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
		if _, validationErr := relayReservationFromRecord(&record); validationErr != nil {
			return validationErr
		}
		if err := refundRelayFundingTx(tx, &record); err != nil {
			return err
		}
		if err := refundRelayTokenTx(tx, &record); err != nil {
			return err
		}
		if err := persist(tx); err != nil {
			return err
		}
		result := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("id = ? AND status = ?", record.ID, record.Status).
			Updates(map[string]any{
				"status":       model.RelayQuotaReservationStatusRefunded,
				"operation":    model.RelayQuotaReservationOperationRefund,
				"actual_quota": 0, "channel_id": 0,
				"completed_at": now, "updated_at": now,
				"lease_owner": "", "lease_expires_at": 0,
				"next_attempt_at": 0, "last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err == nil {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	if alreadyRefunded {
		return err
	}
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservation.reservationId).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusRefunded &&
		record.Operation == model.RelayQuotaReservationOperationRefund {
		markFundingSessionRefunded(reservation.funding)
		return nil
	}
	return errors.Join(err, wrapRelayReservationError("verify atomic durable refund", verifyErr))
}

func reverseSettledRelayQuotaReservationWithPersistence(
	reservationID string,
	persist RelayQuotaReservationPersistence,
) error {
	if strings.TrimSpace(reservationID) == "" || persist == nil {
		return errors.New("invalid durable relay reversal persistence")
	}
	now := wallclock.NowTimestamp()
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			return err
		}
		if record.Status == model.RelayQuotaReservationStatusReversed {
			if record.Operation != model.RelayQuotaReservationOperationReverse {
				return ErrRelayQuotaOperation
			}
			return persist(tx)
		}
		if record.Status != model.RelayQuotaReservationStatusSettled ||
			record.Operation != model.RelayQuotaReservationOperationSettle {
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
		if _, validationErr := relayReservationFromRecord(&record); validationErr != nil {
			return validationErr
		}
		if err := reverseSettledRelayFundingTx(tx, &record, now); err != nil {
			return err
		}
		if err := reverseSettledRelayTokenTx(tx, &record); err != nil {
			return err
		}
		if err := persist(tx); err != nil {
			return err
		}
		result := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("id = ? AND status = ? AND operation = ?", record.ID,
				model.RelayQuotaReservationStatusSettled, model.RelayQuotaReservationOperationSettle).
			Updates(map[string]any{
				"status":       model.RelayQuotaReservationStatusReversed,
				"operation":    model.RelayQuotaReservationOperationReverse,
				"completed_at": now, "updated_at": now,
				"lease_owner": "", "lease_expires_at": 0,
				"next_attempt_at": 0, "last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err == nil {
		return nil
	}
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error
	if verifyErr == nil && record.Status == model.RelayQuotaReservationStatusReversed &&
		record.Operation == model.RelayQuotaReservationOperationReverse {
		return nil
	}
	return errors.Join(err, wrapRelayReservationError("verify atomic durable reversal", verifyErr))
}

func prepareRelayQuotaOperation(reservationId, operation string, actual, channelId int) error {
	if reservationId == "" {
		return errors.New("reservation id is empty")
	}
	now := wallclock.NowTimestamp()
	pendingStatus := model.RelayQuotaReservationStatusPendingRefund
	terminalStatus := model.RelayQuotaReservationStatusRefunded
	if operation == model.RelayQuotaReservationOperationSettle {
		pendingStatus = model.RelayQuotaReservationStatusPendingSettlement
		terminalStatus = model.RelayQuotaReservationStatusSettled
	} else if operation != model.RelayQuotaReservationOperationRefund {
		return fmt.Errorf("%w: unsupported operation %q", ErrRelayQuotaOperation, operation)
	}

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("reservation_id = ?", reservationId).First(&record).Error; err != nil {
			return err
		}
		switch record.Status {
		case terminalStatus:
			return nil
		case model.RelayQuotaReservationStatusHeld, model.RelayQuotaReservationStatusDispatched:
			updates := map[string]any{
				"status": pendingStatus, "operation": operation, "actual_quota": actual,
				"channel_id":      channelId,
				"next_attempt_at": now, "updated_at": now, "last_error": "",
			}
			result := tx.Model(&model.RelayQuotaReservationRecord{}).
				Where("id = ? AND status = ?", record.ID, record.Status).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRelayQuotaReservationBusy
			}
			return nil
		case pendingStatus:
			if record.Operation != operation ||
				(operation == model.RelayQuotaReservationOperationSettle &&
					(record.ActualQuota != actual || record.ChannelID != channelId)) {
				return fmt.Errorf("%w: existing=%s/%d/channel-%d requested=%s/%d/channel-%d",
					ErrRelayQuotaOperation, record.Operation, record.ActualQuota, record.ChannelID,
					operation, actual, channelId)
			}
			return nil
		case model.RelayQuotaReservationStatusManualReview:
			return fmt.Errorf("%w: %s", ErrRelayQuotaManualReview, record.LastError)
		default:
			return fmt.Errorf("%w: reservation is %s", ErrRelayQuotaOperation, record.Status)
		}
	})
	if err == nil {
		return nil
	}
	var record model.RelayQuotaReservationRecord
	verifyErr := model.DB.Where("reservation_id = ?", reservationId).First(&record).Error
	if verifyErr == nil {
		if record.Status == terminalStatus ||
			(record.Status == pendingStatus && record.Operation == operation &&
				(operation != model.RelayQuotaReservationOperationSettle ||
					(record.ActualQuota == actual && record.ChannelID == channelId))) {
			return nil
		}
	}
	return errors.Join(err, wrapRelayReservationError("verify durable operation intent", verifyErr))
}

func markFundingSessionSettled(funding *FundingSession, actual int) {
	if funding == nil {
		return
	}
	funding.mu.Lock()
	defer funding.mu.Unlock()
	funding.settled = true
	if funding.source == BillingSourceSubscription && validateQuotaAmount(actual) == nil && validateQuotaAmount(funding.reserved) == nil {
		funding.postDelta = quotaDeltaByComparison(actual, funding.reserved)
	}
}

func markFundingSessionRefunded(funding *FundingSession) {
	if funding == nil {
		return
	}
	funding.mu.Lock()
	funding.refunded = true
	funding.mu.Unlock()
}
