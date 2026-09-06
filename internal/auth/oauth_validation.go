package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
	return httpx.SSRFDisabled()
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
	if !oauthDevelopmentURLAllowed() && (loopback || address != nil && httpx.IsUnsafeIP(address)) {
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
