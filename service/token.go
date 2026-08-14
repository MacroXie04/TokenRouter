package service

import (
	"errors"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Token status constants.
const (
	TokenStatusEnabled  = 1
	TokenStatusDisabled = 2
	TokenStatusExpired  = 3
	TokenStatusExhausted = 4
)

// ErrTokenNotFound is returned when a token key does not resolve.
var ErrTokenNotFound = errors.New("token not found")

// ErrTokenDisabled is returned when a token is not usable.
var ErrTokenDisabled = errors.New("token disabled")

// ErrTokenExpired is returned when a token has expired.
var ErrTokenExpired = errors.New("token expired")

// TokenByKey loads an enabled token by its key.
func TokenByKey(key string) (*model.Token, error) {
	if key == "" {
		return nil, ErrTokenNotFound
	}
	var token model.Token
	if err := model.DB.Where("key = ?", key).First(&token).Error; err != nil {
		return nil, ErrTokenNotFound
	}
	return &token, nil
}

// GetTokenByID loads a token by id.
func GetTokenByID(id int) (*model.Token, error) {
	var token model.Token
	if err := model.DB.First(&token, id).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

// CheckTokenUsable validates status and expiry.
func CheckTokenUsable(token *model.Token) error {
	if token.Status != TokenStatusEnabled {
		return ErrTokenDisabled
	}
	if token.ExpiredTime > 0 && token.ExpiredTime < common.NowTimestamp() {
		return ErrTokenExpired
	}
	if !token.UnlimitedQuota && token.RemainQuota <= 0 {
		return errors.New("token quota exhausted")
	}
	return nil
}

// IncreaseTokenUsedQuota increments a token's used quota.
func IncreaseTokenUsedQuota(id int, quota int) error {
	if quota <= 0 {
		return nil
	}
	return model.DB.Model(&model.Token{}).Where("id = ?", id).
		Updates(map[string]any{
			"used_quota": gormExpr("used_quota + ?", quota),
			"remain_quota": gormExpr("remain_quota - ?", quota),
		}).Error
}

// DecreaseTokenQuota atomically deducts from remain_quota and adds to used_quota.
func DecreaseTokenQuota(id int, quota int) error {
	if quota <= 0 {
		return nil
	}
	return model.DB.Model(&model.Token{}).Where("id = ?", id).
		Updates(map[string]any{
			"remain_quota": gormExpr("remain_quota - ?", quota),
			"used_quota":   gormExpr("used_quota + ?", quota),
		}).Error
}

// UpdateTokenAccessedTime updates last-access timestamp.
func UpdateTokenAccessedTime(id int, ts int64) error {
	return model.DB.Model(&model.Token{}).Where("id = ?", id).
		UpdateColumn("accessed_time", ts).Error
}
