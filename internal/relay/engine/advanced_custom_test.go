package engine_test

import (
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdvancedCustomOpenAIWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- providerWireObservation{
			Path: r.URL.Path, RawQuery: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"),
			APIKey: r.Header.Get("X-Route-Key"), Title: r.Header.Get("X-Client-Trace"), Body: decoded,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"advanced-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL+"/gateway")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat/{model}?fixed=1","models":["gpt-provider-contract"],"auth":{"type":"header","name":"X-Route-Key","value":"route-{api_key}"}}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping":   `{"gpt-provider-contract":"provider-model"}`,
		"header_override": `{"X-Client-Trace":"{client_header:X-Trace}","Authorization":""}`,
	}).Error)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}],"provider_extension":"kept","group":"local"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Trace", "trace-99")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	wire := <-observed
	assert.Equal(t, "/gateway/chat/provider-model", wire.Path)
	assert.Equal(t, "fixed=1", wire.RawQuery)
	assert.Equal(t, "route-sk-upstream", wire.APIKey)
	assert.Empty(t, wire.Authorization, "the relay token must not cross the upstream trust boundary")
	assert.Equal(t, "trace-99", wire.Title)
	assert.Equal(t, "provider-model", wire.Body["model"])
	assert.Equal(t, "kept", wire.Body["provider_extension"])
	assert.NotContains(t, wire.Body, "group")

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 4, log.PromptTokens)
	assert.Equal(t, 6, log.CompletionTokens)
	assert.Greater(t, log.Quota, 0)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, log.Quota, reservation.ActualQuota)
}

func TestAdvancedCustomEmbeddingWireUsageAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"text-embedding-3-small": {Prompt: 1}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		observed <- providerWireObservation{
			Path: r.URL.Path, RawQuery: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"), Body: decoded,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"provider-embedding","usage":{"prompt_tokens":4,"total_tokens":4}}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL+"/gateway")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/embeddings","upstream_path":"/embed/{model}?version=1","auth":{"type":"query","name":"key","value":"{api_key}"}}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping": `{"text-embedding-3-small":"provider-embedding"}`,
	}).Error)

	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"text-embedding-3-small","input":"hello world"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"embedding"`)

	wire := <-observed
	assert.Equal(t, "/gateway/embed/provider-embedding", wire.Path)
	assert.Equal(t, "key=sk-upstream&version=1", wire.RawQuery)
	assert.Empty(t, wire.Authorization)
	assert.Equal(t, "provider-embedding", wire.Body["model"])
	assert.Equal(t, "hello world", wire.Body["input"])

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 4, log.PromptTokens)
	assert.Zero(t, log.CompletionTokens)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, log.Quota, reservation.ActualQuota)
}

func TestAdvancedCustomErrorIsMappedSanitizedAndRefunded(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/configured/chat", r.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"advanced provider busy for sk-upstream","type":"rate_limit_error"}}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":                int(channelcatalog.ChannelTypeAdvancedCustom),
		"settings":            `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/configured/chat"}]}}`,
		"status_code_mapping": `{"429":503}`,
	}).Error)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "advanced provider busy")
	assert.NotContains(t, recorder.Body.String(), "sk-upstream")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestAdvancedCustomClaudeToOpenAIWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- providerWireObservation{Path: r.URL.Path, RawQuery: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"), Body: decoded}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-claude-converted","object":"chat.completion","model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"converted"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL+"/root")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/messages","upstream_path":"/chat/{model}?tenant=one","converter":"anthropic_messages_to_openai_chat_completions"}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
	}).Error)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-provider-contract","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"type":"message"`)
	assert.Contains(t, recorder.Body.String(), "converted")

	wire := <-observed
	assert.Equal(t, "/root/chat/provider-model", wire.Path)
	assert.Equal(t, "tenant=one", wire.RawQuery)
	assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
	assert.Equal(t, "provider-model", wire.Body["model"])
	messages, ok := wire.Body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].(map[string]any)["role"])

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 5, log.PromptTokens)
	assert.Equal(t, 3, log.CompletionTokens)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}

func TestAdvancedCustomGeminiToOpenAIStreamWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- providerWireObservation{Path: r.URL.Path, RawQuery: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"), Body: decoded}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"id":"chat-gemini-converted","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"streamed"}}]}`,
			`data: {"id":"chat-gemini-converted","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`,
			`data: [DONE]`, "",
		}, "\n\n"))
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1beta/models/{model}:generateContent","upstream_path":"/openai/{model}","converter":"gemini_generate_content_to_openai_chat_completions","auth":{"type":"query","name":"api_key","value":"{api_key}"}}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
	}).Error)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-provider-contract:streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "streamed")
	assert.Contains(t, recorder.Body.String(), `"finishReason":"STOP"`)

	wire := <-observed
	assert.Equal(t, "/openai/provider-model", wire.Path)
	assert.Equal(t, "api_key=sk-upstream", wire.RawQuery)
	assert.Empty(t, wire.Authorization)
	assert.Equal(t, "provider-model", wire.Body["model"])
	assert.Equal(t, true, wire.Body["stream"])

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 7, log.PromptTokens)
	assert.Equal(t, 4, log.CompletionTokens)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}

func TestAdvancedCustomChatToResponsesWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- providerWireObservation{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: decoded}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp-advanced","object":"response","status":"completed","model":"provider-model",
			"output":[
				{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
				{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}
			],
			"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}
		}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/responses","converter":"openai_chat_completions_to_openai_responses"}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
	}).Error)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-provider-contract","messages":[
			{"role":"system","content":"rules"},{"role":"user","content":"hello"}
		],"max_completion_tokens":64
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"object":"chat.completion"`)
	assert.Contains(t, recorder.Body.String(), `"content":"answer"`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)

	wire := <-observed
	assert.Equal(t, "/responses", wire.Path)
	assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
	assert.Equal(t, "provider-model", wire.Body["model"])
	assert.Equal(t, "rules", wire.Body["instructions"])
	assert.Equal(t, float64(64), wire.Body["max_output_tokens"])
	input, ok := wire.Body["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	assert.Equal(t, "user", input[0].(map[string]any)["role"])

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 9, log.PromptTokens)
	assert.Equal(t, 4, log.CompletionTokens)
}

func TestAdvancedCustomResponsesToChatStreamWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- providerWireObservation{Path: r.URL.Path, Body: decoded}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"id":"chat-stream","object":"chat.completion.chunk","model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`data: {"id":"chat-stream","object":"chat.completion.chunk","model":"provider-model","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
			`data: {"id":"chat-stream","object":"chat.completion.chunk","model":"provider-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`,
			`data: [DONE]`, "",
		}, "\n\n"))
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/responses","upstream_path":"/chat","converter":"openai_responses_to_openai_chat_completions"}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
	}).Error)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-provider-contract","instructions":"rules","input":"hello","stream":true,"max_output_tokens":64
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "event: response.created")
	assert.Contains(t, recorder.Body.String(), `"type":"response.output_text.delta"`)
	assert.Contains(t, recorder.Body.String(), `"delta":"hello"`)
	assert.Contains(t, recorder.Body.String(), "event: response.completed")
	assert.Contains(t, recorder.Body.String(), `"input_tokens":8`)

	wire := <-observed
	assert.Equal(t, "/chat", wire.Path)
	assert.Equal(t, "provider-model", wire.Body["model"])
	assert.Equal(t, true, wire.Body["stream"])
	assert.Equal(t, float64(64), wire.Body["max_completion_tokens"])
	messages := wire.Body["messages"].([]any)
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].(map[string]any)["role"])
	assert.Equal(t, "user", messages[1].(map[string]any)["role"])

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 8, log.PromptTokens)
	assert.Equal(t, 2, log.CompletionTokens)
}

func TestAdvancedCustomResponsesToGeminiWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	observed := make(chan providerWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- providerWireObservation{Path: r.URL.Path, RawQuery: r.URL.RawQuery, APIKey: r.Header.Get("X-Gemini-Key"), Body: decoded}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"gemini answer"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":3,"totalTokenCount":9}
		}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	settings := `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/responses","upstream_path":"/v1beta/models/{model}:generateContent","converter":"openai_responses_to_gemini_generate_content","auth":{"type":"header","name":"X-Gemini-Key","value":"{api_key}"}}]}}`
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
		"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
	}).Error)

	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-provider-contract","instructions":"rules","input":[
			{"role":"user","content":"hello"},
			{"type":"function_call","call_id":"call-1","name":"lookup","arguments":{"q":"x"}},
			{"type":"function_call_output","call_id":"call-1","output":{"value":"ok"}}
		],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"object":"response"`)
	assert.Contains(t, recorder.Body.String(), "gemini answer")
	assert.Contains(t, recorder.Body.String(), `"input_tokens":6`)

	wire := <-observed
	assert.Equal(t, "/v1beta/models/provider-model:generateContent", wire.Path)
	assert.Equal(t, "sk-upstream", wire.APIKey)
	assert.NotContains(t, wire.Body, "model")
	contents, ok := wire.Body["contents"].([]any)
	require.True(t, ok)
	require.Len(t, contents, 3)
	functionTurn := contents[1].(map[string]any)
	functionParts := functionTurn["parts"].([]any)
	assert.Equal(t, protocolkit.GeminiThoughtSignatureBypass, functionParts[0].(map[string]any)["thoughtSignature"])
	toolTurn := contents[2].(map[string]any)
	toolParts := toolTurn["parts"].([]any)
	assert.NotContains(t, toolParts[0].(map[string]any), "thoughtSignature")

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 6, log.PromptTokens)
	assert.Equal(t, 3, log.CompletionTokens)
}

func TestAdvancedCustomChatToNativeUpstreamsWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	tests := []struct {
		name              string
		converter         string
		upstreamPath      string
		stream            bool
		responseBody      string
		wantPath          string
		wantQuery         string
		wantResponse      string
		wantPromptTokens  int
		wantOutputTokens  int
		wantAnthropicHead bool
	}{
		{
			name: "Claude non-stream", converter: "openai_chat_completions_to_anthropic_messages",
			upstreamPath: "/anthropic/{model}", wantPath: "/anthropic/provider-model",
			responseBody: `{"id":"msg-native","type":"message","role":"assistant","model":"provider-model","content":[{"type":"text","text":"claude answer"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`,
			wantResponse: "claude answer", wantPromptTokens: 4, wantOutputTokens: 2, wantAnthropicHead: true,
		},
		{
			name: "Gemini stream", converter: "openai_chat_completions_to_gemini_generate_content",
			upstreamPath: "/v1beta/models/{model}:generateContent", stream: true,
			wantPath: "/v1beta/models/provider-model:streamGenerateContent", wantQuery: "alt=sse",
			responseBody: "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"gemini stream"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}` + "\n\n",
			wantResponse: "gemini stream", wantPromptTokens: 5, wantOutputTokens: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan struct {
				path, query, authorization, anthropicVersion string
				body                                         map[string]any
			}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				var decoded map[string]any
				require.NoError(t, json.Unmarshal(body, &decoded))
				observed <- struct {
					path, query, authorization, anthropicVersion string
					body                                         map[string]any
				}{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("anthropic-version"), decoded}
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = io.WriteString(w, test.responseBody)
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			settings := fmt.Sprintf(`{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":%q,"converter":%q}]}}`, test.upstreamPath, test.converter)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
				"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
			}).Error)

			requestBody := fmt.Sprintf(`{"model":"gpt-provider-contract","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, test.stream)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), test.wantResponse)
			if test.stream {
				assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
			}

			wire := <-observed
			assert.Equal(t, test.wantPath, wire.path)
			assert.Equal(t, test.wantQuery, wire.query)
			assert.Equal(t, "Bearer sk-upstream", wire.authorization)
			if test.wantAnthropicHead {
				assert.Equal(t, "2023-06-01", wire.anthropicVersion)
				assert.Equal(t, "provider-model", wire.body["model"])
			} else {
				assert.Empty(t, wire.anthropicVersion)
				assert.NotContains(t, wire.body, "model")
			}

			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
			assert.Equal(t, test.wantPromptTokens, log.PromptTokens)
			assert.Equal(t, test.wantOutputTokens, log.CompletionTokens)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
		})
	}
}

func TestAdvancedCustomNativePassThroughPreservesWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"gpt-provider-contract": {Prompt: 1, Completion: 2}})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	tests := []struct {
		name             string
		incomingPath     string
		upstreamPath     string
		auth             string
		requestBody      string
		responseBody     string
		wantResponseText string
		wantUpstreamPath string
		promptTokens     int
		completionTokens int
		checkWire        func(*testing.T, *http.Request, map[string]any)
	}{
		{
			name: "Claude", incomingPath: "/v1/messages", upstreamPath: "/native/messages/{model}",
			auth:             `,"auth":{"type":"header","name":"x-api-key","value":"{api_key}"}`,
			requestBody:      `{"model":"gpt-provider-contract","max_tokens":64,"system":"rules","messages":[{"role":"user","content":"hello"}]}`,
			responseBody:     `{"id":"msg-native","type":"message","role":"assistant","model":"provider-model","content":[{"type":"text","text":"native claude"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`,
			wantResponseText: "native claude",
			wantUpstreamPath: "/native/messages/provider-model",
			promptTokens:     4,
			completionTokens: 2,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "sk-upstream", request.Header.Get("x-api-key"))
				assert.Equal(t, "2023-06-01", request.Header.Get("anthropic-version"))
				assert.Empty(t, request.Header.Get("Authorization"))
				assert.Equal(t, "provider-model", body["model"])
				assert.Equal(t, "rules", body["system"])
			},
		},
		{
			name: "Gemini", incomingPath: "/v1beta/models/{model}:generateContent", upstreamPath: "/native/models/{model}:generateContent",
			auth:             `,"auth":{"type":"query","name":"key","value":"{api_key}"}`,
			requestBody:      `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			responseBody:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"native gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`,
			wantResponseText: "native gemini",
			wantUpstreamPath: "/native/models/provider-model:generateContent",
			promptTokens:     5,
			completionTokens: 3,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "sk-upstream", request.URL.Query().Get("key"))
				assert.Empty(t, request.Header.Get("Authorization"))
				assert.NotContains(t, body, "model")
				assert.NotEmpty(t, body["contents"])
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan providerWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var decoded map[string]any
				_ = json.Unmarshal(body, &decoded)
				test.checkWire(t, r, decoded)
				observed <- providerWireObservation{Path: r.URL.Path, RawQuery: r.URL.RawQuery, Body: decoded}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.responseBody)
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			settings := fmt.Sprintf(
				`{"advanced_custom":{"advanced_routes":[{"incoming_path":%q,"upstream_path":%q%s}]}}`,
				test.incomingPath, test.upstreamPath, test.auth,
			)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type": int(channelcatalog.ChannelTypeAdvancedCustom), "settings": settings,
				"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
			}).Error)

			requestPath := strings.ReplaceAll(test.incomingPath, "{model}", "gpt-provider-contract")
			handler := router.SetUpRouter()
			request := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(test.requestBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), test.wantResponseText)

			wire := <-observed
			assert.Equal(t, test.wantUpstreamPath, wire.Path)
			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
			assert.Equal(t, test.promptTokens, log.PromptTokens)
			assert.Equal(t, test.completionTokens, log.CompletionTokens)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
		})
	}
}
