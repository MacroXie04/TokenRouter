package relay_test

import (
	"bytes"
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
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

type miniMaxWireObservation struct {
	Path          string
	Authorization string
	APIKey        string
	Accept        string
	ContentType   string
	Body          map[string]any
}

func withMiniMaxPricing(t *testing.T) {
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

func configureMiniMaxIntegration(t *testing.T, upstreamURL, mappedModel string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/gateway")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{"gpt-provider-contract": mappedModel})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(constant.ChannelTypeMiniMax), "key": "minimax-secret",
		"model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func assertMiniMaxSettled(t *testing.T, key string, userID int, channel model.Channel, prompt, completion int) {
	t.Helper()
	wantQuota := service.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, "gpt-provider-contract", log.ModelName)
	assert.Equal(t, prompt, log.PromptTokens)
	assert.Equal(t, completion, log.CompletionTokens)
	assert.Equal(t, wantQuota, log.Quota)
	assert.Equal(t, channel.Id, log.ChannelId)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-wantQuota, user.Quota)
	assert.Equal(t, wantQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000-wantQuota, token.RemainQuota)
	assert.Equal(t, wantQuota, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, wantQuota, reservation.ActualQuota)
	assert.Positive(t, reservation.RequestedQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
}

func assertMiniMaxRefunded(t *testing.T, key string, userID int) {
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
		Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestMiniMaxChatJSONAndStreamWireUsageSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, upstreamResponse string
		stream                 bool
		prompt, completion     int
	}{
		{
			name: "JSON", prompt: 7, completion: 3,
			upstreamResponse: `{"id":"chat-minimax","object":"chat.completion","model":"MiniMax-M2.7-highspeed","choices":[{"index":0,"message":{"role":"assistant","content":"MiniMax JSON"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
		},
		{
			name: "stream", stream: true, prompt: 8, completion: 4,
			upstreamResponse: "data: {\"id\":\"stream-minimax\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"MiniMax stream\"}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n\n" +
				"data: [DONE]\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMiniMaxPricing(t)
			observed := make(chan miniMaxWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				assert.NoError(t, err)
				var decoded map[string]any
				assert.NoError(t, json.Unmarshal(body, &decoded))
				observed <- miniMaxWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					APIKey: request.Header.Get("x-api-key"), Accept: request.Header.Get("Accept"),
					ContentType: request.Header.Get("Content-Type"), Body: decoded,
				}
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = io.WriteString(w, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID, channel := configureMiniMaxIntegration(t, upstream.URL, "MiniMax-M2.7-highspeed")
			body := `{"model":"gpt-provider-contract","max_tokens":16,"group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`
			if test.stream {
				body = `{"model":"gpt-provider-contract","max_tokens":16,"stream":true,"stream_options":{"include_usage":true},"group":"dashboard-only","messages":[{"role":"user","content":"hello"}]}`
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), "MiniMax")
			if test.stream {
				assert.Equal(t, 1, strings.Count(response.Body.String(), "data: [DONE]"))
			}

			wire := <-observed
			assert.Equal(t, "/gateway/v1/text/chatcompletion_v2", wire.Path)
			assert.Equal(t, "Bearer minimax-secret", wire.Authorization)
			assert.Empty(t, wire.APIKey)
			assert.Equal(t, "application/json", wire.ContentType)
			if test.stream {
				assert.Equal(t, "text/event-stream", wire.Accept)
			} else {
				assert.Equal(t, "application/json", wire.Accept)
			}
			assert.Equal(t, "MiniMax-M2.7-highspeed", wire.Body["model"])
			assert.NotContains(t, wire.Body, "group")
			assertMiniMaxSettled(t, key, userID, channel, test.prompt, test.completion)
		})
	}
}

func TestMiniMaxImageAndSpeechWireConversionSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, clientPath, clientBody, mappedModel, upstreamPath, upstreamResponse string
		responseMarker                                                            string
		completion                                                                int
		checkWire                                                                 func(*testing.T, map[string]any)
	}{
		{
			name: "image", clientPath: "/v1/images/generations", mappedModel: "image-01-live",
			upstreamPath: "/gateway/v1/image_generation", completion: 2, responseMarker: "aW1nMQ==",
			clientBody:       `{"model":"gpt-provider-contract","prompt":"a fox in snowfall","n":2,"size":"1536x1024","response_format":"b64_json","prompt_optimizer":true,"watermark":false}`,
			upstreamResponse: `{"data":{"image_base64":["aW1nMQ==","aW1nMg=="]},"metadata":{"request_id":"image-request"},"base_resp":{"status_code":0}}`,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "a fox in snowfall", body["prompt"])
				assert.Equal(t, "3:2", body["aspect_ratio"])
				assert.Equal(t, "base64", body["response_format"])
				assert.EqualValues(t, 2, body["n"])
				assert.Equal(t, true, body["prompt_optimizer"])
				assert.Equal(t, false, body["aigc_watermark"])
			},
		},
		{
			name: "speech", clientPath: "/v1/audio/speech", mappedModel: "speech-02-hd",
			upstreamPath: "/gateway/v1/t2a_v2", responseMarker: "deterministic audio",
			clientBody:       `{"model":"gpt-provider-contract","input":"say this clearly","voice":"English_Graceful_Lady","speed":1.25,"response_format":"wav","metadata":{"language_boost":"English","output_format":"hex","voice_setting":{"voice_id":"custom-voice","vol":2},"audio_setting":{"sample_rate":32000}}}`,
			upstreamResponse: `{"data":{"audio":"64657465726d696e697374696320617564696f","status":0},"extra_info":{"usage_characters":16},"base_resp":{"status_code":0}}`,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "say this clearly", body["text"])
				assert.Equal(t, "English", body["language_boost"])
				assert.Equal(t, "hex", body["output_format"])
				voice := body["voice_setting"].(map[string]any)
				assert.Equal(t, "custom-voice", voice["voice_id"])
				assert.EqualValues(t, 1.25, voice["speed"])
				assert.EqualValues(t, 2, voice["vol"])
				audio := body["audio_setting"].(map[string]any)
				assert.Equal(t, "wav", audio["format"])
				assert.EqualValues(t, 32000, audio["sample_rate"])
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMiniMaxPricing(t)
			observed := make(chan miniMaxWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				assert.NoError(t, err)
				var decoded map[string]any
				assert.NoError(t, json.Unmarshal(body, &decoded))
				observed <- miniMaxWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					APIKey: request.Header.Get("x-api-key"), Accept: request.Header.Get("Accept"),
					ContentType: request.Header.Get("Content-Type"), Body: decoded,
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID, channel := configureMiniMaxIntegration(t, upstream.URL, test.mappedModel)
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.clientBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.responseMarker)

			wire := <-observed
			assert.Equal(t, test.upstreamPath, wire.Path)
			assert.Equal(t, "Bearer minimax-secret", wire.Authorization)
			assert.Empty(t, wire.APIKey)
			assert.Equal(t, "application/json", wire.Accept)
			assert.Equal(t, "application/json", wire.ContentType)
			assert.Equal(t, test.mappedModel, wire.Body["model"])
			test.checkWire(t, wire.Body)

			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
			assert.Positive(t, log.PromptTokens)
			assert.Equal(t, test.completion, log.CompletionTokens)
			assertMiniMaxSettled(t, key, userID, channel, log.PromptTokens, test.completion)
		})
	}
}

func TestMiniMaxNativeClaudeJSONAndStreamSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, upstreamResponse, responseMarker string
		stream                                 bool
		prompt, completion                     int
	}{
		{
			name: "JSON", prompt: 6, completion: 3, responseMarker: "native MiniMax",
			upstreamResponse: `{"id":"msg-minimax","type":"message","role":"assistant","model":"MiniMax-M2.7","content":[{"type":"text","text":"native MiniMax"}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":3}}`,
		},
		{
			name: "stream", stream: true, prompt: 8, completion: 5, responseMarker: "native stream",
			upstreamResponse: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-minimax-stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M2.7\",\"content\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":0}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"native stream\"}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMiniMaxPricing(t)
			observed := make(chan miniMaxWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				assert.NoError(t, err)
				var decoded map[string]any
				assert.NoError(t, json.Unmarshal(body, &decoded))
				observed <- miniMaxWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					APIKey: request.Header.Get("x-api-key"), Accept: request.Header.Get("Accept"),
					ContentType: request.Header.Get("Content-Type"), Body: decoded,
				}
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = io.WriteString(w, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID := setupClaudeRelay(t, upstream.URL+"/gateway", int(constant.ChannelTypeMiniMax), "gpt-provider-contract")
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"key": "minimax-secret", "model_mapping": `{"gpt-provider-contract":"MiniMax-M2.7"}`,
			}).Error)
			require.NoError(t, model.DB.First(&channel, channel.Id).Error)
			body := `{"model":"gpt-provider-contract","max_tokens":32,"system":"follow rules","messages":[{"role":"user","content":"hello"}]}`
			if test.stream {
				body = `{"model":"gpt-provider-contract","max_tokens":32,"stream":true,"system":"follow rules","messages":[{"role":"user","content":"hello"}]}`
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("x-api-key", "client-key-must-not-forward")
			request.Header.Set("anthropic-version", "client-version-must-not-forward")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.responseMarker)

			wire := <-observed
			assert.Equal(t, "/gateway/anthropic/v1/messages", wire.Path)
			assert.Equal(t, "Bearer minimax-secret", wire.Authorization)
			assert.Empty(t, wire.APIKey)
			assert.Equal(t, "application/json", wire.ContentType)
			if test.stream {
				assert.Equal(t, "text/event-stream", wire.Accept)
			} else {
				assert.Equal(t, "application/json", wire.Accept)
			}
			assert.Equal(t, "MiniMax-M2.7", wire.Body["model"])
			assert.EqualValues(t, 32, wire.Body["max_tokens"])
			assert.Equal(t, "follow rules", wire.Body["system"])
			assertMiniMaxSettled(t, key, userID, channel, test.prompt, test.completion)
		})
	}
}

func TestMiniMaxMappedErrorsBoundsAndUnsupportedModeRefundEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name, clientPath, clientBody string
		upstreamStatus               int
		writeResponse                func(http.ResponseWriter)
		wantStatus                   int
		wantCalls                    int32
	}{
		{
			name: "mapped sanitized HTTP error", clientPath: "/v1/chat/completions", upstreamStatus: http.StatusTooManyRequests,
			clientBody: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			writeResponse: func(w http.ResponseWriter) {
				_, _ = io.WriteString(w, `{"error":{"message":"MiniMax busy for minimax-secret","type":"rate_limit_error"}}`)
			},
			wantStatus: http.StatusServiceUnavailable, wantCalls: 1,
		},
		{
			name: "oversized success response", clientPath: "/v1/chat/completions", upstreamStatus: http.StatusOK,
			clientBody: `{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			writeResponse: func(w http.ResponseWriter) {
				remaining := relaycommon.MaxUpstreamJSONBodyBytes + 1
				chunk := bytes.Repeat([]byte{'x'}, 1<<20)
				for remaining > 0 {
					write := int64(len(chunk))
					if write > remaining {
						write = remaining
					}
					_, _ = w.Write(chunk[:write])
					remaining -= write
				}
			},
			wantStatus: http.StatusInternalServerError, wantCalls: 1,
		},
		{
			name: "unsupported embeddings", clientPath: "/v1/embeddings",
			clientBody: `{"model":"gpt-provider-contract","input":"hello"}`,
			wantStatus: http.StatusInternalServerError, wantCalls: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMiniMaxPricing(t)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				assert.Equal(t, "/gateway/v1/text/chatcompletion_v2", request.URL.Path)
				assert.Equal(t, "Bearer minimax-secret", request.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.upstreamStatus)
				if test.writeResponse != nil {
					test.writeResponse(w)
				}
			}))
			defer upstream.Close()

			key, userID, channel := configureMiniMaxIntegration(t, upstream.URL, "MiniMax-M2.7")
			require.NoError(t, model.DB.Model(&channel).Update("status_code_mapping", `{"429":503}`).Error)
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.clientBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Equal(t, test.wantCalls, calls.Load())
			assert.NotContains(t, response.Body.String(), "minimax-secret")
			assertMiniMaxRefunded(t, key, userID)
		})
	}
}
