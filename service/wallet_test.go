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

func initWalletDB(t *testing.T) {
	t.Helper()
	// File-based DB with a busy timeout so concurrent goroutines share the
	// database (in-memory SQLite is per-connection).
	dsn := "file:" + filepath.Join(t.TempDir(), "wallet.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Redemption{}, &model.Checkin{}, &model.TopUp{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
}

func TestRedeemCreditsOnce(t *testing.T) {
	initWalletDB(t)
	u := newUser(t, 0)

	r, err := CreateRedemption(1, "test", 500, 0)
	require.NoError(t, err)

	quota, err := Redeem(u.Id, r.Key)
	require.NoError(t, err)
	assert.Equal(t, 500, quota)

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 500, got.Quota)

	// Second redeem of the same code fails.
	_, err = Redeem(u.Id, r.Key)
	assert.Equal(t, ErrInvalidRedemption, err)
	assert.Equal(t, 500, got.Quota)
}

func TestConcurrentRedeemCreditsExactlyOnce(t *testing.T) {
	initWalletDB(t)
	u := newUser(t, 0)
	r, err := CreateRedemption(1, "test", 100, 0)
	require.NoError(t, err)

	const n = 10
	var wg sync.WaitGroup
	successes := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Redeem(u.Id, r.Key)
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
	assert.Equal(t, 1, count, "exactly one redemption must succeed")
	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 100, got.Quota)
}

func TestCheckInOncePerDay(t *testing.T) {
	initWalletDB(t)
	u := newUser(t, 0)

	reward, err := CheckIn(u.Id)
	require.NoError(t, err)
	assert.Equal(t, DefaultCheckInQuota, reward)
	assert.True(t, CheckInStatus(u.Id))

	_, err = CheckIn(u.Id)
	assert.Equal(t, ErrAlreadyCheckedIn, err)

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, DefaultCheckInQuota, got.Quota)
}

func TestTopUpCompleteIdempotent(t *testing.T) {
	initWalletDB(t)
	u := newUser(t, 0)

	order, err := CreateTopUp(u.Id, 1000, 1.0, "balance", "balance")
	require.NoError(t, err)

	require.NoError(t, CompleteTopUp(u.Id, order.TradeNo, 1000))
	// Second completion must not double-credit.
	require.NoError(t, CompleteTopUp(u.Id, order.TradeNo, 1000))

	var got model.User
	require.NoError(t, model.DB.First(&got, u.Id).Error)
	assert.Equal(t, 1000, got.Quota)
}
