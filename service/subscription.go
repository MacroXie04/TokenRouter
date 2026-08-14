package service

import (
	"errors"
	"strconv"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Subscription status values.
const (
	SubscriptionStatusActive    = "active"
	SubscriptionStatusExpired   = "expired"
	SubscriptionStatusCancelled = "cancelled"
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

// ListSubscriptionPlans returns enabled plans ordered by sort order.
func ListSubscriptionPlans() []model.SubscriptionPlan {
	var plans []model.SubscriptionPlan
	model.DB.Where("enabled = ?", true).Order("sort_order asc").Find(&plans)
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

// PurchaseSubscription purchases a plan for a user (offline balance path),
// creating an order and immediately activating the subscription.
func PurchaseSubscription(userId, planId int) (*model.UserSubscription, error) {
	plan, err := GetSubscriptionPlan(planId)
	if err != nil {
		return nil, err
	}
	if !plan.Enabled {
		return nil, errors.New("订阅计划未启用")
	}

	now := common.NowTimestamp()
	// Cancel any previous active subscription.
	_ = model.DB.Model(&model.UserSubscription{}).
		Where("user_id = ? AND status = ?", userId, SubscriptionStatusActive).
		Update("status", SubscriptionStatusCancelled).Error

	order := model.SubscriptionOrder{
		UserId:        userId,
		PlanId:        planId,
		Money:         parsePrice(plan.PriceAmount),
		TradeNo:       common.RandomAlphanumeric(32),
		PaymentMethod: "balance",
		Status:        "success",
		CreateTime:    now,
		CompleteTime:  now,
	}
	if err := model.DB.Create(&order).Error; err != nil {
		return nil, err
	}

	endTime := now + int64(planDuration(plan).Seconds())
	sub := model.UserSubscription{
		UserId:        userId,
		PlanId:        planId,
		AmountTotal:   plan.TotalAmount,
		AmountUsed:    0,
		StartTime:     now,
		EndTime:       endTime,
		Status:        SubscriptionStatusActive,
		Source:        "purchase",
		NextResetTime: nextResetTime(plan, now),
		UpgradeGroup:  plan.UpgradeGroup,
		DowngradeGroup: plan.DowngradeGroup,
		AllowWalletOverflow: plan.AllowWalletOverflow != nil && *plan.AllowWalletOverflow,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := model.DB.Create(&sub).Error; err != nil {
		return nil, err
	}
	// Apply the plan's group upgrade so the user's routing group changes.
	if plan.UpgradeGroup != "" {
		_ = SetUserGroup(userId, plan.UpgradeGroup)
	}
	return &sub, nil
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

// ResetSubscriptionQuota resets the subscription's used quota according to its
// reset period and advances NextResetTime.
func ResetSubscriptionQuota(sub *model.UserSubscription) error {
	plan, err := GetSubscriptionPlan(sub.PlanId)
	if err != nil {
		return err
	}
	now := common.NowTimestamp()
	return model.DB.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).
		Updates(map[string]any{
			"amount_used":      0,
			"last_reset_time":  now,
			"next_reset_time":  nextResetTime(plan, now),
		}).Error
}

func planDuration(plan *model.SubscriptionPlan) time.Duration {
	if plan.CustomSeconds > 0 {
		return time.Duration(plan.CustomSeconds) * time.Second
	}
	n := plan.DurationValue
	if n <= 0 {
		n = 1
	}
	switch plan.DurationUnit {
	case "day":
		return time.Duration(n) * 24 * time.Hour
	case "month":
		return time.Duration(n) * 30 * 24 * time.Hour
	case "year":
		return time.Duration(n) * 365 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

func nextResetTime(plan *model.SubscriptionPlan, now int64) int64 {
	if plan.QuotaResetCustomSeconds > 0 {
		return now + plan.QuotaResetCustomSeconds
	}
	switch plan.QuotaResetPeriod {
	case "daily":
		return now + int64(24*time.Hour.Seconds())
	case "weekly":
		return now + int64(7*24*time.Hour.Seconds())
	case "monthly":
		return now + int64(30*24*time.Hour.Seconds())
	default:
		return 0 // no periodic reset
	}
}

func parsePrice(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
