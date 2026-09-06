package channels

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const codexTestResetID = "11111111-2222-4333-8444-555555555555"

func setupCodexAdminServiceTest(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "codex-admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})

	// SafeDialContext still backs every test client; this explicit development
	// switch is the only way loopback mock servers may be contacted.
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
}

func codexAdminTestClient() *http.Client {
	return &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			DialContext: httpx.SafeDialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func codexAdminTestRuntime(serverURL string, now time.Time) codexAdminRuntime {
	return codexAdminRuntime{
		client:        codexAdminTestClient(),
		oauthTokenURL: serverURL + "/oauth/token",
		now:           func() time.Time { return now },
		requestID:     func() (string, error) { return codexTestResetID, nil },
	}
}

func codexTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := jsonutil.Marshal(claims)
	require.NoError(t, err)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func createCodexAdminChannel(t *testing.T, baseURL, key string) model.Channel {
	t.Helper()
	channel := model.Channel{
		Type:    int(channelcatalog.ChannelTypeCodex),
		Name:    "codex-admin",
		Key:     key,
		BaseURL: baseURL,
		Status:  channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	return channel
}

func TestCodexCredentialRefreshPersistsRotatedTokensWithoutExposure(t *testing.T) {
	setupCodexAdminServiceTest(t)
	fixedNow := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	accessToken := codexTestJWT(t, map[string]any{
		codexJWTClaimPath: map[string]any{"chatgpt_account_id": "acct-derived"},
		"email":           "codex@example.com",
	})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/oauth/token", r.URL.Path)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		assert.Equal(t, "old-refresh-secret", r.Form.Get("refresh_token"))
		assert.Equal(t, codexOAuthClientID, r.Form.Get("client_id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+accessToken+`","refresh_token":"new-refresh-secret","expires_in":3600}`)
	}))
	defer server.Close()

	channel := createCodexAdminChannel(t, server.URL,
		`{"id_token":"id-secret","access_token":"old-access-secret","refresh_token":"old-refresh-secret"}`)
	result, err := refreshCodexChannelCredential(context.Background(), channel.Id, codexAdminTestRuntime(server.URL, fixedNow))
	require.NoError(t, err)
	assert.Equal(t, int64(1), calls.Load())
	assert.Equal(t, channel.Id, result.ChannelID)
	assert.Equal(t, int(channelcatalog.ChannelTypeCodex), result.ChannelType)
	assert.Equal(t, "codex-admin", result.ChannelName)
	assert.Equal(t, "acct-derived", result.AccountID)
	assert.Equal(t, "codex@example.com", result.Email)
	assert.Equal(t, fixedNow.Format(time.RFC3339), result.LastRefresh)
	assert.Equal(t, fixedNow.Add(time.Hour).Format(time.RFC3339), result.ExpiresAt)
	publicJSON, err := jsonutil.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(publicJSON), "old-access-secret")
	assert.NotContains(t, string(publicJSON), "old-refresh-secret")
	assert.NotContains(t, string(publicJSON), "new-refresh-secret")
	assert.NotContains(t, string(publicJSON), "id-secret")

	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	key, err := parseCodexOAuthKey(stored.Key)
	require.NoError(t, err)
	assert.Equal(t, accessToken, key.AccessToken)
	assert.Equal(t, "new-refresh-secret", key.RefreshToken)
	assert.Equal(t, "id-secret", key.IDToken, "unrelated credential fields must be preserved")
	assert.Equal(t, "acct-derived", key.AccountID)
	assert.Equal(t, "codex@example.com", key.Email)
	assert.Equal(t, "codex", key.Type)
}

func TestCodexUsageAutoRefreshRetriesOnceAndPersists(t *testing.T) {
	setupCodexAdminServiceTest(t)
	fixedNow := time.Date(2026, time.September, 5, 13, 0, 0, 0, time.UTC)
	var usageCalls, oauthCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			usageCalls.Add(1)
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "acct-usage", r.Header.Get("chatgpt-account-id"))
			assert.Equal(t, "codex_cli_rs", r.Header.Get("originator"))
			assert.Equal(t, "application/json", r.Header.Get("Accept"))
			switch r.Header.Get("Authorization") {
			case "Bearer expired-access":
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":"expired"}`)
			case "Bearer renewed-access":
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"plan":"pro","remaining":42}`)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/oauth/token":
			oauthCalls.Add(1)
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh-usage", r.Form.Get("refresh_token"))
			_, _ = io.WriteString(w, `{"access_token":"renewed-access","refresh_token":"rotated-refresh","expires_in":7200}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	channel := createCodexAdminChannel(t, server.URL,
		`{"access_token":"expired-access","refresh_token":"refresh-usage","account_id":"acct-usage"}`)
	result, err := getCodexChannelWhamData(context.Background(), channel.Id, codexWhamUsage,
		codexAdminTestRuntime(server.URL, fixedNow))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.JSONEq(t, `{"plan":"pro","remaining":42}`, string(result.Body))
	assert.Equal(t, int64(2), usageCalls.Load())
	assert.Equal(t, int64(1), oauthCalls.Load())

	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	key, err := parseCodexOAuthKey(stored.Key)
	require.NoError(t, err)
	assert.Equal(t, "renewed-access", key.AccessToken)
	assert.Equal(t, "rotated-refresh", key.RefreshToken)
	assert.Equal(t, fixedNow.Format(time.RFC3339), key.LastRefresh)
	assert.Equal(t, fixedNow.Add(2*time.Hour).Format(time.RFC3339), key.Expired)
}

func TestCodexUsageKeepsOriginalUnauthorizedResultWhenRefreshFails(t *testing.T) {
	setupCodexAdminServiceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"expired"}`)
		case "/oauth/token":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"secret":"must-not-surface-as-an-error"}`)
		}
	}))
	defer server.Close()
	channel := createCodexAdminChannel(t, server.URL,
		`{"access_token":"expired-access","refresh_token":"refresh-secret","account_id":"acct-usage"}`)

	result, err := getCodexChannelWhamData(context.Background(), channel.Id, codexWhamUsage,
		codexAdminTestRuntime(server.URL, time.Now()))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
	assert.JSONEq(t, `{"error":"expired"}`, string(result.Body))
}

func TestCodexWhamOperationWireContracts(t *testing.T) {
	setupCodexAdminServiceTest(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		assert.Equal(t, "Bearer access-secret", r.Header.Get("Authorization"))
		assert.Equal(t, "acct-123", r.Header.Get("chatgpt-account-id"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "codex_cli_rs", r.Header.Get("originator"))
		if r.Method == http.MethodPost {
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			body, err := httpx.ReadAllLimited(r.Body, 1024)
			assert.NoError(t, err)
			assert.JSONEq(t, `{"redeem_request_id":"`+codexTestResetID+`"}`, string(body))
		} else {
			assert.Empty(t, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	defer server.Close()
	client := codexAdminTestClient()

	status, body, err := FetchCodexWhamUsage(context.Background(), client, server.URL, "access-secret", "acct-123")
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	assert.JSONEq(t, `{"accepted":true}`, string(body))
	status, _, err = FetchCodexWhamRateLimitResetCredits(context.Background(), client, server.URL, "access-secret", "acct-123")
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	status, _, err = consumeCodexWhamRateLimitResetCredit(context.Background(), client, server.URL,
		"access-secret", "acct-123", codexTestResetID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, status)
	assert.Equal(t, []string{
		"GET /backend-api/wham/usage",
		"GET /backend-api/wham/rate-limit-reset-credits",
		"POST /backend-api/wham/rate-limit-reset-credits/consume",
	}, paths)
}

func TestCodexAdminTransportIsSSRFGuardedBoundedAndNoRedirect(t *testing.T) {
	setupCodexAdminServiceTest(t)
	assert.Equal(t, codexUsageTimeout, codexAdminHTTPClient.Timeout)
	transport, ok := codexAdminHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy, "environment proxies must not receive Codex credentials")
	assert.NotNil(t, transport.DialContext)
	require.NotNil(t, codexAdminHTTPClient.CheckRedirect)
	redirectRequest, err := http.NewRequest(http.MethodGet, "https://example.com/next", nil)
	require.NoError(t, err)
	assert.ErrorIs(t, codexAdminHTTPClient.CheckRedirect(redirectRequest, nil), http.ErrUseLastResponse)

	var leaked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/leak" {
			leaked.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Location", "/leak")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	runtime := codexAdminTestRuntime(server.URL, time.Now())
	runtime.oauthTokenURL = server.URL + "/redirect"
	_, err = refreshCodexOAuthToken(context.Background(), runtime, "refresh-secret")
	require.ErrorContains(t, err, "status=307")
	assert.Equal(t, int64(0), leaked.Load(), "OAuth credentials must never cross a redirect")

	status, _, err := FetchCodexWhamUsage(context.Background(), runtime.client, server.URL,
		"access-secret", "acct-123")
	require.NoError(t, err)
	assert.Equal(t, http.StatusTemporaryRedirect, status)
	assert.Equal(t, int64(0), leaked.Load(), "account credentials must never cross a redirect")
}

func TestCodexAdminRejectsOversizedResponsesAndCredentials(t *testing.T) {
	setupCodexAdminServiceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_, _ = io.WriteString(w, strings.Repeat("x", int(codexOAuthResponseLimit)+1))
			return
		}
		_, _ = io.WriteString(w, strings.Repeat("x", int(codexWhamResponseLimit)+1))
	}))
	defer server.Close()
	runtime := codexAdminTestRuntime(server.URL, time.Now())
	_, err := refreshCodexOAuthToken(context.Background(), runtime, "refresh-secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	_, _, err = FetchCodexWhamUsage(context.Background(), runtime.client, server.URL, "access-secret", "acct-123")
	require.Error(t, err)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)

	_, err = parseCodexOAuthKey(`{"access_token":"` + strings.Repeat("a", codexAccessTokenLimit+1) + `"}`)
	require.ErrorContains(t, err, "access_token is too large")
	_, _, err = FetchCodexWhamUsage(context.Background(), runtime.client, server.URL, "access\nsecret", "acct-123")
	require.ErrorContains(t, err, "invalid access token")
	_, _, err = consumeCodexWhamRateLimitResetCredit(context.Background(), runtime.client, server.URL,
		"access-secret", "acct-123", "not-a-uuid")
	require.ErrorContains(t, err, "invalid reset request id")
}

func TestCodexAdminContextCancellationStopsUpstreamRequest(t *testing.T) {
	setupCodexAdminServiceTest(t)
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, _, err := FetchCodexWhamUsage(ctx, codexAdminTestClient(), server.URL, "access-secret", "acct-123")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-requestStarted:
	default:
		t.Fatal("mock upstream was not reached")
	}
}

func TestCodexAdminValidationAndJWTMetadata(t *testing.T) {
	setupCodexAdminServiceTest(t)
	jwt := codexTestJWT(t, map[string]any{
		codexJWTClaimPath: map[string]any{"chatgpt_account_id": " acct-jwt "},
		"email":           " user@example.com ",
	})
	accountID, ok := ExtractCodexAccountIDFromJWT(jwt)
	assert.True(t, ok)
	assert.Equal(t, "acct-jwt", accountID)
	email, ok := ExtractEmailFromJWT(jwt)
	assert.True(t, ok)
	assert.Equal(t, "user@example.com", email)
	_, ok = ExtractCodexAccountIDFromJWT("not-a-jwt")
	assert.False(t, ok)

	for _, rawURL := range []string{
		"ftp://example.com",
		"http://user:password@example.com",
		"https://example.com?token=secret",
		"https://example.com/#fragment",
		strings.Repeat("x", 4097),
	} {
		_, err := codexRequestURL(rawURL, "/backend-api/wham/usage")
		assert.Error(t, err, rawURL)
	}

	// With SSRF enforcement restored, cleartext external base URLs fail before
	// a request is created. Restore both the environment and derived state.
	t.Setenv("SSRF_DISABLE", "false")
	httpx.InitSSRF()
	_, err := codexRequestURL("http://example.com", "/backend-api/wham/usage")
	assert.ErrorContains(t, err, "must use https")
}

func TestCodexOAuthResponseValidation(t *testing.T) {
	setupCodexAdminServiceTest(t)
	tests := []struct {
		name string
		body string
	}{
		{"missing access", `{"refresh_token":"new","expires_in":3600}`},
		{"missing refresh", `{"access_token":"new","expires_in":3600}`},
		{"zero expiry", `{"access_token":"new","refresh_token":"new","expires_in":0}`},
		{"excessive expiry", `{"access_token":"new","refresh_token":"new","expires_in":31536001}`},
		{"malformed", `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := refreshCodexOAuthToken(context.Background(), codexAdminTestRuntime(server.URL, time.Now()), "refresh")
			assert.Error(t, err)
		})
	}
}

func TestCodexChannelContractErrorsAreSafeAndExact(t *testing.T) {
	setupCodexAdminServiceTest(t)
	nonCodex := model.Channel{Type: int(channelcatalog.ChannelTypeOpenAI), Name: "other", Key: "secret"}
	require.NoError(t, model.DB.Create(&nonCodex).Error)
	multi := createCodexAdminChannel(t, "https://chatgpt.com",
		`{"access_token":"access","refresh_token":"refresh","account_id":"account"}`)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", multi.Id).
		Update("channel_info", `{"is_multi_key":true}`).Error)
	missingAccess := createCodexAdminChannel(t, "https://chatgpt.com", `{"account_id":"account"}`)
	missingAccount := createCodexAdminChannel(t, "https://chatgpt.com", `{"access_token":"access"}`)
	malformed := createCodexAdminChannel(t, "https://chatgpt.com", "refresh-secret-not-json")

	tests := []struct {
		id      int
		message string
	}{
		{999999, "record not found"},
		{nonCodex.Id, "channel type is not Codex"},
		{multi.Id, "multi-key channel is not supported"},
		{missingAccess.Id, "codex channel: access_token is required"},
		{missingAccount.Id, "codex channel: account_id is required"},
		{malformed.Id, "解析凭证失败，请检查渠道配置"},
	}
	for _, test := range tests {
		_, err := getCodexChannelWhamData(context.Background(), test.id, codexWhamUsage, codexAdminTestRuntime("http://unused", time.Now()))
		require.Error(t, err)
		message, ok := CodexChannelContractMessage(err)
		assert.True(t, ok)
		assert.Equal(t, test.message, message)
	}
}

func TestCodexCredentialCompareAndSwapDoesNotOverwriteNewerSecret(t *testing.T) {
	setupCodexAdminServiceTest(t)
	channel := createCodexAdminChannel(t, "https://chatgpt.com",
		`{"access_token":"old-access","refresh_token":"old-refresh","account_id":"account"}`)
	newerRaw := `{"access_token":"newer-access","refresh_token":"newer-refresh","account_id":"account"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("key", newerRaw).Error)
	err := persistCodexCredential(channel.Id, channel.Key, &CodexOAuthKey{
		AccessToken: "stale-access", RefreshToken: "stale-refresh", AccountID: "account",
	})
	require.ErrorContains(t, err, "credential changed concurrently")
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, channel.Id).Error)
	assert.Equal(t, newerRaw, stored.Key)
}

func TestCodexUsageResetFailsClosedWhenEntropyIsUnavailable(t *testing.T) {
	setupCodexAdminServiceTest(t)
	restoreEntropy := cryptoutil.SetSecureRandomReaderForTesting(testutil.EntropyFailureReader{})
	t.Cleanup(restoreEntropy)
	status, body, err := ConsumeCodexWhamRateLimitResetCredit(context.Background(), codexAdminTestClient(),
		"http://127.0.0.1:1", "access-secret", "acct-123")
	require.ErrorIs(t, err, cryptoutil.ErrSecureRandomUnavailable)
	assert.Zero(t, status)
	assert.Nil(t, body)
	assert.NotContains(t, err.Error(), "access-secret")
}

func TestCodexOAuthFormNeverPlacesCredentialInURL(t *testing.T) {
	setupCodexAdminServiceTest(t)
	var requestURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL = r.URL
		_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":60}`)
	}))
	defer server.Close()
	_, err := refreshCodexOAuthToken(context.Background(), codexAdminTestRuntime(server.URL, time.Now()), "refresh-secret")
	require.NoError(t, err)
	require.NotNil(t, requestURL)
	assert.Empty(t, requestURL.RawQuery)
	assert.NotContains(t, requestURL.String(), "refresh-secret")
}

func TestCodexAdminErrorsWrapContextWithoutCredentialValues(t *testing.T) {
	setupCodexAdminServiceTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := FetchCodexWhamUsage(ctx, codexAdminTestClient(), "http://127.0.0.1:1",
		"do-not-log-access", "acct-do-not-log")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "operation was canceled"))
	assert.NotContains(t, err.Error(), "do-not-log-access")
	assert.NotContains(t, err.Error(), "acct-do-not-log")
}
