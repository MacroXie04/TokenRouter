// Package service implements TokenRouter's business logic: users, tokens,
// channels and routing, quota/billing, and usage logging.
package service

import (
	"errors"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

// ErrUserNotFound is returned when a user id does not resolve.
var ErrUserNotFound = errors.New("user not found")

// GetUserByID loads a user by id (excluding soft-deleted).
func GetUserByID(id int) (*model.User, error) {
	if id == 0 {
		return nil, ErrUserNotFound
	}
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByUsername loads a user by username.
func GetUserByUsername(username string) (*model.User, error) {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// IsAdmin reports whether a role has administrative access.
func IsAdmin(role int) bool {
	return role >= constant.RoleAdminUser
}

// IsRoot reports whether a role has root access.
func IsRoot(role int) bool {
	return role >= constant.RoleRootUser
}

// IncreaseUserQuota adds quota to a user atomically.
func IncreaseUserQuota(id int, quota int) error {
	if quota == 0 {
		return nil
	}
	return model.DB.Model(&model.User{}).Where("id = ?", id).
		UpdateColumn("quota", gormExpr("quota + ?", quota)).Error
}

// DecreaseUserQuota subtracts used quota and increments used_quota atomically,
// guarded so concurrent deductions can never drive quota negative.
func DecreaseUserQuota(id int, quota int) error {
	if quota <= 0 {
		return nil
	}
	res := model.DB.Model(&model.User{}).
		Where("id = ? AND quota >= ?", id, quota).
		UpdateColumn("quota", gormExpr("quota - ?", quota))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInsufficientQuota
	}
	return model.DB.Model(&model.User{}).Where("id = ?", id).
		UpdateColumn("used_quota", gormExpr("used_quota + ?", quota)).Error
}

// UpdateUserRequestCount increments the user's request counter.
func UpdateUserRequestCount(id int, count int) error {
	return model.DB.Model(&model.User{}).Where("id = ?", id).
		UpdateColumn("request_count", gormExpr("request_count + ?", count)).Error
}

// UpdateUserLastLoginAt updates the last login timestamp.
func UpdateUserLastLoginAt(id int, ts int64) error {
	return model.DB.Model(&model.User{}).Where("id = ?", id).
		UpdateColumn("last_login_at", ts).Error
}

// SetUserGroup updates the user's group.
func SetUserGroup(id int, group string) error {
	return model.DB.Model(&model.User{}).Where("id = ?", id).UpdateColumn("group", group).Error
}

// UpdateUser updates mutable profile fields.
func UpdateUser(id int, updates map[string]any) error {
	return model.DB.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteUser soft-deletes a user and their associated tokens/sessions.
func DeleteUser(userId int) error {
	_ = RevokeAllUserSessions(userId)
	_ = model.DB.Where("user_id = ?", userId).Delete(&model.Token{}).Error
	return model.DB.Delete(&model.User{}, userId).Error
}

// Common groups.
const (
	GroupDefault = "default"
	GroupVip     = "vip"
)
