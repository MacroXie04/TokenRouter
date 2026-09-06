package engine_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func withBaiduPrices(t *testing.T) {
	t.Helper()
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-provider-contract":  {Prompt: 1, Completion: 2},
		"text-embedding-3-small": {Prompt: 1, Completion: 2},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})
}

func configureBaiduIntegration(
	t *testing.T,
	upstreamURL string,
	channelType channelcatalog.ChannelType,
	clientModel string,
	upstreamModel string,
	credential string,
) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{clientModel: upstreamModel})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": channelType, "key": credential, "models": clientModel, "model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	return key, userID, channel
}

func baiduRelayRequest(t *testing.T, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	return response
}

func assertBaiduSettled(t *testing.T, key string, userID, channelID, promptTokens, completionTokens int) {
	t.Helper()
	wantQuota := billingsvc.ComputeQuota("gpt-provider-contract", "default", promptTokens, completionTokens)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, promptTokens, log.PromptTokens)
	assert.Equal(t, completionTokens, log.CompletionTokens)
	assert.Equal(t, wantQuota, log.Quota)
	assert.Equal(t, channelID, log.ChannelId)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-wantQuota, user.Quota)
	assert.Equal(t, wantQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000-wantQuota, token.RemainQuota)
	assert.Equal(t, wantQuota, token.UsedQuota)
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, channelID).Error)
	assert.EqualValues(t, wantQuota, channel.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, wantQuota, reservation.ActualQuota)
	assert.Positive(t, reservation.RequestedQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
	assert.NotEmpty(t, reservation.ReservationID)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])
}

func assertBaiduEmbeddingSettled(t *testing.T, key string, userID, channelID, promptTokens int) {
	t.Helper()
	wantQuota := billingsvc.ComputeQuota("text-embedding-3-small", "default", promptTokens, 0)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, "text-embedding-3-small", log.ModelName)
	assert.Equal(t, promptTokens, log.PromptTokens)
	assert.Zero(t, log.CompletionTokens)
	assert.Equal(t, wantQuota, log.Quota)
	assert.Equal(t, channelID, log.ChannelId)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, wantQuota, reservation.ActualQuota)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, wantQuota, token.UsedQuota)
}

func assertBaiduRefunded(t *testing.T, key string, userID int) {
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
	assert.Positive(t, reservation.ReservedQuota)
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestBaiduLegacyChatAndEmbeddingWireSettlementEndToEnd(t *testing.T) {
	withBaiduPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, clientModel, upstreamModel, path, clientBody, providerBody, responseFragment string
		promptTokens, completionTokens                                                     int
		checkWire                                                                          func(*testing.T, map[string]any)
	}{
		{
			name: "chat", clientModel: "gpt-provider-contract", upstreamModel: "ERNIE-4.0-8K",
			path:             "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/completions_pro",
			clientBody:       `{"model":"gpt-provider-contract","max_completion_tokens":1,"group":"dashboard-only","messages":[{"role":"system","content":"policy"},{"role":"user","content":"hello"}]}`,
			providerBody:     `{"id":"ernie-chat","created":123,"result":"legacy answer","usage":{"prompt_tokens":7,"total_tokens":10}}`,
			responseFragment: "legacy answer", promptTokens: 7, completionTokens: 3,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.NotContains(t, body, "model")
				assert.NotContains(t, body, "group")
				assert.Equal(t, "policy", body["system"])
				assert.EqualValues(t, 2, body["max_output_tokens"])
			},
		},
		{
			name: "embedding", clientModel: "text-embedding-3-small", upstreamModel: "Embedding-V1",
			path:             "/rpc/2.0/ai_custom/v1/wenxinworkshop/embeddings/embedding-v1",
			clientBody:       `{"model":"text-embedding-3-small","input":["one","two"],"dimensions":2,"group":"dashboard-only"}`,
			providerBody:     `{"id":"ernie-embedding","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`,
			responseFragment: `"model":"baidu-embedding"`, promptTokens: 4,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, []any{"one", "two"}, body["input"])
				assert.NotContains(t, body, "model")
				assert.NotContains(t, body, "dimensions")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var tokenCalls atomic.Int32
			var inferenceCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/gateway/oauth/2.0/token":
					tokenCalls.Add(1)
					assert.Equal(t, http.MethodPost, request.Method)
					assert.Equal(t, "client_credentials", request.URL.Query().Get("grant_type"))
					assert.Equal(t, "baidu-client", request.URL.Query().Get("client_id"))
					assert.Equal(t, "baidu-secret", request.URL.Query().Get("client_secret"))
					_, _ = io.WriteString(w, `{"access_token":"access-token","expires_in":7200}`)
				case "/gateway" + test.path:
					inferenceCalls.Add(1)
					assert.Equal(t, "access-token", request.URL.Query().Get("access_token"))
					assert.Empty(t, request.Header.Get("Authorization"), "the inference request must not repeat the client secret")
					var body map[string]any
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					test.checkWire(t, body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, test.providerBody)
				default:
					http.NotFound(w, request)
				}
			}))
			defer upstream.Close()

			key, userID, channel := configureBaiduIntegration(t, upstream.URL+"/gateway", channelcatalog.ChannelTypeBaidu,
				test.clientModel, test.upstreamModel, "baidu-client|baidu-secret")
			response := baiduRelayRequest(t, key, map[string]string{
				"chat": "/v1/chat/completions", "embedding": "/v1/embeddings",
			}[test.name], test.clientBody)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.responseFragment)
			assert.Equal(t, int32(1), tokenCalls.Load())
			assert.Equal(t, int32(1), inferenceCalls.Load())
			if test.name == "embedding" {
				assertBaiduEmbeddingSettled(t, key, userID, channel.Id, test.promptTokens)
			} else {
				assertBaiduSettled(t, key, userID, channel.Id, test.promptTokens, test.completionTokens)
			}
		})
	}
}

func TestBaiduLegacyAndV2StreamsSettleTerminalUsageEndToEnd(t *testing.T) {
	withBaiduPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, upstreamModel, credential, upstreamPath, stream string
		channelType                                           channelcatalog.ChannelType
		promptTokens, completionTokens                        int
	}{
		{
			name: "legacy", channelType: channelcatalog.ChannelTypeBaidu, upstreamModel: "ERNIE-3.5-8K",
			credential:   "baidu-client|baidu-secret",
			upstreamPath: "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/completions",
			stream: "data: {\"id\":\"legacy-stream\",\"result\":\"hello\",\"is_end\":false}\n\n" +
				"data: {\"id\":\"legacy-stream\",\"result\":\" world\",\"is_end\":true,\"usage\":{\"prompt_tokens\":8,\"total_tokens\":13}}\n\n" +
				"data: [DONE]\n\n",
			promptTokens: 8, completionTokens: 5,
		},
		{
			name: "v2", channelType: channelcatalog.ChannelTypeBaiduV2, upstreamModel: "ernie-4.0-turbo-8k",
			credential: "v2-token|app-123", upstreamPath: "/v2/chat/completions",
			stream: "data: {\"id\":\"v2-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\n" +
				"data: [DONE]\n\n",
			promptTokens: 9, completionTokens: 4,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var inferenceCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/oauth/2.0/token" {
					_, _ = io.WriteString(w, `{"access_token":"stream-token","expires_in":7200}`)
					return
				}
				inferenceCalls.Add(1)
				assert.Equal(t, test.upstreamPath, request.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				assert.Equal(t, true, body["stream"])
				if test.channelType == channelcatalog.ChannelTypeBaidu {
					assert.Equal(t, "stream-token", request.URL.Query().Get("access_token"))
					assert.NotContains(t, body, "model")
				} else {
					assert.Equal(t, "Bearer v2-token", request.Header.Get("Authorization"))
					assert.Equal(t, "app-123", request.Header.Get("appid"))
					assert.Equal(t, test.upstreamModel, body["model"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.stream)
			}))
			defer upstream.Close()

			key, userID, channel := configureBaiduIntegration(t, upstream.URL, test.channelType,
				"gpt-provider-contract", test.upstreamModel, test.credential)
			response := baiduRelayRequest(t, key, "/v1/chat/completions",
				`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Header().Get("Content-Type"), "text/event-stream")
			assert.Contains(t, response.Body.String(), `"content":"hello"`)
			assert.Equal(t, 1, strings.Count(response.Body.String(), "data: [DONE]"))
			assert.Equal(t, int32(1), inferenceCalls.Load())
			assertBaiduSettled(t, key, userID, channel.Id, test.promptTokens, test.completionTokens)
		})
	}
}

func TestBaiduV2SearchAndNativeClaudeUseOpenAIWireAndSettle(t *testing.T) {
	withBaiduPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, nativeClaude := range []bool{false, true} {
		name := "openai"
		if nativeClaude {
			name = "native Claude"
		}
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/gateway/v2/chat/completions", request.URL.Path)
				assert.Equal(t, "Bearer v2-token", request.Header.Get("Authorization"))
				assert.Equal(t, "application-id", request.Header.Get("appid"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				assert.Equal(t, "ernie-4.0-turbo-8k", body["model"])
				search := body["web_search"].(map[string]any)
				assert.Equal(t, true, search["enable"])
				assert.Equal(t, true, search["enable_citation"])
				assert.NotContains(t, body, "group")
				_, _ = io.WriteString(w, `{"id":"v2-chat","object":"chat.completion","model":"ernie-4.0-turbo-8k","choices":[{"index":0,"message":{"role":"assistant","content":"v2 answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`)
			}))
			defer upstream.Close()

			var key string
			var userID int
			if nativeClaude {
				key, userID = setupClaudeRelay(t, upstream.URL+"/gateway", int(channelcatalog.ChannelTypeBaiduV2), "gpt-provider-contract")
			} else {
				key, userID = setupRelayIntegration(t, upstream.URL+"/gateway")
			}
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type": channelcatalog.ChannelTypeBaiduV2, "key": "v2-token|application-id",
				"models": "gpt-provider-contract", "model_mapping": `{"gpt-provider-contract":"ernie-4.0-turbo-8k-search"}`,
			}).Error)
			require.NoError(t, channelssvc.InitAbilityCache())
			var response *httptest.ResponseRecorder
			if nativeClaude {
				response = claudeRelayRequest(t, router.SetUpRouter(), key,
					`{"model":"gpt-provider-contract","max_tokens":32,"group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`)
			} else {
				response = baiduRelayRequest(t, key, "/v1/chat/completions",
					`{"model":"gpt-provider-contract","group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`)
			}
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "v2 answer")
			if nativeClaude {
				assert.Contains(t, response.Body.String(), `"type":"message"`)
			}
			assertBaiduSettled(t, key, userID, channel.Id, 6, 3)
		})
	}
}

func TestBaiduErrorsAndUnsupportedModesRefundWithoutCredentialLeak(t *testing.T) {
	withBaiduPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, path, body, upstreamModel, credential string
		channelType                                 channelcatalog.ChannelType
		upstreamStatus                              int
		upstreamBody                                string
		wantStatus                                  int
		wantCalls                                   int32
		nativeClaude                                bool
	}{
		{
			name: "v2 mapped error", channelType: channelcatalog.ChannelTypeBaiduV2,
			path: "/v1/chat/completions", body: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			upstreamModel: "ernie-4.0-turbo-8k", credential: "v2-secret-token|application-id",
			upstreamStatus: http.StatusTooManyRequests,
			upstreamBody:   `{"error":{"message":"busy for v2-secret-token and application-id","type":"rate_limit","code":"quota"}}`,
			wantStatus:     http.StatusServiceUnavailable, wantCalls: 1,
		},
		{
			name: "v2 embedding unsupported", channelType: channelcatalog.ChannelTypeBaiduV2,
			path: "/v1/embeddings", body: `{"model":"gpt-provider-contract","input":"hello"}`,
			upstreamModel: "ernie-4.0-turbo-8k", credential: "v2-secret-token|application-id",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "legacy Claude unsupported", channelType: channelcatalog.ChannelTypeBaidu,
			body:          `{"model":"gpt-provider-contract","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
			upstreamModel: "ERNIE-4.0-8K", credential: "legacy-client|legacy-secret",
			wantStatus: http.StatusInternalServerError, nativeClaude: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.upstreamStatus)
				_, _ = io.WriteString(w, test.upstreamBody)
			}))
			defer upstream.Close()
			var key string
			var userID int
			if test.nativeClaude {
				key, userID = setupClaudeRelay(t, upstream.URL, int(test.channelType), "gpt-provider-contract")
			} else {
				key, userID = setupRelayIntegration(t, upstream.URL)
			}
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type": test.channelType, "key": test.credential, "models": "gpt-provider-contract",
				"model_mapping":       `{"gpt-provider-contract":"` + test.upstreamModel + `"}`,
				"status_code_mapping": `{"429":503}`,
			}).Error)
			require.NoError(t, channelssvc.InitAbilityCache())
			var response *httptest.ResponseRecorder
			if test.nativeClaude {
				response = claudeRelayRequest(t, router.SetUpRouter(), key, test.body)
			} else {
				response = baiduRelayRequest(t, key, test.path, test.body)
			}
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Equal(t, test.wantCalls, calls.Load())
			for _, secret := range strings.Split(test.credential, "|") {
				assert.NotContains(t, response.Body.String(), secret)
			}
			assertBaiduRefunded(t, key, userID)
		})
	}
}
