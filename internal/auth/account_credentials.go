package auth

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

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
		return false, userssvc.ErrUserNotFound
	}
	var current model.User
	if err := locking.SubscriptionLockForUpdate(tx).
		Select("id", "role", "group", "auth_version").
		First(&current, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, userssvc.ErrUserNotFound
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
		hash, err := cryptoutil.PasswordHash(update.Password)
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
		return userssvc.ErrUserNotFound
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

// InsertAdminUserWithTx prepares and inserts an administrator-created user in
// the caller's transaction. Only fields already whitelisted by the controller
// are accepted; reference defaults are applied here so every SQL dialect sees
// the same values instead of relying on database-specific column defaults.
func InsertAdminUserWithTx(tx *gorm.DB, user *model.User) error {
	hash, err := cryptoutil.PasswordHash(user.Password)
	if err != nil {
		return err
	}
	user.Password = hash
	if user.Role == 0 {
		user.Role = roles.RoleCommonUser
	}
	user.Status = model.UserStatusEnabled
	plan, err := PlanRegistrationMutationFromEnvironment(user.Username)
	if err != nil {
		return err
	}
	affCode, err := cryptoutil.SecureRandomAlphanumeric(4)
	if err != nil {
		return err
	}
	user.AffCode = affCode
	user.Setting = "{}"
	user.CreatedAt = wallclock.NowTimestamp()
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
	if user.Role >= roles.RoleAdminUser {
		modules["admin"] = map[string]any{
			"enabled": true, "channel": true, "models": true,
			"redemption": true, "user": true,
			"setting": user.Role >= roles.RoleRootUser,
		}
	}
	modulesJSON, err := jsonutil.Marshal(modules)
	if err == nil {
		settingsJSON, marshalErr := jsonutil.Marshal(map[string]any{"sidebar_modules": string(modulesJSON)})
		if marshalErr == nil {
			_ = model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("setting", string(settingsJSON)).Error
		}
	}
	if user.Quota > 0 {
		billingsvc.RecordSystemLog(user.Id, billingsvc.LogTypeSystem, fmt.Sprintf("新用户注册赠送 %d", user.Quota))
	}
}
