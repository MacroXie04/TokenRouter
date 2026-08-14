package service

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

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
