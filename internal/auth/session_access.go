package auth

import (
	"context"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"time"
)

// IssueAccessToken signs a short-lived access JWT for a session.
func IssueAccessToken(user *model.User, sid string) (string, error) {
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return "", err
	}
	return issueAccessTokenAt(user, sid, now)
}

// issueAccessTokenAt validates and signs against one caller-supplied primary
// database clock snapshot.
func issueAccessTokenAt(user *model.User, sid string, now int64) (string, error) {
	if user == nil || user.Id <= 0 || sid == "" {
		return "", ErrSessionRevoked
	}
	var session model.UserSession
	if err := model.DB.Where("sid = ? AND user_id = ?", sid, user.Id).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSessionRevoked
		}
		return "", err
	}
	var currentUser model.User
	if err := model.DB.First(&currentUser, user.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSessionRevoked
		}
		return "", err
	}
	if !validSessionSnapshot(&session, &currentUser, now) {
		return "", ErrSessionRevoked
	}
	return signSessionAccessToken(&session, &currentUser, now)
}

// signSessionAccessToken signs an access token from an already locked and
// validated database snapshot. Callers that mutate the session use this helper
// before committing so a later storage outage cannot strand an active session
// whose credentials were never returned to the user.
func signSessionAccessToken(session *model.UserSession, user *model.User, now int64) (string, error) {
	if !validSessionSnapshot(session, user, now) {
		return "", ErrSessionRevoked
	}
	return cryptoutil.GenerateSessionJWTAt(
		user.Id,
		user.Role,
		session.SID,
		session.UserAuthVersion,
		session.Version,
		cryptoutil.SessionSecret(),
		AccessTokenTTL,
		time.Unix(now, 0).UTC(),
	)
}

// ValidateAccessTokenClaims resolves and validates the live session snapshot
// carried by a dashboard access JWT. Validation is deliberately database
// backed on every request so revocation is immediate rather than TTL-bound.
func ValidateAccessTokenClaims(claims *cryptoutil.JWTClaims) (*model.UserSession, *model.User, error) {
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return nil, nil, err
	}
	return ValidateAccessTokenClaimsAt(claims, now)
}

// ValidateAccessTokenClaimsAt applies live-session checks against the same
// primary-database timestamp used by JWT registered-claim validation.
func ValidateAccessTokenClaimsAt(claims *cryptoutil.JWTClaims, now int64) (*model.UserSession, *model.User, error) {
	if !cryptoutil.IsDashboardAccessToken(claims) || claims.UserID <= 0 || claims.SessionID == "" ||
		claims.UserAuthVersion <= 0 || claims.SessionVersion <= 0 || claims.ExpiresAt == nil ||
		claims.ExpiresAt.Time.Unix() <= now {
		return nil, nil, ErrSessionRevoked
	}

	var session model.UserSession
	if err := model.DB.Where("sid = ? AND user_id = ?", claims.SessionID, claims.UserID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrSessionRevoked
		}
		return nil, nil, err
	}
	var user model.User
	if err := model.DB.First(&user, claims.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrSessionRevoked
		}
		return nil, nil, err
	}
	if !validSessionSnapshot(&session, &user, now) ||
		session.Version != claims.SessionVersion ||
		session.UserAuthVersion != claims.UserAuthVersion {
		return nil, nil, ErrSessionRevoked
	}
	return &session, &user, nil
}

func validSessionSnapshot(session *model.UserSession, user *model.User, now int64) bool {
	return session != nil && user != nil && session.SID != "" && session.UserID == user.Id &&
		session.Version > 0 && session.UserAuthVersion > 0 &&
		session.Status == SessionStatusActive && session.RevokedAt == 0 && session.ExpiresAt > now &&
		user.Status == model.UserStatusEnabled && user.AuthVersion == session.UserAuthVersion
}

// ValidateSessionBinding verifies that a security ceremony is still bound to
// a live session snapshot. Redirect callbacks may not be routed through
// UserAuth, so they use the session identifier sealed into their one-time flow
// instead of accepting a stale or revoked ceremony indefinitely.
func ValidateSessionBinding(userId int, sid string) error {
	if userId <= 0 || sid == "" {
		return ErrSessionRevoked
	}
	var session model.UserSession
	if err := model.DB.Where("user_id = ? AND sid = ?", userId, sid).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	now, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return err
	}
	if !validSessionSnapshot(&session, &user, now) {
		return ErrSessionRevoked
	}
	return nil
}
