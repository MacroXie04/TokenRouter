package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

// DeleteExpiredAuthFlowsContext removes consumed flows and flows older than
// cutoff using the caller's cancellation boundary.
func DeleteExpiredAuthFlowsContext(ctx context.Context, cutoff time.Time) error {
	if ctx == nil {
		return errors.New("auth-flow cleanup context is nil")
	}
	return DB.WithContext(ctx).
		Where("consumed_at IS NOT NULL OR expires_at < ?", cutoff).
		Delete(&AuthFlow{}).Error
}

// GetActiveSystemTaskContext is GetActiveSystemTask with a cancellable query.
func GetActiveSystemTaskContext(ctx context.Context, taskType string) (*SystemTask, error) {
	if ctx == nil {
		return nil, errors.New("system-task context is nil")
	}
	var task SystemTask
	err := DB.WithContext(ctx).
		Where("type = ? AND status IN ?", taskType, activeSystemTaskStatuses()).
		Order("id desc").First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

// CreateSystemTaskContext is CreateSystemTask with a cancellable insert. The
// random task id and payload are prepared before the database mutation.
func CreateSystemTaskContext(ctx context.Context, taskType string, payload, state any) (*SystemTask, error) {
	if ctx == nil {
		return nil, errors.New("system-task context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db := DB.WithContext(ctx)
	now, err := DatabaseUnixTimestamp(db)
	if err != nil {
		return nil, err
	}
	task := &SystemTask{
		TaskID: taskID, Type: taskType, Status: SystemTaskStatusPending,
		ActiveKey: &taskType, Payload: payloadText, State: stateText,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(task).Error; err != nil {
		return nil, err
	}
	return task, nil
}
