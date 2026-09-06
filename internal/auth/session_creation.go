package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"time"
)

// CreateSession creates a server-side session and returns its SID and the
// opaque refresh token (only the hash is persisted).
func CreateSession(user *model.User, ip, userAgent, loginMethod string) (sid, refreshToken string, err error) {
	created, err := createSession(user, ip, userAgent, loginMethod, false)
	if err != nil {
		return "", "", err
	}
	return created.Session.SID, created.RefreshToken, nil
}

type sessionCreationResult struct {
	Session      model.UserSession
	User         model.User
	RefreshToken string
	AccessToken  string
	DatabaseNow  int64
}

func createSession(user *model.User, ip, userAgent, loginMethod string, signAccess bool) (*sessionCreationResult, error) {
	if user == nil || user.Id <= 0 || user.Status != model.UserStatusEnabled || user.AuthVersion <= 0 || model.DB == nil {
		return nil, ErrSessionRevoked
	}
	policy, err := LoadUserSessionPolicy()
	if err != nil {
		return nil, err
	}
	loginMethod = normalizeSessionMetadata(loginMethod, 32)
	if loginMethod == "" {
		loginMethod = "unknown"
	}
	ip = normalizeSessionMetadata(ip, 64)
	userAgent = normalizeSessionMetadata(userAgent, 512)

	sid, err := cryptoutil.SecureRandomAlphanumeric(32)
	if err != nil {
		return nil, err
	}
	refreshToken, err := cryptoutil.SecureRandomAlphanumeric(64)
	if err != nil {
		return nil, err
	}
	created := &sessionCreationResult{RefreshToken: refreshToken}

	// The process-local stripe closes SQLite's missing row-lock gap, while the
	// owning user row serializes issuance across MySQL/PostgreSQL nodes.
	creationLock := userSessionCreationLock(user.Id)
	creationLock.Lock()
	defer creationLock.Unlock()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentUser model.User
		if err := locking.SubscriptionLockForUpdate(tx).
			Select("id", "role", "status", "auth_version").
			Where("id = ?", user.Id).
			First(&currentUser).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSessionRevoked
			}
			return err
		}
		if currentUser.Status != model.UserStatusEnabled || currentUser.AuthVersion <= 0 ||
			currentUser.AuthVersion != user.AuthVersion {
			return ErrSessionRevoked
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var activeCount int64
		if err := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND status = ? AND expires_at > ?",
				user.Id, SessionStatusActive, now).
			Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount >= policy.activeLimit {
			return ErrSessionLimit
		}
		var issuanceCount int64
		issuanceCutoff := time.Unix(now-int64(policy.issuanceWindow/time.Second), 0)
		if err := tx.Model(&model.UserSession{}).
			Where("user_id = ? AND created_at > ?", user.Id, issuanceCutoff).
			Count(&issuanceCount).Error; err != nil {
			return err
		}
		if issuanceCount >= policy.issuanceLimit {
			return ErrSessionIssuanceLimit
		}

		createdAt := time.Unix(now, 0).UTC()
		session := model.UserSession{
			SID:             sid,
			UserID:          currentUser.Id,
			Version:         1,
			UserAuthVersion: currentUser.AuthVersion,
			Status:          SessionStatusActive,
			RefreshHash:     refreshTokenHash(refreshToken),
			LoginMethod:     loginMethod,
			IP:              ip,
			UserAgent:       userAgent,
			CreatedAt:       createdAt,
			LastActiveAt:    now,
			ExpiresAt:       createdAt.Add(RefreshTokenTTL).Unix(),
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		created.Session = session
		created.User = currentUser
		created.DatabaseNow = now
		if signAccess {
			access, err := signSessionAccessToken(&created.Session, &created.User, now)
			if err != nil {
				return err
			}
			created.AccessToken = access
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}
