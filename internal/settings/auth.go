package settings

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

const (
	EmailDomainRestrictionEnabledOption = "EmailDomainRestrictionEnabled"
	EmailAliasRestrictionEnabledOption  = "EmailAliasRestrictionEnabled"
	EmailDomainWhitelistOption          = "EmailDomainWhitelist"

	GitHubClientIDOption     = "GitHubClientId"
	GitHubClientSecretOption = "GitHubClientSecret"

	DiscordOptionEnabled      = "discord.enabled"
	DiscordClientIDOption     = "discord.client_id"
	DiscordClientSecretOption = "discord.client_secret"

	LinuxDOClientIDOption          = "LinuxDOClientId"
	LinuxDOClientSecretOption      = "LinuxDOClientSecret"
	LinuxDOMinimumTrustLevelOption = "LinuxDOMinimumTrustLevel"

	OIDCOAuthEnabledOption          = "oidc.enabled"
	OIDCDisplayNameOption           = "oidc.display_name"
	OIDCClientIDOption              = "oidc.client_id"
	OIDCClientSecretOption          = "oidc.client_secret"
	OIDCWellKnownOption             = "oidc.well_known"
	OIDCAuthorizationEndpointOption = "oidc.authorization_endpoint"
	OIDCTokenEndpointOption         = "oidc.token_endpoint"
	OIDCUserInfoEndpointOption      = "oidc.user_info_endpoint"

	PasskeyRPDisplayNameOption        = "passkey.rp_display_name"
	PasskeyRPIDOption                 = "passkey.rp_id"
	PasskeyOriginsOption              = "passkey.origins"
	PasskeyAllowInsecureOriginOption  = "passkey.allow_insecure_origin"
	PasskeyUserVerificationOption     = "passkey.user_verification"
	PasskeyAttachmentPreferenceOption = "passkey.attachment_preference"

	legacyPasskeyRPDisplayNameOption = "WebAuthnRPDisplayName"
	legacyPasskeyRPIDOption          = "WebAuthnRPID"

	maxAuthClientIDBytes      = 256
	maxAuthClientSecretBytes  = 4096
	maxAuthEndpointBytes      = 2048
	maxAuthDisplayNameBytes   = 128
	maxEmailDomainListBytes   = 16 << 10
	maxEmailDomainCount       = 100
	maxPasskeyOriginsBytes    = 16 << 10
	maxPasskeyOriginCount     = 32
	maxAuthEndpointQueryPairs = 16
	maxAuthEndpointQueryBytes = 2048
)

var (
	authenticationConfig atomic.Pointer[AuthenticationSetting]
	domainLabelPattern   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

	ErrEmailDomainNotAllowed = errors.New("email domain is not allowed")
	ErrEmailAliasNotAllowed  = errors.New("email aliases are not allowed")
)

var defaultEmailDomainWhitelist = []string{
	"gmail.com", "163.com", "126.com", "qq.com", "outlook.com",
	"hotmail.com", "icloud.com", "yahoo.com", "foxmail.com",
}

// BuiltInOAuthSetting is the coherent option-backed configuration for one
// built-in OAuth provider. ClientSecret is intentionally never returned by a
// public status handler.
type BuiltInOAuthSetting struct {
	Enabled               bool
	ClientID              string
	ClientSecret          string
	DisplayName           string
	WellKnown             string
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserInfoEndpoint      string
	MinimumTrustLevel     int
}

// PasskeySetting contains the relying-party and authenticator preferences
// used for every new WebAuthn ceremony.
type PasskeySetting struct {
	Enabled              bool
	RPDisplayName        string
	RPID                 string
	Origins              []string
	AllowInsecureOrigin  bool
	UserVerification     string
	AttachmentPreference string
}

// AuthenticationSetting is published as one immutable snapshot so policy,
// credentials, and endpoint changes cannot be observed halfway through an
// UpdateOptions transaction or a remote Sync.
type AuthenticationSetting struct {
	EmailDomainRestrictionEnabled bool
	EmailAliasRestrictionEnabled  bool
	EmailDomainWhitelist          []string
	GitHub                        BuiltInOAuthSetting
	Discord                       BuiltInOAuthSetting
	LinuxDO                       BuiltInOAuthSetting
	OIDC                          BuiltInOAuthSetting
	Passkey                       PasskeySetting

	emailDomainSet map[string]struct{}
}

func validBoundedAuthText(value string, maximumBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func parseAuthBool(values map[string]string, key string, fallback bool) (bool, error) {
	raw, present := values[key]
	if !present || raw == "" {
		return fallback, nil
	}
	switch strings.ToLower(raw) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, errors.New(key + " must be a boolean")
	}
}

func validDNSName(value string) bool {
	if value == "" || len(value) > 253 || value != strings.TrimSpace(value) || strings.HasSuffix(value, ".") {
		return false
	}
	if ip := net.ParseIP(value); ip != nil {
		return true
	}
	for _, label := range strings.Split(value, ".") {
		if !domainLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func parseEmailDomainWhitelist(raw string, useDefault bool) ([]string, map[string]struct{}, error) {
	if raw == "" && useDefault {
		raw = strings.Join(defaultEmailDomainWhitelist, ",")
	}
	if len(raw) > maxEmailDomainListBytes || !utf8.ValidString(raw) {
		return nil, nil, errors.New("email domain whitelist exceeds safe limits")
	}
	parts := strings.FieldsFunc(raw, func(character rune) bool {
		return character == ',' || character == '\n' || character == '\r'
	})
	if len(parts) > maxEmailDomainCount {
		return nil, nil, errors.New("email domain whitelist has too many entries")
	}
	domains := make([]string, 0, len(parts))
	set := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		domain := strings.ToLower(strings.TrimSpace(part))
		if !validDNSName(domain) || net.ParseIP(domain) != nil {
			return nil, nil, errors.New("email domain whitelist contains an invalid domain")
		}
		if _, exists := set[domain]; exists {
			continue
		}
		set[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains, set, nil
}

func validateAuthEndpoint(raw string) error {
	if raw == "" {
		return nil
	}
	if raw != strings.TrimSpace(raw) || !validBoundedAuthText(raw, maxAuthEndpointBytes, false) || strings.Contains(raw, `\`) {
		return errors.New("OAuth endpoint is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery || strings.Contains(parsed.Path, `\`) {
		return errors.New("OAuth endpoint is invalid")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return errors.New("OAuth endpoint path is ambiguous")
		}
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return errors.New("OAuth endpoint port is invalid")
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return errors.New("OAuth endpoint query is invalid")
	}
	pairs := 0
	for key, items := range query {
		pairs += len(items)
		if key == "" || len(key) > 128 || len(items) != 1 || len(items[0]) > maxAuthEndpointQueryBytes ||
			!validBoundedAuthText(key, 128, false) || !validBoundedAuthText(items[0], maxAuthEndpointQueryBytes, true) {
			return errors.New("OAuth endpoint query is unsafe or ambiguous")
		}
	}
	if pairs > maxAuthEndpointQueryPairs {
		return errors.New("OAuth endpoint has too many query parameters")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(host)
	loopback := host == "localhost" || strings.HasSuffix(host, ".localhost") || address != nil && address.IsLoopback()
	if scheme != "https" && !(scheme == "http" && httpx.SSRFDisabled() && loopback) {
		return errors.New("OAuth endpoint must use HTTPS")
	}
	if !httpx.SSRFDisabled() && address != nil && httpx.IsUnsafeIP(address) {
		return errors.New("OAuth endpoint targets an unsafe address")
	}
	return nil
}

func validateOAuthCredential(value string, maximumBytes int, name string, allowEmpty bool) error {
	if value != strings.TrimSpace(value) || !validBoundedAuthText(value, maximumBytes, allowEmpty) {
		return errors.New(name + " is invalid")
	}
	return nil
}

func buildOAuthSetting(values map[string]string, enabledKey, clientIDKey, secretKey string) (BuiltInOAuthSetting, error) {
	enabled, err := parseAuthBool(values, enabledKey, false)
	if err != nil {
		return BuiltInOAuthSetting{}, err
	}
	setting := BuiltInOAuthSetting{
		Enabled:      enabled,
		ClientID:     values[clientIDKey],
		ClientSecret: values[secretKey],
	}
	if err := validateOAuthCredential(setting.ClientID, maxAuthClientIDBytes, clientIDKey, true); err != nil {
		return BuiltInOAuthSetting{}, err
	}
	if err := validateOAuthCredential(setting.ClientSecret, maxAuthClientSecretBytes, secretKey, true); err != nil {
		return BuiltInOAuthSetting{}, err
	}
	return setting, nil
}

func parsePasskeyOrigins(raw string, allowInsecure bool) ([]string, error) {
	if raw == "" || strings.TrimSpace(raw) == "[]" {
		return nil, nil
	}
	if len(raw) > maxPasskeyOriginsBytes || !utf8.ValidString(raw) {
		return nil, errors.New("passkey origins exceed safe limits")
	}
	parts := strings.FieldsFunc(raw, func(character rune) bool {
		return character == ',' || character == '\n' || character == '\r'
	})
	if len(parts) > maxPasskeyOriginCount {
		return nil, errors.New("passkey origins have too many entries")
	}
	origins := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if !validBoundedAuthText(origin, maxAuthEndpointBytes, false) || strings.Contains(origin, `\`) {
			return nil, errors.New("passkey origin is invalid")
		}
		parsed, err := url.Parse(origin)
		if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Hostname() == "" || parsed.User != nil ||
			parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || (parsed.Path != "" && parsed.Path != "/") {
			return nil, errors.New("passkey origin must be an origin without a path, query, or fragment")
		}
		scheme := strings.ToLower(parsed.Scheme)
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		if strings.Contains(host, "%") {
			return nil, errors.New("passkey origin zone identifiers are not allowed")
		}
		address := net.ParseIP(host)
		loopback := host == "localhost" || strings.HasSuffix(host, ".localhost") || address != nil && address.IsLoopback()
		if scheme != "https" && !(scheme == "http" && (allowInsecure || loopback)) {
			return nil, errors.New("passkey origin must use HTTPS unless insecure origins are explicitly allowed")
		}
		port := parsed.Port()
		if port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 {
				return nil, errors.New("passkey origin port is invalid")
			}
		}
		canonicalHost := host
		if port != "" {
			canonicalHost = net.JoinHostPort(host, port)
		} else if address != nil && strings.Contains(host, ":") {
			canonicalHost = "[" + host + "]"
		}
		canonical := scheme + "://" + canonicalHost
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		origins = append(origins, canonical)
	}
	return origins, nil
}

// ParsePasskeyOrigins validates a deployment-level origins override using the
// same rules as option-backed origins.
func ParsePasskeyOrigins(raw string, allowInsecure bool) ([]string, error) {
	return parsePasskeyOrigins(raw, allowInsecure)
}

func buildPasskeySetting(values map[string]string) (PasskeySetting, error) {
	enabled, err := parseAuthBool(values, PasskeyEnabledOption, false)
	if err != nil {
		return PasskeySetting{}, err
	}
	allowInsecure, err := parseAuthBool(values, PasskeyAllowInsecureOriginOption, false)
	if err != nil {
		return PasskeySetting{}, err
	}
	displayName, present := values[PasskeyRPDisplayNameOption]
	if !present {
		displayName = values[legacyPasskeyRPDisplayNameOption]
	}
	rpID, present := values[PasskeyRPIDOption]
	if !present {
		rpID = values[legacyPasskeyRPIDOption]
	}
	if displayName != strings.TrimSpace(displayName) || !validBoundedAuthText(displayName, maxAuthDisplayNameBytes, true) {
		return PasskeySetting{}, errors.New("passkey relying-party display name is invalid")
	}
	if rpID != "" && !validDNSName(rpID) {
		return PasskeySetting{}, errors.New("passkey relying-party ID is invalid")
	}
	origins, err := parsePasskeyOrigins(values[PasskeyOriginsOption], allowInsecure)
	if err != nil {
		return PasskeySetting{}, err
	}
	userVerification := values[PasskeyUserVerificationOption]
	if userVerification == "" {
		userVerification = "preferred"
	}
	switch userVerification {
	case "required", "preferred", "discouraged":
	default:
		return PasskeySetting{}, errors.New("passkey user verification preference is invalid")
	}
	attachment := values[PasskeyAttachmentPreferenceOption]
	switch attachment {
	case "", "platform", "cross-platform":
	default:
		return PasskeySetting{}, errors.New("passkey attachment preference is invalid")
	}
	return PasskeySetting{
		Enabled:              enabled,
		RPDisplayName:        displayName,
		RPID:                 rpID,
		Origins:              origins,
		AllowInsecureOrigin:  allowInsecure,
		UserVerification:     userVerification,
		AttachmentPreference: attachment,
	}, nil
}

func buildAuthenticationSetting(values map[string]string) (AuthenticationSetting, error) {
	domainRestriction, err := parseAuthBool(values, EmailDomainRestrictionEnabledOption, false)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	aliasRestriction, err := parseAuthBool(values, EmailAliasRestrictionEnabledOption, false)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	whitelistRaw, whitelistPresent := values[EmailDomainWhitelistOption]
	domains, domainSet, err := parseEmailDomainWhitelist(whitelistRaw, !whitelistPresent)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	if domainRestriction && len(domains) == 0 {
		return AuthenticationSetting{}, errors.New("email domain restriction requires at least one allowed domain")
	}

	github, err := buildOAuthSetting(values, GitHubOAuthEnabledOption, GitHubClientIDOption, GitHubClientSecretOption)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	discord, err := buildOAuthSetting(values, DiscordOptionEnabled, DiscordClientIDOption, DiscordClientSecretOption)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	linuxDO, err := buildOAuthSetting(values, LinuxDOOAuthEnabledOption, LinuxDOClientIDOption, LinuxDOClientSecretOption)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	minimumTrustLevel := 0
	if raw := values[LinuxDOMinimumTrustLevelOption]; raw != "" {
		minimumTrustLevel, err = strconv.Atoi(raw)
		if err != nil || minimumTrustLevel < 0 || minimumTrustLevel > 4 || strconv.Itoa(minimumTrustLevel) != raw {
			return AuthenticationSetting{}, errors.New("Linux DO minimum trust level must be an integer from 0 through 4")
		}
	}
	linuxDO.MinimumTrustLevel = minimumTrustLevel

	oidc, err := buildOAuthSetting(values, OIDCOAuthEnabledOption, OIDCClientIDOption, OIDCClientSecretOption)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	oidc.DisplayName = values[OIDCDisplayNameOption]
	if oidc.DisplayName == "" {
		oidc.DisplayName = "OIDC"
	}
	if oidc.DisplayName != strings.TrimSpace(oidc.DisplayName) || !validBoundedAuthText(oidc.DisplayName, maxAuthDisplayNameBytes, false) {
		return AuthenticationSetting{}, errors.New("OIDC display name is invalid")
	}
	oidc.WellKnown = values[OIDCWellKnownOption]
	oidc.AuthorizationEndpoint = values[OIDCAuthorizationEndpointOption]
	oidc.TokenEndpoint = values[OIDCTokenEndpointOption]
	oidc.UserInfoEndpoint = values[OIDCUserInfoEndpointOption]
	for _, endpoint := range []string{oidc.WellKnown, oidc.AuthorizationEndpoint, oidc.TokenEndpoint, oidc.UserInfoEndpoint} {
		if err := validateAuthEndpoint(endpoint); err != nil {
			return AuthenticationSetting{}, err
		}
	}
	passkey, err := buildPasskeySetting(values)
	if err != nil {
		return AuthenticationSetting{}, err
	}
	return AuthenticationSetting{
		EmailDomainRestrictionEnabled: domainRestriction,
		EmailAliasRestrictionEnabled:  aliasRestriction,
		EmailDomainWhitelist:          domains,
		GitHub:                        github,
		Discord:                       discord,
		LinuxDO:                       linuxDO,
		OIDC:                          oidc,
		Passkey:                       passkey,
		emailDomainSet:                domainSet,
	}, nil
}

func cloneAuthenticationSetting(value AuthenticationSetting) AuthenticationSetting {
	value.EmailDomainWhitelist = append([]string(nil), value.EmailDomainWhitelist...)
	value.Passkey.Origins = append([]string(nil), value.Passkey.Origins...)
	value.emailDomainSet = make(map[string]struct{}, len(value.EmailDomainWhitelist))
	for _, domain := range value.EmailDomainWhitelist {
		value.emailDomainSet[domain] = struct{}{}
	}
	return value
}

// GetAuthenticationSetting returns a detached copy of the current coherent
// authentication snapshot.
func GetAuthenticationSetting() AuthenticationSetting {
	if current := authenticationConfig.Load(); current != nil {
		return cloneAuthenticationSetting(*current)
	}
	fallback, _ := buildAuthenticationSetting(map[string]string{})
	return cloneAuthenticationSetting(fallback)
}

// OAuthProviderSetting returns the option-backed provider configuration.
func OAuthProviderSetting(provider string) (BuiltInOAuthSetting, bool) {
	auth := GetAuthenticationSetting()
	switch provider {
	case "github":
		return auth.GitHub, true
	case "discord":
		return auth.Discord, true
	case "linuxdo":
		return auth.LinuxDO, true
	case "oidc":
		return auth.OIDC, true
	default:
		return BuiltInOAuthSetting{}, false
	}
}

// ValidateEmailRegistrationPolicy applies the same canonical domain and alias
// restrictions at code issuance, binding, and final account creation.
func ValidateEmailRegistrationPolicy(email string) error {
	auth := GetAuthenticationSetting()
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return errors.New("email address is invalid")
	}
	local := email[:at]
	domain := strings.ToLower(email[at+1:])
	if auth.EmailDomainRestrictionEnabled {
		if _, allowed := auth.emailDomainSet[domain]; !allowed {
			return ErrEmailDomainNotAllowed
		}
	}
	if auth.EmailAliasRestrictionEnabled && (strings.Contains(local, "+") || strings.Contains(local, ".")) {
		return ErrEmailAliasNotAllowed
	}
	return nil
}

// AuthenticationOptionDefaults supplies non-secret defaults to the root-only
// settings page. Credential values are never synthesized or returned.
func AuthenticationOptionDefaults() map[string]string {
	return map[string]string{
		EmailDomainRestrictionEnabledOption: "false",
		EmailAliasRestrictionEnabledOption:  "false",
		EmailDomainWhitelistOption:          strings.Join(defaultEmailDomainWhitelist, ","),
		GitHubOAuthEnabledOption:            "false",
		DiscordOptionEnabled:                "false",
		LinuxDOOAuthEnabledOption:           "false",
		LinuxDOMinimumTrustLevelOption:      "0",
		OIDCOAuthEnabledOption:              "false",
		OIDCDisplayNameOption:               "OIDC",
		PasskeyEnabledOption:                "false",
		PasskeyRPDisplayNameOption:          "",
		PasskeyRPIDOption:                   "",
		PasskeyOriginsOption:                "",
		PasskeyAllowInsecureOriginOption:    "false",
		PasskeyUserVerificationOption:       "preferred",
		PasskeyAttachmentPreferenceOption:   "",
	}
}
