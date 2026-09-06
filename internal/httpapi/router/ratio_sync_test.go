package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func ratioSyncChannels(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	body := decodeBody(t, recorder)
	require.Equal(t, true, body["success"], recorder.Body.String())
	require.Equal(t, "", body["message"])
	items, ok := body["data"].([]any)
	require.True(t, ok, recorder.Body.String())
	channels := make([]map[string]any, 0, len(items))
	for _, item := range items {
		channel, ok := item.(map[string]any)
		require.True(t, ok)
		channels = append(channels, channel)
	}
	return channels
}

func findRatioSyncChannel(t *testing.T, channels []map[string]any, id int) map[string]any {
	t.Helper()
	for _, channel := range channels {
		if int(channel["id"].(float64)) == id {
			return channel
		}
	}
	t.Fatalf("ratio sync channel %d not found in %#v", id, channels)
	return nil
}

func TestRatioSyncChannelsContractAndRootAuthorization(t *testing.T) {
	handler, do, _ := setupChannelRead(t, roles.RoleRootUser)
	highPriority := int64(10)
	explicit := model.Channel{
		Type: int(channelcatalog.ChannelTypeCustom), Key: "channel-secret-explicit", Name: "explicit",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://ratios.example/api", Priority: &highPriority,
	}
	require.NoError(t, model.DB.Create(&explicit).Error)
	defaultURL := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), Key: "channel-secret-default", Name: "default-url",
		Status: channelcatalog.ChannelStatusManuallyDisabled,
	}
	require.NoError(t, model.DB.Create(&defaultURL).Error)
	withoutURL := model.Channel{
		Type: int(channelcatalog.ChannelTypeCustom), Key: "channel-secret-hidden", Name: "no-url",
		Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&withoutURL).Error)
	credentialedURL := model.Channel{
		Type: int(channelcatalog.ChannelTypeCustom), Key: "another-channel-secret", Name: "unsafe-url",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://user:embedded-url-secret@example.com",
	}
	require.NoError(t, model.DB.Create(&credentialedURL).Error)

	recorder := do(http.MethodGet, "/api/ratio_sync/channels", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	channels := ratioSyncChannels(t, recorder)
	require.Len(t, channels, 4)
	assert.Equal(t, "https://ratios.example/api", findRatioSyncChannel(t, channels, explicit.Id)["base_url"])
	defaultItem := findRatioSyncChannel(t, channels, defaultURL.Id)
	assert.Equal(t, "https://api.openai.com", defaultItem["base_url"])
	assert.EqualValues(t, channelcatalog.ChannelStatusManuallyDisabled, defaultItem["status"])
	assert.Equal(t, "官方倍率预设", findRatioSyncChannel(t, channels, -100)["name"])
	assert.Equal(t, "https://models.dev", findRatioSyncChannel(t, channels, -101)["base_url"])
	assert.NotContains(t, recorder.Body.String(), "channel-secret")
	assert.NotContains(t, recorder.Body.String(), "no-url")
	assert.NotContains(t, recorder.Body.String(), "embedded-url-secret")

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/ratio_sync/channels", nil))
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	_, adminDo, _ := setupChannelRead(t, roles.RoleAdminUser)
	assert.Equal(t, http.StatusForbidden, adminDo(http.MethodGet, "/api/ratio_sync/channels", "").Code)
}

func TestRatioSyncFetchContractWithStoredChannelIDs(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"same-model": {Prompt: 2, Completion: 6},
	})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/pricing", request.URL.Path)
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"success":true,
			"data":[
				{"model_name":"same-model","quota_type":0,"model_ratio":1,"completion_ratio":3},
				{"model_name":"different-model","quota_type":0,"model_ratio":4,"completion_ratio":2}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeCustom), Key: "must-not-be-sent", Name: "stored-source",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: server.URL,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	recorder := do(http.MethodPost, "/api/ratio_sync/fetch", fmt.Sprintf(`{"channel_ids":[%d],"timeout":2}`, channel.Id))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	require.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	results := data["test_results"].([]any)
	require.Len(t, results, 1)
	result := results[0].(map[string]any)
	assert.Equal(t, fmt.Sprintf("stored-source(%d)", channel.Id), result["name"])
	assert.Equal(t, "success", result["status"])
	differences := data["differences"].(map[string]any)
	assert.NotContains(t, differences, "same-model")
	require.Contains(t, differences, "different-model")
	assert.NotContains(t, recorder.Body.String(), "must-not-be-sent")
}

func TestRatioSyncOpenRouterCredentialIsBoundToStoredDestination(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })

	const credential = "openrouter-secret-token"
	var attackerCalls atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attackerCalls.Add(1)
	}))
	t.Cleanup(attacker.Close)

	var storedCalls atomic.Int32
	stored := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		storedCalls.Add(1)
		assert.Equal(t, "/v1/models", request.URL.Path)
		assert.Equal(t, "Bearer "+credential, request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"openrouter/model","pricing":{"prompt":"0.000002","completion":"0.000006"}}]}`))
	}))
	t.Cleanup(stored.Close)

	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenRouter), Key: credential, Name: "stored-openrouter",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: stored.URL,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	payload := fmt.Sprintf(`{
		"upstreams":[{
			"id":%d,
			"name":"spoofed-name",
			"base_url":%q,
			"endpoint":"openrouter"
		}],
		"timeout":2
	}`, channel.Id, attacker.URL)
	recorder := do(http.MethodPost, "/api/ratio_sync/fetch", payload)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	require.Equal(t, true, body["success"])
	results := body["data"].(map[string]any)["test_results"].([]any)
	require.Len(t, results, 1)
	assert.Equal(t, fmt.Sprintf("stored-openrouter(%d)", channel.Id), results[0].(map[string]any)["name"])
	assert.Equal(t, "success", results[0].(map[string]any)["status"])
	assert.EqualValues(t, 1, storedCalls.Load())
	assert.Zero(t, attackerCalls.Load())
	assert.NotContains(t, recorder.Body.String(), credential)
}

func TestRatioSyncOpenRouterCredentialIsNotForwardedAcrossRedirect(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	const credential = "redirect-secret-token"
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	t.Cleanup(destination.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenRouter), Key: credential, Name: "redirecting-openrouter",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: redirector.URL,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	payload := fmt.Sprintf(`{"upstreams":[{"id":%d,"name":"x","base_url":%q,"endpoint":"openrouter"}]}`,
		channel.Id, redirector.URL)
	recorder := do(http.MethodPost, "/api/ratio_sync/fetch", payload)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	require.Equal(t, true, body["success"])
	result := body["data"].(map[string]any)["test_results"].([]any)[0].(map[string]any)
	assert.Equal(t, "error", result["status"])
	assert.Equal(t, "upstream returned HTTP 307", result["error"])
	assert.Zero(t, destinationCalls.Load())
	assert.NotContains(t, recorder.Body.String(), credential)
}

func TestRatioSyncFetchValidationFailureAndRoles(t *testing.T) {
	handler, do, _ := setupChannelRead(t, roles.RoleRootUser)

	recorder := do(http.MethodPost, "/api/ratio_sync/fetch", "{")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "请求参数格式错误", decodeBody(t, recorder)["message"])

	recorder = do(http.MethodPost, "/api/ratio_sync/fetch", `{}`)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, false, decodeBody(t, recorder)["success"])
	assert.Equal(t, "无有效上游渠道", decodeBody(t, recorder)["message"])

	const secret = "url-password-secret"
	recorder = do(http.MethodPost, "/api/ratio_sync/fetch", `{
		"upstreams":[{"name":"bad","base_url":"http://user:`+secret+`@example.com","endpoint":"/pricing"}]
	}`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "请求参数无效", decodeBody(t, recorder)["message"])
	assert.NotContains(t, recorder.Body.String(), secret)

	unauthenticated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/ratio_sync/fetch", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(unauthenticated, request)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	_, adminDo, _ := setupChannelRead(t, roles.RoleAdminUser)
	assert.Equal(t, http.StatusForbidden, adminDo(http.MethodPost, "/api/ratio_sync/fetch", `{}`).Code)
}

func TestRatioSyncUpstreamFailureDoesNotReflectResponseBody(t *testing.T) {
	_, do, _ := setupChannelRead(t, roles.RoleRootUser)
	const secret = "upstream-error-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(secret))
	}))
	t.Cleanup(server.Close)
	payload, err := jsonutil.Marshal(map[string]any{
		"upstreams": []map[string]any{{"name": "broken", "base_url": server.URL, "endpoint": "/pricing"}},
	})
	require.NoError(t, err)
	recorder := do(http.MethodPost, "/api/ratio_sync/fetch", string(payload))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	result := body["data"].(map[string]any)["test_results"].([]any)[0].(map[string]any)
	assert.Equal(t, "error", result["status"])
	assert.Equal(t, "upstream returned HTTP 500", result["error"])
	assert.NotContains(t, recorder.Body.String(), secret)
}
