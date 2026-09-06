package router_test

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func createCodexRouteChannel(t *testing.T, baseURL, key string) model.Channel {
	t.Helper()
	channel := model.Channel{
		Type:    int(channelcatalog.ChannelTypeCodex),
		Name:    "codex-route",
		Key:     key,
		BaseURL: baseURL,
		Status:  channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	return channel
}

// TestCodexCredentialRefreshRouteContract is the exact-route evidence marker
// for POST /api/channel/:id/codex/refresh. Deep service tests exercise the
// successful OAuth exchange and persistence; this full-router test proves the
// sensitive-write gate and safe failure envelope without contacting OpenAI.
func TestCodexCredentialRefreshRouteContract(t *testing.T) {
	root, admin, plain := setupPermissionTest(t)
	channel := createCodexRouteChannel(t, "https://chatgpt.com",
		`{"access_token":"access-secret-never-return","account_id":"acct-route"}`)
	path := "/api/channel/" + strconv.Itoa(channel.Id) + "/codex/refresh"

	rec := admin.do(http.MethodPost, path, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = plain.do(http.MethodPost, path, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = root.do(http.MethodPost, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "刷新凭证失败，请稍后重试", body["message"])
	assert.NotContains(t, rec.Body.String(), "access-secret-never-return")
	assert.NotContains(t, rec.Body.String(), "account_id")

	rec = root.do(http.MethodPost, "/api/channel/not-an-id/codex/refresh", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], `invalid channel id: strconv.Atoi: parsing "not-an-id"`)
}

// TestCodexUsageRouteContract is the exact-route evidence marker for
// GET /api/channel/:id/codex/usage.
func TestCodexUsageRouteContract(t *testing.T) {
	_, admin, plain := setupPermissionTest(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/backend-api/wham/usage", r.URL.Path)
		assert.Equal(t, "Bearer usage-access", r.Header.Get("Authorization"))
		assert.Equal(t, "acct-usage", r.Header.Get("chatgpt-account-id"))
		assert.Equal(t, "codex_cli_rs", r.Header.Get("originator"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		_, _ = io.WriteString(w, `{"plan_type":"pro","remaining":17}`)
	}))
	defer server.Close()
	channel := createCodexRouteChannel(t, server.URL,
		`{"access_token":"usage-access","refresh_token":"usage-refresh","account_id":"acct-usage"}`)
	path := "/api/channel/" + strconv.Itoa(channel.Id) + "/codex/usage"

	rec := plain.do(http.MethodGet, path, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = admin.do(http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	assert.Equal(t, float64(http.StatusOK), body["upstream_status"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "pro", data["plan_type"])
	assert.Equal(t, float64(17), data["remaining"])
	assert.Equal(t, int64(1), calls.Load())
	assert.NotContains(t, rec.Body.String(), "usage-access")
	assert.NotContains(t, rec.Body.String(), "usage-refresh")

	missing := createCodexRouteChannel(t, server.URL, `{"account_id":"acct-usage"}`)
	rec = admin.do(http.MethodGet, "/api/channel/"+strconv.Itoa(missing.Id)+"/codex/usage", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "codex channel: access_token is required", body["message"])
	assert.Equal(t, int64(1), calls.Load(), "invalid credentials must fail before network access")
}

// TestCodexResetCreditsRouteContract is the exact-route evidence marker for
// GET /api/channel/:id/codex/usage/reset-credits.
func TestCodexResetCreditsRouteContract(t *testing.T) {
	_, admin, plain := setupPermissionTest(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/backend-api/wham/rate-limit-reset-credits", r.URL.Path)
		assert.Equal(t, "Bearer reset-credit-access", r.Header.Get("Authorization"))
		assert.Equal(t, "acct-credit", r.Header.Get("chatgpt-account-id"))
		_, _ = io.WriteString(w, `{"credits":3,"resets_at":1234}`)
	}))
	defer server.Close()
	channel := createCodexRouteChannel(t, server.URL,
		`{"access_token":"reset-credit-access","account_id":"acct-credit"}`)
	path := "/api/channel/" + strconv.Itoa(channel.Id) + "/codex/usage/reset-credits"

	rec := plain.do(http.MethodGet, path, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = admin.do(http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	assert.Equal(t, float64(http.StatusOK), body["upstream_status"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(3), data["credits"])
	assert.Equal(t, int64(1), calls.Load())
}

// TestCodexUsageResetRouteContract is the exact-route evidence marker for
// POST /api/channel/:id/codex/usage/reset.
func TestCodexUsageResetRouteContract(t *testing.T) {
	_, admin, plain := setupPermissionTest(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/backend-api/wham/rate-limit-reset-credits/consume", r.URL.Path)
		assert.Equal(t, "Bearer reset-access", r.Header.Get("Authorization"))
		assert.Equal(t, "acct-reset", r.Header.Get("chatgpt-account-id"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		requestBody, err := httpx.ReadAllLimited(r.Body, 1024)
		assert.NoError(t, err)
		var payload map[string]any
		assert.NoError(t, jsonutil.Unmarshal(requestBody, &payload))
		requestID, ok := payload["redeem_request_id"].(string)
		assert.True(t, ok)
		_, err = uuid.Parse(requestID)
		assert.NoError(t, err)
		_, _ = io.WriteString(w, `{"reset":true}`)
	}))
	defer server.Close()
	channel := createCodexRouteChannel(t, server.URL,
		`{"access_token":"reset-access","account_id":"acct-reset"}`)
	path := "/api/channel/" + strconv.Itoa(channel.Id) + "/codex/usage/reset"

	rec := plain.do(http.MethodPost, path, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = admin.do(http.MethodPost, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	assert.Equal(t, float64(http.StatusOK), body["upstream_status"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, data["reset"])
	assert.Equal(t, int64(1), calls.Load())
}

func TestCodexUsageRoutePreservesBoundedNonJSONUpstreamFailure(t *testing.T) {
	_, admin, _ := setupPermissionTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "upstream unavailable")
	}))
	defer server.Close()
	channel := createCodexRouteChannel(t, server.URL,
		`{"access_token":"failure-access","account_id":"acct-failure"}`)
	rec := admin.do(http.MethodGet,
		"/api/channel/"+strconv.Itoa(channel.Id)+"/codex/usage", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "upstream status: 418", body["message"])
	assert.Equal(t, float64(http.StatusTeapot), body["upstream_status"])
	assert.Equal(t, "upstream unavailable", body["data"])
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "failure-access")
}
