package operations

import (
	"context"
	"errors"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

// EnqueueSystemTaskContext is EnqueueSystemTask's cancellable form. It keeps
// the same active-key deduplication contract while applying ctx to every
// database lookup and insert.
func EnqueueSystemTaskContext(ctx context.Context, taskType string, payload any) (*model.SystemTask, bool, error) {
	if ctx == nil {
		return nil, false, errors.New("system-task context is nil")
	}
	activeTask, err := model.GetActiveSystemTaskContext(ctx, taskType)
	if err != nil {
		return nil, false, err
	}
	if activeTask != nil {
		return activeTask, false, nil
	}

	task, err := model.CreateSystemTaskContext(ctx, taskType, payload, nil)
	if err != nil {
		activeTask, activeErr := model.GetActiveSystemTaskContext(ctx, taskType)
		if activeErr == nil && activeTask != nil {
			return activeTask, false, nil
		}
		return nil, false, err
	}
	notifySystemTaskRunner()
	return task, true, nil
}
