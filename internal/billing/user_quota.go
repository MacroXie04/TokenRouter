package billing

import (
	"errors"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
)

// ErrUserQuotaOverflow is returned when a credit cannot be represented by the
// platform's quota column without wrapping. Financial callers must leave their
// accompanying ledger/order mutation uncommitted when this occurs.
var ErrUserQuotaOverflow = errors.New("user quota overflow")

// IncreaseUserQuota adds quota to a user atomically.
func IncreaseUserQuota(id int, quota int) error {
	if quota == 0 {
		return nil
	}
	if id <= 0 || !quotamath.QuotaWithinBounds(quota) {
		return errors.New("invalid user quota credit")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return increaseUserQuotaTx(tx, id, quota)
	})
}

// increaseUserQuotaTx performs a checked credit in the caller's transaction.
// Locking is enabled on MySQL/PostgreSQL; SQLite serializes the write and the
// checked update remains in the same transaction as its calling ledger change.
func increaseUserQuotaTx(tx *gorm.DB, id, quota int) error {
	if tx == nil || id <= 0 || quota <= 0 || !quotamath.QuotaWithinBounds(quota) {
		return errors.New("invalid user quota credit")
	}
	var user model.User
	if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota").First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return userssvc.ErrUserNotFound
		}
		return err
	}
	newQuota, ok := quotamath.AddQuotaWithinBounds(user.Quota, quota)
	if !ok {
		return ErrUserQuotaOverflow
	}
	result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", id, user.Quota).
		UpdateColumn("quota", newQuota)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return userssvc.ErrUserNotFound
	}
	return nil
}

// DecreaseUserQuota subtracts used quota and increments used_quota atomically,
// guarded so concurrent deductions can never drive quota negative.
func DecreaseUserQuota(id int, quota int) error {
	if quota == 0 {
		return nil
	}
	if id <= 0 || quota < 0 || int64(quota) > quotamath.MaxQuota {
		return ErrInvalidQuota
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota", "used_quota").First(&user, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}
		if !quotamath.QuotaWithinBounds(user.Quota) {
			return ErrUserQuotaOverflow
		}
		if user.Quota < quota {
			return ErrInsufficientQuota
		}
		newUsedQuota, ok := quotamath.AddQuotaWithinBounds(user.UsedQuota, quota)
		if !ok {
			return ErrUserUsageOverflow
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND quota = ? AND used_quota = ?", id, user.Quota, user.UsedQuota).
			Updates(map[string]any{
				"quota":      user.Quota - quota,
				"used_quota": newUsedQuota,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("concurrent user quota update")
		}
		return nil
	})
}

// SetUserQuota replaces the usable balance with a bounded value. It is used
// by administrative overrides so a malformed request cannot persist negative
// or database-dependent quota values.
func SetUserQuota(id, quota int) error {
	if id <= 0 || quota < 0 || int64(quota) > quotamath.MaxQuota {
		return ErrInvalidQuota
	}
	result := model.DB.Model(&model.User{}).Where("id = ?", id).UpdateColumn("quota", quota)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return userssvc.ErrUserNotFound
	}
	return nil
}

// UpdateUserRequestCount increments the user's request counter.
func UpdateUserRequestCount(id int, count int) error {
	if id <= 0 || count < 0 || int64(count) > quotamath.MaxQuota {
		return ErrInvalidQuota
	}
	if count == 0 {
		return nil
	}
	result := model.DB.Model(&model.User{}).
		Where("id = ? AND request_count >= 0 AND request_count <= ?", id, quotamath.MaxQuota-int64(count)).
		UpdateColumn("request_count", locking.GormExpr("request_count + ?", count))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrUserUsageOverflow
	}
	return nil
}
