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
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type cloudflareWireObservation struct {
	Path          string
	Authorization string
	ContentType   string
	Accept        string
	Body          []byte
}

func configureCloudflareIntegration(t *testing.T, upstreamURL, modelMapping string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/gateway")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(constant.ChannelCloudflare),
		"key":           "cloudflare-secret",
		"other":         "account_123",
		"model_mapping": modelMapping,
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func withCloudflarePricing(t *testing.T) {
	t.Helper()
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"gpt-provider-contract":  {Prompt: 2, Completion: 4},
		"text-embedding-3-small": {Prompt: 2, Completion: 4},
	})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: `{"gpt-provider-contract":"tiered_expr","text-embedding-3-small":"tiered_expr"}`,
		setting.ModelBillingExprOption: `{"gpt-provider-contract":"p * 2 + c * 4","text-embedding-3-small":"p * 2 + c * 4"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.ModelBillingModeOption: `{}`,
			setting.ModelBillingExprOption: `{}`,
		})
	})
}

func assertCloudflareSettlement(t *testing.T, key string, userID, channelID, promptTokens, completionTokens int) {
	t.Helper()
	wantQuota := promptTokens + completionTokens*2
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, promptTokens, consumeLog.PromptTokens)
	assert.Equal(t, completionTokens, consumeLog.CompletionTokens)
	assert.Equal(t, wantQuota, consumeLog.Quota)
	assert.Equal(t, channelID, consumeLog.ChannelId)

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
}

func TestCloudflareChatStreamWireUsageAndSettlementEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	observed := make(chan cloudflareWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		observed <- cloudflareWireObservation{
			Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
			ContentType: request.Header.Get("Content-Type"), Accept: request.Header.Get("Accept"), Body: body,
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `data: {"id":"provider-one","object":"chat.completion.chunk","model":"provider-model","choices":[{"index":0,"delta":{"content":"cloud "}}]}`+"\n\n")
		_, _ = io.WriteString(writer, `data: {"id":"provider-two","object":"chat.completion.chunk","model":"provider-model","choices":[{"index":0,"delta":{"content":"flare"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	key, userID, channel := configureCloudflareIntegration(t, upstream.URL,
		`{"gpt-provider-contract":"@cf/meta/llama-3.1-8b-instruct"}`)
	withCloudflarePricing(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-provider-contract","stream":true,"max_tokens":64,
		"stream_options":{"include_usage":true},"group":"dashboard-only",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	assert.Contains(t, response.Body.String(), `"content":"cloud "`)
	assert.Contains(t, response.Body.String(), `"model":"@cf/meta/llama-3.1-8b-instruct"`)
	assert.Contains(t, response.Body.String(), `"role":"assistant"`)
	assert.Equal(t, 1, strings.Count(response.Body.String(), "data: [DONE]"))

	wire := <-observed
	assert.Equal(t, "/gateway/client/v4/accounts/account_123/ai/v1/chat/completions", wire.Path)
	assert.Equal(t, "Bearer cloudflare-secret", wire.Authorization)
	assert.Equal(t, "application/json", wire.ContentType)
	assert.Equal(t, "text/event-stream", wire.Accept)
	var requestBody map[string]any
	require.NoError(t, json.Unmarshal(wire.Body, &requestBody))
	assert.Equal(t, "@cf/meta/llama-3.1-8b-instruct", requestBody["model"])
	assert.Equal(t, true, requestBody["stream_options"].(map[string]any)["include_usage"])
	assert.NotContains(t, requestBody, "group")

	promptTokens := relaycommon.CountTokens("hello")
	completionTokens := relaycommon.CountTokens("cloud flare")
	assertCloudflareSettlement(t, key, userID, channel.Id, promptTokens, completionTokens)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Greater(t, reservation.ReservedQuota, reservation.ActualQuota, "unused completion hold must be released")
}

func TestCloudflareOpenAICompatibleModesWireAndSettleEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name             string
		clientPath       string
		clientBody       string
		upstreamPath     string
		upstreamResponse string
		mapping          string
		wantPrompt       int
		wantCompletion   int
		checkResponse    func(*testing.T, map[string]any)
	}{
		{
			name: "embeddings", clientPath: "/v1/embeddings",
			clientBody:       `{"model":"text-embedding-3-small","input":["one","two"],"group":"dashboard-only"}`,
			upstreamPath:     "/gateway/client/v4/accounts/account_123/ai/v1/embeddings",
			upstreamResponse: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"input_tokens":5,"total_tokens":5}}`,
			mapping:          `{"text-embedding-3-small":"@cf/baai/bge-base-en-v1.5"}`,
			wantPrompt:       5,
			checkResponse: func(t *testing.T, response map[string]any) {
				assert.Equal(t, "@cf/baai/bge-base-en-v1.5", response["model"])
				assert.Len(t, response["data"], 1)
			},
		},
		{
			name: "responses", clientPath: "/v1/responses",
			clientBody:       `{"model":"gpt-provider-contract","input":"hello","instructions":"brief"}`,
			upstreamPath:     "/gateway/client/v4/accounts/account_123/ai/v1/responses",
			upstreamResponse: `{"id":"resp_cf","object":"response","output":[],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`,
			mapping:          `{"gpt-provider-contract":"@cf/meta/llama-3.1-8b-instruct"}`,
			wantPrompt:       4, wantCompletion: 2,
			checkResponse: func(t *testing.T, response map[string]any) {
				assert.Equal(t, "resp_cf", response["id"])
				assert.EqualValues(t, 4, response["usage"].(map[string]any)["input_tokens"])
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan cloudflareWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				observed <- cloudflareWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					ContentType: request.Header.Get("Content-Type"), Accept: request.Header.Get("Accept"), Body: body,
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID, channel := configureCloudflareIntegration(t, upstream.URL, test.mapping)
			withCloudflarePricing(t)
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.clientBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())

			wire := <-observed
			assert.Equal(t, test.upstreamPath, wire.Path)
			assert.Equal(t, "Bearer cloudflare-secret", wire.Authorization)
			assert.Equal(t, "application/json", wire.ContentType)
			assert.Equal(t, "application/json", wire.Accept)
			var requestBody map[string]any
			require.NoError(t, json.Unmarshal(wire.Body, &requestBody))
			assert.NotContains(t, requestBody, "group")
			if strings.Contains(test.mapping, "text-embedding") {
				assert.Equal(t, "@cf/baai/bge-base-en-v1.5", requestBody["model"])
			} else {
				assert.Equal(t, "@cf/meta/llama-3.1-8b-instruct", requestBody["model"])
			}
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &decoded))
			test.checkResponse(t, decoded)
			assertCloudflareSettlement(t, key, userID, channel.Id, test.wantPrompt, test.wantCompletion)
		})
	}
}

func TestCloudflareRunModesWireNormalizeAndSettleEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name             string
		clientPath       string
		mappedModel      string
		upstreamResponse string
		makeRequest      func(*testing.T, string) *http.Request
		checkWire        func(*testing.T, cloudflareWireObservation)
		checkResponse    func(*testing.T, map[string]any)
		promptText       string
		completionText   string
	}{
		{
			name: "completion", clientPath: "/v1/completions", mappedModel: "@cf/meta/llama-3.1-8b-instruct",
			upstreamResponse: `{"result":{"response":"native answer"},"success":true,"errors":[],"messages":[]}`,
			makeRequest: func(_ *testing.T, key string) *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(
					`{"model":"gpt-provider-contract","prompt":"prompt text","max_tokens":32}`))
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Content-Type", "application/json")
				return request
			},
			checkWire: func(t *testing.T, wire cloudflareWireObservation) {
				assert.Equal(t, "application/json", wire.ContentType)
				var body map[string]any
				require.NoError(t, json.Unmarshal(wire.Body, &body))
				assert.Equal(t, "prompt text", body["prompt"])
				assert.EqualValues(t, 32, body["max_tokens"])
				assert.NotContains(t, body, "model")
			},
			checkResponse: func(t *testing.T, response map[string]any) {
				assert.Equal(t, "text_completion", response["object"])
				assert.Equal(t, "native answer", response["choices"].([]any)[0].(map[string]any)["text"])
			},
			promptText: "prompt text", completionText: "native answer",
		},
		{
			name: "audio transcription", clientPath: "/v1/audio/transcriptions", mappedModel: "@cf/openai/whisper",
			upstreamResponse: `{"result":{"text":"transcribed words"},"success":true,"errors":[],"messages":[]}`,
			makeRequest: func(t *testing.T, key string) *http.Request {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				part, err := writer.CreateFormFile("file", "sample.wav")
				require.NoError(t, err)
				_, err = part.Write([]byte("RIFF-deterministic-audio"))
				require.NoError(t, err)
				require.NoError(t, writer.WriteField("model", "gpt-provider-contract"))
				require.NoError(t, writer.Close())
				request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Content-Type", writer.FormDataContentType())
				return request
			},
			checkWire: func(t *testing.T, wire cloudflareWireObservation) {
				assert.Equal(t, "application/octet-stream", wire.ContentType)
				assert.Equal(t, []byte("RIFF-deterministic-audio"), wire.Body)
			},
			checkResponse: func(t *testing.T, response map[string]any) {
				assert.Equal(t, "transcribed words", response["text"])
				assert.NotContains(t, response, "result")
			},
			completionText: "transcribed words",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan cloudflareWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				observed <- cloudflareWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					ContentType: request.Header.Get("Content-Type"), Accept: request.Header.Get("Accept"), Body: body,
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.upstreamResponse)
			}))
			defer upstream.Close()

			mapping := `{"gpt-provider-contract":"` + test.mappedModel + `"}`
			key, userID, channel := configureCloudflareIntegration(t, upstream.URL, mapping)
			withCloudflarePricing(t)
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, test.makeRequest(t, key))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())

			wire := <-observed
			assert.Equal(t, "/gateway/client/v4/accounts/account_123/ai/run/"+test.mappedModel, wire.Path)
			assert.Equal(t, "Bearer cloudflare-secret", wire.Authorization)
			assert.Equal(t, "application/json", wire.Accept)
			test.checkWire(t, wire)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &decoded))
			test.checkResponse(t, decoded)
			assertCloudflareSettlement(t, key, userID, channel.Id,
				relaycommon.CountTokens(test.promptText), relaycommon.CountTokens(test.completionText))
		})
	}
}

func TestCloudflareMappedErrorAndResponseBoundRefundEndToEnd(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name       string
		status     int
		writeBody  func(http.ResponseWriter)
		wantStatus int
	}{
		{
			name: "mapped sanitized error", status: http.StatusTooManyRequests, wantStatus: http.StatusServiceUnavailable,
			writeBody: func(writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `{"error":{"message":"Cloudflare busy for cloudflare-secret","type":"rate_limit_error"}}`)
			},
		},
		{
			name: "oversized success response", status: http.StatusOK, wantStatus: http.StatusInternalServerError,
			writeBody: func(writer http.ResponseWriter) {
				for remaining := 16<<20 + 1; remaining > 0; {
					chunk := remaining
					if chunk > 1<<20 {
						chunk = 1 << 20
					}
					_, _ = writer.Write(bytes.Repeat([]byte{'x'}, chunk))
					remaining -= chunk
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/gateway/client/v4/accounts/account_123/ai/v1/chat/completions", request.URL.Path)
				assert.Equal(t, "Bearer cloudflare-secret", request.Header.Get("Authorization"))
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				test.writeBody(writer)
			}))
			defer upstream.Close()

			key, userID, _ := configureCloudflareIntegration(t, upstream.URL,
				`{"gpt-provider-contract":"@cf/meta/llama-3.1-8b-instruct"}`)
			withCloudflarePricing(t)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Update("status_code_mapping", `{"429":503}`).Error)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
				`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.NotContains(t, response.Body.String(), "cloudflare-secret")

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
				Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).Count(&consumeLogs).Error)
			assert.Zero(t, consumeLogs)
		})
	}
}

func TestCloudflareUnsupportedModesAndNativeProtocolsFailBeforeCredentialDispatch(t *testing.T) {
	t.Setenv("RETRY_TIMES", "0")
	tests := []struct {
		name    string
		request func(string) *http.Request
	}{
		{
			name: "Claude Messages",
			request: func(key string) *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
					`{"model":"gpt-provider-contract","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Content-Type", "application/json")
				return request
			},
		},
		{
			name: "Gemini generateContent",
			request: func(key string) *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-provider-contract:generateContent",
					strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Content-Type", "application/json")
				return request
			},
		},
		{
			name: "OpenAI moderations",
			request: func(key string) *http.Request {
				request := httptest.NewRequest(http.MethodPost, "/v1/moderations",
					strings.NewReader(`{"model":"gpt-provider-contract","input":"hello"}`))
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Content-Type", "application/json")
				return request
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var contacts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				contacts.Add(1)
			}))
			defer upstream.Close()
			key, userID, _ := configureCloudflareIntegration(t, upstream.URL,
				`{"gpt-provider-contract":"@cf/meta/llama-3.1-8b-instruct"}`)
			withCloudflarePricing(t)
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, test.request(key))
			require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
			assert.Zero(t, contacts.Load())
			assert.NotContains(t, response.Body.String(), "cloudflare-secret")
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
		})
	}
}
