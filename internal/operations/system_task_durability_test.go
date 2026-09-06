package operations

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func TestSystemTaskTransitionsRequireOwningRunner(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, map[string]any{"target": 1}, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-one")
	require.NoError(t, err)
	require.True(t, ok)

	require.ErrorIs(t, model.UpdateSystemTaskState(claimed.TaskID, "runner-two", `{"progress":50}`), model.ErrSystemTaskTransitionRejected)
	require.ErrorIs(t, model.CompleteSystemTask(claimed.TaskID, "runner-two", `{}`), model.ErrSystemTaskTransitionRejected)
	require.ErrorIs(t, model.FailSystemTask(claimed.TaskID, "runner-two", "wrong owner"), model.ErrSystemTaskTransitionRejected)

	require.NoError(t, model.UpdateSystemTaskState(claimed.TaskID, "runner-one", `{"progress":50}`))
	require.NoError(t, model.CompleteSystemTask(claimed.TaskID, "runner-one", `{"done":true}`))
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, claimed.ID).Error)
	assert.Equal(t, model.SystemTaskStatusSucceeded, stored.Status)
	assert.Nil(t, stored.ActiveKey)
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Where("task_id = ?", claimed.TaskID).Count(&locks).Error)
	assert.Zero(t, locks, "terminal transition must release the per-type lease")
}

func TestSystemTaskIdempotentStateWriteAcceptsMySQLChangedRowsSemantics(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-idempotent")
	require.NoError(t, err)
	require.True(t, ok)

	const state = `{"progress":50}`
	const callback = "test:simulate_mysql_changed_rows"
	require.NoError(t, model.DB.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		updates, isMap := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == (model.SystemTask{}).TableName() && isMap && updates["state"] == state {
			tx.RowsAffected = 0
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	require.NoError(t, model.UpdateSystemTaskState(claimed.TaskID, claimed.LockedBy, state))
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, claimed.ID).Error)
	assert.Equal(t, state, stored.State)
}

func TestLogCleanupRejectsFutureTargetsBeforeEnqueueOrDeletion(t *testing.T) {
	initTaskDB(t)
	future := wallclock.NowTimestamp() + 3600
	_, err := StartLogCleanupTask(future)
	require.EqualError(t, err, "target timestamp cannot be in the future")
	var tasks int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Count(&tasks).Error)
	assert.Zero(t, tasks)

	payload, err := jsonutil.Marshal(logCleanupPayload{TargetTimestamp: future, BatchSize: logCleanupBatchSize})
	require.NoError(t, err)
	_, err = runLogCleanupTask(context.Background(), &model.SystemTask{Payload: string(payload)})
	require.EqualError(t, err, "target timestamp cannot be in the future")
}

func TestLogCleanupRejectsOversizedPersistedBatchBeforeDeletion(t *testing.T) {
	initTaskDB(t)
	payload, err := jsonutil.Marshal(logCleanupPayload{
		TargetTimestamp: wallclock.NowTimestamp() - 1,
		BatchSize:       maxLogCleanupBatchSize + 1,
	})
	require.NoError(t, err)
	_, err = runLogCleanupTask(context.Background(), &model.SystemTask{Payload: string(payload)})
	require.EqualError(t, err, "log cleanup batch size exceeds maximum")
}

func TestRunPendingSystemTasksOnceReturnsTerminalWriteFailure(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, logCleanupPayload{TargetTimestamp: 1}, nil)
	require.NoError(t, err)

	original := systemTaskHandlers[model.SystemTaskTypeLogCleanup]
	systemTaskHandlers[model.SystemTaskTypeLogCleanup] = func(context.Context, *model.SystemTask) (string, error) {
		return `{}`, nil
	}
	t.Cleanup(func() { systemTaskHandlers[model.SystemTaskTypeLogCleanup] = original })

	const callback = "test:fail_system_task_terminal_write"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == (model.SystemTask{}).TableName() && ok && updates["status"] == model.SystemTaskStatusSucceeded {
			tx.AddError(errors.New("injected terminal write failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	err = RunPendingSystemTasksOnce()
	require.ErrorContains(t, err, "injected terminal write failure")
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stored.Status)
}

func TestSystemTaskHandlerPanicIsFailedWithoutLeakingValue(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, logCleanupPayload{TargetTimestamp: 1}, nil)
	require.NoError(t, err)

	original := systemTaskHandlers[model.SystemTaskTypeLogCleanup]
	const panicSecret = "sk-live-system-task-panic-secret"
	systemTaskHandlers[model.SystemTaskTypeLogCleanup] = func(context.Context, *model.SystemTask) (string, error) {
		panic(panicSecret)
	}
	t.Cleanup(func() { systemTaskHandlers[model.SystemTaskTypeLogCleanup] = original })

	err = RunPendingSystemTasksOnce()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic (string)")
	assert.NotContains(t, err.Error(), panicSecret)

	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, stored.Status)
	assert.Contains(t, stored.Error, "panic (string)")
	assert.NotContains(t, stored.Error, panicSecret)
}

func TestConcurrentSystemTaskClaimHasSingleWinnerAndLock(t *testing.T) {
	initTaskDB(t)
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	// Serialize SQLite connections so the test exercises the database CAS
	// deterministically instead of SQLite's single-writer scheduling.
	sqlDB.SetMaxOpenConns(1)

	task, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	const runners = 12
	start := make(chan struct{})
	type claimResult struct {
		claimed bool
		err     error
	}
	results := make(chan claimResult, runners)
	var group sync.WaitGroup
	for i := 0; i < runners; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			_, claimed, claimErr := model.ClaimSystemTask(task.ID, task.Type, fmt.Sprintf("runner-%d", i))
			results <- claimResult{claimed: claimed, err: claimErr}
		}(i)
	}
	close(start)
	group.Wait()
	close(results)

	winners := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.claimed {
			winners++
		}
	}
	assert.Equal(t, 1, winners)
	var locks []model.SystemTaskLock
	require.NoError(t, model.DB.Find(&locks).Error)
	require.Len(t, locks, 1)
	assert.Equal(t, task.TaskID, locks[0].TaskID)
}

func TestScheduledClaimVerifiesPersistedFenceWhenDuplicateReportsAffectedRow(t *testing.T) {
	initTaskDB(t)
	const taskType = model.SystemTaskPeriodicTypePrefix + "mysql-found-rows"
	now := wallclock.NowTimestamp()
	existing := model.SystemTaskLock{
		Type: taskType, TaskID: "existing-task", LockedBy: "existing-runner",
		LockedUntil: now + 60, UpdatedAt: now,
	}
	require.NoError(t, model.DB.Create(&existing).Error)

	const callback = "test:simulate_mysql_client_found_rows"
	require.NoError(t, model.DB.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTaskLock{}).TableName() && tx.RowsAffected == 0 {
			tx.RowsAffected = 1
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Create().Remove(callback) })

	claimed, won, err := model.CreateAndClaimScheduledSystemTask(taskType, "competing-runner", time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	assert.Nil(t, claimed)
	var stored model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", taskType).First(&stored).Error)
	assert.Equal(t, existing.TaskID, stored.TaskID)
	assert.Equal(t, existing.LockedBy, stored.LockedBy)
}

func TestSystemTaskCleanupFailsRunningTaskWhoseLeaseRowIsMissing(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-orphaned")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, model.DB.Where("task_id = ?", claimed.TaskID).Delete(&model.SystemTaskLock{}).Error)

	require.NoError(t, model.ExpireStaleSystemTaskLockType(task.Type))
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, stored.Status)
	assert.Equal(t, "task lease missing", stored.Error)
	assert.Nil(t, stored.ActiveKey)
}

func TestSystemTaskCleanupLeavesLegacyGenericRunningRowsWithoutActiveKeyAlone(t *testing.T) {
	initTaskDB(t)
	legacy := model.SystemTask{
		TaskID: "legacy-generic-running", Type: TaskTypeCleanupLogs,
		Status: model.SystemTaskStatusRunning, LockedBy: "legacy-runner",
	}
	require.NoError(t, model.DB.Create(&legacy).Error)
	require.NoError(t, model.ExpireStaleSystemTaskLocks())

	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, legacy.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stored.Status)
}

func TestStaleSystemTaskLeaseTakeoverFailsPreviousRunAtomically(t *testing.T) {
	initTaskDB(t)
	first, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := model.ClaimSystemTask(first.ID, first.Type, "runner-old")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", first.TaskID).
		Update("locked_until", wallclock.NowTimestamp()-1).Error)

	secondID, err := model.GenerateSystemTaskID()
	require.NoError(t, err)
	second := model.SystemTask{TaskID: secondID, Type: first.Type, Status: model.SystemTaskStatusPending}
	require.NoError(t, model.DB.Create(&second).Error)

	claimedTask, won, err := model.ClaimSystemTask(second.ID, second.Type, "runner-new")
	require.NoError(t, err)
	require.True(t, won)
	assert.Equal(t, second.TaskID, claimedTask.TaskID)

	var oldStored model.SystemTask
	require.NoError(t, model.DB.First(&oldStored, first.ID).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, oldStored.Status)
	assert.Equal(t, "task lease expired", oldStored.Error)
	assert.Nil(t, oldStored.ActiveKey)

	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", first.Type).First(&lock).Error)
	assert.Equal(t, second.TaskID, lock.TaskID)
	assert.Equal(t, "runner-new", lock.LockedBy)
}

func TestStaleSystemTaskCleanupUsesLockExpiryNotTaskTimestamp(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-crashed")
	require.NoError(t, err)
	require.True(t, ok)

	// An old progress timestamp must not expire a task while its lock is live.
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("id = ?", task.ID).Update("updated_at", int64(1)).Error)
	require.NoError(t, model.ExpireStaleSystemTaskLocks())
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stored.Status)

	// Conversely, an expired persisted lock is authoritative even when the
	// task row itself was updated recently.
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("id = ?", task.ID).Update("updated_at", wallclock.NowTimestamp()).Error)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", claimed.TaskID).
		Update("locked_until", wallclock.NowTimestamp()-1).Error)
	require.NoError(t, model.ExpireStaleSystemTaskLocks())
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, stored.Status)
	assert.Equal(t, "task lease expired", stored.Error)
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&locks).Error)
	assert.Zero(t, locks)
}

func TestStaleSystemTaskTakeoverFailureRollsBackPreviousLease(t *testing.T) {
	initTaskDB(t)
	first, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := model.ClaimSystemTask(first.ID, first.Type, "runner-old")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", first.TaskID).
		Update("locked_until", wallclock.NowTimestamp()-1).Error)

	secondID, err := model.GenerateSystemTaskID()
	require.NoError(t, err)
	second := model.SystemTask{TaskID: secondID, Type: first.Type, Status: model.SystemTaskStatusPending}
	require.NoError(t, model.DB.Create(&second).Error)

	const callback = "test:fail_stale_system_task_takeover_claim"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == (model.SystemTask{}).TableName() && ok && updates["status"] == model.SystemTaskStatusRunning {
			tx.AddError(errors.New("injected takeover claim failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	_, won, err := model.ClaimSystemTask(second.ID, second.Type, "runner-new")
	require.ErrorContains(t, err, "injected takeover claim failure")
	assert.False(t, won)

	var oldStored model.SystemTask
	require.NoError(t, model.DB.First(&oldStored, first.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, oldStored.Status)
	var newStored model.SystemTask
	require.NoError(t, model.DB.First(&newStored, second.ID).Error)
	assert.Equal(t, model.SystemTaskStatusPending, newStored.Status)
	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", first.Type).First(&lock).Error)
	assert.Equal(t, first.TaskID, lock.TaskID)
	assert.Equal(t, "runner-old", lock.LockedBy)
}

func TestSystemTaskLeaseRejectsWrongOwnerAndExpiredOwner(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-owner")
	require.NoError(t, err)
	require.True(t, ok)

	require.ErrorIs(t, model.ReleaseSystemTaskLock(claimed.TaskID, "runner-other"), model.ErrSystemTaskLockLost)
	require.ErrorIs(t, model.RenewSystemTaskLock(claimed.TaskID, "runner-other", wallclock.NowTimestamp()+600), model.ErrSystemTaskLockLost)

	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", claimed.TaskID).
		Update("locked_until", wallclock.NowTimestamp()-1).Error)
	require.ErrorIs(t, model.UpdateSystemTaskState(claimed.TaskID, claimed.LockedBy, `{"progress":10}`), model.ErrSystemTaskLockLost)
	require.ErrorIs(t, model.CompleteSystemTask(claimed.TaskID, claimed.LockedBy, `{}`), model.ErrSystemTaskLockLost)
}

func TestSystemTaskHeartbeatRenewsLease(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-heartbeat")
	require.NoError(t, err)
	require.True(t, ok)

	oldUntil := wallclock.NowTimestamp() + 2
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", claimed.TaskID).
		Update("locked_until", oldUntil).Error)

	heartbeats := make(chan time.Time)
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	renewed := make(chan struct{})
	type executionResult struct {
		result   string
		runErr   error
		leaseErr error
	}
	executionDone := make(chan executionResult, 1)
	handler := func(ctx context.Context, _ *model.SystemTask) (string, error) {
		close(handlerStarted)
		select {
		case <-releaseHandler:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	renew := func(ctx context.Context, taskID, runnerID string, until int64) error {
		err := model.RenewSystemTaskLockContext(ctx, taskID, runnerID, until)
		close(renewed)
		return err
	}
	go func() {
		result, runErr, leaseErr := runSystemTaskHandlerWithHeartbeatChannel(claimed, handler, heartbeats, renew)
		executionDone <- executionResult{result: result, runErr: runErr, leaseErr: leaseErr}
	}()
	<-handlerStarted
	heartbeats <- time.Now()
	<-renewed

	var lock model.SystemTaskLock
	require.NoError(t, model.DB.Where("task_id = ?", claimed.TaskID).First(&lock).Error)
	assert.Greater(t, lock.LockedUntil, oldUntil)
	close(releaseHandler)
	outcome := <-executionDone
	assert.Equal(t, "ok", outcome.result)
	require.NoError(t, outcome.runErr)
	require.NoError(t, outcome.leaseErr)
}

func TestSystemTaskHeartbeatCancelsHandlerOnLockLoss(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-heartbeat")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", claimed.TaskID).
		Update("locked_by", "runner-takeover").Error)

	heartbeats := make(chan time.Time)
	handlerStarted := make(chan struct{})
	handlerStopped := make(chan struct{})
	executionDone := make(chan struct {
		runErr   error
		leaseErr error
	}, 1)
	handler := func(ctx context.Context, _ *model.SystemTask) (string, error) {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerStopped)
		return "", ctx.Err()
	}
	go func() {
		_, runErr, leaseErr := runSystemTaskHandlerWithHeartbeatChannel(claimed, handler, heartbeats, model.RenewSystemTaskLockContext)
		executionDone <- struct {
			runErr   error
			leaseErr error
		}{runErr: runErr, leaseErr: leaseErr}
	}()
	<-handlerStarted
	heartbeats <- time.Now()
	outcome := <-executionDone
	<-handlerStopped
	assert.ErrorIs(t, outcome.runErr, context.Canceled)
	assert.ErrorIs(t, outcome.leaseErr, model.ErrSystemTaskLockLost)

	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stored.Status, "a former owner must not finish the task")
}

func TestSystemTaskHeartbeatDoesNotWaitForeverForBrokenHandler(t *testing.T) {
	heartbeats := make(chan time.Time, 1)
	heartbeats <- time.Now()
	release := make(chan struct{})
	task := &model.SystemTask{TaskID: "stuck-task", LockedBy: "stale-runner"}
	startedAt := time.Now()
	_, runErr, leaseErr := runSystemTaskHandlerWithHeartbeatChannelAndGrace(
		task,
		func(context.Context, *model.SystemTask) (string, error) {
			<-release // deliberately violates the handler cancellation contract
			return "", nil
		},
		heartbeats,
		func(context.Context, string, string, int64) error { return model.ErrSystemTaskLockLost },
		20*time.Millisecond,
	)
	close(release)
	assert.ErrorIs(t, runErr, context.Canceled)
	assert.ErrorIs(t, leaseErr, model.ErrSystemTaskLockLost)
	assert.Less(t, time.Since(startedAt), time.Second)
}

func TestSystemTaskHeartbeatBoundsBlockedRenewalAndCancelsHandler(t *testing.T) {
	heartbeats := make(chan time.Time, 1)
	heartbeats <- time.Now()
	handlerCanceled := make(chan struct{})
	renewStarted := make(chan struct{})
	releaseRenew := make(chan struct{})
	task := &model.SystemTask{TaskID: "blocked-renewal", LockedBy: "runner-blocked"}

	startedAt := time.Now()
	_, runErr, leaseErr := runSystemTaskHandlerWithHeartbeatChannelAndBounds(
		task,
		func(ctx context.Context, _ *model.SystemTask) (string, error) {
			<-ctx.Done()
			close(handlerCanceled)
			return "", ctx.Err()
		},
		heartbeats,
		func(context.Context, string, string, int64) error {
			close(renewStarted)
			<-releaseRenew // deliberately simulates a driver that ignores cancellation
			return nil
		},
		20*time.Millisecond,
		100*time.Millisecond,
	)
	<-renewStarted
	close(releaseRenew)
	<-handlerCanceled
	assert.ErrorIs(t, runErr, context.Canceled)
	assert.ErrorIs(t, leaseErr, context.DeadlineExceeded)
	assert.Less(t, time.Since(startedAt), time.Second)
}

func TestFailSystemTaskReleasesLease(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-failure")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, model.FailSystemTask(claimed.TaskID, claimed.LockedBy, "expected failure"))

	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusFailed, stored.Status)
	assert.Equal(t, "expected failure", stored.Error)
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&locks).Error)
	assert.Zero(t, locks)
}

func TestTerminalLeaseReleaseFailureRollsBackTaskState(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-owner")
	require.NoError(t, err)
	require.True(t, ok)

	const callback = "test:fail_system_task_lock_release"
	require.NoError(t, model.DB.Callback().Delete().Before("gorm:delete").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.SystemTaskLock{}).TableName() {
			tx.AddError(errors.New("injected lock release failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Delete().Remove(callback) })

	err = model.CompleteSystemTask(claimed.TaskID, claimed.LockedBy, `{}`)
	require.ErrorContains(t, err, "injected lock release failure")
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stored.Status, "terminal update must roll back with lock release")
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&locks).Error)
	assert.Equal(t, int64(1), locks)
}

func TestStaleLeaseCleanupFailureRollsBackLockDeletion(t *testing.T) {
	initTaskDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner-crashed")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).
		Where("task_id = ?", claimed.TaskID).
		Update("locked_until", wallclock.NowTimestamp()-1).Error)

	const callback = "test:fail_expired_system_task_status"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == (model.SystemTask{}).TableName() && ok && updates["error"] == "task lease expired" {
			tx.AddError(errors.New("injected stale status failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callback) })

	err = model.ExpireStaleSystemTaskLocks()
	require.ErrorContains(t, err, "injected stale status failure")
	var stored model.SystemTask
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.SystemTaskStatusRunning, stored.Status)
	var locks int64
	require.NoError(t, model.DB.Model(&model.SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&locks).Error)
	assert.Equal(t, int64(1), locks, "expired lock deletion must roll back when task update fails")
}

func TestSystemTaskCleanupLeavesGenericRunWithLeaseLockAlone(t *testing.T) {
	initTaskDB(t)
	lock := model.SystemTaskLock{
		Type:        TaskTypeCleanupLogs,
		LockedBy:    "generic-runner",
		LockedUntil: wallclock.NowTimestamp() - 1,
		UpdatedAt:   wallclock.NowTimestamp() - 10,
	}
	require.NoError(t, model.DB.Create(&lock).Error)
	require.NoError(t, model.ExpireStaleSystemTaskLocks())

	var stored model.SystemTaskLock
	require.NoError(t, model.DB.Where("type = ?", lock.Type).First(&stored).Error)
	assert.Empty(t, stored.TaskID)
	assert.Equal(t, "generic-runner", stored.LockedBy)
}

func TestChannelTestTaskNotifyFlagDeliversRootCompletion(t *testing.T) {
	initTaskDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.User{}))
	require.NoError(t, model.DB.Create(&model.User{
		Username: "notification-root", Password: "password", DisplayName: "Root",
		Role: roles.RoleRootUser, Status: model.UserStatusEnabled,
		Email: "root@example.test", EmailVerified: true,
	}).Error)
	mailer := &testutil.RecordingMailer{}
	previousMailer := mailtransport.Mail
	mailtransport.Mail = mailer
	t.Cleanup(func() { mailtransport.Mail = previousMailer })

	result, err := runChannelTestTask(context.Background(), &model.SystemTask{Payload: `{"notify":true}`})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tested":0,"succeeded":0,"failed":0,"disabled":0,"enabled":0}`, result)
	assert.Equal(t, []string{"root@example.test"}, mailer.To)
	assert.Equal(t, []string{"通道测试完成"}, mailer.Subject)
	result, err = runChannelTestTask(context.Background(), &model.SystemTask{Payload: `{"notify":false}`})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tested":0,"succeeded":0,"failed":0,"disabled":0,"enabled":0}`, result)
	assert.Len(t, mailer.To, 1, "notify=false must not deliver another message")
}

func TestChannelTestTaskRejectsMalformedOrUnknownModeWithoutProbing(t *testing.T) {
	initTaskDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	channel := model.Channel{
		Name: "must-not-probe", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "secret",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://invalid.example.test", TestModel: "gpt-4",
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	for _, payload := range []string{
		`{"mode":"unknown","notify":false}`,
		`{"mode":"scheduled_all","mode":"passive_recovery"}`,
		`{"mode":`,
	} {
		_, err := runChannelTestTask(context.Background(), &model.SystemTask{Payload: payload})
		require.Error(t, err, payload)
		var stored model.Channel
		require.NoError(t, model.DB.First(&stored, channel.Id).Error)
		assert.Zero(t, stored.TestTime, "invalid task input must have no probe or persistence side effect")
	}
}
