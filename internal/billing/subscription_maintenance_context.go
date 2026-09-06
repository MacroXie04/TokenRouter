package billing

import (
	"context"
	"errors"
	"fmt"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
)

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
			if err := locking.SubscriptionLockForUpdate(tx).Select("id").Where("id = ?", candidate.UserID).First(&user).Error; err != nil {
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
			if err := locking.SubscriptionLockForUpdate(tx).Where("id = ?", candidate.ID).First(&sub).Error; err != nil {
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
				if err := locking.SubscriptionLockForUpdate(tx).Select("id").Where("id = ?", userID).First(&user).Error; err != nil {
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
