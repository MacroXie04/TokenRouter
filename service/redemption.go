package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Redemption status constants (reference numbering: 0 is never used).
const (
	RedemptionStatusEnabled  = 1
	RedemptionStatusDisabled = 2
	RedemptionStatusUsed     = 3
)

// ErrInvalidRedemption is returned when a redemption key is unknown/unusable.
var ErrInvalidRedemption = errors.New("无效的兑换码")

// randomRedemptionKey returns a 32-char hex key (the reference key width).
func randomRedemptionKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(common.GenerateUUID()[:16]))
	}
	return hex.EncodeToString(buf)
}

// CreateRedemptionBatch creates count redemption codes owned by userId with
// random 32-hex keys and returns the generated keys (reference contract).
func CreateRedemptionBatch(userId int, name string, quota int, expiredTime int64, count int) ([]string, error) {
	keys := make([]string, 0, count)
	for i := 0; i < count; i++ {
		key := randomRedemptionKey()
		r := model.Redemption{
			UserId:      userId,
			Key:         key,
			Status:      RedemptionStatusEnabled,
			Name:        name,
			Quota:       quota,
			CreatedTime: common.NowTimestamp(),
			ExpiredTime: expiredTime,
		}
		if err := model.DB.Create(&r).Error; err != nil {
			return nil, err
		}
		keys = append(keys, key)
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
		now := common.NowTimestamp()
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
	now := common.NowTimestamp()
	res := model.DB.Where("status IN ? OR (status = ? AND expired_time != 0 AND expired_time < ?)",
		[]int{RedemptionStatusUsed, RedemptionStatusDisabled}, RedemptionStatusEnabled, now).
		Delete(&model.Redemption{})
	return res.RowsAffected, res.Error
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
