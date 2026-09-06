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

func TestOpenAIClientNativeProviderToolStreamsSettleEndToEnd(t *testing.T) {
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

	tests := []struct {
		name        string
		channelType channelcatalog.ChannelType
		mappedModel string
		wantPath    string
		wantQuery   string
		stream      string
		checkWire   func(*testing.T, *http.Request, map[string]any)
	}{
		{
			name: "Anthropic", channelType: channelcatalog.ChannelTypeAnthropic,
			mappedModel: "claude-sonnet", wantPath: "/v1/messages",
			stream: strings.Join([]string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"sf\"}"}}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
				`data: {"type":"message_stop"}`,
				"",
			}, "\n\n"),
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "sk-upstream", request.Header.Get("x-api-key"))
				assert.Empty(t, request.Header.Get("Authorization"))
				assert.Equal(t, "claude-sonnet", body["model"])
				tools, ok := body["tools"].([]any)
				require.True(t, ok)
				require.Len(t, tools, 1)
				tool := tools[0].(map[string]any)
				schema := tool["input_schema"].(map[string]any)
				assert.Equal(t, "object", schema["type"])
				assert.Equal(t, []any{"city"}, schema["required"])
			},
		},
		{
			name: "Gemini", channelType: channelcatalog.ChannelTypeGemini,
			mappedModel: "gemini-pro", wantPath: "/v1beta/models/gemini-pro:streamGenerateContent", wantQuery: "alt=sse",
			stream: strings.Join([]string{
				`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"sf"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`,
				"",
			}, "\n\n"),
			checkWire: func(t *testing.T, request *http.Request, body map[string]any) {
				assert.Equal(t, "sk-upstream", request.Header.Get("x-goog-api-key"))
				assert.Empty(t, request.Header.Get("Authorization"))
				tools, ok := body["tools"].([]any)
				require.True(t, ok)
				require.Len(t, tools, 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				bodyBytes, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				var body map[string]any
				require.NoError(t, json.Unmarshal(bodyBytes, &body))
				assert.Equal(t, test.wantPath, request.URL.Path)
				assert.Equal(t, test.wantQuery, request.URL.RawQuery)
				test.checkWire(t, request, body)
				response.Header().Set("Content-Type", "text/event-stream")
				_, err = io.WriteString(response, test.stream)
				require.NoError(t, err)
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			var channel model.Channel
			require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
			require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
				"type": int(test.channelType), "model_mapping": `{"gpt-provider-contract":"` + test.mappedModel + `"}`,
			}).Error)

			handler := router.SetUpRouter()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
				"model":"gpt-provider-contract","stream":true,"messages":[{"role":"user","content":"weather?"}],
				"tools":[{"type":"function","function":{"name":"get_weather","description":"weather lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]
			}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			clientStream := recorder.Body.String()
			assert.Contains(t, clientStream, `"tool_calls"`)
			assert.Contains(t, clientStream, `"name":"get_weather"`)
			assert.Contains(t, clientStream, `\"city\":\"sf\"`)
			assert.Contains(t, clientStream, `"finish_reason":"tool_calls"`)
			assert.Equal(t, 1, strings.Count(clientStream, "data: [DONE]"))

			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
			assert.Equal(t, 5, log.PromptTokens)
			assert.Equal(t, 3, log.CompletionTokens)
			assert.Greater(t, log.Quota, 0)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, log.Quota, reservation.ActualQuota)
		})
	}
}
