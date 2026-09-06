package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Subscription pre-consume ledger statuses.
const (
	SubscriptionPreConsumeStatusConsumed = "consumed"
	SubscriptionPreConsumeStatusSettled  = "settled"
	SubscriptionPreConsumeStatusRefunded = "refunded"
)

// SubscriptionPreConsumeResult reports the effect of a subscription
// pre-consume, including the balance snapshot used for billing logs.
type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
	UsageEpoch         int64
	PreConsumed        int64
	AmountTotal        int64
	AmountUsedBefore   int64
	AmountUsedAfter    int64
}

func getSubscriptionPlanByIdTx(tx *gorm.DB, planId int) (*model.SubscriptionPlan, error) {
	var plan model.SubscriptionPlan
	if err := tx.Where("id = ?", planId).First(&plan).Error; err != nil {
		return nil, err
	}
	return &plan, nil
}

// maybeResetUserSubscriptionWithPlanTx lazily applies any quota resets that
// became due since the subscription was last touched, walking the calendar
// forward from the last reset. Unlike the periodic-job reset path, a plan on
// the "never" period leaves any stale schedule untouched (deviation #17).
func maybeResetUserSubscriptionWithPlanTx(tx *gorm.DB, sub *model.UserSubscription, plan *model.SubscriptionPlan, now int64) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	if NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod) == SubscriptionResetNever {
		return nil
	}
	baseUnix := sub.LastResetTime
	if baseUnix <= 0 {
		baseUnix = sub.StartTime
	}
	last, next, advanced := advanceSubscriptionResetSchedule(baseUnix, now, sub.EndTime, plan)
	if !advanced {
		// No reset was due; initialize a missing schedule so the next one fires.
		if sub.NextResetTime == 0 && next > 0 {
			return updateSubscriptionResetState(tx, sub, sub.AmountUsed, baseUnix, next, now, false)
		}
		return nil
	}
	return updateSubscriptionResetState(tx, sub, 0, last, next, now, true)
}

// PreConsumeUserSubscription reserves amount against one of the user's active
// subscriptions, recording an idempotent ledger row keyed by requestId. A
// replayed requestId returns the original reservation without double
// consuming; a refunded requestId can never be consumed again.
func PreConsumeUserSubscription(requestId string, userId int, amount int64) (*SubscriptionPreConsumeResult, error) {
	var result *SubscriptionPreConsumeResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var innerErr error
		result, innerErr = preConsumeUserSubscriptionTx(tx, requestId, userId, amount)
		return innerErr
	})
	return result, err
}

// preConsumeUserSubscriptionTx is the checked transaction-scoped form used
// when a caller must persist a durable reservation marker atomically with the
// subscription hold.
func preConsumeUserSubscriptionTx(tx *gorm.DB, requestId string, userId int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if tx == nil {
		return nil, errors.New("subscription transaction is nil")
	}
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	if amount > common.MaxQuota {
		return nil, ErrSubscriptionQuotaOverflow
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return nil, err
	}
	result := &SubscriptionPreConsumeResult{}
	var existing model.SubscriptionPreConsumeRecord
	query := subscriptionLockForUpdate(tx).Where("request_id = ?", requestId).Limit(1).Find(&existing)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected > 0 {
		if err := populateSubscriptionPreConsumeReplay(tx, &existing, userId, result); err != nil {
			return nil, err
		}
		return result, nil
	}
	var subs []model.UserSubscription
	if err := subscriptionLockForUpdate(tx).
		Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
		Order("end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, errors.New("no active subscription")
	}
	for i := range subs {
		sub := subs[i]
		plan, err := subscriptionResetPlanTx(tx, &sub)
		if err != nil {
			return nil, err
		}
		if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
			return nil, err
		}
		usedBefore := sub.AmountUsed
		total, totalOK := boundedSubscriptionQuota(sub.AmountTotal)
		used, usedOK := boundedSubscriptionQuota(usedBefore)
		if !totalOK || !usedOK || (total > 0 && used > total) {
			return nil, fmt.Errorf("%w: subscription=%d used=%d total=%d",
				ErrSubscriptionQuotaOverflow, sub.Id, usedBefore, sub.AmountTotal)
		}
		newUsed, ok := common.AddQuotaWithinBounds(used, int(amount))
		if !ok {
			return nil, ErrSubscriptionQuotaOverflow
		}
		if total > 0 && newUsed > total {
			continue
		}
		record := &model.SubscriptionPreConsumeRecord{
			RequestId:          requestId,
			UserId:             userId,
			UserSubscriptionId: sub.Id,
			PreConsumed:        amount,
			UsageEpoch:         sub.UsageEpoch,
			Status:             SubscriptionPreConsumeStatusConsumed,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		create := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "request_id"}},
			DoNothing: true,
		}).Create(record)
		if create.Error != nil {
			return nil, create.Error
		}
		if create.RowsAffected == 0 {
			var duplicate model.SubscriptionPreConsumeRecord
			if err := subscriptionLockForUpdate(tx).Where("request_id = ?", requestId).First(&duplicate).Error; err != nil {
				return nil, err
			}
			if err := populateSubscriptionPreConsumeReplay(tx, &duplicate, userId, result); err != nil {
				return nil, err
			}
			return result, nil
		}
		update := tx.Model(&model.UserSubscription{}).
			Where("id = ? AND amount_used = ? AND amount_total = ? AND usage_epoch = ? AND entitlement_version = ? AND status = ? AND end_time > ?",
				sub.Id, sub.AmountUsed, sub.AmountTotal, sub.UsageEpoch, sub.EntitlementVersion, SubscriptionStatusActive, now).
			Updates(map[string]any{"amount_used": int64(newUsed), "updated_at": now})
		if update.Error != nil {
			return nil, update.Error
		}
		if update.RowsAffected != 1 {
			return nil, errors.New("subscription changed during pre-consume")
		}
		result.UserSubscriptionId = sub.Id
		result.UsageEpoch = sub.UsageEpoch
		result.PreConsumed = amount
		result.AmountTotal = sub.AmountTotal
		result.AmountUsedBefore = usedBefore
		result.AmountUsedAfter = int64(newUsed)
		return result, nil
	}
	return nil, fmt.Errorf("subscription quota insufficient, need=%d", amount)
}

func populateSubscriptionPreConsumeReplay(
	tx *gorm.DB,
	record *model.SubscriptionPreConsumeRecord,
	userId int,
	result *SubscriptionPreConsumeResult,
) error {
	if tx == nil || record == nil || result == nil || record.UserId != userId {
		return errors.New("subscription pre-consume request does not belong to user")
	}
	if record.Status == SubscriptionPreConsumeStatusRefunded {
		return errors.New("subscription pre-consume already refunded")
	}
	if record.Status == SubscriptionPreConsumeStatusSettled {
		return errors.New("subscription pre-consume already settled")
	}
	if record.Status != SubscriptionPreConsumeStatusConsumed || record.PreConsumed <= 0 || record.PreConsumed > common.MaxQuota {
		return ErrSubscriptionQuotaOverflow
	}
	var sub model.UserSubscription
	if err := subscriptionLockForUpdate(tx).Where("id = ? AND user_id = ?", record.UserSubscriptionId, userId).
		First(&sub).Error; err != nil {
		return err
	}
	total, totalOK := boundedSubscriptionQuota(sub.AmountTotal)
	used, usedOK := boundedSubscriptionQuota(sub.AmountUsed)
	if !totalOK || !usedOK || (total > 0 && used > total) {
		return ErrSubscriptionQuotaOverflow
	}
	result.UserSubscriptionId = sub.Id
	result.UsageEpoch = record.UsageEpoch
	result.PreConsumed = record.PreConsumed
	result.AmountTotal = sub.AmountTotal
	result.AmountUsedBefore = sub.AmountUsed
	result.AmountUsedAfter = sub.AmountUsed
	return nil
}

func postConsumeUserSubscriptionDeltaTx(tx *gorm.DB, userSubscriptionId int, delta int64, now int64) error {
	return postConsumeUserSubscriptionDeltaEpochTx(tx, userSubscriptionId, delta, now, nil)
}

func postConsumeUserSubscriptionDeltaEpochTx(tx *gorm.DB, userSubscriptionId int, delta int64, now int64, expectedEpoch *int64) error {
	var sub model.UserSubscription
	if err := subscriptionLockForUpdate(tx).
		Where("id = ?", userSubscriptionId).
		First(&sub).Error; err != nil {
		return err
	}
	if expectedEpoch != nil && sub.UsageEpoch != *expectedEpoch {
		return nil
	}
	total, totalOK := boundedSubscriptionQuota(sub.AmountTotal)
	used, usedOK := boundedSubscriptionQuota(sub.AmountUsed)
	if !totalOK || !usedOK || (total > 0 && used > total) {
		return ErrSubscriptionQuotaOverflow
	}
	newUsed, ok := applySubscriptionQuotaDelta(sub.AmountUsed, delta)
	if !ok {
		return ErrSubscriptionQuotaOverflow
	}
	if total > 0 && newUsed > int64(total) {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
	}
	if newUsed == sub.AmountUsed {
		return nil
	}
	update := tx.Model(&model.UserSubscription{}).
		Where("id = ? AND amount_used = ? AND amount_total = ? AND usage_epoch = ?", sub.Id, sub.AmountUsed, sub.AmountTotal, sub.UsageEpoch).
		Updates(map[string]any{"amount_used": newUsed, "updated_at": now})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("subscription changed during post-consume")
	}
	return nil
}

// PostConsumeUserSubscriptionDelta adjusts a subscription's used amount by
// delta (positive consumes more, negative refunds), clamping at zero and
// rejecting overshoot past the total.
func PostConsumeUserSubscriptionDelta(userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		return postConsumeUserSubscriptionDeltaTx(tx, userSubscriptionId, delta, now)
	})
}

// settleSubscriptionPreConsumeTx closes the idempotency ledger in the same
// transaction as authoritative usage accounting. A non-empty return value
// means an earlier transaction already terminalized the reservation, so the
// caller must not apply any counters again.
//
// An empty request ID is accepted only for legacy restored FundingSessions
// whose historical persistence format did not retain the ledger key.
func settleSubscriptionPreConsumeTx(
	tx *gorm.DB,
	requestId string,
	userId, userSubscriptionId, reserved int,
	usageEpoch, now int64,
) (string, error) {
	if tx == nil {
		return "", errors.New("subscription transaction is nil")
	}
	requestId = strings.TrimSpace(requestId)
	if requestId == "" {
		return "", nil
	}
	var record model.SubscriptionPreConsumeRecord
	if err := subscriptionLockForUpdate(tx).
		Where("request_id = ?", requestId).First(&record).Error; err != nil {
		return "", err
	}
	if record.UserId != userId || record.UserSubscriptionId != userSubscriptionId ||
		record.PreConsumed != int64(reserved) || record.UsageEpoch != usageEpoch {
		return "", errors.New("subscription pre-consume does not match funding session")
	}
	switch record.Status {
	case SubscriptionPreConsumeStatusSettled, SubscriptionPreConsumeStatusRefunded:
		return record.Status, nil
	case SubscriptionPreConsumeStatusConsumed:
	default:
		return "", fmt.Errorf("unsupported subscription pre-consume status %q", record.Status)
	}
	transition := tx.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("id = ? AND status = ? AND usage_epoch = ?", record.Id, SubscriptionPreConsumeStatusConsumed, usageEpoch).
		Updates(map[string]any{"status": SubscriptionPreConsumeStatusSettled, "updated_at": now})
	if transition.Error != nil {
		return "", transition.Error
	}
	if transition.RowsAffected != 1 {
		return "", errors.New("subscription pre-consume changed during settlement")
	}
	return "", nil
}

// RefundSubscriptionPreConsume returns a pre-consumed reservation to the
// subscription. It is idempotent by requestId: refunding twice is a no-op, so
// callers may retry safely.
func RefundSubscriptionPreConsume(requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var record model.SubscriptionPreConsumeRecord
		if err := subscriptionLockForUpdate(tx).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == SubscriptionPreConsumeStatusRefunded {
			return nil
		}
		if record.Status == SubscriptionPreConsumeStatusSettled {
			return errors.New("subscription pre-consume already settled")
		}
		if record.Status != SubscriptionPreConsumeStatusConsumed || record.PreConsumed <= 0 || record.PreConsumed > common.MaxQuota {
			return ErrSubscriptionQuotaOverflow
		}
		if record.PreConsumed > 0 {
			if err := postConsumeUserSubscriptionDeltaEpochTx(tx, record.UserSubscriptionId, 0-record.PreConsumed, now, &record.UsageEpoch); err != nil {
				return err
			}
		}
		transition := tx.Model(&model.SubscriptionPreConsumeRecord{}).
			Where("id = ? AND status = ? AND usage_epoch = ?", record.Id, SubscriptionPreConsumeStatusConsumed, record.UsageEpoch).
			Updates(map[string]any{"status": SubscriptionPreConsumeStatusRefunded, "updated_at": now})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return errors.New("subscription pre-consume changed during refund")
		}
		return nil
	})
}

// CleanupSubscriptionPreConsumeRecords removes old idempotency ledger rows to
// keep the table small. olderThanSeconds <= 0 defaults to seven days.
func CleanupSubscriptionPreConsumeRecords(olderThanSeconds int64) (int64, error) {
	return CleanupSubscriptionPreConsumeRecordsContext(context.Background(), olderThanSeconds)
}

// CleanupSubscriptionPreConsumeRecordsContext is the cancellable form used by
// lease-managed maintenance. Cancellation rolls back the deletion transaction.
func CleanupSubscriptionPreConsumeRecordsContext(ctx context.Context, olderThanSeconds int64) (int64, error) {
	if ctx == nil {
		return 0, errors.New("subscription pre-consume cleanup context is nil")
	}
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	var deleted int64
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if olderThanSeconds >= now {
			return nil
		}
		cutoff := now - olderThanSeconds
		// A nonterminal durable relay reservation still needs its subscription
		// ledger for operator audit and normal refund handling. Keep those rows even
		// when the generic idempotency retention window has elapsed. NOT IN is safe
		// here because relay reservation IDs are non-null and unique on every
		// supported database.
		protectedRequestIds := tx.Model(&model.RelayQuotaReservationRecord{}).
			Select("reservation_id").
			Where("status IN ?", []string{
				model.RelayQuotaReservationStatusHeld,
				model.RelayQuotaReservationStatusDispatched,
				model.RelayQuotaReservationStatusPendingSettlement,
				model.RelayQuotaReservationStatusPendingRefund,
				model.RelayQuotaReservationStatusManualReview,
			})
		res := tx.Where("updated_at < ?", cutoff).
			Where("request_id NOT IN (?)", protectedRequestIds).
			Delete(&model.SubscriptionPreConsumeRecord{})
		deleted = res.RowsAffected
		return res.Error
	})
	return deleted, err
}

// HasActiveUserSubscription is a lightweight existence check used to pick the
// funding source without opening a pre-consume transaction.
func HasActiveUserSubscription(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	var count int64
	if err := model.DB.Model(&model.UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UserActiveSubscriptionsAllowWalletOverflow reports whether the wallet may be
// used once subscription quota is exhausted: a single active subscription with
// allow_wallet_overflow=false blocks the fallback.
func UserActiveSubscriptionsAllowWalletOverflow(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	var strictCount int64
	if err := model.DB.Model(&model.UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ? AND allow_wallet_overflow = ?",
			userId, SubscriptionStatusActive, now, false).
		Count(&strictCount).Error; err != nil {
		return false, err
	}
	return strictCount == 0, nil
}

// GetAllActiveUserSubscriptions returns the user's active subscriptions,
// newest ending first.
func GetAllActiveUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, err
	}
	var subs []model.UserSubscription
	if err := model.DB.
		Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
		Order("end_time desc, id desc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

// GetAllUserSubscriptions returns every subscription of the user (any
// status), newest ending first.
func GetAllUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []model.UserSubscription
	if err := model.DB.
		Where("user_id = ?", userId).
		Order("end_time desc, id desc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

func buildSubscriptionSummaries(subs []model.UserSubscription) []SubscriptionSummary {
	result := make([]SubscriptionSummary, 0, len(subs))
	for i := range subs {
		result = append(result, SubscriptionSummary{Subscription: &subs[i]})
	}
	return result
}
