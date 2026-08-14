package service

import (
	"path/filepath"
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

func initTaskDB(t *testing.T) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "task.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
	model.DB = db
	model.LOG_DB = db
}

func TestAcquireAndReleaseTaskLock(t *testing.T) {
	initTaskDB(t)

	ok, err := AcquireTaskLock("type-a", "node-1", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	// Second acquire while held fails.
	ok, err = AcquireTaskLock("type-a", "node-2", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)

	// After release, another node can acquire.
	require.NoError(t, ReleaseTaskLock("type-a", "node-1"))
	ok, _ = AcquireTaskLock("type-a", "node-2", time.Minute)
	assert.True(t, ok)
}

func TestStaleTaskLockTakeover(t *testing.T) {
	initTaskDB(t)

	// Simulate a crashed node's stale lock.
	now := common.NowTimestamp()
	lock := model.SystemTaskLock{Type: "type-b", LockedBy: "crashed", LockedUntil: now - 10, UpdatedAt: now}
	require.NoError(t, model.DB.Create(&lock).Error)

	ok, err := AcquireTaskLock("type-b", "node-1", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "a stale lock must be taken over")
}

func TestConcurrentAcquireSingleWinner(t *testing.T) {
	initTaskDB(t)

	const n = 10
	var wg sync.WaitGroup
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := AcquireTaskLock("type-c", "node-x", time.Minute)
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for r := range results {
		if r {
			winners++
		}
	}
	assert.Equal(t, 1, winners, "exactly one node must win the lease")
}
