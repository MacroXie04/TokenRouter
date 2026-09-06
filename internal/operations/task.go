package operations

import (
	"context"
	"errors"
	"fmt"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrTaskLeaseLost reports that a caller attempted to release a lease it no
// longer owns. A missing release is operationally different from ordinary
// lock contention and must not be reported as a successful run.
var ErrTaskLeaseLost = errors.New("task lease lost")

// AcquireTaskLock acquires a distributed lease for a task type, keyed by the
// node name. A stale (expired) lock is atomically taken over so a crashed node
// does not block the task forever.
func AcquireTaskLock(taskType, lockBy string, ttl time.Duration) (bool, error) {
	leaseSeconds := taskLeaseSeconds(ttl)
	if taskType == "" || lockBy == "" || leaseSeconds <= 0 {
		return false, errors.New("invalid task lease")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	lockUntil := now + leaseSeconds

	var lock model.SystemTaskLock
	err = model.DB.Where("type = ?", taskType).First(&lock).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// No existing lock: insert. A concurrent insert fails the unique key.
		lock = model.SystemTaskLock{Type: taskType, LockedBy: lockBy, LockedUntil: lockUntil, UpdatedAt: now}
		if cerr := model.DB.Create(&lock).Error; cerr != nil {
			// Portable contention detection: after a failed insert, an existing
			// row proves another node won the unique-key race. If no row exists,
			// preserve the actual database failure instead of calling it contention.
			var winner model.SystemTaskLock
			verifyErr := model.DB.Where("type = ?", taskType).First(&winner).Error
			switch {
			case verifyErr == nil:
				return false, nil
			case errors.Is(verifyErr, gorm.ErrRecordNotFound):
				return false, fmt.Errorf("create task lease: %w", cerr)
			default:
				return false, errors.Join(
					fmt.Errorf("create task lease: %w", cerr),
					fmt.Errorf("verify task lease contention: %w", verifyErr),
				)
			}
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("load task lease: %w", err)
	}
	if lock.LockedUntil > now {
		// Held by another node and still valid: not acquired, but not an error.
		return false, nil
	}
	// Stale lock: atomically take over using the old value as a compare.
	res := model.DB.Model(&model.SystemTaskLock{}).
		Where("type = ? AND locked_until = ?", taskType, lock.LockedUntil).
		Updates(map[string]any{"locked_by": lockBy, "locked_until": lockUntil, "updated_at": now})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ReleaseTaskLock releases the lease for a task type if held by lockBy.
func ReleaseTaskLock(taskType, lockBy string) error {
	result := model.DB.Where("type = ? AND locked_by = ?", taskType, lockBy).
		Delete(&model.SystemTaskLock{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTaskLeaseLost
	}
	return nil
}

// RenewTaskLock extends an unexpired lease only for its exact fencing token.
// Once another runner takes over, an older runner can neither renew nor
// release the replacement lease.
func RenewTaskLock(taskType, lockBy string, ttl time.Duration) error {
	leaseSeconds := taskLeaseSeconds(ttl)
	if taskType == "" || lockBy == "" || leaseSeconds <= 0 {
		return errors.New("invalid task lease renewal")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var lock model.SystemTaskLock
		if err := tx.Where("type = ? AND locked_by = ? AND locked_until > ?", taskType, lockBy, now).
			First(&lock).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTaskLeaseLost
			}
			return err
		}
		lockUntil := now + leaseSeconds
		if lockUntil <= lock.LockedUntil {
			lockUntil = lock.LockedUntil + 1
		}
		result := tx.Model(&model.SystemTaskLock{}).
			Where("type = ? AND locked_by = ? AND locked_until = ? AND locked_until > ?",
				taskType, lockBy, lock.LockedUntil, now).
			Updates(map[string]any{"locked_until": lockUntil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTaskLeaseLost
		}
		return nil
	})
}

func taskLeaseSeconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	seconds := int64((ttl + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

// RunWithLease runs fn under a distributed lease; if another node holds the
// lease, fn is skipped. Database/task/release failures are both returned and
// logged because periodic callers intentionally run jobs independently.
// The node name is read from NODE_NAME.
func RunWithLease(taskType string, ttl time.Duration, fn func() error) error {
	if fn == nil {
		return RunWithLeaseContext(taskType, ttl, nil)
	}
	return RunWithLeaseContext(taskType, ttl, func(context.Context) error { return fn() })
}

// RunWithLeaseContext is RunWithLease's context-aware form. It renews the
// lease while work is active and cancels ctx immediately if renewal loses the
// exact fencing token.
func RunWithLeaseContext(taskType string, ttl time.Duration, fn func(context.Context) error) error {
	return runWithLeaseContext(taskType, ttl, fn)
}

const periodicSystemTaskHistoryLimit = 32

// RunScheduledWithLeaseContext runs a periodic job through the durable system
// task lifecycle. Pending work survives a process crash, the claimed task ID
// is the fencing token for heartbeat and completion, and the latest terminal
// run (success or failure) supplies durable retry cadence.
func RunScheduledWithLeaseContext(taskType string, interval time.Duration, fn func(context.Context) error) error {
	taskType = strings.TrimSpace(taskType)
	if taskType == "" || interval <= 0 || fn == nil {
		err := errors.New("invalid leased task configuration")
		logging.SysError("leased task failed: " + err.Error())
		return err
	}
	if !strings.HasPrefix(taskType, model.SystemTaskPeriodicTypePrefix) {
		taskType = model.SystemTaskPeriodicTypePrefix + taskType
	}
	if len(taskType) > 64 {
		err := errors.New("periodic task type exceeds 64 characters")
		logging.SysError("leased task failed: " + err.Error())
		return err
	}
	if err := model.ExpireStaleSystemTaskLockType(taskType); err != nil {
		return logScheduledTaskError(taskType, "expire stale task leases", err)
	}

	task, err := model.GetActiveSystemTask(taskType)
	if err != nil {
		return logScheduledTaskError(taskType, "load active task", err)
	}
	runnerID, err := taskLeaseOwnerToken(env.GetEnv("NODE_NAME", "tokenrouter-node-1"))
	if err != nil {
		return logScheduledTaskError(taskType, "create runner token", err)
	}
	var claimed *model.SystemTask
	var won bool
	if task == nil {
		claimed, won, err = model.CreateAndClaimScheduledSystemTask(taskType, runnerID, interval)
	} else if task.Status == model.SystemTaskStatusPending {
		claimed, won, err = model.ClaimSystemTask(task.ID, taskType, runnerID)
	} else {
		return nil
	}
	if err != nil {
		return logScheduledTaskError(taskType, "admit task", err)
	}
	if !won {
		return nil
	}

	_, runErr, leaseErr := runSystemTaskHandlerWithHeartbeat(claimed, func(ctx context.Context, _ *model.SystemTask) (string, error) {
		return "", runLeasedTaskFunction(ctx, fn)
	})
	if leaseErr != nil {
		// A former owner has no authority to write a terminal state. Stale-lock
		// recovery or the succeeding owner resolves the persisted task row.
		return logScheduledTaskError(taskType, "run task", errors.Join(leaseErr, runErr))
	}

	var transitionErr error
	if runErr != nil {
		transitionErr = model.FailSystemTask(claimed.TaskID, claimed.LockedBy, boundedPeriodicTaskError(runErr))
	} else {
		transitionErr = model.CompleteSystemTask(claimed.TaskID, claimed.LockedBy, "")
	}
	pruneErr := prunePeriodicSystemTaskHistory(taskType)
	finalErr := errors.Join(runErr, wrapSystemTaskTransitionError("persist terminal task state", transitionErr), pruneErr)
	if finalErr != nil {
		return logScheduledTaskError(taskType, "finish task", finalErr)
	}
	return nil
}

func runWithLeaseContext(
	taskType string,
	leaseTTL time.Duration,
	fn func(context.Context) error,
) error {
	if taskType == "" || leaseTTL <= 0 || fn == nil {
		err := errors.New("invalid leased task configuration")
		logging.SysError("leased task failed: " + err.Error())
		return err
	}
	nodeName := env.GetEnv("NODE_NAME", "tokenrouter-node-1")
	leaseOwner, err := taskLeaseOwnerToken(nodeName)
	if err != nil {
		return err
	}
	acquired, err := AcquireTaskLock(taskType, leaseOwner, leaseTTL)
	if err != nil {
		err = fmt.Errorf("acquire %s lease: %w", taskType, err)
		logging.SysError("leased task failed: " + err.Error())
		return err
	}
	if !acquired {
		return nil
	}
	releasePending := true
	defer func() {
		if releasePending {
			if releaseErr := ReleaseTaskLock(taskType, leaseOwner); releaseErr != nil {
				logging.SysError("release abandoned " + taskType + " lease: " + releaseErr.Error())
			}
		}
	}()
	taskID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		releasePending = false
		releaseErr := ReleaseTaskLock(taskType, leaseOwner)
		err = errors.Join(err, wrapTaskLeaseError("release lease after task-id failure", releaseErr))
		logging.SysError("leased task failed: " + err.Error())
		return err
	}

	task := model.SystemTask{
		TaskID:    taskID,
		Type:      taskType,
		Status:    model.SystemTaskStatusRunning,
		LockedBy:  leaseOwner,
		CreatedAt: wallclock.NowTimestamp(),
		UpdatedAt: wallclock.NowTimestamp(),
	}
	if err := model.DB.Create(&task).Error; err != nil {
		releasePending = false
		releaseErr := ReleaseTaskLock(taskType, leaseOwner)
		err = errors.Join(
			fmt.Errorf("create %s task row: %w", taskType, err),
			wrapTaskLeaseError("release lease after task-row failure", releaseErr),
		)
		logging.SysError("leased task failed: " + err.Error())
		return err
	}

	runErr, heartbeatErr := runLeasedTaskFunctionWithHeartbeat(
		taskType, leaseOwner, leaseTTL, fn,
	)
	finalErr := errors.Join(
		wrapTaskLeaseError("run task", runErr),
		wrapTaskLeaseError("renew task lease", heartbeatErr),
	)
	releasePending = false
	releaseErr := ReleaseTaskLock(taskType, leaseOwner)
	finalErr = errors.Join(finalErr, wrapTaskLeaseError("release task lease", releaseErr))
	now := wallclock.NowTimestamp()
	updates := map[string]any{"status": model.SystemTaskStatusSucceeded, "updated_at": now}
	if finalErr != nil {
		updates["status"] = model.SystemTaskStatusFailed
		updates["error"] = boundedPeriodicTaskError(finalErr)
	}
	result := model.DB.Model(&model.SystemTask{}).
		Where("id = ? AND status = ?", task.ID, model.SystemTaskStatusRunning).
		Updates(updates)
	if result.Error != nil {
		finalErr = errors.Join(finalErr, fmt.Errorf("update %s task status: %w", taskType, result.Error))
	} else if result.RowsAffected != 1 {
		finalErr = errors.Join(finalErr, fmt.Errorf("update %s task status: task row missing or no longer running", taskType))
	}
	if finalErr != nil {
		logging.SysError("leased task failed: " + finalErr.Error())
	}
	return finalErr
}

func prunePeriodicSystemTaskHistory(taskType string) error {
	_, err := model.PrunePeriodicSystemTaskHistory(taskType, periodicSystemTaskHistoryLimit)
	if err != nil {
		return fmt.Errorf("prune periodic task history: %w", err)
	}
	return nil
}

func logScheduledTaskError(taskType, operation string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s %s: %w", operation, taskType, err)
	logging.SysError("scheduled task failed: " + wrapped.Error())
	return wrapped
}

func boundedPeriodicTaskError(err error) string {
	if err == nil {
		return ""
	}
	const maxBytes = 1024
	message := logging.RedactSensitiveText(err.Error())
	if len(message) <= maxBytes {
		return message
	}
	const suffix = "...[truncated]"
	cut := maxBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

func taskLeaseOwnerToken(nodeName string) (string, error) {
	runes := []rune(strings.TrimSpace(nodeName))
	if len(runes) > 80 {
		runes = runes[:80]
	}
	prefix := string(runes)
	if prefix == "" {
		prefix = "tokenrouter-node"
	}
	ownerID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return "", err
	}
	return prefix + ":" + ownerID, nil
}

func runLeasedTaskFunctionWithHeartbeat(
	taskType, leaseOwner string,
	ttl time.Duration,
	fn func(context.Context) error,
) (runErr, leaseErr error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLeasedTaskFunction(ctx, fn) }()
	heartbeatInterval := ttl / 3
	if heartbeatInterval < time.Second {
		heartbeatInterval = time.Second
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case runErr = <-done:
			return runErr, leaseErr
		case <-ticker.C:
			if err := RenewTaskLock(taskType, leaseOwner, ttl); err != nil {
				leaseErr = err
				cancel()
				return <-done, leaseErr
			}
		}
	}
}

func runLeasedTaskFunction(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// Panic values can contain request bodies or credentials. The type is
			// sufficient to distinguish the failure class without persisting or
			// logging attacker-controlled contents.
			err = fmt.Errorf("panic (%T)", recovered)
		}
	}()
	return fn(ctx)
}

func wrapTaskLeaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
