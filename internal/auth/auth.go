package auth

import (
	"errors"
	"net/http"
	"time"
)

// Session-related constants.
const (
	AccessTokenTTL      = 15 * time.Minute
	RefreshTokenTTL     = 30 * 24 * time.Hour
	RefreshReplayWindow = 30 * time.Second

	SessionStatusActive  = "active"
	SessionStatusRevoked = "revoked"
)

// Auth errors.
var (
	ErrInvalidCredentials       = errors.New("用户名或密码错误")
	ErrRefreshTokenInvalid      = errors.New("刷新令牌无效")
	ErrSessionRevoked           = errors.New("会话已撤销")
	ErrRefreshReplay            = errors.New("刷新令牌重放")
	ErrRefreshRace              = errors.New("刷新令牌已被并发轮换")
	ErrSessionLimit             = errors.New("active user session limit reached")
	ErrSessionIssuanceLimit     = errors.New("user session issuance limit reached")
	ErrAuthVersionOverflow      = errors.New("account authentication version is invalid or exhausted")
	ErrSessionVersionOverflow   = errors.New("session version is invalid or exhausted")
	ErrSessionExpiryInvalid     = errors.New("session expiry is invalid")
	ErrPasswordVerificationBusy = errors.New("password verification capacity is busy")
)

// AuthSessionErrorCode maps session lifecycle errors to stable API semantics.
// Unexpected storage or entropy failures stay generic at the controller edge.
func AuthSessionErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, ErrSessionLimit):
		return http.StatusConflict, "AUTH_SESSION_LIMIT"
	case errors.Is(err, ErrSessionIssuanceLimit):
		return http.StatusTooManyRequests, "AUTH_SESSION_ISSUANCE_LIMIT"
	case errors.Is(err, ErrRefreshRace):
		return http.StatusConflict, "AUTH_REFRESH_RACE"
	case errors.Is(err, ErrRefreshTokenInvalid):
		return http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	case errors.Is(err, ErrSessionRevoked), errors.Is(err, ErrRefreshReplay):
		return http.StatusUnauthorized, "AUTH_SESSION_REVOKED"
	default:
		return http.StatusInternalServerError, "AUTH_INTERNAL_ERROR"
	}
}
