package auth

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func initTwoFADB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.TwoFABackupCode{}))
	model.DB = db
	model.LOG_DB = db
}

func seedTwoFASession(t *testing.T, userId int) string {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userId).Update("auth_version", 1).Error)
	sid := "twofa-current-session"
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: sid, UserID: userId, Version: 1, UserAuthVersion: 1,
		Status: SessionStatusActive, RefreshHash: "hash", ExpiresAt: wallclock.NowTimestamp() + 60,
	}).Error)
	return sid
}

func TestTwoFAEnableAndVerify(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)

	secret, url, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	assert.NotEmpty(t, secret)
	assert.Contains(t, url, "otpauth://")

	// Enable with a valid code.
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	codes, err := EnableTwoFA(u.Id, code)
	require.NoError(t, err)
	assert.Len(t, codes, backupCodeCount)
	assert.True(t, TwoFAStatus(u.Id))

	// Verify with a fresh valid code.
	code2, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	assert.NoError(t, VerifyTwoFA(u.Id, code2))

	// Invalid code fails.
	assert.Equal(t, ErrTwoFAInvalidCode, VerifyTwoFA(u.Id, "000000"))
}

func TestTwoFAStatusCheckedDistinguishesMissingFactorFromDatabaseFailure(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)

	enabled, err := TwoFAStatusChecked(u.Id)
	require.NoError(t, err)
	assert.False(t, enabled)

	require.NoError(t, model.DB.Migrator().DropTable(&model.TwoFA{}))
	_, err = TwoFAStatusChecked(u.Id)
	require.Error(t, err)
}

func TestEnableTwoFAAndRotateSessionRollsBackFactorOnSessionFailure(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	sid := seedTwoFASession(t, u.Id)
	secret, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_twofa_session_update BEFORE UPDATE ON user_sessions
		BEGIN SELECT RAISE(FAIL, 'forced session update failure'); END;
	`).Error)

	_, err = EnableTwoFAAndRotateSession(u.Id, code, sid)
	require.Error(t, err)
	enabled, statusErr := TwoFAStatusChecked(u.Id)
	require.NoError(t, statusErr)
	assert.False(t, enabled)
	var backupCount int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).Where("user_id = ?", u.Id).Count(&backupCount).Error)
	assert.Zero(t, backupCount)
}

func TestEnableTwoFAAndRotateSessionWithAccessRollsBackOnSigningFailure(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	sid := seedTwoFASession(t, u.Id)
	secret, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	// Rotation itself can update a disabled user's counters, but the
	// transaction-local signer rejects that snapshot. Factor enablement and
	// recovery-code creation must roll back with the signing failure.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", u.Id).
		Update("status", model.UserStatusDisabled).Error)

	codes, access, err := EnableTwoFAAndRotateSessionWithAccess(u.Id, code, sid)
	require.ErrorIs(t, err, ErrSessionRevoked)
	assert.Nil(t, codes)
	assert.Empty(t, access)

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	assert.False(t, factor.IsEnabled)
	var backupCount int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", u.Id).Count(&backupCount).Error)
	assert.Zero(t, backupCount)
	var user model.User
	require.NoError(t, model.DB.First(&user, u.Id).Error)
	assert.Equal(t, int64(1), user.AuthVersion)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, int64(1), session.UserAuthVersion)
}

func TestDisableTwoFAWithCodeAndRotateSessionRollsBackOnSessionFailure(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	sid := seedTwoFASession(t, u.Id)
	secret, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	backupCodes, err := EnableTwoFA(u.Id, code)
	require.NoError(t, err)
	require.NotEmpty(t, backupCodes)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_twofa_session_update BEFORE UPDATE ON user_sessions
		BEGIN SELECT RAISE(FAIL, 'forced session update failure'); END;
	`).Error)

	err = DisableTwoFAWithCodeAndRotateSession(u.Id, code, sid)
	require.Error(t, err)
	enabled, statusErr := TwoFAStatusChecked(u.Id)
	require.NoError(t, statusErr)
	assert.True(t, enabled)
	var backupCount int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).Where("user_id = ?", u.Id).Count(&backupCount).Error)
	assert.EqualValues(t, backupCodeCount, backupCount)
}

func TestDisableTwoFAWithCodeAndRotateSessionWithAccessRollsBackOnSigningFailure(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	sid := seedTwoFASession(t, u.Id)
	secret, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	backupCodes, err := EnableTwoFA(u.Id, code)
	require.NoError(t, err)
	require.Len(t, backupCodes, backupCodeCount)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", u.Id).
		Update("status", model.UserStatusDisabled).Error)

	access, err := DisableTwoFAWithCodeAndRotateSessionWithAccess(u.Id, backupCodes[0], sid)
	require.ErrorIs(t, err, ErrSessionRevoked)
	assert.Empty(t, access)

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	assert.True(t, factor.IsEnabled)
	var backupCount int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", u.Id).Count(&backupCount).Error)
	assert.EqualValues(t, backupCodeCount, backupCount)
	var user model.User
	require.NoError(t, model.DB.First(&user, u.Id).Error)
	assert.Equal(t, int64(1), user.AuthVersion)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, int64(1), session.UserAuthVersion)
}

func TestTwoFABackupCodeSingleUse(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	secret, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	codes, err := EnableTwoFA(u.Id, code)
	require.NoError(t, err)

	// A backup code works once.
	assert.NoError(t, VerifyBackupCode(u.Id, codes[0]))
	// Second use is rejected.
	assert.Equal(t, ErrTwoFAInvalidCode, VerifyBackupCode(u.Id, codes[0]))
}

func TestTwoFALockout(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	secret, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	_, err = EnableTwoFA(u.Id, code)
	require.NoError(t, err)

	for i := 0; i < max2FAFailedAttempts; i++ {
		assert.Equal(t, ErrTwoFAInvalidCode, VerifyTwoFA(u.Id, "000000"))
	}
	// Locked now: even a valid code is rejected.
	assert.Equal(t, ErrTwoFALocked, VerifyTwoFA(u.Id, code))
}

func TestTwoFATOTPUsesAuthoritativeClockDespiteProcessSkew(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	const secret = "JBSWY3DPEHPK3PXP"
	// This fixed historical instant is deliberately unrelated to the test
	// process clock, making a process-clock regression deterministic.
	databaseNow := time.Unix(946684800, 0).UTC()
	code, err := totp.GenerateCode(secret, databaseNow)
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: secret, IsEnabled: true,
	}).Error)

	err = verifyTwoFAWithClock(u.Id, code, func(*gorm.DB) (time.Time, error) {
		return databaseNow, nil
	})
	require.NoError(t, err)

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	require.NotNil(t, factor.LastUsedAt)
	assert.True(t, factor.LastUsedAt.Equal(databaseNow))
	assert.True(t, factor.UpdatedAt.Equal(databaseNow))
}

func TestEnableAndDisableTwoFAUseAuthoritativeTOTPClock(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	const secret = "JBSWY3DPEHPK3PXP"
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: secret, IsEnabled: false,
	}).Error)
	databaseNow := time.Unix(1_100_000_000, 0).UTC()
	clock := func(*gorm.DB) (time.Time, error) { return databaseNow, nil }
	enableCode, err := totp.GenerateCode(secret, databaseNow)
	require.NoError(t, err)
	var backupCodes []string
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		var storageErr error
		backupCodes, storageErr = enableTwoFATxWithClock(tx, u.Id, enableCode, clock)
		return storageErr
	}))
	require.Len(t, backupCodes, backupCodeCount)

	var backups []model.TwoFABackupCode
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).Find(&backups).Error)
	require.Len(t, backups, backupCodeCount)
	for i := range backups {
		assert.True(t, backups[i].CreatedAt.Equal(databaseNow))
	}

	databaseNow = databaseNow.Add(90 * time.Second)
	disableCode, err := totp.GenerateCode(secret, databaseNow)
	require.NoError(t, err)
	var verificationErr error
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		var storageErr error
		verificationErr, storageErr = disableTwoFAWithCodeTxWithClock(tx, u.Id, disableCode, clock)
		return storageErr
	}))
	require.NoError(t, verificationErr)

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	assert.False(t, factor.IsEnabled)
	require.NotNil(t, factor.LastUsedAt)
	assert.True(t, factor.LastUsedAt.Equal(databaseNow))
	assert.True(t, factor.UpdatedAt.Equal(databaseNow))
	var activeBackupCount int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", u.Id).Count(&activeBackupCount).Error)
	assert.Zero(t, activeBackupCount)
}

func TestTwoFALockoutUsesAuthoritativeClockBoundary(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	const secret = "JBSWY3DPEHPK3PXP"
	databaseNow := time.Unix(1_000_000_000, 0).UTC()
	clock := func(*gorm.DB) (time.Time, error) { return databaseNow, nil }
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: secret, IsEnabled: true,
	}).Error)

	for i := 0; i < max2FAFailedAttempts; i++ {
		require.ErrorIs(t, verifyTwoFAWithClock(u.Id, "invalid", clock), ErrTwoFAInvalidCode)
	}
	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	require.NotNil(t, factor.LockedUntil)
	assert.True(t, factor.LockedUntil.Equal(databaseNow.Add(twoFALockDuration)))

	databaseNow = databaseNow.Add(twoFALockDuration - time.Second)
	code, err := totp.GenerateCode(secret, databaseNow)
	require.NoError(t, err)
	require.ErrorIs(t, verifyTwoFAWithClock(u.Id, code, clock), ErrTwoFALocked)

	databaseNow = databaseNow.Add(time.Second)
	code, err = totp.GenerateCode(secret, databaseNow)
	require.NoError(t, err)
	require.NoError(t, verifyTwoFAWithClock(u.Id, code, clock),
		"the lock expires exactly at the shared database-clock boundary")
	factor = model.TwoFA{}
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	assert.Nil(t, factor.LockedUntil)
	require.NotNil(t, factor.LastUsedAt)
	assert.True(t, factor.LastUsedAt.Equal(databaseNow))
}

func TestTwoFAVerificationFailsClosedWhenAuthoritativeClockFails(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	const secret = "JBSWY3DPEHPK3PXP"
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: secret, IsEnabled: true,
	}).Error)
	clockErr := errors.New("injected database clock failure")
	err := verifyTwoFAWithClock(u.Id, "123456", func(*gorm.DB) (time.Time, error) {
		return time.Time{}, clockErr
	})
	require.ErrorIs(t, err, clockErr)

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	assert.Zero(t, factor.FailedAttempts)
	assert.Nil(t, factor.LockedUntil)
	assert.Nil(t, factor.LastUsedAt)
}

func TestConcurrentTwoFAFailuresReachLockoutWithoutLostWrites(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true,
	}).Error)

	start := make(chan struct{})
	errs := make(chan error, max2FAFailedAttempts)
	var ready sync.WaitGroup
	ready.Add(max2FAFailedAttempts)
	for range max2FAFailedAttempts {
		go func() {
			ready.Done()
			<-start
			errs <- VerifyTwoFA(u.Id, "invalid")
		}()
	}
	ready.Wait()
	close(start)
	for range max2FAFailedAttempts {
		assert.ErrorIs(t, <-errs, ErrTwoFAInvalidCode)
	}

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	require.NotNil(t, factor.LockedUntil)
	assert.True(t, factor.LockedUntil.After(time.Now()))
	assert.ErrorIs(t, VerifyTwoFA(u.Id, "invalid"), ErrTwoFALocked)
}

func TestVerifyTwoFAFailsClosedWhenStateCannotBePersisted(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	secret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: secret, IsEnabled: true,
	}).Error)
	validCode, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_twofa_verify_update BEFORE UPDATE ON two_fas
		BEGIN SELECT RAISE(FAIL, 'forced verify state failure'); END;
	`).Error)

	err = VerifyTwoFA(u.Id, validCode)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTwoFAInvalidCode), "a valid factor must not authenticate when its state write fails")
	err = VerifyTwoFA(u.Id, "invalid")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTwoFAInvalidCode), "a failed attempt must not be reported as recorded when its write fails")

	var factor model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", u.Id).First(&factor).Error)
	assert.Zero(t, factor.FailedAttempts)
	assert.Nil(t, factor.LastUsedAt)
}

func TestTwoFAResetRequiresAuthorizationAndKeepsGuardClosed(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	oldSecret := "JBSWY3DPEHPK3PXP"
	recovery := model.TwoFABackupCode{
		UserId: u.Id, CodeHash: "existing-recovery-code", CreatedAt: time.Now(),
	}
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: oldSecret, IsEnabled: true,
	}).Error)
	require.NoError(t, model.DB.Create(&recovery).Error)

	_, _, err := GenerateTwoFASecret(u.Id, u.Username)
	require.ErrorIs(t, err, ErrTwoFAResetProofRequired)
	assertTwoFAState(t, u.Id, oldSecret, true, 1)

	newSecret, _, err := ResetTwoFASecret(u.Id, u.Username)
	require.NoError(t, err)
	require.NotEqual(t, oldSecret, newSecret)
	assertTwoFAState(t, u.Id, newSecret, true, 1)

	// A proved reset must not temporarily mark the factor disabled. A second
	// unproved reset therefore remains denied even immediately afterward.
	_, _, err = GenerateTwoFASecret(u.Id, u.Username)
	require.ErrorIs(t, err, ErrTwoFAResetProofRequired)
	assertTwoFAState(t, u.Id, newSecret, true, 1)
}

func TestTwoFAResetFailureRollsBackExistingState(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	oldSecret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: oldSecret, IsEnabled: true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.TwoFABackupCode{
		UserId: u.Id, CodeHash: "existing-recovery-code", CreatedAt: time.Now(),
	}).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_twofa_update BEFORE UPDATE ON two_fas
		BEGIN SELECT RAISE(FAIL, 'forced twofa update failure'); END;
	`).Error)

	_, _, err := ResetTwoFASecret(u.Id, u.Username)
	require.Error(t, err)
	assertTwoFAState(t, u.Id, oldSecret, true, 1)
}

func TestRegenerateBackupCodesFailureRollsBackDeletion(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.TwoFABackupCode{
		UserId: u.Id, CodeHash: "existing-recovery-code", CreatedAt: time.Now(),
	}).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_backup_insert BEFORE INSERT ON two_fa_backup_codes
		BEGIN SELECT RAISE(FAIL, 'forced backup insert failure'); END;
	`).Error)

	_, err := RegenerateBackupCodes(u.Id)
	require.Error(t, err)
	assertTwoFAState(t, u.Id, "JBSWY3DPEHPK3PXP", true, 1)
}

func TestDisableTwoFAWithCodePreservesStateOnDenial(t *testing.T) {
	initTwoFADB(t)
	u := testutil.NewUser(t, 100)
	secret := "JBSWY3DPEHPK3PXP"
	recoveryCode := "12345678"
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: u.Id, Secret: secret, IsEnabled: true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.TwoFABackupCode{
		UserId: u.Id, CodeHash: cryptoutil.SHA256Hex(recoveryCode), CreatedAt: time.Now(),
	}).Error)

	require.ErrorIs(t, DisableTwoFAWithCode(u.Id, "wrong"), ErrTwoFAInvalidCode)
	assertTwoFAState(t, u.Id, secret, true, 1)

	// A current recovery code is an established-factor verification and may
	// disable 2FA; the factor and its codes change atomically.
	require.NoError(t, DisableTwoFAWithCode(u.Id, recoveryCode))
	assertTwoFAState(t, u.Id, secret, false, 0)
}

func assertTwoFAState(t *testing.T, userId int, secret string, enabled bool, backupCount int64) {
	t.Helper()
	var twoFA model.TwoFA
	require.NoError(t, model.DB.Where("user_id = ?", userId).First(&twoFA).Error)
	assert.Equal(t, secret, twoFA.Secret)
	assert.Equal(t, enabled, twoFA.IsEnabled)
	var count int64
	require.NoError(t, model.DB.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", userId).Count(&count).Error)
	assert.Equal(t, backupCount, count)
}
