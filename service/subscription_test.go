package service

import (
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
	dsn := "file:" + filepath.Join(t.TempDir(), "sub.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.SubscriptionPlan{},
		&model.SubscriptionOrder{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
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
}

func TestListSubscriptionPlansOrderAndFilter(t *testing.T) {
	initSubDB(t)
	seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Title = "low"; p.SortOrder = 1 })
	seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Title = "high"; p.SortOrder = 9 })
	seedRawPlan(t, func(p *model.SubscriptionPlan) { p.Title = "off"; p.Enabled = false })

	plans := ListSubscriptionPlans()
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
		expectedLast := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
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
