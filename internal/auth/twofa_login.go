package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"time"
)

type loginTwoFAFlowPayload struct {
	AuthVersion int64 `json:"auth_version"`
}

// BeginTwoFALogin creates a short-lived flow bound to the authentication
// version proven by the password step.
func BeginTwoFALogin(user *model.User) (string, error) {
	if user == nil || user.Id <= 0 || user.Status != model.UserStatusEnabled || user.AuthVersion <= 0 {
		return "", ErrInvalidCredentials
	}
	payload, err := jsonutil.Marshal(loginTwoFAFlowPayload{AuthVersion: user.AuthVersion})
	if err != nil {
		return "", err
	}
	return CreateAuthFlow(
		AuthFlowPurposeLogin2FA, "password", "", user.Id, "", string(payload), 5*time.Minute,
	)
}

// Login2FA completes a two-step login using a one-time TOTP code or a backup
// code, after the password was already verified and a flow token issued.
func Login2FA(flowToken, code, ip, userAgent string) (*model.User, string, string, string, error) {
	flow, err := PeekAuthFlow(flowToken, AuthFlowPurposeLogin2FA)
	if err != nil {
		if errors.Is(err, ErrInvalidFlowToken) {
			return nil, "", "", "", ErrInvalidFlowToken
		}
		return nil, "", "", "", err
	}
	user, err := userssvc.GetUserByID(flow.UserId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, userssvc.ErrUserNotFound) {
			return nil, "", "", "", ErrInvalidCredentials
		}
		return nil, "", "", "", err
	}
	var payload loginTwoFAFlowPayload
	if jsonutil.UnmarshalJsonStr(flow.Payload, &payload) != nil || payload.AuthVersion <= 0 ||
		payload.AuthVersion != user.AuthVersion || user.Status != model.UserStatusEnabled {
		return nil, "", "", "", ErrInvalidFlowToken
	}
	enabled, err := TwoFAStatusChecked(user.Id)
	if err != nil {
		return nil, "", "", "", err
	}
	if !enabled {
		return nil, "", "", "", ErrTwoFANotEnabled
	}
	if err := VerifyTwoFA(user.Id, code); err != nil {
		if !errors.Is(err, ErrTwoFAInvalidCode) {
			return nil, "", "", "", err
		}
		backupErr := VerifyBackupCode(user.Id, code)
		if backupErr != nil && !errors.Is(backupErr, ErrTwoFAInvalidCode) {
			return nil, "", "", "", backupErr
		}
		if backupErr != nil {
			return nil, "", "", "", ErrTwoFAInvalidCode
		}
	}
	if _, err := ConsumeAuthFlowExact(flowToken, AuthFlowMatch{
		Purpose: AuthFlowPurposeLogin2FA, Provider: "password", UserId: user.Id,
	}); err != nil {
		if errors.Is(err, ErrInvalidFlowToken) {
			return nil, "", "", "", ErrInvalidFlowToken
		}
		return nil, "", "", "", err
	}
	sid, access, refresh, err := CompleteLogin(user, ip, userAgent, "2fa")
	if err != nil {
		return nil, "", "", "", err
	}
	return user, sid, access, refresh, nil
}
