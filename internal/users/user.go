// Package users implements user lookup, profiles, preferences, and notifications.
// Credential mutations belong to auth; quota mutations belong to billing.
package users

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
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
	return role >= roles.RoleAdminUser
}

// IsRoot reports whether a role has root access.
func IsRoot(role int) bool {
	return role >= roles.RoleRootUser
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

// Common groups.
const (
	GroupDefault = "default"
	GroupVip     = "vip"
)

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
