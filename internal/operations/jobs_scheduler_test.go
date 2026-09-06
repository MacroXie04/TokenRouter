package operations

import (
	"context"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeriodicJobsSchedulesRelayQuotaReconciliationUnderLease(t *testing.T) {
	t.Setenv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", "true")
	t.Setenv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", "30")
	t.Setenv("CHANNEL_UPDATE_FREQUENCY", "30")
	t.Setenv("CHANNEL_TEST_ENABLED", "true")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, setting.Init())
	jimengTaskReconciler.Lock()
	previousPromoter := jimengTaskReconciler.promote
	jimengTaskReconciler.promote = nil
	jimengTaskReconciler.Unlock()
	asyncTaskReconciler.Lock()
	previousAsyncPromoters := asyncTaskReconciler.promotes
	asyncTaskReconciler.promotes = nil
	asyncTaskReconciler.Unlock()
	var localPromotions int
	var localAsyncPromotions int
	RegisterJimengTaskPromoter(func(context.Context) error {
		localPromotions++
		return nil
	})
	RegisterAsyncTaskPromoter(func(context.Context) error {
		localAsyncPromotions++
		return nil
	})
	t.Cleanup(func() {
		jimengTaskReconciler.Lock()
		jimengTaskReconciler.promote = previousPromoter
		jimengTaskReconciler.Unlock()
		asyncTaskReconciler.Lock()
		asyncTaskReconciler.promotes = previousAsyncPromoters
		asyncTaskReconciler.Unlock()
	})

	type scheduled struct {
		taskType string
		ttl      time.Duration
		fn       func(context.Context) error
	}
	var jobs []scheduled
	runPeriodicJobsWithLease(func(taskType string, ttl time.Duration, fn func(context.Context) error) error {
		jobs = append(jobs, scheduled{taskType: taskType, ttl: ttl, fn: fn})
		return nil
	})

	seen := make(map[string]scheduled, len(jobs))
	for _, job := range jobs {
		seen[job.taskType] = job
	}
	require.Contains(t, seen, TaskTypeRelayQuotaReconcile)
	assert.Equal(t, 2*time.Minute, seen[TaskTypeRelayQuotaReconcile].ttl)
	assert.NotNil(t, seen[TaskTypeRelayQuotaReconcile].fn)
	require.Contains(t, seen, TaskTypeAuditLogDelivery)
	assert.Equal(t, 2*time.Minute, seen[TaskTypeAuditLogDelivery].ttl)
	assert.NotNil(t, seen[TaskTypeAuditLogDelivery].fn)
	require.Contains(t, seen, TaskTypeJimengTaskRecovery)
	assert.Equal(t, 2*time.Minute, seen[TaskTypeJimengTaskRecovery].ttl)
	assert.NotNil(t, seen[TaskTypeJimengTaskRecovery].fn)
	require.Contains(t, seen, TaskTypeAsyncTaskRecovery)
	assert.Equal(t, 2*time.Minute, seen[TaskTypeAsyncTaskRecovery].ttl)
	assert.NotNil(t, seen[TaskTypeAsyncTaskRecovery].fn)
	require.Contains(t, seen, TaskTypeStripeReconcile)
	assert.Equal(t, 2*time.Minute, seen[TaskTypeStripeReconcile].ttl)
	assert.NotNil(t, seen[TaskTypeStripeReconcile].fn)
	require.Contains(t, seen, TaskTypeChannelModelUpdate)
	assert.Equal(t, 30*time.Minute, seen[TaskTypeChannelModelUpdate].ttl)
	assert.NotNil(t, seen[TaskTypeChannelModelUpdate].fn)
	require.Contains(t, seen, TaskTypeChannelBalance)
	assert.Equal(t, 30*time.Minute, seen[TaskTypeChannelBalance].ttl)
	assert.NotNil(t, seen[TaskTypeChannelBalance].fn)
	require.Contains(t, seen, TaskTypeChannelHealth)
	assert.Equal(t, setting.DefaultAutoTestChannelMinutes*time.Minute, seen[TaskTypeChannelHealth].ttl)
	assert.NotNil(t, seen[TaskTypeChannelHealth].fn)
	require.Contains(t, seen, TaskTypeCleanupUserSessions)
	assert.Equal(t, time.Hour, seen[TaskTypeCleanupUserSessions].ttl)
	assert.NotNil(t, seen[TaskTypeCleanupUserSessions].fn)
	assert.Len(t, jobs, 12, "every cluster-singleton periodic job must pass through the lease runner")
	assert.NotContains(t, seen, TaskTypeSyncAbilityCache,
		"node-local ability caches must never be hidden behind a cluster lease")
	assert.NotContains(t, seen, TaskTypeInstanceHeartbeat,
		"each node must publish its own heartbeat independently")
	assert.Equal(t, 1, localPromotions,
		"every node must promote its own Jimeng journal outside the cluster lease")
	assert.Equal(t, 1, localAsyncPromotions,
		"every node must promote its own async-task journal outside the cluster lease")
}

func TestBackgroundJobLoopRunsImmediatelyAndOnEveryTick(t *testing.T) {
	ticks := make(chan time.Time, 2)
	ticks <- time.Now()
	ticks <- time.Now()
	close(ticks)

	calls := 0
	runBackgroundJobLoop(func() { calls++ }, ticks)
	assert.Equal(t, 3, calls, "startup must run once before waiting for the first ticker interval")
}

func TestBackgroundJobLoopRecoversOnePassAndContinues(t *testing.T) {
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	close(ticks)
	calls := 0
	runBackgroundJobLoop(func() {
		calls++
		if calls == 1 {
			panic("first-pass-secret-must-not-escape")
		}
	}, ticks)
	assert.Equal(t, 2, calls, "one panicking pass must not kill the process-long scheduler")
}

func TestAsyncPeriodicLeaseRunnerIsolatesStalledJobs(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	duplicateFirstStarted := make(chan struct{}, 1)
	done := make(chan struct{}, 2)
	var firstCalls atomic.Int64
	runner := asyncPeriodicLeaseRunner(func(taskType string, _ time.Duration, _ func(context.Context) error) error {
		switch taskType {
		case "first":
			if firstCalls.Add(1) == 1 {
				close(firstStarted)
			} else {
				duplicateFirstStarted <- struct{}{}
			}
			<-releaseFirst
		case "second":
			close(secondStarted)
		}
		done <- struct{}{}
		return nil
	})

	require.NoError(t, runner("first", time.Minute, func(context.Context) error { return nil }))
	<-firstStarted
	require.NoError(t, runner("first", time.Minute, func(context.Context) error { return nil }))
	select {
	case <-duplicateFirstStarted:
		t.Fatal("a repeated scheduler tick accumulated another worker for the same task type")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, runner("second", time.Minute, func(context.Context) error { return nil }))
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("a stalled periodic job blocked an independent job")
	}
	close(releaseFirst)
	<-done
	<-done
	assert.Equal(t, int64(1), firstCalls.Load())
}

func TestAsyncPeriodicLeaseRunnerContainsRunnerPanic(t *testing.T) {
	panicked := make(chan struct{})
	runner := asyncPeriodicLeaseRunner(func(string, time.Duration, func(context.Context) error) error {
		defer close(panicked)
		panic("runner-secret-must-not-escape")
	})
	require.NoError(t, runner("panic", time.Minute, func(context.Context) error { return nil }))
	select {
	case <-panicked:
	case <-time.After(time.Second):
		t.Fatal("panicking runner did not execute")
	}
}

func TestBackgroundJobScheduleValidationHasNoStartupSideEffects(t *testing.T) {
	t.Setenv("SYNC_FREQUENCY", "0")
	calls := 0
	hooks := backgroundJobStartupHooks{
		startSystemTaskRunner:  func() { calls++ },
		startQuotaDataFlusher:  func() { calls++ },
		startPerfMetricFlusher: func() { calls++ },
		startPermissionSync:    func(int) { calls++ },
	}
	require.ErrorContains(t, startBackgroundJobsWithHooks(hooks), "SYNC_FREQUENCY")
	assert.Zero(t, calls)

	t.Setenv("SYNC_FREQUENCY", "60")
	t.Setenv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", "525601")
	require.ErrorContains(t, startBackgroundJobsWithHooks(hooks), "CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES")
	assert.Zero(t, calls)

	t.Setenv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", "30")
	t.Setenv("USER_SESSION_ACTIVE_LIMIT", "0")
	require.ErrorContains(t, startBackgroundJobsWithHooks(hooks), "USER_SESSION_ACTIVE_LIMIT")
	assert.Zero(t, calls)
}

func TestBackgroundJobScheduleStrictBoundaries(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "SYNC_FREQUENCY", value: "-1"},
		{key: "SYNC_FREQUENCY", value: "01"},
		{key: "SYNC_FREQUENCY", value: "86401"},
		{key: "CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", value: "yes"},
		{key: "CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", value: "NaN"},
		{key: "CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", value: "0"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv("SYNC_FREQUENCY", "60")
			t.Setenv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", "true")
			t.Setenv("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES", "30")
			t.Setenv(test.key, test.value)
			_, err := loadBackgroundJobSchedule()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.key)
		})
	}
}

func TestBackgroundJobCadencesKeepHeartbeatAndMaintenanceIndependentFromSyncFrequency(t *testing.T) {
	t.Setenv("SYNC_FREQUENCY", "86400")
	schedule, err := loadBackgroundJobSchedule()
	require.NoError(t, err)
	cadences := cadencesForBackgroundJobs(schedule)

	assert.Equal(t, 24*time.Hour, cadences.runtimeSync)
	assert.Equal(t, 30*time.Second, cadences.maintenancePoll)
	assert.Equal(t, 30*time.Second, cadences.instanceHeartbeat)
	assert.Less(t, cadences.instanceHeartbeat, time.Duration(model.SystemInstanceStaleAfterSeconds)*time.Second)
	assert.LessOrEqual(t, cadences.maintenancePoll, 2*time.Minute,
		"the scheduler poll must not exceed the shortest critical reconciliation cadence")
}

func TestPromotionFailureSuppressesDependentRecoveryJobsForThatPass(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, setting.Init())

	jimengTaskReconciler.Lock()
	previousPromoter := jimengTaskReconciler.promote
	jimengTaskReconciler.promote = func(context.Context) error {
		return errors.New("injected promotion failure")
	}
	jimengTaskReconciler.Unlock()
	asyncTaskReconciler.Lock()
	previousAsyncPromoters := asyncTaskReconciler.promotes
	asyncTaskReconciler.promotes = nil
	asyncTaskReconciler.Unlock()
	t.Cleanup(func() {
		jimengTaskReconciler.Lock()
		jimengTaskReconciler.promote = previousPromoter
		jimengTaskReconciler.Unlock()
		asyncTaskReconciler.Lock()
		asyncTaskReconciler.promotes = previousAsyncPromoters
		asyncTaskReconciler.Unlock()
	})

	var jobs []string
	runPeriodicJobsWithLeaseContext(context.Background(), func(taskType string, _ time.Duration, _ func(context.Context) error) error {
		jobs = append(jobs, taskType)
		return nil
	})

	assert.NotContains(t, jobs, TaskTypeRelayQuotaReconcile)
	assert.NotContains(t, jobs, TaskTypeJimengTaskRecovery)
	assert.NotContains(t, jobs, TaskTypeAsyncTaskRecovery)
	assert.Contains(t, jobs, TaskTypeAuditLogDelivery,
		"independent maintenance must remain schedulable after a local recovery import failure")
	assert.Contains(t, jobs, TaskTypeCleanupLogs)
}

func TestChannelBalanceUpdateIntervalValidation(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		wantEnabled bool
		want        time.Duration
		wantError   bool
	}{
		{name: "unset"},
		{name: "valid", value: "30", wantEnabled: true, want: 30 * time.Minute},
		{name: "trimmed", value: " 5 ", wantEnabled: true, want: 5 * time.Minute},
		{name: "zero", value: "0", wantError: true},
		{name: "negative", value: "-1", wantError: true},
		{name: "malformed", value: "often", wantError: true},
		{name: "too large", value: "525601", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "unset" {
				t.Setenv("CHANNEL_UPDATE_FREQUENCY", "")
			} else {
				t.Setenv("CHANNEL_UPDATE_FREQUENCY", test.value)
			}
			interval, enabled, err := channelBalanceUpdateInterval()
			if test.wantError {
				require.Error(t, err)
				assert.False(t, enabled)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantEnabled, enabled)
			assert.Equal(t, test.want, interval)
		})
	}
}
