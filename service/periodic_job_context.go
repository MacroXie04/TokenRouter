package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
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

// BackfillLegacySubscriptionEntitlementSnapshotsContext is the cancellable
// form of BackfillLegacySubscriptionEntitlementSnapshots. Each candidate keeps
// its original independent transaction boundary.
func BackfillLegacySubscriptionEntitlementSnapshotsContext(ctx context.Context, limit int) error {
	if ctx == nil {
		return errors.New("subscription backfill context is nil")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	type legacySubscription struct {
		ID     int
		UserID int
	}
	db := model.DB.WithContext(ctx)
	var candidates []legacySubscription
	if err := db.Model(&model.UserSubscription{}).
		Select("id", "user_id").
		Where("entitlement_migration_state IN ?", []string{"", SubscriptionEntitlementMigrationPending}).
		Order("id asc").Limit(limit).Scan(&candidates).Error; err != nil {
		return err
	}
	errs := make([]error, 0)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			now, err := model.DatabaseUnixTimestamp(tx)
			if err != nil {
				return err
			}
			var user model.User
			if err := subscriptionLockForUpdate(tx).Select("id").Where("id = ?", candidate.UserID).First(&user).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				return tx.Model(&model.UserSubscription{}).
					Where("id = ? AND user_id = ? AND entitlement_migration_state IN ?",
						candidate.ID, candidate.UserID, []string{"", SubscriptionEntitlementMigrationPending}).
					Updates(map[string]any{
						"entitlement_migration_state": SubscriptionEntitlementMigrationReview,
						"updated_at":                  now,
					}).Error
			}
			var sub model.UserSubscription
			if err := subscriptionLockForUpdate(tx).Where("id = ?", candidate.ID).First(&sub).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if sub.UserId != candidate.UserID {
				return ErrSubscriptionStateChanged
			}
			if sub.EntitlementVersion >= UserSubscriptionEntitlementVersion {
				return tx.Model(&model.UserSubscription{}).
					Where("id = ? AND entitlement_migration_state IN ?", sub.Id, []string{"", SubscriptionEntitlementMigrationPending}).
					Updates(map[string]any{
						"entitlement_migration_state": SubscriptionEntitlementMigrationBackfilled,
						"updated_at":                  now,
					}).Error
			}
			if sub.EntitlementMigrationState != "" && sub.EntitlementMigrationState != SubscriptionEntitlementMigrationPending {
				return nil
			}
			_, err = subscriptionResetPlanTx(tx, &sub)
			if errors.Is(err, ErrSubscriptionEntitlementSnapshotMissing) {
				return tx.Model(&model.UserSubscription{}).
					Where("id = ? AND entitlement_version = ? AND entitlement_migration_state IN ?",
						sub.Id, 0, []string{"", SubscriptionEntitlementMigrationPending}).
					Updates(map[string]any{
						"entitlement_migration_state": SubscriptionEntitlementMigrationReview,
						"updated_at":                  now,
					}).Error
			}
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("backfill subscription %d: %w", candidate.ID, err))
		}
	}
	return errors.Join(errs...)
}

// ExpireDueSubscriptionsContext is ExpireDueSubscriptions with cancellation
// applied to every scan and per-user transaction.
func ExpireDueSubscriptionsContext(ctx context.Context, limit int) error {
	if ctx == nil {
		return errors.New("subscription expiry context is nil")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	db := model.DB.WithContext(ctx)
	errs := make([]error, 0)
	lastUserID := 0
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		now, err := model.DatabaseUnixTimestamp(db)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		var userIDs []int
		if err := db.Model(&model.UserSubscription{}).
			Distinct("user_id").
			Where("user_id > ? AND status = ? AND end_time <= ?", lastUserID, SubscriptionStatusActive, now).
			Order("user_id asc").Limit(limit).Pluck("user_id", &userIDs).Error; err != nil {
			return errors.Join(append(errs, err)...)
		}
		if len(userIDs) == 0 {
			break
		}
		for _, userID := range userIDs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			lastUserID = userID
			err := db.Transaction(func(tx *gorm.DB) error {
				transactionNow, err := model.DatabaseUnixTimestamp(tx)
				if err != nil {
					return err
				}
				var user model.User
				if err := subscriptionLockForUpdate(tx).Select("id").Where("id = ?", userID).First(&user).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return nil
					}
					return err
				}
				_, _, err = expireDueSubscriptionsForUserTx(tx, userID, transactionNow)
				return err
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("expire subscriptions for user %d: %w", userID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ResetDueSubscriptionQuotasContext is ResetDueSubscriptionQuotas with a
// cancellable initial scan and cancellable per-subscription transactions.
func ResetDueSubscriptionQuotasContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("subscription reset context is nil")
	}
	db := model.DB.WithContext(ctx)
	now, err := model.DatabaseUnixTimestamp(db)
	if err != nil {
		return err
	}
	var subs []model.UserSubscription
	if err := db.Where("status = ? AND next_reset_time > 0 AND next_reset_time <= ?",
		SubscriptionStatusActive, now).Order("id asc").Find(&subs).Error; err != nil {
		return err
	}
	resetErrors := make([]error, 0)
	for i := range subs {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(resetErrors, err)...)
		}
		if err := ResetSubscriptionQuotaContext(ctx, &subs[i]); err != nil {
			resetErrors = append(resetErrors, fmt.Errorf("reset subscription %d: %w", subs[i].Id, err))
		}
	}
	return errors.Join(resetErrors...)
}

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
