package settings

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
	"testing"
)

func useProductionAuthURLPolicy(t *testing.T) {
	t.Helper()
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	httpx.InitSSRF()
}

func TestAuthenticationSettingsValidateAndPublishAtomically(t *testing.T) {
	useProductionAuthURLPolicy(t)
	setupAffinitySettingTest(t)

	defaults := GetAuthenticationSetting()
	assert.False(t, defaults.EmailDomainRestrictionEnabled)
	assert.Equal(t, defaultEmailDomainWhitelist, defaults.EmailDomainWhitelist)
	assert.Equal(t, "preferred", defaults.Passkey.UserVerification)
	assert.Empty(t, defaults.Passkey.Origins)

	require.NoError(t, UpdateOptions(map[string]string{
		EmailDomainRestrictionEnabledOption: "true",
		EmailAliasRestrictionEnabledOption:  "true",
		EmailDomainWhitelistOption:          "Example.COM\nexample.org,example.com",
		GitHubClientIDOption:                "github-client",
		GitHubClientSecretOption:            "github-secret",
		GitHubOAuthEnabledOption:            "true",
		DiscordClientIDOption:               "discord-client",
		DiscordClientSecretOption:           "discord-secret",
		DiscordOptionEnabled:                "true",
		LinuxDOClientIDOption:               "linuxdo-client",
		LinuxDOClientSecretOption:           "linuxdo-secret",
		LinuxDOOAuthEnabledOption:           "true",
		LinuxDOMinimumTrustLevelOption:      "2",
		OIDCClientIDOption:                  "oidc-client",
		OIDCClientSecretOption:              "oidc-secret",
		OIDCDisplayNameOption:               "Company SSO",
		OIDCWellKnownOption:                 "https://identity.example/.well-known/openid-configuration",
		OIDCAuthorizationEndpointOption:     "https://identity.example/authorize",
		OIDCTokenEndpointOption:             "https://identity.example/token",
		OIDCUserInfoEndpointOption:          "https://identity.example/userinfo",
		OIDCOAuthEnabledOption:              "true",
		PasskeyEnabledOption:                "true",
		PasskeyRPDisplayNameOption:          "Example Passkeys",
		PasskeyRPIDOption:                   "example.com",
		PasskeyOriginsOption:                "https://EXAMPLE.com\nhttps://login.example.com:8443",
		PasskeyUserVerificationOption:       "required",
		PasskeyAttachmentPreferenceOption:   "platform",
	}))

	configured := GetAuthenticationSetting()
	assert.Equal(t, []string{"example.com", "example.org"}, configured.EmailDomainWhitelist)
	assert.True(t, configured.GitHub.Enabled)
	assert.True(t, configured.Discord.Enabled)
	assert.Equal(t, 2, configured.LinuxDO.MinimumTrustLevel)
	assert.Equal(t, "Company SSO", configured.OIDC.DisplayName)
	assert.Equal(t, []string{"https://example.com", "https://login.example.com:8443"}, configured.Passkey.Origins)
	assert.Equal(t, "required", configured.Passkey.UserVerification)
	assert.Equal(t, "platform", configured.Passkey.AttachmentPreference)

	configured.EmailDomainWhitelist[0] = "mutated.invalid"
	configured.Passkey.Origins[0] = "https://mutated.invalid"
	fresh := GetAuthenticationSetting()
	assert.Equal(t, "example.com", fresh.EmailDomainWhitelist[0])
	assert.Equal(t, "https://example.com", fresh.Passkey.Origins[0])

	before := GetAuthenticationSetting()
	invalidUpdates := []map[string]string{
		{EmailDomainRestrictionEnabledOption: "sometimes"},
		{EmailDomainWhitelistOption: "good.example,127.0.0.1"},
		{EmailDomainWhitelistOption: strings.Repeat("x", maxEmailDomainListBytes+1)},
		{GitHubClientIDOption: "bad\nclient"},
		{DiscordClientSecretOption: "bad\nsecret"},
		{LinuxDOMinimumTrustLevelOption: "5"},
		{OIDCTokenEndpointOption: "http://identity.example/token"},
		{OIDCUserInfoEndpointOption: "https://127.0.0.1/userinfo"},
		{OIDCAuthorizationEndpointOption: "https://identity.example/a/../authorize"},
		{PasskeyRPIDOption: "https://example.com"},
		{PasskeyOriginsOption: "https://example.com/path"},
		{PasskeyOriginsOption: "http://example.com"},
		{PasskeyUserVerificationOption: "optional"},
		{PasskeyAttachmentPreferenceOption: "usb"},
	}
	for _, update := range invalidUpdates {
		require.Error(t, UpdateOptions(update), update)
		assert.Equal(t, before, GetAuthenticationSetting(), "failed update must retain every auth field")
	}

	// Incomplete option sets remain persistable so operators can configure one
	// write-only field at a time and so a complete deployment environment can
	// override them. Runtime/status still fail closed until the selected source
	// is complete.
	require.NoError(t, UpdateOptions(map[string]string{
		GitHubClientIDOption:     "",
		GitHubClientSecretOption: "",
		GitHubOAuthEnabledOption: "true",
		OIDCTokenEndpointOption:  "",
	}))
	incomplete := GetAuthenticationSetting()
	assert.True(t, incomplete.GitHub.Enabled)
	assert.Empty(t, incomplete.GitHub.ClientID)
	assert.True(t, incomplete.OIDC.Enabled)
	assert.Empty(t, incomplete.OIDC.TokenEndpoint)
}

func TestEmailRegistrationPolicyUsesCanonicalExactDomainsAndAliasRules(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		EmailDomainRestrictionEnabledOption: "true",
		EmailAliasRestrictionEnabledOption:  "true",
		EmailDomainWhitelistOption:          "example.com",
	}))

	assert.NoError(t, ValidateEmailRegistrationPolicy("person@example.com"))
	assert.ErrorIs(t, ValidateEmailRegistrationPolicy("person@sub.example.com"), ErrEmailDomainNotAllowed)
	assert.ErrorIs(t, ValidateEmailRegistrationPolicy("person+tag@example.com"), ErrEmailAliasNotAllowed)
	assert.ErrorIs(t, ValidateEmailRegistrationPolicy("first.last@example.com"), ErrEmailAliasNotAllowed)
	assert.Error(t, ValidateEmailRegistrationPolicy("not-an-address"))
}

func TestAuthenticationRemoteSyncRetainsLastValidSnapshot(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		EmailDomainRestrictionEnabledOption: "true",
		EmailDomainWhitelistOption:          "example.com",
	}))

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", EmailDomainWhitelistOption).
		Update("value", "127.0.0.1").Error)
	require.Error(t, Sync())

	auth := GetAuthenticationSetting()
	assert.Equal(t, []string{"example.com"}, auth.EmailDomainWhitelist)
	assert.Equal(t, "example.com", GetOption(EmailDomainWhitelistOption))
}
