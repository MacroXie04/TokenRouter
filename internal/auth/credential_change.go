package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"math"
	"time"
)

// BumpAuthVersionKeepSession increments the user's auth version, immediately
// invalidating all existing access JWTs, and re-syncs one refresh session so
// the client can obtain a replacement access token.
func BumpAuthVersionKeepSession(userId int, keepSid string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return bumpAuthVersionKeepSession(tx, userId, keepSid)
	})
}

func bumpAuthVersionKeepSession(tx *gorm.DB, userId int, keepSid string) error {
	if userId <= 0 || keepSid == "" {
		return ErrSessionRevoked
	}
	user, nextVersion, err := lockNextAuthVersion(tx, userId)
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	var session model.UserSession
	if err := locking.SubscriptionLockForUpdate(tx).
		Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND user_auth_version = ?",
			userId, keepSid, SessionStatusActive, now, user.AuthVersion).
		First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	if err := updateAuthVersionCAS(tx, userId, user.AuthVersion, nextVersion, nil); err != nil {
		return err
	}
	result := tx.Model(&model.UserSession{}).
		Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND user_auth_version = ?",
			userId, keepSid, SessionStatusActive, now, user.AuthVersion).
		UpdateColumn("user_auth_version", nextVersion)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSessionRevoked
	}
	return tx.Model(&model.UserSession{}).
		Where("user_id = ? AND sid <> ? AND status = ? AND revoked_at = 0", userId, keepSid, SessionStatusActive).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     now,
			"revoked_reason": "auth_version_changed",
		}).Error
}

// ChangePasswordKeepSession changes a password and advances the account's
// authentication version in one transaction. The browser session that proved
// possession of the old password remains usable; every other active session is
// revoked. A replacement access token is returned because the caller's old JWT
// becomes invalid as soon as the transaction commits.
func ChangePasswordKeepSession(userId int, keepSid, oldPassword, newPassword, displayName string) (string, error) {
	if userId <= 0 || keepSid == "" {
		return "", ErrSessionRevoked
	}
	if len(newPassword) < 8 || len(newPassword) > 64 {
		return "", ErrInvalidCredentials
	}
	passwordHash, err := cryptoutil.PasswordHash(newPassword)
	if err != nil {
		return "", err
	}

	var accessToken string
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).
			Select("id", "role", "status", "password", "auth_version").
			First(&user, userId).Error; err != nil {
			return err
		}
		if user.Status != model.UserStatusEnabled {
			return ErrSessionRevoked
		}
		if !cryptoutil.PasswordVerify(oldPassword, user.Password) {
			return ErrInvalidCredentials
		}
		if user.AuthVersion <= 0 || user.AuthVersion == math.MaxInt64 {
			return ErrAuthVersionOverflow
		}

		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var session model.UserSession
		if err := locking.SubscriptionLockForUpdate(tx).
			Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND user_auth_version = ?",
				userId, keepSid, SessionStatusActive, now, user.AuthVersion).
			First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionRevoked
			}
			return err
		}
		if session.Version <= 0 {
			return ErrSessionRevoked
		}

		nextVersion := user.AuthVersion + 1
		userUpdates := map[string]any{"password": passwordHash}
		if displayName != "" {
			userUpdates["display_name"] = displayName
		}
		if err := updateAuthVersionCAS(tx, userId, user.AuthVersion, nextVersion, userUpdates); err != nil {
			return err
		}
		kept := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND user_auth_version = ?",
				userId, keepSid, SessionStatusActive, user.AuthVersion).
			UpdateColumn("user_auth_version", nextVersion)
		if kept.Error != nil {
			return kept.Error
		}
		if kept.RowsAffected != 1 {
			return ErrSessionRevoked
		}
		if err := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND sid <> ? AND status = ? AND revoked_at = 0", userId, keepSid, SessionStatusActive).
			Updates(map[string]any{
				"status":         SessionStatusRevoked,
				"revoked_at":     now,
				"revoked_reason": "password_changed",
			}).Error; err != nil {
			return err
		}

		accessToken, err = cryptoutil.GenerateSessionJWTAt(
			user.Id, user.Role, keepSid, nextVersion, session.Version,
			cryptoutil.SessionSecret(), AccessTokenTTL, time.Unix(now, 0).UTC(),
		)
		return err
	})
	if err != nil {
		return "", err
	}
	return accessToken, nil
}

func lockNextAuthVersion(tx *gorm.DB, userId int) (*model.User, int64, error) {
	if tx == nil || userId <= 0 {
		return nil, 0, ErrSessionRevoked
	}
	var user model.User
	if err := locking.SubscriptionLockForUpdate(tx).Select("id", "auth_version").First(&user, userId).Error; err != nil {
		return nil, 0, err
	}
	if user.AuthVersion <= 0 || user.AuthVersion == math.MaxInt64 {
		return nil, 0, ErrAuthVersionOverflow
	}
	return &user, user.AuthVersion + 1, nil
}

func updateAuthVersionCAS(
	tx *gorm.DB,
	userId int,
	currentVersion int64,
	nextVersion int64,
	extraUpdates map[string]any,
) error {
	if tx == nil || userId <= 0 || currentVersion <= 0 || currentVersion == math.MaxInt64 ||
		nextVersion != currentVersion+1 || nextVersion <= 0 {
		return ErrAuthVersionOverflow
	}
	updates := make(map[string]any, len(extraUpdates)+1)
	for key, value := range extraUpdates {
		updates[key] = value
	}
	updates["auth_version"] = nextVersion
	result := tx.Model(&model.User{}).
		Where("id = ? AND auth_version = ?", userId, currentVersion).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuthVersionOverflow
	}
	return nil
}
