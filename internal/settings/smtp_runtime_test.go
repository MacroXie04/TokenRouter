package settings

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
)

func clearSMTPEnvironment(t *testing.T) {
	t.Helper()
	seen := make(map[string]struct{})
	for _, aliases := range smtpEnvironmentAliases {
		for _, key := range aliases {
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			value, present := os.LookupEnv(key)
			require.NoError(t, os.Unsetenv(key))
			t.Cleanup(func() {
				if present {
					_ = os.Setenv(key, value)
				} else {
					_ = os.Unsetenv(key)
				}
			})
		}
	}
}

func setupSMTPSettingTest(t *testing.T) {
	t.Helper()
	clearSMTPEnvironment(t)
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "smtp.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Option{}))
	previous := model.DB
	model.DB = database
	require.NoError(t, Init())
	t.Cleanup(func() {
		_ = UpdateOptions(OperationsOptionDefaults())
		model.DB = previous
	})
}

func TestEffectiveSMTPSettingUsesWholeEnvironmentDomain(t *testing.T) {
	setupSMTPSettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		SMTPServerOption:     "option.example.com",
		SMTPPortOption:       "465",
		SMTPAccountOption:    "option-user",
		SMTPFromOption:       "option@example.com",
		SMTPTokenOption:      "option-secret",
		SMTPSSLEnabledOption: "true",
	}))

	configured, enabled, err := EffectiveSMTPSetting()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "option.example.com", configured.Server)
	assert.Equal(t, "option-secret", configured.Token)

	require.NoError(t, UpdateOption(SMTPTokenOption, "rotated-option-secret"))
	configured, enabled, err = EffectiveSMTPSetting()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "rotated-option-secret", configured.Token, "a new delivery sees the latest complete option snapshot")

	t.Setenv("SMTP_HOST", "env.example.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_USER", "env-user")
	t.Setenv("SMTP_PASSWORD", "env-secret")
	t.Setenv("SMTP_FROM", "env@example.com")
	configured, enabled, err = EffectiveSMTPSetting()
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "env.example.com", configured.Server)
	assert.Equal(t, "env-secret", configured.Token)
	assert.False(t, configured.SSLEnabled, "absent environment fields use environment defaults, never option values")

	t.Setenv("SMTP_PASSWORD", "")
	_, enabled, err = EffectiveSMTPSetting()
	assert.False(t, enabled)
	assert.ErrorIs(t, err, ErrSMTPIncompleteCredentials)
	assert.NotContains(t, err.Error(), "option-secret")

	t.Setenv("SMTP_SERVER", "conflict.example.com")
	_, enabled, err = EffectiveSMTPSetting()
	assert.False(t, enabled)
	assert.ErrorContains(t, err, "conflicting SMTP environment aliases")
}
