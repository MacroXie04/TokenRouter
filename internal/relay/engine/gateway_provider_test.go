package engine_test

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gatewayChannelTypes() []channelcatalog.ChannelType {
	return []channelcatalog.ChannelType{channelcatalog.ChannelTypeSub2API, channelcatalog.ChannelTypeNewAPI}
}

func TestGatewayProvidersOpenAIChatLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, channelType := range gatewayChannelTypes() {
		t.Run(channelcatalog.ChannelTypeName(channelType), func(t *testing.T) {
			var gotPath, gotAuthorization string
			var gotBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotPath = request.URL.Path
				gotAuthorization = request.Header.Get("Authorization")
				require.NoError(t, json.NewDecoder(request.Body).Decode(&gotBody))
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"id":"chatcmpl-gateway","object":"chat.completion","model":"gateway-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"gateway reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`))
			}))
			defer upstream.Close()

			key := setupChannelIntegration(t, upstream.URL+"/proxy", channelType, "gateway-client")
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
				"key": "gateway-upstream-key", "model_mapping": `{"gateway-client":"gateway-upstream"}`,
			}).Error)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"gateway-client","messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "/proxy/v1/chat/completions", gotPath)
			assert.Equal(t, "Bearer gateway-upstream-key", gotAuthorization)
			assert.Equal(t, "gateway-upstream", gotBody["model"])
			assert.Contains(t, recorder.Body.String(), "gateway reply")
			assertGatewaySettled(t, 7, 3)
		})
	}
}

func TestGatewayProvidersResponsesCompactLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, channelType := range gatewayChannelTypes() {
		t.Run(channelcatalog.ChannelTypeName(channelType), func(t *testing.T) {
			var gotPath, gotAuthorization string
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotPath = request.URL.Path
				gotAuthorization = request.Header.Get("Authorization")
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				assert.Equal(t, "resp_previous", body["previous_response_id"])
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"id":"cmpt-gateway","object":"responses.compaction","output":[],"usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}`))
			}))
			defer upstream.Close()

			key := setupChannelIntegration(t, upstream.URL, channelType, "gateway-model")
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Update("key", "gateway-key").Error)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses/compact",
				strings.NewReader(`{"model":"gateway-model","previous_response_id":"resp_previous","input":"compact this"}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "/v1/responses/compact", gotPath)
			assert.Equal(t, "Bearer gateway-key", gotAuthorization)
			assert.Contains(t, recorder.Body.String(), "responses.compaction")
			assertGatewaySettled(t, 11, 2)
		})
	}
}

func TestGatewayProvidersClaudeNativeLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, channelType := range gatewayChannelTypes() {
		t.Run(channelcatalog.ChannelTypeName(channelType), func(t *testing.T) {
			var gotPath, gotBearer, gotAPIKey, gotVersion string
			var gotBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotPath = request.URL.Path
				gotBearer = request.Header.Get("Authorization")
				gotAPIKey = request.Header.Get("x-api-key")
				gotVersion = request.Header.Get("anthropic-version")
				require.NoError(t, json.NewDecoder(request.Body).Decode(&gotBody))
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"id":"msg_gateway","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"native gateway reply"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":4}}`))
			}))
			defer upstream.Close()

			key, userID := setupClaudeRelay(t, upstream.URL, int(channelType), "claude-client")
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
				"key": "gateway-claude-key", "model_mapping": `{"claude-client":"claude-upstream"}`,
			}).Error)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages",
				strings.NewReader(`{"model":"claude-client","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("anthropic-version", "2024-01-01")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "/v1/messages", gotPath)
			assert.Equal(t, "Bearer gateway-claude-key", gotBearer)
			assert.Equal(t, "gateway-claude-key", gotAPIKey)
			assert.Equal(t, "2024-01-01", gotVersion)
			assert.Equal(t, "claude-upstream", gotBody["model"])
			assert.Equal(t, float64(64), gotBody["max_tokens"])
			assert.NotContains(t, gotBody, "max_completion_tokens", "Claude must not be converted to OpenAI")
			assert.Contains(t, recorder.Body.String(), "native gateway reply")

			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			assert.Less(t, user.Quota, 500000)
			assertGatewaySettled(t, 9, 4)
		})
	}
}

func TestGatewayProvidersGeminiNativeLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for index, channelType := range gatewayChannelTypes() {
		t.Run(channelcatalog.ChannelTypeName(channelType), func(t *testing.T) {
			version := "v1beta"
			if index == 1 {
				version = "v1"
			}
			var gotURI, gotBearer, gotAPIKey string
			var gotBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotURI = request.URL.RequestURI()
				gotBearer = request.Header.Get("Authorization")
				gotAPIKey = request.Header.Get("x-goog-api-key")
				require.NoError(t, json.NewDecoder(request.Body).Decode(&gotBody))
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"native Gemini gateway reply"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3,"totalTokenCount":11}}`))
			}))
			defer upstream.Close()

			key := setupChannelIntegration(t, upstream.URL, channelType, "gemini-client")
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
				"key": "gateway-gemini-key", "model_mapping": `{"gemini-client":"gemini-upstream"}`,
			}).Error)
			request := httptest.NewRequest(http.MethodPost, "/"+version+"/models/gemini-client:generateContent",
				strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"temperature":0.25}}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, "/"+version+"/models/gemini-client:generateContent", gotURI)
			assert.Equal(t, "Bearer gateway-gemini-key", gotBearer)
			assert.Equal(t, "gateway-gemini-key", gotAPIKey)
			assert.Contains(t, gotBody, "contents")
			assert.NotContains(t, gotBody, "messages", "Gemini must not be converted to OpenAI")
			assert.Contains(t, recorder.Body.String(), "native Gemini gateway reply")
			assertGatewaySettled(t, 8, 3)
		})
	}
}

func TestNewAPIGatewayGeminiStreamPreservesURIAndSettles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotURI, gotBearer, gotAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotURI = request.URL.RequestURI()
		gotBearer = request.Header.Get("Authorization")
		gotAPIKey = request.Header.Get("x-goog-api-key")
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":2,\"totalTokenCount\":8}}\n\n"))
	}))
	defer upstream.Close()

	key := setupChannelIntegration(t, upstream.URL, channelcatalog.ChannelTypeNewAPI, "gemini-stream")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Update("key", "gateway-stream-key").Error)
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-stream:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "/v1beta/models/gemini-stream:streamGenerateContent?alt=sse", gotURI)
	assert.Equal(t, "Bearer gateway-stream-key", gotBearer)
	assert.Equal(t, "gateway-stream-key", gotAPIKey)
	assert.Contains(t, recorder.Body.String(), `"candidates"`)
	assert.NotContains(t, recorder.Body.String(), `"choices"`)
	assertGatewaySettled(t, 6, 2)
}

func TestNewAPIGatewayAcceptedMalformedResponseSettlesWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"claude-malformed": {Prompt: 2, Completion: 4}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`not-json`))
	}))
	defer upstream.Close()

	key, _ := setupClaudeRelay(t, upstream.URL, int(channelcatalog.ChannelTypeNewAPI), "claude-malformed")
	request := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-malformed","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)

	assert.NotEqual(t, http.StatusOK, recorder.Code)
	assert.Equal(t, 1, calls, "accepted malformed work must not be replayed")
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Greater(t, reservation.ActualQuota, 0)
}

func TestNewAPIGatewayDefinitiveRejectionRefunds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"invalid native request","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	key := setupChannelIntegration(t, upstream.URL, channelcatalog.ChannelTypeNewAPI, "gemini-rejected")
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-rejected:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	assert.Zero(t, reservation.ActualQuota)
}

func assertGatewaySettled(t *testing.T, promptTokens, completionTokens int) {
	t.Helper()
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Greater(t, reservation.ActualQuota, 0)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, promptTokens, log.PromptTokens)
	assert.Equal(t, completionTokens, log.CompletionTokens)
}
