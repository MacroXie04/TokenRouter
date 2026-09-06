package relay_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
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

func withAliPrices(t *testing.T, modelNames ...string) {
	t.Helper()
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	prices := make(map[string]service.ModelPrice, len(modelNames)+1)
	prices["gpt-provider-contract"] = service.ModelPrice{Prompt: 1, Completion: 2}
	for _, modelName := range modelNames {
		prices[modelName] = service.ModelPrice{Prompt: 1, Completion: 2}
	}
	service.SetModelPriceRegistry(prices)
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
}

func configureAliIntegration(t *testing.T, upstreamURL, clientModel, upstreamModel string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{clientModel: upstreamModel})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(constant.ChannelTypeAli), "models": clientModel,
		"model_mapping": string(mapping), "other": "plugin-contract",
	}).Error)
	if clientModel != "gpt-provider-contract" {
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: clientModel, ChannelId: channel.Id, Enabled: true, Weight: 1,
		}).Error)
		require.NoError(t, service.InitAbilityCache())
	}
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func assertAliSettled(t *testing.T, key string, userID int, channelID int, prompt, completion int) {
	t.Helper()
	expectedQuota := service.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, prompt, consumeLog.PromptTokens)
	assert.Equal(t, completion, consumeLog.CompletionTokens)
	assert.Equal(t, expectedQuota, consumeLog.Quota)
	assert.Equal(t, channelID, consumeLog.ChannelId)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-expectedQuota, user.Quota)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000-expectedQuota, token.RemainQuota)
	assert.Equal(t, expectedQuota, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Positive(t, reservation.ReservedQuota)
}

func assertAliRefunded(t *testing.T, key string, userID int) {
	t.Helper()
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
	assert.Zero(t, reservation.ActualQuota)
	assert.Positive(t, reservation.RequestedQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestAliTextEmbeddingResponsesRerankWireAndSettlementEndToEnd(t *testing.T) {
	withAliPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name, clientPath, upstreamPath, upstreamModel string
		requestBody, upstreamResponse                 string
		promptTokens, completionTokens                int
		check                                         func(*testing.T, map[string]any)
	}{
		{
			name: "chat", clientPath: "/v1/chat/completions", upstreamPath: "/gateway/compatible-mode/v1/chat/completions", upstreamModel: "qwen-plus",
			requestBody:      `{"model":"gpt-provider-contract","top_p":1,"thinking_budget":0,"enable_thinking":true,"group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`,
			upstreamResponse: `{"id":"chat-ali","object":"chat.completion","model":"qwen-plus","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
			promptTokens:     7, completionTokens: 3,
			check: func(t *testing.T, body map[string]any) {
				assert.EqualValues(t, .99, body["top_p"])
				assert.EqualValues(t, 0, body["thinking_budget"])
				assert.NotContains(t, body, "group")
			},
		},
		{
			name: "completion", clientPath: "/v1/completions", upstreamPath: "/gateway/compatible-mode/v1/completions", upstreamModel: "qwen-plus",
			requestBody:      `{"model":"gpt-provider-contract","prompt":"hello","max_tokens":5}`,
			upstreamResponse: `{"id":"cmpl-ali","object":"text_completion","model":"qwen-plus","choices":[{"index":0,"text":"ok","finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
			promptTokens:     4, completionTokens: 2,
		},
		{
			name: "embedding", clientPath: "/v1/embeddings", upstreamPath: "/gateway/compatible-mode/v1/embeddings", upstreamModel: "text-embedding-v1",
			requestBody:      `{"model":"gpt-provider-contract","input":["one","two"]}`,
			upstreamResponse: `{"object":"list","data":[{"index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`,
			promptTokens:     5,
		},
		{
			name: "responses", clientPath: "/v1/responses", upstreamPath: "/gateway/api/v2/apps/protocols/compatible-mode/v1/responses", upstreamModel: "qwen-plus",
			requestBody:      `{"model":"gpt-provider-contract","input":"hello","instructions":"brief"}`,
			upstreamResponse: `{"id":"resp-ali","object":"response","status":"completed","model":"qwen-plus","output":[],"usage":{"input_tokens":6,"output_tokens":2,"total_tokens":8}}`,
			promptTokens:     6, completionTokens: 2,
		},
		{
			name: "rerank", clientPath: "/v1/rerank", upstreamPath: "/gateway/api/v1/services/rerank/text-rerank/text-rerank", upstreamModel: "gte-rerank-v2",
			requestBody:      `{"model":"gpt-provider-contract","query":"needle","documents":["one",{"text":"two"}],"top_n":1,"return_documents":false}`,
			upstreamResponse: `{"output":{"results":[{"index":1,"relevance_score":0.91}]},"usage":{"total_tokens":9}}`,
			promptTokens:     9,
			check: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "needle", body["input"].(map[string]any)["query"])
				assert.Equal(t, false, body["parameters"].(map[string]any)["return_documents"])
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan map[string]any, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.upstreamPath, request.URL.Path)
				assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
				assert.Equal(t, "plugin-contract", request.Header.Get("X-DashScope-Plugin"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				observed <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID, channel := configureAliIntegration(t, upstream.URL+"/gateway", "gpt-provider-contract", test.upstreamModel)
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.requestBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			body := <-observed
			assert.Equal(t, test.upstreamModel, body["model"])
			if test.check != nil {
				test.check(t, body)
			}
			assertAliSettled(t, key, userID, channel.Id, test.promptTokens, test.completionTokens)
		})
	}
}

func TestAliStreamWireUsageAndSettlementEndToEnd(t *testing.T) {
	withAliPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	observed := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/compatible-mode/v1/chat/completions", request.URL.Path)
		assert.Equal(t, "enable", request.Header.Get("X-DashScope-SSE"))
		assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		observed <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"ali-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	key, userID, channel := configureAliIntegration(t, upstream.URL, "gpt-provider-contract", "qwen-plus")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"content":"hello"`)
	body := <-observed
	assert.Equal(t, true, body["stream_options"].(map[string]any)["include_usage"])
	assertAliSettled(t, key, userID, channel.Id, 8, 4)
}

func TestAliSyncAndAsyncImageLifecycleEndToEnd(t *testing.T) {
	withAliPrices(t, "qwen-image-client")
	t.Setenv("RETRY_TIMES", "0")

	for _, test := range []struct {
		name, clientModel, upstreamModel, upstreamPath string
		async                                          bool
	}{
		{name: "sync", clientModel: "qwen-image-client", upstreamModel: "qwen-image", upstreamPath: "/api/v1/services/aigc/multimodal-generation/generation"},
		{name: "async", clientModel: "gpt-provider-contract", upstreamModel: "wanx-v1", upstreamPath: "/api/v1/services/aigc/text2image/image-synthesis", async: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var pollCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
				switch request.URL.Path {
				case test.upstreamPath:
					assert.Equal(t, test.async, request.Header.Get("X-DashScope-Async") == "enable")
					var body map[string]any
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					assert.Equal(t, test.upstreamModel, body["model"])
					if test.async {
						_, _ = io.WriteString(w, `{"output":{"task_id":"task-123","task_status":"PENDING"}}`)
					} else {
						_, _ = io.WriteString(w, `{"output":{"choices":[{"message":{"content":[{"image":"inline-base64"},{"text":"expanded prompt"}]}}]},"usage":{"image_count":1}}`)
					}
				case "/api/v1/tasks/task-123":
					pollCalls.Add(1)
					_, _ = io.WriteString(w, `{"output":{"task_id":"task-123","task_status":"SUCCEEDED","results":[{"b64_image":"async-base64"}]},"usage":{"image_count":2}}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer upstream.Close()

			key, userID, channel := configureAliIntegration(t, upstream.URL, test.clientModel, test.upstreamModel)
			request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
				`{"model":"`+test.clientModel+`","prompt":"draw a cat","n":2,"size":"1024x1024"}`,
			))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "base64")
			if test.async {
				assert.Equal(t, int32(1), pollCalls.Load())
			}
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Positive(t, reservation.ActualQuota)
			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
			assert.Equal(t, channel.Id, log.ChannelId)
			if test.async {
				assert.Equal(t, 2, log.CompletionTokens)
			} else {
				assert.Equal(t, 1, log.CompletionTokens)
			}
			var token model.Token
			require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
			assert.Positive(t, token.UsedQuota)
		})
	}
}

func TestAliMultipartImageEditWireAndSettlementEndToEnd(t *testing.T) {
	const clientModel = "qwen-image-edit-client"
	withAliPrices(t, clientModel)
	t.Setenv("RETRY_TIMES", "0")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v1/services/aigc/multimodal-generation/generation", request.URL.Path)
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "qwen-image-edit", body["model"])
		contents := body["input"].(map[string]any)["messages"].([]any)[0].(map[string]any)["content"].([]any)
		require.Len(t, contents, 2)
		assert.True(t, strings.HasPrefix(contents[0].(map[string]any)["image"].(string), "data:"))
		_, _ = io.WriteString(w, `{"output":{"choices":[{"message":{"content":[{"image":"edited-base64"}]}}]},"usage":{"image_count":1}}`)
	}))
	defer upstream.Close()
	key, userID, _ := configureAliIntegration(t, upstream.URL, clientModel, "qwen-image-edit")

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	require.NoError(t, writer.WriteField("model", clientModel))
	require.NoError(t, writer.WriteField("prompt", "make blue"))
	imagePart, err := writer.CreateFormFile("image", "input.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("fake-image"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &requestBody)
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "edited-base64")
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}

func TestAliClaudeNativeAndConvertedLifecycleEndToEnd(t *testing.T) {
	withAliPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, upstreamModel, upstreamPath, responseBody, responseText string
		native                                                        bool
	}{
		{
			name: "native", upstreamModel: "qwen-plus", upstreamPath: "/apps/anthropic/v1/messages", native: true,
			responseBody: `{"id":"msg-ali","type":"message","role":"assistant","model":"qwen-plus","content":[{"type":"text","text":"native Ali"}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":3}}`,
			responseText: "native Ali",
		},
		{
			name: "converted", upstreamModel: "other-provider-model", upstreamPath: "/compatible-mode/v1/chat/completions",
			responseBody: `{"id":"chat-ali","object":"chat.completion","model":"other-provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"converted Ali"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
			responseText: "converted Ali",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.upstreamPath, request.URL.Path)
				assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				assert.Equal(t, test.upstreamModel, body["model"])
				if test.native {
					assert.EqualValues(t, 32, body["max_tokens"])
				} else {
					assert.NotEmpty(t, body["messages"])
				}
				_, _ = io.WriteString(w, test.responseBody)
			}))
			defer upstream.Close()
			key, userID := setupClaudeRelay(t, upstream.URL, int(constant.ChannelTypeAli), "gpt-provider-contract")
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Update(
				"model_mapping", `{"gpt-provider-contract":"`+test.upstreamModel+`"}`,
			).Error)
			response := claudeRelayRequest(t, router.SetUpRouter(), key,
				`{"model":"gpt-provider-contract","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.responseText)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
		})
	}
}

func TestAliClaudeConvertedErrorMappingAndRefundEndToEnd(t *testing.T) {
	withAliPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/compatible-mode/v1/chat/completions", request.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"Throttling","message":"busy for sk-upstream","request_id":"req-ali"}`)
	}))
	defer upstream.Close()

	key, userID := setupClaudeRelay(t, upstream.URL, int(constant.ChannelTypeAli), "gpt-provider-contract")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
		"model_mapping":       `{"gpt-provider-contract":"other-provider-model"}`,
		"status_code_mapping": `{"429":503}`,
	}).Error)
	response := claudeRelayRequest(t, router.SetUpRouter(), key,
		`{"model":"gpt-provider-contract","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Equal(t, int32(1), calls.Load())
	assert.Contains(t, response.Body.String(), `"code":"Throttling"`)
	assert.NotContains(t, response.Body.String(), "sk-upstream")
	assertAliRefunded(t, key, userID)
}

func TestAliErrorsRefundAndUnsupportedModeNeverContactsUpstream(t *testing.T) {
	withAliPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, clientPath, body string
		upstreamStatus         int
		upstreamBody           string
		wantStatus             int
		wantCalls              int32
	}{
		{
			name: "mapped provider error", clientPath: "/v1/chat/completions",
			body:           `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			upstreamStatus: http.StatusTooManyRequests,
			upstreamBody:   `{"code":"Throttling","message":"busy for sk-upstream"}`,
			wantStatus:     http.StatusServiceUnavailable, wantCalls: 1,
		},
		{
			name: "unsupported moderation", clientPath: "/v1/moderations",
			body:       `{"model":"gpt-provider-contract","input":"hello"}`,
			wantStatus: http.StatusInternalServerError, wantCalls: 0,
		},
		{
			name: "unsupported Gemini format", clientPath: "/v1beta/models/gpt-provider-contract:generateContent",
			body:       `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			wantStatus: http.StatusInternalServerError, wantCalls: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.upstreamStatus)
				_, _ = io.WriteString(w, test.upstreamBody)
			}))
			defer upstream.Close()
			key, userID, channel := configureAliIntegration(t, upstream.URL, "gpt-provider-contract", "qwen-plus")
			require.NoError(t, model.DB.Model(&channel).Update("status_code_mapping", `{"429":503}`).Error)
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Equal(t, test.wantCalls, calls.Load())
			assert.NotContains(t, response.Body.String(), "sk-upstream")
			if test.upstreamStatus == http.StatusTooManyRequests {
				assert.Contains(t, response.Body.String(), `"code":"Throttling"`)
			}
			assertAliRefunded(t, key, userID)
		})
	}
}
