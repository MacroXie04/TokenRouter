package auth

import (
	"errors"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
	"time"
)

func serviceTwoFALoginFixture(t *testing.T, username string) (*model.User, string, string) {
	t.Helper()
	initAuthDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TwoFA{}, &model.TwoFABackupCode{}))
	user := seedPasswordUser(t, username, "password123")
	secret, _, err := GenerateTwoFASecret(user.Id, user.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	_, err = EnableTwoFA(user.Id, code)
	require.NoError(t, err)
	flowToken, err := BeginTwoFALogin(user)
	require.NoError(t, err)
	return user, flowToken, secret
}

func TestLogin2FAKeepsAuthFlowStorageFailuresInternal(t *testing.T) {
	_, flowToken, _ := serviceTwoFALoginFixture(t, "twofa-flow-storage")
	require.NoError(t, model.DB.Migrator().DropTable(&model.AuthFlow{}))

	_, _, _, _, err := Login2FA(flowToken, "invalid", "127.0.0.1", "test")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrInvalidFlowToken), "storage failure must not be downgraded to an invalid proof")
}

func TestLogin2FAKeepsBackupCodeStorageFailuresInternal(t *testing.T) {
	_, flowToken, _ := serviceTwoFALoginFixture(t, "twofa-backup-storage")
	require.NoError(t, model.DB.Migrator().DropTable(&model.TwoFABackupCode{}))

	_, _, _, _, err := Login2FA(flowToken, "invalid", "127.0.0.1", "test")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTwoFAInvalidCode), "storage failure must not be downgraded to a bad factor")
}

func TestLogin2FAKeepsFlowConsumptionFailuresInternal(t *testing.T) {
	_, flowToken, secret := serviceTwoFALoginFixture(t, "twofa-consume-storage")
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_login_flow_consume
		BEFORE UPDATE ON auth_flows
		BEGIN SELECT RAISE(ABORT, 'forced flow consume failure'); END
	`).Error)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	_, _, _, _, err = Login2FA(flowToken, code, "127.0.0.1", "test")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrInvalidFlowToken), "storage failure must not be downgraded to an invalid proof")
}
