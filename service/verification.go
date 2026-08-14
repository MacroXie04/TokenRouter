package service

import (
	"errors"
	"strings"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// EmailVerificationPurpose is the auth-flow purpose for email verification.
const EmailVerificationPurpose = "email_verification"

// ErrInvalidVerificationCode is returned when an email code is wrong/expired.
var ErrInvalidVerificationCode = errors.New("验证码错误或已过期")

// ErrEmailAlreadyTaken is returned when the email belongs to another user.
var ErrEmailAlreadyTaken = errors.New("邮箱已被其他用户使用")

// NormalizeEmail canonicalizes an email address for storage and comparison.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// SendEmailVerificationCode sends a one-time verification code to an email.
func SendEmailVerificationCode(email string) error {
	code := common.RandomNumeric(6)
	// The flow stores the normalized email in Intent so the code can only be
	// redeemed for the address it was sent to.
	if _, err := CreateAuthFlow(EmailVerificationPurpose, "email", NormalizeEmail(email), 0, "", code, 15*time.Minute); err != nil {
		return err
	}
	body := "你的 TokenRouter 邮箱验证码是：" + code + "（15 分钟内有效）。"
	return Mail.Send(email, "TokenRouter 邮箱验证", body)
}

// VerifyAndBindEmail verifies a one-time code sent to the given email and
// binds the email to the user, marking it verified. The code is keyed to the
// normalized email address and the email must not belong to another user.
func VerifyAndBindEmail(userId int, email, code string) error {
	email = NormalizeEmail(email)
	var flow model.AuthFlow
	if err := model.DB.Where("purpose = ? AND intent = ? AND payload = ? AND consumed_at IS NULL",
		EmailVerificationPurpose, email, code).First(&flow).Error; err != nil {
		return ErrInvalidVerificationCode
	}
	if time.Now().After(flow.ExpiresAt) {
		return ErrInvalidVerificationCode
	}
	var taken model.User
	if err := model.DB.Where("id <> ? AND LOWER(email) = ?", userId, email).First(&taken).Error; err == nil {
		return ErrEmailAlreadyTaken
	}
	if err := model.DB.Model(&model.User{}).Where("id = ?", userId).
		Updates(map[string]any{"email": email, "email_verified": true}).Error; err != nil {
		return err
	}
	now := time.Now()
	_ = model.DB.Model(&flow).Update("consumed_at", &now).Error
	return nil
}

// UserEmailVerified reports whether the user's email is verified.
func UserEmailVerified(userId int) bool {
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		return false
	}
	return user.EmailVerified
}
