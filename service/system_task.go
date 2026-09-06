// Package service: durable system-task queue with a lease-guarded runner.
// EnqueueSystemTask + the claim pass mirror the reference contract: one active
// task per type, pending tasks claimed by a runner, lease-expired running tasks
// failed so a crashed node never wedges the queue.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// systemTaskRunnerIdleInterval is how often the runner sweeps for pending
// tasks without an explicit wakeup.
const systemTaskRunnerIdleInterval = 5 * time.Second

// A system-task lease is renewed well before its expiry. The lease duration is
// a crash-detection window, not a maximum runtime for a handler.
const systemTaskHeartbeatInterval = model.SystemTaskLeaseDuration / 3

// A handler is required to honor cancellation, but a defensive grace bound
// keeps a broken handler from wedging its scheduler goroutine forever after
// lease ownership has already been lost.
const systemTaskHandlerCancellationGracePeriod = 5 * time.Second

// Renewal gets its own deadline well before the remaining lease expires. The
// wrapper also races the call against this deadline so even a misbehaving
// driver cannot keep the handler context alive indefinitely.
const systemTaskLeaseRenewalTimeout = 5 * time.Second

var (
	systemTaskWakeup      = make(chan struct{}, 1)
	systemTaskRunnerOnce  sync.Once
	systemTaskRunnerStop  = make(chan struct{})
	systemTaskRunnerGroup sync.WaitGroup
)

// systemTaskHandler executes one claimed task. Handlers must stop when ctx is
// canceled: the lease heartbeat cancels it as soon as renewal fails or the lock
// is lost. Returning an error marks the task failed only while the caller still
// owns the lease.
type systemTaskHandler func(ctx context.Context, task *model.SystemTask) (result string, err error)

var systemTaskHandlers = map[string]systemTaskHandler{
	model.SystemTaskTypeChannelTest: runChannelTestTask,
	model.SystemTaskTypeLogCleanup:  runLogCleanupTask,
	model.SystemTaskTypeModelUpdate: runModelUpdateTask,
}

// EnqueueSystemTask creates a pending task of the given type unless an active
// (pending/running) task already exists, in which case that task is returned
// with created=false. The race where two enqueues pass the existence check is
// resolved by the active_key unique index: the losing insert falls back to the
// existing active task.
func EnqueueSystemTask(taskType string, payload any) (*model.SystemTask, bool, error) {
	activeTask, err := model.GetActiveSystemTask(taskType)
	if err != nil {
		return nil, false, err
	}
	if activeTask != nil {
		return activeTask, false, nil
	}

	task, err := model.CreateSystemTask(taskType, payload, nil)
	if err != nil {
		activeTask, activeErr := model.GetActiveSystemTask(taskType)
		if activeErr == nil && activeTask != nil {
			return activeTask, false, nil
		}
		return nil, false, err
	}
	notifySystemTaskRunner()
	return task, true, nil
}

// notifySystemTaskRunner wakes the runner loop (non-blocking).
func notifySystemTaskRunner() {
	select {
	case systemTaskWakeup <- struct{}{}:
	default:
	}
}

// StartSystemTaskRunner launches the background runner once. Idempotent across
// calls; the loop exits when the process stops.
func StartSystemTaskRunner() {
	systemTaskRunnerOnce.Do(func() {
		systemTaskRunnerGroup.Add(1)
		go func() {
			defer systemTaskRunnerGroup.Done()
			runnerUUID, err := common.SecureRandomUUID()
			if err != nil {
				common.SysError("start system task runner: " + err.Error())
				return
			}
			runnerID := "runner-" + runnerUUID[:8]
			ticker := time.NewTicker(systemTaskRunnerIdleInterval)
			defer ticker.Stop()
			for {
				select {
				case <-systemTaskRunnerStop:
					return
				case <-systemTaskWakeup:
				case <-ticker.C:
				}
				if err := ExpireStaleSystemTaskLocks(); err != nil {
					common.SysError("expire stale system tasks: " + err.Error())
				}
				if err := runSystemTaskClaimPass(runnerID, false); err != nil {
					common.SysError("run system task claim pass: " + err.Error())
				}
			}
		}()
	})
}

// RunPendingSystemTasksOnce claims one pending task per type and waits for all
// claimed work to finish. It is useful for controlled maintenance execution and
// deterministic callers that do not want to start the process-long runner.
func RunPendingSystemTasksOnce() error {
	if err := ExpireStaleSystemTaskLocks(); err != nil {
		return err
	}
	runnerUUID, err := common.SecureRandomUUID()
	if err != nil {
		return err
	}
	return runSystemTaskClaimPass("oneshot-"+runnerUUID[:8], true)
}

// runSystemTaskClaimPass claims the earliest pending task per registered type.
// The process runner dispatches in parallel; one-shot callers wait for completion.
func runSystemTaskClaimPass(runnerID string, wait bool) error {
	var group sync.WaitGroup
	errCh := make(chan error, len(systemTaskHandlers)*2)
	report := func(err error) {
		if err == nil {
			return
		}
		common.SysError("system task runner: " + err.Error())
		if wait {
			errCh <- err
		}
	}
	for taskType, handler := range systemTaskHandlers {
		pending, err := model.FindPendingSystemTasks(taskType, 1)
		if err != nil {
			report(errors.New("find pending " + taskType + " task: " + err.Error()))
			continue
		}
		if len(pending) == 0 {
			continue
		}
		task, claimed, err := model.ClaimSystemTask(pending[0].ID, taskType, runnerID)
		if err != nil {
			report(errors.New("claim " + taskType + " task: " + err.Error()))
			continue
		}
		if !claimed {
			continue
		}
		run := func(t *model.SystemTask, h systemTaskHandler) {
			defer group.Done()
			result, runErr, leaseErr := runSystemTaskHandlerWithHeartbeat(t, h)
			if leaseErr != nil {
				// Once renewal fails, this runner no longer has authority to write
				// either progress or a terminal state. The stale-lock sweep or the
				// succeeding owner is responsible for resolving the task row.
				report(errors.Join(leaseErr, wrapSystemTaskTransitionError("stop system task handler", runErr)))
				return
			}
			if runErr != nil {
				transitionErr := model.FailSystemTask(t.TaskID, t.LockedBy, runErr.Error())
				report(errors.Join(runErr, wrapSystemTaskTransitionError("mark system task failed", transitionErr)))
				return
			}
			if transitionErr := model.CompleteSystemTask(t.TaskID, t.LockedBy, result); transitionErr != nil {
				report(errors.New("complete system task: " + transitionErr.Error()))
			}
		}
		group.Add(1)
		go run(task, handler)
	}
	if wait {
		group.Wait()
	}
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

type systemTaskHandlerResult struct {
	result string
	err    error
}

type systemTaskLeaseRenewer func(ctx context.Context, taskID, runnerID string, lockUntil int64) error

// runSystemTaskHandlerWithHeartbeat keeps the task lease alive for the whole
// handler run. A separate helper accepts a heartbeat channel and renew function
// so lock-loss behavior can be tested deterministically without sleeping.
func runSystemTaskHandlerWithHeartbeat(task *model.SystemTask, handler systemTaskHandler) (string, error, error) {
	ticker := time.NewTicker(systemTaskHeartbeatInterval)
	defer ticker.Stop()
	return runSystemTaskHandlerWithHeartbeatChannel(task, handler, ticker.C, model.RenewSystemTaskLockContext)
}

func runSystemTaskHandlerWithHeartbeatChannel(
	task *model.SystemTask,
	handler systemTaskHandler,
	heartbeats <-chan time.Time,
	renew systemTaskLeaseRenewer,
) (string, error, error) {
	return runSystemTaskHandlerWithHeartbeatChannelAndBounds(
		task, handler, heartbeats, renew,
		systemTaskLeaseRenewalTimeout, systemTaskHandlerCancellationGracePeriod,
	)
}

func runSystemTaskHandlerWithHeartbeatChannelAndGrace(
	task *model.SystemTask,
	handler systemTaskHandler,
	heartbeats <-chan time.Time,
	renew systemTaskLeaseRenewer,
	cancellationGrace time.Duration,
) (string, error, error) {
	return runSystemTaskHandlerWithHeartbeatChannelAndBounds(
		task, handler, heartbeats, renew, systemTaskLeaseRenewalTimeout, cancellationGrace,
	)
}

func runSystemTaskHandlerWithHeartbeatChannelAndBounds(
	task *model.SystemTask,
	handler systemTaskHandler,
	heartbeats <-chan time.Time,
	renew systemTaskLeaseRenewer,
	renewalTimeout time.Duration,
	cancellationGrace time.Duration,
) (string, error, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan systemTaskHandlerResult, 1)
	go func() {
		outcome := systemTaskHandlerResult{}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					// Handler panic values may contain provider responses or
					// credentials. Preserve only their type in durable state/logs.
					outcome.err = fmt.Errorf("panic (%T)", recovered)
				}
			}()
			outcome.result, outcome.err = handler(ctx, task)
		}()
		done <- outcome
	}()

	for {
		select {
		case outcome := <-done:
			return outcome.result, outcome.err, nil
		case _, ok := <-heartbeats:
			if !ok {
				heartbeats = nil
				continue
			}
			if renewalTimeout <= 0 {
				cancel()
				return "", context.Canceled, errors.New("renew system task lease: renewal timeout is invalid")
			}
			renewCtx, cancelRenew := context.WithTimeout(ctx, renewalTimeout)
			renewed := make(chan error, 1)
			go func() {
				renewed <- renew(renewCtx, task.TaskID, task.LockedBy, 0)
			}()
			var renewErr error
			select {
			case outcome := <-done:
				cancelRenew()
				return outcome.result, outcome.err, nil
			case renewErr = <-renewed:
				cancelRenew()
			case <-renewCtx.Done():
				renewErr = renewCtx.Err()
				cancelRenew()
			}
			if renewErr != nil {
				cancel()
				leaseErr := fmt.Errorf("renew system task lease: %w", renewErr)
				if cancellationGrace <= 0 {
					return "", context.Canceled, leaseErr
				}
				timer := time.NewTimer(cancellationGrace)
				select {
				case outcome := <-done:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return outcome.result, outcome.err, leaseErr
				case <-timer.C:
					return "", context.Canceled, leaseErr
				}
			}
		}
	}
}

func wrapSystemTaskTransitionError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// ExpireStaleSystemTaskLocks fails running tasks whose lease duration passed.
func ExpireStaleSystemTaskLocks() error {
	return model.ExpireStaleSystemTaskLocks()
}

// channelTestTaskPayload mirrors the reference channel_test task payload.
type channelTestTaskPayload struct {
	Mode   string `json:"mode"`   // "scheduled_all" for the manual full sweep
	Notify bool   `json:"notify"` // admin notification on completion
}

// channelTestSummary is the per-run result persisted on the task row.
type channelTestSummary struct {
	Tested    int `json:"tested"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Disabled  int `json:"disabled"`
	Enabled   int `json:"enabled"`
}

// runChannelTestTask sweeps every testable channel with TestChannelHealth
// (which records response_time/test_time and honors auto-ban), then persists a
// summary and optionally sends the reference completion notice to the root
// operator through that account's configured notification channel.
func runChannelTestTask(ctx context.Context, task *model.SystemTask) (string, error) {
	if task == nil {
		return "", errors.New("channel test task is nil")
	}
	payload := channelTestTaskPayload{Mode: setting.ChannelTestModeScheduledAll}
	if task.Payload != "" && task.Payload != "null" {
		if err := common.ValidateJSONNoDuplicateKeys([]byte(task.Payload)); err != nil {
			return "", fmt.Errorf("invalid channel test payload: %w", err)
		}
		if err := common.UnmarshalJsonStr(task.Payload, &payload); err != nil {
			return "", fmt.Errorf("invalid channel test payload: %w", err)
		}
	}

	var channels []model.Channel
	if err := model.DB.Order("id asc").Find(&channels).Error; err != nil {
		return "", err
	}
	selected, err := SelectChannelsForReliabilityTest(channels, payload.Mode)
	if err != nil {
		return "", err
	}
	now := common.NowTimestamp()
	summary := channelTestSummary{}
	allowDisable := payload.Mode != setting.ChannelTestModePassiveRecovery
	for i := range selected {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		ch := &selected[i]
		summary.Tested++
		success, _, recordErr := testAndRecordChannelContextWithMode(ctx, ch, now, allowDisable)
		if recordErr != nil {
			return "", recordErr
		}
		if success {
			summary.Succeeded++
		} else {
			summary.Failed++
		}
		var refreshed model.Channel
		if err := model.DB.First(&refreshed, ch.Id).Error; err == nil {
			if refreshed.Status == constant.ChannelStatusEnabled {
				summary.Enabled++
			} else {
				summary.Disabled++
			}
		}
		if i%5 == 0 || i == len(selected)-1 {
			state := map[string]any{"processed": i + 1, "total": len(selected)}
			if b, err := common.Marshal(state); err == nil {
				if err := model.UpdateSystemTaskState(task.TaskID, task.LockedBy, string(b)); err != nil {
					return "", err
				}
			}
		}
	}
	resultBytes, err := common.Marshal(summary)
	if err != nil {
		return "", err
	}
	if payload.Notify {
		if err := NotifyRootUser(UserNotification{
			Type: "channel_test", Title: "通道测试完成", Content: "所有通道测试已完成",
		}); err != nil {
			common.SysError("channel test completion notification failed")
		}
	}
	return string(resultBytes), nil
}

// modelUpdateTaskPayload distinguishes a manual detect-all request from the
// scheduled sweep. Manual runs force a fresh check and stage every change for
// review; scheduled runs honor the minimum interval and may auto-add models on
// channels that explicitly opted in.
type modelUpdateTaskPayload struct {
	Manual bool `json:"manual,omitempty"`
}

type modelUpdateTaskState struct {
	Processed int `json:"processed"`
	Total     int `json:"total"`
	Progress  int `json:"progress"`
}

// runModelUpdateTask executes discovery inside the lease-guarded durable task
// runner. Every state update remains fenced by task ID and runner ownership.
func runModelUpdateTask(ctx context.Context, task *model.SystemTask) (string, error) {
	var payload modelUpdateTaskPayload
	if task.Payload != "" && task.Payload != "null" {
		if err := common.UnmarshalJsonStr(task.Payload, &payload); err != nil {
			return "", err
		}
	}
	report := func(processed, total int) error {
		progress := 0
		if total > 0 {
			progress = processed * 100 / total
			if progress > 100 {
				progress = 100
			}
		} else if processed == total {
			progress = 100
		}
		state, err := common.Marshal(modelUpdateTaskState{Processed: processed, Total: total, Progress: progress})
		if err != nil {
			return err
		}
		return model.UpdateSystemTaskState(task.TaskID, task.LockedBy, string(state))
	}
	summary, err := RunChannelUpstreamModelUpdateTask(ctx, payload.Manual, !payload.Manual, report)
	if err != nil {
		return "", err
	}
	result, err := common.Marshal(summary)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

// logCleanupPayload mirrors the reference log_cleanup task payload.
type logCleanupPayload struct {
	TargetTimestamp int64 `json:"target_timestamp"`
	BatchSize       int   `json:"batch_size"`
}

// logCleanupState tracks progress across batches (reference contract).
type logCleanupState struct {
	Total     int64 `json:"total"`
	Processed int64 `json:"processed"`
	Progress  int   `json:"progress"`
	Remaining int64 `json:"remaining"`
}

// logCleanupResult is the persisted completion result.
type logCleanupResult struct {
	DeletedCount int64 `json:"deleted_count"`
}

const (
	logCleanupBatchSize    = 100
	maxLogCleanupBatchSize = 1_000
)

// StartLogCleanupTask enqueues a log-cleanup task for the given target
// timestamp; an already-active cleanup task is returned instead (reference
// contract).
func StartLogCleanupTask(targetTimestamp int64) (*model.SystemTask, error) {
	return StartLogCleanupTaskContext(context.Background(), targetTimestamp)
}

// StartLogCleanupTaskContext is the cancellable, database-clock-backed form
// used by HTTP and periodic callers.
func StartLogCleanupTaskContext(ctx context.Context, targetTimestamp int64) (*model.SystemTask, error) {
	if ctx == nil {
		return nil, errors.New("log cleanup context is nil")
	}
	if targetTimestamp <= 0 {
		return nil, errors.New("target timestamp is required")
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(ctx)
	if err != nil {
		return nil, err
	}
	if targetTimestamp > now {
		return nil, errors.New("target timestamp cannot be in the future")
	}
	activeTask, err := model.GetActiveSystemTaskContext(ctx, model.SystemTaskTypeLogCleanup)
	if err != nil {
		return nil, err
	}
	if activeTask != nil {
		return activeTask, nil
	}
	task, _, err := EnqueueSystemTaskContext(ctx, model.SystemTaskTypeLogCleanup, logCleanupPayload{
		TargetTimestamp: targetTimestamp,
		BatchSize:       logCleanupBatchSize,
	})
	if err != nil {
		activeTask, activeErr := model.GetActiveSystemTaskContext(ctx, model.SystemTaskTypeLogCleanup)
		if activeErr == nil && activeTask != nil {
			return activeTask, nil
		}
		return nil, err
	}
	notifySystemTaskRunner()
	return task, nil
}

// runLogCleanupTask deletes logs older than the target timestamp in batches,
// persisting progress state and finishing with the deleted count (reference
// execution semantics).
func runLogCleanupTask(ctx context.Context, task *model.SystemTask) (string, error) {
	var payload logCleanupPayload
	if task.Payload != "" {
		if err := common.UnmarshalJsonStr(task.Payload, &payload); err != nil {
			return "", err
		}
	}
	if payload.TargetTimestamp <= 0 {
		return "", errors.New("target timestamp is required")
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(ctx)
	if err != nil {
		return "", err
	}
	if payload.TargetTimestamp > now {
		return "", errors.New("target timestamp cannot be in the future")
	}
	if payload.BatchSize <= 0 {
		payload.BatchSize = logCleanupBatchSize
	}
	if payload.BatchSize > maxLogCleanupBatchSize {
		return "", errors.New("log cleanup batch size exceeds maximum")
	}
	var state logCleanupState
	if task.State != "" {
		if err := common.UnmarshalJsonStr(task.State, &state); err != nil {
			return "", err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		remaining, err := model.CountOldLog(ctx, payload.TargetTimestamp)
		if err != nil {
			return "", err
		}
		syncLogCleanupStateFromRemaining(&state, remaining)
		if err := saveLogCleanupState(task, state); err != nil {
			return "", err
		}
		if state.Remaining == 0 {
			break
		}
		progressed := false
		for state.Remaining > 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			rowsAffected, err := model.DeleteOldLogBatch(ctx, payload.TargetTimestamp, payload.BatchSize)
			if err != nil {
				return "", err
			}
			if rowsAffected == 0 {
				remaining, countErr := model.CountOldLog(ctx, payload.TargetTimestamp)
				if countErr != nil {
					return "", countErr
				}
				if remaining == 0 {
					state.Remaining = 0
					progressed = true
				}
				break
			}
			progressed = true
			state.Processed += rowsAffected
			if state.Total < state.Processed {
				state.Total = state.Processed
			}
			if state.Remaining > rowsAffected {
				state.Remaining -= rowsAffected
			} else {
				state.Remaining = 0
			}
			state.Progress = logCleanupProgress(state.Processed, state.Total)
			if err := saveLogCleanupState(task, state); err != nil {
				return "", err
			}
		}
		if !progressed {
			return "", errors.New("no log rows were deleted")
		}
	}
	state.Remaining = 0
	state.Progress = 100
	if state.Total < state.Processed {
		state.Total = state.Processed
	}
	if err := saveLogCleanupState(task, state); err != nil {
		return "", err
	}
	result := logCleanupResult{DeletedCount: state.Processed}
	data, err := common.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func saveLogCleanupState(task *model.SystemTask, state logCleanupState) error {
	data, err := common.Marshal(state)
	if err != nil {
		return err
	}
	return model.UpdateSystemTaskState(task.TaskID, task.LockedBy, string(data))
}

func syncLogCleanupStateFromRemaining(state *logCleanupState, remaining int64) {
	if state.Total <= 0 {
		state.Total = remaining
		state.Processed = 0
	} else {
		processedFromRemaining := state.Total - remaining
		if processedFromRemaining > state.Processed {
			state.Processed = processedFromRemaining
		}
	}
	if state.Processed < 0 {
		state.Processed = 0
	}
	state.Remaining = remaining
	state.Progress = logCleanupProgress(state.Processed, state.Total)
}

func logCleanupProgress(processed, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int(processed * 100 / total)
	if p > 100 {
		p = 100
	}
	return p
}
