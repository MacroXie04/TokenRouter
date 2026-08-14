package service

import (
	"errors"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// 2FA errors.
var (
	ErrTwoFANotEnabled  = errors.New("2FA not enabled")
	ErrTwoFAInvalidCode = errors.New("invalid 2FA code")
	ErrTwoFALocked      = errors.New("2FA locked")
)

const (
	max2FAFailedAttempts = 5
	twoFALockDuration    = 5 * time.Minute
	backupCodeCount      = 8
)

// GenerateTwoFASecret creates (or resets) a user's TOTP secret and returns the
// provisioning URL and raw secret for QR display. It does not enable 2FA.
func GenerateTwoFASecret(userId int, accountName string) (secret, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      common.ProductName,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", err
	}
	secret = key.Secret()
	otpauthURL = key.URL()

	var twoFA model.TwoFA
	res := model.DB.Where("user_id = ?", userId).First(&twoFA)
	if res.Error != nil {
		twoFA = model.TwoFA{UserId: userId, Secret: secret, IsEnabled: false, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := model.DB.Create(&twoFA).Error; err != nil {
			return "", "", err
		}
	} else {
		if err := model.DB.Model(&twoFA).Updates(map[string]any{
			"secret": secret, "is_enabled": false, "failed_attempts": 0,
		}).Error; err != nil {
			return "", "", err
		}
		_ = model.DB.Where("user_id = ?", userId).Delete(&model.TwoFABackupCode{}).Error
	}
	return secret, otpauthURL, nil
}

// EnableTwoFA verifies a TOTP code and enables 2FA, returning one-time backup
// codes. The secret must already exist via GenerateTwoFASecret.
func EnableTwoFA(userId int, code string) ([]string, error) {
	var twoFA model.TwoFA
	if err := model.DB.Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		return nil, ErrTwoFANotEnabled
	}
	if !totp.Validate(code, twoFA.Secret) {
		return nil, ErrTwoFAInvalidCode
	}
	if err := model.DB.Model(&twoFA).Updates(map[string]any{
		"is_enabled": true, "failed_attempts": 0,
	}).Error; err != nil {
		return nil, err
	}
	codes := make([]string, 0, backupCodeCount)
	for i := 0; i < backupCodeCount; i++ {
		c := common.RandomNumeric(8)
		codes = append(codes, c)
		bc := model.TwoFABackupCode{UserId: userId, CodeHash: common.SHA256Hex(c), CreatedAt: time.Now()}
		if err := model.DB.Create(&bc).Error; err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// VerifyTwoFA validates a TOTP code with failed-attempt lockout. It returns nil
// when 2FA is not enabled (a no-op for users who never opted in).
func VerifyTwoFA(userId int, code string) error {
	var twoFA model.TwoFA
	if err := model.DB.Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		return ErrTwoFANotEnabled
	}
	if !twoFA.IsEnabled {
		return nil
	}
	if twoFA.LockedUntil != nil && time.Now().Before(*twoFA.LockedUntil) {
		return ErrTwoFALocked
	}
	if totp.Validate(code, twoFA.Secret) {
		_ = model.DB.Model(&twoFA).Updates(map[string]any{
			"failed_attempts": 0, "last_used_at": time.Now(),
		}).Error
		return nil
	}
	failed := twoFA.FailedAttempts + 1
	updates := map[string]any{"failed_attempts": failed}
	if failed >= max2FAFailedAttempts {
		locked := time.Now().Add(twoFALockDuration)
		updates["locked_until"] = &locked
		updates["failed_attempts"] = 0
	}
	_ = model.DB.Model(&twoFA).Updates(updates).Error
	return ErrTwoFAInvalidCode
}

// VerifyBackupCode validates a single-use backup code.
func VerifyBackupCode(userId int, code string) error {
	hash := common.SHA256Hex(code)
	var bc model.TwoFABackupCode
	if err := model.DB.Where("user_id = ? AND code_hash = ? AND is_used = ?", userId, hash, false).First(&bc).Error; err != nil {
		return ErrTwoFAInvalidCode
	}
	usedAt := time.Now()
	_ = model.DB.Model(&bc).Updates(map[string]any{"is_used": true, "used_at": &usedAt}).Error
	return nil
}

// DisableTwoFA disables 2FA and removes backup codes.
func DisableTwoFA(userId int) error {
	_ = model.DB.Where("user_id = ?", userId).Delete(&model.TwoFABackupCode{}).Error
	return model.DB.Model(&model.TwoFA{}).Where("user_id = ?", userId).
		Updates(map[string]any{"is_enabled": false, "failed_attempts": 0}).Error
}

// TwoFAStatus reports whether 2FA is enabled for a user.
func TwoFAStatus(userId int) bool {
	var twoFA model.TwoFA
	if err := model.DB.Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		return false
	}
	return twoFA.IsEnabled
}

// RegenerateBackupCodes replaces the user's 2FA backup codes with a fresh set
// of one-time codes (requires 2FA to be enabled).
func RegenerateBackupCodes(userId int) ([]string, error) {
	var twoFA model.TwoFA
	if err := model.DB.Where("user_id = ? AND is_enabled = ?", userId, true).First(&twoFA).Error; err != nil {
		return nil, ErrTwoFANotEnabled
	}
	if err := model.DB.Where("user_id = ?", userId).Delete(&model.TwoFABackupCode{}).Error; err != nil {
		return nil, err
	}
	codes := make([]string, 0, backupCodeCount)
	for i := 0; i < backupCodeCount; i++ {
		c := common.RandomNumeric(8)
		codes = append(codes, c)
		bc := model.TwoFABackupCode{UserId: userId, CodeHash: common.SHA256Hex(c), CreatedAt: time.Now()}
		if err := model.DB.Create(&bc).Error; err != nil {
			return nil, err
		}
	}
	return codes, nil
}
