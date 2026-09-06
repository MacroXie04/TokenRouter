package billing

import (
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"math"
	"strings"
	"time"
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

// ErrSubscriptionStateChanged reports a compare-and-swap conflict while
// resetting a subscription. The caller can safely retry from a fresh row;
// the concurrent usage or lifecycle transition is never overwritten.
var ErrSubscriptionStateChanged = errors.New("订阅状态已变化，请重试")

var ErrSubscriptionEntitlementSnapshotMissing = errors.New("旧订阅缺少不可变权益快照")

var ErrSubscriptionHasAccountingReferences = errors.New("订阅存在未完成的计费预留，请先取消订阅并完成对账")

// ErrSubscriptionTargetForbidden prevents an administrator from using a
// subscription route to inspect or mutate an account at the same or a higher
// role. Target checks are repeated under the same transaction/row lock as the
// mutation so a concurrent role change cannot bypass the controller policy.
var ErrSubscriptionTargetForbidden = errors.New("无权操作同级或更高级用户")

const UserSubscriptionEntitlementVersion = 1

const (
	SubscriptionEntitlementMigrationPending    = "pending"
	SubscriptionEntitlementMigrationBackfilled = "backfilled"
	SubscriptionEntitlementMigrationReview     = "review"
	SubscriptionEntitlementMigrationResolved   = "resolved"
)

// LegacySubscriptionEntitlementResolution is the complete immutable cadence
// snapshot a root operator must attest when an orphaned legacy subscription
// can no longer be backfilled from its deleted plan.
type LegacySubscriptionEntitlementResolution struct {
	ExpectedPlanID          int    `json:"expected_plan_id"`
	ExpectedUsageEpoch      int64  `json:"expected_usage_epoch"`
	QuotaResetPeriod        string `json:"quota_reset_period"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds"`
	GroupBaseline           string `json:"group_baseline"`
	Reason                  string `json:"reason"`
}

// PaymentComplianceConfirmed reports whether the current, versioned payment
// compliance statement has been confirmed.
func PaymentComplianceConfirmed() bool {
	values := setting.GetOptions(
		setting.PaymentComplianceConfirmedOption,
		setting.PaymentComplianceTermsVersionOption,
	)
	confirmed := values[setting.PaymentComplianceConfirmedOption]
	return (confirmed == "true" || confirmed == "1" || confirmed == "yes") &&
		values[setting.PaymentComplianceTermsVersionOption] == CurrentPaymentComplianceTermsVersion
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

func authorizeSubscriptionTargetTx(tx *gorm.DB, userID int, operatorRole *int) error {
	if tx == nil || userID <= 0 {
		return errors.New("invalid subscription target")
	}
	var user model.User
	if err := locking.SubscriptionLockForUpdate(tx).Select("id", "role").Where("id = ?", userID).First(&user).Error; err != nil {
		return err
	}
	if operatorRole != nil && user.Role >= *operatorRole {
		return ErrSubscriptionTargetForbidden
	}
	return nil
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
	if p == nil {
		return
	}
	if p.AllowBalancePay == nil {
		v := true
		p.AllowBalancePay = &v
	}
	if p.AllowWalletOverflow == nil {
		v := true
		p.AllowWalletOverflow = &v
	}
}

// validateSubscriptionPlanFinancialPayload enforces the finite, persisted
// numeric domain shared by both plan-creation APIs. TotalAmount is later
// copied into quota counters backed by machine-sized integers, so accepting a
// larger bigint here would make every fulfillment of the plan unsafe.
func validateSubscriptionPlanFinancialPayload(p *model.SubscriptionPlan) error {
	if p == nil {
		return errors.New("参数错误")
	}
	price, err := parseSubscriptionPlanPriceDecimal(p.PriceAmount)
	if err != nil {
		return errors.New("参数错误")
	}
	if price.IsNegative() {
		return errors.New("价格不能为负数")
	}
	if price.GreaterThan(decimal.NewFromInt(9999)) {
		return errors.New("价格不能超过9999")
	}
	if !subscriptionPlanPriceFitsDatabase(price) {
		return errors.New("参数错误")
	}
	// Keep the precision-safe JSON string API while persisting one canonical
	// value that every supported DECIMAL(10,6) driver accepts identically.
	p.PriceAmount = price.StringFixed(6)
	if p.TotalAmount < 0 {
		return errors.New("总额度不能为负数")
	}
	if p.TotalAmount > quotamath.MaxQuota {
		return fmt.Errorf("总额度不能超过%d", quotamath.MaxQuota)
	}
	return nil
}

// validateSubscriptionPlanPayload validates and normalizes an admin plan
// payload in place. Shared by create and update.
func validateSubscriptionPlanPayload(p *model.SubscriptionPlan) error {
	if p == nil {
		return errors.New("参数错误")
	}
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("套餐标题不能为空")
	}
	if err := validateSubscriptionPlanFinancialPayload(p); err != nil {
		return err
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
	if _, err := calcPlanEndTime(time.Unix(wallclock.NowTimestamp(), 0), p); err != nil {
		return errors.New("订阅时长配置无效")
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
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		p.CreatedAt = now
		p.UpdatedAt = now
		return tx.Create(p).Error
	})
}

// AdminUpdateSubscriptionPlan validates and applies a full plan update. A map
// update is used so zero values (disabled, sort order 0, ...) persist; the
// balance-pay/overflow flags are only written when the payload sets them.
func AdminUpdateSubscriptionPlan(id int, p *model.SubscriptionPlan) error {
	if err := validateSubscriptionPlanPayload(p); err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
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
			"updated_at":                 now,
		}
		if p.AllowBalancePay != nil {
			updates["allow_balance_pay"] = *p.AllowBalancePay
		}
		if p.AllowWalletOverflow != nil {
			updates["allow_wallet_overflow"] = *p.AllowWalletOverflow
		}
		return tx.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Updates(updates).Error
	})
}

// AdminUpdateSubscriptionPlanStatus toggles a plan's enabled flag.
func AdminUpdateSubscriptionPlanStatus(id int, enabled bool) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		return tx.Model(&model.SubscriptionPlan{}).Where("id = ?", id).
			Updates(map[string]any{"enabled": enabled, "updated_at": now}).Error
	})
}

// calcPlanEndTime computes a subscription end time from a plan's duration
// using calendar-accurate arithmetic (months/years via AddDate).
func calcPlanEndTime(start time.Time, plan *model.SubscriptionPlan) (int64, error) {
	if plan == nil {
		return 0, errors.New("plan is nil")
	}
	start = start.UTC()
	if plan.DurationValue <= 0 && plan.DurationUnit != SubscriptionDurationCustom {
		return 0, errors.New("duration_value must be > 0")
	}
	checkedFixedEnd := func(value, secondsPerUnit int64) (int64, error) {
		if value <= 0 || secondsPerUnit <= 0 || value > math.MaxInt64/secondsPerUnit {
			return 0, errors.New("subscription duration overflows")
		}
		delta := value * secondsPerUnit
		startUnix := start.Unix()
		if startUnix > math.MaxInt64-delta {
			return 0, errors.New("subscription end time overflows")
		}
		end := startUnix + delta
		if end <= startUnix {
			return 0, errors.New("subscription end time must be after start")
		}
		return end, nil
	}
	checkedCalendarEnd := func(years, months int) (int64, error) {
		startYear := start.Year()
		if years > 0 && startYear > math.MaxInt-years {
			return 0, errors.New("subscription calendar duration overflows")
		}
		// Unix seconds cannot represent calendar years beyond this range.
		// Reject before AddDate so enormous persisted values cannot wrap inside
		// calendar normalization.
		const maxUnixCalendarYear int64 = 292277026596
		targetYear := int64(startYear) + int64(years)
		if months > 0 && start.Month()+time.Month(months) > 12 {
			targetYear++
		}
		if targetYear > maxUnixCalendarYear {
			return 0, errors.New("subscription calendar duration overflows")
		}
		endTime := start.AddDate(years, months, 0)
		endUnix := endTime.Unix()
		if !endTime.After(start) || endUnix <= start.Unix() {
			return 0, errors.New("subscription end time must be after start")
		}
		return endUnix, nil
	}
	switch plan.DurationUnit {
	case SubscriptionDurationYear:
		return checkedCalendarEnd(plan.DurationValue, 0)
	case SubscriptionDurationMonth:
		years := plan.DurationValue / 12
		months := plan.DurationValue % 12
		return checkedCalendarEnd(years, months)
	case SubscriptionDurationDay:
		return checkedFixedEnd(int64(plan.DurationValue), 24*60*60)
	case SubscriptionDurationHour:
		return checkedFixedEnd(int64(plan.DurationValue), 60*60)
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 {
			return 0, errors.New("custom_seconds must be > 0")
		}
		return checkedFixedEnd(plan.CustomSeconds, 1)
	default:
		return 0, fmt.Errorf("invalid duration_unit: %s", plan.DurationUnit)
	}
}

// calcSubscriptionNextResetTime computes the next quota reset aligned to the
// calendar (next midnight / next Monday / first of next month), or base plus
// the custom interval. Returns 0 when the reset would land past the end time.
func calcSubscriptionNextResetTime(base time.Time, plan *model.SubscriptionPlan, endUnix int64) int64 {
	base = base.UTC()
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
		baseUnix := base.Unix()
		if baseUnix > math.MaxInt64-plan.QuotaResetCustomSeconds {
			return 0
		}
		next = time.Unix(baseUnix+plan.QuotaResetCustomSeconds, 0).UTC()
	default:
		return 0
	}
	if next.Unix() <= base.Unix() || (endUnix > 0 && next.Unix() > endUnix) {
		return 0
	}
	return next.Unix()
}

// advanceSubscriptionResetSchedule advances a due reset in constant time.
// Calendar periods are canonical UTC boundaries on every node; custom periods
// remain aligned to the immutable start/last-reset anchor. This avoids a loop
// proportional to downtime for very old subscriptions.
func advanceSubscriptionResetSchedule(baseUnix, now, endUnix int64, plan *model.SubscriptionPlan) (last, next int64, advanced bool) {
	if baseUnix <= 0 || now <= 0 || plan == nil {
		return baseUnix, 0, false
	}
	base := time.Unix(baseUnix, 0).UTC()
	first := calcSubscriptionNextResetTime(base, plan, endUnix)
	if first <= 0 || first > now {
		return baseUnix, first, false
	}
	horizon := now
	if endUnix > 0 && endUnix < horizon {
		horizon = endUnix
	}
	horizonTime := time.Unix(horizon, 0).UTC()
	period := NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod)
	var latest int64
	switch period {
	case SubscriptionResetDaily:
		latest = time.Date(horizonTime.Year(), horizonTime.Month(), horizonTime.Day(), 0, 0, 0, 0, time.UTC).Unix()
	case SubscriptionResetWeekly:
		weekday := int(horizonTime.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		latest = time.Date(horizonTime.Year(), horizonTime.Month(), horizonTime.Day(), 0, 0, 0, 0, time.UTC).
			AddDate(0, 0, 1-weekday).Unix()
	case SubscriptionResetMonthly:
		latest = time.Date(horizonTime.Year(), horizonTime.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	case SubscriptionResetCustom:
		interval := plan.QuotaResetCustomSeconds
		if interval <= 0 || horizon <= baseUnix {
			return baseUnix, first, false
		}
		steps := (horizon - baseUnix) / interval
		if steps <= 0 || steps > (math.MaxInt64-baseUnix)/interval {
			return baseUnix, first, false
		}
		latest = baseUnix + steps*interval
	default:
		return baseUnix, 0, false
	}
	if latest < first || latest <= baseUnix || latest > horizon {
		latest = first
	}
	next = calcSubscriptionNextResetTime(time.Unix(latest, 0).UTC(), plan, endUnix)
	return latest, next, true
}

// getUserGroupByIdTx loads a user's group under a row lock.
func getUserGroupByIdTx(tx *gorm.DB, userId int) (string, error) {
	var user model.User
	if err := locking.SubscriptionLockForUpdate(tx).First(&user, userId).Error; err != nil {
		return "", err
	}
	return user.Group, nil
}

// createUserSubscriptionFromPlanTx creates a subscription instance from a
// plan inside a transaction, enforcing the per-user purchase cap and applying
// the plan's group upgrade with a snapshot of the previous group.
func createUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *model.SubscriptionPlan, source string) (*model.UserSubscription, error) {
	return createUserSubscriptionFromPlanTxWithCapacity(tx, userId, plan, source, true)
}

// createUserSubscriptionFromPlanTxWithCapacity optionally skips a fresh cap
// check when a Stripe order is consuming a slot atomically reserved at order
// creation. Every non-reserved purchase path uses enforceCapacity=true.
func createUserSubscriptionFromPlanTxWithCapacity(tx *gorm.DB, userId int, plan *model.SubscriptionPlan, source string, enforceCapacity bool) (*model.UserSubscription, error) {
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("invalid plan")
	}
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	if _, ok := boundedSubscriptionQuota(plan.TotalAmount); !ok {
		return nil, ErrSubscriptionQuotaOverflow
	}
	if plan.MaxPurchasePerUser < 0 {
		return nil, errors.New("购买上限不能为负数")
	}
	if enforceCapacity && plan.MaxPurchasePerUser > 0 {
		used, err := countSubscriptionCapacityUsedTx(tx, userId, plan.Id)
		if err != nil {
			return nil, err
		}
		if used >= int64(plan.MaxPurchasePerUser) {
			return nil, ErrSubscriptionPurchaseLimit
		}
	}
	nowUnix, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return nil, err
	}
	now := time.Unix(nowUnix, 0).UTC()
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
	groupBaseline := ""
	if upgradeGroup != "" {
		if _, _, err := expireDueSubscriptionsForUserTx(tx, userId, nowUnix); err != nil {
			return nil, err
		}
		currentGroup, err := getUserGroupByIdTx(tx, userId)
		if err != nil {
			return nil, err
		}
		groupBaseline = currentGroup
		var prior model.UserSubscription
		priorQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
			userId, SubscriptionStatusActive, nowUnix).
			Order("start_time asc, id asc").Limit(1).Find(&prior)
		if priorQuery.Error != nil {
			return nil, priorQuery.Error
		}
		if priorQuery.RowsAffected == 1 {
			if strings.TrimSpace(prior.GroupBaseline) != "" {
				groupBaseline = strings.TrimSpace(prior.GroupBaseline)
			} else if strings.TrimSpace(prior.PrevUserGroup) != "" {
				groupBaseline = strings.TrimSpace(prior.PrevUserGroup)
			}
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
		UserId:                          userId,
		PlanId:                          plan.Id,
		AmountTotal:                     plan.TotalAmount,
		AmountUsed:                      0,
		UsageEpoch:                      0,
		StartTime:                       nowUnix,
		EndTime:                         endUnix,
		Status:                          SubscriptionStatusActive,
		Source:                          source,
		LastResetTime:                   lastReset,
		NextResetTime:                   nextReset,
		UpgradeGroup:                    upgradeGroup,
		PrevUserGroup:                   prevGroup,
		DowngradeGroup:                  strings.TrimSpace(plan.DowngradeGroup),
		GroupBaseline:                   groupBaseline,
		EntitlementVersion:              UserSubscriptionEntitlementVersion,
		EntitlementMigrationState:       SubscriptionEntitlementMigrationBackfilled,
		QuotaResetPeriodSnapshot:        NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod),
		QuotaResetCustomSecondsSnapshot: plan.QuotaResetCustomSeconds,
		AllowWalletOverflow:             allowWalletOverflow,
		CreatedAt:                       nowUnix,
		UpdatedAt:                       nowUnix,
	}
	if err := tx.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

// countSubscriptionCapacityUsedTx counts fulfilled purchases plus live
// pending reservations. This is deliberately lifetime-based, matching the
// existing MaxPurchasePerUser semantics for UserSubscription rows.
func countSubscriptionCapacityUsedTx(tx *gorm.DB, userId, planId int) (int64, error) {
	var fulfilled int64
	if err := tx.Model(&model.UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&fulfilled).Error; err != nil {
		return 0, err
	}
	var reserved int64
	if err := tx.Model(&model.SubscriptionOrder{}).
		Where("user_id = ? AND plan_id = ? AND status = ? AND capacity_reserved = ?",
			userId, planId, TopUpStatusPending, true).
		Count(&reserved).Error; err != nil {
		return 0, err
	}
	if fulfilled > math.MaxInt64-reserved {
		return 0, errors.New("订阅购买计数超出安全范围")
	}
	return fulfilled + reserved, nil
}

// AdminBindSubscription grants a plan to a user without payment. It returns a
// user-facing message when the bind changed the user's group.
func AdminBindSubscription(userId, planId int) (string, error) {
	return adminBindSubscription(userId, planId, nil)
}

// AdminBindSubscriptionAuthorized is the dashboard-admin boundary for a
// no-payment grant. Internal billing tests and trusted jobs may continue to
// use AdminBindSubscription without an operator role.
func AdminBindSubscriptionAuthorized(userId, planId, operatorRole int) (string, error) {
	return adminBindSubscription(userId, planId, &operatorRole)
}

func adminBindSubscription(userId, planId int, operatorRole *int) (string, error) {
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
		// check and group snapshot. The authorization decision shares this lock.
		if err := authorizeSubscriptionTargetTx(tx, userId, operatorRole); err != nil {
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
	return adminListUserSubscriptions(userId, nil)
}

// AdminListUserSubscriptionsAuthorized keeps the role check and list read in
// one database snapshot for the dashboard administration surface.
func AdminListUserSubscriptionsAuthorized(userId, operatorRole int) ([]SubscriptionSummary, error) {
	return adminListUserSubscriptions(userId, &operatorRole)
}

func adminListUserSubscriptions(userId int, operatorRole *int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []model.UserSubscription
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := authorizeSubscriptionTargetTx(tx, userId, operatorRole); err != nil {
			return err
		}
		return tx.Where("user_id = ?", userId).
			Order("end_time desc, id desc").
			Find(&subs).Error
	}); err != nil {
		return nil, err
	}
	result := make([]SubscriptionSummary, 0, len(subs))
	for i := range subs {
		result = append(result, SubscriptionSummary{Subscription: &subs[i]})
	}
	return result, nil
}

// downgradeUserGroupForSubscriptionTx recomputes the effective group after a
// subscription leaves the active set. The newest remaining group entitlement
// wins; after the last one ends, its immutable shared baseline (or a legacy
// previous-group snapshot) is restored.
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
		Order("start_time desc, id desc").
		Limit(1).
		Find(&activeSub)
	if activeQuery.Error != nil {
		return "", activeQuery.Error
	}
	target := ""
	if activeQuery.RowsAffected > 0 {
		target = strings.TrimSpace(activeSub.UpgradeGroup)
	} else if downgradeGroup != "" {
		target = downgradeGroup
	} else {
		if currentGroup != upgradeGroup {
			return "", nil
		}
		target = strings.TrimSpace(sub.GroupBaseline)
		if target == "" {
			target = strings.TrimSpace(sub.PrevUserGroup)
		}
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

// expireDueSubscriptionsForUserTx marks every currently due subscription for
// one locked user before deriving the final group. Expiring a chain as a set
// prevents an intermediate member's downgrade snapshot from winning merely
// because it happened to be processed first.
func expireDueSubscriptionsForUserTx(tx *gorm.DB, userId int, now int64) (int, string, error) {
	if tx == nil || userId <= 0 || now <= 0 {
		return 0, "", errors.New("invalid subscription expiry args")
	}
	var due []model.UserSubscription
	if err := locking.SubscriptionLockForUpdate(tx).
		Where("user_id = ? AND status = ? AND end_time <= ?", userId, SubscriptionStatusActive, now).
		Order("end_time asc, start_time asc, id asc").Find(&due).Error; err != nil {
		return 0, "", err
	}
	if len(due) == 0 {
		return 0, "", nil
	}
	for i := range due {
		transition := tx.Model(&model.UserSubscription{}).
			Where("id = ? AND user_id = ? AND status = ? AND end_time = ?",
				due[i].Id, userId, SubscriptionStatusActive, due[i].EndTime).
			Updates(map[string]any{"status": SubscriptionStatusExpired, "updated_at": now})
		if transition.Error != nil {
			return 0, "", transition.Error
		}
		if transition.RowsAffected != 1 {
			return 0, "", ErrSubscriptionStateChanged
		}
	}

	currentGroup, err := getUserGroupByIdTx(tx, userId)
	if err != nil {
		return 0, "", err
	}
	var remaining model.UserSubscription
	remainingQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
		userId, SubscriptionStatusActive, now).
		Order("start_time desc, id desc").Limit(1).Find(&remaining)
	if remainingQuery.Error != nil {
		return 0, "", remainingQuery.Error
	}
	target := ""
	if remainingQuery.RowsAffected == 1 {
		target = strings.TrimSpace(remaining.UpgradeGroup)
	} else {
		// A plan's explicit downgrade takes precedence; the latest-ending
		// entitlement is the one that had authority immediately before expiry.
		for i := len(due) - 1; i >= 0; i-- {
			if candidate := strings.TrimSpace(due[i].DowngradeGroup); candidate != "" {
				target = candidate
				break
			}
		}
		if target == "" {
			// GroupBaseline is shared across a stacked upgrade chain. Prefer the
			// earliest immutable baseline, then its legacy previous-group snapshot.
			for i := range due {
				if candidate := strings.TrimSpace(due[i].GroupBaseline); candidate != "" {
					target = candidate
					break
				}
			}
			if target == "" {
				for i := range due {
					if candidate := strings.TrimSpace(due[i].PrevUserGroup); candidate != "" {
						target = candidate
						break
					}
				}
			}
		}
	}
	if target == "" || target == currentGroup {
		return len(due), "", nil
	}
	result := tx.Model(&model.User{}).Where(map[string]any{"id": userId, "group": currentGroup}).Update("group", target)
	if result.Error != nil {
		return 0, "", result.Error
	}
	if result.RowsAffected != 1 {
		return 0, "", ErrSubscriptionStateChanged
	}
	return len(due), target, nil
}

// AdminInvalidateUserSubscription cancels a subscription immediately and
// reverts the user's group when applicable. It returns a user-facing message
// when the group changed.
func AdminInvalidateUserSubscription(subId int) (string, error) {
	return adminInvalidateUserSubscription(subId, nil)
}

func AdminInvalidateUserSubscriptionAuthorized(subId, operatorRole int) (string, error) {
	return adminInvalidateUserSubscription(subId, &operatorRole)
}

func adminInvalidateUserSubscription(subId int, operatorRole *int) (string, error) {
	if subId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	downgradeGroup := ""
	var identity model.UserSubscription
	if err := model.DB.Select("id", "user_id").Where("id = ?", subId).First(&identity).Error; err != nil {
		return "", err
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if err := authorizeSubscriptionTargetTx(tx, identity.UserId, operatorRole); err != nil {
			return err
		}
		var sub model.UserSubscription
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("id = ?", subId).First(&sub).Error; err != nil {
			return err
		}
		if sub.UserId != identity.UserId {
			return ErrSubscriptionStateChanged
		}
		if sub.Status != SubscriptionStatusActive {
			return nil
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
	return adminDeleteUserSubscription(subId, nil)
}

func AdminDeleteUserSubscriptionAuthorized(subId, operatorRole int) (string, error) {
	return adminDeleteUserSubscription(subId, &operatorRole)
}

func adminDeleteUserSubscription(subId int, operatorRole *int) (string, error) {
	if subId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	downgradeGroup := ""
	var identity model.UserSubscription
	if err := model.DB.Select("id", "user_id").Where("id = ?", subId).First(&identity).Error; err != nil {
		return "", err
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if err := authorizeSubscriptionTargetTx(tx, identity.UserId, operatorRole); err != nil {
			return err
		}
		var sub model.UserSubscription
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("id = ?", subId).First(&sub).Error; err != nil {
			return err
		}
		if sub.UserId != identity.UserId {
			return ErrSubscriptionStateChanged
		}
		var preConsumeCount int64
		if err := tx.Model(&model.SubscriptionPreConsumeRecord{}).
			Where("user_subscription_id = ? AND status = ?", sub.Id, SubscriptionPreConsumeStatusConsumed).
			Count(&preConsumeCount).Error; err != nil {
			return err
		}
		var durableCount int64
		if err := tx.Model(&model.RelayQuotaReservationRecord{}).
			Where("subscription_id = ? AND status IN ?", sub.Id, []string{
				model.RelayQuotaReservationStatusHeld,
				model.RelayQuotaReservationStatusDispatched,
				model.RelayQuotaReservationStatusPendingSettlement,
				model.RelayQuotaReservationStatusPendingRefund,
				model.RelayQuotaReservationStatusManualReview,
			}).Count(&durableCount).Error; err != nil {
			return err
		}
		if preConsumeCount > 0 || durableCount > 0 {
			return ErrSubscriptionHasAccountingReferences
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

// ResolveLegacySubscriptionEntitlementReview replaces an orphaned legacy
// cadence only from an explicit root-attested snapshot. It never consults the
// mutable/deleted plan row. The exact state transition and audit event share a
// transaction, and a retry with the identical request is idempotent.
func ResolveLegacySubscriptionEntitlementReview(operatorUserID, subscriptionID int, resolution LegacySubscriptionEntitlementResolution) error {
	resolution.QuotaResetPeriod = strings.TrimSpace(resolution.QuotaResetPeriod)
	resolution.GroupBaseline = strings.TrimSpace(resolution.GroupBaseline)
	resolution.Reason = strings.TrimSpace(resolution.Reason)
	if operatorUserID <= 0 || subscriptionID <= 0 || resolution.ExpectedPlanID <= 0 || resolution.ExpectedUsageEpoch < 0 ||
		len(resolution.Reason) < 8 || len(resolution.Reason) > 500 || len(resolution.GroupBaseline) > 64 {
		return errors.New("invalid legacy subscription resolution")
	}
	switch resolution.QuotaResetPeriod {
	case SubscriptionResetNever, SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly:
		if resolution.QuotaResetCustomSeconds != 0 {
			return errors.New("non-custom reset snapshot cannot carry custom seconds")
		}
	case SubscriptionResetCustom:
		if resolution.QuotaResetCustomSeconds <= 0 {
			return errors.New("custom reset snapshot requires positive seconds")
		}
	default:
		return errors.New("invalid reset-period snapshot")
	}
	if resolution.GroupBaseline != "" {
		if _, ok := getGroupRatios()[resolution.GroupBaseline]; !ok {
			return errors.New("invalid group baseline snapshot")
		}
	}

	var identity model.UserSubscription
	if err := model.DB.Select("id", "user_id").Where("id = ?", subscriptionID).First(&identity).Error; err != nil {
		return err
	}
	auditEventID := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var targetUser model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id").Where("id = ?", identity.UserId).First(&targetUser).Error; err != nil {
			return err
		}
		var operator model.User
		if err := tx.Select("id", "role").Where("id = ?", operatorUserID).First(&operator).Error; err != nil {
			return err
		}
		if !userssvc.IsRoot(operator.Role) {
			return errors.New("root operator required")
		}
		var sub model.UserSubscription
		if err := locking.SubscriptionLockForUpdate(tx).Where("id = ?", subscriptionID).First(&sub).Error; err != nil {
			return err
		}
		if sub.UserId != identity.UserId || sub.PlanId != resolution.ExpectedPlanID ||
			sub.UsageEpoch != resolution.ExpectedUsageEpoch {
			return ErrSubscriptionStateChanged
		}
		if strings.TrimSpace(sub.UpgradeGroup) != "" && resolution.GroupBaseline == "" {
			return errors.New("upgraded subscription requires a group baseline snapshot")
		}
		alreadyResolved := sub.EntitlementVersion == UserSubscriptionEntitlementVersion &&
			sub.EntitlementMigrationState == SubscriptionEntitlementMigrationResolved &&
			sub.QuotaResetPeriodSnapshot == resolution.QuotaResetPeriod &&
			sub.QuotaResetCustomSecondsSnapshot == resolution.QuotaResetCustomSeconds &&
			sub.GroupBaseline == resolution.GroupBaseline
		if !alreadyResolved {
			if sub.EntitlementVersion != 0 || sub.EntitlementMigrationState != SubscriptionEntitlementMigrationReview {
				return ErrSubscriptionEntitlementSnapshotMissing
			}
			result := tx.Model(&model.UserSubscription{}).
				Where(`id = ? AND user_id = ? AND plan_id = ? AND usage_epoch = ? AND entitlement_version = ?
					AND entitlement_migration_state = ? AND updated_at = ?`,
					sub.Id, sub.UserId, sub.PlanId, sub.UsageEpoch, 0, SubscriptionEntitlementMigrationReview, sub.UpdatedAt).
				Updates(map[string]any{
					"entitlement_version":                 UserSubscriptionEntitlementVersion,
					"entitlement_migration_state":         SubscriptionEntitlementMigrationResolved,
					"quota_reset_period_snapshot":         resolution.QuotaResetPeriod,
					"quota_reset_custom_seconds_snapshot": resolution.QuotaResetCustomSeconds,
					"group_baseline":                      resolution.GroupBaseline,
					"updated_at":                          now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrSubscriptionStateChanged
			}
			sub.UpdatedAt = now
		}
		content := fmt.Sprintf("root operator %d resolved legacy subscription %d entitlement: plan=%d epoch=%d reset=%s custom_seconds=%d group_baseline=%q reason=%q",
			operatorUserID, sub.Id, resolution.ExpectedPlanID, resolution.ExpectedUsageEpoch,
			resolution.QuotaResetPeriod, resolution.QuotaResetCustomSeconds, resolution.GroupBaseline, resolution.Reason)
		auditEventID, err = enqueuePaymentSystemLogTx(tx, "subscription_entitlement_resolution",
			fmt.Sprintf("%d:%d", sub.Id, resolution.ExpectedUsageEpoch), sub.UserId, content, sub.UpdatedAt)
		return err
	})
	if err != nil {
		return err
	}
	if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
		logging.SysError(fmt.Sprintf("legacy subscription resolution audit delivery deferred subscription_id=%d: %v", subscriptionID, err))
	}
	return nil
}

// updateSubscriptionResetState changes only quota-reset fields and binds the
// write to the snapshot used to calculate them. This prevents an admin or
// periodic reset from erasing quota consumed after that snapshot was read.
func updateSubscriptionResetState(tx *gorm.DB, sub *model.UserSubscription, amountUsed, lastResetTime, nextResetTime, now int64, incrementUsageEpoch bool) error {
	if tx == nil || sub == nil || sub.Id <= 0 {
		return errors.New("invalid reset args")
	}
	if sub.Status != SubscriptionStatusActive {
		return ErrSubscriptionStateChanged
	}
	if amountUsed < 0 || amountUsed > quotamath.MaxQuota {
		return ErrSubscriptionQuotaOverflow
	}
	if sub.UsageEpoch < 0 || (incrementUsageEpoch && sub.UsageEpoch == math.MaxInt64) {
		return ErrSubscriptionQuotaOverflow
	}
	snapshot := tx.Model(&model.UserSubscription{}).
		Where(`id = ? AND user_id = ? AND plan_id = ? AND status = ? AND end_time = ?
			AND COALESCE(amount_used, 0) = ? AND COALESCE(last_reset_time, 0) = ?
			AND COALESCE(next_reset_time, 0) = ? AND COALESCE(usage_epoch, 0) = ?
			AND COALESCE(entitlement_version, 0) = ?`,
			sub.Id, sub.UserId, sub.PlanId, SubscriptionStatusActive, sub.EndTime,
			sub.AmountUsed, sub.LastResetTime, sub.NextResetTime, sub.UsageEpoch, sub.EntitlementVersion)
	if amountUsed == sub.AmountUsed && lastResetTime == sub.LastResetTime && nextResetTime == sub.NextResetTime && !incrementUsageEpoch {
		var count int64
		if err := snapshot.Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrSubscriptionStateChanged
		}
		return nil
	}
	updates := map[string]any{"updated_at": now}
	if amountUsed != sub.AmountUsed {
		updates["amount_used"] = amountUsed
	}
	if lastResetTime != sub.LastResetTime {
		updates["last_reset_time"] = lastResetTime
	}
	if nextResetTime != sub.NextResetTime {
		updates["next_reset_time"] = nextResetTime
	}
	if incrementUsageEpoch {
		updates["usage_epoch"] = sub.UsageEpoch + 1
	}
	result := snapshot.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSubscriptionStateChanged
	}
	sub.AmountUsed = amountUsed
	sub.LastResetTime = lastResetTime
	sub.NextResetTime = nextResetTime
	if incrementUsageEpoch {
		sub.UsageEpoch++
	}
	sub.UpdatedAt = now
	return nil
}

// subscriptionResetPlanTx returns only the immutable cadence needed by reset
// arithmetic. Legacy rows are backfilled exactly once from the current plan;
// if that source no longer exists, reset fails closed instead of inventing a
// cadence.
func subscriptionResetPlanTx(tx *gorm.DB, sub *model.UserSubscription) (*model.SubscriptionPlan, error) {
	if tx == nil || sub == nil || sub.Id <= 0 {
		return nil, errors.New("invalid subscription snapshot args")
	}
	if sub.EntitlementVersion >= UserSubscriptionEntitlementVersion {
		period := strings.TrimSpace(sub.QuotaResetPeriodSnapshot)
		switch period {
		case SubscriptionResetNever, SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly:
		case SubscriptionResetCustom:
			if sub.QuotaResetCustomSecondsSnapshot <= 0 {
				return nil, ErrSubscriptionEntitlementSnapshotMissing
			}
		default:
			return nil, ErrSubscriptionEntitlementSnapshotMissing
		}
		return &model.SubscriptionPlan{
			QuotaResetPeriod:        period,
			QuotaResetCustomSeconds: sub.QuotaResetCustomSecondsSnapshot,
		}, nil
	}
	if sub.EntitlementMigrationState == SubscriptionEntitlementMigrationReview {
		return nil, ErrSubscriptionEntitlementSnapshotMissing
	}

	var plan model.SubscriptionPlan
	if err := tx.Select("id", "quota_reset_period", "quota_reset_custom_seconds").
		Where("id = ?", sub.PlanId).First(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSubscriptionEntitlementSnapshotMissing
		}
		return nil, err
	}
	period := NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod)
	if period == SubscriptionResetCustom && plan.QuotaResetCustomSeconds <= 0 {
		return nil, ErrSubscriptionEntitlementSnapshotMissing
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return nil, err
	}
	groupBaseline := strings.TrimSpace(sub.GroupBaseline)
	if groupBaseline == "" && strings.TrimSpace(sub.UpgradeGroup) != "" {
		var earliest model.UserSubscription
		lookup := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
			sub.UserId, SubscriptionStatusActive, now).
			Order("start_time asc, id asc").Limit(1).Find(&earliest)
		if lookup.Error != nil {
			return nil, lookup.Error
		}
		if lookup.RowsAffected == 1 {
			groupBaseline = strings.TrimSpace(earliest.GroupBaseline)
			if groupBaseline == "" {
				groupBaseline = strings.TrimSpace(earliest.PrevUserGroup)
			}
		}
		if groupBaseline == "" {
			groupBaseline = strings.TrimSpace(sub.PrevUserGroup)
		}
	}
	result := tx.Model(&model.UserSubscription{}).
		Where("id = ? AND entitlement_version = ? AND entitlement_migration_state IN ?",
			sub.Id, sub.EntitlementVersion, []string{"", SubscriptionEntitlementMigrationPending}).
		Updates(map[string]any{
			"entitlement_version":                 UserSubscriptionEntitlementVersion,
			"entitlement_migration_state":         SubscriptionEntitlementMigrationBackfilled,
			"quota_reset_period_snapshot":         period,
			"quota_reset_custom_seconds_snapshot": plan.QuotaResetCustomSeconds,
			"group_baseline":                      groupBaseline,
			"updated_at":                          now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrSubscriptionStateChanged
	}
	sub.EntitlementVersion = UserSubscriptionEntitlementVersion
	sub.EntitlementMigrationState = SubscriptionEntitlementMigrationBackfilled
	sub.QuotaResetPeriodSnapshot = period
	sub.QuotaResetCustomSecondsSnapshot = plan.QuotaResetCustomSeconds
	sub.GroupBaseline = groupBaseline
	sub.UpdatedAt = now
	plan.QuotaResetPeriod = period
	return &plan, nil
}

// resetUserSubscriptionTx zeroes a subscription's used quota; when
// advanceResetTime is set the reset schedule is recomputed from now.
func resetUserSubscriptionTx(tx *gorm.DB, sub *model.UserSubscription, plan *model.SubscriptionPlan, now int64, advanceResetTime bool) error {
	if tx == nil || sub == nil {
		return errors.New("invalid reset args")
	}
	resetPlan, err := subscriptionResetPlanTx(tx, sub)
	if err != nil {
		return err
	}
	lastResetTime := sub.LastResetTime
	nextResetTime := sub.NextResetTime
	if advanceResetTime {
		nextResetTime = calcSubscriptionNextResetTime(time.Unix(now, 0), resetPlan, sub.EndTime)
		if nextResetTime > 0 {
			lastResetTime = now
		} else {
			lastResetTime = 0
		}
	}
	return updateSubscriptionResetState(tx, sub, 0, lastResetTime, nextResetTime, now, true)
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
	return adminResetUserSubscriptionsByPlan(userId, planId, advanceResetTime, nil)
}

func AdminResetUserSubscriptionsByPlanAuthorized(userId, planId int, advanceResetTime bool, operatorRole int) (*SubscriptionResetResult, error) {
	return adminResetUserSubscriptionsByPlan(userId, planId, advanceResetTime, &operatorRole)
}

func adminResetUserSubscriptionsByPlan(userId, planId int, advanceResetTime bool, operatorRole *int) (*SubscriptionResetResult, error) {
	if userId <= 0 || planId <= 0 {
		return nil, errors.New("invalid userId or planId")
	}
	var result *SubscriptionResetResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := authorizeSubscriptionTargetTx(tx, userId, operatorRole); err != nil {
			return err
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planId).Error; err != nil {
			return err
		}
		var subs []model.UserSubscription
		if err := locking.SubscriptionLockForUpdate(tx).
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
	return adminResetPlanSubscriptions(planId, advanceResetTime, nil)
}

func AdminResetPlanSubscriptionsAuthorized(planId int, advanceResetTime bool, operatorRole int) (*SubscriptionResetResult, error) {
	return adminResetPlanSubscriptions(planId, advanceResetTime, &operatorRole)
}

func adminResetPlanSubscriptions(planId int, advanceResetTime bool, operatorRole *int) (*SubscriptionResetResult, error) {
	if planId <= 0 {
		return nil, errors.New("invalid planId")
	}
	var result *SubscriptionResetResult
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planId).Error; err != nil {
			return err
		}
		authorizedUsers := map[int]struct{}{}
		if operatorRole != nil {
			var userIDs []int
			if err := tx.Model(&model.UserSubscription{}).
				Distinct("user_id").
				Where("plan_id = ? AND status = ? AND end_time > ?", planId, SubscriptionStatusActive, now).
				Order("user_id asc").
				Pluck("user_id", &userIDs).Error; err != nil {
				return err
			}
			for _, userID := range userIDs {
				if err := authorizeSubscriptionTargetTx(tx, userID, operatorRole); err != nil {
					return err
				}
				authorizedUsers[userID] = struct{}{}
			}
		}
		var subs []model.UserSubscription
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("plan_id = ? AND status = ? AND end_time > ?", planId, SubscriptionStatusActive, now).
			Order("user_id asc, end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return err
		}
		if operatorRole != nil {
			// A subscription inserted for a previously unseen user while the
			// target rows were being locked changes the authorization scope. Abort
			// without partial resets and let the operator retry from a fresh set.
			for i := range subs {
				if _, ok := authorizedUsers[subs[i].UserId]; !ok {
					return ErrSubscriptionStateChanged
				}
			}
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
