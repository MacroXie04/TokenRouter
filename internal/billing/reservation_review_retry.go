package billing

import (
	"errors"
	"fmt"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"math"
	"sync"
)

// Manual retry is a rare operator action. Serializing it in-process avoids
// SQLite read-to-write upgrade races; MySQL/PostgreSQL and concurrent nodes
// remain protected by the row lock, full-snapshot compare-and-swap, and the
// unique audit revision.
var relayQuotaReviewRetryMu sync.Mutex

// RetryManualReviewRelayQuotaReservation atomically appends a primary-DB audit
// event and returns a structurally valid manual-review row to the pending state
// implied by its existing operation. It never accepts replacement accounting
// fields and the compare-and-swap predicate covers the complete immutable
// economic snapshot.
func RetryManualReviewRelayQuotaReservation(reservationID string, operatorUserID int) (*RelayQuotaReservationRetryResult, error) {
	if !validRelayQuotaReviewReservationID(reservationID) || operatorUserID <= 0 {
		return nil, ErrRelayQuotaReviewQueryInvalid
	}
	if model.DB == nil {
		return nil, errors.New("relay quota reservation database is unavailable")
	}
	relayQuotaReviewRetryMu.Lock()
	defer relayQuotaReviewRetryMu.Unlock()
	var current model.RelayQuotaReservationRecord
	lookup := model.DB.Where("reservation_id = ?", reservationID).Limit(1).Find(&current)
	if lookup.Error != nil {
		return nil, lookup.Error
	}
	if lookup.RowsAffected != 1 {
		return nil, ErrRelayQuotaReviewNotFound
	}
	if !emptyGrokViolationFeeIntent(&current) {
		return retryGrokViolationFeeReviewLocked(&current, operatorUserID)
	}

	eventID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, err
	}
	var outcome *RelayQuotaReservationRetryResult
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.RelayQuotaReservationRecord
		if err := locking.SubscriptionLockForUpdate(tx).Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRelayQuotaReviewNotFound
			}
			return err
		}

		if record.Status != model.RelayQuotaReservationStatusManualReview {
			idempotent, err := relayQuotaReviewIdempotentResultTx(tx, &record)
			if err != nil {
				return err
			}
			if idempotent == nil {
				return fmt.Errorf("%w: current status is %s", ErrRelayQuotaReviewInvalidState, record.Status)
			}
			outcome = idempotent
			return nil
		}

		targetStatus, validationErr := manualReviewRetryTarget(&record)
		if validationErr != nil {
			return fmt.Errorf("%w: %v", ErrRelayQuotaReviewUnsafeRetry, validationErr)
		}

		var maxRevision int64
		if err := tx.Model(&model.RelayQuotaReservationReviewEvent{}).
			Where("reservation_id = ?", record.ReservationID).
			Select("COALESCE(MAX(revision), 0)").Scan(&maxRevision).Error; err != nil {
			return err
		}
		if maxRevision >= int64(math.MaxInt) {
			return errors.New("relay quota review revision overflow")
		}
		now := wallclock.NowTimestamp()
		revision := int(maxRevision + 1)

		result := relayQuotaReviewSnapshotPredicate(tx, &record).
			Updates(map[string]any{
				"status":           targetStatus,
				"attempts":         0,
				"next_attempt_at":  now,
				"lease_owner":      "",
				"lease_expires_at": 0,
				"last_error":       "",
				"updated_at":       now,
				"completed_at":     0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRelayQuotaReservationBusy
		}

		event := relayQuotaReviewEventFromRecord(eventID, revision, operatorUserID, targetStatus, now, &record)
		if err := tx.Create(&event).Error; err != nil {
			return err
		}

		record.Status = targetStatus
		record.Attempts = 0
		record.NextAttemptAt = now
		record.LeaseOwner = ""
		record.LeaseExpiresAt = 0
		record.LastError = ""
		record.UpdatedAt = now
		record.CompletedAt = 0
		item := relayQuotaReservationReviewItem(&record, nil, nil)
		outcome = &RelayQuotaReservationRetryResult{
			Reservation:  item,
			AuditEventID: event.EventID,
			Changed:      true,
		}
		return nil
	})
	if err == nil {
		return outcome, nil
	}

	// If a driver reports a post-COMMIT error, the unique event id proves that
	// this exact state change committed. If another concurrent identical retry
	// won, its matching event makes this request an idempotent replay.
	if verified, verifyErr := verifyRelayQuotaReviewRetry(reservationID, eventID); verifyErr == nil && verified != nil {
		return verified, nil
	}
	return nil, err
}

func manualReviewRetryTarget(record *model.RelayQuotaReservationRecord) (string, error) {
	if record == nil || record.Status != model.RelayQuotaReservationStatusManualReview {
		return "", ErrRelayQuotaReviewInvalidState
	}
	candidate := *record
	switch record.Operation {
	case model.RelayQuotaReservationOperationSettle:
		candidate.Status = model.RelayQuotaReservationStatusPendingSettlement
	case model.RelayQuotaReservationOperationRefund:
		if record.ChannelID != 0 {
			return "", errors.New("refund operation contains a settlement channel")
		}
		candidate.Status = model.RelayQuotaReservationStatusPendingRefund
	default:
		return "", errors.New("manual-review operation is unsupported")
	}
	if _, err := relayReservationFromRecord(&candidate); err != nil {
		return "", err
	}
	return candidate.Status, nil
}

func relayQuotaReviewSnapshotPredicate(tx *gorm.DB, record *model.RelayQuotaReservationRecord) *gorm.DB {
	return relayQuotaReviewSnapshotPredicateForStatus(
		tx, record, model.RelayQuotaReservationStatusManualReview,
	)
}

func relayQuotaReviewSnapshotPredicateForStatus(
	tx *gorm.DB,
	record *model.RelayQuotaReservationRecord,
	status string,
) *gorm.DB {
	return tx.Model(&model.RelayQuotaReservationRecord{}).
		Where("id = ? AND reservation_id = ? AND status = ? AND operation = ?", record.ID,
			record.ReservationID, status, record.Operation).
		Where("user_id = ? AND token_id = ? AND token_unlimited = ? AND trust_quota_bypassed = ? AND channel_id = ?",
			record.UserID, record.TokenID, record.TokenUnlimited, record.TrustQuotaBypassed, record.ChannelID).
		Where("funding_source = ? AND requested_quota = ? AND reserved_quota = ? AND token_reserved = ?",
			record.FundingSource, record.RequestedQuota, record.ReservedQuota, record.TokenReserved).
		Where("subscription_id = ? AND usage_epoch = ? AND actual_quota = ?",
			record.SubscriptionID, record.UsageEpoch, record.ActualQuota)
}

func relayQuotaReviewIdempotentResultTx(tx *gorm.DB, record *model.RelayQuotaReservationRecord) (*RelayQuotaReservationRetryResult, error) {
	var event model.RelayQuotaReservationReviewEvent
	result := tx.Where("reservation_id = ? AND action = ?", record.ReservationID,
		model.RelayQuotaReservationReviewActionRetry).Order("revision DESC").Limit(1).Find(&event)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 || !relayQuotaReviewEventMatchesRecord(&event, record) {
		return nil, nil
	}
	item := relayQuotaReservationReviewItem(record, nil, nil)
	return &RelayQuotaReservationRetryResult{Reservation: item, AuditEventID: event.EventID, Changed: false}, nil
}

func relayQuotaReviewEventMatchesRecord(event *model.RelayQuotaReservationReviewEvent, record *model.RelayQuotaReservationRecord) bool {
	if event == nil || record == nil || event.Action != model.RelayQuotaReservationReviewActionRetry ||
		event.FromStatus != model.RelayQuotaReservationStatusManualReview || event.ReservationID != record.ReservationID ||
		event.Operation != record.Operation || event.UserID != record.UserID || event.TokenID != record.TokenID ||
		event.TokenUnlimited != record.TokenUnlimited || event.TrustQuotaBypassed != record.TrustQuotaBypassed ||
		event.ChannelID != record.ChannelID ||
		event.FundingSource != record.FundingSource || event.RequestedQuota != record.RequestedQuota ||
		event.ReservedQuota != record.ReservedQuota || event.TokenReserved != record.TokenReserved ||
		event.SubscriptionID != record.SubscriptionID || event.UsageEpoch != record.UsageEpoch ||
		event.ActualQuota != record.ActualQuota {
		return false
	}
	switch event.Operation {
	case model.RelayQuotaReservationOperationSettle:
		return event.ToStatus == model.RelayQuotaReservationStatusPendingSettlement &&
			(record.Status == event.ToStatus || record.Status == model.RelayQuotaReservationStatusSettled)
	case model.RelayQuotaReservationOperationRefund:
		return event.ToStatus == model.RelayQuotaReservationStatusPendingRefund &&
			(record.Status == event.ToStatus || record.Status == model.RelayQuotaReservationStatusRefunded)
	default:
		return false
	}
}

func verifyRelayQuotaReviewRetry(reservationID, eventID string) (*RelayQuotaReservationRetryResult, error) {
	var record model.RelayQuotaReservationRecord
	if err := model.DB.Where("reservation_id = ?", reservationID).First(&record).Error; err != nil {
		return nil, err
	}
	if eventID != "" {
		var event model.RelayQuotaReservationReviewEvent
		if err := model.DB.Where("event_id = ?", eventID).First(&event).Error; err == nil {
			if relayQuotaReviewEventMatchesRecord(&event, &record) {
				item := relayQuotaReservationReviewItem(&record, nil, nil)
				return &RelayQuotaReservationRetryResult{Reservation: item, AuditEventID: event.EventID, Changed: true}, nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return relayQuotaReviewIdempotentResultTx(model.DB, &record)
}
