package relay_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

const tencentIntegrationCredential = "1300000000|AKID-upstream|secret-upstream"

func withTencentPrices(t *testing.T) {
	t.Helper()
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"gpt-provider-contract": {Prompt: 1, Completion: 2},
	})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
}

func configureTencentIntegration(t *testing.T, upstreamURL, credential string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(constant.ChannelTypeTencent), "key": credential,
		"model_mapping": `{"gpt-provider-contract":"hunyuan-pro","text-embedding-3-small":"hunyuan-pro"}`,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, service.InitAbilityCache())
	return key, userID, channel
}

func tencentRelayRequest(t *testing.T, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	return recorder
}

func assertTencentSettlement(t *testing.T, key string, userID int, channel model.Channel, prompt, completion int) {
	t.Helper()
	expectedQuota := service.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var user model.User
	var token model.Token
	var storedChannel model.Channel
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	require.NoError(t, model.DB.First(&storedChannel, channel.Id).Error)
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 500_000-expectedQuota, user.Quota)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 500_000-expectedQuota, token.RemainQuota)
	assert.Equal(t, expectedQuota, token.UsedQuota)
	assert.EqualValues(t, expectedQuota, storedChannel.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Equal(t, prompt, log.PromptTokens)
	assert.Equal(t, completion, log.CompletionTokens)
	assert.Equal(t, expectedQuota, log.Quota)
	assert.NotContains(t, log.Other, "AKID-upstream")
	assert.NotContains(t, log.Other, "secret-upstream")
}

func assertTencentRefund(t *testing.T, key string, userID int) {
	t.Helper()
	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, 500_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Equal(t, 500_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Zero(t, reservation.ActualQuota)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).Count(&logs).Error)
	assert.Zero(t, logs)
}

func TestTencentNativeWireResponseAndSettlementEndToEnd(t *testing.T) {
	withTencentPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/", request.URL.Path)
		assert.Equal(t, "ChatCompletions", request.Header.Get("X-TC-Action"))
		assert.Equal(t, "2023-09-01", request.Header.Get("X-TC-Version"))
		assert.NotEmpty(t, request.Header.Get("X-TC-Timestamp"))
		assert.Contains(t, request.Header.Get("Authorization"), "Credential=AKID-upstream/")
		assert.NotContains(t, request.Header.Get("Authorization"), "secret-upstream")
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "hunyuan-pro", body["Model"])
		assert.Equal(t, false, body["Stream"])
		messages := body["Messages"].([]any)
		require.Len(t, messages, 1)
		assert.Equal(t, "user", messages[0].(map[string]any)["Role"])
		assert.Equal(t, "hello", messages[0].(map[string]any)["Content"])
		_, _ = io.WriteString(writer, `{"Response":{"Choices":[{"FinishReason":"stop","Message":{"Role":"assistant","Content":"Hunyuan answer"}}],"Created":1700000000,"Id":"hy-1","Usage":{"PromptTokens":7,"CompletionTokens":3,"TotalTokens":10}}}`)
	}))
	defer upstream.Close()

	key, userID, channel := configureTencentIntegration(t, upstream.URL, tencentIntegrationCredential)
	response := tencentRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, calls.Load())
	assert.Contains(t, response.Body.String(), `"content":"Hunyuan answer"`)
	assert.Contains(t, response.Body.String(), `"model":"gpt-provider-contract"`)
	assertTencentSettlement(t, key, userID, channel, 7, 3)
}

func TestTencentNativeStreamWireAndSettlementEndToEnd(t *testing.T) {
	withTencentPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, true, body["Stream"])
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"Choices\":[{\"Delta\":{\"Role\":\"assistant\",\"Content\":\"streamed \"}}],\"Created\":1700000000,\"Id\":\"hy-stream\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"Choices\":[{\"FinishReason\":\"stop\",\"Delta\":{\"Content\":\"answer\"}}],\"Created\":1700000000,\"Id\":\"hy-stream\",\"Usage\":{\"PromptTokens\":6,\"CompletionTokens\":2,\"TotalTokens\":8}}\n\n")
	}))
	defer upstream.Close()

	key, userID, channel := configureTencentIntegration(t, upstream.URL, tencentIntegrationCredential)
	response := tencentRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	assert.Contains(t, response.Body.String(), `"content":"streamed "`)
	assert.Contains(t, response.Body.String(), `"content":"answer"`)
	assert.Equal(t, 1, strings.Count(response.Body.String(), "data: [DONE]"))
	assertTencentSettlement(t, key, userID, channel, 6, 2)
}

func TestTencentTokenHubDispatchPreservesCompatibleWireAndSettlement(t *testing.T) {
	withTencentPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/v1/chat/completions", request.URL.Path)
		assert.Equal(t, "Bearer tokenhub-upstream", request.Header.Get("Authorization"))
		assert.Empty(t, request.Header.Get("X-TC-Action"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "hunyuan-pro", body["model"])
		_, _ = io.WriteString(writer, `{"id":"tokenhub-1","object":"chat.completion","created":1700000000,"model":"hunyuan-pro","choices":[{"index":0,"message":{"role":"assistant","content":"TokenHub answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer upstream.Close()

	key, userID, channel := configureTencentIntegration(t, upstream.URL, "tokenhub-upstream")
	response := tencentRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, calls.Load())
	assert.Contains(t, response.Body.String(), "TokenHub answer")
	assertTencentSettlement(t, key, userID, channel, 5, 2)
}

func TestTencentDefinitiveRejectionsRefundAndRedactEndToEnd(t *testing.T) {
	withTencentPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP rejection", http.StatusTooManyRequests, `{"error":{"message":"secret-upstream rate limited"}}`},
		{"native business rejection", http.StatusOK, `{"Response":{"Error":{"Code":4001,"Message":"AKID-upstream secret-upstream rejected"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer upstream.Close()
			key, userID, _ := configureTencentIntegration(t, upstream.URL, tencentIntegrationCredential)
			response := tencentRelayRequest(t, key, "/v1/chat/completions",
				`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`)
			assert.GreaterOrEqual(t, response.Code, http.StatusBadRequest, response.Body.String())
			assert.NotContains(t, response.Body.String(), "AKID-upstream")
			assert.NotContains(t, response.Body.String(), "secret-upstream")
			assert.Contains(t, response.Body.String(), "[REDACTED]")
			assertTencentRefund(t, key, userID)
		})
	}
}

func TestTencentAmbiguousAcceptedResponseSettlesWithoutRetryEndToEnd(t *testing.T) {
	withTencentPrices(t)
	t.Setenv("RETRY_TIMES", "3")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, `{"Response":`)
	}))
	defer upstream.Close()
	key, userID, channel := configureTencentIntegration(t, upstream.URL, tencentIntegrationCredential)
	response := tencentRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"accepted work"}]}`)
	assert.GreaterOrEqual(t, response.Code, http.StatusBadRequest, response.Body.String())
	assert.EqualValues(t, 1, calls.Load(), "an accepted ambiguous request must never be replayed")

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
	assert.Positive(t, log.PromptTokens)
	assert.Zero(t, log.CompletionTokens)
	assert.Positive(t, log.Quota)
	assert.Equal(t, channel.Id, log.ChannelId)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, log.Quota, reservation.ActualQuota)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500_000-log.Quota, user.Quota)
}

func TestTencentUnsupportedModeFailsBeforeCredentialedContact(t *testing.T) {
	withTencentPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	key, userID, _ := configureTencentIntegration(t, upstream.URL, tencentIntegrationCredential)
	response := tencentRelayRequest(t, key, "/v1/embeddings",
		`{"model":"text-embedding-3-small","input":"hello"}`)
	assert.GreaterOrEqual(t, response.Code, http.StatusBadRequest, response.Body.String())
	assert.Zero(t, calls.Load())
	assertTencentRefund(t, key, userID)
}
