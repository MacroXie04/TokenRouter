package vertex

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	vertexKeyTypeJSON       = "json"
	vertexKeyTypeAPIKey     = "api_key"
	googleOAuthTokenURL     = "https://www.googleapis.com/oauth2/v4/token"
	googleCloudScope        = "https://www.googleapis.com/auth/cloud-platform"
	maxCredentialBytes      = 128 << 10
	maxAccessTokenBytes     = 64 << 10
	maxOAuthResponseBytes   = 1 << 20
	maxTokenCacheEntries    = 1024
	maxOAuthTokenLifetime   = 24 * time.Hour
	defaultOAuthJWTLifetime = 35 * time.Minute
)

// Credentials is the subset of a standard Google service-account document
// needed by Vertex AI. The remaining standard fields are retained so strict
// decoding accepts credentials exported directly by Google Cloud.
type Credentials struct {
	Type                string `json:"type,omitempty"`
	ProjectID           string `json:"project_id"`
	PrivateKeyID        string `json:"private_key_id,omitempty"`
	PrivateKey          string `json:"private_key"`
	ClientEmail         string `json:"client_email"`
	ClientID            string `json:"client_id,omitempty"`
	AuthURI             string `json:"auth_uri,omitempty"`
	TokenURI            string `json:"token_uri,omitempty"`
	AuthProviderCertURL string `json:"auth_provider_x509_cert_url,omitempty"`
	ClientCertURL       string `json:"client_x509_cert_url,omitempty"`
	UniverseDomain      string `json:"universe_domain,omitempty"`
}

type channelSettings struct {
	VertexKeyType string `json:"vertex_key_type,omitempty"`
}

type cachedAccessToken struct {
	value     string
	expiresAt time.Time
}

type tokenCall struct {
	done  chan struct{}
	token cachedAccessToken
	err   error
}

var serviceTokenCache = struct {
	sync.Mutex
	entries  map[[sha256.Size]byte]cachedAccessToken
	inFlight map[[sha256.Size]byte]*tokenCall
}{
	entries:  make(map[[sha256.Size]byte]cachedAccessToken),
	inFlight: make(map[[sha256.Size]byte]*tokenCall),
}

var oauthHTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DialContext: httpx.SafeDialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func vertexKeyType(meta *relaycommon.Meta) (string, error) {
	if meta == nil || meta.Channel == nil {
		return "", errors.New("Vertex AI channel metadata is nil")
	}
	raw := strings.TrimSpace(meta.Channel.OtherSettings)
	if raw == "" {
		return vertexKeyTypeJSON, nil
	}
	if len(raw) > maxCredentialBytes {
		return "", errors.New("Vertex AI channel settings are too large")
	}
	var settings channelSettings
	if err := strictJSON([]byte(raw), &settings, false); err != nil {
		return "", errors.New("Vertex AI channel settings are invalid")
	}
	keyType := strings.TrimSpace(settings.VertexKeyType)
	if keyType == "" {
		keyType = vertexKeyTypeJSON
	}
	if keyType != vertexKeyTypeJSON && keyType != vertexKeyTypeAPIKey {
		return "", errors.New("Vertex AI vertex_key_type must be json or api_key")
	}
	return keyType, nil
}

func parseServiceAccount(raw string) (Credentials, error) {
	var credentials Credentials
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > maxCredentialBytes || !strings.HasPrefix(trimmed, "{") {
		return credentials, errors.New("Vertex AI service-account credential is invalid")
	}
	if err := strictJSON([]byte(trimmed), &credentials, true); err != nil {
		return credentials, errors.New("Vertex AI service-account credential is invalid")
	}
	if err := validateProjectID(credentials.ProjectID); err != nil {
		return credentials, err
	}
	if len(credentials.ClientEmail) == 0 || len(credentials.ClientEmail) > 320 ||
		!strings.Contains(credentials.ClientEmail, "@") || strings.ContainsAny(credentials.ClientEmail, "\r\n\x00") {
		return credentials, errors.New("Vertex AI client_email is invalid")
	}
	if len(credentials.PrivateKey) == 0 || len(credentials.PrivateKey) > 64<<10 ||
		strings.ContainsRune(credentials.PrivateKey, '\x00') {
		return credentials, errors.New("Vertex AI private_key is invalid")
	}
	if len(credentials.PrivateKeyID) > 512 || strings.ContainsAny(credentials.PrivateKeyID, "\r\n\x00") {
		return credentials, errors.New("Vertex AI private_key_id is invalid")
	}
	return credentials, nil
}

func validateAPIKey(raw string) (string, error) {
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "Bearer "))
	if key == "" || len(key) > 16<<10 || strings.ContainsAny(key, "\r\n\x00") {
		return "", errors.New("Vertex AI API key is invalid")
	}
	return key, nil
}

func createSignedJWT(credentials Credentials, now time.Time) (string, error) {
	privateKey, err := parsePrivateKey(credentials.PrivateKey)
	if err != nil {
		return "", err
	}
	claims := jwt.MapClaims{
		"iss":   credentials.ClientEmail,
		"scope": googleCloudScope,
		"aud":   googleOAuthTokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(defaultOAuthJWTLifetime).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if credentials.PrivateKeyID != "" {
		token.Header["kid"] = credentials.PrivateKeyID
	}
	signed, err := token.SignedString(privateKey)
	if err != nil {
		return "", errors.New("sign Vertex AI OAuth assertion")
	}
	return signed, nil
}

func parsePrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, `\n`, "\n")
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("Vertex AI private_key must be a PKCS#8 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("parse Vertex AI private_key")
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok || privateKey.N == nil || privateKey.N.BitLen() < 2048 {
		return nil, errors.New("Vertex AI private_key must be RSA with at least 2048 bits")
	}
	return privateKey, nil
}

func exchangeServiceAccountToken(ctx context.Context, credentials Credentials) (cachedAccessToken, error) {
	return exchangeServiceAccountTokenWithClient(ctx, credentials, googleOAuthTokenURL, oauthHTTPClient, time.Now())
}

func exchangeServiceAccountTokenWithClient(ctx context.Context, credentials Credentials, endpoint string, client *http.Client, now time.Time) (cachedAccessToken, error) {
	var empty cachedAccessToken
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return empty, errors.New("Vertex AI OAuth client is nil")
	}
	assertion, err := createSignedJWT(credentials, now)
	if err != nil {
		return empty, err
	}
	values := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return empty, errors.New("create Vertex AI OAuth request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return empty, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamErrorBodyBytes)
		return empty, fmt.Errorf("Vertex AI OAuth endpoint returned status %d", response.StatusCode)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, maxOAuthResponseBytes)
	if err != nil {
		return empty, fmt.Errorf("read Vertex AI OAuth response: %w", err)
	}
	var envelope struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
		Scope       string `json:"scope,omitempty"`
	}
	if err := strictJSON(body, &envelope, true); err != nil {
		return empty, errors.New("Vertex AI OAuth endpoint returned an invalid response")
	}
	if envelope.AccessToken == "" || len(envelope.AccessToken) > maxAccessTokenBytes ||
		strings.ContainsAny(envelope.AccessToken, "\r\n\x00") {
		return empty, errors.New("Vertex AI OAuth endpoint returned an invalid access token")
	}
	if envelope.TokenType != "" && !strings.EqualFold(envelope.TokenType, "Bearer") {
		return empty, errors.New("Vertex AI OAuth endpoint returned an unsupported token type")
	}
	if envelope.ExpiresIn <= 0 || envelope.ExpiresIn > int64(maxOAuthTokenLifetime/time.Second) {
		return empty, errors.New("Vertex AI OAuth endpoint returned an invalid expiry")
	}
	expiresAt := now.Add(time.Duration(envelope.ExpiresIn) * time.Second)
	return cachedAccessToken{value: envelope.AccessToken, expiresAt: expiresAt}, nil
}

func cachedServiceAccountToken(ctx context.Context, rawCredential string, credentials Credentials) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cacheKey := sha256.Sum256([]byte(rawCredential))
	now := time.Now()
	serviceTokenCache.Lock()
	if token, ok := serviceTokenCache.entries[cacheKey]; ok && tokenUsable(token, now) {
		serviceTokenCache.Unlock()
		return token.value, nil
	}
	delete(serviceTokenCache.entries, cacheKey)
	if call := serviceTokenCache.inFlight[cacheKey]; call != nil {
		done := call.done
		serviceTokenCache.Unlock()
		select {
		case <-done:
			return call.token.value, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if len(serviceTokenCache.inFlight) >= maxTokenCacheEntries {
		serviceTokenCache.Unlock()
		return "", errors.New("Vertex AI OAuth token cache is busy")
	}
	call := &tokenCall{done: make(chan struct{})}
	serviceTokenCache.inFlight[cacheKey] = call
	serviceTokenCache.Unlock()

	token, err := exchangeServiceAccountToken(ctx, credentials)

	serviceTokenCache.Lock()
	call.token, call.err = token, err
	if err == nil {
		if len(serviceTokenCache.entries) >= maxTokenCacheEntries {
			evictOldestTokenEntry(serviceTokenCache.entries)
		}
		serviceTokenCache.entries[cacheKey] = token
	}
	delete(serviceTokenCache.inFlight, cacheKey)
	close(call.done)
	serviceTokenCache.Unlock()
	return token.value, err
}

func tokenUsable(token cachedAccessToken, now time.Time) bool {
	remaining := token.expiresAt.Sub(now)
	skew := time.Minute
	if remaining < 10*time.Minute {
		skew = remaining / 10
	}
	return token.value != "" && remaining > skew && remaining > 0
}

func evictOldestTokenEntry(entries map[[sha256.Size]byte]cachedAccessToken) {
	var oldestKey [sha256.Size]byte
	var oldest time.Time
	set := false
	for key, token := range entries {
		if !set || token.expiresAt.Before(oldest) {
			oldestKey, oldest, set = key, token.expiresAt, true
		}
	}
	if set {
		delete(entries, oldestKey)
	}
}

func addAPIKey(rawURL, key string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("parse Vertex AI request URL")
	}
	query := parsed.Query()
	query.Set("key", key)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func addSSEQuery(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("parse Vertex AI stream URL")
	}
	query := parsed.Query()
	query.Set("alt", "sse")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func sanitizeOAuthError(err error, rawCredential string) error {
	return relaycommon.SanitizeUpstreamError(err, rawCredential)
}
