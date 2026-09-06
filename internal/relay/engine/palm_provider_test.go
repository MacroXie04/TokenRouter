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

func withPaLMPrices(t *testing.T) {
	t.Helper()
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"palm-client-model": {Prompt: 1, Completion: 2},
	})
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
	})
}

func configurePaLMIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/palm")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{"palm-client-model": "PaLM-2"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(channelcatalog.ChannelTypePaLM), "model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "palm-client-model", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func TestPaLMBlockingWireResponseAndSettlementEndToEnd(t *testing.T) {
	withPaLMPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/palm/v1beta2/models/chat-bison-001:generateMessage", request.URL.Path)
		assert.Equal(t, "sk-upstream", request.Header.Get("x-goog-api-key"))
		assert.Empty(t, request.Header.Get("Authorization"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.EqualValues(t, 1, body["candidateCount"])
		prompt := body["prompt"].(map[string]any)
		messages := prompt["messages"].([]any)
		require.Len(t, messages, 2)
		assert.Equal(t, "0", messages[0].(map[string]any)["author"])
		assert.Equal(t, "1", messages[1].(map[string]any)["author"])
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"candidates":[{"author":"1","content":"PaLM response"}]}`)
	}))
	defer upstream.Close()

	key, userID, channel := configurePaLMIntegration(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"palm-client-model","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"prior"}]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, calls.Load())
	var normalized struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &normalized))
	assert.Equal(t, "palm-client-model", normalized.Model)
	require.Len(t, normalized.Choices, 1)
	assert.Equal(t, "PaLM response", normalized.Choices[0].Message.Content)

	expectedQuota := billingsvc.ComputeQuota("palm-client-model", "default", normalized.Usage.PromptTokens, normalized.Usage.CompletionTokens)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-expectedQuota, user.Quota)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, "palm-client-model", log.ModelName)
	assert.Equal(t, normalized.Usage.PromptTokens, log.PromptTokens)
	assert.Equal(t, normalized.Usage.CompletionTokens, log.CompletionTokens)
	assert.Equal(t, expectedQuota, log.Quota)
	assert.Equal(t, channel.Id, log.ChannelId)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
}

func TestPaLMStreamingWireAndSettlementEndToEnd(t *testing.T) {
	withPaLMPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"candidates":[{"author":"1","content":"streamed PaLM response"}]}`)
	}))
	defer upstream.Close()

	key, userID, _ := configurePaLMIntegration(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"palm-client-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	assert.Contains(t, response.Body.String(), `"content":"streamed PaLM response"`)
	assert.Equal(t, 1, strings.Count(response.Body.String(), "data: [DONE]"))

	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Positive(t, reservation.ActualQuota)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Positive(t, log.CompletionTokens)
}

func TestPaLMProviderFailuresAreSanitizedAndRefunded(t *testing.T) {
	withPaLMPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "http", body: `{"error":{"message":"credential sk-upstream rejected"}}`},
		{name: "business", body: `{"error":{"code":400,"message":"credential sk-upstream rejected","status":"INVALID_ARGUMENT"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if test.name == "http" {
					writer.WriteHeader(http.StatusBadRequest)
				}
				_, _ = io.WriteString(writer, test.body)
			}))
			defer upstream.Close()

			key, userID, _ := configurePaLMIntegration(t, upstream.URL)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
				`{"model":"palm-client-model","messages":[{"role":"user","content":"hello"}]}`,
			))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			assert.NotContains(t, response.Body.String(), "sk-upstream")
			assert.Contains(t, response.Body.String(), "[REDACTED]")

			var user model.User
			require.NoError(t, model.DB.First(&user, userID).Error)
			assert.Equal(t, 500000, user.Quota)
			assert.Zero(t, user.UsedQuota)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
		})
	}
}

func TestPaLMUnsupportedModeFailsBeforeProviderContactAndRefunds(t *testing.T) {
	withPaLMPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()

	key, userID, _ := configurePaLMIntegration(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(
		`{"model":"palm-client-model","input":"hello"}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	assert.Zero(t, calls.Load())

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000, user.Quota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
}
