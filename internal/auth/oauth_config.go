package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"os"
	"strconv"
	"strings"
)

// GetOAuthConfig builds the configuration for a named provider from
// environment/settings. Supported: github, discord, oidc, linuxdo, plus
// DB-configured custom providers resolved by slug.
func GetOAuthConfig(provider string) *OAuthConfig {
	if !validOAuthProviderSlug(provider) {
		return &OAuthConfig{}
	}
	// The standard OAuth providers (non-standard Telegram/WeChat flows have
	// their own endpoints and are not state-ceremony providers).
	switch provider {
	case "github", "discord", "oidc", "linuxdo":
	default:
		if custom := resolveCustomOAuthConfig(provider); custom != nil {
			if custom.Enabled && validateOAuthRuntimeConfig(custom) != nil {
				custom.Enabled = false
			}
			return custom
		}
		return &OAuthConfig{}
	}

	var cfg *OAuthConfig
	if hasOAuthEnvironmentOverride(provider) {
		cfg = environmentOAuthConfig(provider)
	} else {
		cfg = optionOAuthConfig(provider)
	}
	cfg.Known = true
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = defaultScopes(provider)
	}
	// A provider is publicly advertised only when the complete browser and
	// server exchange can run. In particular, OIDC has no safe default
	// endpoints; credentials alone must not create a dead sign-in button.
	cfg.Enabled = cfg.Enabled && cfg.ClientID != "" && cfg.ClientSecret != "" &&
		strings.TrimSpace(cfg.AuthURL) != "" && strings.TrimSpace(cfg.TokenURL) != "" &&
		strings.TrimSpace(cfg.UserInfoURL) != "" && validateOAuthRuntimeConfig(cfg) == nil
	return cfg
}

func oauthEnvironmentKeys(provider string) []string {
	prefix := strings.ToUpper(provider)
	keys := []string{
		prefix + "_CLIENT_ID", prefix + "_CLIENT_SECRET", prefix + "_AUTH_URL",
		prefix + "_TOKEN_URL", prefix + "_USER_INFO_URL", prefix + "_SCOPES",
	}
	if provider == "linuxdo" {
		keys = append(keys, "LINUX_DO_TOKEN_ENDPOINT", "LINUX_DO_USER_ENDPOINT",
			"LINUXDO_MINIMUM_TRUST_LEVEL", "LINUX_DO_MINIMUM_TRUST_LEVEL")
	}
	if provider == "oidc" {
		keys = append(keys, "OIDC_DISPLAY_NAME")
	}
	return keys
}

func hasOAuthEnvironmentOverride(provider string) bool {
	for _, key := range oauthEnvironmentKeys(provider) {
		if _, present := os.LookupEnv(key); present {
			return true
		}
	}
	return false
}

func environmentOAuthConfig(provider string) *OAuthConfig {
	prefix := strings.ToUpper(provider)
	cfg := &OAuthConfig{
		ClientID:     env.GetEnv(prefix+"_CLIENT_ID", ""),
		ClientSecret: env.GetEnv(prefix+"_CLIENT_SECRET", ""),
		AuthURL:      env.GetEnv(prefix+"_AUTH_URL", defaultAuthURL(provider)),
		TokenURL:     env.GetEnv(prefix+"_TOKEN_URL", defaultTokenURL(provider)),
		UserInfoURL:  env.GetEnv(prefix+"_USER_INFO_URL", defaultUserInfoURL(provider)),
		Scopes:       env.GetEnvStrings(prefix + "_SCOPES"),
		Enabled:      true,
	}
	if provider == "oidc" {
		cfg.DisplayName = env.GetEnv("OIDC_DISPLAY_NAME", "OIDC")
	}
	if provider == "linuxdo" {
		cfg.MinimumTrustLevel = environmentLinuxDOTrustLevel()
	}
	return cfg
}

func environmentLinuxDOTrustLevel() int {
	raw := ""
	if value, present := os.LookupEnv("LINUXDO_MINIMUM_TRUST_LEVEL"); present {
		raw = value
	} else if value, present := os.LookupEnv("LINUX_DO_MINIMUM_TRUST_LEVEL"); present {
		raw = value
	}
	if raw == "" {
		return 0
	}
	level, err := strconv.Atoi(raw)
	if err != nil || level < 0 || level > 4 || strconv.Itoa(level) != raw {
		return -1
	}
	return level
}

func optionOAuthConfig(provider string) *OAuthConfig {
	configured, known := setting.OAuthProviderSetting(provider)
	if !known {
		return &OAuthConfig{}
	}
	cfg := &OAuthConfig{
		ClientID:          configured.ClientID,
		ClientSecret:      configured.ClientSecret,
		AuthURL:           defaultAuthURL(provider),
		TokenURL:          defaultTokenURL(provider),
		UserInfoURL:       defaultUserInfoURL(provider),
		DisplayName:       configured.DisplayName,
		MinimumTrustLevel: configured.MinimumTrustLevel,
		Scopes:            defaultScopes(provider),
		Enabled:           configured.Enabled,
	}
	if provider == "oidc" {
		cfg.AuthURL = configured.AuthorizationEndpoint
		cfg.TokenURL = configured.TokenEndpoint
		cfg.UserInfoURL = configured.UserInfoEndpoint
		if cfg.DisplayName == "" {
			cfg.DisplayName = "OIDC"
		}
	}
	if !configured.Enabled {
		return cfg
	}
	return cfg
}

func defaultAuthURL(p string) string {
	switch p {
	case "github":
		return "https://github.com/login/oauth/authorize"
	case "discord":
		return "https://discord.com/oauth2/authorize"
	case "linuxdo":
		return "https://connect.linux.do/oauth2/authorize"
	default: // oidc
		return ""
	}
}

func defaultTokenURL(p string) string {
	switch p {
	case "github":
		return "https://github.com/login/oauth/access_token"
	case "discord":
		return "https://discord.com/api/oauth2/token"
	case "linuxdo":
		return env.GetEnv("LINUX_DO_TOKEN_ENDPOINT", "https://connect.linux.do/oauth2/token")
	default:
		return ""
	}
}

func defaultUserInfoURL(p string) string {
	switch p {
	case "github":
		return "https://api.github.com/user"
	case "discord":
		return "https://discord.com/api/users/@me"
	case "linuxdo":
		return env.GetEnv("LINUX_DO_USER_ENDPOINT", "https://connect.linux.do/api/user")
	default:
		return ""
	}
}

func defaultScopes(p string) []string {
	switch p {
	case "github":
		return []string{"read:user", "user:email"}
	case "discord":
		return []string{"identify", "email"}
	default:
		return []string{"openid", "email", "profile"}
	}
}

// BuildAuthorizationURL returns the provider's authorization URL for a state.
func BuildAuthorizationURL(cfg *OAuthConfig, state, redirectURI string) (string, error) {
	if cfg == nil || cfg.AuthURL == "" {
		return "", errors.New("provider authorization endpoint not configured")
	}
	if err := validateOAuthCredentials(cfg.ClientID, cfg.ClientSecret, false); err != nil {
		return "", errors.New("provider authorization configuration is invalid")
	}
	if !validOAuthText(state, maxOAuthStateBytes, false) || strings.TrimSpace(state) != state {
		return "", errors.New("OAuth state is invalid")
	}
	if _, err := validateOAuthURL(redirectURI, maxOAuthRedirectURIBytes, false); err != nil {
		return "", errors.New("OAuth redirect URI is invalid")
	}
	if err := validateOAuthScopes(cfg.Scopes); err != nil {
		return "", err
	}
	u, err := validateOAuthURL(cfg.AuthURL, maxOAuthEndpointBytes, true)
	if err != nil {
		return "", errors.New("provider authorization endpoint is invalid")
	}
	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	validated, err := validateOAuthURL(u.String(), maxOAuthAuthorizationURLBytes, true)
	if err != nil {
		return "", errors.New("provider authorization URL exceeds safe limits")
	}
	return validated.String(), nil
}
