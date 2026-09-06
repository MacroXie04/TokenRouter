package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/google/uuid"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	codexOAuthClientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthTokenURL        = "https://auth.openai.com/oauth/token"
	codexJWTClaimPath         = "https://api.openai.com/auth"
	codexRefreshTimeout       = 10 * time.Second
	codexUsageTimeout         = 15 * time.Second
	codexOAuthResponseLimit   = int64(256 << 10)
	codexWhamResponseLimit    = int64(1 << 20)
	codexCredentialLimit      = 64 << 10
	codexAccessTokenLimit     = 32 << 10
	codexRefreshTokenLimit    = 16 << 10
	codexIDTokenLimit         = 32 << 10
	codexAccountIDLimit       = 512
	codexEmailLimit           = 320
	codexMetadataFieldLimit   = 128
	codexMaxOAuthExpirySecond = int64(365 * 24 * 60 * 60)
)

// codexAdminHTTPClient is deliberately independent from the general channel
// client. Codex admin calls carry account credentials, so redirects are never
// followed and every connection is made through the DNS-rebinding-safe dialer.
var codexAdminHTTPClient = &http.Client{
	Timeout: codexUsageTimeout,
	Transport: &http.Transport{
		DialContext: httpx.SafeDialContext,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// CodexOAuthKey is the persisted Codex channel credential shape. Callers must
// not serialize this value into an API response; the public refresh operation
// returns CodexCredentialRefreshResult instead.
type CodexOAuthKey struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
	Email        string `json:"email,omitempty"`
	Type         string `json:"type,omitempty"`
	Expired      string `json:"expired,omitempty"`
}

// CodexOAuthTokenResult is the validated result of the OAuth refresh exchange.
type CodexOAuthTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// CodexCredentialRefreshResult is the non-secret dashboard response returned
// after a channel credential refresh.
type CodexCredentialRefreshResult struct {
	ExpiresAt   string `json:"expires_at"`
	LastRefresh string `json:"last_refresh"`
	AccountID   string `json:"account_id"`
	Email       string `json:"email"`
	ChannelID   int    `json:"channel_id"`
	ChannelType int    `json:"channel_type"`
	ChannelName string `json:"channel_name"`
}

// CodexUpstreamResult preserves the reference endpoint's upstream status and
// bounded response body. Controllers decode JSON when possible and otherwise
// return the body as a string.
type CodexUpstreamResult struct {
	StatusCode int
	Body       []byte
}

type codexAdminRuntime struct {
	client        *http.Client
	oauthTokenURL string
	now           func() time.Time
	requestID     func() (string, error)
}

func defaultCodexAdminRuntime() codexAdminRuntime {
	return codexAdminRuntime{
		client:        codexAdminHTTPClient,
		oauthTokenURL: codexOAuthTokenURL,
		now:           time.Now,
		requestID:     cryptoutil.SecureRandomUUID,
	}
}

type codexChannelContractError struct {
	message string
}

func (e *codexChannelContractError) Error() string { return e.message }

func newCodexChannelContractError(message string) error {
	return &codexChannelContractError{message: message}
}

// CodexChannelContractMessage identifies errors that the immutable reference
// intentionally exposes to an authenticated dashboard caller. All other
// errors must be mapped to the operation's generic failure message.
func CodexChannelContractMessage(err error) (string, bool) {
	var contractErr *codexChannelContractError
	if errors.As(err, &contractErr) {
		return contractErr.message, true
	}
	return "", false
}

func parseCodexOAuthKey(raw string) (*CodexOAuthKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("codex channel: empty oauth key")
	}
	if len(trimmed) > codexCredentialLimit {
		return nil, errors.New("codex channel: oauth key is too large")
	}
	var key CodexOAuthKey
	if err := jsonutil.Unmarshal([]byte(trimmed), &key); err != nil {
		return nil, errors.New("codex channel: invalid oauth key json")
	}
	if err := validateCodexOAuthKey(&key); err != nil {
		return nil, err
	}
	return &key, nil
}

func validateCodexOAuthKey(key *CodexOAuthKey) error {
	if key == nil {
		return errors.New("codex channel: invalid oauth key")
	}
	fields := []struct {
		name  string
		value string
		limit int
	}{
		{"id_token", key.IDToken, codexIDTokenLimit},
		{"access_token", key.AccessToken, codexAccessTokenLimit},
		{"refresh_token", key.RefreshToken, codexRefreshTokenLimit},
		{"account_id", key.AccountID, codexAccountIDLimit},
		{"email", key.Email, codexEmailLimit},
		{"last_refresh", key.LastRefresh, codexMetadataFieldLimit},
		{"type", key.Type, codexMetadataFieldLimit},
		{"expired", key.Expired, codexMetadataFieldLimit},
	}
	for _, field := range fields {
		if len(field.value) > field.limit {
			return fmt.Errorf("codex channel: %s is too large", field.name)
		}
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("codex channel: %s is not valid UTF-8", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"access_token", key.AccessToken},
		{"account_id", key.AccountID},
		{"refresh_token", key.RefreshToken},
	} {
		if strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("codex channel: %s contains invalid control characters", field.name)
		}
	}
	return nil
}

func validateCodexAdminRuntime(runtime codexAdminRuntime) error {
	if runtime.client == nil {
		return errors.New("codex admin: nil http client")
	}
	if runtime.now == nil {
		return errors.New("codex admin: nil clock")
	}
	if runtime.requestID == nil {
		return errors.New("codex admin: nil request id generator")
	}
	return nil
}

func codexRequestURL(baseURL, endpoint string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", errors.New("codex upstream base URL is empty")
	}
	if len(baseURL) > 4096 {
		return "", errors.New("codex upstream base URL is too large")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", errors.New("codex upstream base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("codex upstream URL scheme is not supported")
	}
	if parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", errors.New("codex upstream URL host is invalid")
	}
	if parsed.User != nil {
		return "", errors.New("codex upstream URL credentials are not allowed")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("codex upstream base URL query and fragment are not allowed")
	}
	// Credential-bearing production requests must use TLS. Local HTTP mock
	// servers remain available only when the explicit development SSRF bypass
	// is enabled.
	if parsed.Scheme != "https" && !httpx.SSRFDisabled() {
		return "", errors.New("codex upstream URL must use https")
	}
	return strings.TrimRight(baseURL, "/") + endpoint, nil
}

// RefreshCodexOAuthToken refreshes a Codex OAuth credential against the fixed
// OpenAI token endpoint. The endpoint is not configurable in production.
func RefreshCodexOAuthToken(ctx context.Context, refreshToken string) (*CodexOAuthTokenResult, error) {
	runtime := defaultCodexAdminRuntime()
	return refreshCodexOAuthToken(ctx, runtime, refreshToken)
}

func refreshCodexOAuthToken(
	ctx context.Context,
	runtime codexAdminRuntime,
	refreshToken string,
) (*CodexOAuthTokenResult, error) {
	if err := validateCodexAdminRuntime(runtime); err != nil {
		return nil, err
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("codex oauth: empty refresh_token")
	}
	if len(refreshToken) > codexRefreshTokenLimit || strings.ContainsAny(refreshToken, "\r\n") {
		return nil, errors.New("codex oauth: invalid refresh_token")
	}
	tokenURL, err := codexRequestURL(runtime.oauthTokenURL, "")
	if err != nil {
		return nil, fmt.Errorf("codex oauth endpoint: %w", err)
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", codexOAuthClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("codex oauth: create refresh request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := runtime.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex oauth refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := httpx.ReadAllLimited(resp.Body, codexOAuthResponseLimit)
	if err != nil {
		return nil, fmt.Errorf("codex oauth refresh response rejected: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("codex oauth refresh failed: status=%d", resp.StatusCode)
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := jsonutil.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("codex oauth refresh response is invalid JSON")
	}
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
	if payload.AccessToken == "" || payload.RefreshToken == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("codex oauth refresh response missing fields")
	}
	if len(payload.AccessToken) > codexAccessTokenLimit ||
		len(payload.RefreshToken) > codexRefreshTokenLimit ||
		strings.ContainsAny(payload.AccessToken, "\r\n") ||
		strings.ContainsAny(payload.RefreshToken, "\r\n") {
		return nil, errors.New("codex oauth refresh response contains invalid token fields")
	}
	if payload.ExpiresIn > codexMaxOAuthExpirySecond {
		return nil, errors.New("codex oauth refresh response expiry is out of range")
	}
	return &CodexOAuthTokenResult{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    runtime.now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// RefreshCodexChannelCredential refreshes and atomically persists a Codex
// channel credential while returning only non-secret metadata.
func RefreshCodexChannelCredential(ctx context.Context, channelID int) (*CodexCredentialRefreshResult, error) {
	return refreshCodexChannelCredential(ctx, channelID, defaultCodexAdminRuntime())
}

func refreshCodexChannelCredential(
	ctx context.Context,
	channelID int,
	runtime codexAdminRuntime,
) (*CodexCredentialRefreshResult, error) {
	if err := validateCodexAdminRuntime(runtime); err != nil {
		return nil, err
	}
	channel, key, rawKey, err := loadCodexChannelCredential(channelID, false)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key.RefreshToken) == "" {
		return nil, errors.New("codex channel: refresh_token is required to refresh credential")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, codexRefreshTimeout)
	defer cancel()
	token, err := refreshCodexOAuthToken(refreshCtx, runtime, key.RefreshToken)
	if err != nil {
		return nil, err
	}
	now := runtime.now()
	key.AccessToken = token.AccessToken
	key.RefreshToken = token.RefreshToken
	key.LastRefresh = now.Format(time.RFC3339)
	key.Expired = token.ExpiresAt.Format(time.RFC3339)
	if strings.TrimSpace(key.Type) == "" {
		key.Type = "codex"
	}
	if strings.TrimSpace(key.AccountID) == "" {
		if accountID, ok := ExtractCodexAccountIDFromJWT(key.AccessToken); ok {
			key.AccountID = accountID
		}
	}
	if strings.TrimSpace(key.Email) == "" {
		if email, ok := ExtractEmailFromJWT(key.AccessToken); ok {
			key.Email = email
		}
	}
	if err := validateCodexOAuthKey(key); err != nil {
		return nil, err
	}
	if err := persistCodexCredential(channel.Id, rawKey, key); err != nil {
		return nil, err
	}
	return &CodexCredentialRefreshResult{
		ExpiresAt:   key.Expired,
		LastRefresh: key.LastRefresh,
		AccountID:   key.AccountID,
		Email:       key.Email,
		ChannelID:   channel.Id,
		ChannelType: channel.Type,
		ChannelName: channel.Name,
	}, nil
}

func loadCodexChannelCredential(channelID int, requireUsageFields bool) (*model.Channel, *CodexOAuthKey, string, error) {
	if channelID <= 0 {
		return nil, nil, "", newCodexChannelContractError(gorm.ErrRecordNotFound.Error())
	}
	channel, err := GetChannelByID(channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, "", newCodexChannelContractError(gorm.ErrRecordNotFound.Error())
	}
	if err != nil {
		return nil, nil, "", fmt.Errorf("load codex channel: %w", err)
	}
	if channel.Type != int(channelcatalog.ChannelTypeCodex) {
		return nil, nil, "", newCodexChannelContractError("channel type is not Codex")
	}
	if IsMultiKeyChannel(channel) {
		return nil, nil, "", newCodexChannelContractError("multi-key channel is not supported")
	}
	rawKey := channel.Key
	key, err := parseCodexOAuthKey(rawKey)
	if err != nil {
		return nil, nil, "", newCodexChannelContractError("解析凭证失败，请检查渠道配置")
	}
	if requireUsageFields {
		if strings.TrimSpace(key.AccessToken) == "" {
			return nil, nil, "", newCodexChannelContractError("codex channel: access_token is required")
		}
		if strings.TrimSpace(key.AccountID) == "" {
			return nil, nil, "", newCodexChannelContractError("codex channel: account_id is required")
		}
	}
	return channel, key, rawKey, nil
}

func persistCodexCredential(channelID int, previousRaw string, key *CodexOAuthKey) error {
	encoded, err := jsonutil.Marshal(key)
	if err != nil {
		return errors.New("encode codex credential failed")
	}
	if len(encoded) > codexCredentialLimit {
		return errors.New("encoded codex credential is too large")
	}
	result := model.DB.Model(&model.Channel{}).
		Where("id = ? AND key = ?", channelID, previousRaw).
		Update("key", string(encoded))
	if result.Error != nil {
		return fmt.Errorf("persist codex credential: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("persist codex credential: credential changed concurrently")
	}
	return nil
}

func effectiveCodexBaseURL(channel *model.Channel) string {
	baseURL := strings.TrimSpace(channel.BaseURL)
	if baseURL == "" && channel.Type >= 0 && channel.Type < len(channelcatalog.ChannelBaseURLs) {
		baseURL = channelcatalog.ChannelBaseURLs[channel.Type]
	}
	return baseURL
}

func setCodexWhamHeaders(req *http.Request, accessToken, accountID string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("originator", "codex_cli_rs")
}

func fetchCodexWham(
	ctx context.Context,
	client *http.Client,
	baseURL, endpoint, method, accessToken, accountID string,
	body []byte,
) (int, []byte, error) {
	if client == nil {
		return 0, nil, errors.New("codex wham: nil http client")
	}
	accessToken = strings.TrimSpace(accessToken)
	accountID = strings.TrimSpace(accountID)
	if accessToken == "" || len(accessToken) > codexAccessTokenLimit || strings.ContainsAny(accessToken, "\r\n") {
		return 0, nil, errors.New("codex wham: invalid access token")
	}
	if accountID == "" || len(accountID) > codexAccountIDLimit || strings.ContainsAny(accountID, "\r\n") {
		return 0, nil, errors.New("codex wham: invalid account id")
	}
	requestURL, err := codexRequestURL(baseURL, endpoint)
	if err != nil {
		return 0, nil, err
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return 0, nil, errors.New("codex wham: create request failed")
	}
	setCodexWhamHeaders(req, accessToken, accountID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("codex wham request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := httpx.ReadAllLimited(resp.Body, codexWhamResponseLimit)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("codex wham response rejected: %w", err)
	}
	return resp.StatusCode, responseBody, nil
}

// FetchCodexWhamUsage retrieves the Codex usage document.
func FetchCodexWhamUsage(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, accountID string,
) (int, []byte, error) {
	return fetchCodexWham(ctx, client, baseURL, "/backend-api/wham/usage", http.MethodGet, accessToken, accountID, nil)
}

// FetchCodexWhamRateLimitResetCredits retrieves the reset-credit document.
func FetchCodexWhamRateLimitResetCredits(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, accountID string,
) (int, []byte, error) {
	return fetchCodexWham(ctx, client, baseURL, "/backend-api/wham/rate-limit-reset-credits", http.MethodGet, accessToken, accountID, nil)
}

// ConsumeCodexWhamRateLimitResetCredit consumes one reset credit using a fresh
// idempotency identifier generated by this process.
func ConsumeCodexWhamRateLimitResetCredit(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, accountID string,
) (int, []byte, error) {
	requestID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return 0, nil, fmt.Errorf("codex wham: generate reset request id: %w", err)
	}
	return consumeCodexWhamRateLimitResetCredit(ctx, client, baseURL, accessToken, accountID, requestID)
}

func consumeCodexWhamRateLimitResetCredit(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, accountID, requestID string,
) (int, []byte, error) {
	if _, err := uuid.Parse(requestID); err != nil {
		return 0, nil, errors.New("codex wham: invalid reset request id")
	}
	body, err := jsonutil.Marshal(map[string]string{"redeem_request_id": requestID})
	if err != nil {
		return 0, nil, errors.New("codex wham: encode reset request failed")
	}
	return fetchCodexWham(ctx, client, baseURL,
		"/backend-api/wham/rate-limit-reset-credits/consume", http.MethodPost,
		accessToken, accountID, body)
}

type codexWhamOperation int

const (
	codexWhamUsage codexWhamOperation = iota
	codexWhamResetCredits
	codexWhamReset
)

// GetCodexChannelUsage performs the channel-scoped usage request.
func GetCodexChannelUsage(ctx context.Context, channelID int) (*CodexUpstreamResult, error) {
	return getCodexChannelWhamData(ctx, channelID, codexWhamUsage, defaultCodexAdminRuntime())
}

// GetCodexChannelRateLimitResetCredits performs the channel-scoped reset
// credit lookup.
func GetCodexChannelRateLimitResetCredits(ctx context.Context, channelID int) (*CodexUpstreamResult, error) {
	return getCodexChannelWhamData(ctx, channelID, codexWhamResetCredits, defaultCodexAdminRuntime())
}

// ResetCodexChannelUsage consumes a reset credit for the channel.
func ResetCodexChannelUsage(ctx context.Context, channelID int) (*CodexUpstreamResult, error) {
	return getCodexChannelWhamData(ctx, channelID, codexWhamReset, defaultCodexAdminRuntime())
}

func getCodexChannelWhamData(
	ctx context.Context,
	channelID int,
	operation codexWhamOperation,
	runtime codexAdminRuntime,
) (*CodexUpstreamResult, error) {
	if err := validateCodexAdminRuntime(runtime); err != nil {
		return nil, err
	}
	channel, key, rawKey, err := loadCodexChannelCredential(channelID, true)
	if err != nil {
		return nil, err
	}
	fetch := func(requestCtx context.Context, accessToken string) (int, []byte, error) {
		switch operation {
		case codexWhamUsage:
			return FetchCodexWhamUsage(requestCtx, runtime.client, effectiveCodexBaseURL(channel), accessToken, key.AccountID)
		case codexWhamResetCredits:
			return FetchCodexWhamRateLimitResetCredits(requestCtx, runtime.client, effectiveCodexBaseURL(channel), accessToken, key.AccountID)
		case codexWhamReset:
			requestID, err := runtime.requestID()
			if err != nil {
				return 0, nil, fmt.Errorf("codex wham: generate reset request id: %w", err)
			}
			return consumeCodexWhamRateLimitResetCredit(requestCtx, runtime.client, effectiveCodexBaseURL(channel), accessToken, key.AccountID, requestID)
		default:
			return 0, nil, errors.New("codex wham: invalid operation")
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, codexUsageTimeout)
	statusCode, body, err := fetch(fetchCtx, key.AccessToken)
	cancel()
	if err != nil {
		return nil, err
	}
	if (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) && strings.TrimSpace(key.RefreshToken) != "" {
		originalStatus, originalBody := statusCode, body
		refreshCtx, refreshCancel := context.WithTimeout(ctx, codexRefreshTimeout)
		refreshed, refreshErr := refreshCodexOAuthToken(refreshCtx, runtime, key.RefreshToken)
		refreshCancel()
		if refreshErr != nil {
			return &CodexUpstreamResult{StatusCode: originalStatus, Body: originalBody}, nil
		}

		key.AccessToken = refreshed.AccessToken
		key.RefreshToken = refreshed.RefreshToken
		key.LastRefresh = runtime.now().Format(time.RFC3339)
		key.Expired = refreshed.ExpiresAt.Format(time.RFC3339)
		if strings.TrimSpace(key.Type) == "" {
			key.Type = "codex"
		}
		if persistErr := persistCodexCredential(channel.Id, rawKey, key); persistErr != nil {
			// The reference preserves the original upstream result when an
			// opportunistic refresh cannot complete. A failed or racing write
			// must likewise never overwrite newer credentials.
			return &CodexUpstreamResult{StatusCode: originalStatus, Body: originalBody}, nil
		}

		retryCtx, retryCancel := context.WithTimeout(ctx, codexUsageTimeout)
		statusCode, body, err = fetch(retryCtx, key.AccessToken)
		retryCancel()
		if err != nil {
			return nil, err
		}
	}
	return &CodexUpstreamResult{StatusCode: statusCode, Body: body}, nil
}

// ExtractCodexAccountIDFromJWT extracts the ChatGPT account id from the
// unsigned claim payload. This is metadata extraction only; it is never used
// to authenticate a request by itself.
func ExtractCodexAccountIDFromJWT(token string) (string, bool) {
	claims, ok := decodeCodexJWTClaims(token)
	if !ok {
		return "", false
	}
	raw, ok := claims[codexJWTClaimPath]
	if !ok {
		return "", false
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	accountID, ok := object["chatgpt_account_id"].(string)
	accountID = strings.TrimSpace(accountID)
	if !ok || accountID == "" || len(accountID) > codexAccountIDLimit || strings.ContainsAny(accountID, "\r\n") {
		return "", false
	}
	return accountID, true
}

// ExtractEmailFromJWT extracts the optional email metadata claim.
func ExtractEmailFromJWT(token string) (string, bool) {
	claims, ok := decodeCodexJWTClaims(token)
	if !ok {
		return "", false
	}
	email, ok := claims["email"].(string)
	email = strings.TrimSpace(email)
	if !ok || email == "" || len(email) > codexEmailLimit || strings.ContainsAny(email, "\r\n") {
		return "", false
	}
	return email, true
}

func decodeCodexJWTClaims(token string) (map[string]any, bool) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > codexAccessTokenLimit {
		return nil, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > codexAccessTokenLimit {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > codexAccessTokenLimit {
		return nil, false
	}
	var claims map[string]any
	if err := jsonutil.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}
