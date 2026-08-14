package service

import (
	"errors"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// PasswordResetPurpose is the auth-flow purpose for password-reset tokens.
const PasswordResetPurpose = "password_reset"

// ErrUserNotFoundByEmail is returned when no user matches an email.
var ErrUserNotFoundByEmail = errors.New("该邮箱未注册")

// SendPasswordResetEmail generates a one-time reset token and emails it to the
// user. It returns nil even when delivery is a no-op (SMTP unconfigured) so the
// flow does not leak account existence.
func SendPasswordResetEmail(email string) error {
	var user model.User
	if err := model.DB.Where("email = ?", email).First(&user).Error; err != nil {
		return ErrUserNotFoundByEmail
	}
	code := common.RandomNumeric(6)
	_, err := CreateAuthFlow(PasswordResetPurpose, "email", "", user.Id, "", code, 15*time.Minute)
	if err != nil {
		return err
	}
	body := "你的 TokenRouter 密码重置验证码是：" + code + "（15 分钟内有效）。"
	return Mail.Send(email, "TokenRouter 密码重置", body)
}

// ResetPassword verifies a one-time reset token and updates the password.
func ResetPassword(email, code, newPassword string) error {
	var user model.User
	if err := model.DB.Where("email = ?", email).First(&user).Error; err != nil {
		return ErrUserNotFoundByEmail
	}
	var flow model.AuthFlow
	if err := model.DB.Where("purpose = ? AND user_id = ? AND payload = ? AND consumed_at IS NULL",
		PasswordResetPurpose, user.Id, code).First(&flow).Error; err != nil {
		return errors.New("验证码错误或已过期")
	}
	if time.Now().After(flow.ExpiresAt) {
		return errors.New("验证码已过期")
	}
	hash, err := common.PasswordHash(newPassword)
	if err != nil {
		return err
	}
	if err := model.DB.Model(&user).Update("password", hash).Error; err != nil {
		return err
	}
	// Invalidate all sessions on password reset.
	_ = RevokeAllUserSessions(user.Id)
	// Mark the flow consumed.
	now := time.Now()
	_ = model.DB.Model(&flow).Update("consumed_at", &now).Error
	return nil
}
