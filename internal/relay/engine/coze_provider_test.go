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

func withCozePrices(t *testing.T) {
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

func configureCozeIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/coze")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeCoze), "other": "bot_123",
		"model_mapping": `{"gpt-provider-contract":"deepseek-v3"}`,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	return key, userID, channel
}

func cozeRelayRequest(t *testing.T, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	return response
}

func assertCozeSettled(t *testing.T, key string, userID int, channel model.Channel, prompt, completion int) {
	t.Helper()
	expectedQuota := billingsvc.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName)
	assert.Equal(t, prompt, consumeLog.PromptTokens)
	assert.Equal(t, completion, consumeLog.CompletionTokens)
	assert.Equal(t, expectedQuota, consumeLog.Quota)
	assert.Equal(t, channel.Id, consumeLog.ChannelId)

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
	require.NoError(t, model.DB.First(&settledChannel, channel.Id).Error)
	assert.EqualValues(t, expectedQuota, settledChannel.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
}

func assertCozeAcceptedSettled(t *testing.T, key string, userID int, channel model.Channel) {
	t.Helper()
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName)
	assert.Positive(t, consumeLog.PromptTokens)
	assert.Zero(t, consumeLog.CompletionTokens)
	assert.Positive(t, consumeLog.Quota)
	assert.Equal(t, channel.Id, consumeLog.ChannelId)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-consumeLog.Quota, user.Quota)
	assert.Equal(t, consumeLog.Quota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000-consumeLog.Quota, token.RemainQuota)
	assert.Equal(t, consumeLog.Quota, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, consumeLog.Quota, reservation.ActualQuota)
}

func assertCozeRefunded(t *testing.T, key string, userID int) {
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
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestCozeBlockingWirePollingAndSettlementEndToEnd(t *testing.T) {
	withCozePrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var createCalls, retrieveCalls, detailCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/coze/v3/chat":
			createCalls.Add(1)
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "application/json", request.Header.Get("Accept"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "bot_123", body["bot_id"])
			assert.Equal(t, "coze-user", body["user_id"])
			assert.NotContains(t, body, "stream")
			assert.NotContains(t, body, "model")
			messages := body["additional_messages"].([]any)
			require.Len(t, messages, 1)
			assert.Equal(t, "hello", messages[0].(map[string]any)["content"])
			_, _ = io.WriteString(w, `{"code":0,"data":{"id":"chat-1","conversation_id":"conversation-1","status":"created"}}`)
		case "/coze/v3/chat/retrieve":
			retrieveCalls.Add(1)
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "chat-1", request.URL.Query().Get("chat_id"))
			assert.Equal(t, "conversation-1", request.URL.Query().Get("conversation_id"))
			_, _ = io.WriteString(w, `{"code":0,"data":{"id":"chat-1","conversation_id":"conversation-1","status":"completed","usage":{"input_count":5,"output_count":3,"token_count":8}}}`)
		case "/coze/v3/chat/message/list":
			detailCalls.Add(1)
			assert.Equal(t, http.MethodGet, request.Method)
			_, _ = io.WriteString(w, `{"code":0,"data":[{"id":"message-1","type":"answer","role":"assistant","chat_id":"chat-1","conversation_id":"conversation-1","content":"hello from Coze","created_at":1700000000}]}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer upstream.Close()

	key, userID, channel := configureCozeIntegration(t, upstream.URL)
	response := cozeRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","user":"coze-user","messages":[{"role":"system","content":"owned by bot"},{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, createCalls.Load())
	assert.EqualValues(t, 1, retrieveCalls.Load())
	assert.EqualValues(t, 1, detailCalls.Load())
	var output map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &output))
	assert.Regexp(t, `^chatcmpl-coze-[0-9a-f]{32}$`, output["id"])
	assert.Equal(t, "gpt-provider-contract", output["model"])
	assert.Equal(t, "hello from Coze", output["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
	assertCozeSettled(t, key, userID, channel, 5, 3)
}

func TestCozeStreamingWireUsageAndSettlementEndToEnd(t *testing.T) {
	withCozePrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/coze/v3/chat", request.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, true, body["stream"])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: conversation.message.delta\ndata: {\"id\":\"message-1\",\"type\":\"answer\",\"content\":\"streamed answer\"}\n\n")
		_, _ = io.WriteString(w, "event: conversation.chat.completed\ndata: {\"id\":\"chat-1\",\"status\":\"completed\",\"usage\":{\"input_count\":6,\"output_count\":2,\"token_count\":8}}\n\n")
	}))
	defer upstream.Close()
	key, userID, channel := configureCozeIntegration(t, upstream.URL)
	response := cozeRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, calls.Load())
	assert.Contains(t, response.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, response.Body.String(), `"content":"streamed answer"`)
	assert.Contains(t, response.Body.String(), `"finish_reason":"stop"`)
	assert.Contains(t, response.Body.String(), "data: [DONE]")
	assertCozeSettled(t, key, userID, channel, 6, 2)
}

func TestCozeRejectedRequestsRefundAndRedactEndToEnd(t *testing.T) {
	withCozePrices(t)
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP rejection", http.StatusTooManyRequests, `{"error":{"message":"sk-upstream rate limited"}}`},
		{"creation rejection", http.StatusOK, `{"code":4001,"msg":"sk-upstream rejected bot"}`},
		{"early stream rejection", http.StatusOK, "event: error\ndata: {\"code\":4002,\"message\":\"sk-upstream stream rejected\"}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()
			key, userID, _ := configureCozeIntegration(t, upstream.URL)
			stream := strings.Contains(test.name, "stream")
			body := `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`
			if stream {
				body = `{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`
			}
			response := cozeRelayRequest(t, key, "/v1/chat/completions", body)
			assert.GreaterOrEqual(t, response.Code, 400, response.Body.String())
			assert.EqualValues(t, 1, calls.Load())
			assert.NotContains(t, response.Body.String(), "sk-upstream")
			assert.Contains(t, response.Body.String(), "[REDACTED]")
			assertCozeRefunded(t, key, userID)
		})
	}
}

func TestCozeAcceptedPollOrDetailFailureSettlesWithoutRetryEndToEnd(t *testing.T) {
	withCozePrices(t)
	t.Setenv("RETRY_TIMES", "3")
	for _, failPath := range []string{"retrieve", "detail"} {
		t.Run(failPath, func(t *testing.T) {
			var createCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/coze/v3/chat":
					createCalls.Add(1)
					_, _ = io.WriteString(w, `{"code":0,"data":{"id":"chat-1","conversation_id":"conversation-1"}}`)
				case "/coze/v3/chat/retrieve":
					if failPath == "retrieve" {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"error":{"message":"sk-upstream poll failed"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"id":"chat-1","conversation_id":"conversation-1","status":"completed","usage":{"input_count":5,"output_count":2,"token_count":7}}}`)
				case "/coze/v3/chat/message/list":
					w.WriteHeader(http.StatusBadGateway)
					_, _ = io.WriteString(w, `{"error":{"message":"sk-upstream detail failed"}}`)
				default:
					http.NotFound(w, request)
				}
			}))
			defer upstream.Close()
			key, userID, channel := configureCozeIntegration(t, upstream.URL)
			response := cozeRelayRequest(t, key, "/v1/chat/completions",
				`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"accepted work"}]}`)
			assert.GreaterOrEqual(t, response.Code, 400, response.Body.String())
			assert.EqualValues(t, 1, createCalls.Load(), "accepted Coze work must not be retried")
			assert.NotContains(t, response.Body.String(), "sk-upstream")
			assert.Contains(t, response.Body.String(), "[REDACTED]")
			assertCozeAcceptedSettled(t, key, userID, channel)
		})
	}
}

func TestCozeUnsupportedModesFailBeforeProviderContactEndToEnd(t *testing.T) {
	withCozePrices(t)
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct{ name, path, body string }{
		{"completions", "/v1/completions", `{"model":"gpt-provider-contract","prompt":"hello"}`},
		{"embeddings", "/v1/embeddings", `{"model":"gpt-provider-contract","input":"hello"}`},
		{"images", "/v1/images/generations", `{"model":"gpt-provider-contract","prompt":"hello"}`},
		{"responses", "/v1/responses", `{"model":"gpt-provider-contract","input":"hello"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var contacted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Add(1) }))
			defer upstream.Close()
			key, userID, _ := configureCozeIntegration(t, upstream.URL)
			response := cozeRelayRequest(t, key, test.path, test.body)
			assert.GreaterOrEqual(t, response.Code, 400, response.Body.String())
			assert.Zero(t, contacted.Load(), "unsupported Coze mode must fail before provider contact")
			assertCozeRefunded(t, key, userID)
		})
	}
}
