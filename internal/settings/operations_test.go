package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func TestOperationsSettingsValidateAndPublishAtomically(t *testing.T) {
	setupAffinitySettingTest(t)

	defaults := GetOperationsSetting()
	assert.Equal(t, defaultSMTPPort, defaults.SMTP.Port)
	assert.Empty(t, defaults.SMTP.Server)
	assert.False(t, defaults.DefaultCollapseSidebar)

	require.NoError(t, UpdateOptions(map[string]string{
		SMTPServerOption:             "smtp.example.com",
		SMTPPortOption:               "587",
		SMTPAccountOption:            "mailer@example.com",
		SMTPFromOption:               "no-reply@example.com",
		SMTPTokenOption:              " secret with spaces ",
		SMTPSSLEnabledOption:         "false",
		SMTPStartTLSEnabledOption:    "true",
		SMTPInsecureSkipVerifyOption: "false",
		SMTPForceAuthLoginOption:     "true",
		DefaultCollapseSidebarOption: "true",
	}))

	configured := GetOperationsSetting()
	assert.Equal(t, "smtp.example.com", configured.SMTP.Server)
	assert.Equal(t, " secret with spaces ", configured.SMTP.Token)
	assert.True(t, configured.SMTP.StartTLSEnabled)
	assert.True(t, configured.SMTP.ForceAuthLogin)
	assert.True(t, configured.DefaultCollapseSidebar)

	before := GetOperationsSetting()
	for _, update := range []map[string]string{
		{SMTPPortOption: "0"},
		{SMTPPortOption: "0587"},
		{SMTPServerOption: "smtp.example.com:587"},
		{SMTPFromOption: "Mailer <mailer@example.com>"},
		{SMTPTokenOption: "secret\nvalue"},
		{SMTPSSLEnabledOption: "true"},
		{SMTPStartTLSEnabledOption: "false"},
		{SMTPInsecureSkipVerifyOption: "sometimes"},
		{SMTPTokenOption: ""},
		{DefaultCollapseSidebarOption: "1"},
	} {
		require.Error(t, UpdateOptions(update), update)
		assert.Equal(t, before, GetOperationsSetting(), "failed update must retain the full operations snapshot")
		for key := range update {
			assert.Equal(t, map[string]string{
				SMTPPortOption:               "587",
				SMTPServerOption:             "smtp.example.com",
				SMTPFromOption:               "no-reply@example.com",
				SMTPTokenOption:              " secret with spaces ",
				SMTPSSLEnabledOption:         "false",
				SMTPStartTLSEnabledOption:    "true",
				SMTPInsecureSkipVerifyOption: "false",
				DefaultCollapseSidebarOption: "true",
			}[key], GetOption(key))
		}
	}
}

func TestSMTPSettingAllowsSafeStagingButRequiresCompleteRunnableConfiguration(t *testing.T) {
	staged, err := ParseSMTPSetting(map[string]string{
		SMTPAccountOption: "mailer@example.com",
		SMTPTokenOption:   "secret",
	})
	require.NoError(t, err)
	assert.ErrorIs(t, staged.ValidateRunnable(), ErrSMTPNotConfigured)

	_, err = ParseSMTPSetting(map[string]string{
		SMTPServerOption:  "smtp.example.com",
		SMTPFromOption:    "no-reply@example.com",
		SMTPAccountOption: "mailer@example.com",
		SMTPTokenOption:   "secret",
	})
	assert.ErrorContains(t, err, "requires SSL/TLS or STARTTLS")

	_, err = ParseSMTPSetting(map[string]string{
		SMTPServerOption:     "smtp.example.com",
		SMTPFromOption:       "no-reply@example.com",
		SMTPAccountOption:    "mailer@example.com",
		SMTPTokenOption:      "",
		SMTPSSLEnabledOption: "true",
	})
	assert.ErrorIs(t, err, ErrSMTPIncompleteCredentials)

	_, err = ParseSMTPSetting(map[string]string{
		SMTPServerOption: "smtp.example.com",
		SMTPFromOption:   "no-reply@example.com",
	})
	assert.ErrorContains(t, err, "non-loopback server requires SSL/TLS or STARTTLS")

	plainLoopback, err := ParseSMTPSetting(map[string]string{
		SMTPServerOption: "127.0.0.1",
		SMTPFromOption:   "no-reply@example.com",
	})
	require.NoError(t, err, "a loopback-only development server may explicitly use plaintext")
	assert.NoError(t, plainLoopback.ValidateRunnable())

	_, err = ParseSMTPSetting(map[string]string{
		SMTPServerOption: "mail.localhost",
		SMTPFromOption:   "no-reply@example.com",
	})
	assert.ErrorContains(t, err, "non-loopback server requires SSL/TLS or STARTTLS")

	implicitTLS, err := ParseSMTPSetting(map[string]string{
		SMTPServerOption:  "smtp.example.com",
		SMTPPortOption:    "465",
		SMTPAccountOption: "mailer@example.com",
		SMTPTokenOption:   "secret",
	})
	require.NoError(t, err)
	assert.True(t, implicitTLS.UsesTLS())
}

func TestOperationsRemoteSyncRetainsLastValidSnapshot(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		SMTPServerOption:             "smtp.example.com",
		SMTPFromOption:               "no-reply@example.com",
		SMTPStartTLSEnabledOption:    "true",
		DefaultCollapseSidebarOption: "true",
	}))

	require.NoError(t, model.DB.Create(&model.Option{Key: SMTPSSLEnabledOption, Value: "true"}).Error)
	require.Error(t, Sync())

	operations := GetOperationsSetting()
	assert.Equal(t, "smtp.example.com", operations.SMTP.Server)
	assert.True(t, operations.SMTP.StartTLSEnabled)
	assert.False(t, operations.SMTP.SSLEnabled)
	assert.True(t, operations.DefaultCollapseSidebar)
	assert.Empty(t, GetOption(SMTPSSLEnabledOption), "invalid remote state must not replace the raw option cache")
}

func TestOperationsOptionDefaultsAreComplete(t *testing.T) {
	defaults := OperationsOptionDefaults()
	assert.Equal(t, "587", defaults[SMTPPortOption])
	assert.Equal(t, "false", defaults[SMTPSSLEnabledOption])
	assert.Equal(t, "false", defaults[SMTPStartTLSEnabledOption])
	assert.Equal(t, "false", defaults[DefaultCollapseSidebarOption])
	assert.Len(t, defaults, 10)
}
