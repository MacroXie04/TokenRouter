package engine_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type providerWireObservation struct {
	Path          string
	RawQuery      string
	Authorization string
	APIKey        string
	AccountID     string
	OpenAIBeta    string
	Originator    string
	Referer       string
	Title         string
	Organization  string
	Body          map[string]any
}

func TestCodexResponsesWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-provider-contract": {Prompt: 1, Completion: 2},
	})
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
			Path: r.URL.Path, Authorization: r.Header.Get("Authorization"),
			AccountID: r.Header.Get("chatgpt-account-id"), OpenAIBeta: r.Header.Get("OpenAI-Beta"),
			Originator: r.Header.Get("originator"), Body: decoded,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp-codex", "object": "response", "status": "completed", "model": "gpt-5-codex",
			"output": []any{},
			"usage":  map[string]any{"input_tokens": 4, "output_tokens": 6, "total_tokens": 10},
		})
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeCodex),
		"key":           `{"access_token":"codex-access","account_id":"acct-42","refresh_token":"never-forward"}`,
		"model_mapping": `{"gpt-provider-contract":"gpt-5-codex"}`,
	}).Error)

	handler := router.SetUpRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-provider-contract","input":"hello","max_output_tokens":128,
		"temperature":1,"frequency_penalty":0.5,"presence_penalty":0.5,"group":"dashboard-only"
	}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "resp-codex")

	wire := <-observed
	assert.Equal(t, "/backend-api/codex/responses", wire.Path)
	assert.Equal(t, "Bearer codex-access", wire.Authorization)
	assert.Equal(t, "acct-42", wire.AccountID)
	assert.Equal(t, "responses=experimental", wire.OpenAIBeta)
	assert.Equal(t, "codex_cli_rs", wire.Originator)
	assert.NotContains(t, wire.Authorization, "refresh_token")
	assert.Equal(t, "gpt-5-codex", wire.Body["model"])
	assert.Equal(t, "", wire.Body["instructions"])
	assert.Equal(t, false, wire.Body["store"])
	assert.NotContains(t, wire.Body, "max_output_tokens")
	assert.NotContains(t, wire.Body, "temperature")
	assert.NotContains(t, wire.Body, "frequency_penalty")
	assert.NotContains(t, wire.Body, "presence_penalty")
	assert.NotContains(t, wire.Body, "group")

	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName)
	assert.Equal(t, 4, consumeLog.PromptTokens)
	assert.Equal(t, 6, consumeLog.CompletionTokens)
	assert.Greater(t, consumeLog.Quota, 0)

	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}

func TestOpenAICompatibleProvidersWireAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-provider-contract": {Prompt: 1, Completion: 2},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	for _, test := range []struct {
		name         string
		channelType  channelcatalog.ChannelType
		basePath     string
		expectedPath string
		openRouter   bool
	}{
		{name: "OpenRouter", channelType: channelcatalog.ChannelTypeOpenRouter, basePath: "/api", expectedPath: "/api/v1/chat/completions", openRouter: true},
		{name: "OpenAI Max", channelType: channelcatalog.ChannelTypeOpenAIMax, expectedPath: "/v1/chat/completions"},
		{name: "OhMyGPT", channelType: channelcatalog.ChannelTypeOhMyGPT, expectedPath: "/v1/chat/completions"},
		{name: "AILS", channelType: channelcatalog.ChannelTypeAILS, expectedPath: "/v1/chat/completions"},
		{name: "AIProxy", channelType: channelcatalog.ChannelTypeAIProxy, expectedPath: "/v1/chat/completions"},
		{name: "API2GPT", channelType: channelcatalog.ChannelTypeAPI2GPT, expectedPath: "/v1/chat/completions"},
		{name: "AIGC2D", channelType: channelcatalog.ChannelTypeAIGC2D, expectedPath: "/v1/chat/completions"},
		{name: "360", channelType: channelcatalog.ChannelType360, expectedPath: "/v1/chat/completions"},
		{name: "FastGPT", channelType: channelcatalog.ChannelTypeFastGPT, expectedPath: "/v1/chat/completions"},
		{name: "LingYiWanWu", channelType: channelcatalog.ChannelTypeLingYiWanWu, expectedPath: "/v1/chat/completions"},
		{name: "Xinference", channelType: channelcatalog.ChannelTypeXinference, expectedPath: "/v1/chat/completions"},
		{name: "DeepSeek", channelType: channelcatalog.ChannelTypeDeepSeek, expectedPath: "/v1/chat/completions"},
		{name: "Mistral", channelType: channelcatalog.ChannelTypeMistral, expectedPath: "/v1/chat/completions"},
		{name: "xAI", channelType: channelcatalog.ChannelTypeXai, expectedPath: "/v1/chat/completions"},
		{name: "SiliconFlow", channelType: channelcatalog.ChannelTypeSiliconFlow, expectedPath: "/v1/chat/completions"},
		{name: "Submodel", channelType: channelcatalog.ChannelTypeSubmodel, expectedPath: "/v1/chat/completions"},
		{name: "Azure OpenAI", channelType: channelcatalog.ChannelTypeAzure, expectedPath: "/openai/deployments/provider-model/chat/completions"},
		{name: "Custom exact endpoint", channelType: channelcatalog.ChannelTypeCustom, expectedPath: "/custom/provider/model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mappedModel := "provider/model"
			if test.channelType == channelcatalog.ChannelTypeAzure {
				mappedModel = "provider-model"
			}
			observed := make(chan providerWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var decoded map[string]any
				_ = json.Unmarshal(body, &decoded)
				observed <- providerWireObservation{
					Path: r.URL.Path, RawQuery: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"), APIKey: r.Header.Get("api-key"),
					Referer: r.Header.Get("HTTP-Referer"), Title: r.Header.Get("X-OpenRouter-Title"),
					Organization: r.Header.Get("OpenAI-Organization"), Body: decoded,
				}
				w.Header().Set("Content-Type", "application/json")
				completionTokens := 6
				if test.channelType == channelcatalog.ChannelTypeXai {
					// xAI's compatibility response has historically reported this
					// field inconsistently; total - prompt is authoritative.
					completionTokens = 999
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "chatcmpl-provider", "object": "chat.completion", "model": "provider/model",
					"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
					"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": completionTokens, "total_tokens": 10},
				})
			}))
			defer upstream.Close()

			baseURL := upstream.URL + test.basePath
			if test.channelType == channelcatalog.ChannelTypeCustom {
				baseURL = upstream.URL + "/custom/{model}"
			}
			key, userID := setupRelayIntegration(t, baseURL)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type":                 int(test.channelType),
				"model_mapping":        `{"gpt-provider-contract":"` + mappedModel + `"}`,
				"open_ai_organization": "private-openai-org",
			}).Error)

			handler := router.SetUpRouter()
			requestBody := `{"model":"gpt-provider-contract","group":"dashboard-only","messages":[{"role":"user","content":"hello"}],"provider_extension":"kept"}`
			requestPath := "/v1/chat/completions"
			if test.channelType == channelcatalog.ChannelTypeAzure {
				requestPath += "?api-version=2024-06-01"
			}
			req := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(requestBody))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "chatcmpl-provider")

			wire := <-observed
			assert.Equal(t, test.expectedPath, wire.Path)
			if test.channelType == channelcatalog.ChannelTypeAzure {
				assert.Empty(t, wire.Authorization)
				assert.Equal(t, "sk-upstream", wire.APIKey)
				assert.Equal(t, "api-version=2024-06-01", wire.RawQuery)
			} else {
				assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
				assert.Empty(t, wire.APIKey)
			}
			assert.Equal(t, mappedModel, wire.Body["model"], "channel model mapping must be applied on wire")
			assert.Equal(t, "kept", wire.Body["provider_extension"])
			assert.NotContains(t, wire.Body, "group", "dashboard routing state must not leak upstream")
			assert.Empty(t, wire.Organization, "OpenAI organization metadata is not a header for compatible providers")
			if test.openRouter {
				assert.Equal(t, "https://github.com/MacroXie04/TokenRouter", wire.Referer)
				assert.Equal(t, "TokenRouter", wire.Title)
				assert.Equal(t, true, wire.Body["usage"].(map[string]any)["include"])
			} else {
				assert.Empty(t, wire.Referer)
				assert.Empty(t, wire.Title)
			}

			var consumeLog model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
			assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName, "billing and logs use the requested model")
			assert.Equal(t, 4, consumeLog.PromptTokens)
			assert.Equal(t, 6, consumeLog.CompletionTokens)
			assert.Greater(t, consumeLog.Quota, 0)
			assert.Equal(t, channel.Id, consumeLog.ChannelId)

			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			assert.Equal(t, consumeLog.Quota, user.UsedQuota)
			assert.Equal(t, 500000-consumeLog.Quota, user.Quota)
			assert.Equal(t, 1, user.RequestCount)

			var token model.Token
			require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
			assert.Equal(t, consumeLog.Quota, token.UsedQuota)
			assert.Equal(t, 500000-consumeLog.Quota, token.RemainQuota)

			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, consumeLog.Quota, reservation.ActualQuota)
		})
	}
}

func TestNarrowOpenAIProviderStreamsWireUsageAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-provider-contract": {Prompt: 1, Completion: 2},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	for _, test := range []struct {
		name                  string
		channelType           channelcatalog.ChannelType
		expectedPath          string
		expectExtension       bool
		expectSearchControls  bool
		expectCitationForward bool
	}{
		{name: "Perplexity", channelType: channelcatalog.ChannelTypePerplexity, expectedPath: "/chat/completions", expectSearchControls: true, expectCitationForward: true},
		{name: "Submodel", channelType: channelcatalog.ChannelTypeSubmodel, expectedPath: "/v1/chat/completions", expectExtension: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan providerWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var decoded map[string]any
				_ = json.Unmarshal(body, &decoded)
				observed <- providerWireObservation{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: decoded}
				w.Header().Set("Content-Type", "text/event-stream")
				if test.expectCitationForward {
					_, _ = io.WriteString(w, "data: {\"id\":\"pplx-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}],\"citations\":[\"https://example.test/source\"]}\n\n")
				} else {
					_, _ = io.WriteString(w, "data: {\"id\":\"submodel-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n")
				}
				_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type":          int(test.channelType),
				"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
			}).Error)

			handler := router.SetUpRouter()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
				"model":"gpt-provider-contract","stream":true,"top_p":1,
				"messages":[{"role":"user","content":"hello","name":"private-name"}],
				"search_domain_filter":["example.test"],"return_images":true,
				"provider_extension":"kept-only-by-submodel","group":"dashboard-only"
			}`))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "data: [DONE]")
			if test.expectCitationForward {
				assert.Contains(t, recorder.Body.String(), "https://example.test/source")
			}

			wire := <-observed
			assert.Equal(t, test.expectedPath, wire.Path)
			assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
			assert.Equal(t, "provider-model", wire.Body["model"])
			assert.Equal(t, true, wire.Body["stream"])
			assert.NotContains(t, wire.Body, "group")
			if test.expectExtension {
				assert.Equal(t, "kept-only-by-submodel", wire.Body["provider_extension"])
			} else {
				assert.NotContains(t, wire.Body, "provider_extension")
			}
			if test.expectSearchControls {
				assert.Equal(t, []any{"example.test"}, wire.Body["search_domain_filter"])
				assert.InDelta(t, 0.99, wire.Body["top_p"], 0.0001)
				messages := wire.Body["messages"].([]any)
				assert.NotContains(t, messages[0].(map[string]any), "name")
			}

			var consumeLog model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
			assert.Equal(t, 5, consumeLog.PromptTokens)
			assert.Equal(t, 3, consumeLog.CompletionTokens)
			assert.Greater(t, consumeLog.Quota, 0)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, consumeLog.Quota, reservation.ActualQuota)
		})
	}
}

func TestJinaWireUsageAndSettlement(t *testing.T) {
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-provider-contract": {Prompt: 1, Completion: 2},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})

	for _, test := range []struct {
		name             string
		clientPath       string
		expectedPath     string
		requestBody      string
		responseBody     string
		expectedPrompt   int
		forbiddenRequest string
	}{
		{
			name: "rerank", clientPath: "/v1/rerank", expectedPath: "/v1/rerank",
			requestBody:    `{"model":"gpt-provider-contract","query":"needle","documents":["one","two"],"top_n":1,"return_documents":true,"provider_extension":"strip"}`,
			responseBody:   `{"results":[{"index":1,"relevance_score":0.92}],"usage":{"total_tokens":9}}`,
			expectedPrompt: 9, forbiddenRequest: "provider_extension",
		},
		{
			name: "embeddings", clientPath: "/v1/embeddings", expectedPath: "/v1/embeddings",
			requestBody:    `{"model":"gpt-provider-contract","input":["alpha","beta"],"encoding_format":"base64","dimensions":256,"provider_extension":"strip"}`,
			responseBody:   `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"provider-model","usage":{"prompt_tokens":4,"completion_tokens":0,"total_tokens":4}}`,
			expectedPrompt: 4, forbiddenRequest: "encoding_format",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan providerWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var decoded map[string]any
				_ = json.Unmarshal(body, &decoded)
				observed <- providerWireObservation{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: decoded}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.responseBody)
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type":          int(channelcatalog.ChannelTypeJina),
				"model_mapping": `{"gpt-provider-contract":"provider-model"}`,
			}).Error)

			req := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.requestBody))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, req)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

			wire := <-observed
			assert.Equal(t, test.expectedPath, wire.Path)
			assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
			assert.Equal(t, "provider-model", wire.Body["model"])
			assert.NotContains(t, wire.Body, test.forbiddenRequest)
			assert.NotContains(t, wire.Body, "provider_extension")
			if test.name == "rerank" {
				assert.Contains(t, recorder.Body.String(), `"prompt_tokens":9`)
			}

			var consumeLog model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
			assert.Equal(t, test.expectedPrompt, consumeLog.PromptTokens)
			assert.Zero(t, consumeLog.CompletionTokens)
			assert.Greater(t, consumeLog.Quota, 0)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, consumeLog.Quota, reservation.ActualQuota)
		})
	}
}

func TestOpenAICompatibleProviderErrorRefundAndMapping(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"provider busy: sk-upstream","type":"rate_limit_error"}}`))
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":                int(channelcatalog.ChannelTypeMistral),
		"status_code_mapping": `{"429":503}`,
	}).Error)

	handler := router.SetUpRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "provider busy")
	assert.NotContains(t, recorder.Body.String(), "sk-upstream", "an upstream must not reflect the selected channel key")

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
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&logs).Error)
	assert.Zero(t, logs)
}
