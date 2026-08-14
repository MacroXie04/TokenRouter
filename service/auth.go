package service

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Session-related constants.
const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 7 * 24 * time.Hour

	SessionStatusActive  = "active"
	SessionStatusRevoked = "revoked"
)

// Auth errors.
var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrSessionRevoked     = errors.New("会话已撤销")
	ErrRefreshReplay      = errors.New("刷新令牌重放")
)

// IssueAccessToken signs a short-lived access JWT for a session.
func IssueAccessToken(user *model.User, sid string) (string, error) {
	return common.GenerateJWT(user.Id, user.Role, "", common.SessionSecret(), AccessTokenTTL)
}

// CreateSession creates a server-side session and returns its SID and the
// opaque refresh token (only the hash is persisted).
func CreateSession(user *model.User, ip, userAgent, loginMethod string) (sid, refreshToken string, err error) {
	sid = common.RandomAlphanumeric(32)
	refreshToken = common.RandomAlphanumeric(64)
	now := time.Now()
	session := model.UserSession{
		SID:             sid,
		UserID:          user.Id,
		Version:         1,
		UserAuthVersion: user.AuthVersion,
		Status:          SessionStatusActive,
		RefreshHash:     common.SHA256Hex(refreshToken),
		LoginMethod:     loginMethod,
		IP:              ip,
		UserAgent:       userAgent,
		CreatedAt:       now,
		LastActiveAt:    now.Unix(),
		ExpiresAt:       now.Add(RefreshTokenTTL).Unix(),
	}
	if err := model.DB.Create(&session).Error; err != nil {
		return "", "", err
	}
	return sid, refreshToken, nil
}

// AuthenticatePassword verifies a username/password pair without creating a
// session (used by the two-step login flow when 2FA is enabled).
func AuthenticatePassword(username, password string) (*model.User, error) {
	user, err := GetUserByUsername(username)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if user.Status == model.UserStatusDisabled {
		return nil, errors.New("用户已禁用")
	}
	if !common.PasswordVerify(password, user.Password) {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// CompleteLogin creates a session and returns the tokens for a user.
func CompleteLogin(user *model.User, ip, userAgent, loginMethod string) (sid, access, refresh string, err error) {
	sid, refresh, err = CreateSession(user, ip, userAgent, loginMethod)
	if err != nil {
		return "", "", "", err
	}
	access, err = IssueAccessToken(user, sid)
	if err != nil {
		return "", "", "", err
	}
	_ = UpdateUserLastLoginAt(user.Id, common.NowTimestamp())
	return sid, access, refresh, nil
}

// Login validates credentials and returns a fresh session + access token.
func Login(username, password, ip, userAgent string) (*model.User, string, string, string, error) {
	user, err := AuthenticatePassword(username, password)
	if err != nil {
		return nil, "", "", "", err
	}
	sid, access, refresh, err := CompleteLogin(user, ip, userAgent, "password")
	if err != nil {
		return nil, "", "", "", err
	}
	return user, sid, access, refresh, nil
}

// Login2FA completes a two-step login using a one-time TOTP code or a backup
// code, after the password was already verified and a flow token issued.
func Login2FA(flowToken, code, ip, userAgent string) (*model.User, string, string, string, error) {
	flow, err := ConsumeAuthFlow(flowToken, AuthFlowPurposeLogin2FA)
	if err != nil {
		return nil, "", "", "", ErrInvalidFlowToken
	}
	user, err := GetUserByID(flow.UserId)
	if err != nil {
		return nil, "", "", "", ErrInvalidCredentials
	}
	if err := VerifyTwoFA(user.Id, code); err != nil {
		if err == ErrTwoFAInvalidCode && VerifyBackupCode(user.Id, code) == nil {
			// accepted via backup code
		} else {
			return nil, "", "", "", err
		}
	}
	sid, access, refresh, err := CompleteLogin(user, ip, userAgent, "2fa")
	if err != nil {
		return nil, "", "", "", err
	}
	return user, sid, access, refresh, nil
}

// RefreshSession validates a refresh token, rotates it (replay detection), and
// returns new access + refresh tokens.
func RefreshSession(sid, refreshToken string) (string, string, *model.User, error) {
	var session model.UserSession
	if err := model.DB.Where("sid = ?", sid).First(&session).Error; err != nil {
		return "", "", nil, ErrSessionRevoked
	}
	if session.Status != SessionStatusActive {
		return "", "", nil, ErrSessionRevoked
	}
	if session.ExpiresAt > 0 && session.ExpiresAt < common.NowTimestamp() {
		return "", "", nil, ErrSessionRevoked
	}
	// Replay detection: if the presented token is the *previous* refresh token,
	// revoke the session (token family reuse).
	presentedHash := common.SHA256Hex(refreshToken)
	if session.PreviousRefreshHash != "" && presentedHash == session.PreviousRefreshHash {
		_ = model.DB.Model(&session).Update("status", SessionStatusRevoked).Error
		return "", "", nil, ErrRefreshReplay
	}
	if presentedHash != session.RefreshHash {
		return "", "", nil, ErrSessionRevoked
	}

	user, err := GetUserByID(session.UserID)
	if err != nil {
		return "", "", nil, ErrSessionRevoked
	}
	if user.AuthVersion != session.UserAuthVersion {
		_ = model.DB.Model(&session).Update("status", SessionStatusRevoked).Error
		return "", "", nil, ErrSessionRevoked
	}

	// Rotate.
	newRefresh := common.RandomAlphanumeric(64)
	now := time.Now()
	updates := map[string]any{
		"refresh_hash":          common.SHA256Hex(newRefresh),
		"previous_refresh_hash": session.RefreshHash,
		"previous_valid_until":  common.NowTimestamp() + 60,
		"last_active_at":        now.Unix(),
		"version":               session.Version + 1,
	}
	if err := model.DB.Model(&session).Updates(updates).Error; err != nil {
		return "", "", nil, err
	}
	access, err := IssueAccessToken(user, sid)
	if err != nil {
		return "", "", nil, err
	}
	return access, newRefresh, user, nil
}

// RevokeSession marks a session revoked.
func RevokeSession(sid string) error {
	return model.DB.Model(&model.UserSession{}).
		Where("sid = ?", sid).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     common.NowTimestamp(),
			"revoked_reason": "logout",
		}).Error
}

// RevokeAllUserSessions revokes every active session for a user.
func RevokeAllUserSessions(userId int) error {
	return model.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND status = ?", userId, SessionStatusActive).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     common.NowTimestamp(),
			"revoked_reason": "revoke_all",
		}).Error
}

// GetUserSessions lists a user's sessions, most recent first.
func GetUserSessions(userId int) []model.UserSession {
	var sessions []model.UserSession
	model.DB.Where("user_id = ?", userId).Order("created_at desc").Find(&sessions)
	return sessions
}

// RevokeOtherSessions deletes every session of a user except the given sid.
func RevokeOtherSessions(userId int, keepSid string) error {
	return model.DB.Where("user_id = ? AND sid <> ?", userId, keepSid).
		Delete(&model.UserSession{}).Error
}

// BumpAuthVersionKeepSession increments the user's auth version (invalidating
// every other session on next refresh) and re-syncs the kept session so it
// survives.
func BumpAuthVersionKeepSession(userId int, keepSid string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", userId).
			UpdateColumn("auth_version", gorm.Expr("auth_version + 1")).Error; err != nil {
			return err
		}
		var user model.User
		if err := tx.First(&user, userId).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserSession{}).
			Where("user_id = ? AND sid = ?", userId, keepSid).
			UpdateColumn("user_auth_version", user.AuthVersion).Error
	})
}
