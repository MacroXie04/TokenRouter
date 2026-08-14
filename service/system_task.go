// Package service: durable system-task queue with a lease-guarded runner.
// EnqueueSystemTask + the claim pass mirror the reference contract: one active
// task per type, pending tasks claimed by a runner, lease-expired running tasks
// failed so a crashed node never wedges the queue.
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

// systemTaskRunnerIdleInterval is how often the runner sweeps for pending
// tasks without an explicit wakeup.
const systemTaskRunnerIdleInterval = 5 * time.Second

var (
	systemTaskWakeup      = make(chan struct{}, 1)
	systemTaskRunnerOnce  sync.Once
	systemTaskRunnerStop  = make(chan struct{})
	systemTaskRunnerGroup sync.WaitGroup
)

// systemTaskHandler executes one claimed task. Returning an error marks the
// task failed; otherwise the task is marked succeeded with the returned result.
type systemTaskHandler func(task *model.SystemTask) (result string, err error)

var systemTaskHandlers = map[string]systemTaskHandler{
	model.SystemTaskTypeChannelTest: runChannelTestTask,
	model.SystemTaskTypeLogCleanup:  runLogCleanupTask,
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
			runnerID := "runner-" + common.GenerateUUID()[:8]
			ticker := time.NewTicker(systemTaskRunnerIdleInterval)
			defer ticker.Stop()
			for {
				select {
				case <-systemTaskRunnerStop:
					return
				case <-systemTaskWakeup:
				case <-ticker.C:
				}
				_ = ExpireStaleSystemTaskLocks()
				runSystemTaskClaimPass(runnerID, false)
			}
		}()
	})
}

// RunPendingSystemTasksOnce claims one pending task per type and waits for all
// claimed work to finish. It is useful for controlled maintenance execution and
// deterministic callers that do not want to start the process-long runner.
func RunPendingSystemTasksOnce() {
	_ = ExpireStaleSystemTaskLocks()
	runSystemTaskClaimPass("oneshot-"+common.GenerateUUID()[:8], true)
}

// runSystemTaskClaimPass claims the earliest pending task per registered type.
// The process runner dispatches in parallel; one-shot callers wait for completion.
func runSystemTaskClaimPass(runnerID string, wait bool) {
	var group sync.WaitGroup
	for taskType, handler := range systemTaskHandlers {
		pending, err := model.FindPendingSystemTasks(taskType, 1)
		if err != nil || len(pending) == 0 {
			continue
		}
		task, claimed, err := model.ClaimSystemTask(pending[0].ID, taskType, runnerID)
		if err != nil || !claimed {
			continue
		}
		run := func(t *model.SystemTask, h systemTaskHandler) {
			defer group.Done()
			result, runErr := h(t)
			if runErr != nil {
				_ = model.FailSystemTask(t.TaskID, runErr.Error())
				return
			}
			_ = model.CompleteSystemTask(t.TaskID, result)
		}
		group.Add(1)
		go run(task, handler)
	}
	if wait {
		group.Wait()
	}
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
// summary. Notify is stored in the payload for operators; TokenRouter has no
// admin message-delivery subsystem, so no messages are sent (deviation #14).
func runChannelTestTask(task *model.SystemTask) (string, error) {
	var payload channelTestTaskPayload
	if task.Payload != "" {
		_ = common.UnmarshalJsonStr(task.Payload, &payload)
	}

	var channels []model.Channel
	if err := model.DB.Find(&channels).Error; err != nil {
		return "", err
	}
	now := common.NowTimestamp()
	summary := channelTestSummary{}
	for i := range channels {
		ch := &channels[i]
		if ch.TestModel == "" && ch.Models == "" {
			continue
		}
		summary.Tested++
		success, _ := testAndRecordChannel(ch, now)
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
		if i%5 == 0 || i == len(channels)-1 {
			state := map[string]any{"processed": i + 1, "total": len(channels)}
			if b, err := common.Marshal(state); err == nil {
				_ = model.UpdateSystemTaskState(task.TaskID, string(b))
			}
		}
	}
	resultBytes, err := common.Marshal(summary)
	if err != nil {
		return "", err
	}
	return string(resultBytes), nil
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

const logCleanupBatchSize = 100

// StartLogCleanupTask enqueues a log-cleanup task for the given target
// timestamp; an already-active cleanup task is returned instead (reference
// contract).
func StartLogCleanupTask(targetTimestamp int64) (*model.SystemTask, error) {
	if targetTimestamp <= 0 {
		return nil, errors.New("target timestamp is required")
	}
	activeTask, err := model.GetActiveSystemTask(model.SystemTaskTypeLogCleanup)
	if err != nil {
		return nil, err
	}
	if activeTask != nil {
		return activeTask, nil
	}
	task, _, err := EnqueueSystemTask(model.SystemTaskTypeLogCleanup, logCleanupPayload{
		TargetTimestamp: targetTimestamp,
		BatchSize:       logCleanupBatchSize,
	})
	if err != nil {
		activeTask, activeErr := model.GetActiveSystemTask(model.SystemTaskTypeLogCleanup)
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
func runLogCleanupTask(task *model.SystemTask) (string, error) {
	var payload logCleanupPayload
	if task.Payload != "" {
		if err := common.UnmarshalJsonStr(task.Payload, &payload); err != nil {
			return "", err
		}
	}
	if payload.TargetTimestamp <= 0 {
		return "", errors.New("target timestamp is required")
	}
	if payload.BatchSize <= 0 {
		payload.BatchSize = logCleanupBatchSize
	}
	var state logCleanupState
	if task.State != "" {
		if err := common.UnmarshalJsonStr(task.State, &state); err != nil {
			return "", err
		}
	}
	ctx := context.Background()
	for {
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
			rowsAffected, err := model.DeleteOldLogBatch(ctx, payload.TargetTimestamp, payload.BatchSize)
			if err != nil {
				return "", err
			}
			if rowsAffected == 0 {
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
	return model.UpdateSystemTaskState(task.TaskID, string(data))
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
