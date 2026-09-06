package store

import (
	"context"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// System task statuses (reference contract).
const (
	SystemTaskStatusPending   = "pending"
	SystemTaskStatusRunning   = "running"
	SystemTaskStatusSucceeded = "succeeded"
	SystemTaskStatusFailed    = "failed"
)

// ErrSystemTaskTransitionRejected means the task no longer belongs to the
// runner attempting to update it (for example, its lease expired or another
// runner took over). Callers must stop work instead of treating a zero-row
// transition as success.
var ErrSystemTaskTransitionRejected = errors.New("system task transition rejected")

// ErrSystemTaskLockLost is the reference name for a rejected lease-guarded
// transition. Keep ErrSystemTaskTransitionRejected as an alias for callers
// that already distinguish this condition from ordinary database failures.
var ErrSystemTaskLockLost = ErrSystemTaskTransitionRejected

// System task types.
const (
	SystemTaskTypeChannelTest = "channel_test"
	SystemTaskTypeLogCleanup  = "log_cleanup"
	SystemTaskTypeModelUpdate = "model_update"

	// SystemTaskPeriodicTypePrefix reserves task rows used only to coordinate
	// internal maintenance jobs. They retain durable execution evidence without
	// crowding the operator-facing system-task history.
	SystemTaskPeriodicTypePrefix = "periodic:"
)

// SystemTaskLeaseDuration is the crash-detection window for a claimed system
// task. Long-running work renews this lease periodically; it is not a maximum
// task runtime.
const SystemTaskLeaseDuration = 60 * time.Second

// SystemTaskFailureRetryInterval caps the retry delay after a failed periodic
// run. Successful work still observes its configured cadence, while a
// transient failure cannot suppress health or reconciliation work for months.
const SystemTaskFailureRetryInterval = 5 * time.Minute

// activeSystemTaskStatuses returns the statuses that count as "active".
func activeSystemTaskStatuses() []string {
	return []string{SystemTaskStatusPending, SystemTaskStatusRunning}
}

// GenerateSystemTaskID returns a unique 32-hex-char task id.
func GenerateSystemTaskID() (string, error) {
	return cryptoutil.GenerateKey(16)
}

// CreateSystemTask persists a pending task. ActiveKey is set to the task type
// so concurrent enqueues of the same type fail the unique index and fall back
// to the already-active task.
func CreateSystemTask(taskType string, payload, state any) (*SystemTask, error) {
	return CreateSystemTaskContext(context.Background(), taskType, payload, state)
}

// GetSystemTaskByTaskID loads a task by its task id; a missing task returns
// (nil, nil) so callers can emit the reference 404.
func GetSystemTaskByTaskID(taskID string) (*SystemTask, error) {
	var task SystemTask
	if err := DB.Where("task_id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

// GetActiveSystemTask returns the latest pending/running task of a type, or
// nil when none exists.
func GetActiveSystemTask(taskType string) (*SystemTask, error) {
	return GetActiveSystemTaskContext(context.Background(), taskType)
}

// FindPendingSystemTasks returns up to limit pending tasks of a type, oldest
// first.
func FindPendingSystemTasks(taskType string, limit int) ([]*SystemTask, error) {
	if limit <= 0 {
		limit = 1
	}
	var tasks []*SystemTask
	err := DB.Where("type = ? AND status = ?", taskType, SystemTaskStatusPending).
		Order("id asc").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

// GetPaginatedSystemTasks lists tasks (optionally of one type) newest first.
func GetPaginatedSystemTasks(taskType string, offset, limit int) ([]*SystemTask, int64, error) {
	query := DB.Model(&SystemTask{})
	if taskType != "" {
		query = query.Where("type = ?", taskType)
	} else {
		query = visibleSystemTasksQuery(query)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tasks []*SystemTask
	if err := query.Order("id desc").Offset(offset).Limit(limit).Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

func visibleSystemTasksQuery(query *gorm.DB) *gorm.DB {
	return query.Where(`system_tasks.type NOT LIKE ? OR (
		system_tasks.status = ? AND NOT EXISTS (
			SELECT 1 FROM system_tasks AS newer
			WHERE newer.type = system_tasks.type
				AND newer.status IN (?, ?)
				AND (newer.updated_at > system_tasks.updated_at
					OR (newer.updated_at = system_tasks.updated_at AND newer.id > system_tasks.id))
		)
	)`, SystemTaskPeriodicTypePrefix+"%", SystemTaskStatusFailed,
		SystemTaskStatusSucceeded, SystemTaskStatusFailed)
}

// GetLatestTerminalSystemTask returns the latest completed run of a type. Both
// successes and failures gate the next scheduler attempt, preventing a broken
// job from retrying on every short polling tick.
func GetLatestTerminalSystemTask(taskType string) (*SystemTask, error) {
	var task SystemTask
	err := DB.Where("type = ? AND status IN ?", taskType, []string{
		SystemTaskStatusSucceeded,
		SystemTaskStatusFailed,
	}).Order("updated_at DESC, id DESC").First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

// CreateAndClaimScheduledSystemTask atomically serializes the cadence check,
// task creation, and execution-lease acquisition for one internal maintenance
// type. Holding the per-type lock while re-reading the latest terminal row
// closes the race where a fast winner could finish between another node's due
// check and insert, causing two runs in one interval.
func CreateAndClaimScheduledSystemTask(
	taskType, runnerID string,
	interval time.Duration,
) (*SystemTask, bool, error) {
	taskType = strings.TrimSpace(taskType)
	runnerID = strings.TrimSpace(runnerID)
	if !strings.HasPrefix(taskType, SystemTaskPeriodicTypePrefix) || len(taskType) > 64 ||
		runnerID == "" || len(runnerID) > 128 || interval <= 0 {
		return nil, false, errors.New("invalid scheduled system-task claim")
	}
	intervalSeconds := int64(interval / time.Second)
	if interval%time.Second != 0 {
		intervalSeconds++
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 1
	}
	taskID, err := GenerateSystemTaskID()
	if err != nil {
		return nil, false, err
	}

	var claimed *SystemTask
	err = DB.Transaction(func(tx *gorm.DB) error {
		now, err := DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		lockUntil := now + int64(SystemTaskLeaseDuration/time.Second)
		acquired, expiredLock, err := acquireSystemTaskLock(
			tx, taskType, taskID, runnerID, now, lockUntil,
		)
		if err != nil || !acquired {
			return err
		}
		if expiredLock != nil && expiredLock.TaskID != "" {
			if err := markSystemTaskLeaseExpiredTx(tx, expiredLock.TaskID, now); err != nil {
				return err
			}
		}

		var active SystemTask
		activeErr := tx.Where("type = ? AND status IN ?", taskType, activeSystemTaskStatuses()).
			Order("id DESC").First(&active).Error
		if activeErr == nil {
			return releaseSystemTaskLockTx(tx, taskID, runnerID, true)
		}
		if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}

		var latest SystemTask
		latestErr := tx.Where("type = ? AND status IN ?", taskType, []string{
			SystemTaskStatusSucceeded,
			SystemTaskStatusFailed,
		}).Order("updated_at DESC, id DESC").First(&latest).Error
		switch {
		case latestErr == nil:
			cadenceSeconds := intervalSeconds
			failureRetrySeconds := int64(SystemTaskFailureRetryInterval / time.Second)
			if latest.Status == SystemTaskStatusFailed && cadenceSeconds > failureRetrySeconds {
				cadenceSeconds = failureRetrySeconds
			}
			if latest.UpdatedAt > now-cadenceSeconds {
				return releaseSystemTaskLockTx(tx, taskID, runnerID, true)
			}
		case latestErr != nil && !errors.Is(latestErr, gorm.ErrRecordNotFound):
			return latestErr
		}

		task := &SystemTask{
			TaskID:    taskID,
			Type:      taskType,
			Status:    SystemTaskStatusRunning,
			ActiveKey: &taskType,
			Payload:   "null",
			LockedBy:  runnerID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		claimed = task
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return claimed, claimed != nil, nil
}

// PrunePeriodicSystemTaskHistory bounds internal maintenance history while
// retaining active rows and the newest keep terminal runs. The prefix guard is
// deliberately fail-closed so this helper cannot delete operator-created task
// history even if called with the wrong type.
func PrunePeriodicSystemTaskHistory(taskType string, keep int) (int64, error) {
	if !strings.HasPrefix(taskType, SystemTaskPeriodicTypePrefix) || keep < 1 {
		return 0, errors.New("invalid periodic system-task retention request")
	}
	var boundary SystemTask
	err := DB.Where("type = ? AND status IN ?", taskType, []string{
		SystemTaskStatusSucceeded,
		SystemTaskStatusFailed,
	}).Order("updated_at DESC, id DESC").Offset(keep - 1).Limit(1).First(&boundary).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	result := DB.Where("type = ? AND status IN ?", taskType, []string{
		SystemTaskStatusSucceeded,
		SystemTaskStatusFailed,
	}).Where("updated_at < ? OR (updated_at = ? AND id < ?)",
		boundary.UpdatedAt, boundary.UpdatedAt, boundary.ID).
		Delete(&SystemTask{})
	return result.RowsAffected, result.Error
}

// ClaimSystemTask atomically acquires the per-type lease and moves a pending
// task to running under runnerID. A stale lease may be taken over with a CAS;
// the previous run is failed in the same transaction before the new claim is
// committed.
func ClaimSystemTask(id int64, taskType, runnerID string) (*SystemTask, bool, error) {
	var task SystemTask
	if err := DB.Where("id = ? AND type = ? AND status = ?", id, taskType, SystemTaskStatusPending).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}

	claimed := false
	var now int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		now, err = DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		lockUntil := now + int64(SystemTaskLeaseDuration.Seconds())
		acquired, expiredLock, err := acquireSystemTaskLock(tx, taskType, task.TaskID, runnerID, now, lockUntil)
		if err != nil || !acquired {
			return err
		}

		if expiredLock != nil && expiredLock.TaskID != "" && expiredLock.TaskID != task.TaskID {
			if err := markSystemTaskLeaseExpiredTx(tx, expiredLock.TaskID, now); err != nil {
				return err
			}
		}

		res := tx.Model(&SystemTask{}).
			Where("id = ? AND type = ? AND status = ?", id, taskType, SystemTaskStatusPending).
			Updates(map[string]any{
				"status":     SystemTaskStatusRunning,
				"locked_by":  runnerID,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Another claimant changed the task after our initial read. Release
			// only the exact lease acquired by this transaction.
			return releaseSystemTaskLockTx(tx, task.TaskID, runnerID, false)
		}
		claimed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !claimed {
		return nil, false, nil
	}
	task.Status = SystemTaskStatusRunning
	task.LockedBy = runnerID
	task.UpdatedAt = now
	return &task, true, nil
}

// acquireSystemTaskLock creates the per-type lock or atomically replaces an
// expired lock. It runs inside the claim transaction so stale takeover and the
// task status changes commit together. OnConflict/DoNothing is portable across
// SQLite, MySQL, and PostgreSQL and does not poison a PostgreSQL transaction on
// ordinary lock contention.
func acquireSystemTaskLock(tx *gorm.DB, taskType, taskID, runnerID string, now, lockUntil int64) (bool, *SystemTaskLock, error) {
	lock := &SystemTaskLock{
		Type:        taskType,
		TaskID:      taskID,
		LockedBy:    runnerID,
		LockedUntil: lockUntil,
		UpdatedAt:   now,
	}
	insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(lock)
	if insert.Error != nil {
		return false, nil, insert.Error
	}
	var existing SystemTaskLock
	if err := tx.Where("type = ?", taskType).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil, nil
		}
		return false, nil, err
	}
	// MySQL expresses DoNothing as an ON DUPLICATE KEY no-op. DSNs with
	// clientFoundRows=true may report that duplicate as one affected row, so
	// RowsAffected is not proof of ownership. Verify the persisted fencing token
	// before treating the insert as acquired.
	if existing.TaskID == taskID && existing.LockedBy == runnerID &&
		existing.LockedUntil == lockUntil {
		return true, nil, nil
	}
	if existing.LockedUntil >= now {
		return false, nil, nil
	}

	takeover := tx.Model(&SystemTaskLock{}).
		Where("type = ? AND task_id = ? AND locked_by = ? AND locked_until = ? AND locked_until < ?",
			existing.Type, existing.TaskID, existing.LockedBy, existing.LockedUntil, now).
		Updates(map[string]any{
			"task_id":      taskID,
			"locked_by":    runnerID,
			"locked_until": lockUntil,
			"updated_at":   now,
		})
	if takeover.Error != nil {
		return false, nil, takeover.Error
	}
	if takeover.RowsAffected == 0 {
		return false, nil, nil
	}
	return true, &existing, nil
}

// CompleteSystemTask marks a task succeeded and clears its active key.
func CompleteSystemTask(taskID, runnerID, result string) error {
	return finishSystemTask(taskID, runnerID, SystemTaskStatusSucceeded, result, "")
}

// FailSystemTask marks a task failed with an error message and clears its
// active key.
func FailSystemTask(taskID, runnerID, errMsg string) error {
	return finishSystemTask(taskID, runnerID, SystemTaskStatusFailed, "", errMsg)
}

// UpdateSystemTaskState records runner progress on a running task.
func UpdateSystemTaskState(taskID, runnerID, state string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		now, err := DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		update := tx.Model(&SystemTask{}).
			Where("task_id = ? AND status = ? AND locked_by = ?", taskID, SystemTaskStatusRunning, runnerID).
			Where(systemTaskCurrentLeaseSQL(), runnerID, now).
			Updates(map[string]any{"state": state, "updated_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 1 {
			return nil
		}
		if update.RowsAffected > 1 {
			return ErrSystemTaskTransitionRejected
		}

		// MySQL reports changed rows rather than matched rows by default. An
		// idempotent progress write within the same database-clock second can
		// therefore report zero even though the exact owner still holds a live
		// lease. Re-read the fenced state before classifying that result as a
		// lost lease; wrong-owner, expired, and stale writes still fail closed.
		var current SystemTask
		err = tx.Where(
			"task_id = ? AND status = ? AND locked_by = ? AND state = ?",
			taskID, SystemTaskStatusRunning, runnerID, state,
		).Where(systemTaskCurrentLeaseSQL(), runnerID, now).First(&current).Error
		if err == nil {
			return nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSystemTaskTransitionRejected
		}
		return err
	})
}

// RenewSystemTaskLock extends the exact task/runner lease only while it is
// unexpired and its task is still running. A failed CAS means the worker must
// cancel promptly and must not write progress or a terminal state.
func RenewSystemTaskLock(taskID, runnerID string, _ int64) error {
	return RenewSystemTaskLockContext(context.Background(), taskID, runnerID, 0)
}

// RenewSystemTaskLockContext is the cancellable form used by process-long
// workers so a stalled database cannot keep a stale handler alive past its
// lease window.
func RenewSystemTaskLockContext(ctx context.Context, taskID, runnerID string, _ int64) error {
	if ctx == nil {
		return errors.New("system task renewal context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db := DB.WithContext(ctx)
	now, err := DatabaseUnixTimestamp(db)
	if err != nil {
		return err
	}
	lockUntil := now + int64(SystemTaskLeaseDuration.Seconds())
	update := db.Model(&SystemTaskLock{}).
		Where("task_id = ? AND locked_by = ? AND locked_until >= ?", taskID, runnerID, now).
		Where("EXISTS (SELECT 1 FROM system_tasks WHERE system_tasks.task_id = system_task_locks.task_id AND system_tasks.status = ? AND system_tasks.locked_by = ?)", SystemTaskStatusRunning, runnerID).
		Updates(map[string]any{"locked_until": lockUntil, "updated_at": now})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return ErrSystemTaskTransitionRejected
	}
	return nil
}

// ReleaseSystemTaskLock releases only the lease owned by taskID/runnerID.
func ReleaseSystemTaskLock(taskID, runnerID string) error {
	return releaseSystemTaskLockTx(DB, taskID, runnerID, true)
}

func releaseSystemTaskLockTx(tx *gorm.DB, taskID, runnerID string, requireOwner bool) error {
	result := tx.Where("task_id = ? AND locked_by = ?", taskID, runnerID).Delete(&SystemTaskLock{})
	if result.Error != nil {
		return result.Error
	}
	if requireOwner && result.RowsAffected != 1 {
		return ErrSystemTaskTransitionRejected
	}
	return nil
}

func finishSystemTask(taskID, runnerID, status, result, errMsg string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		now, err := DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		update := tx.Model(&SystemTask{}).
			Where("task_id = ? AND status = ? AND locked_by = ?", taskID, SystemTaskStatusRunning, runnerID).
			Where(systemTaskCurrentLeaseSQL(), runnerID, now).
			Updates(map[string]any{
				"status":     status,
				"active_key": nil,
				"result":     result,
				"error":      errMsg,
				"updated_at": now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrSystemTaskTransitionRejected
		}
		// Releasing in the same transaction prevents a terminal task from
		// retaining the per-type lock and makes a release failure roll back the
		// terminal transition.
		return releaseSystemTaskLockTx(tx, taskID, runnerID, true)
	})
}

func systemTaskCurrentLeaseSQL() string {
	return "EXISTS (SELECT 1 FROM system_task_locks WHERE system_task_locks.type = system_tasks.type AND system_task_locks.task_id = system_tasks.task_id AND system_task_locks.locked_by = ? AND system_task_locks.locked_until >= ?)"
}

// MarkSystemTaskLeaseExpired fails a running task only when its persisted lock
// row is expired. A task UpdatedAt value is never used as a lease surrogate.
func MarkSystemTaskLeaseExpired(taskID string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		now, err := DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var lock SystemTaskLock
		if err := tx.Where("task_id = ? AND locked_until < ?", taskID, now).First(&lock).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return expireSystemTaskLockTx(tx, &lock, now)
	})
}

// ExpireStaleSystemTaskLocks fails running tasks represented by expired lock
// rows. Deleting the lock and failing its run share a transaction; a concurrent
// heartbeat/takeover wins the lock CAS and prevents stale cleanup from touching
// the task.
func ExpireStaleSystemTaskLocks() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		now, err := DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var locks []*SystemTaskLock
		// Generic RunWithLease rows intentionally have no task_id and manage
		// their own stale takeover in internal/operations/task.go. Leave those independent
		// leases alone here.
		if err := tx.Where("task_id <> ? AND locked_until < ?", "", now).Find(&locks).Error; err != nil {
			return err
		}
		for _, lock := range locks {
			if err := expireSystemTaskLockTx(tx, lock, now); err != nil {
				return err
			}
		}
		return expireOrphanedSystemTasksTx(tx, "", now)
	})
}

// ExpireStaleSystemTaskLockType is the scoped form used by independently
// dispatched periodic jobs. It avoids every scheduler worker sweeping and
// contending on the entire lock table at the same time.
func ExpireStaleSystemTaskLockType(taskType string) error {
	if strings.TrimSpace(taskType) == "" {
		return errors.New("system task type is required")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		now, err := DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var lock SystemTaskLock
		err = tx.Where("type = ? AND task_id <> ? AND locked_until < ?", taskType, "", now).
			First(&lock).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return expireOrphanedSystemTasksTx(tx, taskType, now)
		}
		if err != nil {
			return err
		}
		if err := expireSystemTaskLockTx(tx, &lock, now); err != nil {
			return err
		}
		return expireOrphanedSystemTasksTx(tx, taskType, now)
	})
}

func expireOrphanedSystemTasksTx(tx *gorm.DB, taskType string, now int64) error {
	query := tx.Model(&SystemTask{}).
		Where("status = ? AND active_key IS NOT NULL", SystemTaskStatusRunning).
		Where("NOT EXISTS (SELECT 1 FROM system_task_locks WHERE system_task_locks.type = system_tasks.type AND system_task_locks.task_id = system_tasks.task_id AND system_task_locks.locked_by = system_tasks.locked_by)")
	if taskType != "" {
		query = query.Where("type = ?", taskType)
	}
	return query.Updates(map[string]any{
		"status":     SystemTaskStatusFailed,
		"active_key": nil,
		"error":      "task lease missing",
		"updated_at": now,
	}).Error
}

func expireSystemTaskLockTx(tx *gorm.DB, lock *SystemTaskLock, now int64) error {
	deleted := tx.Where("type = ? AND task_id = ? AND locked_by = ? AND locked_until = ? AND locked_until < ?",
		lock.Type, lock.TaskID, lock.LockedBy, lock.LockedUntil, now).
		Delete(&SystemTaskLock{})
	if deleted.Error != nil {
		return deleted.Error
	}
	if deleted.RowsAffected == 0 {
		return nil
	}
	return markSystemTaskLeaseExpiredTx(tx, lock.TaskID, now)
}

func markSystemTaskLeaseExpiredTx(tx *gorm.DB, taskID string, now int64) error {
	if taskID == "" {
		return nil
	}
	return tx.Model(&SystemTask{}).
		Where("task_id = ? AND status = ?", taskID, SystemTaskStatusRunning).
		Updates(map[string]any{
			"status":     SystemTaskStatusFailed,
			"active_key": nil,
			"error":      "task lease expired",
			"updated_at": now,
		}).Error
}

// SystemTaskResponse is the admin-facing task shape; payload/state/result
// are JSON-decoded when possible (reference contract).
type SystemTaskResponse struct {
	ID        int64   `json:"id"`
	TaskID    string  `json:"task_id"`
	Type      string  `json:"type"`
	Status    string  `json:"status"`
	ActiveKey *string `json:"active_key,omitempty"`
	Payload   any     `json:"payload"`
	State     any     `json:"state"`
	Result    any     `json:"result"`
	Error     string  `json:"error"`
	LockedBy  string  `json:"locked_by"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// decodeSystemTaskJSONValue decodes a JSON payload cell; unparseable values
// are returned verbatim (reference behavior).
func decodeSystemTaskJSONValue(data string) any {
	if data == "" {
		return nil
	}
	var value any
	if err := jsonutil.UnmarshalJsonStr(data, &value); err != nil {
		return data
	}
	return value
}

// ToResponse converts the row into the admin-facing response shape.
func (task *SystemTask) ToResponse() SystemTaskResponse {
	return SystemTaskResponse{
		ID:        task.ID,
		TaskID:    task.TaskID,
		Type:      task.Type,
		Status:    task.Status,
		ActiveKey: task.ActiveKey,
		Payload:   decodeSystemTaskJSONValue(task.Payload),
		State:     decodeSystemTaskJSONValue(task.State),
		Result:    decodeSystemTaskJSONValue(task.Result),
		Error:     task.Error,
		LockedBy:  task.LockedBy,
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
}

// ListSystemTasks returns the newest operator tasks plus failed internal
// maintenance runs (default 20, capped at 100). Successful periodic history is
// intentionally hidden, but a durable failure ID and redacted error must remain
// discoverable to administrators.
func ListSystemTasks(limit int) ([]*SystemTask, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var tasks []*SystemTask
	err := visibleSystemTasksQuery(DB.Model(&SystemTask{})).
		Order("id desc").Limit(limit).Find(&tasks).Error
	return tasks, err
}
