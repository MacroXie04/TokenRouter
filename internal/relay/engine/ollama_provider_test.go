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
	"time"
)

type ollamaWireObservation struct {
	Path          string
	Authorization string
	ContentType   string
	Accept        string
	Body          map[string]any
}

func configureOllamaIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/ollama")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypeOllama),
		"key":  "ollama-secret",
		"model_mapping": `{
			"gpt-provider-contract":"llama3.2:latest",
			"text-embedding-3-small":"nomic-embed-text"
		}`,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func withOllamaPrices(t *testing.T) {
	t.Helper()
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-provider-contract":  {Prompt: 1, Completion: 2},
		"text-embedding-3-small": {Prompt: 1},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})
}

func TestOllamaNativeModesWireNormalizeAndSettleEndToEnd(t *testing.T) {
	withOllamaPrices(t)
	t.Setenv("RETRY_TIMES", "0")

	tests := []struct {
		name             string
		clientPath       string
		clientBody       string
		upstreamPath     string
		upstreamResponse string
		mappedModel      string
		wantObject       string
		wantPrompt       int
		wantCompletion   int
		checkWire        func(*testing.T, map[string]any)
		checkResponse    func(*testing.T, map[string]any)
	}{
		{
			name: "chat with tools and reasoning", clientPath: "/v1/chat/completions",
			clientBody: `{
				"model":"gpt-provider-contract","messages":[{"role":"user","content":"weather?"}],
				"tools":[{"type":"function","function":{"name":"weather","description":"lookup","parameters":{"type":"object"}}}],
				"temperature":0.2,"max_completion_tokens":64,"reasoning_effort":"medium","group":"must-not-leak"
			}`,
			upstreamPath: "/ollama/api/chat", mappedModel: "llama3.2:latest", wantObject: "chat.completion",
			upstreamResponse: `{
				"model":"llama3.2:latest","created_at":"2026-01-02T03:04:05Z",
				"message":{"role":"assistant","thinking":"checking","content":"","tool_calls":[{"id":"tool-1","function":{"name":"weather","arguments":{"city":"sf"}}}]},
				"done":true,"done_reason":"stop","prompt_eval_count":4,"eval_count":6
			}`,
			wantPrompt: 4, wantCompletion: 6,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "medium", body["think"])
				assert.EqualValues(t, 0.2, body["options"].(map[string]any)["temperature"])
				assert.EqualValues(t, 64, body["options"].(map[string]any)["num_predict"])
				assert.Equal(t, "weather", body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"])
				assert.NotContains(t, body, "group")
			},
			checkResponse: func(t *testing.T, response map[string]any) {
				choice := response["choices"].([]any)[0].(map[string]any)
				assert.Equal(t, "tool_calls", choice["finish_reason"])
				message := choice["message"].(map[string]any)
				assert.Equal(t, "checking", message["reasoning_content"])
				assert.Nil(t, message["content"])
				assert.Equal(t, "tool-1", message["tool_calls"].([]any)[0].(map[string]any)["id"])
			},
		},
		{
			name: "legacy completion", clientPath: "/v1/completions",
			clientBody:   `{"model":"gpt-provider-contract","prompt":"hello","suffix":"!","max_tokens":32}`,
			upstreamPath: "/ollama/api/generate", mappedModel: "llama3.2:latest", wantObject: "text_completion",
			upstreamResponse: `{"model":"llama3.2:latest","response":"answer","done":true,"done_reason":"length","prompt_eval_count":3,"eval_count":2}`,
			wantPrompt:       3, wantCompletion: 2,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "hello", body["prompt"])
				assert.Equal(t, "!", body["suffix"])
				assert.EqualValues(t, 32, body["options"].(map[string]any)["num_predict"])
				assert.NotContains(t, body, "messages")
			},
			checkResponse: func(t *testing.T, response map[string]any) {
				choice := response["choices"].([]any)[0].(map[string]any)
				assert.Equal(t, "answer", choice["text"])
				assert.Equal(t, "length", choice["finish_reason"])
			},
		},
		{
			name: "embeddings", clientPath: "/v1/embeddings",
			clientBody:   `{"model":"text-embedding-3-small","input":["alpha","beta"],"dimensions":2,"encoding_format":"float"}`,
			upstreamPath: "/ollama/api/embed", mappedModel: "nomic-embed-text", wantObject: "list",
			upstreamResponse: `{"model":"nomic-embed-text","embeddings":[[0.1,0.2],[0.3,0.4]],"prompt_eval_count":7}`,
			wantPrompt:       7,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, []any{"alpha", "beta"}, body["input"])
				assert.EqualValues(t, 2, body["dimensions"])
				assert.NotContains(t, body, "encoding_format")
			},
			checkResponse: func(t *testing.T, response map[string]any) {
				assert.Len(t, response["data"], 2)
				assert.Equal(t, "embedding", response["data"].([]any)[0].(map[string]any)["object"])
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan ollamaWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				var decoded map[string]any
				_ = json.Unmarshal(body, &decoded)
				observed <- ollamaWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					ContentType: request.Header.Get("Content-Type"), Accept: request.Header.Get("Accept"), Body: decoded,
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID, channel := configureOllamaIntegration(t, upstream.URL)
			handler := router.SetUpRouter()
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.clientBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())

			wire := <-observed
			assert.Equal(t, test.upstreamPath, wire.Path)
			assert.Equal(t, "Bearer ollama-secret", wire.Authorization)
			assert.Equal(t, "application/json", wire.ContentType)
			assert.Equal(t, "application/json", wire.Accept)
			assert.Equal(t, test.mappedModel, wire.Body["model"])
			test.checkWire(t, wire.Body)

			var normalized map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &normalized))
			assert.Equal(t, test.wantObject, normalized["object"])
			usage := normalized["usage"].(map[string]any)
			assert.EqualValues(t, test.wantPrompt, usage["prompt_tokens"])
			assert.EqualValues(t, test.wantCompletion, usage["completion_tokens"])
			test.checkResponse(t, normalized)

			var consumeLog model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
			assert.Equal(t, test.wantPrompt, consumeLog.PromptTokens)
			assert.Equal(t, test.wantCompletion, consumeLog.CompletionTokens)
			assert.Equal(t, channel.Id, consumeLog.ChannelId)
			assert.Greater(t, consumeLog.Quota, 0)

			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			assert.Equal(t, consumeLog.Quota, user.UsedQuota)
			assert.Equal(t, 500000-consumeLog.Quota, user.Quota)
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

func TestOllamaNDJSONStreamSettlesTerminalUsageEndToEnd(t *testing.T) {
	withOllamaPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	observed := make(chan ollamaWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- ollamaWireObservation{Path: request.URL.Path, Authorization: request.Header.Get("Authorization"), Body: decoded}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, strings.Join([]string{
			`{"model":"llama3.2:latest","created_at":"2026-01-02T03:04:05Z","message":{"role":"assistant","thinking":"plan","content":"hello"},"done":false}`,
			`{"model":"llama3.2:latest","created_at":"2026-01-02T03:04:06Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"weather","arguments":{"city":"sf"}}}]},"done":false}`,
			`{"model":"llama3.2:latest","created_at":"2026-01-02T03:04:07Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":3}`,
		}, "\n"))
	}))
	defer upstream.Close()

	key, userID, _ := configureOllamaIntegration(t, upstream.URL)
	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}],"stream":true}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	wire := <-observed
	assert.Equal(t, "/ollama/api/chat", wire.Path)
	assert.Equal(t, true, wire.Body["stream"])
	assert.Equal(t, "Bearer ollama-secret", wire.Authorization)
	assert.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	assert.Contains(t, response.Body.String(), `"reasoning_content":"plan"`)
	assert.Contains(t, response.Body.String(), `"finish_reason":"tool_calls"`)
	assert.Contains(t, response.Body.String(), `"prompt_tokens":5`)
	assert.Contains(t, response.Body.String(), `"completion_tokens":3`)
	assert.Contains(t, response.Body.String(), "data: [DONE]\n\n")

	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, 5, consumeLog.PromptTokens)
	assert.Equal(t, 3, consumeLog.CompletionTokens)
	assert.Greater(t, consumeLog.Quota, 0)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, consumeLog.Quota, reservation.ActualQuota)
}

func TestOllamaMalformedOversizedAndTimedOutResponsesRefundEndToEnd(t *testing.T) {
	withOllamaPrices(t)
	t.Setenv("RETRY_TIMES", "0")

	for _, test := range []struct {
		name    string
		timeout bool
		handler http.HandlerFunc
	}{
		{
			name: "malformed JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"done":`)
			},
		},
		{
			name: "oversized response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.CopyN(w, repeatingReader('x'), 16<<20+1)
			},
		},
		{
			name: "request deadline", timeout: true,
			handler: func(_ http.ResponseWriter, request *http.Request) {
				select {
				case <-request.Context().Done():
				case <-time.After(2 * time.Second):
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.timeout {
				t.Setenv("RELAY_TIMEOUT", "1")
			}
			upstream := httptest.NewServer(test.handler)
			defer upstream.Close()
			key, userID, _ := configureOllamaIntegration(t, upstream.URL)
			handler := router.SetUpRouter()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
				`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assert.GreaterOrEqual(t, response.Code, http.StatusInternalServerError, response.Body.String())

			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			assert.Equal(t, 500000, user.Quota)
			assert.Zero(t, user.UsedQuota)
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
		})
	}
}

type repeatingByteReader byte

func repeatingReader(value byte) io.Reader { return repeatingByteReader(value) }

func (reader repeatingByteReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(reader)
	}
	return len(buffer), nil
}
