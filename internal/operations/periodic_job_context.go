package operations

import (
	"context"
	"errors"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

// CleanupExpiredLogsContext is CleanupExpiredLogs with a cancellable delete.
func CleanupExpiredLogsContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("log cleanup context is nil")
	}
	retention, enabled, err := logRetentionDays()
	if err != nil {
		return err
	}
	if !enabled {
		return ctx.Err()
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(ctx)
	if err != nil {
		return err
	}
	cutoff := now - retention*24*60*60
	const batchSize = 1_000
	for {
		deleted, err := model.DeleteOldLogBatch(ctx, cutoff, batchSize)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
	}
}

// EnqueueExpiredLogCleanupContext routes automatic retention through the same
// active-key/lease domain as operator-triggered log cleanup. This prevents two
// independent tasks from racing over the same relational rows or ClickHouse
// mutation while retaining durable progress and failure visibility.
func EnqueueExpiredLogCleanupContext(ctx context.Context) error {
	return enqueueExpiredLogCleanupContextWithClock(ctx, model.PrimaryDatabaseUnixTimestamp)
}

type logRetentionDatabaseClock func(context.Context) (int64, error)

func enqueueExpiredLogCleanupContextWithClock(ctx context.Context, clock logRetentionDatabaseClock) error {
	if ctx == nil {
		return errors.New("log cleanup context is nil")
	}
	if clock == nil {
		return errors.New("log cleanup clock is nil")
	}
	retention, enabled, err := logRetentionDays()
	if err != nil {
		return err
	}
	if !enabled {
		return ctx.Err()
	}
	now, err := clock(ctx)
	if err != nil {
		return err
	}
	cutoff := now - retention*24*60*60
	// A valid horizon can predate Unix epoch (for example the documented
	// 36,500-day maximum today). There cannot be an application log before
	// epoch, so do not enqueue a task whose worker would reject the meaningless
	// non-positive target.
	if cutoff <= 0 {
		return nil
	}
	_, _, err = EnqueueSystemTaskContext(ctx, model.SystemTaskTypeLogCleanup, logCleanupPayload{
		TargetTimestamp: cutoff,
		BatchSize:       logCleanupBatchSize,
	})
	return err
}
