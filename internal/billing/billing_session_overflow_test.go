package billing

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func setupBillingSessionOverflowDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "billing-overflow.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}))
	model.DB = db
	model.LOG_DB = db
	return db
}

func TestCommitReservedUsageRejectsCorruptWalletQuotaAndRetries(t *testing.T) {
	db := setupBillingSessionOverflowDB(t)
	user := model.User{Username: "wallet-max-int", Status: model.UserStatusEnabled, Quota: math.MaxInt}
	require.NoError(t, db.Create(&user).Error)
	funding := &FundingSession{userId: user.Id, source: BillingSourceWallet, reserved: 1}

	err := funding.CommitReservedUsage(0, 0, 0)
	assert.ErrorIs(t, err, ErrUserQuotaOverflow)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, math.MaxInt, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.False(t, funding.settled)

	require.NoError(t, db.Model(&user).Update("quota", 99).Error)
	require.NoError(t, funding.CommitReservedUsage(0, 0, 0))
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestCommitReservedUsageRejectsCorruptUserCounters(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
	}{
		{name: "used quota", column: "used_quota"},
		{name: "request count", column: "request_count"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupBillingSessionOverflowDB(t)
			user := model.User{Username: "user-counter-max-int", Status: model.UserStatusEnabled, Quota: 100}
			require.NoError(t, db.Create(&user).Error)
			require.NoError(t, db.Model(&user).UpdateColumn(test.column, math.MaxInt).Error)
			funding := &FundingSession{userId: user.Id, source: BillingSourceWallet}

			err := funding.CommitReservedUsage(1, 0, 0)
			assert.ErrorIs(t, err, ErrUserUsageOverflow)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 100, user.Quota, "wallet charge must roll back")
			assert.Equal(t, math.MaxInt, map[string]int{
				"used_quota": user.UsedQuota, "request_count": user.RequestCount,
			}[test.column])
			assert.False(t, funding.settled)

			require.NoError(t, db.Model(&user).UpdateColumn(test.column, 0).Error)
			require.NoError(t, funding.CommitReservedUsage(1, 0, 0))
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 99, user.Quota)
			assert.Equal(t, 1, user.UsedQuota)
			assert.Equal(t, 1, user.RequestCount)
		})
	}
}

func TestCommitReservedUsageRejectsCorruptSubscriptionUsageAndRetries(t *testing.T) {
	db := setupBillingSessionOverflowDB(t)
	user := model.User{Username: "subscription-max-int", Status: model.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	subscription := model.UserSubscription{
		UserId: user.Id, AmountTotal: 0, AmountUsed: int64(math.MaxInt), Status: SubscriptionStatusActive,
	}
	require.NoError(t, db.Create(&subscription).Error)
	funding := &FundingSession{
		userId: user.Id, source: BillingSourceSubscription, reserved: 1, subscriptionId: subscription.Id,
	}

	err := funding.CommitReservedUsage(2, 0, 0)
	require.ErrorContains(t, err, "subscription quota overflow")
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, int64(math.MaxInt), subscription.AmountUsed)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.False(t, funding.settled)

	require.NoError(t, db.Model(&subscription).Update("amount_used", 1).Error)
	require.NoError(t, funding.CommitReservedUsage(2, 0, 0))
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, int64(2), subscription.AmountUsed)
	assert.Equal(t, 2, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestCommitReservedUsageRejectsCorruptTokenQuotaAndRollsBackWallet(t *testing.T) {
	db := setupBillingSessionOverflowDB(t)
	user := model.User{Username: "token-max-int", Status: model.UserStatusEnabled, Quota: 90}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-token-max-int", Status: TokenStatusEnabled,
		RemainQuota: math.MaxInt,
	}
	require.NoError(t, db.Create(&token).Error)
	funding := &FundingSession{userId: user.Id, source: BillingSourceWallet, reserved: 10}

	err := funding.CommitReservedUsage(0, token.Id, 10)
	assert.ErrorIs(t, err, ErrTokenQuotaOverflow)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota, "the user refund must roll back with token reconciliation")
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, math.MaxInt, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.False(t, funding.settled)

	require.NoError(t, db.Model(&token).Update("remain_quota", 90).Error)
	require.NoError(t, funding.CommitReservedUsage(0, token.Id, 10))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 100, user.Quota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 100, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
}

func TestCommitReservedUsageRejectsCorruptTokenUsageAndRetries(t *testing.T) {
	db := setupBillingSessionOverflowDB(t)
	user := model.User{Username: "token-used-max-int", Status: model.UserStatusEnabled, Quota: 90}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-token-used-max-int", Status: TokenStatusEnabled,
		RemainQuota: 90, UsedQuota: math.MaxInt,
	}
	require.NoError(t, db.Create(&token).Error)
	funding := &FundingSession{userId: user.Id, source: BillingSourceWallet, reserved: 10}

	err := funding.CommitReservedUsage(10, token.Id, 10)
	assert.ErrorIs(t, err, ErrTokenQuotaOverflow)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Zero(t, user.UsedQuota, "user accounting must roll back with token reconciliation")
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, math.MaxInt, token.UsedQuota)
	assert.False(t, funding.settled)

	require.NoError(t, db.Model(&token).Update("used_quota", 0).Error)
	require.NoError(t, funding.CommitReservedUsage(10, token.Id, 10))
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 90, token.RemainQuota)
	assert.Equal(t, 10, token.UsedQuota)
}

func TestFundingSessionSettleRollsBackAndRetriesSubscriptionFailure(t *testing.T) {
	db := setupBillingSessionOverflowDB(t)
	user := model.User{Username: "settle-retry", Status: model.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	subscription := model.UserSubscription{UserId: user.Id, AmountTotal: 100, AmountUsed: 1}
	require.NoError(t, db.Create(&subscription).Error)
	funding := &FundingSession{
		userId: user.Id, source: BillingSourceSubscription, reserved: 1, subscriptionId: subscription.Id,
	}

	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_legacy_settle_user_usage"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.User{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected settle usage failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	err := funding.Settle(2)
	require.ErrorContains(t, err, "injected settle usage failure")
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, int64(1), subscription.AmountUsed, "subscription delta rolls back with user counters")
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)

	require.NoError(t, funding.Settle(2))
	require.NoError(t, funding.Settle(2), "successful retry remains idempotent")
	require.NoError(t, db.First(&subscription, subscription.Id).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, int64(2), subscription.AmountUsed)
	assert.Equal(t, 2, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
}

func TestFundingSessionConcurrentSettleIsExactlyOnce(t *testing.T) {
	db := setupBillingSessionOverflowDB(t)
	user := model.User{Username: "settle-concurrent", Status: model.UserStatusEnabled, Quota: 90}
	require.NoError(t, db.Create(&user).Error)
	funding := &FundingSession{userId: user.Id, source: BillingSourceWallet, reserved: 10}

	const workers = 16
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- funding.Settle(10)
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, db.First(&user, user.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.True(t, funding.settled)
}

func TestBillingLogFieldsRejectsCorruptSubscriptionSnapshot(t *testing.T) {
	funding := &FundingSession{
		userId: 1, source: BillingSourceSubscription, reserved: 1,
		subTotal: math.MaxInt, subUsedAfter: math.MaxInt, postDelta: 1,
	}

	fields := funding.BillingLogFields()
	assert.Equal(t, int64(2), fields["subscription_consumed"])
	assert.NotContains(t, fields, "subscription_total")
	assert.NotContains(t, fields, "subscription_used")
	assert.NotContains(t, fields, "subscription_remain")
}

func TestRestoreFundingSessionRejectsOutOfDomainReservation(t *testing.T) {
	_, err := RestoreFundingSession(1, FundingReservation{
		Source: BillingSourceWallet, Reserved: math.MaxInt,
	})
	require.ErrorIs(t, err, ErrInvalidQuota)
}
