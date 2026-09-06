package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func initUserDB(t *testing.T, dsn string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	model.LOG_DB = db
}

func newUser(t *testing.T, quota int) *model.User {
	t.Helper()
	u := &model.User{Username: fmt.Sprintf("u-%d", quota), Password: "x", Role: 1, Status: 1, Quota: quota}
	require.NoError(t, model.DB.Create(u).Error)
	return u
}

func TestSettleUserQuotaNoDoubleCharge(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 1000)

	// Pre-consume 100, then settle at actual 60. Total deduction must be 60,
	// never 100+60 (double charge).
	require.NoError(t, PreConsumeUserQuota(u.Id, 100))
	require.NoError(t, SettleUserQuota(u.Id, 100, 60))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 940, got.Quota, "quota must be 1000-60=940 (no double charge)")
	assert.Equal(t, 60, got.UsedQuota)
	assert.Equal(t, 1, got.RequestCount)
}

func TestSettleUserQuotaShortfall(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 1000)

	// Pre-consume 100, actual 150 -> deduct the 50 shortfall.
	require.NoError(t, PreConsumeUserQuota(u.Id, 100))
	require.NoError(t, SettleUserQuota(u.Id, 100, 150))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 850, got.Quota)
	assert.Equal(t, 150, got.UsedQuota)
}

func TestSettleUserQuotaRefund(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 1000)

	require.NoError(t, PreConsumeUserQuota(u.Id, 100))
	require.NoError(t, RefundUserQuota(u.Id, 100))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 1000, got.Quota)
}

func TestConcurrentPreConsumeDoesNotOverspend(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db") + "?_pragma=busy_timeout(5000)"
	initUserDB(t, dsn)
	u := newUser(t, 100)

	const workers = 10
	const each = 15 // 10*15=150 > 100, so at most 6 can succeed
	var wg sync.WaitGroup
	successes := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			successes <- PreConsumeUserQuota(u.Id, each) == nil
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
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.True(t, got.Quota >= 0, "quota must never go negative, got %d", got.Quota)
	// Exactly count successes deducted 15 each; the remainder is untouched.
	assert.Equal(t, 100-count*each, got.Quota)
	assert.LessOrEqual(t, count, 6, "at most floor(100/15)=6 can succeed")
	assert.Less(t, got.Quota, each, "remaining quota is less than one deduction")
}

func TestSettleUserQuotaShortfallFailureIsAtomic(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 100)
	require.NoError(t, PreConsumeUserQuota(u.Id, 80))

	err := SettleUserQuota(u.Id, 80, 120)
	require.ErrorIs(t, err, ErrInsufficientQuota)

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 20, got.Quota, "the held reservation is preserved when the shortfall cannot be funded")
	assert.Zero(t, got.UsedQuota)
	assert.Zero(t, got.RequestCount)
}

func TestSettleUserQuotaDatabaseFailureRollsBackAdjustmentAndCounters(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 1000)
	require.NoError(t, PreConsumeUserQuota(u.Id, 100))

	callbackName := "test:fail_legacy_quota_settlement"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("injected update failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	require.ErrorContains(t, SettleUserQuota(u.Id, 100, 60), "injected update failure")
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 900, got.Quota)
	assert.Zero(t, got.UsedQuota)
	assert.Zero(t, got.RequestCount)
}

func TestRefundUserQuotaRejectsOverflowAndMissingUser(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, int(common.MaxQuota))

	require.ErrorIs(t, RefundUserQuota(u.Id, 1), ErrUserQuotaOverflow)
	require.ErrorIs(t, RefundUserQuota(u.Id+999, 1), ErrUserNotFound)
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, int(common.MaxQuota), got.Quota)
}

func TestQuotaHelpersRejectNegativeAndUsageOverflow(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 1000)
	require.NoError(t, model.DB.Model(u).Update("used_quota", int(common.MaxQuota)).Error)

	require.ErrorIs(t, PreConsumeUserQuota(u.Id, -1), ErrInvalidQuota)
	require.ErrorIs(t, RefundUserQuota(u.Id, -1), ErrInvalidQuota)
	require.ErrorIs(t, SettleUserQuota(u.Id, 0, -1), ErrInvalidQuota)
	require.ErrorIs(t, RecordUserUsage(u.Id, -1), ErrInvalidQuota)
	require.ErrorIs(t, RecordUserUsage(u.Id, 1), ErrUserUsageOverflow)

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, int(common.MaxQuota), got.UsedQuota)
	assert.Zero(t, got.RequestCount)
}

func TestNewFundingSessionReturnsBillingPreferenceReadFailure(t *testing.T) {
	initUserDB(t, ":memory:")
	u := newUser(t, 100)

	callbackName := "test:fail_billing_preference_read"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("injected settings read failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	_, err := NewFundingSession(u.Id, 25)
	require.ErrorContains(t, err, "load billing preference")
	require.ErrorContains(t, err, "injected settings read failure")

	var quota int
	require.NoError(t, model.DB.Raw("SELECT quota FROM users WHERE id = ?", u.Id).Scan(&quota).Error)
	assert.Equal(t, 100, quota, "a settings read failure must not silently choose and reserve another funding source")
}
