package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	mailtransport "github.com/tokenrouter/tokenrouter/internal/platform/mail"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"time"
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
	var err error
	email, _, err = model.NormalizeVerifiedEmail(email)
	if err != nil {
		return err
	}
	if err := setting.ValidateEmailRegistrationPolicy(email); err != nil {
		return err
	}
	code, err := cryptoutil.SecureRandomNumeric(6)
	if err != nil {
		return err
	}
	// Bind the code to a fixed-width digest rather than storing an address in
	// AuthFlow.Intent (a short generic ceremony discriminator). Legacy flows
	// remain readable during upgrades.
	if _, err := CreateAuthFlow(
		EmailVerificationPurpose, cryptoutil.SHA256Hex(email), "email", 0, "", code, 15*time.Minute,
	); err != nil {
		return err
	}
	body := "你的 TokenRouter 邮箱验证码是：" + code + "（15 分钟内有效）。"
	return mailtransport.Mail.Send(email, "TokenRouter 邮箱验证", body)
}

func ValidEmailVerificationCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func findEmailVerificationFlow(email, code string) (*model.AuthFlow, error) {
	if !ValidEmailVerificationCode(code) {
		return nil, ErrInvalidVerificationCode
	}
	nowUnix, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, err
	}
	var flow model.AuthFlow
	err = model.DB.Where(
		"purpose = ? AND payload = ? AND consumed_at IS NULL AND expires_at > ? AND ((provider = ? AND intent = ?) OR (provider = ? AND intent = ?))",
		EmailVerificationPurpose, code,
		time.Unix(nowUnix, 0).UTC(),
		cryptoutil.SHA256Hex(email), "email",
		"email", email,
	).Order("created_at DESC").First(&flow).Error
	if err != nil {
		return nil, ErrInvalidVerificationCode
	}
	return &flow, nil
}

// VerifyAndBindEmail verifies a one-time code sent to the given email and
// binds the email to the user, marking it verified. The code is keyed to the
// normalized email address and the email must not belong to another user.
func VerifyAndBindEmail(userId int, email, code string) error {
	var emailKey string
	var err error
	email, emailKey, err = model.NormalizeVerifiedEmail(email)
	if err != nil {
		return err
	}
	if err := setting.ValidateEmailRegistrationPolicy(email); err != nil {
		return err
	}
	flow, err := findEmailVerificationFlow(email, code)
	if err != nil {
		return err
	}
	_, err = consumeAuthFlowRecordWithAction(flow, func(tx *gorm.DB, _ *model.AuthFlow) error {
		var taken model.User
		lookup := tx.Unscoped().Where("id <> ? AND verified_email_key = ?", userId, emailKey).First(&taken)
		if lookup.Error == nil {
			if normalized, _, normalizeErr := model.NormalizeVerifiedEmail(taken.Email); normalizeErr != nil || normalized != email {
				return model.ErrAmbiguousPersistentIdentity
			}
			return ErrEmailAlreadyTaken
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		result := tx.Model(&model.User{}).Where("id = ?", userId).
			Updates(map[string]any{"email": email, "email_verified": true, "verified_email_key": &emailKey})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if errors.Is(err, ErrInvalidFlowToken) {
		return ErrInvalidVerificationCode
	}
	return err
}

// CreateUserWithReferralAndVerifiedEmail consumes an address-bound one-time
// code, creates the account, and applies referral credits in one transaction.
// A duplicate/race/downstream failure rolls back both the account and code so
// a transient failure can be retried safely.
func CreateUserWithReferralAndVerifiedEmail(user *model.User, email, code string) error {
	if user == nil {
		return errors.New("user is required")
	}
	normalized, emailKey, err := model.NormalizeVerifiedEmail(email)
	if err != nil {
		return err
	}
	if err := setting.ValidateEmailRegistrationPolicy(normalized); err != nil {
		return err
	}
	flow, err := findEmailVerificationFlow(normalized, code)
	if err != nil {
		return err
	}
	plan, err := PlanRegistrationMutationFromEnvironment(user.Username)
	if err != nil {
		return err
	}
	candidate := *user
	candidate.Email = normalized
	candidate.EmailVerified = true
	candidate.VerifiedEmailKey = &emailKey
	_, err = consumeAuthFlowRecordWithAction(flow, func(tx *gorm.DB, _ *model.AuthFlow) error {
		if err := requirePasswordRegistrationEnabledWithTx(tx); err != nil {
			return err
		}
		var taken model.User
		lookup := tx.Unscoped().Where("verified_email_key = ?", emailKey).First(&taken)
		if lookup.Error == nil {
			return ErrEmailAlreadyTaken
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		if err := InsertPlannedRegistrationUserWithTx(tx, &candidate, plan); err != nil {
			return err
		}
		return creditInviterTx(tx, candidate.InviterId, candidate.Id)
	})
	if errors.Is(err, ErrInvalidFlowToken) {
		return ErrInvalidVerificationCode
	}
	if err != nil {
		return err
	}
	*user = candidate
	return nil
}

// UserEmailVerified reports whether the user's email is verified.
func UserEmailVerified(userId int) bool {
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		return false
	}
	if !user.EmailVerified || user.VerifiedEmailKey == nil {
		return false
	}
	_, key, err := model.NormalizeVerifiedEmail(user.Email)
	return err == nil && key == *user.VerifiedEmailKey
}
