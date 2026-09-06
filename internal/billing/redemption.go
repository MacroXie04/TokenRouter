package billing

import (
	"errors"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"strconv"
	"unicode/utf8"
)

// Redemption status constants (reference numbering: 0 is never used).
const (
	RedemptionStatusEnabled  = 1
	RedemptionStatusDisabled = 2
	RedemptionStatusUsed     = 3
)

// ErrInvalidRedemption is returned when a redemption key is unknown/unusable.
var ErrInvalidRedemption = errors.New("无效的兑换码")

const maxRedemptionBatchSize = 100

// randomRedemptionKey returns a cryptographically random 32-char hex key (the
// reference key width). Entropy failure is propagated so no redeemable value
// is created from predictable fallback material.
func randomRedemptionKey() (string, error) {
	return cryptoutil.GenerateKey(16)
}

// CreateRedemptionBatch creates count redemption codes owned by userId with
// random 32-hex keys and returns the generated keys (reference contract).
func CreateRedemptionBatch(userId int, name string, quota int, expiredTime int64, count int) ([]string, error) {
	if userId <= 0 || !validRedemptionPayload(name, quota, expiredTime) ||
		count <= 0 || count > maxRedemptionBatchSize {
		return nil, errors.New("invalid redemption batch")
	}
	keys := make([]string, 0, count)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		createdAt := wallclock.NowTimestamp()
		for i := 0; i < count; i++ {
			key, err := randomRedemptionKey()
			if err != nil {
				return err
			}
			r := model.Redemption{
				UserId:      userId,
				Key:         key,
				Status:      RedemptionStatusEnabled,
				Name:        name,
				Quota:       quota,
				CreatedTime: createdAt,
				ExpiredTime: expiredTime,
			}
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// GetPagedRedemptions lists redemption codes newest-first with the reference
// pageInfo paging.
func GetPagedRedemptions(offset, limit int) ([]model.Redemption, int64, error) {
	var total int64
	if err := model.DB.Model(&model.Redemption{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []model.Redemption
	if err := model.DB.Order("id desc").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// SearchRedemptions searches redemption codes: a numeric keyword also matches
// the id, otherwise the name is prefix-matched. The status filter accepts
// "expired", "1" (enabled, unexpired), "2" (disabled) and "3" (used).
func SearchRedemptions(keyword, status string, offset, limit int) ([]model.Redemption, int64, error) {
	query := model.DB.Model(&model.Redemption{})
	if keyword != "" {
		if id, err := strconv.Atoi(keyword); err == nil {
			query = query.Where("id = ? OR name LIKE ?", id, keyword+"%")
		} else {
			query = query.Where("name LIKE ?", keyword+"%")
		}
	}
	if status != "" {
		now := wallclock.NowTimestamp()
		switch status {
		case "expired":
			query = query.Where("status = ? AND expired_time != 0 AND expired_time < ?",
				RedemptionStatusEnabled, now)
		case strconv.Itoa(RedemptionStatusEnabled):
			query = query.Where("status = ? AND (expired_time = 0 OR expired_time >= ?)",
				RedemptionStatusEnabled, now)
		case strconv.Itoa(RedemptionStatusDisabled):
			query = query.Where("status = ?", RedemptionStatusDisabled)
		case strconv.Itoa(RedemptionStatusUsed):
			query = query.Where("status = ?", RedemptionStatusUsed)
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []model.Redemption
	if err := query.Order("id desc").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetRedemptionByID loads a redemption code by id.
func GetRedemptionByID(id int) (*model.Redemption, error) {
	if id == 0 {
		return nil, errors.New("id 为空！")
	}
	var r model.Redemption
	if err := model.DB.First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateRedemption updates a redemption code. With statusOnly only the status
// changes; otherwise name/quota/expired_time are applied after the caller has
// validated the expiry.
func UpdateRedemption(id int, statusOnly bool, name string, quota int, expiredTime int64, status int) (*model.Redemption, error) {
	if id <= 0 {
		return nil, errors.New("invalid redemption id")
	}
	if statusOnly {
		if status != RedemptionStatusEnabled && status != RedemptionStatusDisabled && status != RedemptionStatusUsed {
			return nil, errors.New("invalid redemption status")
		}
	} else if !validRedemptionPayload(name, quota, expiredTime) {
		return nil, errors.New("invalid redemption payload")
	}
	r, err := GetRedemptionByID(id)
	if err != nil {
		return nil, err
	}
	if statusOnly {
		r.Status = status
	} else {
		r.Name = name
		r.Quota = quota
		r.ExpiredTime = expiredTime
	}
	if err := model.DB.Model(r).Select("name", "status", "quota", "redeemed_time", "expired_time").
		Updates(r).Error; err != nil {
		return nil, err
	}
	return r, nil
}

func validRedemptionPayload(name string, quota int, expiredTime int64) bool {
	nameLength := utf8.RuneCountInString(name)
	return nameLength >= 1 && nameLength <= 20 && quotamath.QuotaWithinBounds(quota) && quota > 0 &&
		(expiredTime == 0 || expiredTime >= wallclock.NowTimestamp())
}

// DeleteRedemptionByID deletes a redemption code.
func DeleteRedemptionByID(id int) error {
	if id == 0 {
		return errors.New("id 为空！")
	}
	var r model.Redemption
	if err := model.DB.Where("id = ?", id).First(&r).Error; err != nil {
		return err
	}
	return model.DB.Delete(&r).Error
}

// DeleteInvalidRedemptions hard-deletes used/disabled codes and expired
// enabled codes (reference contract).
func DeleteInvalidRedemptions() (int64, error) {
	now := wallclock.NowTimestamp()
	res := model.DB.Where("status IN ? OR (status = ? AND expired_time != 0 AND expired_time < ?)",
		[]int{RedemptionStatusUsed, RedemptionStatusDisabled}, RedemptionStatusEnabled, now).
		Delete(&model.Redemption{})
	return res.RowsAffected, res.Error
}

// Redeem redeems a code on behalf of the user, crediting quota exactly once.
func Redeem(userId int, key string) (int, error) {
	if userId <= 0 || key == "" {
		return 0, ErrInvalidRedemption
	}
	// Redemption converts an administrator-issued instrument into spendable
	// wallet quota. Enforce the versioned payment gate in the service layer so
	// alternate or future callers cannot bypass the controller check.
	if !PaymentComplianceConfirmed() {
		return 0, ErrPaymentComplianceRequired
	}
	redeemedQuota := 0
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var redemption model.Redemption
		if err := locking.SubscriptionLockForUpdate(tx).Where(map[string]any{"key": key}).First(&redemption).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidRedemption
			}
			return err
		}
		now := wallclock.NowTimestamp()
		if redemption.Status != RedemptionStatusEnabled || redemption.Quota <= 0 ||
			(redemption.ExpiredTime > 0 && redemption.ExpiredTime < now) {
			return ErrInvalidRedemption
		}

		// The conditional write is needed even with the row lock because SQLite
		// intentionally treats FOR UPDATE as a no-op.
		claimed := tx.Model(&model.Redemption{}).
			Where("id = ? AND status = ?", redemption.Id, RedemptionStatusEnabled).
			Updates(map[string]any{
				"status":        RedemptionStatusUsed,
				"used_user_id":  userId,
				"redeemed_time": now,
			})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return ErrInvalidRedemption
		}
		if err := increaseUserQuotaTx(tx, userId, redemption.Quota); err != nil {
			return err
		}
		redeemedQuota = redemption.Quota
		return nil
	})
	if err != nil {
		return 0, err
	}
	return redeemedQuota, nil
}
