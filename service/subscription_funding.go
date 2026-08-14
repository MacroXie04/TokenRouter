package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Subscription pre-consume ledger statuses.
const (
	SubscriptionPreConsumeStatusConsumed = "consumed"
	SubscriptionPreConsumeStatusRefunded = "refunded"
)

// SubscriptionPreConsumeResult reports the effect of a subscription
// pre-consume, including the balance snapshot used for billing logs.
type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
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
	base := time.Unix(baseUnix, 0)
	next := calcSubscriptionNextResetTime(base, plan, sub.EndTime)
	advanced := false
	for next > 0 && next <= now {
		advanced = true
		base = time.Unix(next, 0)
		next = calcSubscriptionNextResetTime(base, plan, sub.EndTime)
	}
	if !advanced {
		// No reset was due; initialize a missing schedule so the next one fires.
		if sub.NextResetTime == 0 && next > 0 {
			sub.NextResetTime = next
			sub.LastResetTime = base.Unix()
			sub.UpdatedAt = now
			return tx.Save(sub).Error
		}
		return nil
	}
	sub.AmountUsed = 0
	sub.LastResetTime = base.Unix()
	sub.NextResetTime = next
	sub.UpdatedAt = now
	return tx.Save(sub).Error
}

// PreConsumeUserSubscription reserves amount against one of the user's active
// subscriptions, recording an idempotent ledger row keyed by requestId. A
// replayed requestId returns the original reservation without double
// consuming; a refunded requestId can never be consumed again.
func PreConsumeUserSubscription(requestId string, userId int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	now := common.NowTimestamp()
	result := &SubscriptionPreConsumeResult{}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.SubscriptionPreConsumeRecord
		query := tx.Where("request_id = ?", requestId).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if existing.Status == SubscriptionPreConsumeStatusRefunded {
				return errors.New("subscription pre-consume already refunded")
			}
			var sub model.UserSubscription
			if err := tx.Where("id = ?", existing.UserSubscriptionId).First(&sub).Error; err != nil {
				return err
			}
			result.UserSubscriptionId = sub.Id
			result.PreConsumed = existing.PreConsumed
			result.AmountTotal = sub.AmountTotal
			result.AmountUsedBefore = sub.AmountUsed
			result.AmountUsedAfter = sub.AmountUsed
			return nil
		}
		var subs []model.UserSubscription
		if err := subscriptionLockForUpdate(tx).
			Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
			Order("end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return errors.New("no active subscription")
		}
		if len(subs) == 0 {
			return errors.New("no active subscription")
		}
		for i := range subs {
			sub := subs[i]
			plan, err := getSubscriptionPlanByIdTx(tx, sub.PlanId)
			if err != nil {
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
				return err
			}
			usedBefore := sub.AmountUsed
			if sub.AmountTotal > 0 {
				remain := sub.AmountTotal - usedBefore
				if remain < amount {
					continue
				}
			}
			record := &model.SubscriptionPreConsumeRecord{
				RequestId:          requestId,
				UserId:             userId,
				UserSubscriptionId: sub.Id,
				PreConsumed:        amount,
				Status:             SubscriptionPreConsumeStatusConsumed,
				CreatedAt:          now,
				UpdatedAt:          now,
			}
			if err := tx.Create(record).Error; err != nil {
				// A concurrent request with the same id won the unique index:
				// treat as a replay of that reservation.
				var dup model.SubscriptionPreConsumeRecord
				if err2 := tx.Where("request_id = ?", requestId).First(&dup).Error; err2 == nil {
					if dup.Status == SubscriptionPreConsumeStatusRefunded {
						return errors.New("subscription pre-consume already refunded")
					}
					result.UserSubscriptionId = sub.Id
					result.PreConsumed = dup.PreConsumed
					result.AmountTotal = sub.AmountTotal
					result.AmountUsedBefore = sub.AmountUsed
					result.AmountUsedAfter = sub.AmountUsed
					return nil
				}
				return err
			}
			sub.AmountUsed += amount
			sub.UpdatedAt = now
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			result.UserSubscriptionId = sub.Id
			result.PreConsumed = amount
			result.AmountTotal = sub.AmountTotal
			result.AmountUsedBefore = usedBefore
			result.AmountUsedAfter = sub.AmountUsed
			return nil
		}
		return fmt.Errorf("subscription quota insufficient, need=%d", amount)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func postConsumeUserSubscriptionDeltaTx(tx *gorm.DB, userSubscriptionId int, delta int64, now int64) error {
	var sub model.UserSubscription
	if err := subscriptionLockForUpdate(tx).
		Where("id = ?", userSubscriptionId).
		First(&sub).Error; err != nil {
		return err
	}
	newUsed := sub.AmountUsed + delta
	if newUsed < 0 {
		newUsed = 0
	}
	if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
	}
	sub.AmountUsed = newUsed
	sub.UpdatedAt = now
	return tx.Save(&sub).Error
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
	now := common.NowTimestamp()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return postConsumeUserSubscriptionDeltaTx(tx, userSubscriptionId, delta, now)
	})
}

// RefundSubscriptionPreConsume returns a pre-consumed reservation to the
// subscription. It is idempotent by requestId: refunding twice is a no-op, so
// callers may retry safely.
func RefundSubscriptionPreConsume(requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	now := common.NowTimestamp()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var record model.SubscriptionPreConsumeRecord
		if err := subscriptionLockForUpdate(tx).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == SubscriptionPreConsumeStatusRefunded {
			return nil
		}
		if record.PreConsumed > 0 {
			if err := postConsumeUserSubscriptionDeltaTx(tx, record.UserSubscriptionId, -record.PreConsumed, now); err != nil {
				return err
			}
		}
		record.Status = SubscriptionPreConsumeStatusRefunded
		record.UpdatedAt = now
		return tx.Save(&record).Error
	})
}

// CleanupSubscriptionPreConsumeRecords removes old idempotency ledger rows to
// keep the table small. olderThanSeconds <= 0 defaults to seven days.
func CleanupSubscriptionPreConsumeRecords(olderThanSeconds int64) (int64, error) {
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	cutoff := common.NowTimestamp() - olderThanSeconds
	res := model.DB.Where("updated_at < ?", cutoff).Delete(&model.SubscriptionPreConsumeRecord{})
	return res.RowsAffected, res.Error
}

// HasActiveUserSubscription is a lightweight existence check used to pick the
// funding source without opening a pre-consume transaction.
func HasActiveUserSubscription(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.NowTimestamp()
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
	now := common.NowTimestamp()
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
	now := common.NowTimestamp()
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
