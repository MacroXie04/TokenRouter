package operations

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSystemTaskExternalDatabaseLeaseAndCadence proves the durable periodic
// scheduler on the real row-locking dialects. It covers concurrent admission,
// database-clock cadence, owner fencing, idempotent progress writes under
// MySQL's changed-row semantics, lease renewal, and atomic terminal release.
func TestSystemTaskExternalDatabaseLeaseAndCadence(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}
	if os.Getenv("TOKENROUTER_TEST_ALLOW_SCHEMA_RESET") != "1" {
		t.Fatal("set TOKENROUTER_TEST_ALLOW_SCHEMA_RESET=1 only for an isolated disposable database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	suffix := cryptoutil.BestEffortRandomAlphanumeric(12)
	scheduledType := "scheduler-db-int-" + suffix
	storedScheduledType := model.SystemTaskPeriodicTypePrefix + scheduledType
	t.Setenv("NODE_NAME", "scheduler-db-int-node")

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	var workersDone sync.WaitGroup
	var runs atomic.Int64
	ready.Add(workers)
	workersDone.Add(workers)
	for range workers {
		go func() {
			defer workersDone.Done()
			ready.Done()
			<-start
			errs <- RunScheduledWithLeaseContext(scheduledType, time.Hour, func(context.Context) error {
				runs.Add(1)
				return nil
			})
		}()
	}
	ready.Wait()
	close(start)
	workersDone.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), runs.Load(), "one cadence window must admit exactly one runner")

	var succeeded int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("type = ? AND status = ?", storedScheduledType, model.SystemTaskStatusSucceeded).
		Count(&succeeded).Error)
	assert.Equal(t, int64(1), succeeded)
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("type = ?", storedScheduledType).Count(&locks).Error)
	assert.Zero(t, locks, "terminal completion must release the execution lease")

	require.NoError(t, RunScheduledWithLeaseContext(scheduledType, time.Hour, func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.Equal(t, int64(1), runs.Load(), "a completed run must gate the next poll by durable cadence")

	databaseNow, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	aged := model.DB.Model(&model.SystemTask{}).
		Where("type = ? AND status = ?", storedScheduledType, model.SystemTaskStatusSucceeded).
		Update("updated_at", databaseNow-int64(time.Hour/time.Second)-1)
	require.NoError(t, aged.Error)
	require.EqualValues(t, 1, aged.RowsAffected)
	require.NoError(t, RunScheduledWithLeaseContext(scheduledType, time.Hour, func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.Equal(t, int64(2), runs.Load(), "an expired cadence window must admit one new run")

	leaseType := model.SystemTaskPeriodicTypePrefix + "lease-db-int-" + suffix
	claimed, won, err := model.CreateAndClaimScheduledSystemTask(leaseType, "external-runner-a", time.Hour)
	require.NoError(t, err)
	require.True(t, won)
	require.NotNil(t, claimed)
	_, won, err = model.CreateAndClaimScheduledSystemTask(leaseType, "external-runner-b", time.Hour)
	require.NoError(t, err)
	require.False(t, won, "a live execution lease must fence a competing runner")

	const progress = `{"phase":"external"}`
	require.NoError(t, model.UpdateSystemTaskState(claimed.TaskID, claimed.LockedBy, progress))
	require.NoError(t, model.UpdateSystemTaskState(claimed.TaskID, claimed.LockedBy, progress),
		"an identical state write must survive MySQL changed-row reporting")
	require.ErrorIs(t,
		model.UpdateSystemTaskState(claimed.TaskID, "external-runner-b", progress),
		model.ErrSystemTaskLockLost,
	)

	databaseNow, err = model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ? AND locked_by = ?", claimed.TaskID, claimed.LockedBy).
		Update("locked_until", databaseNow+2).Error)
	require.NoError(t, model.RenewSystemTaskLock(claimed.TaskID, claimed.LockedBy, databaseNow+60))
	var renewedLock model.SystemTaskLock
	require.NoError(t, model.DB.Where("task_id = ?", claimed.TaskID).First(&renewedLock).Error)
	assert.Greater(t, renewedLock.LockedUntil, databaseNow+2)

	require.NoError(t, model.CompleteSystemTask(claimed.TaskID, claimed.LockedBy, `{"ok":true}`))
	var stored model.SystemTask
	require.NoError(t, model.DB.Where("task_id = ?", claimed.TaskID).First(&stored).Error)
	assert.Equal(t, model.SystemTaskStatusSucceeded, stored.Status)
	assert.Nil(t, stored.ActiveKey)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", claimed.TaskID).Count(&locks).Error)
	assert.Zero(t, locks)
}
