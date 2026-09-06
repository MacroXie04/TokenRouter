package billing

import (
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
)

// ErrInsufficientQuota is returned when pre-consume would overspend.
var ErrInsufficientQuota = errors.New("insufficient quota")

// ErrInvalidQuota rejects negative or out-of-policy accounting inputs before
// they can turn a debit into a credit or overflow a database quota column.
var ErrInvalidQuota = errors.New("invalid quota")

// ErrUserUsageOverflow reports that lifetime usage/request counters cannot be
// incremented without exceeding the platform's persisted quota bounds.
var ErrUserUsageOverflow = errors.New("user usage overflow")

// PreConsumeUserQuota atomically deducts quota from the user if sufficient.
func PreConsumeUserQuota(userId int, quota int) error {
	if userId <= 0 {
		return userssvc.ErrUserNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	res := model.DB.Model(&model.User{}).
		Where("id = ? AND quota >= ? AND quota <= ?", userId, quota, quotamath.MaxQuota).
		UpdateColumn("quota", locking.GormExpr("quota - ?", quota))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInsufficientQuota
	}
	return nil
}

// RefundUserQuota adds quota back to the user (failure rollback / over-deduct).
func RefundUserQuota(userId int, quota int) error {
	if userId <= 0 {
		return userssvc.ErrUserNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}
		newQuota, ok := quotamath.AddQuotaWithinBounds(user.Quota, quota)
		if !ok {
			return ErrUserQuotaOverflow
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", userId, user.Quota).
			UpdateColumn("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return userssvc.ErrUserNotFound
		}
		return nil
	})
}

// SettleUserQuota adjusts the user's quota from a pre-consumed reservation to
// the actual usage: refunds the over-reservation, deducts the shortfall, and
// records used_quota + request count exactly once. This is the pre-consume then
// settle accounting pattern; it must never double-charge.
func SettleUserQuota(userId, reserved, actual int) error {
	if userId <= 0 {
		return userssvc.ErrUserNotFound
	}
	if err := validateQuotaAmount(reserved); err != nil {
		return fmt.Errorf("reserved quota: %w", err)
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).
			Select("id", "quota", "used_quota", "request_count").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}

		if !quotamath.QuotaWithinBounds(user.Quota) {
			return ErrUserQuotaOverflow
		}
		newQuota := user.Quota
		switch {
		case reserved > actual:
			var ok bool
			newQuota, ok = quotamath.AddQuotaWithinBounds(user.Quota, reserved-actual)
			if !ok {
				return ErrUserQuotaOverflow
			}
		case actual > reserved:
			debit := actual - reserved
			if user.Quota < debit {
				return ErrInsufficientQuota
			}
			newQuota = user.Quota - debit
		}
		newUsedQuota, usageOK := quotamath.AddQuotaWithinBounds(user.UsedQuota, actual)
		newRequestCount, countOK := quotamath.AddQuotaWithinBounds(user.RequestCount, 1)
		if !usageOK || !countOK {
			return ErrUserUsageOverflow
		}

		updates := map[string]any{
			"quota":         newQuota,
			"used_quota":    newUsedQuota,
			"request_count": newRequestCount,
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND quota = ? AND used_quota = ? AND request_count = ?",
				userId, user.Quota, user.UsedQuota, user.RequestCount).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return userssvc.ErrUserNotFound
		}
		return nil
	})
}

// RecordUserUsage adds actual usage to the user's lifetime counters
// (used_quota and request count), regardless of which funding source paid.
func RecordUserUsage(userId, actual int) error {
	if userId <= 0 {
		return userssvc.ErrUserNotFound
	}
	if err := validateQuotaAmount(actual); err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).
			Select("id", "used_quota", "request_count").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return userssvc.ErrUserNotFound
			}
			return err
		}
		newUsedQuota, usageOK := quotamath.AddQuotaWithinBounds(user.UsedQuota, actual)
		newRequestCount, countOK := quotamath.AddQuotaWithinBounds(user.RequestCount, 1)
		if !usageOK || !countOK {
			return ErrUserUsageOverflow
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND used_quota = ? AND request_count = ?", userId, user.UsedQuota, user.RequestCount).
			Updates(map[string]any{
				"used_quota":    newUsedQuota,
				"request_count": newRequestCount,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return userssvc.ErrUserNotFound
		}
		return nil
	})
}

func validateQuotaAmount(quota int) error {
	if quota < 0 || int64(quota) > quotamath.MaxQuota {
		return fmt.Errorf("%w: %d", ErrInvalidQuota, quota)
	}
	return nil
}

// PostConsumeTokenQuota increments the token used quota after actual usage.
func PostConsumeTokenQuota(tokenId int, quota int) error {
	return IncreaseTokenUsedQuota(tokenId, quota)
}
