package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"time"
)

// PasswordResetPurpose is the auth-flow purpose for password-reset tokens.
const PasswordResetPurpose = "password_reset"

// ErrUserNotFoundByEmail is returned when no user matches an email.
var ErrUserNotFoundByEmail = errors.New("该邮箱未注册")

// SendPasswordResetEmail generates a one-time reset token and emails it to the
// user. It returns nil even when delivery is a no-op (SMTP unconfigured) so the
// flow does not leak account existence.
func SendPasswordResetEmail(email string) error {
	user, email, err := verifiedEmailUser(email)
	if err != nil {
		return ErrUserNotFoundByEmail
	}
	code, err := cryptoutil.SecureRandomNumeric(6)
	if err != nil {
		return err
	}
	_, err = CreateAuthFlow(PasswordResetPurpose, "email", "", user.Id, "", code, 15*time.Minute)
	if err != nil {
		return err
	}
	body := "你的 TokenRouter 密码重置验证码是：" + code + "（15 分钟内有效）。"
	return mailtransport.Mail.Send(email, "TokenRouter 密码重置", body)
}

// ResetPassword verifies a one-time reset token and updates the password.
func ResetPassword(email, code, newPassword string) error {
	user, _, err := verifiedEmailUser(email)
	if err != nil {
		return ErrUserNotFoundByEmail
	}
	var flow model.AuthFlow
	nowUnix, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return err
	}
	if err := model.DB.Where("purpose = ? AND user_id = ? AND payload = ? AND consumed_at IS NULL AND expires_at > ?",
		PasswordResetPurpose, user.Id, code, time.Unix(nowUnix, 0).UTC()).First(&flow).Error; err != nil {
		return errors.New("验证码错误或已过期")
	}
	hash, err := cryptoutil.PasswordHash(newPassword)
	if err != nil {
		return err
	}
	_, err = consumeAuthFlowRecordWithAction(&flow, func(tx *gorm.DB, consumedFlow *model.AuthFlow) error {
		if consumedFlow == nil || consumedFlow.ConsumedAt == nil {
			return ErrInvalidFlowToken
		}
		current, nextVersion, err := lockNextAuthVersion(tx, user.Id)
		if err != nil {
			return err
		}
		if err := updateAuthVersionCAS(tx, user.Id, current.AuthVersion, nextVersion, map[string]any{
			"password": hash,
		}); err != nil {
			return err
		}
		return tx.Model(&model.UserSession{}).
			Where("user_id = ? AND status = ?", user.Id, SessionStatusActive).
			Updates(map[string]any{
				"status":         SessionStatusRevoked,
				"revoked_at":     consumedFlow.ConsumedAt.Unix(),
				"revoked_reason": "password_reset",
			}).Error
	})
	if errors.Is(err, ErrInvalidFlowToken) {
		return errors.New("验证码错误或已过期")
	}
	return err
}

func verifiedEmailUser(email string) (*model.User, string, error) {
	normalized, key, err := model.NormalizeVerifiedEmail(email)
	if err != nil {
		return nil, "", ErrUserNotFoundByEmail
	}
	var users []model.User
	if err := model.DB.Where("email_verified = ? AND verified_email_key = ?", true, key).
		Limit(2).Find(&users).Error; err != nil {
		return nil, "", err
	}
	if len(users) != 1 {
		return nil, "", ErrUserNotFoundByEmail
	}
	stored, storedKey, err := model.NormalizeVerifiedEmail(users[0].Email)
	if err != nil || stored != normalized || storedKey != key || users[0].VerifiedEmailKey == nil || *users[0].VerifiedEmailKey != key {
		return nil, "", ErrUserNotFoundByEmail
	}
	return &users[0], normalized, nil
}
