package service

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	return AdminCreateSubscriptionPlan(p)
}

// ListSubscriptionPlans returns enabled plans for the user-facing plans view,
// highest sort order first, with display defaults normalized.
func ListSubscriptionPlans() ([]model.SubscriptionPlan, error) {
	var plans []model.SubscriptionPlan
	if err := model.DB.Where("enabled = ?", true).Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		return nil, err
	}
	for i := range plans {
		NormalizeSubscriptionPlanDefaults(&plans[i])
	}
	return plans, nil
}

// GetSubscriptionPlan loads a plan by id.
func GetSubscriptionPlan(id int) (*model.SubscriptionPlan, error) {
	var p model.SubscriptionPlan
	if err := model.DB.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

const maxSubscriptionPlanPriceTextLength = 64

var maxPersistedSubscriptionPlanPrice = decimal.New(9999999999, -6) // DECIMAL(10,6)

// parseSubscriptionPlanPriceDecimal accepts only finite, plain base-10
// decimal syntax. Exponents are intentionally rejected: extreme exponents can
// make arbitrary-precision comparison allocate unreasonable amounts of memory,
// and the persisted price column represents a conventional decimal amount.
func parseSubscriptionPlanPriceDecimal(s string) (decimal.Decimal, error) {
	price := strings.TrimSpace(s)
	if price == "" {
		return decimal.Zero, nil
	}
	if len(price) > maxSubscriptionPlanPriceTextLength {
		return decimal.Zero, errors.New("参数错误")
	}
	digits := 0
	dotSeen := false
	for i, char := range []byte(price) {
		switch {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' && !dotSeen:
			dotSeen = true
		case (char == '+' || char == '-') && i == 0:
		default:
			return decimal.Zero, errors.New("参数错误")
		}
	}
	if digits == 0 {
		return decimal.Zero, errors.New("参数错误")
	}
	parsed, err := decimal.NewFromString(price)
	if err != nil {
		return decimal.Zero, errors.New("参数错误")
	}
	return parsed, nil
}

func subscriptionPlanPriceFitsDatabase(price decimal.Decimal) bool {
	return price.Abs().LessThanOrEqual(maxPersistedSubscriptionPlanPrice) &&
		price.Equal(price.Truncate(6))
}

// ParseSubscriptionPlanPrice parses the plan's stored price string. An empty
// price means free; a non-decimal or non-finite value is a parameter error,
// matching the admin plan-validation contract.
func ParseSubscriptionPlanPrice(s string) (float64, error) {
	price, err := parseSubscriptionPlanPriceDecimal(s)
	if err != nil {
		return 0, errors.New("参数错误")
	}
	if !subscriptionPlanPriceFitsDatabase(price) {
		return 0, errors.New("参数错误")
	}
	f, _ := price.Float64()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("参数错误")
	}
	return f, nil
}

// calcSubscriptionBalanceQuota converts a plan price (USD) into the wallet
// quota to deduct, rounding up, and rejects amounts that would saturate the
// quota column instead of charging a clamped value.
func calcSubscriptionBalanceQuota(priceAmount float64) (int, error) {
	if math.IsNaN(priceAmount) || math.IsInf(priceAmount, 0) {
		return 0, errors.New("参数错误")
	}
	if priceAmount <= 0 {
		return 0, nil
	}
	quota := decimal.NewFromFloat(priceAmount).
		Mul(decimal.NewFromInt(common.QuotaPerUnit)).
		Ceil()
	return common.QuotaFromDecimalStrict(quota)
}

func calcSubscriptionBalanceQuotaDecimal(price decimal.Decimal) (int, error) {
	if price.Sign() <= 0 {
		return 0, nil
	}
	quota := price.Mul(decimal.NewFromInt(common.QuotaPerUnit)).Ceil()
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
	tradeSuffix, err := common.SecureRandomAlphanumeric(6)
	if err != nil {
		return err
	}
	tradeNo := fmt.Sprintf("SUBBALUSR%dNO%s%d", userId, tradeSuffix, time.Now().UnixNano())
	var (
		logPlanTitle string
		logMoney     float64
		chargedQuota int
		auditEventID string
	)
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planId).Error; err != nil {
			return err
		}
		NormalizeSubscriptionPlanDefaults(&plan)
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		priceDecimal, err := parseSubscriptionPlanPriceDecimal(plan.PriceAmount)
		if err != nil {
			return err
		}
		if !subscriptionPlanPriceFitsDatabase(priceDecimal) {
			return errors.New("参数错误")
		}
		if priceDecimal.IsNegative() {
			return errors.New("套餐价格不能为负数")
		}
		if priceDecimal.GreaterThan(decimal.NewFromFloat(math.MaxFloat64)) {
			return errors.New("参数错误")
		}
		price, _ := priceDecimal.Float64()
		if math.IsNaN(price) || math.IsInf(price, 0) {
			// Money is a legacy display column; billing above remains exact.
			return errors.New("套餐价格无法精确表示")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}
		requiredQuota, err := calcSubscriptionBalanceQuotaDecimal(priceDecimal)
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
			if !common.QuotaWithinBounds(user.Quota) {
				return ErrUserQuotaOverflow
			}
			if user.Quota < requiredQuota {
				return errors.New("余额不足")
			}
			newQuota := user.Quota - requiredQuota
			result := tx.Model(&model.User{}).
				Where("id = ? AND quota = ?", userId, user.Quota).
				Update("quota", newQuota)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrUserQuotaOverflow
			}
		}
		if _, err := createUserSubscriptionFromPlanTx(tx, userId, &plan, PaymentMethodBalance); err != nil {
			return err
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		order := model.SubscriptionOrder{
			UserId:          userId,
			PlanId:          plan.Id,
			Money:           price,
			TradeNo:         tradeNo,
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
		content := fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", logPlanTitle, logMoney, chargedQuota)
		var auditErr error
		auditEventID, auditErr = enqueuePaymentSystemLogTx(tx, "subscription_balance", order.TradeNo, userId, content, now)
		if auditErr != nil {
			return fmt.Errorf("persist balance subscription audit: %w", auditErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
		common.SysError("balance subscription audit delivery deferred: " + err.Error())
	}
	return nil
}

// GetActiveSubscription returns the user's active (non-expired) subscription.
func GetActiveSubscription(userId int) (*model.UserSubscription, error) {
	var sub model.UserSubscription
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, err
	}
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
	if quota == 0 {
		return 0, nil
	}
	if err := validateQuotaAmount(quota); err != nil {
		return 0, fmt.Errorf("subscription quota: %w", err)
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var sub model.UserSubscription
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if err := subscriptionLockForUpdate(tx).
			Where("user_id = ? AND status = ? AND end_time > ?", userId, SubscriptionStatusActive, now).
			Order("id desc").First(&sub).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNoActiveSubscription
			}
			return err
		}
		total, totalOK := boundedSubscriptionQuota(sub.AmountTotal)
		used, usedOK := boundedSubscriptionQuota(sub.AmountUsed)
		if !totalOK || !usedOK || used > total {
			return ErrSubscriptionQuotaOverflow
		}
		resetPlan, err := subscriptionResetPlanTx(tx, &sub)
		if err != nil {
			return err
		}
		if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, resetPlan, now); err != nil {
			return err
		}
		total, totalOK = boundedSubscriptionQuota(sub.AmountTotal)
		used, usedOK = boundedSubscriptionQuota(sub.AmountUsed)
		if !totalOK || !usedOK || used > total {
			return ErrSubscriptionQuotaOverflow
		}
		newUsed, ok := common.AddQuotaWithinBounds(used, quota)
		if !ok {
			return ErrSubscriptionQuotaOverflow
		}
		if newUsed > total {
			return ErrSubscriptionQuotaExceeded
		}
		result := tx.Model(&model.UserSubscription{}).
			Where("id = ? AND status = ? AND end_time > ? AND amount_total = ? AND amount_used = ? AND usage_epoch = ? AND entitlement_version = ?",
				sub.Id, SubscriptionStatusActive, now, sub.AmountTotal, sub.AmountUsed, sub.UsageEpoch, sub.EntitlementVersion).
			UpdateColumn("amount_used", int64(newUsed))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSubscriptionQuotaExceeded
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return quota, nil
}

// ResetSubscriptionQuota performs a due periodic reset: usage is zeroed and
// the schedule advances along calendar-aligned boundaries walked forward from
// the last reset base, so a subscription that slept through several windows
// catches up to the current one instead of drifting.
func ResetSubscriptionQuota(sub *model.UserSubscription) error {
	return ResetSubscriptionQuotaContext(context.Background(), sub)
}

// ResetSubscriptionQuotaContext is ResetSubscriptionQuota's cancellable form.
// The transaction inherits ctx so cancellation cannot publish a partial reset.
func ResetSubscriptionQuotaContext(ctx context.Context, sub *model.UserSubscription) error {
	if ctx == nil {
		return errors.New("subscription reset context is nil")
	}
	if sub == nil || sub.Id <= 0 {
		return errors.New("invalid subscription")
	}
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.UserSubscription
		if err := subscriptionLockForUpdate(tx).Where("id = ?", sub.Id).First(&current).Error; err != nil {
			return err
		}
		if current.Status != SubscriptionStatusActive {
			return ErrSubscriptionStateChanged
		}
		if current.AmountUsed != sub.AmountUsed || current.LastResetTime != sub.LastResetTime ||
			current.NextResetTime != sub.NextResetTime || current.EndTime != sub.EndTime ||
			current.UsageEpoch != sub.UsageEpoch || current.EntitlementVersion != sub.EntitlementVersion {
			return ErrSubscriptionStateChanged
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if current.NextResetTime > 0 && current.NextResetTime > now {
			*sub = current
			return nil
		}
		plan, err := subscriptionResetPlanTx(tx, &current)
		if err != nil {
			return err
		}
		if plan.QuotaResetPeriod == SubscriptionResetNever {
			if current.NextResetTime != 0 {
				if err := updateSubscriptionResetState(tx, &current, current.AmountUsed, current.LastResetTime, 0, now, false); err != nil {
					return err
				}
			}
			*sub = current
			return nil
		}
		baseUnix := current.LastResetTime
		if baseUnix <= 0 {
			baseUnix = current.StartTime
		}
		last, next, advanced := advanceSubscriptionResetSchedule(baseUnix, now, current.EndTime, plan)
		switch {
		case advanced:
			err = updateSubscriptionResetState(tx, &current, 0, last, next, now, true)
		case next > 0 && current.NextResetTime == 0:
			err = updateSubscriptionResetState(tx, &current, current.AmountUsed, baseUnix, next, now, false)
		case next != current.NextResetTime:
			err = updateSubscriptionResetState(tx, &current, current.AmountUsed, current.LastResetTime, next, now, false)
		}
		if err != nil {
			return err
		}
		*sub = current
		return nil
	})
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
