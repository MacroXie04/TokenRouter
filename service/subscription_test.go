package service

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func initSubDB(t *testing.T) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "sub.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.SubscriptionPlan{}, &model.SubscriptionOrder{}, &model.UserSubscription{}))
	model.DB = db
	model.LOG_DB = db
}

func seedPlan(t *testing.T, quota int64, group string) *model.SubscriptionPlan {
	t.Helper()
	p := &model.SubscriptionPlan{
		Title:        "Pro",
		PriceAmount:  "10.000000",
		Enabled:      true,
		TotalAmount:  quota,
		DurationUnit: "day",
		DurationValue: 1,
		UpgradeGroup: group,
	}
	require.NoError(t, CreateSubscriptionPlan(p))
	return p
}

func TestPurchaseAndConsumeSubscription(t *testing.T) {
	initSubDB(t)
	u := newUser(t, 0)
	plan := seedPlan(t, 1000, "vip")

	sub, err := PurchaseSubscription(u.Id, plan.Id)
	require.NoError(t, err)
	assert.Equal(t, SubscriptionStatusActive, sub.Status)
	assert.Equal(t, int64(1000), sub.AmountTotal)

	// Group upgrade applied.
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, "vip", got.Group)

	// Consume 400 -> ok.
	consumed, err := ConsumeSubscriptionQuota(u.Id, 400)
	require.NoError(t, err)
	assert.Equal(t, 400, consumed)

	// Consume 700 -> exceeds remaining 600.
	_, err = ConsumeSubscriptionQuota(u.Id, 700)
	assert.Equal(t, ErrSubscriptionQuotaExceeded, err)
}

func TestConcurrentSubscriptionConsumeDoesNotOverspend(t *testing.T) {
	initSubDB(t)
	u := newUser(t, 0)
	plan := seedPlan(t, 100, "")
	_, err := PurchaseSubscription(u.Id, plan.Id)
	require.NoError(t, err)

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
