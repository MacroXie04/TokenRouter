package relay_test

import (
	"encoding/base64"
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

func withDifyPrices(t *testing.T) {
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

func configureDifyIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/dify")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{"gpt-provider-contract": "must-not-be-sent"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(constant.ChannelTypeDify), "model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func difyRelayRequest(t *testing.T, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	return response
}

func assertDifySettled(t *testing.T, key string, userID int, channelID, prompt, completion int) {
	t.Helper()
	expectedQuota := service.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&consumeLog).Error)
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

func assertDifyRefunded(t *testing.T, key string, userID int) {
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

func TestDifyBlockingUploadWireResponseAndSettlementEndToEnd(t *testing.T) {
	withDifyPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var uploadCalls, chatCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/dify/v1/files/upload":
			uploadCalls.Add(1)
			require.NoError(t, request.ParseMultipartForm(1<<20))
			assert.Equal(t, "dify-user", request.FormValue("user"))
			file, header, err := request.FormFile("file")
			require.NoError(t, err)
			defer file.Close()
			contents, err := io.ReadAll(file)
			require.NoError(t, err)
			assert.Equal(t, "image.png", header.Filename)
			assert.Equal(t, []byte("png-data"), contents)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"file-1"}`)
		case "/dify/v1/chat-messages":
			chatCalls.Add(1)
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			assert.Equal(t, "application/json", request.Header.Get("Accept"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.NotContains(t, body, "model")
			assert.Equal(t, "dify-user", body["user"])
			assert.Equal(t, "blocking", body["response_mode"])
			assert.Equal(t, "SYSTEM: \npolicy\nUSER: \nhello\n", body["query"])
			files := body["files"].([]any)
			require.Len(t, files, 1)
			assert.Equal(t, "file-1", files[0].(map[string]any)["upload_file_id"])
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"conversation_id":"dify-chat","answer":"answer","metadata":{"usage":{"prompt_tokens":6,"completion_tokens":2,"total_tokens":8}}}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer upstream.Close()

	key, userID, channel := configureDifyIntegration(t, upstream.URL)
	encoded := base64.StdEncoding.EncodeToString([]byte("png-data"))
	response := difyRelayRequest(t, key, "/v1/chat/completions", `{
		"model":"gpt-provider-contract","user":"dify-user","group":"dashboard-only",
		"messages":[
			{"role":"system","content":"policy"},
			{"role":"user","content":[
				{"type":"text","text":"hello"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,`+encoded+`","mime_type":"image/png"}}
			]}
		]
	}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, uploadCalls.Load())
	assert.EqualValues(t, 1, chatCalls.Load())
	var normalized map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &normalized))
	assert.Equal(t, "dify-chat", normalized["id"])
	assert.Equal(t, "", normalized["model"])
	assert.Equal(t, "answer", normalized["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
	assertDifySettled(t, key, userID, channel.Id, 6, 2)
}

func TestDifyStreamingWireUsageAndSettlementEndToEnd(t *testing.T) {
	withDifyPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	t.Setenv("DIFY_DEBUG", "true")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/dify/v1/chat-messages", request.URL.Path)
		assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "streaming", body["response_mode"])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"event\":\"agent_message\",\"answer\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"event\":\"node_finished\",\"data\":{\"node_type\":\"llm\",\"status\":\"ok\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"event\":\"message_end\",\"metadata\":{\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	key, userID, channel := configureDifyIntegration(t, upstream.URL)
	response := difyRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, response.Body.String(), `"content":"hello"`)
	assert.Contains(t, response.Body.String(), `"reasoning_content":"Node: llm ok\n"`)
	assert.Contains(t, response.Body.String(), "data: [DONE]")
	assertDifySettled(t, key, userID, channel.Id, 5, 3)
}

func TestDifyPartialStreamErrorSettlesAcceptedOutputWithoutRetryEndToEnd(t *testing.T) {
	withDifyPrices(t)
	t.Setenv("RETRY_TIMES", "3")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"event\":\"message\",\"answer\":\"accepted\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"event\":\"error\",\"code\":\"late_error\",\"message\":\"late failure\"}\n\n")
	}))
	defer upstream.Close()
	key, userID, channel := configureDifyIntegration(t, upstream.URL)
	response := difyRelayRequest(t, key, "/v1/chat/completions",
		`{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, calls.Load(), "a partial stream must never be retried")
	assert.Contains(t, response.Body.String(), `"content":"accepted"`)
	assert.NotContains(t, response.Body.String(), "late_error", "a second JSON error envelope must not corrupt SSE")
	assertDifySettled(t, key, userID, channel.Id, 1, 1)
}

func TestDifyHTTPAndUploadFailuresRefundAndSanitizeEndToEnd(t *testing.T) {
	withDifyPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, requestBody string
		handler           http.HandlerFunc
	}{
		{
			name:        "chat error",
			requestBody: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			handler: func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/dify/v1/chat-messages", request.URL.Path)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"code":"rate_limit","message":"credential sk-upstream rejected"}`)
			},
		},
		{
			name:        "upload error",
			requestBody: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("data")) + `"}}]}]}`,
			handler: func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/dify/v1/files/upload", request.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"code":"bad_file","message":"sk-upstream file rejected"}`)
			},
		},
		{
			name:        "stream error before first chunk",
			requestBody: `{"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			handler: func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/dify/v1/chat-messages", request.URL.Path)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"event\":\"error\",\"code\":\"bad_stream\",\"message\":\"sk-upstream stream rejected\"}\n\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(test.handler)
			defer upstream.Close()
			key, userID, _ := configureDifyIntegration(t, upstream.URL)
			response := difyRelayRequest(t, key, "/v1/chat/completions", test.requestBody)
			assert.GreaterOrEqual(t, response.Code, 400)
			assert.NotContains(t, response.Body.String(), "sk-upstream")
			assert.Contains(t, response.Body.String(), "[REDACTED]")
			assertDifyRefunded(t, key, userID)
		})
	}
}

func TestDifyUnsupportedModesFailBeforeProviderContactAndRefundEndToEnd(t *testing.T) {
	withDifyPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct{ name, path, body string }{
		{"completions", "/v1/completions", `{"model":"gpt-provider-contract","prompt":"hello"}`},
		{"embeddings", "/v1/embeddings", `{"model":"gpt-provider-contract","input":"hello"}`},
		{"images", "/v1/images/generations", `{"model":"gpt-provider-contract","prompt":"hello"}`},
		{"rerank", "/v1/rerank", `{"model":"gpt-provider-contract","query":"hello","documents":["one"]}`},
		{"responses", "/v1/responses", `{"model":"gpt-provider-contract","input":"hello"}`},
		{"claude", "/v1/messages", `{"model":"gpt-provider-contract","max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`},
		{"gemini", "/v1beta/models/gpt-provider-contract:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var contacted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Add(1) }))
			defer upstream.Close()
			key, userID, _ := configureDifyIntegration(t, upstream.URL)
			response := difyRelayRequest(t, key, test.path, test.body)
			assert.GreaterOrEqual(t, response.Code, 400, response.Body.String())
			assert.Zero(t, contacted.Load(), "unsupported Dify mode must fail before credentialed network")
			assertDifyRefunded(t, key, userID)
		})
	}
}
