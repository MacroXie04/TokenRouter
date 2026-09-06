package baidu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	maxBaiduCredentialBytes   = 16 << 10
	maxBaiduAccessTokenBytes  = 16 << 10
	maxBaiduTokenResponseBody = 64 << 10
	defaultTokenRefreshBefore = time.Hour
	maxTokenLifetimeSeconds   = int64((1<<63 - 1) / int64(time.Second))
)

type accessTokenEntry struct {
	token     string
	refreshAt time.Time
	expiresAt time.Time
}

type accessTokenFlight struct {
	done  chan struct{}
	token string
	err   error
}

// accessTokenManager caches OAuth tokens without retaining credentials as map
// keys. A per-credential flight coalesces cold starts and refreshes so a burst
// cannot stampede Baidu's token endpoint.
type accessTokenManager struct {
	mu               sync.Mutex
	entries          map[string]accessTokenEntry
	flights          map[string]*accessTokenFlight
	client           *http.Client
	now              func() time.Time
	maxRefreshBefore time.Duration
}

func newAccessTokenManager(client *http.Client) *accessTokenManager {
	if client == nil {
		client = &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{DialContext: appcommon.SafeDialContext},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &accessTokenManager{
		entries:          make(map[string]accessTokenEntry),
		flights:          make(map[string]*accessTokenFlight),
		client:           client,
		now:              time.Now,
		maxRefreshBefore: defaultTokenRefreshBefore,
	}
}

var defaultAccessTokens = newAccessTokenManager(nil)

func (m *accessTokenManager) token(ctx context.Context, baseURL, credential string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientID, clientSecret, err := parseLegacyCredential(credential)
	if err != nil {
		return "", err
	}
	baseURL, err = validateAndNormalizeBaseURL(baseURL, defaultBaseURL)
	if err != nil {
		return "", err
	}
	cacheKey := tokenCacheKey(baseURL, credential)

	m.mu.Lock()
	now := m.now()
	stale, hasStale := m.entries[cacheKey]
	if hasStale && now.Before(stale.refreshAt) {
		m.mu.Unlock()
		return stale.token, nil
	}
	if flight := m.flights[cacheKey]; flight != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for Baidu access token: %w", ctx.Err())
		case <-flight.done:
			return flight.token, flight.err
		}
	}
	flight := &accessTokenFlight{done: make(chan struct{})}
	m.flights[cacheKey] = flight
	m.mu.Unlock()

	entry, fetchErr := m.fetch(ctx, baseURL, clientID, clientSecret)
	now = m.now()
	if fetchErr != nil && hasStale && now.Before(stale.expiresAt) {
		entry = stale
		fetchErr = nil
	}

	m.mu.Lock()
	if fetchErr == nil {
		m.entries[cacheKey] = entry
		flight.token = entry.token
	} else {
		flight.err = fetchErr
	}
	delete(m.flights, cacheKey)
	close(flight.done)
	m.mu.Unlock()
	return flight.token, flight.err
}

func (m *accessTokenManager) fetch(ctx context.Context, baseURL, clientID, clientSecret string) (accessTokenEntry, error) {
	tokenURL, err := url.Parse(relaycommon.JoinURL(baseURL, "/oauth/2.0/token"))
	if err != nil {
		return accessTokenEntry{}, errors.New("construct Baidu OAuth request")
	}
	query := tokenURL.Query()
	query.Set("grant_type", "client_credentials")
	query.Set("client_id", clientID)
	query.Set("client_secret", clientSecret)
	tokenURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL.String(), nil)
	if err != nil {
		return accessTokenEntry{}, errors.New("construct Baidu OAuth request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		// net/http transport errors include the complete request URL. This URL
		// contains client_secret by provider contract, so never propagate it.
		return accessTokenEntry{}, errors.New("Baidu OAuth request failed")
	}
	defer resp.Body.Close()
	body, err := relaycommon.ReadUpstreamBody(resp.Body, maxBaiduTokenResponseBody)
	if err != nil {
		return accessTokenEntry{}, fmt.Errorf("read Baidu OAuth response: %w", err)
	}
	var envelope struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil {
		return accessTokenEntry{}, errors.New("Baidu OAuth returned invalid JSON")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || envelope.Error != "" {
		code := strings.TrimSpace(envelope.Error)
		if code == "" {
			code = "http_" + fmt.Sprint(resp.StatusCode)
		}
		return accessTokenEntry{}, fmt.Errorf("Baidu OAuth rejected credentials (%s)", safeOAuthCode(code))
	}
	token := strings.TrimSpace(envelope.AccessToken)
	if token == "" || len(token) > maxBaiduAccessTokenBytes || strings.ContainsAny(token, "\r\n") {
		return accessTokenEntry{}, errors.New("Baidu OAuth returned an invalid access token")
	}
	if envelope.ExpiresIn <= 0 || envelope.ExpiresIn > maxTokenLifetimeSeconds {
		return accessTokenEntry{}, errors.New("Baidu OAuth returned an invalid token lifetime")
	}
	now := m.now()
	ttl := time.Duration(envelope.ExpiresIn) * time.Second
	if ttl <= 0 || now.Add(ttl).Before(now) {
		return accessTokenEntry{}, errors.New("Baidu OAuth returned an invalid token lifetime")
	}
	refreshBefore := m.maxRefreshBefore
	if refreshBefore <= 0 {
		refreshBefore = defaultTokenRefreshBefore
	}
	// For unusually short test/proxy lifetimes, keep most of the token usable
	// while still refreshing before hard expiry. Production Baidu tokens use
	// the reference's one-hour early-refresh window.
	if tenth := ttl / 10; tenth < refreshBefore {
		refreshBefore = tenth
	}
	if refreshBefore <= 0 {
		refreshBefore = time.Nanosecond
	}
	return accessTokenEntry{
		token: token, refreshAt: now.Add(ttl - refreshBefore), expiresAt: now.Add(ttl),
	}, nil
}

func parseLegacyCredential(raw string) (string, string, error) {
	if len(raw) == 0 || len(raw) > maxBaiduCredentialBytes || strings.ContainsAny(raw, "\r\n") {
		return "", "", errors.New("Baidu API key must be client_id|client_secret")
	}
	parts := strings.Split(raw, "|")
	if len(parts) != 2 {
		return "", "", errors.New("Baidu API key must be client_id|client_secret")
	}
	clientID, clientSecret := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if clientID == "" || clientSecret == "" {
		return "", "", errors.New("Baidu API key must be client_id|client_secret")
	}
	return clientID, clientSecret, nil
}

func tokenCacheKey(baseURL, credential string) string {
	digest := sha256.Sum256([]byte(baseURL + "\x00" + credential))
	return hex.EncodeToString(digest[:])
}

func safeOAuthCode(code string) string {
	if len(code) > 128 {
		return "oauth_error"
	}
	for _, character := range code {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return "oauth_error"
	}
	return code
}
