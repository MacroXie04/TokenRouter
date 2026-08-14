package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Subscription status values.
const (
	SubscriptionStatusActive    = "active"
	SubscriptionStatusExpired   = "expired"
	SubscriptionStatusCancelled = "cancelled"
)

// Subscription payment method/provider identifiers.
const (
	PaymentMethodBalance   = "balance"
	PaymentProviderBalance = "balance"
)

// ErrNoActiveSubscription is returned when a user has no active subscription.
var ErrNoActiveSubscription = errors.New("没有生效中的订阅")

// ErrSubscriptionQuotaExceeded is returned when a subscription has no quota left.
var ErrSubscriptionQuotaExceeded = errors.New("订阅额度已用尽")

// CreateSubscriptionPlan persists a subscription plan (admin).
func CreateSubscriptionPlan(p *model.SubscriptionPlan) error {
	if p.TotalAmount <= 0 {
		return errors.New("订阅额度必须大于 0")
	}
	p.CreatedAt = common.NowTimestamp()
	p.UpdatedAt = p.CreatedAt
	return model.DB.Create(p).Error
}

// ListSubscriptionPlans returns enabled plans for the user-facing plans view,
// highest sort order first, with display defaults normalized.
func ListSubscriptionPlans() []model.SubscriptionPlan {
	var plans []model.SubscriptionPlan
	model.DB.Where("enabled = ?", true).Order("sort_order desc, id desc").Find(&plans)
	for i := range plans {
		NormalizeSubscriptionPlanDefaults(&plans[i])
	}
	return plans
}

// GetSubscriptionPlan loads a plan by id.
func GetSubscriptionPlan(id int) (*model.SubscriptionPlan, error) {
	var p model.SubscriptionPlan
	if err := model.DB.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ParseSubscriptionPlanPrice parses the plan's stored price string. An empty
// price means free; an unparseable non-empty value is a parameter error,
// matching the admin plan-validation contract.
func ParseSubscriptionPlanPrice(s string) (float64, error) {
	price := strings.TrimSpace(s)
	if price == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(price, 64)
	if err != nil {
		return 0, errors.New("参数错误")
	}
	return f, nil
}

// calcSubscriptionBalanceQuota converts a plan price (USD) into the wallet
// quota to deduct, rounding up, and rejects amounts that would saturate the
// quota column instead of charging a clamped value.
func calcSubscriptionBalanceQuota(priceAmount float64) (int, error) {
	if priceAmount <= 0 {
		return 0, nil
	}
	quota := decimal.NewFromFloat(priceAmount).
		Mul(decimal.NewFromInt(common.QuotaPerUnit)).
		Ceil()
	return common.QuotaFromDecimalStrict(quota)
}

// PurchaseSubscriptionWithBalance purchases a plan for the user by deducting
// wallet quota. The subscription is created through the same transaction used
// by the admin bind (stacking, per-user purchase cap, calendar-accurate end
// and reset times, group upgrade with previous-group snapshot), together with
// a completed order row; a top-up log records the purchase.
func PurchaseSubscriptionWithBalance(userId, planId int) error {
	if userId <= 0 || planId <= 0 {
		return errors.New("invalid userId or planId")
	}
	var (
		logPlanTitle string
		logMoney     float64
		chargedQuota int
	)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planId).Error; err != nil {
			return err
		}
		NormalizeSubscriptionPlanDefaults(&plan)
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		price, err := ParseSubscriptionPlanPrice(plan.PriceAmount)
		if err != nil {
			return err
		}
		if price < 0 {
			return errors.New("套餐价格不能为负数")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}
		requiredQuota, err := calcSubscriptionBalanceQuota(price)
		if err != nil {
			return err
		}
		// Lock the user row first so concurrent purchases serialize the
		// balance check, purchase-cap check, and group snapshot.
		var user model.User
		if err := subscriptionLockForUpdate(tx).Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		if requiredQuota > 0 {
			if user.Quota < requiredQuota {
				return errors.New("余额不足")
			}
			if err := tx.Model(&model.User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota - ?", requiredQuota)).Error; err != nil {
				return err
			}
		}
		if _, err := createUserSubscriptionFromPlanTx(tx, userId, &plan, PaymentMethodBalance); err != nil {
			return err
		}
		now := common.NowTimestamp()
		order := model.SubscriptionOrder{
			UserId:          userId,
			PlanId:          plan.Id,
			Money:           price,
			TradeNo:         fmt.Sprintf("SUBBALUSR%dNO%s%d", userId, common.RandomAlphanumeric(6), time.Now().UnixNano()),
			PaymentMethod:   PaymentMethodBalance,
			PaymentProvider: PaymentProviderBalance,
			Status:          TopUpStatusSuccess,
			CreateTime:      now,
			CompleteTime:    now,
			ProviderPayload: fmt.Sprintf("charged_quota=%d", requiredQuota),
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		logPlanTitle = plan.Title
		logMoney = price
		chargedQuota = requiredQuota
		return nil
	})
	if err != nil {
		return err
	}
	RecordSystemLog(userId, LogTypeTopup,
		fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", logPlanTitle, logMoney, chargedQuota))
	return nil
}

// GetActiveSubscription returns the user's active (non-expired) subscription.
func GetActiveSubscription(userId int) (*model.UserSubscription, error) {
	var sub model.UserSubscription
	now := common.NowTimestamp()
	if err := model.DB.Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
		Order("id desc").First(&sub).Error; err != nil {
		return nil, ErrNoActiveSubscription
	}
	return &sub, nil
}

// ConsumeSubscriptionQuota deducts quota from the active subscription's pool,
// returning the amount consumed. It is atomic so concurrent requests cannot
// overspend the subscription quota.
func ConsumeSubscriptionQuota(userId, quota int) (int, error) {
	if quota <= 0 {
		return 0, nil
	}
	sub, err := GetActiveSubscription(userId)
	if err != nil {
		return 0, err
	}
	remaining := sub.AmountTotal - sub.AmountUsed
	if remaining < int64(quota) {
		return 0, ErrSubscriptionQuotaExceeded
	}
	res := model.DB.Model(&model.UserSubscription{}).
		Where("id = ? AND amount_used + ? <= amount_total", sub.Id, quota).
		UpdateColumn("amount_used", gormExpr("amount_used + ?", quota))
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, ErrSubscriptionQuotaExceeded
	}
	return quota, nil
}

// ResetSubscriptionQuota performs a due periodic reset: usage is zeroed and
// the schedule advances along calendar-aligned boundaries walked forward from
// the last reset base, so a subscription that slept through several windows
// catches up to the current one instead of drifting.
func ResetSubscriptionQuota(sub *model.UserSubscription) error {
	plan, err := GetSubscriptionPlan(sub.PlanId)
	if err != nil {
		return err
	}
	now := common.NowTimestamp()
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	updates := map[string]any{"updated_at": now}
	if NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod) == SubscriptionResetNever {
		// The plan no longer resets: clear a stale schedule so the reset job
		// stops selecting this subscription. Usage is intentionally kept.
		if sub.NextResetTime == 0 {
			return nil
		}
		updates["next_reset_time"] = 0
		return model.DB.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error
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
	if advanced {
		updates["amount_used"] = 0
		updates["last_reset_time"] = base.Unix()
	} else if next > 0 && sub.NextResetTime == 0 {
		updates["last_reset_time"] = base.Unix()
	} else if next == sub.NextResetTime {
		// Nothing due and nothing to correct.
		return nil
	}
	updates["next_reset_time"] = next
	return model.DB.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error
}

// PurchaseSubscription purchases a plan for the user with wallet balance and
// returns the resulting active subscription (adapter over
// PurchaseSubscriptionWithBalance for the dashboard purchase endpoint).
func PurchaseSubscription(userId, planId int) (*model.UserSubscription, error) {
	if err := PurchaseSubscriptionWithBalance(userId, planId); err != nil {
		return nil, err
	}
	return GetActiveSubscription(userId)
}
