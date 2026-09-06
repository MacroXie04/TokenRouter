package service

import (
	"errors"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// 2FA errors.
var (
	ErrTwoFANotEnabled         = errors.New("2FA not enabled")
	ErrTwoFAInvalidCode        = errors.New("invalid 2FA code")
	ErrTwoFALocked             = errors.New("2FA locked")
	ErrTwoFAResetProofRequired = errors.New("2FA reset proof required")
)

const (
	max2FAFailedAttempts = 5
	twoFALockDuration    = 5 * time.Minute
	backupCodeCount      = 8
)

type twoFADatabaseClock func(*gorm.DB) (time.Time, error)

func twoFADatabaseNow(tx *gorm.DB) (time.Time, error) {
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(now, 0).UTC(), nil
}

func validateTwoFATOTPAt(code, secret string, now time.Time) bool {
	valid, err := totp.ValidateCustom(code, secret, now.UTC(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && valid
}

// GenerateTwoFASecret creates a TOTP secret for first-time setup. It refuses to
// overwrite an enabled factor; callers must validate a session-bound step-up
// proof and then use ResetTwoFASecret for that operation.
func GenerateTwoFASecret(userId int, accountName string) (secret, otpauthURL string, err error) {
	return generateTwoFASecret(userId, accountName, false)
}

// ResetTwoFASecret replaces an enabled TOTP secret after the controller has
// validated a session-bound twofa.reset proof. The factor remains enabled while
// the replacement is pending, which keeps concurrent unproved starts closed.
func ResetTwoFASecret(userId int, accountName string) (secret, otpauthURL string, err error) {
	return generateTwoFASecret(userId, accountName, true)
}

func generateTwoFASecret(userId int, accountName string, resetAuthorized bool) (secret, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      common.ProductName,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", err
	}
	secret = key.Secret()
	otpauthURL = key.URL()

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var twoFA model.TwoFA
		queryErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userId).First(&twoFA).Error
		switch {
		case errors.Is(queryErr, gorm.ErrRecordNotFound):
			now, err := twoFADatabaseNow(tx)
			if err != nil {
				return err
			}
			twoFA = model.TwoFA{
				UserId: userId, Secret: secret, IsEnabled: false,
				CreatedAt: now, UpdatedAt: now,
			}
			return tx.Create(&twoFA).Error
		case queryErr != nil:
			return queryErr
		case twoFA.IsEnabled && !resetAuthorized:
			return ErrTwoFAResetProofRequired
		default:
			now, err := twoFADatabaseNow(tx)
			if err != nil {
				return err
			}
			// Do not clear IsEnabled or backup codes here. In particular, an
			// authorized replacement must not open a window in which a second,
			// unproved request looks like first-time setup.
			updates := map[string]any{
				"secret": secret, "failed_attempts": 0,
				"locked_until": nil, "last_used_at": nil, "updated_at": now,
			}
			query := tx.Model(&twoFA)
			if !resetAuthorized {
				// Keep the authorization decision in the write predicate as well
				// as the locked read. This remains fail-closed on engines such as
				// SQLite that do not implement SELECT ... FOR UPDATE.
				query = query.Where("is_enabled = ?", false)
			}
			res := query.Updates(updates)
			if res.Error != nil {
				return res.Error
			}
			if !resetAuthorized && res.RowsAffected == 0 {
				return ErrTwoFAResetProofRequired
			}
			return nil
		}
	})
	if err != nil {
		return "", "", err
	}
	return secret, otpauthURL, nil
}

// EnableTwoFA verifies a TOTP code and enables 2FA, returning one-time backup
// codes. The secret must already exist via GenerateTwoFASecret.
func EnableTwoFA(userId int, code string) ([]string, error) {
	var codes []string
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		codes, err = enableTwoFATx(tx, userId, code)
		return err
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// EnableTwoFAAndRotateSession enables the factor and invalidates other access
// tokens atomically, retaining only the browser session that performed setup.
func EnableTwoFAAndRotateSession(userId int, code, keepSid string) ([]string, error) {
	var codes []string
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		codes, err = enableTwoFATx(tx, userId, code)
		if err != nil {
			return err
		}
		return bumpAuthVersionKeepSession(tx, userId, keepSid)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// EnableTwoFAAndRotateSessionWithAccess enables the factor, rotates the
// authenticated browser session, and signs its replacement access token in
// the same transaction. The recovery codes and access token are therefore
// already available when the factor commit becomes visible; a storage outage
// immediately after commit cannot strand the user without either credential.
func EnableTwoFAAndRotateSessionWithAccess(userId int, code, keepSid string) ([]string, string, error) {
	var (
		codes       []string
		accessToken string
	)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		codes, err = enableTwoFATx(tx, userId, code)
		if err != nil {
			return err
		}
		accessToken, err = bumpAuthVersionKeepSessionWithAccess(tx, userId, keepSid)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return codes, accessToken, nil
}

func enableTwoFATx(tx *gorm.DB, userId int, code string) ([]string, error) {
	return enableTwoFATxWithClock(tx, userId, code, twoFADatabaseNow)
}

func enableTwoFATxWithClock(tx *gorm.DB, userId int, code string, clock twoFADatabaseClock) ([]string, error) {
	var twoFA model.TwoFA
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTwoFANotEnabled
		}
		return nil, err
	}
	now, err := clock(tx)
	if err != nil {
		return nil, err
	}
	if !validateTwoFATOTPAt(code, twoFA.Secret, now) {
		return nil, ErrTwoFAInvalidCode
	}
	if err := deleteTwoFABackupCodesAt(tx, userId, now); err != nil {
		return nil, err
	}
	codes, err := createTwoFABackupCodesAt(tx, userId, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Model(&twoFA).Updates(map[string]any{
		"is_enabled": true, "failed_attempts": 0, "locked_until": nil, "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	return codes, nil
}

// VerifyTwoFA validates a TOTP code with failed-attempt lockout. It returns nil
// when 2FA is not enabled (a no-op for users who never opted in).
func VerifyTwoFA(userId int, code string) error {
	return verifyTwoFAWithClock(userId, code, twoFADatabaseNow)
}

func verifyTwoFAWithClock(userId int, code string, clock twoFADatabaseClock) error {
	if userId <= 0 {
		return ErrTwoFANotEnabled
	}
	var verificationErr error
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var twoFA model.TwoFA
		query := tx.Where("user_id = ?", userId)
		if model.UsingPostgreSQL() || model.UsingMySQL() {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&twoFA).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTwoFANotEnabled
			}
			return err
		}
		if !twoFA.IsEnabled {
			return nil
		}
		now, err := clock(tx)
		if err != nil {
			return err
		}
		if twoFA.LockedUntil != nil && now.Before(*twoFA.LockedUntil) {
			return ErrTwoFALocked
		}
		if twoFA.FailedAttempts < 0 || twoFA.FailedAttempts >= max2FAFailedAttempts {
			return errors.New("invalid 2FA attempt state")
		}

		updates := map[string]any{}
		verificationErr = ErrTwoFAInvalidCode
		if validateTwoFATOTPAt(code, twoFA.Secret, now) {
			updates["failed_attempts"] = 0
			updates["locked_until"] = nil
			updates["last_used_at"] = now
			verificationErr = nil
		} else {
			failed := twoFA.FailedAttempts + 1
			updates["failed_attempts"] = failed
			if failed >= max2FAFailedAttempts {
				locked := now.Add(twoFALockDuration)
				updates["locked_until"] = &locked
				updates["failed_attempts"] = 0
			}
		}
		updates["updated_at"] = now

		// Retain the locked snapshot in the write predicate. This prevents an
		// old secret or counter value from overwriting a concurrent reset on
		// SQLite, where SELECT FOR UPDATE is unavailable.
		result := tx.Model(&model.TwoFA{}).
			Where("id = ? AND user_id = ? AND is_enabled = ? AND secret = ? AND failed_attempts = ?",
				twoFA.Id, userId, true, twoFA.Secret, twoFA.FailedAttempts).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("2FA state changed concurrently")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return verificationErr
}

// VerifyBackupCode validates a single-use backup code.
func VerifyBackupCode(userId int, code string) error {
	hash := common.SHA256Hex(code)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		usedAt, err := twoFADatabaseNow(tx)
		if err != nil {
			return err
		}
		res := tx.Model(&model.TwoFABackupCode{}).
			Where("user_id = ? AND code_hash = ? AND is_used = ?", userId, hash, false).
			Updates(map[string]any{"is_used": true, "used_at": &usedAt})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTwoFAInvalidCode
		}
		return nil
	})
}

// DisableTwoFA disables 2FA and removes backup codes.
func DisableTwoFA(userId int) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return disableTwoFATx(tx, userId)
	})
}

// DisableTwoFAAndRevokeSessions is the administrator reset variant. Factor
// removal and session revocation commit together.
func DisableTwoFAAndRevokeSessions(userId int) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := disableTwoFATx(tx, userId); err != nil {
			return err
		}
		return revokeAllUserSessions(tx, userId, "twofa_reset")
	})
}

func disableTwoFATx(tx *gorm.DB, userId int) error {
	var twoFA model.TwoFA
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTwoFANotEnabled
		}
		return err
	}
	now, err := twoFADatabaseNow(tx)
	if err != nil {
		return err
	}
	if err := deleteTwoFABackupCodesAt(tx, userId, now); err != nil {
		return err
	}
	return tx.Model(&twoFA).Updates(map[string]any{
		"is_enabled": false, "failed_attempts": 0, "locked_until": nil, "updated_at": now,
	}).Error
}

// DisableTwoFAWithCode verifies the existing TOTP or a backup code and
// disables the factor in one transaction. Failed mutation rolls verification
// state back, so a recovery code is never lost without disabling the factor.
func DisableTwoFAWithCode(userId int, code string) error {
	var verificationErr error
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		verificationErr, err = disableTwoFAWithCodeTx(tx, userId, code)
		return err
	})
	if err != nil {
		return err
	}
	return verificationErr
}

// DisableTwoFAWithCodeAndRotateSession verifies and removes the factor while
// rotating authentication state in the same transaction. Invalid attempts
// still commit their lockout counters, matching DisableTwoFAWithCode.
func DisableTwoFAWithCodeAndRotateSession(userId int, code, keepSid string) error {
	var verificationErr error
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		verificationErr, err = disableTwoFAWithCodeTx(tx, userId, code)
		if err != nil || verificationErr != nil {
			return err
		}
		return bumpAuthVersionKeepSession(tx, userId, keepSid)
	})
	if err != nil {
		return err
	}
	return verificationErr
}

// DisableTwoFAWithCodeAndRotateSessionWithAccess disables the factor and
// returns an access token signed from the kept session's transaction-local
// snapshot. Invalid attempts still commit lockout counters, while any token
// signing or session failure rolls factor removal back.
func DisableTwoFAWithCodeAndRotateSessionWithAccess(userId int, code, keepSid string) (string, error) {
	var (
		verificationErr error
		accessToken     string
	)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		verificationErr, err = disableTwoFAWithCodeTx(tx, userId, code)
		if err != nil || verificationErr != nil {
			return err
		}
		accessToken, err = bumpAuthVersionKeepSessionWithAccess(tx, userId, keepSid)
		return err
	})
	if err != nil {
		return "", err
	}
	if verificationErr != nil {
		return "", verificationErr
	}
	return accessToken, nil
}

// bumpAuthVersionKeepSessionWithAccess builds on the common auth-version
// rotation primitive, then signs from snapshots read and validated before the
// surrounding transaction commits. It lives beside the 2FA workflows so
// callers do not need a post-commit user or session read.
func bumpAuthVersionKeepSessionWithAccess(tx *gorm.DB, userId int, keepSid string) (string, error) {
	if err := bumpAuthVersionKeepSession(tx, userId, keepSid); err != nil {
		return "", err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return "", err
	}
	var user model.User
	if err := subscriptionLockForUpdate(tx).First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSessionRevoked
		}
		return "", err
	}
	var session model.UserSession
	if err := subscriptionLockForUpdate(tx).
		Where("user_id = ? AND sid = ?", userId, keepSid).
		First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSessionRevoked
		}
		return "", err
	}
	return signSessionAccessToken(&session, &user, now)
}

func disableTwoFAWithCodeTx(tx *gorm.DB, userId int, code string) (verificationErr, storageErr error) {
	return disableTwoFAWithCodeTxWithClock(tx, userId, code, twoFADatabaseNow)
}

func disableTwoFAWithCodeTxWithClock(tx *gorm.DB, userId int, code string, clock twoFADatabaseClock) (verificationErr, storageErr error) {
	var twoFA model.TwoFA
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTwoFANotEnabled, nil
		}
		return nil, err
	}
	if !twoFA.IsEnabled {
		return ErrTwoFANotEnabled, nil
	}
	now, err := clock(tx)
	if err != nil {
		return nil, err
	}
	if twoFA.LockedUntil != nil && now.Before(*twoFA.LockedUntil) {
		return ErrTwoFALocked, nil
	}
	if twoFA.FailedAttempts < 0 || twoFA.FailedAttempts >= max2FAFailedAttempts {
		return nil, errors.New("invalid 2FA attempt state")
	}

	verified := validateTwoFATOTPAt(code, twoFA.Secret, now)
	if !verified {
		var backup model.TwoFABackupCode
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND code_hash = ? AND is_used = ?", userId, common.SHA256Hex(code), false).
			First(&backup).Error
		switch {
		case err == nil:
			verified = true
		case errors.Is(err, gorm.ErrRecordNotFound):
			failed := twoFA.FailedAttempts + 1
			updates := map[string]any{"failed_attempts": failed, "updated_at": now}
			if failed >= max2FAFailedAttempts {
				locked := now.Add(twoFALockDuration)
				updates["locked_until"] = &locked
				updates["failed_attempts"] = 0
			}
			result := tx.Model(&model.TwoFA{}).
				Where("id = ? AND user_id = ? AND is_enabled = ? AND secret = ? AND failed_attempts = ?",
					twoFA.Id, userId, true, twoFA.Secret, twoFA.FailedAttempts).
				Updates(updates)
			if result.Error != nil {
				return nil, result.Error
			}
			if result.RowsAffected != 1 {
				return nil, errors.New("2FA state changed concurrently")
			}
			return ErrTwoFAInvalidCode, nil
		default:
			return nil, err
		}
	}

	if err := deleteTwoFABackupCodesAt(tx, userId, now); err != nil {
		return nil, err
	}
	updates := map[string]any{
		"is_enabled": false, "failed_attempts": 0, "locked_until": nil,
		"last_used_at": now, "updated_at": now,
	}
	if err := tx.Model(&twoFA).Updates(updates).Error; err != nil {
		return nil, err
	}
	return nil, nil
}

// TwoFAStatusChecked reports whether 2FA is enabled for a user and
// distinguishes an absent/disabled factor from a storage failure.
func TwoFAStatusChecked(userId int) (bool, error) {
	if model.DB == nil {
		return false, errors.New("database is nil")
	}
	var twoFA model.TwoFA
	if err := model.DB.Where("user_id = ?", userId).First(&twoFA).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return twoFA.IsEnabled, nil
}

// TwoFAStatus is the compatibility form for non-authorizing status displays.
// Security decisions must use TwoFAStatusChecked and fail closed on errors.
func TwoFAStatus(userId int) bool {
	enabled, err := TwoFAStatusChecked(userId)
	if err != nil {
		common.SysError("query 2FA status failed: " + err.Error())
		return false
	}
	return enabled
}

// RegenerateBackupCodes replaces the user's 2FA backup codes with a fresh set
// of one-time codes (requires 2FA to be enabled).
func RegenerateBackupCodes(userId int) ([]string, error) {
	var codes []string
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		codes, err = regenerateBackupCodesTx(tx, userId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// RegenerateBackupCodesAndRotateSession replaces the recovery set and rotates
// authentication state in one transaction.
func RegenerateBackupCodesAndRotateSession(userId int, keepSid string) ([]string, error) {
	var codes []string
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		codes, err = regenerateBackupCodesTx(tx, userId)
		if err != nil {
			return err
		}
		return bumpAuthVersionKeepSession(tx, userId, keepSid)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

func regenerateBackupCodesTx(tx *gorm.DB, userId int) ([]string, error) {
	var twoFA model.TwoFA
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND is_enabled = ?", userId, true).First(&twoFA).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTwoFANotEnabled
		}
		return nil, err
	}
	now, err := twoFADatabaseNow(tx)
	if err != nil {
		return nil, err
	}
	if err := deleteTwoFABackupCodesAt(tx, userId, now); err != nil {
		return nil, err
	}
	return createTwoFABackupCodesAt(tx, userId, now)
}

func createTwoFABackupCodesAt(tx *gorm.DB, userId int, now time.Time) ([]string, error) {
	codes := make([]string, 0, backupCodeCount)
	records := make([]model.TwoFABackupCode, 0, backupCodeCount)
	for i := 0; i < backupCodeCount; i++ {
		code, err := common.SecureRandomNumeric(8)
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
		records = append(records, model.TwoFABackupCode{
			UserId: userId, CodeHash: common.SHA256Hex(code), CreatedAt: now,
		})
	}
	if err := tx.Create(&records).Error; err != nil {
		return nil, err
	}
	return codes, nil
}

func deleteTwoFABackupCodesAt(tx *gorm.DB, userId int, now time.Time) error {
	return tx.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", userId).
		Update("deleted_at", now).Error
}
