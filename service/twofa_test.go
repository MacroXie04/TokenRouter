package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func initTwoFADB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}, &model.TwoFABackupCode{}))
	model.DB = db
	model.LOG_DB = db
}

func TestTwoFAEnableAndVerify(t *testing.T) {
	initTwoFADB(t)
	u := newUser(t, 100)

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

func TestTwoFABackupCodeSingleUse(t *testing.T) {
	initTwoFADB(t)
	u := newUser(t, 100)
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
	u := newUser(t, 100)
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
