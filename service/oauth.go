package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// OAuthConfig holds the OAuth2 code-flow configuration for a provider.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	DisplayName  string
	// MinimumTrustLevel is enforced for Linux DO user-info responses.
	MinimumTrustLevel int
	Scopes            []string
	Enabled           bool
	Known             bool
	// Custom is set when the provider is a DB-configured custom provider
	// (resolved by slug); nil for the built-in env-configured providers.
	Custom *model.CustomOAuthProvider
}

// OAuthToken is the outcome of the code exchange. TokenType matters for
// custom providers whose userinfo endpoint expects a non-Bearer scheme.
type OAuthToken struct {
	AccessToken string
	TokenType   string
}

// ProviderUser is the normalized identity returned by a provider's userinfo.
type ProviderUser struct {
	ProviderID       string
	LegacyProviderID string
	Username         string
	Email            string
	DisplayName      string
	// CustomProviderId is the CustomOAuthProvider row id when the identity
	// came from a custom provider; 0 for built-ins. It decides whether the
	// binding lives in a users column or in user_oauth_bindings.
	CustomProviderId int
}

// ErrRegistrationDisabled is returned only when an external identity has no
// existing owner and global registration is disabled. The check happens in
// the same database transaction as account creation and identity claiming.
var ErrRegistrationDisabled = errors.New("registration is disabled")

// ErrLinuxDOTrustLevel is returned before identity persistence when the
// provider's asserted trust level is absent or below the configured minimum.
var ErrLinuxDOTrustLevel = errors.New("Linux DO trust level is below the configured minimum")

const (
	maxOAuthClientIDBytes           = 256
	maxOAuthClientSecretBytes       = 4096
	maxOAuthEndpointBytes           = 2048
	maxOAuthAuthorizationURLBytes   = 16 << 10
	maxOAuthRedirectURIBytes        = 2048
	maxOAuthAuthorizationCodeBytes  = 4096
	maxOAuthStateBytes              = 128
	maxOAuthAccessTokenBytes        = 16 << 10
	maxOAuthTokenTypeBytes          = 32
	maxOAuthScopeCount              = 32
	maxOAuthScopeBytes              = 128
	maxOAuthEndpointQueryPairs      = 16
	maxOAuthEndpointQueryKeyBytes   = 128
	maxOAuthEndpointQueryValueBytes = 2048
	maxOAuthBuiltInSubjectBytes     = 64
	maxOAuthCustomSubjectBytes      = 256
	maxOAuthUsernameBytes           = 20
	maxOAuthDisplayNameBytes        = 20
	maxOAuthProviderSlugBytes       = 64
	maxOAuthJSONDepth               = 16
	maxOAuthJSONNodes               = 4096
	maxOAuthJSONObjectItems         = 256
	maxOAuthJSONArrayItems          = 256
	maxOAuthJSONKeyBytes            = 128
	maxOAuthJSONStringBytes         = 16 << 10
	maxOAuthJSONNumberBytes         = 128
)

var oauthTokenTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]*$`)

func validOAuthText(value string, maximumBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func validOAuthProviderSlug(provider string) bool {
	return provider != "" && len(provider) <= maxOAuthProviderSlugBytes &&
		provider == strings.TrimSpace(provider) && customSlugPattern.MatchString(provider)
}

func oauthDevelopmentURLAllowed() bool {
	// SSRF_DISABLE is the process-wide, explicit local-development escape
	// hatch. Reuse it for plaintext loopback OAuth endpoints so DEBUG logging
	// alone can never downgrade credential transport.
	return common.SSRFDisabled()
}

func validateOAuthURL(raw string, maximumBytes int, allowQuery bool) (*url.URL, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || !validOAuthText(raw, maximumBytes, false) || strings.Contains(raw, `\`) {
		return nil, errors.New("OAuth URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery ||
		!validOAuthText(parsed.Path, maximumBytes, true) || strings.Contains(parsed.Path, `\`) {
		return nil, errors.New("OAuth URL is invalid")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("OAuth URL path is ambiguous")
		}
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("OAuth URL port is invalid")
		}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return nil, errors.New("OAuth URL scheme is not supported")
	}
	if !allowQuery && (parsed.RawQuery != "" || parsed.ForceQuery) {
		return nil, errors.New("OAuth URL query is not allowed")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, errors.New("OAuth URL query is invalid")
	}
	pairs := 0
	for key, values := range query {
		pairs += len(values)
		if key == "" || len(values) != 1 || !validOAuthText(key, maxOAuthEndpointQueryKeyBytes, false) ||
			!validOAuthText(values[0], maxOAuthEndpointQueryValueBytes, true) {
			return nil, errors.New("OAuth URL query is unsafe or ambiguous")
		}
	}
	if pairs > maxOAuthEndpointQueryPairs {
		return nil, errors.New("OAuth URL has too many query parameters")
	}

	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(hostname)
	loopback := hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") ||
		address != nil && address.IsLoopback()
	if !oauthDevelopmentURLAllowed() && (loopback || address != nil && common.IsUnsafeIP(address)) {
		return nil, errors.New("OAuth URL targets an unsafe address")
	}
	if scheme == "http" && !(oauthDevelopmentURLAllowed() && loopback) {
		return nil, errors.New("OAuth URL must use HTTPS")
	}
	return parsed, nil
}

func validateOAuthScopes(scopes []string) error {
	if len(scopes) > maxOAuthScopeCount {
		return errors.New("OAuth scope list exceeds safe limits")
	}
	totalBytes := 0
	for _, scope := range scopes {
		if scope == "" || scope != strings.TrimSpace(scope) || !validOAuthText(scope, maxOAuthScopeBytes, false) {
			return errors.New("OAuth scope list exceeds safe limits")
		}
		for _, character := range scope {
			if character < 0x21 || character > 0x7e || character == '"' || character == '\\' {
				return errors.New("OAuth scope list exceeds safe limits")
			}
		}
		totalBytes += len(scope)
	}
	if len(scopes) > 1 {
		totalBytes += len(scopes) - 1
	}
	if totalBytes > maxOAuthEndpointQueryValueBytes {
		return errors.New("OAuth scope list exceeds safe limits")
	}
	return nil
}

func validateOAuthCredentials(clientID, clientSecret string, requireSecret bool) error {
	if clientID != strings.TrimSpace(clientID) || !validOAuthText(clientID, maxOAuthClientIDBytes, false) {
		return errors.New("OAuth client ID is invalid")
	}
	if requireSecret && !validOAuthText(clientSecret, maxOAuthClientSecretBytes, false) {
		return errors.New("OAuth client secret is invalid")
	}
	if !requireSecret && !validOAuthText(clientSecret, maxOAuthClientSecretBytes, true) {
		return errors.New("OAuth client secret is invalid")
	}
	return nil
}

func validateOAuthRuntimeConfig(cfg *OAuthConfig) error {
	if cfg == nil {
		return errors.New("OAuth provider is not configured")
	}
	if err := validateOAuthCredentials(cfg.ClientID, cfg.ClientSecret, true); err != nil {
		return err
	}
	if _, err := validateOAuthURL(cfg.AuthURL, maxOAuthEndpointBytes, true); err != nil {
		return errors.New("OAuth authorization endpoint is invalid")
	}
	if _, err := validateOAuthURL(cfg.TokenURL, maxOAuthEndpointBytes, true); err != nil {
		return errors.New("OAuth token endpoint is invalid")
	}
	if _, err := validateOAuthURL(cfg.UserInfoURL, maxOAuthEndpointBytes, true); err != nil {
		return errors.New("OAuth userinfo endpoint is invalid")
	}
	if cfg.MinimumTrustLevel < 0 || cfg.MinimumTrustLevel > 4 {
		return errors.New("OAuth minimum trust level is invalid")
	}
	return validateOAuthScopes(cfg.Scopes)
}

// BuildOAuthRedirectURI validates the configured public frontend base and
// appends the provider callback path without allowing query/fragment
// confusion or a plaintext production callback.
func BuildOAuthRedirectURI(baseAddress, provider string) (string, error) {
	if !validOAuthProviderSlug(provider) {
		return "", errors.New("OAuth provider is invalid")
	}
	base, err := validateOAuthURL(baseAddress, maxOAuthRedirectURIBytes, false)
	if err != nil {
		return "", errors.New("OAuth redirect base is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/oauth/" + provider + "/callback"
	base.RawPath = ""
	return base.String(), nil
}

// BuildOAuthDiscoveryURL validates the single operator-supplied discovery
// destination. An issuer is converted to its standard well-known document;
// providing both inputs is rejected rather than choosing one implicitly.
func BuildOAuthDiscoveryURL(wellKnownAddress, issuerAddress string) (string, error) {
	wellKnownAddress = strings.TrimSpace(wellKnownAddress)
	issuerAddress = strings.TrimSpace(issuerAddress)
	if (wellKnownAddress == "") == (issuerAddress == "") {
		return "", errors.New("exactly one OAuth discovery address is required")
	}
	if wellKnownAddress != "" {
		parsed, err := validateOAuthURL(wellKnownAddress, maxOAuthEndpointBytes, true)
		if err != nil {
			return "", errors.New("OAuth discovery URL is invalid")
		}
		return parsed.String(), nil
	}
	issuer, err := validateOAuthURL(issuerAddress, maxOAuthEndpointBytes, false)
	if err != nil {
		return "", errors.New("OAuth issuer URL is invalid")
	}
	issuer.Path = strings.TrimRight(issuer.Path, "/") + "/.well-known/openid-configuration"
	issuer.RawPath = ""
	return issuer.String(), nil
}

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
		ClientID:     common.GetEnv(prefix+"_CLIENT_ID", ""),
		ClientSecret: common.GetEnv(prefix+"_CLIENT_SECRET", ""),
		AuthURL:      common.GetEnv(prefix+"_AUTH_URL", defaultAuthURL(provider)),
		TokenURL:     common.GetEnv(prefix+"_TOKEN_URL", defaultTokenURL(provider)),
		UserInfoURL:  common.GetEnv(prefix+"_USER_INFO_URL", defaultUserInfoURL(provider)),
		Scopes:       common.GetEnvStrings(prefix + "_SCOPES"),
		Enabled:      true,
	}
	if provider == "oidc" {
		cfg.DisplayName = common.GetEnv("OIDC_DISPLAY_NAME", "OIDC")
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
		return common.GetEnv("LINUX_DO_TOKEN_ENDPOINT", "https://connect.linux.do/oauth2/token")
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
		return common.GetEnv("LINUX_DO_USER_ENDPOINT", "https://connect.linux.do/api/user")
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

var oauthHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	// Built-in provider endpoints can be overridden for OIDC/self-hosted
	// deployments. Resolve and connect directly through the SSRF guard; an
	// environment proxy must never get a chance to re-resolve the destination.
	Transport: &http.Transport{DialContext: common.SafeDialContext},
	// OAuth token requests contain a client secret and user-info requests carry
	// an access token. Never forward either across an HTTP redirect.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

const maxOAuthResponseBytes = int64(1 << 20)

// ExchangeCode exchanges an authorization code for an access token.
func ExchangeCode(cfg *OAuthConfig, code, redirectURI string) (*OAuthToken, error) {
	if cfg == nil {
		return nil, errors.New("OAuth provider is not configured")
	}
	if err := validateOAuthCredentials(cfg.ClientID, cfg.ClientSecret, true); err != nil {
		return nil, errors.New("OAuth token exchange configuration is invalid")
	}
	if !validOAuthText(code, maxOAuthAuthorizationCodeBytes, false) || code != strings.TrimSpace(code) {
		return nil, errors.New("OAuth authorization code is invalid")
	}
	if _, err := validateOAuthURL(redirectURI, maxOAuthRedirectURIBytes, false); err != nil {
		return nil, errors.New("OAuth redirect URI is invalid")
	}
	if _, err := validateOAuthURL(cfg.TokenURL, maxOAuthEndpointBytes, true); err != nil {
		return nil, errors.New("OAuth token endpoint is invalid")
	}
	if cfg.Custom != nil {
		return customExchangeCode(cfg, code, redirectURI)
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequest(http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, errors.New("OAuth token exchange request failed")
	}
	defer resp.Body.Close()
	body, err := common.ReadAllLimited(resp.Body, maxOAuthResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read token exchange response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("OAuth token exchange was rejected")
	}
	raw, err := decodeBoundedOAuthJSONObject(body)
	if err != nil {
		return nil, errors.New("OAuth token response is invalid")
	}
	accessToken, ok := raw["access_token"].(string)
	if !ok || accessToken == "" {
		return nil, errors.New("token exchange returned no access_token")
	}
	tokenType := ""
	if value, present := raw["token_type"]; present {
		var typeOK bool
		tokenType, typeOK = value.(string)
		if !typeOK {
			return nil, errors.New("OAuth token response is invalid")
		}
	}
	token := &OAuthToken{AccessToken: accessToken, TokenType: tokenType}
	if err := validateOAuthToken(token); err != nil {
		return nil, err
	}
	return token, nil
}

// FetchUserInfo fetches and normalizes the provider identity for an access token.
func FetchUserInfo(cfg *OAuthConfig, provider string, token *OAuthToken) (*ProviderUser, error) {
	if cfg == nil || !validOAuthProviderSlug(provider) {
		return nil, errors.New("OAuth provider is invalid")
	}
	if err := validateOAuthToken(token); err != nil {
		return nil, err
	}
	if _, err := validateOAuthURL(cfg.UserInfoURL, maxOAuthEndpointBytes, true); err != nil {
		return nil, errors.New("OAuth userinfo endpoint is invalid")
	}
	if cfg.Custom != nil {
		pu, err := customFetchUserInfo(cfg, token)
		if err != nil {
			return nil, err
		}
		return normalizeProviderUser(provider, pu)
	}
	req, err := http.NewRequest(http.MethodGet, cfg.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, errors.New("OAuth userinfo request failed")
	}
	defer resp.Body.Close()
	body, err := common.ReadAllLimited(resp.Body, maxOAuthResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read userinfo response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("OAuth userinfo request was rejected")
	}
	raw, err := decodeBoundedOAuthJSONObject(body)
	if err != nil {
		return nil, errors.New("OAuth userinfo response is invalid")
	}
	if provider == model.ExternalIdentityProviderLinuxDO && cfg.MinimumTrustLevel > 0 {
		trustLevel, ok := boundedOAuthInteger(raw["trust_level"], 0, 4)
		if !ok || trustLevel < cfg.MinimumTrustLevel {
			return nil, ErrLinuxDOTrustLevel
		}
	}
	pu := &ProviderUser{
		ProviderID:  firstProviderID(raw),
		Username:    firstString(raw, "preferred_username", "login", "username"),
		Email:       firstString(raw, "email"),
		DisplayName: firstString(raw, "name", "display_name", "login"),
	}
	if provider == model.ExternalIdentityProviderGitHub {
		legacyID := firstString(raw, "login")
		if legacyID != pu.ProviderID {
			pu.LegacyProviderID = legacyID
		}
	}
	return normalizeProviderUser(provider, pu)
}

// decodeBoundedOAuthJSONObject parses an untrusted provider response while
// enforcing per-field, cardinality, depth, and duplicate-key bounds. The
// aggregate body cap alone is insufficient: a tiny response can still carry
// thousands of keys or an identity string too large for persistent fields.
func decodeBoundedOAuthJSONObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	nodes := 0
	value, err := decodeBoundedOAuthJSONValue(decoder, 1, &nodes)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("OAuth JSON response has trailing data")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("OAuth JSON response must be an object")
	}
	return object, nil
}

func decodeBoundedOAuthJSONValue(decoder *json.Decoder, depth int, nodes *int) (any, error) {
	if decoder == nil || nodes == nil || depth > maxOAuthJSONDepth || *nodes >= maxOAuthJSONNodes {
		return nil, errors.New("OAuth JSON response exceeds safe limits")
	}
	(*nodes)++
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch value := token.(type) {
		case string:
			if !validOAuthText(value, maxOAuthJSONStringBytes, true) {
				return nil, errors.New("OAuth JSON string exceeds safe limits")
			}
		case json.Number:
			if value.String() == "" || len(value.String()) > maxOAuthJSONNumberBytes {
				return nil, errors.New("OAuth JSON number exceeds safe limits")
			}
		}
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			if len(object) >= maxOAuthJSONObjectItems {
				return nil, errors.New("OAuth JSON object exceeds safe limits")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok || !validOAuthText(key, maxOAuthJSONKeyBytes, false) {
				return nil, errors.New("OAuth JSON key exceeds safe limits")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("OAuth JSON response contains a duplicate key")
			}
			value, err := decodeBoundedOAuthJSONValue(decoder, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return nil, errors.New("OAuth JSON object is invalid")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			if len(array) >= maxOAuthJSONArrayItems {
				return nil, errors.New("OAuth JSON array exceeds safe limits")
			}
			value, err := decodeBoundedOAuthJSONValue(decoder, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return nil, errors.New("OAuth JSON array is invalid")
		}
		return array, nil
	default:
		return nil, errors.New("OAuth JSON response is invalid")
	}
}

func validateOAuthToken(token *OAuthToken) error {
	if token == nil || !validOAuthText(token.AccessToken, maxOAuthAccessTokenBytes, false) {
		return errors.New("OAuth access token is invalid")
	}
	for _, character := range token.AccessToken {
		if character < 0x21 || character > 0x7e {
			return errors.New("OAuth access token is invalid")
		}
	}
	token.TokenType = strings.TrimSpace(token.TokenType)
	if token.TokenType != "" && (len(token.TokenType) > maxOAuthTokenTypeBytes || !oauthTokenTypePattern.MatchString(token.TokenType)) {
		return errors.New("OAuth token type is invalid")
	}
	return nil
}

func normalizeProviderUser(provider string, input *ProviderUser) (*ProviderUser, error) {
	if input == nil || !validOAuthProviderSlug(provider) {
		return nil, errors.New("provider identity is invalid")
	}
	pu := *input
	maximumSubjectBytes := maxOAuthBuiltInSubjectBytes
	if pu.CustomProviderId > 0 {
		maximumSubjectBytes = maxOAuthCustomSubjectBytes
		configured, err := model.GetCustomOAuthProviderById(pu.CustomProviderId)
		if err != nil || !configured.Enabled || configured.Slug != provider || IsBuiltInOAuthProvider(provider) {
			return nil, errors.New("provider identity origin is invalid")
		}
	} else if _, ok := model.BuiltInExternalIdentityColumn(provider); !ok {
		return nil, errors.New("unsupported external identity provider")
	}
	pu.ProviderID = strings.TrimSpace(pu.ProviderID)
	if !validOAuthText(pu.ProviderID, maximumSubjectBytes, false) {
		if pu.CustomProviderId == 0 {
			return nil, fmt.Errorf("%w: provider identity exceeds safe limits", model.ErrInvalidPersistentIdentifier)
		}
		return nil, errors.New("provider identity exceeds safe limits")
	}
	if pu.LegacyProviderID != "" {
		pu.LegacyProviderID = strings.TrimSpace(pu.LegacyProviderID)
		if !validOAuthText(pu.LegacyProviderID, maxOAuthBuiltInSubjectBytes, false) {
			return nil, errors.New("legacy provider identity exceeds safe limits")
		}
	}
	pu.Username = normalizeOAuthProfileText(pu.Username, maxOAuthUsernameBytes)
	pu.DisplayName = normalizeOAuthProfileText(pu.DisplayName, maxOAuthDisplayNameBytes)
	if pu.Username == "" && pu.CustomProviderId == 0 {
		pu.Username = normalizeOAuthProfileText(pu.ProviderID, maxOAuthUsernameBytes)
	}
	if pu.DisplayName == "" {
		pu.DisplayName = pu.Username
	}
	if pu.Email != "" {
		if email, _, err := model.NormalizeVerifiedEmail(pu.Email); err == nil {
			pu.Email = email
		} else {
			// Provider emails are profile hints, not verified recovery identities.
			// Drop malformed values instead of rejecting an otherwise valid login.
			pu.Email = ""
		}
	}
	return &pu, nil
}

func normalizeOAuthProfileText(value string, maximumBytes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return -1
		}
		return character
	}, value)
	if len(value) <= maximumBytes {
		return value
	}
	for len(value) > maximumBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func boundedOAuthInteger(value any, minimum, maximum int) (int, bool) {
	var raw string
	switch typed := value.(type) {
	case json.Number:
		raw = typed.String()
	case int:
		raw = strconv.Itoa(typed)
	default:
		return 0, false
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minimum || parsed > maximum || strconv.Itoa(parsed) != raw {
		return 0, false
	}
	return parsed, true
}

// firstProviderID extracts the provider subject, handling string (OIDC "sub")
// and numeric (GitHub/Discord "id") shapes.
func firstProviderID(m map[string]any) string {
	if v, ok := m["sub"].(string); ok && v != "" {
		return v
	}
	if v, ok := m["id"].(string); ok && v != "" {
		return v
	}
	switch v := m["id"].(type) {
	case json.Number:
		value := v.String()
		if value != "" {
			for _, character := range value {
				if character < '0' || character > '9' {
					return ""
				}
			}
		}
		return value
	case float64:
		value := strconv.FormatFloat(v, 'f', -1, 64)
		if value == "" {
			return ""
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return ""
			}
		}
		return value
	case int:
		if v < 0 {
			return ""
		}
		return strconv.Itoa(v)
	}
	return ""
}

// LoginOrBindUser finds an existing user bound to the provider identity, or
// creates a new user and binds the identity (via the user's provider-scoped id
// field for built-in providers, and UserOAuthBinding for custom providers).
func LoginOrBindUser(provider string, pu *ProviderUser) (*model.User, bool, error) {
	return LoginOrBindUserWithAff(provider, pu, "")
}

// LoginOrBindUserWithAff finds an existing user by provider identity or creates
// a new one, applying the optional affiliate code as the inviter.
func LoginOrBindUserWithAff(provider string, pu *ProviderUser, affCode string) (*model.User, bool, error) {
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return nil, false, err
	}
	affCode = strings.TrimSpace(affCode)
	if !validOAuthText(affCode, 32, true) {
		return nil, false, errors.New("affiliate code exceeds safe limits")
	}
	var user *model.User
	var created bool
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		user, created, txErr = loginOrBindUserWithAffTx(tx, provider, normalized, affCode)
		return txErr
	})
	if err == nil {
		return user, created, nil
	}
	// A portable read-back resolves unique-index races without relying on a
	// driver's duplicate-key error type.
	if normalized.CustomProviderId > 0 {
		configured, originErr := model.GetCustomOAuthProviderById(normalized.CustomProviderId)
		if originErr != nil || !configured.Enabled || configured.Slug != provider {
			return nil, false, err
		}
	}
	if winner, lookupErr := findProviderUser(provider, normalized); lookupErr == nil {
		return winner, false, nil
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return nil, false, lookupErr
	}
	return nil, false, err
}

func loginOrBindUserWithAffTx(tx *gorm.DB, provider string, pu *ProviderUser, affCode string) (*model.User, bool, error) {
	if tx == nil || pu == nil {
		return nil, false, errors.New("invalid provider login transaction")
	}
	if err := validateCustomProviderIdentityWithTx(tx, provider, pu); err != nil {
		return nil, false, err
	}
	if user, err := findProviderUserWithTx(tx, provider, pu); err == nil {
		return user, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	// Older GitHub releases stored the login name rather than the stable
	// numeric id. Move the claim and compatibility mirror inside this caller's
	// transaction so the state consume can commit with the migration.
	if provider == model.ExternalIdentityProviderGitHub && pu.LegacyProviderID != "" && pu.LegacyProviderID != pu.ProviderID {
		legacy := &ProviderUser{ProviderID: pu.LegacyProviderID}
		legacyOwner, legacyErr := findProviderUserWithTx(tx, provider, legacy)
		if legacyErr == nil {
			if err := bindProviderToUserWithTx(tx, provider, pu, legacyOwner.Id); err != nil {
				return nil, false, err
			}
			legacyOwner.GitHubId = pu.ProviderID
			return legacyOwner, false, nil
		}
		if !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
			return nil, false, legacyErr
		}
	}

	registrationEnabled, err := registrationEnabledWithTx(tx)
	if err != nil {
		return nil, false, err
	}
	if !registrationEnabled {
		return nil, false, ErrRegistrationDisabled
	}

	inviterId := 0
	if affCode != "" {
		var inviter model.User
		if err := tx.Where("aff_code = ?", affCode).First(&inviter).Error; err == nil {
			inviterId = inviter.Id
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
	}
	baseUsername := pu.Username
	if baseUsername == "" {
		baseUsername = normalizeOAuthProfileText(provider+"_"+pu.ProviderID, maxOAuthUsernameBytes)
	}
	displayName := pu.DisplayName
	if displayName == "" {
		displayName = baseUsername
	}
	username, err := uniqueUsernameWithTx(tx, baseUsername)
	if err != nil {
		return nil, false, err
	}
	registrationPlan, err := PlanRegistrationMutationFromEnvironment(username)
	if err != nil {
		return nil, false, err
	}
	user := &model.User{
		Username: username, Password: "", DisplayName: displayName, Email: pu.Email,
		Role: constant.RoleCommonUser, Status: model.UserStatusEnabled,
		CreatedAt: common.NowTimestamp(), AuthVersion: 1, InviterId: inviterId,
	}
	if pu.CustomProviderId == 0 {
		setProviderField(user, provider, pu.ProviderID)
	}
	if err := InsertPlannedRegistrationUserWithTx(tx, user, registrationPlan); err != nil {
		return nil, false, err
	}
	if pu.CustomProviderId > 0 {
		if err := model.CreateUserOAuthBindingWithTx(tx, &model.UserOAuthBinding{
			UserId: user.Id, ProviderId: pu.CustomProviderId, ProviderUserId: pu.ProviderID,
		}); err != nil {
			return nil, false, err
		}
	} else if err := model.ClaimExternalIdentityWithTx(tx, provider, pu.ProviderID, user.Id); err != nil {
		return nil, false, err
	}
	if err := creditInviterTx(tx, inviterId, user.Id); err != nil {
		return nil, false, err
	}
	return user, true, nil
}

func registrationEnabledWithTx(tx *gorm.DB) (bool, error) {
	gates, err := registrationGateStateWithTx(tx)
	if err != nil {
		return false, err
	}
	return gates.registrationEnabled, nil
}

// BindProviderToUser binds a provider identity to an existing user by setting
// the provider-scoped column (built-ins) or upserting the user_oauth_bindings
// row (custom providers). The identity must not already be bound elsewhere.
func BindProviderToUser(provider string, pu *ProviderUser, userId int) error {
	if userId <= 0 {
		return errors.New("invalid external identity binding")
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		return bindProviderToUserWithTx(tx, provider, normalized, userId)
	})
	if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) || errors.Is(err, model.ErrOAuthBindingTaken) {
		return ErrBindingTaken
	}
	return err
}

// BindProviderToSession revalidates the exact live browser session inside the
// same transaction as the identity mutation. This is used by direct bind
// endpoints (such as WeChat) that do not have a separate one-time flow.
func BindProviderToSession(provider string, pu *ProviderUser, userId int, sessionId string) error {
	if userId <= 0 || !validOAuthText(sessionId, maxAuthFlowSessionBytes, false) || sessionId != strings.TrimSpace(sessionId) {
		return ErrSessionRevoked
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := validateSessionBindingWithTx(tx, userId, sessionId); err != nil {
			return err
		}
		return bindProviderToUserWithTx(tx, provider, normalized, userId)
	})
	if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) || errors.Is(err, model.ErrOAuthBindingTaken) {
		return ErrBindingTaken
	}
	return err
}

func bindProviderToUserWithTx(tx *gorm.DB, provider string, pu *ProviderUser, userId int) error {
	if tx == nil || pu == nil || userId <= 0 {
		return errors.New("invalid external identity binding")
	}
	if err := validateCustomProviderIdentityWithTx(tx, provider, pu); err != nil {
		return err
	}
	if pu.CustomProviderId > 0 {
		if err := model.UpdateUserOAuthBindingWithTx(tx, userId, pu.CustomProviderId, pu.ProviderID); err != nil {
			if errors.Is(err, model.ErrOAuthBindingTaken) {
				return ErrBindingTaken
			}
			return err
		}
		return nil
	}
	column, ok := model.BuiltInExternalIdentityColumn(provider)
	if !ok {
		return errors.New("unsupported external identity provider")
	}
	var user model.User
	query := tx
	if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Select("id", column).First(&user, userId).Error; err != nil {
		return err
	}
	if _, err := model.FindExternalIdentityClaimWithTx(tx, provider, pu.ProviderID); err == nil {
		return ErrBindingTaken
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if provider == model.ExternalIdentityProviderGitHub && pu.LegacyProviderID != "" && pu.LegacyProviderID != pu.ProviderID {
		if legacyClaim, err := model.FindExternalIdentityClaimWithTx(tx, provider, pu.LegacyProviderID); err == nil {
			if legacyClaim.UserId != userId {
				return ErrBindingTaken
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	oldSubject := providerFieldValue(&user, provider)
	if err := model.ReleaseExternalIdentityWithTx(tx, provider, userId); err != nil {
		return err
	}
	if err := model.ClaimExternalIdentityWithTx(tx, provider, pu.ProviderID, userId); err != nil {
		if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) {
			return ErrBindingTaken
		}
		return err
	}
	result := tx.Model(&model.User{}).
		Where("id = ? AND "+column+" = ?", userId, oldSubject).
		Update(column, pu.ProviderID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("concurrent external identity binding update")
	}
	return nil
}

func setProviderField(user *model.User, provider, id string) {
	switch provider {
	case "github":
		user.GitHubId = id
	case "discord":
		user.DiscordId = id
	case "oidc":
		user.OidcId = id
	case "linuxdo":
		user.LinuxDOId = id
	case "telegram":
		user.TelegramId = id
	case "wechat":
		user.WeChatId = id
	}
}

// providerFieldValue returns the provider-scoped identity value of a user.
func providerFieldValue(user *model.User, provider string) string {
	switch provider {
	case "github":
		return user.GitHubId
	case "discord":
		return user.DiscordId
	case "oidc":
		return user.OidcId
	case "linuxdo":
		return user.LinuxDOId
	case "telegram":
		return user.TelegramId
	case "wechat":
		return user.WeChatId
	}
	return ""
}

// FindUserByProviderIdentity resolves built-in identity ownership through the
// canonical claim table. A missing/deleted owner is an error, not permission
// to create a replacement account.
func FindUserByProviderIdentity(provider, id string) (*model.User, error) {
	normalized, err := normalizeProviderUser(provider, &ProviderUser{ProviderID: id})
	if err != nil {
		return nil, err
	}
	return model.FindUserByExternalIdentity(provider, normalized.ProviderID)
}

func uniqueUsername(base string) (string, error) {
	return uniqueUsernameWithTx(model.DB, base)
}

func uniqueUsernameWithTx(tx *gorm.DB, base string) (string, error) {
	if tx == nil {
		return "", errors.New("database is nil")
	}
	base = normalizeOAuthProfileText(base, maxOAuthUsernameBytes)
	if base == "" {
		base = "oauth_user"
	}
	var user model.User
	err := tx.Where("username = ?", base).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return base, nil
	}
	if err != nil {
		return "", err
	}
	prefix := normalizeOAuthProfileText(base, maxOAuthUsernameBytes-7)
	for range 5 {
		suffix, err := common.SecureRandomAlphanumeric(6)
		if err != nil {
			return "", err
		}
		candidate := prefix + "-" + suffix
		user = model.User{}
		err = tx.Where("username = ?", candidate).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("unable to allocate OAuth username")
}

func findProviderUser(provider string, pu *ProviderUser) (*model.User, error) {
	return findProviderUserWithTx(model.DB, provider, pu)
}

func validateCustomProviderIdentityWithTx(tx *gorm.DB, provider string, pu *ProviderUser) error {
	if pu == nil || pu.CustomProviderId == 0 {
		return nil
	}
	query := tx
	if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var configured model.CustomOAuthProvider
	if err := query.Select("id", "slug", "enabled").First(&configured, pu.CustomProviderId).Error; err != nil {
		return errors.New("custom OAuth identity provider is unavailable")
	}
	if !configured.Enabled || configured.Slug != provider || IsBuiltInOAuthProvider(provider) {
		return errors.New("custom OAuth identity provider is unavailable")
	}
	return nil
}

func findProviderUserWithTx(tx *gorm.DB, provider string, pu *ProviderUser) (*model.User, error) {
	if tx == nil || pu == nil {
		return nil, errors.New("invalid provider identity lookup")
	}
	if pu.CustomProviderId > 0 {
		return model.GetUserByOAuthBindingWithTx(tx, pu.CustomProviderId, pu.ProviderID)
	}
	claim, err := model.FindExternalIdentityClaimWithTx(tx, provider, pu.ProviderID)
	if err != nil {
		return nil, err
	}
	var user model.User
	if err := tx.First(&user, claim.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: user %d", model.ErrExternalIdentityOwnerInvalid, claim.UserId)
		}
		return nil, err
	}
	return &user, nil
}

func validateSessionBindingWithTx(tx *gorm.DB, userId int, sid string) error {
	if tx == nil || userId <= 0 || sid == "" {
		return ErrSessionRevoked
	}
	var session model.UserSession
	if err := tx.Where("user_id = ? AND sid = ?", userId, sid).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	var user model.User
	if err := tx.First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	if !validSessionSnapshot(&session, &user, now) {
		return ErrSessionRevoked
	}
	return nil
}

// ConsumeProviderLoginFlow commits the one-time flow consumption, account
// creation, identity claim, and referral credit in one transaction. Existing
// identities are resolved in that same snapshot.
func ConsumeProviderLoginFlow(token string, match AuthFlowMatch, provider string, pu *ProviderUser, affCode string) (*model.AuthFlow, *model.User, bool, error) {
	if match.Provider != provider || match.UserId != 0 || match.SessionId != "" {
		return nil, nil, false, ErrInvalidFlowToken
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return nil, nil, false, err
	}
	affCode = strings.TrimSpace(affCode)
	if !validOAuthText(affCode, 32, true) {
		return nil, nil, false, errors.New("affiliate code exceeds safe limits")
	}
	var user *model.User
	var created bool
	flow, err := ConsumeAuthFlowExactWithAction(token, match, func(tx *gorm.DB, _ *model.AuthFlow) error {
		var actionErr error
		user, created, actionErr = loginOrBindUserWithAffTx(tx, provider, normalized, affCode)
		return actionErr
	})
	if err != nil {
		return nil, nil, false, err
	}
	return flow, user, created, nil
}

// ConsumeProviderBindFlow commits a one-time session-bound flow and its
// identity replacement together. Ownership conflicts are terminal and still
// consume the flow; transient database failures roll everything back.
func ConsumeProviderBindFlow(token string, match AuthFlowMatch, provider string, pu *ProviderUser) (*model.AuthFlow, error) {
	if match.Provider != provider || match.UserId <= 0 || match.SessionId == "" {
		return nil, ErrInvalidFlowToken
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return nil, err
	}
	var bindErr error
	flow, err := ConsumeAuthFlowExactWithAction(token, match, func(tx *gorm.DB, _ *model.AuthFlow) error {
		if err := validateSessionBindingWithTx(tx, match.UserId, match.SessionId); err != nil {
			return err
		}
		bindErr = bindProviderToUserWithTx(tx, provider, normalized, match.UserId)
		if errors.Is(bindErr, ErrBindingTaken) {
			return nil
		}
		return bindErr
	})
	if err != nil {
		return nil, err
	}
	return flow, bindErr
}

// ErrBindingTaken is returned when a provider identity is already bound to a
// different account (built-in column or custom binding row).
var ErrBindingTaken = errors.New("该账号已绑定其他用户")
