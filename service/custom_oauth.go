package service

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// Custom-provider auth styles (how client credentials reach the token
// endpoint). 0 auto-detects, which defaults to form params.
const (
	OAuthAuthStyleAutoDetect = 0
	OAuthAuthStyleInParams   = 1
	OAuthAuthStyleInHeader   = 2
)

// builtInOAuthProviders are the slugs reserved by TokenRouter's first-class
// providers; a custom provider may not shadow them. This includes telegram
// and wechat (unlike the reference's 4-slug registry) because TokenRouter
// routes those flows through the same /api/oauth namespace.
var builtInOAuthProviders = map[string]struct{}{
	"github": {}, "discord": {}, "oidc": {}, "linuxdo": {},
	"telegram": {}, "wechat": {},
}

// IsBuiltInOAuthProvider reports whether a slug belongs to a built-in flow.
func IsBuiltInOAuthProvider(slug string) bool {
	_, ok := builtInOAuthProviders[slug]
	return ok
}

// customSlugPattern mirrors the provider-slug charset so arbitrary :provider
// path params don't turn into DB lookups.
var customSlugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// resolveCustomOAuthConfig loads a custom provider by slug into an
// OAuthConfig. Returns nil when the slug is built-in, malformed, or unknown.
func resolveCustomOAuthConfig(slug string) *OAuthConfig {
	if IsBuiltInOAuthProvider(slug) || !customSlugPattern.MatchString(slug) {
		return nil
	}
	provider, err := model.GetCustomOAuthProviderBySlug(slug)
	if err != nil {
		return nil
	}
	return &OAuthConfig{
		ClientID:     provider.ClientId,
		ClientSecret: provider.ClientSecret,
		AuthURL:      provider.AuthorizationEndpoint,
		TokenURL:     provider.TokenEndpoint,
		UserInfoURL:  provider.UserInfoEndpoint,
		Scopes:       strings.Fields(provider.Scopes),
		Enabled:      provider.Enabled,
		Known:        true,
		Custom:       provider,
	}
}

// customOAuthHTTPClient talks to operator-configured endpoints, so dials go
// through the SSRF guard (the reference uses a plain client here).
var customOAuthHTTPClient = &http.Client{
	Timeout:   20 * time.Second,
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: common.SafeDialContext},
}

// customExchangeCode exchanges an authorization code at a custom provider's
// token endpoint, honoring the configured auth style and accepting both JSON
// and urlencoded (GitHub-style) token responses.
func customExchangeCode(cfg *OAuthConfig, code, redirectURI string) (*OAuthToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	authStyle := cfg.Custom.AuthStyle
	if authStyle == OAuthAuthStyleAutoDetect {
		authStyle = OAuthAuthStyleInParams
	}
	if authStyle == OAuthAuthStyleInParams {
		form.Set("client_id", cfg.ClientID)
		form.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequest(http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if authStyle == OAuthAuthStyleInHeader {
		credentials := base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.ClientSecret))
		req.Header.Set("Authorization", "Basic "+credentials)
	}

	resp, err := customOAuthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := common.Unmarshal(body, &tokenResp); err != nil {
		values, parseErr := url.ParseQuery(string(body))
		if parseErr != nil {
			return nil, err
		}
		tokenResp.AccessToken = values.Get("access_token")
		tokenResp.TokenType = values.Get("token_type")
		tokenResp.Error = values.Get("error")
		tokenResp.ErrorDesc = values.Get("error_description")
	}
	if tokenResp.Error != "" {
		return nil, fmt.Errorf("token exchange failed: %s %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	if tokenResp.AccessToken == "" {
		return nil, errors.New("token exchange returned no access_token")
	}
	return &OAuthToken{AccessToken: tokenResp.AccessToken, TokenType: tokenResp.TokenType}, nil
}

// OAuthAccessDeniedError signals that the provider's access policy rejected
// the account; Message is already rendered for display.
type OAuthAccessDeniedError struct {
	Message string
}

func (e *OAuthAccessDeniedError) Error() string { return e.Message }

// normalizeAuthorizationTokenType canonicalizes the token_type used in the
// userinfo Authorization header ("", "bearer" → "Bearer"; otherwise verbatim).
func normalizeAuthorizationTokenType(tokenType string) string {
	tokenType = strings.TrimSpace(tokenType)
	if tokenType == "" || strings.EqualFold(tokenType, "Bearer") {
		return "Bearer"
	}
	return tokenType
}

// customFetchUserInfo fetches the raw userinfo document, extracts the mapped
// identity fields via gjson paths, and enforces the provider's access policy.
func customFetchUserInfo(cfg *OAuthConfig, token *OAuthToken) (*ProviderUser, error) {
	provider := cfg.Custom
	req, err := http.NewRequest(http.MethodGet, cfg.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", normalizeAuthorizationTokenType(token.TokenType)+" "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := customOAuthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo failed: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	bodyStr := string(body)

	userId := gjson.Get(bodyStr, provider.UserIdField).String()
	if userId == "" {
		// Numeric/typed ids stringify through the raw token (quotes trimmed).
		if result := gjson.Get(bodyStr, provider.UserIdField); result.Exists() {
			userId = strings.Trim(result.Raw, "\"")
		}
	}
	if userId == "" {
		return nil, fmt.Errorf("provider user has no id (field: %s)", provider.UserIdField)
	}

	if policyRaw := strings.TrimSpace(provider.AccessPolicy); policyRaw != "" {
		policy, err := model.ParseAccessPolicy(policyRaw)
		if err != nil {
			return nil, errors.New("invalid access policy configuration")
		}
		if allowed, failure := evaluateAccessPolicy(bodyStr, policy); !allowed {
			message := renderAccessDeniedMessage(provider.AccessDeniedMessage, provider.Name, bodyStr, failure)
			return nil, &OAuthAccessDeniedError{Message: message}
		}
	}

	return &ProviderUser{
		ProviderID:       userId,
		Username:         gjson.Get(bodyStr, provider.UsernameField).String(),
		DisplayName:      gjson.Get(bodyStr, provider.DisplayNameField).String(),
		Email:            gjson.Get(bodyStr, provider.EmailField).String(),
		CustomProviderId: provider.Id,
	}, nil
}

// accessPolicyFailure captures the first failing condition for the denied-
// message template.
type accessPolicyFailure struct {
	Field    string
	Op       string
	Expected any
	Current  any
}

// evaluateAccessPolicy walks the policy tree against the raw userinfo JSON.
// AND returns the first failure; OR passes on the first success and reports
// the first failure when nothing passed. An empty tree allows.
func evaluateAccessPolicy(body string, policy *model.AccessPolicyDocument) (bool, *accessPolicyFailure) {
	if policy == nil {
		return true, nil
	}
	logic := strings.ToLower(strings.TrimSpace(policy.Logic))
	if logic == "" {
		logic = "and"
	}
	if len(policy.Conditions) == 0 && len(policy.Groups) == 0 {
		return true, nil
	}

	if logic == "or" {
		var firstFailure *accessPolicyFailure
		for _, cond := range policy.Conditions {
			ok, failure := evaluateAccessCondition(body, cond)
			if ok {
				return true, nil
			}
			if firstFailure == nil {
				firstFailure = failure
			}
		}
		for i := range policy.Groups {
			ok, failure := evaluateAccessPolicy(body, &policy.Groups[i])
			if ok {
				return true, nil
			}
			if firstFailure == nil {
				firstFailure = failure
			}
		}
		return false, firstFailure
	}

	for _, cond := range policy.Conditions {
		if ok, failure := evaluateAccessCondition(body, cond); !ok {
			return false, failure
		}
	}
	for i := range policy.Groups {
		if ok, failure := evaluateAccessPolicy(body, &policy.Groups[i]); !ok {
			return false, failure
		}
	}
	return true, nil
}

func evaluateAccessCondition(body string, cond model.AccessPolicyCondition) (bool, *accessPolicyFailure) {
	op := strings.ToLower(strings.TrimSpace(cond.Op))
	result := gjson.Get(body, cond.Field)
	current := gjsonResultToValue(result)
	failure := &accessPolicyFailure{Field: cond.Field, Op: op, Expected: cond.Value, Current: current}

	switch op {
	case "exists":
		return result.Exists(), failure
	case "not_exists":
		return !result.Exists(), failure
	case "eq":
		return compareAny(current, cond.Value) == 0, failure
	case "ne":
		return compareAny(current, cond.Value) != 0, failure
	case "gt":
		return compareAny(current, cond.Value) > 0, failure
	case "gte":
		return compareAny(current, cond.Value) >= 0, failure
	case "lt":
		return compareAny(current, cond.Value) < 0, failure
	case "lte":
		return compareAny(current, cond.Value) <= 0, failure
	case "in":
		return valueInSlice(current, cond.Value), failure
	case "not_in":
		return !valueInSlice(current, cond.Value), failure
	case "contains":
		return containsValue(current, cond.Value), failure
	case "not_contains":
		return !containsValue(current, cond.Value), failure
	default:
		return false, failure
	}
}

// gjsonResultToValue converts a gjson result into plain Go values (arrays
// recurse; objects unmarshal, falling back to the raw JSON string).
func gjsonResultToValue(result gjson.Result) any {
	if !result.Exists() {
		return nil
	}
	if result.IsArray() {
		items := result.Array()
		values := make([]any, 0, len(items))
		for _, item := range items {
			values = append(values, gjsonResultToValue(item))
		}
		return values
	}
	switch result.Type {
	case gjson.Null:
		return nil
	case gjson.True:
		return true
	case gjson.False:
		return false
	case gjson.Number:
		return result.Num
	case gjson.String:
		return result.String()
	case gjson.JSON:
		var data any
		if err := common.UnmarshalJsonStr(result.Raw, &data); err == nil {
			return data
		}
		return result.Raw
	default:
		return result.Value()
	}
}

// compareAny compares numerically when both sides parse as numbers, falling
// back to a trimmed string comparison.
func compareAny(left, right any) int {
	if lf, ok := policyToFloat(left); ok {
		if rf, ok2 := policyToFloat(right); ok2 {
			switch {
			case lf < rf:
				return -1
			case lf > rf:
				return 1
			default:
				return 0
			}
		}
	}
	ls := strings.TrimSpace(fmt.Sprint(left))
	rs := strings.TrimSpace(fmt.Sprint(right))
	switch {
	case ls < rs:
		return -1
	case ls > rs:
		return 1
	default:
		return 0
	}
}

func policyToFloat(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func valueInSlice(current, expected any) bool {
	list, ok := expected.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if compareAny(current, item) == 0 {
			return true
		}
	}
	return false
}

func containsValue(current, expected any) bool {
	switch value := current.(type) {
	case string:
		return strings.Contains(value, strings.TrimSpace(fmt.Sprint(expected)))
	case []any:
		for _, item := range value {
			if compareAny(item, expected) == 0 {
				return true
			}
		}
	}
	return false
}

var (
	deniedCurrentPattern  = regexp.MustCompile(`\{\{current\.([^}]+)\}\}`)
	deniedRequiredPattern = regexp.MustCompile(`\{\{required\.([^}]+)\}\}`)
)

// renderAccessDeniedMessage fills the provider's denied-message template:
// {{provider}}/{{field}}/{{op}}/{{required}}/{{current}} plus
// {{current.<path>}} (from the userinfo body) and {{required.<path>}} (the
// expected value when <path> is the failing field).
func renderAccessDeniedMessage(template, providerName, body string, failure *accessPolicyFailure) string {
	message := strings.TrimSpace(template)
	if message == "" {
		return "Access denied: your account does not meet this provider's access requirements."
	}
	if failure == nil {
		failure = &accessPolicyFailure{}
	}

	replacements := map[string]string{
		"{{provider}}": providerName,
		"{{field}}":    failure.Field,
		"{{op}}":       failure.Op,
		"{{required}}": fmt.Sprint(failure.Expected),
		"{{current}}":  fmt.Sprint(failure.Current),
	}
	for key, value := range replacements {
		message = strings.ReplaceAll(message, key, value)
	}

	message = deniedCurrentPattern.ReplaceAllStringFunc(message, func(token string) string {
		match := deniedCurrentPattern.FindStringSubmatch(token)
		if len(match) != 2 {
			return ""
		}
		path := strings.TrimSpace(match[1])
		if path == "" {
			return ""
		}
		return strings.TrimSpace(gjson.Get(body, path).String())
	})
	message = deniedRequiredPattern.ReplaceAllStringFunc(message, func(token string) string {
		match := deniedRequiredPattern.FindStringSubmatch(token)
		if len(match) != 2 {
			return ""
		}
		if failure.Field == strings.TrimSpace(match[1]) {
			return fmt.Sprint(failure.Expected)
		}
		return ""
	})

	return strings.TrimSpace(message)
}

// OAuthBindingInfo is the user-facing binding shape (provider metadata plus
// the provider-side user id).
type OAuthBindingInfo struct {
	ProviderId     int    `json:"provider_id"`
	ProviderName   string `json:"provider_name"`
	ProviderSlug   string `json:"provider_slug"`
	ProviderIcon   string `json:"provider_icon"`
	ProviderUserId string `json:"provider_user_id"`
}

// ListOAuthBindings returns a user's custom-provider bindings with provider
// metadata resolved; bindings whose provider was deleted are skipped
// (reference semantics).
func ListOAuthBindings(userId int) ([]OAuthBindingInfo, error) {
	bindings, err := model.GetUserOAuthBindingsByUserId(userId)
	if err != nil {
		return nil, err
	}
	out := make([]OAuthBindingInfo, 0, len(bindings))
	for _, binding := range bindings {
		provider, err := model.GetCustomOAuthProviderById(binding.ProviderId)
		if err != nil {
			continue
		}
		out = append(out, OAuthBindingInfo{
			ProviderId:     binding.ProviderId,
			ProviderName:   provider.Name,
			ProviderSlug:   provider.Slug,
			ProviderIcon:   provider.Icon,
			ProviderUserId: binding.ProviderUserId,
		})
	}
	return out, nil
}

// UnbindOAuth removes one custom-provider binding (provider id in the path,
// reference semantics).
func UnbindOAuth(userId int, providerIdStr string) error {
	providerId, err := strconv.Atoi(providerIdStr)
	if err != nil {
		return errors.New("无效的提供商 ID")
	}
	return model.DeleteUserOAuthBinding(userId, providerId)
}
