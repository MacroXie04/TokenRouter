package service

import (
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// AcquireTaskLock acquires a distributed lease for a task type, keyed by the
// node name. A stale (expired) lock is atomically taken over so a crashed node
// does not block the task forever.
func AcquireTaskLock(taskType, lockBy string, ttl time.Duration) (bool, error) {
	now := common.NowTimestamp()
	lockUntil := now + int64(ttl.Seconds())

	var lock model.SystemTaskLock
	err := model.DB.Where("type = ?", taskType).First(&lock).Error
	if err != nil {
		// No existing lock: insert. A concurrent insert fails the unique key.
		lock = model.SystemTaskLock{Type: taskType, LockedBy: lockBy, LockedUntil: lockUntil, UpdatedAt: now}
		if cerr := model.DB.Create(&lock).Error; cerr != nil {
			return false, nil
		}
		return true, nil
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
	return model.DB.Where("type = ? AND locked_by = ?", taskType, lockBy).
		Delete(&model.SystemTaskLock{}).Error
}

// RunWithLease runs fn under a distributed lease; if another node holds the
// lease, fn is skipped. The node name is read from NODE_NAME.
func RunWithLease(taskType string, ttl time.Duration, fn func() error) {
	nodeName := common.GetEnv("NODE_NAME", "tokenrouter-node-1")
	acquired, err := AcquireTaskLock(taskType, nodeName, ttl)
	if err != nil || !acquired {
		return
	}
	defer func() { _ = ReleaseTaskLock(taskType, nodeName) }()

	task := model.SystemTask{
		TaskID:    common.GenerateUUID(),
		Type:      taskType,
		Status:    "running",
		LockedBy:  nodeName,
		CreatedAt: common.NowTimestamp(),
		UpdatedAt: common.NowTimestamp(),
	}
	_ = model.DB.Create(&task).Error

	err = fn()
	now := common.NowTimestamp()
	updates := map[string]any{"status": "succeeded", "updated_at": now}
	if err != nil {
		updates["status"] = "failed"
		updates["error"] = err.Error()
	}
	_ = model.DB.Model(&task).Updates(updates).Error
}
