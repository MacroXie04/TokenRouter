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
)

// ErrUserNotFound is returned when a user id does not resolve.
var ErrUserNotFound = errors.New("user not found")

// ErrUserQuotaOverflow is returned when a credit cannot be represented by the
// platform's quota column without wrapping. Financial callers must leave their
// accompanying ledger/order mutation uncommitted when this occurs.
var ErrUserQuotaOverflow = errors.New("user quota overflow")

// ErrInvalidIdentityBindingType is returned for unsupported built-in binding
// names on the administrator clear-binding route.
var ErrInvalidIdentityBindingType = errors.New("invalid identity binding type")

// ErrManagedUserRoleChanged prevents an administrator edit authorized against
// one role from committing after a concurrent promotion changes the target's
// privilege boundary.
var ErrManagedUserRoleChanged = errors.New("managed user role changed")

// ManagedUserUpdate is the allowlisted account state accepted by the
// administrator edit transaction. Nil pointers leave their field unchanged;
// Password is write-only and an empty value leaves the credential unchanged.
type ManagedUserUpdate struct {
	DisplayName *string
	Group       *string
	Remark      *string
	Quota       *int
	Status      *int
	Password    string
}

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
	if id <= 0 || !common.QuotaWithinBounds(quota) {
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
	if tx == nil || id <= 0 || quota <= 0 || !common.QuotaWithinBounds(quota) {
		return errors.New("invalid user quota credit")
	}
	var user model.User
	if err := subscriptionLockForUpdate(tx).Select("id", "quota").First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	newQuota, ok := common.AddQuotaWithinBounds(user.Quota, quota)
	if !ok {
		return ErrUserQuotaOverflow
	}
	result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", id, user.Quota).
		UpdateColumn("quota", newQuota)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrUserNotFound
	}
	return nil
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
	if quota == 0 {
		return nil
	}
	if id <= 0 || quota < 0 || int64(quota) > common.MaxQuota {
		return ErrInvalidQuota
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "quota", "used_quota").First(&user, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		if !common.QuotaWithinBounds(user.Quota) {
			return ErrUserQuotaOverflow
		}
		if user.Quota < quota {
			return ErrInsufficientQuota
		}
		newUsedQuota, ok := common.AddQuotaWithinBounds(user.UsedQuota, quota)
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
	if id <= 0 || quota < 0 || int64(quota) > common.MaxQuota {
		return ErrInvalidQuota
	}
	result := model.DB.Model(&model.User{}).Where("id = ?", id).UpdateColumn("quota", quota)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrUserNotFound
	}
	return nil
}

// UpdateUserRequestCount increments the user's request counter.
func UpdateUserRequestCount(id int, count int) error {
	if id <= 0 || count < 0 || int64(count) > common.MaxQuota {
		return ErrInvalidQuota
	}
	if count == 0 {
		return nil
	}
	result := model.DB.Model(&model.User{}).
		Where("id = ? AND request_count >= 0 AND request_count <= ?", id, common.MaxQuota-int64(count)).
		UpdateColumn("request_count", gormExpr("request_count + ?", count))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrUserUsageOverflow
	}
	return nil
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

// UpdateManagedUserInTx applies an administrator edit under a row lock. Group
// changes and password resets advance the account authentication version and
// revoke every active session in the same transaction, so an authorization
// change can never commit with stale sessions still usable.
func UpdateManagedUserInTx(
	tx *gorm.DB,
	userID int,
	expectedRole int,
	update ManagedUserUpdate,
) (bool, error) {
	if tx == nil || userID <= 0 {
		return false, ErrUserNotFound
	}
	var current model.User
	if err := subscriptionLockForUpdate(tx).
		Select("id", "role", "group", "auth_version").
		First(&current, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrUserNotFound
		}
		return false, err
	}
	if current.Role != expectedRole {
		return false, ErrManagedUserRoleChanged
	}

	updates := make(map[string]any, 6)
	if update.DisplayName != nil {
		updates["display_name"] = *update.DisplayName
	}
	if update.Group != nil {
		updates["group"] = *update.Group
	}
	if update.Remark != nil {
		updates["remark"] = *update.Remark
	}
	if update.Quota != nil {
		updates["quota"] = *update.Quota
	}
	if update.Status != nil {
		updates["status"] = *update.Status
	}

	authChanged := update.Password != "" || (update.Group != nil && *update.Group != current.Group)
	if update.Password != "" {
		if len(update.Password) < 8 || len(update.Password) > 64 {
			return false, ErrInvalidCredentials
		}
		hash, err := common.PasswordHash(update.Password)
		if err != nil {
			return false, err
		}
		updates["password"] = hash
	}
	if !authChanged {
		if len(updates) == 0 {
			return false, nil
		}
		result := tx.Model(&model.User{}).Where("id = ? AND role = ?", userID, expectedRole).Updates(updates)
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&model.User{}).Where("id = ? AND role = ?", userID, expectedRole).Count(&count).Error; err != nil {
				return false, err
			}
			if count != 1 {
				return false, ErrManagedUserRoleChanged
			}
		}
		return false, nil
	}

	if current.AuthVersion <= 0 {
		return false, ErrAuthVersionOverflow
	}
	nextVersion := current.AuthVersion + 1
	if nextVersion <= current.AuthVersion {
		return false, ErrAuthVersionOverflow
	}
	if err := updateAuthVersionCAS(tx, userID, current.AuthVersion, nextVersion, updates); err != nil {
		return false, err
	}
	if err := revokeAllUserSessions(tx, userID, "admin_user_update"); err != nil {
		return false, err
	}
	return true, nil
}

// ClearUserIdentityBinding transactionally clears one built-in login binding
// and its durable ownership claim. Email needs additional fields cleared so a
// stale verified-email key can neither authenticate nor block another owner.
func ClearUserIdentityBinding(userID int, bindingType string) error {
	bindingType = strings.ToLower(strings.TrimSpace(bindingType))
	if userID <= 0 {
		return ErrUserNotFound
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userID).Error; err != nil {
			return err
		}

		if bindingType == "email" {
			return tx.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
				"email":              "",
				"email_verified":     false,
				"verified_email_key": nil,
			}).Error
		}

		column, ok := model.BuiltInExternalIdentityColumn(bindingType)
		if !ok {
			return ErrInvalidIdentityBindingType
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Update(column, "").Error; err != nil {
			return err
		}
		return model.ReleaseExternalIdentityWithTx(tx, bindingType, userID)
	})
}

// DeleteUser soft-deletes a user and their associated tokens/sessions.
func DeleteUser(userId int) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := revokeAllUserSessions(tx, userId, "account_deleted"); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userId).Delete(&model.Token{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.User{}, userId).Error
	})
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
	plan, err := PlanRegistrationMutationFromEnvironment(user.Username)
	if err != nil {
		return err
	}
	affCode, err := common.SecureRandomAlphanumeric(4)
	if err != nil {
		return err
	}
	user.AffCode = affCode
	user.Setting = "{}"
	user.CreatedAt = common.NowTimestamp()
	user.AuthVersion = 1
	return InsertPlannedRegistrationUserWithTx(tx, user, plan)
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
