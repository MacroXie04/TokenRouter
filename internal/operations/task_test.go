package operations

import (
	"context"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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
	now := wallclock.NowTimestamp()
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

func TestAcquireTaskLockReturnsDatabaseFailures(t *testing.T) {
	initTaskDB(t)
	callbackName := "test:fail_task_lease_query"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTaskLock{}).TableName() {
			tx.AddError(errors.New("injected lease query failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	acquired, err := AcquireTaskLock("query-failure", "node", time.Minute)
	assert.False(t, acquired)
	require.ErrorContains(t, err, "injected lease query failure")
}

func TestAcquireTaskLockDoesNotMisclassifyCreateFailureAsContention(t *testing.T) {
	initTaskDB(t)
	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_task_lease_create"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTaskLock{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected lease create failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	acquired, err := AcquireTaskLock("create-failure", "node", time.Minute)
	assert.False(t, acquired)
	require.ErrorContains(t, err, "injected lease create failure")
	acquired, err = AcquireTaskLock("create-failure", "node", time.Minute)
	require.NoError(t, err)
	assert.True(t, acquired, "a transient database failure must remain retryable")
}

func TestRunWithLeaseCreationFailureSkipsTaskAndReleasesLease(t *testing.T) {
	initTaskDB(t)
	var fail atomic.Bool
	fail.Store(true)
	callbackName := "test:fail_periodic_task_create"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTask{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected task row failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callbackName) })

	var ran atomic.Bool
	err := RunWithLease("create-row-failure", time.Minute, func() error {
		ran.Store(true)
		return nil
	})
	require.ErrorContains(t, err, "injected task row failure")
	assert.False(t, ran.Load())
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Count(&locks).Error)
	assert.Zero(t, locks)

	err = RunWithLease("create-row-failure", time.Minute, func() error {
		ran.Store(true)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, ran.Load(), "the released lease permits a clean retry")
	var task model.SystemTask
	require.NoError(t, model.DB.Where("type = ?", "create-row-failure").First(&task).Error)
	assert.Equal(t, model.SystemTaskStatusSucceeded, task.Status)
}

func TestRunWithLeaseStatusFailureNeverClaimsSuccess(t *testing.T) {
	initTaskDB(t)
	callbackName := "test:fail_periodic_task_status"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTask{}).TableName() {
			tx.AddError(errors.New("injected task status failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	err := RunWithLease("status-failure", time.Minute, func() error { return nil })
	require.ErrorContains(t, err, "injected task status failure")
	var task model.SystemTask
	require.NoError(t, model.DB.Where("type = ?", "status-failure").First(&task).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, task.Status)
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Count(&locks).Error)
	assert.Zero(t, locks)
}

func TestRunScheduledWithLeaseUsesLatestTerminalCompletionAsCadenceGate(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "scheduled-cadence"

	var runs atomic.Int64
	run := func(context.Context) error {
		runs.Add(1)
		return nil
	}
	require.NoError(t, RunScheduledWithLeaseContext("scheduled-cadence", time.Hour, run))
	require.NoError(t, RunScheduledWithLeaseContext("scheduled-cadence", time.Hour, run))
	assert.Equal(t, int64(1), runs.Load(), "a scheduler poll inside the interval must be skipped")

	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("type = ?", storedType).Count(&locks).Error)
	assert.Zero(t, locks, "the crash-detection lease must not double as the schedule interval")
	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("type = ? AND status = ?", storedType, model.SystemTaskStatusSucceeded).
		Update("updated_at", wallclock.NowTimestamp()-int64(time.Hour/time.Second)-1).Error)

	require.NoError(t, RunScheduledWithLeaseContext("scheduled-cadence", time.Hour, run))
	assert.Equal(t, int64(2), runs.Load(), "an expired cadence gate must permit the next run")
	var tasks int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("type = ? AND status = ?", storedType, model.SystemTaskStatusSucceeded).
		Count(&tasks).Error)
	assert.Equal(t, int64(2), tasks)
}

func TestConcurrentScheduledAdmissionRunsOnlyOncePerCadence(t *testing.T) {
	initTaskDB(t)
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	// A single SQLite writer makes the test deterministic while still forcing
	// every competing scheduler call through the transactional admission gate.
	sqlDB.SetMaxOpenConns(1)

	const workers = 24
	start := make(chan struct{})
	errs := make(chan error, workers)
	var runs atomic.Int64
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errs <- RunScheduledWithLeaseContext("atomic-cadence", time.Hour, func(context.Context) error {
				runs.Add(1)
				return nil
			})
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for runErr := range errs {
		require.NoError(t, runErr)
	}
	assert.Equal(t, int64(1), runs.Load())

	var succeeded int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("type = ? AND status = ?", model.SystemTaskPeriodicTypePrefix+"atomic-cadence", model.SystemTaskStatusSucceeded).
		Count(&succeeded).Error)
	assert.Equal(t, int64(1), succeeded)
}

func TestScheduledAdmissionFailureRollsBackLease(t *testing.T) {
	initTaskDB(t)
	var fail atomic.Bool
	fail.Store(true)
	const callback = "test:fail_scheduled_task_admission"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTask{}).TableName() && fail.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected scheduled task create failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callback) })

	err := RunScheduledWithLeaseContext("admission-rollback", time.Hour, func(context.Context) error {
		t.Fatal("a job with a failed durable admission must not run")
		return nil
	})
	require.ErrorContains(t, err, "injected scheduled task create failure")
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Count(&locks).Error)
	assert.Zero(t, locks)

	var runs atomic.Int64
	require.NoError(t, RunScheduledWithLeaseContext("admission-rollback", time.Hour, func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.Equal(t, int64(1), runs.Load())
}

func TestRunScheduledWithLeaseUsesShortCrashDetectionLease(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "scheduled-short-lease"

	started := make(chan struct{})
	finish := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- RunScheduledWithLeaseContext("scheduled-short-lease", 24*time.Hour, func(context.Context) error {
			close(started)
			<-finish
			return nil
		})
	}()
	<-started

	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", storedType).First(&lock).Error)
	remaining := lock.LockedUntil - wallclock.NowTimestamp()
	assert.Greater(t, remaining, int64(0))
	assert.LessOrEqual(t, remaining, int64(model.SystemTaskLeaseDuration/time.Second),
		"a long schedule interval must not create a long crash-recovery window")
	close(finish)
	require.NoError(t, <-result)
}

func TestRunScheduledWithLeaseBacksOffAfterFailure(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "scheduled-retry"

	var runs atomic.Int64
	err := RunScheduledWithLeaseContext("scheduled-retry", time.Hour, func(context.Context) error {
		runs.Add(1)
		return errors.New("temporary scheduled failure")
	})
	require.ErrorContains(t, err, "temporary scheduled failure")
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("type = ?", storedType).Count(&locks).Error)
	assert.Zero(t, locks)

	require.NoError(t, RunScheduledWithLeaseContext("scheduled-retry", time.Hour, func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.Equal(t, int64(1), runs.Load(), "a failed job must not retry on every scheduler poll")
	assert.Less(t, model.SystemTaskFailureRetryInterval, time.Hour)

	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("type = ? AND status = ?", storedType, model.SystemTaskStatusFailed).
		Update("updated_at", wallclock.NowTimestamp()-int64(model.SystemTaskFailureRetryInterval/time.Second)-1).Error)
	require.NoError(t, RunScheduledWithLeaseContext("scheduled-retry", time.Hour, func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.Equal(t, int64(2), runs.Load(), "a failed run becomes retryable after the bounded failure backoff")
}

func TestRunScheduledWithLeaseRecoversPendingTaskAfterSchedulerCrash(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "pending-recovery"

	pending, err := model.CreateSystemTask(storedType, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, model.SystemTaskStatusPending, pending.Status)

	var runs atomic.Int64
	require.NoError(t, RunScheduledWithLeaseContext("pending-recovery", time.Hour, func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	assert.Equal(t, int64(1), runs.Load())

	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, pending.ID).Error)
	assert.Equal(t, model.SystemTaskStatusSucceeded, stored.Status)
	assert.Nil(t, stored.ActiveKey)
}

func TestRunScheduledWithLeaseFencesStaleRunnerCompletion(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "stale-finalization"

	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- RunScheduledWithLeaseContext("stale-finalization", time.Hour, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", storedType).First(&lock).Error)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("type = ? AND task_id = ?", storedType, lock.TaskID).
		Updates(map[string]any{
			"locked_by":    "replacement-owner",
			"locked_until": wallclock.NowTimestamp() + 60,
		}).Error)
	close(release)
	require.ErrorIs(t, <-result, model.ErrSystemTaskLockLost)

	var stale model.SystemTask
	require.NoError(t, model.DB.Where("task_id = ?", lock.TaskID).First(&stale).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stale.Status,
		"a stale runner must never publish a successful completion")

	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", lock.TaskID).
		Update("locked_until", wallclock.NowTimestamp()-1).Error)
	require.NoError(t, model.ExpireStaleSystemTaskLocks())
	require.NoError(t, model.DB.First(&stale, stale.ID).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, stale.Status)

	var replacementRuns atomic.Int64
	require.NoError(t, RunScheduledWithLeaseContext("stale-finalization", time.Hour, func(context.Context) error {
		replacementRuns.Add(1)
		return nil
	}))
	assert.Zero(t, replacementRuns.Load(), "lease-expiry failure observes the normal retry cadence")
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("id = ?", stale.ID).
		Update("updated_at", wallclock.NowTimestamp()-int64(time.Hour/time.Second)-1).Error)
	require.NoError(t, RunScheduledWithLeaseContext("stale-finalization", time.Hour, func(context.Context) error {
		replacementRuns.Add(1)
		return nil
	}))
	assert.Equal(t, int64(1), replacementRuns.Load())
}

func TestPeriodicSystemTaskHistoryIsBoundedAndHiddenFromAdminLists(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "retention"

	operatorTask, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	for i := 0; i < periodicSystemTaskHistoryLimit+8; i++ {
		task, createErr := model.CreateSystemTask(storedType, nil, nil)
		require.NoError(t, createErr)
		claimed, won, claimErr := model.ClaimSystemTask(task.ID, task.Type, "retention-runner")
		require.NoError(t, claimErr)
		require.True(t, won)
		require.NoError(t, model.CompleteSystemTask(claimed.TaskID, claimed.LockedBy, ""))
	}

	deleted, err := model.PrunePeriodicSystemTaskHistory(storedType, periodicSystemTaskHistoryLimit)
	require.NoError(t, err)
	assert.Equal(t, int64(8), deleted)
	var retained int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("type = ?", storedType).Count(&retained).Error)
	assert.Equal(t, int64(periodicSystemTaskHistoryLimit), retained)

	listed, err := model.ListSystemTasks(100)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, operatorTask.TaskID, listed[0].TaskID)
	paginated, total, err := model.GetPaginatedSystemTasks("", 0, 100)
	require.NoError(t, err)
	require.Len(t, paginated, 1)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, operatorTask.TaskID, paginated[0].TaskID)

	_, err = model.PrunePeriodicSystemTaskHistory(model.SystemTaskTypeLogCleanup, 1)
	require.Error(t, err, "the retention helper must fail closed for operator-visible task types")
}

func TestLatestPeriodicFailureIsVisibleUntilSuccessfulRecovery(t *testing.T) {
	initTaskDB(t)
	const storedType = model.SystemTaskPeriodicTypePrefix + "scheduler-health"

	first, err := model.CreateSystemTask(storedType, nil, nil)
	require.NoError(t, err)
	first, won, err := model.ClaimSystemTask(first.ID, first.Type, "health-runner-1")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, model.FailSystemTask(first.TaskID, first.LockedBy, "first bounded failure"))

	latest, err := model.CreateSystemTask(storedType, nil, nil)
	require.NoError(t, err)
	latest, won, err = model.ClaimSystemTask(latest.ID, latest.Type, "health-runner-2")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, model.FailSystemTask(latest.TaskID, latest.LockedBy, "latest bounded failure"))

	listed, err := model.ListSystemTasks(100)
	require.NoError(t, err)
	require.Len(t, listed, 1, "only the newest failure for one periodic type should be visible")
	assert.Equal(t, latest.TaskID, listed[0].TaskID)
	assert.Equal(t, "latest bounded failure", listed[0].Error)
	paginated, total, err := model.GetPaginatedSystemTasks("", 0, 100)
	require.NoError(t, err)
	require.Len(t, paginated, 1)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, latest.TaskID, paginated[0].TaskID)

	recovered, err := model.CreateSystemTask(storedType, nil, nil)
	require.NoError(t, err)
	recovered, won, err = model.ClaimSystemTask(recovered.ID, recovered.Type, "health-runner-3")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, model.CompleteSystemTask(recovered.TaskID, recovered.LockedBy, ""))

	listed, err = model.ListSystemTasks(100)
	require.NoError(t, err)
	assert.Empty(t, listed, "a later successful run clears the scheduler-health failure")
}

func TestRunScheduledWithLeaseBoundsAndRedactsPersistedFailure(t *testing.T) {
	initTaskDB(t)
	const secret = "scheduler-secret-value"
	runErr := errors.New("password=" + secret + " " + strings.Repeat("界", 600))

	err := RunScheduledWithLeaseContext("safe-error", time.Hour, func(context.Context) error {
		return runErr
	})
	require.ErrorIs(t, err, runErr)
	var task model.SystemTask
	require.NoError(t, model.DB.Where("type = ?", model.SystemTaskPeriodicTypePrefix+"safe-error").First(&task).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, task.Status)
	assert.NotContains(t, task.Error, secret)
	assert.Contains(t, task.Error, "[REDACTED]")
	assert.LessOrEqual(t, len(task.Error), 1024)
	assert.True(t, utf8.ValidString(task.Error))
}

func TestRunWithLeaseReleaseFailureMarksTaskFailed(t *testing.T) {
	initTaskDB(t)
	callbackName := "test:fail_periodic_task_release"
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTaskLock{}).TableName() {
			tx.AddError(errors.New("injected lease release failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Delete().Remove(callbackName) })

	err := RunWithLease("release-failure", time.Minute, func() error { return nil })
	require.ErrorContains(t, err, "injected lease release failure")
	var task model.SystemTask
	require.NoError(t, model.DB.Where("type = ?", "release-failure").First(&task).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, task.Status)
	assert.Contains(t, task.Error, "release task lease")
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Count(&locks).Error)
	assert.Equal(t, int64(1), locks, "the failed release must remain visible for expiry/takeover")
}

func TestRunWithLeaseNeverPersistsRawPanicValue(t *testing.T) {
	initTaskDB(t)
	const panicSecret = "sk-live-panic-secret-must-not-persist"

	err := RunWithLease("panic-redaction", time.Minute, func() error {
		panic(panicSecret)
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), panicSecret)
	assert.Contains(t, err.Error(), "panic (string)")

	var task model.SystemTask
	require.NoError(t, model.DB.Where("type = ?", "panic-redaction").First(&task).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, task.Status)
	assert.NotContains(t, task.Error, panicSecret)
	assert.Contains(t, task.Error, "panic (string)")
}

func TestTaskLeaseTakeoverFencesRenewalAndStaleRelease(t *testing.T) {
	initTaskDB(t)
	now := wallclock.NowTimestamp()
	require.NoError(t, model.DB.Create(&model.SystemTaskLock{
		Type: "fenced-takeover", LockedBy: "old-run-token", LockedUntil: now - 1, UpdatedAt: now - 2,
	}).Error)

	acquired, err := AcquireTaskLock("fenced-takeover", "new-run-token", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.ErrorIs(t, RenewTaskLock("fenced-takeover", "old-run-token", time.Minute), ErrTaskLeaseLost)
	require.ErrorIs(t, ReleaseTaskLock("fenced-takeover", "old-run-token"), ErrTaskLeaseLost)
	require.NoError(t, RenewTaskLock("fenced-takeover", "new-run-token", time.Minute))

	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", "fenced-takeover").First(&lock).Error)
	assert.Equal(t, "new-run-token", lock.LockedBy)
}

func TestRunWithLeaseContextHeartbeatsAndCancelsOnFenceLoss(t *testing.T) {
	initTaskDB(t)
	t.Setenv("NODE_NAME", "shared-node-name")
	started := make(chan struct{})
	stopped := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- RunWithLeaseContext("heartbeat-fencing", 3*time.Second, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		})
	}()
	<-started

	var original model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", "heartbeat-fencing").First(&original).Error)
	assert.Contains(t, original.LockedBy, "shared-node-name:")
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("type = ? AND locked_by = ?", original.Type, original.LockedBy).
		Updates(map[string]any{
			"locked_by": "takeover-fence-token", "locked_until": wallclock.NowTimestamp() + 60,
		}).Error)

	select {
	case <-stopped:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("lease-loss heartbeat did not cancel the running task")
	}
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrTaskLeaseLost)
	case <-time.After(time.Second):
		t.Fatal("leased task did not report fence loss")
	}
	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", "heartbeat-fencing").First(&lock).Error)
	assert.Equal(t, "takeover-fence-token", lock.LockedBy,
		"stale cleanup must not delete a newer owner's lease")
}
