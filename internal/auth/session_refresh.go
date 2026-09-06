package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"time"
)

// SessionRefreshResult carries the newly rotated credentials together with
// the immutable absolute database expiry. Its fields are deliberately hidden
// from JSON so an accidental controller serialization cannot disclose tokens.
type SessionRefreshResult struct {
	AccessToken  string      `json:"-"`
	RefreshToken string      `json:"-"`
	User         *model.User `json:"-"`
	ExpiresAt    int64       `json:"-"`
	// ValidatedAt is the primary-database timestamp used to validate ExpiresAt.
	// The controller uses the same clock snapshot for cookie Max-Age so process
	// clock skew cannot shorten or extend the refresh credential.
	ValidatedAt int64 `json:"-"`
}

// RefreshSession preserves the original service contract for non-HTTP callers.
func RefreshSession(sid, refreshToken string) (string, string, *model.User, error) {
	result, err := RefreshSessionWithResult(sid, refreshToken)
	if err != nil {
		return "", "", nil, err
	}
	return result.AccessToken, result.RefreshToken, result.User, nil
}

// RefreshSessionWithResult validates and rotates a refresh token while
// returning the session's unchanged absolute expiry for browser-cookie
// lifetime enforcement.
func RefreshSessionWithResult(sid, refreshToken string) (*SessionRefreshResult, error) {
	if sid == "" || refreshToken == "" {
		return nil, ErrRefreshTokenInvalid
	}
	nextRefresh := deriveNextRefreshToken(sid, refreshToken)
	for range 3 {
		var session model.UserSession
		if err := model.DB.Where("sid = ?", sid).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrRefreshTokenInvalid
			}
			return nil, err
		}
		now, err := model.DatabaseUnixTimestamp(model.DB)
		if err != nil {
			return nil, err
		}
		if session.Status != SessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= now ||
			session.Version <= 0 || session.UserAuthVersion <= 0 {
			return nil, ErrSessionRevoked
		}
		if session.ExpiresAt-now > int64(RefreshTokenTTL/time.Second) {
			return nil, ErrSessionExpiryInvalid
		}
		if session.Version == math.MaxInt64 {
			_, revokeErr := revokeActiveSessionAt(session.UserID, session.SID, "session_version_exhausted", now)
			return nil, errors.Join(ErrSessionVersionOverflow, revokeErr)
		}

		var user model.User
		if err := model.DB.First(&user, session.UserID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
			if _, revokeErr := revokeActiveSessionAt(session.UserID, session.SID, "user_invalid", now); revokeErr != nil {
				return nil, revokeErr
			}
			return nil, ErrSessionRevoked
		}
		if user.Status != model.UserStatusEnabled || user.AuthVersion != session.UserAuthVersion {
			if _, err := revokeActiveSessionAt(session.UserID, session.SID, "user_security_changed", now); err != nil {
				return nil, err
			}
			return nil, ErrSessionRevoked
		}

		if refreshTokenHashMatches(session.PreviousRefreshHash, refreshToken) {
			if now <= session.PreviousValidUntil && refreshTokenHashMatches(session.RefreshHash, nextRefresh) {
				access, err := signSessionAccessToken(&session, &user, now)
				if err != nil {
					return nil, err
				}
				return &SessionRefreshResult{
					AccessToken: access, RefreshToken: nextRefresh, User: &user,
					ExpiresAt: session.ExpiresAt, ValidatedAt: now,
				}, nil
			}
			if now <= session.PreviousValidUntil {
				return nil, ErrRefreshRace
			}
			if _, err := revokeActiveSessionAt(session.UserID, session.SID, "refresh_replay", now); err != nil {
				return nil, err
			}
			return nil, ErrRefreshReplay
		}
		if !refreshTokenHashMatches(session.RefreshHash, refreshToken) {
			return nil, ErrRefreshTokenInvalid
		}
		rotatedSession := session
		rotatedSession.Version++
		rotatedSession.RefreshHash = refreshTokenHash(nextRefresh)
		rotatedSession.PreviousRefreshHash = session.RefreshHash
		rotatedSession.PreviousValidUntil = now + int64(RefreshReplayWindow/time.Second)
		rotatedSession.LastActiveAt = now
		access, err := signSessionAccessToken(&rotatedSession, &user, now)
		if err != nil {
			return nil, err
		}
		result := model.DB.Model(&model.UserSession{}).
			Where("sid = ? AND user_id = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND version = ? AND user_auth_version = ? AND refresh_hash = ?",
				session.SID, session.UserID, SessionStatusActive, now, session.Version, session.UserAuthVersion, session.RefreshHash).
			Updates(map[string]any{
				"refresh_hash":          refreshTokenHash(nextRefresh),
				"previous_refresh_hash": session.RefreshHash,
				"previous_valid_until":  now + int64(RefreshReplayWindow/time.Second),
				"last_active_at":        now,
				"version":               session.Version + 1,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		return &SessionRefreshResult{
			AccessToken: access, RefreshToken: nextRefresh, User: &user,
			ExpiresAt: session.ExpiresAt, ValidatedAt: now,
		}, nil
	}
	return nil, ErrRefreshRace
}

func refreshTokenHash(raw string) string {
	mac := hmac.New(sha256.New, []byte(cryptoutil.SessionSecret()))
	_, _ = mac.Write([]byte("tokenrouter/auth/refresh/v1\x00"))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func deriveNextRefreshToken(sid, current string) string {
	mac := hmac.New(sha256.New, []byte(cryptoutil.SessionSecret()))
	_, _ = mac.Write([]byte("tokenrouter/auth/refresh-rotate/v1\x00"))
	_, _ = mac.Write([]byte(sid))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(current))
	return hex.EncodeToString(mac.Sum(nil))
}

// refreshTokenHashMatches accepts the former unkeyed digest only long enough
// for deployed sessions to rotate forward. Every newly issued or rotated
// refresh token is persisted with the keyed, domain-separated digest.
func refreshTokenHashMatches(stored, raw string) bool {
	if stored == "" || raw == "" {
		return false
	}
	keyed := refreshTokenHash(raw)
	legacy := cryptoutil.SHA256Hex(raw)
	return hmac.Equal([]byte(stored), []byte(keyed)) || hmac.Equal([]byte(stored), []byte(legacy))
}
