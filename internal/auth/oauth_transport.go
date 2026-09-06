package auth

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var oauthHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	// Built-in provider endpoints can be overridden for OIDC/self-hosted
	// deployments. Resolve and connect directly through the SSRF guard; an
	// environment proxy must never get a chance to re-resolve the destination.
	Transport: &http.Transport{DialContext: httpx.SafeDialContext},
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
	body, err := httpx.ReadAllLimited(resp.Body, maxOAuthResponseBytes)
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
	body, err := httpx.ReadAllLimited(resp.Body, maxOAuthResponseBytes)
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
