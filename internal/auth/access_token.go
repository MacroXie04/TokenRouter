package auth

import (
	"encoding/base64"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
)

// GenerateUserAccessToken issues a new dashboard access token (PAT) for a
// user and replaces any existing one. The token format matches the reference:
// base64 random bytes of length 29-32 characters, unique across users.
func GenerateUserAccessToken(userId int) (string, error) {
	if userId == 0 {
		return "", errors.New("id 为空！")
	}
	for i := 0; i < 3; i++ {
		lengthOffset, err := randomInt4()
		if err != nil {
			return "", err
		}
		length := 29 + lengthOffset
		buf, err := cryptoutil.SecureRandomBytes(length * 3 / 4)
		if err != nil {
			return "", err
		}
		key := base64.StdEncoding.EncodeToString(buf)
		var count int64
		if err := model.DB.Model(&model.User{}).Where("access_token = ?", key).Count(&count).Error; err != nil {
			return "", err
		}
		if count > 0 {
			continue
		}
		res := model.DB.Model(&model.User{}).Where("id = ?", userId).Update("access_token", key)
		if res.Error != nil {
			return "", res.Error
		}
		if res.RowsAffected == 0 {
			return "", gorm.ErrRecordNotFound
		}
		return key, nil
	}
	return "", errors.New("生成访问令牌失败")
}

// UserByAccessToken resolves a user by their dashboard access token.
func UserByAccessToken(token string) (*model.User, error) {
	if token == "" {
		return nil, userssvc.ErrUserNotFound
	}
	var user model.User
	if err := model.DB.Where("access_token = ?", token).First(&user).Error; err != nil {
		return nil, userssvc.ErrUserNotFound
	}
	return &user, nil
}

// randomInt4 returns a random integer in [0, 4). Four divides 256, so this
// single-byte mapping is uniform.
func randomInt4() (int, error) {
	b, err := cryptoutil.SecureRandomBytes(1)
	if err != nil {
		return 0, err
	}
	return int(b[0] % 4), nil
}
