package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Subscription plan duration units and quota reset periods.
const (
	SubscriptionDurationYear   = "year"
	SubscriptionDurationMonth  = "month"
	SubscriptionDurationDay    = "day"
	SubscriptionDurationHour   = "hour"
	SubscriptionDurationCustom = "custom"

	SubscriptionResetNever   = "never"
	SubscriptionResetDaily   = "daily"
	SubscriptionResetWeekly  = "weekly"
	SubscriptionResetMonthly = "monthly"
	SubscriptionResetCustom  = "custom"
)

// ErrPaymentComplianceRequired gates payment-adjacent admin operations until
// the operator confirms the compliance statement.
var ErrPaymentComplianceRequired = errors.New("支付、兑换码、订阅计划和邀请返利功能已禁用。管理员需先确认合规声明后方可启用。")

// PaymentComplianceConfirmed reports whether the current, versioned payment
// compliance statement has been confirmed.
func PaymentComplianceConfirmed() bool {
	return setting.GetOptionBool(setting.PaymentComplianceConfirmedOption, false) &&
		setting.GetOption(setting.PaymentComplianceTermsVersionOption) == CurrentPaymentComplianceTermsVersion
}

// SubscriptionSummary wraps a subscription row for list responses.
type SubscriptionSummary struct {
	Subscription *model.UserSubscription `json:"subscription"`
}

// SubscriptionResetResult reports the outcome of an admin quota reset.
type SubscriptionResetResult struct {
	PlanId           int    `json:"plan_id"`
	MatchedCount     int    `json:"matched_count"`
	ResetCount       int    `json:"reset_count"`
	UserCount        int    `json:"user_count"`
	AdvanceResetTime bool   `json:"advance_reset_time"`
	PlanTitle        string `json:"-"`
	AffectedUserIds  []int  `json:"-"`
}

// subscriptionLockForUpdate applies SELECT ... FOR UPDATE on MySQL/PostgreSQL
// and is a no-op on SQLite, which does not support the syntax.
func subscriptionLockForUpdate(tx *gorm.DB) *gorm.DB {
	if model.UsingPostgreSQL() || model.UsingMySQL() {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

// NormalizeSubscriptionResetPeriod maps unknown reset periods to "never".
func NormalizeSubscriptionResetPeriod(period string) string {
	switch strings.TrimSpace(period) {
	case SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly, SubscriptionResetCustom:
		return strings.TrimSpace(period)
	default:
		return SubscriptionResetNever
	}
}

// NormalizeSubscriptionPlanDefaults fills nil balance-pay/overflow flags with
// their default (true) for API responses.
func NormalizeSubscriptionPlanDefaults(p *model.SubscriptionPlan) {
	if p.AllowBalancePay == nil {
		v := true
		p.AllowBalancePay = &v
	}
	if p.AllowWalletOverflow == nil {
		v := true
		p.AllowWalletOverflow = &v
	}
}

// validateSubscriptionPlanPayload validates and normalizes an admin plan
// payload in place. Shared by create and update.
func validateSubscriptionPlanPayload(p *model.SubscriptionPlan) error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("套餐标题不能为空")
	}
	price := strings.TrimSpace(p.PriceAmount)
	if price != "" {
		f, err := strconv.ParseFloat(price, 64)
		if err != nil {
			return errors.New("参数错误")
		}
		if f < 0 {
			return errors.New("价格不能为负数")
		}
		if f > 9999 {
			return errors.New("价格不能超过9999")
		}
	}
	p.Currency = "USD"
	if p.DurationUnit == "" {
		p.DurationUnit = SubscriptionDurationMonth
	}
	if p.DurationValue <= 0 && p.DurationUnit != SubscriptionDurationCustom {
		p.DurationValue = 1
	}
	if p.MaxPurchasePerUser < 0 {
		return errors.New("购买上限不能为负数")
	}
	if p.TotalAmount < 0 {
		return errors.New("总额度不能为负数")
	}
	ratios := getGroupRatios()
	p.UpgradeGroup = strings.TrimSpace(p.UpgradeGroup)
	if p.UpgradeGroup != "" {
		if _, ok := ratios[p.UpgradeGroup]; !ok {
			return errors.New("升级分组不存在")
		}
	}
	p.DowngradeGroup = strings.TrimSpace(p.DowngradeGroup)
	if p.DowngradeGroup != "" {
		if _, ok := ratios[p.DowngradeGroup]; !ok {
			return errors.New("降级分组不存在")
		}
	}
	p.QuotaResetPeriod = NormalizeSubscriptionResetPeriod(p.QuotaResetPeriod)
	if p.QuotaResetPeriod == SubscriptionResetCustom && p.QuotaResetCustomSeconds <= 0 {
		return errors.New("自定义重置周期需大于0秒")
	}
	return nil
}

// AdminListSubscriptionPlans returns every plan (enabled or not) ordered by
// sort order then id, with display defaults normalized.
func AdminListSubscriptionPlans() ([]model.SubscriptionPlan, error) {
	var plans []model.SubscriptionPlan
	if err := model.DB.Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		return nil, err
	}
	for i := range plans {
		NormalizeSubscriptionPlanDefaults(&plans[i])
	}
	return plans, nil
}

// AdminCreateSubscriptionPlan validates and persists a new plan.
func AdminCreateSubscriptionPlan(p *model.SubscriptionPlan) error {
	p.Id = 0
	if err := validateSubscriptionPlanPayload(p); err != nil {
		return err
	}
	NormalizeSubscriptionPlanDefaults(p)
	p.CreatedAt = common.NowTimestamp()
	p.UpdatedAt = p.CreatedAt
	return model.DB.Create(p).Error
}

// AdminUpdateSubscriptionPlan validates and applies a full plan update. A map
// update is used so zero values (disabled, sort order 0, ...) persist; the
// balance-pay/overflow flags are only written when the payload sets them.
func AdminUpdateSubscriptionPlan(id int, p *model.SubscriptionPlan) error {
	if err := validateSubscriptionPlanPayload(p); err != nil {
		return err
	}
	updates := map[string]any{
		"title":                      p.Title,
		"subtitle":                   p.Subtitle,
		"price_amount":               p.PriceAmount,
		"currency":                   p.Currency,
		"duration_unit":              p.DurationUnit,
		"duration_value":             p.DurationValue,
		"custom_seconds":             p.CustomSeconds,
		"enabled":                    p.Enabled,
		"sort_order":                 p.SortOrder,
		"stripe_price_id":            p.StripePriceId,
		"creem_product_id":           p.CreemProductId,
		"waffo_pancake_product_id":   p.WaffoPancakeProductId,
		"max_purchase_per_user":      p.MaxPurchasePerUser,
		"total_amount":               p.TotalAmount,
		"upgrade_group":              p.UpgradeGroup,
		"downgrade_group":            p.DowngradeGroup,
		"quota_reset_period":         p.QuotaResetPeriod,
		"quota_reset_custom_seconds": p.QuotaResetCustomSeconds,
		"updated_at":                 common.NowTimestamp(),
	}
	if p.AllowBalancePay != nil {
		updates["allow_balance_pay"] = *p.AllowBalancePay
	}
	if p.AllowWalletOverflow != nil {
		updates["allow_wallet_overflow"] = *p.AllowWalletOverflow
	}
	return model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Updates(updates).Error
}

// AdminUpdateSubscriptionPlanStatus toggles a plan's enabled flag.
func AdminUpdateSubscriptionPlanStatus(id int, enabled bool) error {
	return model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Update("enabled", enabled).Error
}

// calcPlanEndTime computes a subscription end time from a plan's duration
// using calendar-accurate arithmetic (months/years via AddDate).
func calcPlanEndTime(start time.Time, plan *model.SubscriptionPlan) (int64, error) {
	if plan.DurationValue <= 0 && plan.DurationUnit != SubscriptionDurationCustom {
		return 0, errors.New("duration_value must be > 0")
	}
	switch plan.DurationUnit {
	case SubscriptionDurationYear:
		return start.AddDate(plan.DurationValue, 0, 0).Unix(), nil
	case SubscriptionDurationMonth:
		return start.AddDate(0, plan.DurationValue, 0).Unix(), nil
	case SubscriptionDurationDay:
		return start.Add(time.Duration(plan.DurationValue) * 24 * time.Hour).Unix(), nil
	case SubscriptionDurationHour:
		return start.Add(time.Duration(plan.DurationValue) * time.Hour).Unix(), nil
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 {
			return 0, errors.New("custom_seconds must be > 0")
		}
		return start.Add(time.Duration(plan.CustomSeconds) * time.Second).Unix(), nil
	default:
		return 0, fmt.Errorf("invalid duration_unit: %s", plan.DurationUnit)
	}
}

// calcSubscriptionNextResetTime computes the next quota reset aligned to the
// calendar (next midnight / next Monday / first of next month), or base plus
// the custom interval. Returns 0 when the reset would land past the end time.
func calcSubscriptionNextResetTime(base time.Time, plan *model.SubscriptionPlan, endUnix int64) int64 {
	period := NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod)
	if period == SubscriptionResetNever {
		return 0
	}
	var next time.Time
	switch period {
	case SubscriptionResetDaily:
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, 1)
	case SubscriptionResetWeekly:
		// Align to next Monday 00:00 (Sunday=0 -> 7).
		weekday := int(base.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, 8-weekday)
	case SubscriptionResetMonthly:
		next = time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, base.Location()).
			AddDate(0, 1, 0)
	case SubscriptionResetCustom:
		if plan.QuotaResetCustomSeconds <= 0 {
			return 0
		}
		next = base.Add(time.Duration(plan.QuotaResetCustomSeconds) * time.Second)
	default:
		return 0
	}
	if endUnix > 0 && next.Unix() > endUnix {
		return 0
	}
	return next.Unix()
}

// getUserGroupByIdTx loads a user's group under a row lock.
func getUserGroupByIdTx(tx *gorm.DB, userId int) (string, error) {
	var user model.User
	if err := subscriptionLockForUpdate(tx).First(&user, userId).Error; err != nil {
		return "", err
	}
	return user.Group, nil
}

// createUserSubscriptionFromPlanTx creates a subscription instance from a
// plan inside a transaction, enforcing the per-user purchase cap and applying
// the plan's group upgrade with a snapshot of the previous group.
func createUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *model.SubscriptionPlan, source string) (*model.UserSubscription, error) {
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("invalid plan")
	}
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	if plan.MaxPurchasePerUser > 0 {
		var count int64
		if err := tx.Model(&model.UserSubscription{}).
			Where("user_id = ? AND plan_id = ?", userId, plan.Id).
			Count(&count).Error; err != nil {
			return nil, err
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return nil, errors.New("已达到该套餐购买上限")
		}
	}
	nowUnix := common.NowTimestamp()
	now := time.Unix(nowUnix, 0)
	endUnix, err := calcPlanEndTime(now, plan)
	if err != nil {
		return nil, err
	}
	nextReset := calcSubscriptionNextResetTime(now, plan, endUnix)
	lastReset := int64(0)
	if nextReset > 0 {
		lastReset = nowUnix
	}
	upgradeGroup := strings.TrimSpace(plan.UpgradeGroup)
	prevGroup := ""
	if upgradeGroup != "" {
		currentGroup, err := getUserGroupByIdTx(tx, userId)
		if err != nil {
			return nil, err
		}
		if currentGroup != upgradeGroup {
			prevGroup = currentGroup
			if err := tx.Model(&model.User{}).Where("id = ?", userId).
				Update("group", upgradeGroup).Error; err != nil {
				return nil, err
			}
		}
	}
	allowWalletOverflow := true
	if plan.AllowWalletOverflow != nil {
		allowWalletOverflow = *plan.AllowWalletOverflow
	}
	sub := &model.UserSubscription{
		UserId:              userId,
		PlanId:              plan.Id,
		AmountTotal:         plan.TotalAmount,
		AmountUsed:          0,
		StartTime:           nowUnix,
		EndTime:             endUnix,
		Status:              SubscriptionStatusActive,
		Source:              source,
		LastResetTime:       lastReset,
		NextResetTime:       nextReset,
		UpgradeGroup:        upgradeGroup,
		PrevUserGroup:       prevGroup,
		DowngradeGroup:      strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow: allowWalletOverflow,
		CreatedAt:           nowUnix,
		UpdatedAt:           nowUnix,
	}
	if err := tx.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

// AdminBindSubscription grants a plan to a user without payment. It returns a
// user-facing message when the bind changed the user's group.
func AdminBindSubscription(userId, planId int) (string, error) {
	if userId <= 0 || planId <= 0 {
		return "", errors.New("invalid userId or planId")
	}
	plan, err := GetSubscriptionPlan(planId)
	if err != nil {
		return "", err
	}
	groupChanged := false
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		// Lock the user row first so concurrent binds serialize the purchase-cap
		// check and group snapshot.
		var userRow model.User
		if err := subscriptionLockForUpdate(tx).Select("id").Where("id = ?", userId).First(&userRow).Error; err != nil {
			return err
		}
		sub, err := createUserSubscriptionFromPlanTx(tx, userId, plan, "admin")
		if err == nil {
			groupChanged = sub.PrevUserGroup != ""
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if groupChanged {
		return fmt.Sprintf("用户分组将升级到 %s", plan.UpgradeGroup), nil
	}
	return "", nil
}

// AdminListUserSubscriptions returns all of a user's subscriptions (any
// status), newest ending first.
func AdminListUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []model.UserSubscription
	if err := model.DB.Where("user_id = ?", userId).
		Order("end_time desc, id desc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	result := make([]SubscriptionSummary, 0, len(subs))
	for i := range subs {
		result = append(result, SubscriptionSummary{Subscription: &subs[i]})
	}
	return result, nil
}

// downgradeUserGroupForSubscriptionTx reverts a user's group when a
// subscription ends. An explicit downgrade group takes precedence; otherwise
// the group snapshot taken at purchase is restored, but only when the
// subscription actually elevated the user and no other active upgraded
// subscription remains.
func downgradeUserGroupForSubscriptionTx(tx *gorm.DB, sub *model.UserSubscription, now int64) (string, error) {
	downgradeGroup := strings.TrimSpace(sub.DowngradeGroup)
	upgradeGroup := strings.TrimSpace(sub.UpgradeGroup)
	if downgradeGroup == "" && upgradeGroup == "" {
		return "", nil
	}
	currentGroup, err := getUserGroupByIdTx(tx, sub.UserId)
	if err != nil {
		return "", err
	}
	var activeSub model.UserSubscription
	activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND id <> ? AND upgrade_group <> ''",
		sub.UserId, SubscriptionStatusActive, now, sub.Id).
		Order("end_time desc, id desc").
		Limit(1).
		Find(&activeSub)
	if activeQuery.Error == nil && activeQuery.RowsAffected > 0 {
		return "", nil
	}
	target := downgradeGroup
	if target == "" {
		if currentGroup != upgradeGroup {
			return "", nil
		}
		target = strings.TrimSpace(sub.PrevUserGroup)
	}
	if target == "" || target == currentGroup {
		return "", nil
	}
	if err := tx.Model(&model.User{}).Where("id = ?", sub.UserId).
		Update("group", target).Error; err != nil {
		return "", err
	}
	return target, nil
}

// AdminInvalidateUserSubscription cancels a subscription immediately and
// reverts the user's group when applicable. It returns a user-facing message
// when the group changed.
func AdminInvalidateUserSubscription(subId int) (string, error) {
	if subId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	now := common.NowTimestamp()
	downgradeGroup := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var sub model.UserSubscription
		if err := subscriptionLockForUpdate(tx).
			Where("id = ?", subId).First(&sub).Error; err != nil {
			return err
		}
		if err := tx.Model(&sub).Updates(map[string]any{
			"status":     SubscriptionStatusCancelled,
			"end_time":   now,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		downgradeGroup = target
		return nil
	})
	if err != nil {
		return "", err
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

// AdminDeleteUserSubscription hard-deletes a subscription, reverting the
// user's group when applicable.
func AdminDeleteUserSubscription(subId int) (string, error) {
	if subId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	now := common.NowTimestamp()
	downgradeGroup := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var sub model.UserSubscription
		if err := subscriptionLockForUpdate(tx).
			Where("id = ?", subId).First(&sub).Error; err != nil {
			return err
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		downgradeGroup = target
		return tx.Where("id = ?", subId).Delete(&model.UserSubscription{}).Error
	})
	if err != nil {
		return "", err
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

// resetUserSubscriptionTx zeroes a subscription's used quota; when
// advanceResetTime is set the reset schedule is recomputed from now.
func resetUserSubscriptionTx(tx *gorm.DB, sub *model.UserSubscription, plan *model.SubscriptionPlan, now int64, advanceResetTime bool) error {
	sub.AmountUsed = 0
	if advanceResetTime {
		nextReset := calcSubscriptionNextResetTime(time.Unix(now, 0), plan, sub.EndTime)
		sub.NextResetTime = nextReset
		if nextReset > 0 {
			sub.LastResetTime = now
		} else {
			sub.LastResetTime = 0
		}
	}
	sub.UpdatedAt = now
	return tx.Save(sub).Error
}

func buildSubscriptionResetResult(plan *model.SubscriptionPlan, subs []model.UserSubscription, advanceResetTime bool) *SubscriptionResetResult {
	userIds := make([]int, 0, len(subs))
	seen := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if _, ok := seen[sub.UserId]; ok {
			continue
		}
		seen[sub.UserId] = struct{}{}
		userIds = append(userIds, sub.UserId)
	}
	return &SubscriptionResetResult{
		PlanId:           plan.Id,
		MatchedCount:     len(subs),
		ResetCount:       len(subs),
		UserCount:        len(userIds),
		AdvanceResetTime: advanceResetTime,
		PlanTitle:        plan.Title,
		AffectedUserIds:  userIds,
	}
}

// AdminResetUserSubscriptionsByPlan resets one user's active subscriptions of
// a plan. It errors when the user holds no active subscription of that plan.
func AdminResetUserSubscriptionsByPlan(userId, planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if userId <= 0 || planId <= 0 {
		return nil, errors.New("invalid userId or planId")
	}
	now := common.NowTimestamp()
	var result *SubscriptionResetResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planId).Error; err != nil {
			return err
		}
		var subs []model.UserSubscription
		if err := subscriptionLockForUpdate(tx).
			Where("user_id = ? AND plan_id = ? AND status = ? AND end_time > ?", userId, planId, SubscriptionStatusActive, now).
			Order("end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return err
		}
		if len(subs) == 0 {
			return errors.New("该用户没有有效的此套餐订阅")
		}
		for i := range subs {
			if err := resetUserSubscriptionTx(tx, &subs[i], &plan, now, advanceResetTime); err != nil {
				return err
			}
		}
		result = buildSubscriptionResetResult(&plan, subs, advanceResetTime)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// AdminResetPlanSubscriptions resets every active subscription of a plan. An
// empty match is not an error; the result reports zero counts.
func AdminResetPlanSubscriptions(planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if planId <= 0 {
		return nil, errors.New("invalid planId")
	}
	now := common.NowTimestamp()
	var result *SubscriptionResetResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planId).Error; err != nil {
			return err
		}
		var subs []model.UserSubscription
		if err := subscriptionLockForUpdate(tx).
			Where("plan_id = ? AND status = ? AND end_time > ?", planId, SubscriptionStatusActive, now).
			Order("user_id asc, end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return err
		}
		for i := range subs {
			if err := resetUserSubscriptionTx(tx, &subs[i], &plan, now, advanceResetTime); err != nil {
				return err
			}
		}
		result = buildSubscriptionResetResult(&plan, subs, advanceResetTime)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
