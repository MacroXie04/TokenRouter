package engine_test

import (
	"encoding/base64"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
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

func withZhipuPrices(t *testing.T) {
	t.Helper()
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
}

func configureZhipuIntegration(t *testing.T, upstreamURL string, channelType channelcatalog.ChannelType, upstreamModel string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	credential := "zhipu-v4-secret"
	if channelType == channelcatalog.ChannelTypeZhipu {
		credential = "provider-id.provider-secret"
	}
	mapping, err := json.Marshal(map[string]string{"gpt-provider-contract": upstreamModel})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": channelType, "key": credential, "models": "gpt-provider-contract", "model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	return key, userID, channel
}

func assertZhipuSettled(t *testing.T, key string, userID int, channelID, prompt, completion int) {
	t.Helper()
	expectedQuota := billingsvc.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName)
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
	var settledChannel model.Channel
	require.NoError(t, model.DB.First(&settledChannel, channelID).Error)
	assert.EqualValues(t, expectedQuota, settledChannel.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Positive(t, reservation.RequestedQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
	assert.NotEmpty(t, reservation.ReservationID)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(consumeLog.Other), &other))
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])
}

func assertZhipuRefunded(t *testing.T, key string, userID int) {
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
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func zhipuRelayRequest(t *testing.T, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	return response
}

func TestZhipuLegacyAndV4OrdinaryRelayWireAndSettlementEndToEnd(t *testing.T) {
	withZhipuPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name             string
		channelType      channelcatalog.ChannelType
		clientPath       string
		clientBody       string
		upstreamModel    string
		upstreamPath     string
		upstreamResponse string
		promptTokens     int
		completionTokens int
		checkWire        func(*testing.T, *http.Request, map[string]any)
		checkResponse    func(*testing.T, string)
	}{
		{
			name: "legacy chat", channelType: channelcatalog.ChannelTypeZhipu,
			clientPath: "/v1/chat/completions", upstreamModel: "chatglm_pro",
			upstreamPath:     "/gateway/api/paas/v3/model-api/chatglm_pro/invoke",
			clientBody:       `{"model":"gpt-provider-contract","top_p":1,"group":"dashboard-only","messages":[{"role":"system","content":"policy"},{"role":"user","content":"hello"}]}`,
			upstreamResponse: `{"code":200,"msg":"ok","success":true,"data":{"task_id":"legacy-task","request_id":"legacy-request","task_status":"SUCCESS","choices":[{"role":"assistant","content":"legacy answer"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}}`,
			promptTokens:     7, completionTokens: 3,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				tokenString := request.Header.Get("Authorization")
				assert.NotContains(t, tokenString, "Bearer ")
				parsed, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
					assert.Equal(t, "SIGN", token.Header["sign_type"])
					return []byte("provider-secret"), nil
				}, jwt.WithoutClaimsValidation())
				require.NoError(t, err)
				assert.True(t, parsed.Valid)
				assert.Equal(t, "provider-id", parsed.Claims.(jwt.MapClaims)["api_key"])
				assert.NotContains(t, body, "model")
				assert.NotContains(t, body, "group")
				assert.EqualValues(t, 0.99, body["top_p"])
				prompt := body["prompt"].([]any)
				require.Len(t, prompt, 3)
				assert.Equal(t, "Okay", prompt[1].(map[string]any)["content"])
			},
			checkResponse: func(t *testing.T, response string) {
				assert.Contains(t, response, `"id":"legacy-task"`)
				assert.Contains(t, response, "legacy answer")
			},
		},
		{
			name: "v4 chat", channelType: channelcatalog.ChannelTypeZhipuV4,
			clientPath: "/v1/chat/completions", upstreamModel: "glm-4-plus",
			upstreamPath:     "/gateway/api/paas/v4/chat/completions",
			clientBody:       `{"model":"gpt-provider-contract","top_p":1,"max_tokens":20,"max_completion_tokens":9,"stop":"END","provider_extension":"drop","group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`,
			upstreamResponse: `{"id":"v4-chat","object":"chat.completion","model":"glm-4-plus","choices":[{"index":0,"message":{"role":"assistant","content":"v4 answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`,
			promptTokens:     8, completionTokens: 4,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "Bearer zhipu-v4-secret", request.Header.Get("Authorization"))
				assert.Equal(t, "glm-4-plus", body["model"])
				assert.EqualValues(t, 0.99, body["top_p"])
				assert.EqualValues(t, 9, body["max_tokens"])
				assert.Equal(t, []any{"END"}, body["stop"])
				assert.NotContains(t, body, "provider_extension")
				assert.NotContains(t, body, "group")
			},
			checkResponse: func(t *testing.T, response string) {
				assert.Contains(t, response, `"id":"v4-chat"`)
				assert.Contains(t, response, "v4 answer")
			},
		},
		{
			name: "v4 embedding", channelType: channelcatalog.ChannelTypeZhipuV4,
			clientPath: "/v1/embeddings", upstreamModel: "embedding-3",
			upstreamPath:     "/gateway/api/paas/v4/embeddings",
			clientBody:       `{"model":"gpt-provider-contract","input":["one","two"],"encoding_format":"float","dimensions":2,"group":"dashboard-only"}`,
			upstreamResponse: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"embedding-3","usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`,
			promptTokens:     5,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "Bearer zhipu-v4-secret", request.Header.Get("Authorization"))
				assert.Equal(t, "embedding-3", body["model"])
				assert.Equal(t, []any{"one", "two"}, body["input"])
				assert.EqualValues(t, 2, body["dimensions"])
				assert.NotContains(t, body, "group")
			},
			checkResponse: func(t *testing.T, response string) { assert.Contains(t, response, `"embedding"`) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.upstreamPath, request.URL.Path)
				assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				test.checkWire(t, request, body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.upstreamResponse)
				observed <- struct{}{}
			}))
			defer upstream.Close()
			key, userID, channel := configureZhipuIntegration(t, upstream.URL+"/gateway", test.channelType, test.upstreamModel)
			response := zhipuRelayRequest(t, key, test.clientPath, test.clientBody)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			<-observed
			test.checkResponse(t, response.Body.String())
			assertZhipuSettled(t, key, userID, channel.Id, test.promptTokens, test.completionTokens)
		})
	}
}

func TestZhipuLegacyAndV4StreamingWireUsageAndSettlementEndToEnd(t *testing.T) {
	withZhipuPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, upstreamModel, upstreamPath, streamBody string
		channelType                                   channelcatalog.ChannelType
		promptTokens, completionTokens                int
		checkWire                                     func(*testing.T, *http.Request, map[string]any)
		responseFragment                              string
	}{
		{
			name: "legacy", channelType: channelcatalog.ChannelTypeZhipu, upstreamModel: "chatglm_pro",
			upstreamPath: "/api/paas/v3/model-api/chatglm_pro/sse-invoke",
			streamBody:   "data:legacy stream\nmeta:{\"request_id\":\"stream-id\",\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"total_tokens\":12}}\n",
			promptTokens: 9, completionTokens: 3, responseFragment: `"content":"legacy stream"`,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.NotContains(t, request.Header.Get("Authorization"), "Bearer ")
				assert.NotContains(t, body, "stream")
			},
		},
		{
			name: "v4", channelType: channelcatalog.ChannelTypeZhipuV4, upstreamModel: "glm-4-flash",
			upstreamPath: "/api/paas/v4/chat/completions",
			streamBody: "data: {\"id\":\"v4-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"v4 stream\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\n" +
				"data: [DONE]\n\n",
			promptTokens: 10, completionTokens: 4, responseFragment: `"content":"v4 stream"`,
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "Bearer zhipu-v4-secret", request.Header.Get("Authorization"))
				assert.Equal(t, true, body["stream"])
				assert.Equal(t, true, body["stream_options"].(map[string]any)["include_usage"])
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.upstreamPath, request.URL.Path)
				assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				test.checkWire(t, request, body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.streamBody)
			}))
			defer upstream.Close()
			key, userID, channel := configureZhipuIntegration(t, upstream.URL, test.channelType, test.upstreamModel)
			response := zhipuRelayRequest(t, key, "/v1/chat/completions",
				`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Header().Get("Content-Type"), "text/event-stream")
			assert.Contains(t, response.Body.String(), test.responseFragment)
			assert.Contains(t, response.Body.String(), "data: [DONE]")
			assertZhipuSettled(t, key, userID, channel.Id, test.promptTokens, test.completionTokens)
		})
	}
}

func TestZhipuV4ImageDownloadNormalizationAndSettlementEndToEnd(t *testing.T) {
	withZhipuPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var upstream *httptest.Server
	var imageCalls atomic.Int32
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/paas/v4/images/generations":
			assert.Equal(t, "Bearer zhipu-v4-secret", request.Header.Get("Authorization"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "cogview-4", body["model"])
			assert.Equal(t, "draw a fox", body["prompt"])
			assert.EqualValues(t, 1, body["n"])
			assert.Equal(t, false, body["watermark_enabled"])
			_, _ = io.WriteString(w, `{"created":123,"data":[{"url":"`+upstream.URL+`/generated.png"}],"usage":{"prompt_tokens":6,"completion_tokens":1,"total_tokens":7}}`)
		case "/generated.png":
			imageCalls.Add(1)
			assert.Empty(t, request.Header.Get("Authorization"), "provider credentials must not reach result URLs")
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, "generated-image")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	key, userID, channel := configureZhipuIntegration(t, upstream.URL, channelcatalog.ChannelTypeZhipuV4, "cogview-4")
	response := zhipuRelayRequest(t, key, "/v1/images/generations",
		`{"model":"gpt-provider-contract","prompt":"draw a fox","n":1,"watermark_enabled":false,"group":"dashboard-only"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int32(1), imageCalls.Load())
	var output struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &output))
	assert.EqualValues(t, 123, output.Created)
	require.Len(t, output.Data, 1)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("generated-image")), output.Data[0].B64JSON)
	assertZhipuSettled(t, key, userID, channel.Id, 6, 1)
}

func TestZhipuV4NativeClaudeNonStreamAndStreamSettlementEndToEnd(t *testing.T) {
	withZhipuPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, upstreamBody, responseFragment string
		stream                               bool
		promptTokens, completionTokens       int
	}{
		{
			name: "non-stream", promptTokens: 7, completionTokens: 3, responseFragment: "native Zhipu",
			upstreamBody: `{"id":"message-zhipu","type":"message","role":"assistant","model":"glm-4.7","content":[{"type":"text","text":"native Zhipu"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`,
		},
		{
			name: "stream", stream: true, promptTokens: 8, completionTokens: 4, responseFragment: `"text":"native stream"`,
			upstreamBody: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"message-zhipu\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"glm-4.7\",\"content\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":0}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"native stream\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/api/anthropic/v1/messages", request.URL.Path)
				assert.Equal(t, "Bearer zhipu-v4-secret", request.Header.Get("Authorization"))
				assert.Empty(t, request.Header.Get("x-api-key"))
				assert.Empty(t, request.Header.Get("anthropic-version"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				assert.Equal(t, "glm-4.7", body["model"])
				assert.Equal(t, test.stream, body["stream"] == true)
				assert.NotContains(t, body, "group")
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, test.upstreamBody)
			}))
			defer upstream.Close()

			key, userID := setupClaudeRelay(t, upstream.URL, int(channelcatalog.ChannelTypeZhipuV4), "gpt-provider-contract")
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"key": "zhipu-v4-secret", "model_mapping": `{"gpt-provider-contract":"glm-4.7"}`,
			}).Error)
			streamField := ""
			if test.stream {
				streamField = `,"stream":true`
			}
			response := claudeRelayRequest(t, router.SetUpRouter(), key,
				`{"model":"gpt-provider-contract","max_tokens":32`+streamField+`,"group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.responseFragment)
			if test.stream {
				assert.Contains(t, response.Header().Get("Content-Type"), "text/event-stream")
			}
			assertZhipuSettled(t, key, userID, channel.Id, test.promptTokens, test.completionTokens)
		})
	}
}

func TestZhipuErrorsRefundAndUnsupportedModesNeverContactUpstream(t *testing.T) {
	withZhipuPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, path, body, upstreamBody string
		channelType                    channelcatalog.ChannelType
		upstreamModel                  string
		upstreamStatus                 int
		wantStatus                     int
		wantCalls                      int32
		invalidCredential              bool
	}{
		{
			name: "mapped v4 provider error", channelType: channelcatalog.ChannelTypeZhipuV4, upstreamModel: "glm-4-plus",
			path: "/v1/chat/completions", body: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			upstreamStatus: http.StatusTooManyRequests, upstreamBody: `{"error":{"message":"busy for zhipu-v4-secret","type":"rate_limit","code":"quota"}}`,
			wantStatus: http.StatusServiceUnavailable, wantCalls: 1,
		},
		{
			name: "malformed legacy credential", channelType: channelcatalog.ChannelTypeZhipu, upstreamModel: "chatglm_pro",
			path: "/v1/chat/completions", body: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			wantStatus: http.StatusInternalServerError, invalidCredential: true,
		},
		{
			name: "legacy embedding unsupported", channelType: channelcatalog.ChannelTypeZhipu, upstreamModel: "chatglm_pro",
			path: "/v1/embeddings", body: `{"model":"gpt-provider-contract","input":"hello"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "v4 image edit unsupported", channelType: channelcatalog.ChannelTypeZhipuV4, upstreamModel: "cogview-4",
			path: "/v1/images/edits", body: `{"model":"gpt-provider-contract","prompt":"edit","n":1}`,
			wantStatus: http.StatusInternalServerError,
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
			key, userID, channel := configureZhipuIntegration(t, upstream.URL, test.channelType, test.upstreamModel)
			updates := map[string]any{"status_code_mapping": `{"429":503}`}
			if test.invalidCredential {
				updates["key"] = "invalid-credential"
			}
			require.NoError(t, model.DB.Model(&channel).Updates(updates).Error)
			response := zhipuRelayRequest(t, key, test.path, test.body)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Equal(t, test.wantCalls, calls.Load())
			assert.NotContains(t, response.Body.String(), "zhipu-v4-secret")
			assertZhipuRefunded(t, key, userID)
		})
	}
}
