package service

import (
	"errors"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Redemption status constants.
const (
	RedemptionStatusEnabled  = 1
	RedemptionStatusUsed     = 2
	RedemptionStatusDisabled = 3
)

// ErrInvalidRedemption is returned when a redemption key is unknown/unusable.
var ErrInvalidRedemption = errors.New("无效的兑换码")

// CreateRedemption creates a redemption code owned by userId (admin).
func CreateRedemption(userId int, name string, quota int, expiredTime int64) (*model.Redemption, error) {
	r := model.Redemption{
		UserId:      userId,
		Key:         common.RandomAlphanumeric(32),
		Status:      RedemptionStatusEnabled,
		Name:        name,
		Quota:       quota,
		CreatedTime: common.NowTimestamp(),
		ExpiredTime: expiredTime,
	}
	if err := model.DB.Create(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRedemptions lists redemption codes (optionally filtered by owner).
func GetRedemptions(userId int, all bool) []model.Redemption {
	q := model.DB.Order("id desc")
	if !all {
		q = q.Where("user_id = ?", userId)
	}
	var out []model.Redemption
	q.Find(&out)
	return out
}

// Redeem redeems a code on behalf of the user, crediting quota exactly once.
func Redeem(userId int, key string) (int, error) {
	if key == "" {
		return 0, ErrInvalidRedemption
	}
	var r model.Redemption
	if err := model.DB.Where("key = ?", key).First(&r).Error; err != nil {
		return 0, ErrInvalidRedemption
	}
	if r.Status != RedemptionStatusEnabled {
		return 0, ErrInvalidRedemption
	}
	if r.ExpiredTime > 0 && r.ExpiredTime < common.NowTimestamp() {
		return 0, ErrInvalidRedemption
	}

	now := common.NowTimestamp()
	// Atomically claim the code so concurrent redemption cannot double-credit.
	res := model.DB.Model(&model.Redemption{}).
		Where("id = ? AND status = ?", r.Id, RedemptionStatusEnabled).
		Updates(map[string]any{
			"status":        RedemptionStatusUsed,
			"used_user_id":  userId,
			"redeemed_time": now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, ErrInvalidRedemption
	}

	if err := IncreaseUserQuota(userId, r.Quota); err != nil {
		return 0, err
	}
	return r.Quota, nil
}
