package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestLogRetentionIsStrictAndInvalidValuesDoNotDelete(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	require.NoError(t, db.Create(&model.Log{CreatedAt: common.NowTimestamp() - 200*24*60*60}).Error)

	for _, raw := range []string{"-1", "01", "1.5", "often", "36501", "9223372036854775807"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("LOG_RETENTION_DAYS", raw)
			require.Error(t, CleanupExpiredLogs())
			var count int64
			require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
			assert.Equal(t, int64(1), count)
		})
	}
}

func TestLogRetentionZeroDisablesAndBoundaryDeletes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	oldLive := model.Log{CreatedAt: common.NowTimestamp() - 2*24*60*60}
	oldTombstone := model.Log{CreatedAt: oldLive.CreatedAt, Content: "legacy-soft-delete"}
	fresh := model.Log{CreatedAt: common.NowTimestamp()}
	require.NoError(t, db.Create(&[]*model.Log{&oldLive, &oldTombstone, &fresh}).Error)
	require.NoError(t, db.Delete(&oldTombstone).Error)

	t.Setenv("LOG_RETENTION_DAYS", "0")
	require.NoError(t, CleanupExpiredLogs())
	var count int64
	require.NoError(t, db.Unscoped().Model(&model.Log{}).Count(&count).Error)
	assert.Equal(t, int64(3), count)

	t.Setenv("LOG_RETENTION_DAYS", "1")
	require.NoError(t, CleanupExpiredLogs())
	require.NoError(t, db.Unscoped().Model(&model.Log{}).Where("created_at < ?", common.NowTimestamp()-24*60*60).
		Count(&count).Error)
	assert.Zero(t, count, "retention must physically remove live rows and legacy tombstones")
	require.NoError(t, db.Unscoped().Model(&model.Log{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "fresh rows must remain")

	t.Setenv("LOG_RETENTION_DAYS", "36500")
	days, enabled, err := logRetentionDays()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, int64(maxLogRetentionDays), days)
}

func TestLogRetentionCancellationDoesNotDeleteRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	require.NoError(t, db.Create(&model.Log{CreatedAt: common.NowTimestamp() - 2*24*60*60}).Error)
	t.Setenv("LOG_RETENTION_DAYS", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, CleanupExpiredLogsContext(ctx), context.Canceled)
	var count int64
	require.NoError(t, db.Unscoped().Model(&model.Log{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestAutomaticRetentionSharesDurableCleanupQueueWithManualRequests(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Log{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })
	t.Setenv("LOG_RETENTION_DAYS", "1")

	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Log{CreatedAt: now - 2*24*60*60}).Error)

	require.NoError(t, EnqueueExpiredLogCleanupContext(context.Background()))
	automatic, err := model.GetActiveSystemTask(model.SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, automatic)
	var payload logCleanupPayload
	require.NoError(t, common.UnmarshalJsonStr(automatic.Payload, &payload))
	assert.Equal(t, logCleanupBatchSize, payload.BatchSize)
	assert.InDelta(t, now-24*60*60, payload.TargetTimestamp, 1)

	manual, err := StartLogCleanupTaskContext(context.Background(), now-60*60)
	require.NoError(t, err)
	assert.Equal(t, automatic.TaskID, manual.TaskID,
		"automatic and manual cleanup must deduplicate in one active-key domain")

	var taskCount, logCount int64
	require.NoError(t, db.Model(&model.SystemTask{}).Count(&taskCount).Error)
	require.NoError(t, db.Unscoped().Model(&model.Log{}).Count(&logCount).Error)
	assert.Equal(t, int64(1), taskCount)
	assert.Equal(t, int64(1), logCount,
		"enqueueing retention must not bypass the durable task runner and delete inline")
}

func TestAutomaticRetentionBeforeUnixEpochIsSuccessfulNoOpAtDatabaseClock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	rawDB, err := db.DB()
	require.NoError(t, err)
	rawDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	t.Setenv("LOG_RETENTION_DAYS", "36500")

	const databaseNow = int64(1_000_000_000)
	clockCalls := 0
	err = enqueueExpiredLogCleanupContextWithClock(context.Background(), func(ctx context.Context) (int64, error) {
		clockCalls++
		require.NoError(t, ctx.Err())
		return databaseNow, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, clockCalls)
	var taskCount int64
	require.NoError(t, db.Model(&model.SystemTask{}).Count(&taskCount).Error)
	assert.Zero(t, taskCount, "a pre-epoch horizon must not enqueue a predictably invalid cleanup task")

	// Invalid configuration still fails before consulting the clock or writing
	// a task; the no-op applies only to a valid retention horizon.
	t.Setenv("LOG_RETENTION_DAYS", "36501")
	err = enqueueExpiredLogCleanupContextWithClock(context.Background(), func(context.Context) (int64, error) {
		clockCalls++
		return databaseNow, nil
	})
	require.Error(t, err)
	assert.Equal(t, 1, clockCalls)
	require.NoError(t, db.Model(&model.SystemTask{}).Count(&taskCount).Error)
	assert.Zero(t, taskCount)
}
