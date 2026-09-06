package service

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func initSubDB(t *testing.T) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "sub.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.SubscriptionPlan{},
		&model.SubscriptionOrder{}, &model.TopUp{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{}, &model.Log{}, &model.AuditLogOutbox{}))
	model.DB = db
	model.LOG_DB = db
	previousRatios := getGroupRatios()
	SetGroupRatios(map[string]float64{"default": 1, "vip": 1, "pro": 1})
	t.Cleanup(func() { SetGroupRatios(previousRatios) })
}

func TestSubscriptionDurationArithmeticRejectsOverflow(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	for _, plan := range []*model.SubscriptionPlan{
		{DurationUnit: SubscriptionDurationHour, DurationValue: math.MaxInt},
		{DurationUnit: SubscriptionDurationDay, DurationValue: math.MaxInt},
		{DurationUnit: SubscriptionDurationMonth, DurationValue: math.MaxInt},
		{DurationUnit: SubscriptionDurationYear, DurationValue: math.MaxInt},
		{DurationUnit: SubscriptionDurationCustom, CustomSeconds: math.MaxInt64},
	} {
		end, err := calcPlanEndTime(start, plan)
		require.Error(t, err, "%+v", plan)
		assert.Zero(t, end)
	}
	for _, plan := range []*model.SubscriptionPlan{
		{DurationUnit: SubscriptionDurationHour, DurationValue: 1},
		{DurationUnit: SubscriptionDurationDay, DurationValue: 1},
		{DurationUnit: SubscriptionDurationMonth, DurationValue: 1},
		{DurationUnit: SubscriptionDurationYear, DurationValue: 1},
		{DurationUnit: SubscriptionDurationCustom, CustomSeconds: 1},
	} {
		end, err := calcPlanEndTime(start, plan)
		require.NoError(t, err)
		assert.Greater(t, end, start.Unix())
	}
	_, err := calcPlanEndTime(time.Unix(math.MaxInt64-1, 0),
		&model.SubscriptionPlan{DurationUnit: SubscriptionDurationCustom, CustomSeconds: 2})
	require.Error(t, err)
}

func TestSubscriptionResetCadenceSnapshotSurvivesPlanMutationAndDeletion(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "immutable-reset", 0, "default")
	plan := seedPlan(t, 1000, "")
	plan.QuotaResetPeriod = SubscriptionResetDaily
	require.NoError(t, model.DB.Model(plan).Updates(map[string]any{
		"quota_reset_period": SubscriptionResetDaily,
	}).Error)
	_, err := AdminBindSubscription(user.Id, plan.Id)
	require.NoError(t, err)
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&sub).Error)
	assert.Equal(t, UserSubscriptionEntitlementVersion, sub.EntitlementVersion)
	assert.Equal(t, SubscriptionResetDaily, sub.QuotaResetPeriodSnapshot)

	now := common.NowTimestamp()
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", sub.Id).Updates(map[string]any{
		"amount_used": 500, "last_reset_time": now - 2*86400, "next_reset_time": now - 86400,
	}).Error)
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		Update("quota_reset_period", SubscriptionResetNever).Error)
	require.NoError(t, model.DB.Delete(&model.SubscriptionPlan{}, plan.Id).Error)
	require.NoError(t, model.DB.First(&sub, sub.Id).Error)
	require.NoError(t, ResetSubscriptionQuota(&sub))
	require.NoError(t, model.DB.First(&sub, sub.Id).Error)
	assert.Zero(t, sub.AmountUsed)
	assert.Greater(t, sub.NextResetTime, now)
}

func TestExpireDueSubscriptionsRecomputesOverlappingGroupsAndRacesInvalidation(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "expiry-groups", 0, "default")
	vip := seedPlan(t, 100, "vip")
	pro := seedPlan(t, 100, "pro")
	_, err := AdminBindSubscription(user.Id, vip.Id)
	require.NoError(t, err)
	_, err = AdminBindSubscription(user.Id, pro.Id)
	require.NoError(t, err)
	var subs []model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).Order("id asc").Find(&subs).Error)
	require.Len(t, subs, 2)
	assert.Equal(t, "default", subs[0].GroupBaseline)
	assert.Equal(t, "default", subs[1].GroupBaseline)
	now := common.NowTimestamp()
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", subs[0].Id).Update("end_time", now-1).Error)
	require.NoError(t, ExpireDueSubscriptions(1))
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, "pro", gotUser.Group, "remaining newest entitlement must win")

	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", subs[1].Id).Update("end_time", now-1).Error)
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; errs <- ExpireDueSubscriptions(10) }()
	go func() { <-start; _, err := AdminInvalidateUserSubscription(subs[1].Id); errs <- err }()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, "default", gotUser.Group)
	var final model.UserSubscription
	require.NoError(t, model.DB.First(&final, subs[1].Id).Error)
	assert.Contains(t, []string{SubscriptionStatusExpired, SubscriptionStatusCancelled}, final.Status)
}

func TestLegacySubscriptionCadenceBackfillsOnceAndThenStaysImmutable(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "legacy-reset-backfill", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.QuotaResetPeriod = SubscriptionResetWeekly
	})
	legacy := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 1,
		StartTime: common.NowTimestamp(), EndTime: common.NowTimestamp() + 86400, Status: SubscriptionStatusActive}
	require.NoError(t, model.DB.Create(&legacy).Error)
	require.NoError(t, BackfillLegacySubscriptionEntitlementSnapshots(10))
	require.NoError(t, model.DB.First(&legacy, legacy.Id).Error)
	assert.Equal(t, UserSubscriptionEntitlementVersion, legacy.EntitlementVersion)
	assert.Equal(t, SubscriptionResetWeekly, legacy.QuotaResetPeriodSnapshot)
	require.NoError(t, model.DB.Model(plan).Update("quota_reset_period", SubscriptionResetNever).Error)
	require.NoError(t, BackfillLegacySubscriptionEntitlementSnapshots(10))
	require.NoError(t, model.DB.First(&legacy, legacy.Id).Error)
	assert.Equal(t, SubscriptionResetWeekly, legacy.QuotaResetPeriodSnapshot)
}

func subUser(t *testing.T, name string, quota int, group string) *model.User {
	t.Helper()
	u := &model.User{Username: name, Password: "x", Role: 1, Status: 1, Quota: quota, Group: group}
	require.NoError(t, model.DB.Create(u).Error)
	return u
}

// seedPlan creates an enabled $10 one-day plan; at QuotaPerUnit=500000 a
// purchase deducts exactly 5,000,000 wallet quota.
func seedPlan(t *testing.T, quota int64, group string) *model.SubscriptionPlan {
	t.Helper()
	p := &model.SubscriptionPlan{
		Title:         "Pro",
		PriceAmount:   "10.000000",
		Enabled:       true,
		TotalAmount:   quota,
		DurationUnit:  "day",
		DurationValue: 1,
		UpgradeGroup:  group,
	}
	require.NoError(t, CreateSubscriptionPlan(p))
	return p
}

// seedRawPlan inserts a plan row directly so guard tests control every field
// (including values admin validation would reject).
func seedRawPlan(t *testing.T, mutate func(*model.SubscriptionPlan)) *model.SubscriptionPlan {
	t.Helper()
	p := &model.SubscriptionPlan{
		Title:         "Guard",
		PriceAmount:   "10",
		Enabled:       true,
		TotalAmount:   1000,
		DurationUnit:  "day",
		DurationValue: 1,
	}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, model.DB.Create(p).Error)
	return p
}

const tenDollarQuota = 10 * common.QuotaPerUnit // 5,000,000

func TestPurchaseAndConsumeSubscription(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "buyer", tenDollarQuota, "default")
	plan := seedPlan(t, 1000, "vip")

	require.NoError(t, PurchaseSubscriptionWithBalance(u.Id, plan.Id))

	// Wallet charged exactly price * QuotaPerUnit, group upgraded with snapshot.
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 0, got.Quota)
	assert.Equal(t, "vip", got.Group)

	sub, err := GetActiveSubscription(u.Id)
	require.NoError(t, err)
	assert.Equal(t, SubscriptionStatusActive, sub.Status)
	assert.Equal(t, int64(1000), sub.AmountTotal)
	assert.Equal(t, PaymentMethodBalance, sub.Source)
	assert.Equal(t, "default", sub.PrevUserGroup)

	// Completed order row with the balance trade-no format.
	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&order).Error)
	assert.Equal(t, plan.Id, order.PlanId)
	assert.Equal(t, 10.0, order.Money)
	assert.Equal(t, PaymentMethodBalance, order.PaymentMethod)
	assert.Equal(t, PaymentProviderBalance, order.PaymentProvider)
	assert.Equal(t, TopUpStatusSuccess, order.Status)
	assert.True(t, strings.HasPrefix(order.TradeNo, "SUBBALUSR"), "trade no: %s", order.TradeNo)
	assert.Equal(t, "charged_quota=5000000", order.ProviderPayload)

	// Top-up log recorded after commit.
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", u.Id, LogTypeTopup).First(&log).Error)
	assert.Contains(t, log.Content, "使用余额购买订阅成功")
	assert.Contains(t, log.Content, "扣除额度: 5000000")

	// Consume 400 -> ok.
	consumed, err := ConsumeSubscriptionQuota(u.Id, 400)
	require.NoError(t, err)
	assert.Equal(t, 400, consumed)

	// Consume 700 -> exceeds remaining 600.
	_, err = ConsumeSubscriptionQuota(u.Id, 700)
	assert.Equal(t, ErrSubscriptionQuotaExceeded, err)
}

func TestBalanceSubscriptionRollsBackWhenDurableAuditCannotBeEnqueued(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "subscription-audit-failure", tenDollarQuota, "default")
	plan := seedPlan(t, 1000, "vip")
	injected := errors.New("injected subscription audit failure")
	const callback = "test:fail_subscription_audit_enqueue"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err := PurchaseSubscriptionWithBalance(user.Id, plan.Id)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Equal(t, tenDollarQuota, stored.Quota)
	assert.Equal(t, "default", stored.Group)
	var subscriptions, orders int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", user.Id).Count(&orders).Error)
	assert.Zero(t, subscriptions)
	assert.Zero(t, orders)
}

func TestPurchaseSubscriptionWithBalanceGuards(t *testing.T) {
	initSubDB(t)

	t.Run("disabled plan", func(t *testing.T) {
		u := subUser(t, "g-disabled", tenDollarQuota, "")
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Enabled = false })
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.EqualError(t, err, "套餐未启用")
	})

	t.Run("balance pay disallowed", func(t *testing.T) {
		u := subUser(t, "g-nobal", tenDollarQuota, "")
		no := false
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.AllowBalancePay = &no })
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.EqualError(t, err, "该套餐不允许使用余额兑换")
	})

	t.Run("negative price", func(t *testing.T) {
		u := subUser(t, "g-neg", tenDollarQuota, "")
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.PriceAmount = "-5" })
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.EqualError(t, err, "套餐价格不能为负数")
	})

	t.Run("unparseable price", func(t *testing.T) {
		u := subUser(t, "g-badprice", tenDollarQuota, "")
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.PriceAmount = "ten dollars" })
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.EqualError(t, err, "参数错误")
	})

	t.Run("insufficient balance rolls back everything", func(t *testing.T) {
		u := subUser(t, "g-poor", tenDollarQuota-1, "")
		p := seedRawPlan(t, nil)
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.EqualError(t, err, "余额不足")
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, tenDollarQuota-1, got.Quota, "quota must be untouched")
		var subs, orders int64
		model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", u.Id).Count(&subs)
		model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", u.Id).Count(&orders)
		assert.Zero(t, subs)
		assert.Zero(t, orders)
	})

	t.Run("purchase cap refunds nothing on failure", func(t *testing.T) {
		u := subUser(t, "g-cap", 2*tenDollarQuota, "")
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.MaxPurchasePerUser = 1 })
		require.NoError(t, PurchaseSubscriptionWithBalance(u.Id, p.Id))
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.EqualError(t, err, "已达到该套餐购买上限")
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, tenDollarQuota, got.Quota, "second purchase must not charge")
		var orders int64
		model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", u.Id).Count(&orders)
		assert.Equal(t, int64(1), orders)
	})

	t.Run("stacked purchases both active", func(t *testing.T) {
		u := subUser(t, "g-stack", 2*tenDollarQuota, "")
		p := seedRawPlan(t, nil)
		require.NoError(t, PurchaseSubscriptionWithBalance(u.Id, p.Id))
		require.NoError(t, PurchaseSubscriptionWithBalance(u.Id, p.Id))
		var subs int64
		model.DB.Model(&model.UserSubscription{}).
			Where("user_id = ? AND status = ?", u.Id, SubscriptionStatusActive).Count(&subs)
		assert.Equal(t, int64(2), subs)
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, 0, got.Quota)
	})

	t.Run("free plan charges nothing", func(t *testing.T) {
		u := subUser(t, "g-free", 123, "")
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) { p.PriceAmount = "" })
		require.NoError(t, PurchaseSubscriptionWithBalance(u.Id, p.Id))
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, 123, got.Quota)
		var order model.SubscriptionOrder
		require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&order).Error)
		assert.Equal(t, "charged_quota=0", order.ProviderPayload)
	})

	t.Run("missing plan", func(t *testing.T) {
		u := subUser(t, "g-noplan", tenDollarQuota, "")
		err := PurchaseSubscriptionWithBalance(u.Id, 99999)
		require.Error(t, err)
	})

	t.Run("corrupt wallet balance fails closed without wrapping", func(t *testing.T) {
		u := subUser(t, "g-corrupt-balance", math.MaxInt, "")
		p := seedRawPlan(t, nil)
		err := PurchaseSubscriptionWithBalance(u.Id, p.Id)
		require.ErrorIs(t, err, ErrUserQuotaOverflow)
		var got model.User
		require.NoError(t, model.DB.First(&got, u.Id).Error)
		assert.Equal(t, math.MaxInt, got.Quota)
		var subscriptions, orders int64
		require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", u.Id).Count(&subscriptions).Error)
		require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", u.Id).Count(&orders).Error)
		assert.Zero(t, subscriptions)
		assert.Zero(t, orders)
	})
}

func TestSubscriptionPlanFinancialValidation(t *testing.T) {
	initSubDB(t)

	for _, price := range []string{"NaN", "+Inf", "-Inf", "0x1p2", "1e2147483647", "1.0000001"} {
		t.Run("reject non-decimal price "+price, func(t *testing.T) {
			_, err := ParseSubscriptionPlanPrice(price)
			require.EqualError(t, err, "参数错误")
			plan := model.SubscriptionPlan{Title: "invalid price", PriceAmount: price, TotalAmount: 1}
			require.EqualError(t, AdminCreateSubscriptionPlan(&plan), "参数错误")
			assert.Zero(t, plan.Id)
		})
	}

	t.Run("persisted decimal overflow is rejected", func(t *testing.T) {
		_, err := ParseSubscriptionPlanPrice("10000")
		require.EqualError(t, err, "参数错误")
	})

	t.Run("quota upper bound is accepted", func(t *testing.T) {
		plan := model.SubscriptionPlan{Title: "bounded", PriceAmount: "9999.000", TotalAmount: common.MaxQuota}
		require.NoError(t, AdminCreateSubscriptionPlan(&plan))
		assert.NotZero(t, plan.Id)
		assert.Equal(t, "9999.000000", plan.PriceAmount)
	})

	t.Run("quota above upper bound is rejected by both create paths", func(t *testing.T) {
		for _, create := range []func(*model.SubscriptionPlan) error{CreateSubscriptionPlan, AdminCreateSubscriptionPlan} {
			plan := model.SubscriptionPlan{Title: "oversized", PriceAmount: "1", TotalAmount: common.MaxQuota + 1}
			err := create(&plan)
			require.EqualError(t, err, fmt.Sprintf("总额度不能超过%d", common.MaxQuota))
			assert.Zero(t, plan.Id)
		}
	})
}

func TestSubscriptionFulfillmentRejectsOversizedPersistedPlan(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "oversized-plan-fulfillment", 100, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = ""
		plan.TotalAmount = common.MaxQuota + 1
	})

	err := PurchaseSubscriptionWithBalance(user.Id, plan.Id)
	require.ErrorIs(t, err, ErrSubscriptionQuotaOverflow)

	order := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 1, TradeNo: "oversized-plan-order",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(&order).Error)
	err = CompleteSubscriptionOrder(order.TradeNo, "provider-payload", PaymentProviderStripe, "")
	require.ErrorIs(t, err, ErrSubscriptionQuotaOverflow)

	var subscriptions, topups int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Zero(t, subscriptions)
	assert.Zero(t, topups)
	var storedOrder model.SubscriptionOrder
	require.NoError(t, model.DB.First(&storedOrder, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, storedOrder.Status)
}

func TestListSubscriptionPlansOrderAndFilter(t *testing.T) {
	initSubDB(t)
	seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Title = "low"; p.SortOrder = 1 })
	seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Title = "high"; p.SortOrder = 9 })
	seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Title = "off"; p.Enabled = false })

	plans, err := ListSubscriptionPlans()
	require.NoError(t, err)
	require.Len(t, plans, 2)
	assert.Equal(t, "high", plans[0].Title)
	assert.Equal(t, "low", plans[1].Title)
	// Defaults normalized for display.
	require.NotNil(t, plans[0].AllowBalancePay)
	assert.True(t, *plans[0].AllowBalancePay)
}

func TestConcurrentSubscriptionConsumeDoesNotOverspend(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "racer", tenDollarQuota, "")
	plan := seedPlan(t, 100, "")
	require.NoError(t, PurchaseSubscriptionWithBalance(u.Id, plan.Id))

	const workers = 10
	const each = 30 // 10*30=300 > 100
	var wg sync.WaitGroup
	successes := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ConsumeSubscriptionQuota(u.Id, each)
			successes <- err == nil
		}()
	}
	wg.Wait()
	close(successes)
	count := 0
	for s := range successes {
		if s {
			count++
		}
	}
	assert.LessOrEqual(t, count, 3, "at most floor(100/30)=3 can succeed")
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&sub).Error)
	assert.LessOrEqual(t, sub.AmountUsed, sub.AmountTotal)
}

func TestConsumeSubscriptionQuotaRejectsCorruptCounters(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "corrupt-subscription-counter", tenDollarQuota, "")
	for name, values := range map[string]struct{ total, used int64 }{
		"machine overflow": {total: math.MaxInt64, used: math.MaxInt64},
		"negative used":    {total: 100, used: -1},
		"used above total": {total: 100, used: 101},
	} {
		t.Run(name, func(t *testing.T) {
			sub := model.UserSubscription{
				UserId: u.Id, PlanId: 1, AmountTotal: values.total, AmountUsed: values.used,
				StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
				Status: SubscriptionStatusActive,
			}
			require.NoError(t, model.DB.Create(&sub).Error)
			_, err := ConsumeSubscriptionQuota(u.Id, 1)
			require.ErrorIs(t, err, ErrSubscriptionQuotaOverflow)
			var got model.UserSubscription
			require.NoError(t, model.DB.First(&got, sub.Id).Error)
			assert.Equal(t, values.used, got.AmountUsed)
			require.NoError(t, model.DB.Delete(&sub).Error)
		})
	}
	_, err := ConsumeSubscriptionQuota(u.Id, -1)
	require.Error(t, err)
}

func TestResetSubscriptionQuotaCalendarWalk(t *testing.T) {
	initSubDB(t)
	now := time.Now()

	t.Run("daily walk catches up multiple missed windows", func(t *testing.T) {
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) {
			p.QuotaResetPeriod = SubscriptionResetDaily
			p.DurationUnit = "month"
		})
		start := now.AddDate(0, 0, -10)
		sub := model.UserSubscription{
			UserId: 501, PlanId: p.Id, AmountTotal: 1000, AmountUsed: 700,
			StartTime: start.Unix(), EndTime: now.AddDate(0, 1, 0).Unix(),
			Status:        SubscriptionStatusActive,
			LastResetTime: start.Unix(),
			NextResetTime: start.AddDate(0, 0, 1).Unix(), // long overdue
		}
		require.NoError(t, model.DB.Create(&sub).Error)

		require.NoError(t, ResetSubscriptionQuota(&sub))

		var got model.UserSubscription
		require.NoError(t, model.DB.First(&got, sub.Id).Error)
		assert.Zero(t, got.AmountUsed, "usage resets on a due window")
		assert.LessOrEqual(t, got.LastResetTime, now.Unix())
		assert.Greater(t, got.NextResetTime, now.Unix(), "schedule lands in the future, not the next missed slot")
		// The walk must land on the current window's midnight boundary, not drift.
		nowUTC := now.UTC()
		expectedLast := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
		assert.Equal(t, expectedLast.Unix(), got.LastResetTime)
	})

	t.Run("not yet due is a no-op", func(t *testing.T) {
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) {
			p.QuotaResetPeriod = SubscriptionResetDaily
			p.DurationUnit = "month"
		})
		sub := model.UserSubscription{
			UserId: 502, PlanId: p.Id, AmountTotal: 1000, AmountUsed: 300,
			StartTime: now.Unix(), EndTime: now.AddDate(0, 1, 0).Unix(),
			Status:        SubscriptionStatusActive,
			LastResetTime: now.Unix(),
			NextResetTime: now.Add(12 * time.Hour).Unix(),
		}
		require.NoError(t, model.DB.Create(&sub).Error)
		require.NoError(t, ResetSubscriptionQuota(&sub))
		var got model.UserSubscription
		require.NoError(t, model.DB.First(&got, sub.Id).Error)
		assert.Equal(t, int64(300), got.AmountUsed)
		assert.Equal(t, sub.NextResetTime, got.NextResetTime)
	})

	t.Run("never period clears stale schedule and keeps usage", func(t *testing.T) {
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) {
			p.QuotaResetPeriod = SubscriptionResetNever
			p.DurationUnit = "month"
		})
		sub := model.UserSubscription{
			UserId: 503, PlanId: p.Id, AmountTotal: 1000, AmountUsed: 500,
			StartTime: now.AddDate(0, 0, -5).Unix(), EndTime: now.AddDate(0, 1, 0).Unix(),
			Status:        SubscriptionStatusActive,
			NextResetTime: now.Add(-time.Hour).Unix(), // stale schedule from an old plan config
		}
		require.NoError(t, model.DB.Create(&sub).Error)
		require.NoError(t, ResetSubscriptionQuota(&sub))
		var got model.UserSubscription
		require.NoError(t, model.DB.First(&got, sub.Id).Error)
		assert.Equal(t, int64(500), got.AmountUsed, "never-period must not zero usage")
		assert.Zero(t, got.NextResetTime, "stale schedule cleared so the job stops selecting it")
	})

	t.Run("reset past end time clears schedule", func(t *testing.T) {
		p := seedRawPlan(t, func(p *model.SubscriptionPlan) {
			p.QuotaResetPeriod = SubscriptionResetCustom
			p.QuotaResetCustomSeconds = 7 * 24 * 3600 // weekly interval
			p.DurationUnit = "month"
		})
		// Ends in an hour: the next custom reset would land past end time.
		sub := model.UserSubscription{
			UserId: 504, PlanId: p.Id, AmountTotal: 1000, AmountUsed: 200,
			StartTime: now.Add(-2 * time.Hour).Unix(), EndTime: now.Add(time.Hour).Unix(),
			Status:        SubscriptionStatusActive,
			LastResetTime: now.Add(-2 * time.Hour).Unix(),
			NextResetTime: now.Add(-time.Minute).Unix(),
		}
		require.NoError(t, model.DB.Create(&sub).Error)
		require.NoError(t, ResetSubscriptionQuota(&sub))
		var got model.UserSubscription
		require.NoError(t, model.DB.First(&got, sub.Id).Error)
		assert.Zero(t, got.NextResetTime)
	})
}

func TestResetSubscriptionQuotaRejectsConcurrentUsageChange(t *testing.T) {
	initSubDB(t)
	now := time.Now()
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.QuotaResetPeriod = SubscriptionResetDaily
		plan.DurationUnit = SubscriptionDurationMonth
	})
	stale := model.UserSubscription{
		UserId: 701, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 40,
		StartTime: now.AddDate(0, 0, -2).Unix(), EndTime: now.AddDate(0, 1, 0).Unix(),
		Status:        SubscriptionStatusActive,
		LastResetTime: now.AddDate(0, 0, -2).Unix(),
		NextResetTime: now.AddDate(0, 0, -1).Unix(),
	}
	require.NoError(t, model.DB.Create(&stale).Error)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", stale.Id).
		UpdateColumn("amount_used", 55).Error)

	err := ResetSubscriptionQuota(&stale)
	require.ErrorIs(t, err, ErrSubscriptionStateChanged)
	var stored model.UserSubscription
	require.NoError(t, model.DB.First(&stored, stale.Id).Error)
	assert.Equal(t, int64(55), stored.AmountUsed, "a stale reset must not erase concurrent consumption")
	assert.Equal(t, stale.NextResetTime, stored.NextResetTime)

	staleZero := model.UserSubscription{
		UserId: 702, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 0,
		StartTime: now.Unix(), EndTime: now.AddDate(0, 1, 0).Unix(), Status: SubscriptionStatusActive,
	}
	require.NoError(t, model.DB.Create(&staleZero).Error)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", staleZero.Id).
		UpdateColumn("amount_used", 1).Error)
	err = resetUserSubscriptionTx(model.DB, &staleZero, plan, now.Unix(), false)
	require.ErrorIs(t, err, ErrSubscriptionStateChanged,
		"an apparent no-op must still detect consumption after the snapshot")
	stored = model.UserSubscription{}
	require.NoError(t, model.DB.First(&stored, staleZero.Id).Error)
	assert.Equal(t, int64(1), stored.AmountUsed)
}

func TestSubscriptionResetNarrowUpdatePreservesConcurrentFields(t *testing.T) {
	initSubDB(t)
	now := common.NowTimestamp()
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.QuotaResetPeriod = SubscriptionResetDaily
	})
	sub := model.UserSubscription{
		UserId: 702, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 40,
		StartTime: now - 3600, EndTime: now + 86400, Status: SubscriptionStatusActive,
		AllowWalletOverflow: false,
	}
	require.NoError(t, model.DB.Create(&sub).Error)

	const callbackName = "test:subscription_reset_concurrent_field"
	injected := false
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement.Table != (model.UserSubscription{}).TableName() {
			return
		}
		injected = true
		_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
			"UPDATE user_subscriptions SET allow_wallet_overflow = ? WHERE id = ?", true, sub.Id)
		if err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	require.NoError(t, resetUserSubscriptionTx(model.DB, &sub, plan, now, false))
	var stored model.UserSubscription
	require.NoError(t, model.DB.First(&stored, sub.Id).Error)
	assert.Zero(t, stored.AmountUsed)
	assert.True(t, stored.AllowWalletOverflow, "reset must not overwrite an unrelated concurrent update")
}

func TestCompleteSubscriptionOrderNarrowUpdatesPreserveConcurrentTopUpFields(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "subscription-order-narrow", 0, "default")
	plan := seedRawPlan(t, nil)
	order := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 10, TradeNo: "subscription-order-narrow",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: TopUpStatusPending, CreateTime: common.NowTimestamp(),
	}
	require.NoError(t, model.DB.Create(&order).Error)
	topup := model.TopUp{
		UserId: user.Id, Amount: 0, Money: 9, TradeNo: order.TradeNo,
		PaymentMethod: PaymentMethodStripe, PaymentProvider: "initial-provider",
		Status: TopUpStatusPending, CreateTime: order.CreateTime,
	}
	require.NoError(t, model.DB.Create(&topup).Error)

	const callbackName = "test:subscription_topup_concurrent_field"
	injected := false
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement.Table != (model.TopUp{}).TableName() {
			return
		}
		injected = true
		_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
			"UPDATE top_ups SET payment_provider = ? WHERE id = ?", "concurrent-provider", topup.Id)
		if err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "payload", PaymentProviderStripe, ""))
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, topup.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.Equal(t, 10.0, stored.Money)
	assert.Equal(t, "concurrent-provider", stored.PaymentProvider,
		"completion must not write a stale full top-up row")
}

func TestCompleteSubscriptionOrderCASRollsBackOnLifecycleRace(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "subscription-order-cas", 0, "default")
	plan := seedRawPlan(t, nil)
	order := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 10, TradeNo: "subscription-order-cas",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: TopUpStatusPending, CreateTime: common.NowTimestamp(),
	}
	require.NoError(t, model.DB.Create(&order).Error)

	const callbackName = "test:subscription_order_lifecycle_race"
	injected := false
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if injected || tx.Statement.Table != (model.SubscriptionOrder{}).TableName() {
			return
		}
		injected = true
		_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
			"UPDATE subscription_orders SET status = ? WHERE id = ?", TopUpStatusExpired, order.Id)
		if err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	err := CompleteSubscriptionOrder(order.TradeNo, "payload", PaymentProviderStripe, "")
	require.ErrorIs(t, err, ErrSubscriptionOrderStatusInvalid)
	var storedOrder model.SubscriptionOrder
	require.NoError(t, model.DB.First(&storedOrder, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, storedOrder.Status, "the whole failed completion transaction must roll back")
	var subscriptions, topups int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Zero(t, subscriptions)
	assert.Zero(t, topups)
}

func TestConcurrentSubscriptionOrderCompletionIsSingleEffect(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "subscription-order-concurrent", 0, "default")
	plan := seedRawPlan(t, nil)
	order := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 10, TradeNo: "subscription-order-concurrent",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: TopUpStatusPending, CreateTime: common.NowTimestamp(),
	}
	require.NoError(t, model.DB.Create(&order).Error)

	const workers = 4
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- CompleteSubscriptionOrder(order.TradeNo, "payload", PaymentProviderStripe, "")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	assert.Positive(t, succeeded, "at least one delivery must commit")

	var storedOrder model.SubscriptionOrder
	require.NoError(t, model.DB.First(&storedOrder, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, storedOrder.Status)
	var subscriptions, topups int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Equal(t, int64(1), subscriptions)
	assert.Equal(t, int64(1), topups)
}
