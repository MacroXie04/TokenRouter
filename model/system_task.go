package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

// System task statuses (reference contract).
const (
	SystemTaskStatusPending   = "pending"
	SystemTaskStatusRunning   = "running"
	SystemTaskStatusSucceeded = "succeeded"
	SystemTaskStatusFailed    = "failed"
)

// System task types.
const (
	SystemTaskTypeChannelTest = "channel_test"
	SystemTaskTypeLogCleanup  = "log_cleanup"
)

// systemTaskLockDuration is how long a claimed task may run before its lease
// can be taken over (reference: 24h).
const systemTaskLockDuration = 24 * time.Hour

// activeSystemTaskStatuses returns the statuses that count as "active".
func activeSystemTaskStatuses() []string {
	return []string{SystemTaskStatusPending, SystemTaskStatusRunning}
}

// GenerateSystemTaskID returns a unique 32-hex-char task id.
func GenerateSystemTaskID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// CreateSystemTask persists a pending task. ActiveKey is set to the task type
// so concurrent enqueues of the same type fail the unique index and fall back
// to the already-active task.
func CreateSystemTask(taskType string, payload, state any) (*SystemTask, error) {
	taskID, err := GenerateSystemTaskID()
	if err != nil {
		return nil, err
	}
	payloadBytes, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	payloadText := string(payloadBytes)
	stateText := ""
	if state != nil {
		stateBytes, err := common.Marshal(state)
		if err != nil {
			return nil, err
		}
		stateText = string(stateBytes)
	}
	task := &SystemTask{
		TaskID:    taskID,
		Type:      taskType,
		Status:    SystemTaskStatusPending,
		ActiveKey: &taskType,
		Payload:   payloadText,
		State:     stateText,
		CreatedAt: common.NowTimestamp(),
		UpdatedAt: common.NowTimestamp(),
	}
	if err := DB.Create(task).Error; err != nil {
		return nil, err
	}
	return task, nil
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
	var task SystemTask
	err := DB.Where("type = ? AND status IN ?", taskType, activeSystemTaskStatuses()).
		Order("id desc").
		First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
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

// ClaimSystemTask atomically moves a pending task to running under the given
// runner. The claim is lease-guarded: a stale running task whose lease expired
// is failed first, mirroring the reference lock semantics.
func ClaimSystemTask(id int64, taskType, runnerID string) (*SystemTask, bool, error) {
	now := common.NowTimestamp()
	var task SystemTask
	if err := DB.Where("id = ? AND type = ? AND status = ?", id, taskType, SystemTaskStatusPending).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}

	// Claim is a compare-and-swap on the pending status, so concurrent runners
	// cannot double-claim.
	res := DB.Model(&SystemTask{}).
		Where("id = ? AND type = ? AND status = ?", id, taskType, SystemTaskStatusPending).
		Updates(map[string]any{
			"status":     SystemTaskStatusRunning,
			"locked_by":  runnerID,
			"updated_at": now,
		})
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, false, nil
	}
	task.Status = SystemTaskStatusRunning
	task.LockedBy = runnerID
	task.UpdatedAt = now
	return &task, true, nil
}

// CompleteSystemTask marks a task succeeded and clears its active key.
func CompleteSystemTask(taskID, result string) error {
	return DB.Model(&SystemTask{}).
		Where("task_id = ? AND status = ?", taskID, SystemTaskStatusRunning).
		Updates(map[string]any{
			"status":     SystemTaskStatusSucceeded,
			"active_key": nil,
			"result":     result,
			"updated_at": common.NowTimestamp(),
		}).Error
}

// FailSystemTask marks a task failed with an error message and clears its
// active key.
func FailSystemTask(taskID, errMsg string) error {
	return DB.Model(&SystemTask{}).
		Where("task_id = ? AND status = ?", taskID, SystemTaskStatusRunning).
		Updates(map[string]any{
			"status":     SystemTaskStatusFailed,
			"active_key": nil,
			"error":      errMsg,
			"updated_at": common.NowTimestamp(),
		}).Error
}

// UpdateSystemTaskState records runner progress on a running task.
func UpdateSystemTaskState(taskID, state string) error {
	return DB.Model(&SystemTask{}).
		Where("task_id = ? AND status = ?", taskID, SystemTaskStatusRunning).
		Updates(map[string]any{"state": state, "updated_at": common.NowTimestamp()}).Error
}

// MarkSystemTaskLeaseExpired fails a running task whose lease ran out.
func MarkSystemTaskLeaseExpired(taskID string) error {
	return DB.Model(&SystemTask{}).
		Where("task_id = ? AND status = ?", taskID, SystemTaskStatusRunning).
		Updates(map[string]any{
			"status":     SystemTaskStatusFailed,
			"active_key": nil,
			"error":      "task lease expired",
			"updated_at": common.NowTimestamp(),
		}).Error
}

// ExpireStaleSystemTaskLocks fails running tasks whose lease duration has
// passed (a crashed runner's tasks become retryable).
func ExpireStaleSystemTaskLocks() error {
	cutoff := common.NowTimestamp() - int64(systemTaskLockDuration.Seconds())
	return DB.Model(&SystemTask{}).
		Where("status = ? AND updated_at < ?", SystemTaskStatusRunning, cutoff).
		Updates(map[string]any{
			"status":     SystemTaskStatusFailed,
			"active_key": nil,
			"error":      "task lease expired",
			"updated_at": common.NowTimestamp(),
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
	if err := common.UnmarshalJsonStr(data, &value); err != nil {
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

// ListSystemTasks returns the newest task rows (default 20, capped at 100).
func ListSystemTasks(limit int) ([]*SystemTask, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var tasks []*SystemTask
	err := DB.Order("id desc").Limit(limit).Find(&tasks).Error
	return tasks, err
}
