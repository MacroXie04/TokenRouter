package auth

import (
	"context"
	"errors"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"time"
)

// CleanupAuthFlowsContext removes consumed and expired authentication flows
// with the same 24-hour retention used by the periodic cleanup job.
func CleanupAuthFlowsContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("auth-flow cleanup context is nil")
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(ctx)
	if err != nil {
		return err
	}
	return cleanupAuthFlowsAt(ctx, now)
}

func cleanupAuthFlowsAt(ctx context.Context, now int64) error {
	if ctx == nil || now <= 0 {
		return errors.New("auth-flow cleanup clock is invalid")
	}
	return model.DeleteExpiredAuthFlowsContext(ctx, time.Unix(now, 0).UTC().Add(-24*time.Hour))
}
