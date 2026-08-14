// Package service implements TokenRouter's business logic: users, tokens,
// channels and routing, quota/billing, and usage logging.
package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
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

// GetUserByAffCode resolves an affiliate code to its owning user.
func GetUserByAffCode(code string) (*model.User, error) {
	if code == "" {
		return nil, ErrUserNotFound
	}
	var user model.User
	if err := model.DB.Where("aff_code = ?", code).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
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

// InsertAdminUserWithTx prepares and inserts an administrator-created user in
// the caller's transaction. Only fields already whitelisted by the controller
// are accepted; reference defaults are applied here so every SQL dialect sees
// the same values instead of relying on database-specific column defaults.
func InsertAdminUserWithTx(tx *gorm.DB, user *model.User) error {
	hash, err := common.PasswordHash(user.Password)
	if err != nil {
		return err
	}
	user.Password = hash
	if user.Role == 0 {
		user.Role = constant.RoleCommonUser
	}
	user.Status = model.UserStatusEnabled
	user.Group = GroupDefault
	user.Quota = setting.GetOptionIntOrDefault(setting.InitialQuotaOption, 500000)
	user.AffCode = common.RandomAlphanumeric(4)
	user.Setting = "{}"
	user.CreatedAt = common.NowTimestamp()
	user.AuthVersion = 1
	return tx.Create(user).Error
}

// FinishAdminUserCreation performs the reference's best-effort post-commit
// initialization: role-derived sidebar settings and the initial-quota log.
func FinishAdminUserCreation(user *model.User) {
	modules := map[string]any{
		"chat":     map[string]any{"enabled": true, "playground": true, "chat": true},
		"console":  map[string]any{"enabled": true, "detail": true, "token": true, "log": true, "midjourney": true, "task": true},
		"personal": map[string]any{"enabled": true, "topup": true, "personal": true},
	}
	if user.Role >= constant.RoleAdminUser {
		modules["admin"] = map[string]any{
			"enabled": true, "channel": true, "models": true,
			"redemption": true, "user": true,
			"setting": user.Role >= constant.RoleRootUser,
		}
	}
	modulesJSON, err := common.Marshal(modules)
	if err == nil {
		settingsJSON, marshalErr := common.Marshal(map[string]any{"sidebar_modules": string(modulesJSON)})
		if marshalErr == nil {
			_ = model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("setting", string(settingsJSON)).Error
		}
	}
	if user.Quota > 0 {
		RecordSystemLog(user.Id, LogTypeSystem, fmt.Sprintf("新用户注册赠送 %d", user.Quota))
	}
}

// userSortColumns is the sort whitelist for admin user search (reference
// contract: unknown columns fall back to id desc).
var userSortColumns = map[string]string{
	"id":            "id",
	"username":      "username",
	"quota":         "quota",
	"group":         "group",
	"created_at":    "created_at",
	"last_login_at": "last_login_at",
}

// SearchUsers searches users (including soft-deleted) by keyword — a numeric
// keyword also matches the id — plus group/role/status filters, with the
// reference sort whitelist and paging. status=-1 selects deleted users only.
func SearchUsers(keyword, group string, role, status *int, offset, limit int, sortBy, sortOrder string) ([]*model.User, int64, error) {
	sortBy = strings.ToLower(strings.TrimSpace(sortBy))
	sortOrder = strings.ToLower(strings.TrimSpace(sortOrder))
	column, ok := userSortColumns[sortBy]
	if !ok {
		column, sortOrder = "id", "desc"
	} else if sortOrder != "asc" {
		sortOrder = "desc"
	}

	query := model.DB.Unscoped().Model(&model.User{})
	like := "username LIKE ? OR email LIKE ? OR display_name LIKE ?"
	args := []any{"%" + keyword + "%", "%" + keyword + "%", "%" + keyword + "%"}
	if id, err := strconv.Atoi(keyword); err == nil {
		like = "id = ? OR " + like
		args = append([]any{id}, args...)
	}
	query = query.Where("("+like+")", args...)
	if group != "" {
		// Map form so GORM quotes the column per dialect ("group" is a
		// reserved word in expression context on SQLite/MySQL).
		query = query.Where(map[string]any{"group": group})
	}
	if role != nil {
		query = query.Where("role = ?", *role)
	}
	if status != nil {
		if *status == -1 {
			query = query.Where("deleted_at IS NOT NULL")
		} else {
			query = query.Where("deleted_at IS NULL").Where("status = ?", *status)
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// Clause-based ordering so GORM quotes the column per dialect ("group"
	// is a reserved word in SQLite/MySQL expression contexts).
	query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: column}, Desc: sortOrder != "asc"})
	if column != "id" {
		query = query.Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true})
	}
	var users []*model.User
	if err := query.Omit("password", "access_token").
		Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}
