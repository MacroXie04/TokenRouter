package engine_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMoonshotOpenAIStreamWireCacheUsageAndSettlement(t *testing.T) {
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
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		observed <- providerWireObservation{
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			APIKey:        r.Header.Get("x-api-key"),
			Body:          decoded,
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"moonshot-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\",\"usage\":{\"cached_tokens\":4}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeMoonshot),
		"model_mapping": `{"gpt-provider-contract":"kimi-k2.6"}`,
	}).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: `{"gpt-provider-contract":"tiered_expr"}`,
		setting.ModelBillingExprOption: `{"gpt-provider-contract":"p * 10000 + c * 10000 + cr * 100000"}`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.ModelBillingModeOption: `{}`,
			setting.ModelBillingExprOption: `{}`,
		})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-provider-contract","stream":true,"temperature":0.2,"max_tokens":2,
		"stream_options":{"include_usage":true},"group":"dashboard-only",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))

	wire := <-observed
	assert.Equal(t, "/v1/chat/completions", wire.Path)
	assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
	assert.Empty(t, wire.APIKey)
	assert.Equal(t, "kimi-k2.6", wire.Body["model"])
	assert.EqualValues(t, 1, wire.Body["temperature"], "kimi-k2.6 accepts only temperature=1")
	assert.Equal(t, true, wire.Body["stream_options"].(map[string]any)["include_usage"])
	assert.NotContains(t, wire.Body, "group")

	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName)
	assert.Equal(t, 10, consumeLog.PromptTokens)
	assert.Equal(t, 2, consumeLog.CompletionTokens)
	assert.Equal(t, 240000, consumeLog.Quota, "nested Moonshot cached_tokens must participate in tiered settlement")
	assert.Equal(t, channel.Id, consumeLog.ChannelId)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 260000, user.Quota)
	assert.Equal(t, 240000, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 260000, token.RemainQuota)
	assert.Equal(t, 240000, token.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 240000, reservation.ActualQuota)
}

func TestMoonshotOpenAIErrorIsMappedSanitizedAndRefunded(t *testing.T) {
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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Moonshot busy for sk-upstream","type":"rate_limit_error"}}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":                int(channelcatalog.ChannelTypeMoonshot),
		"status_code_mapping": `{"429":503}`,
	}).Error)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "Moonshot busy")
	assert.NotContains(t, recorder.Body.String(), "sk-upstream")

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
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestMoonshotNativeClaudeWireAndSettlement(t *testing.T) {
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
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		observed <- providerWireObservation{
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			APIKey:        r.Header.Get("x-api-key"),
			Body:          decoded,
		}
		assert.Empty(t, r.Header.Get("anthropic-version"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-moonshot","type":"message","role":"assistant","model":"kimi-k2.5","content":[{"type":"text","text":"native moonshot"}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":3}}`)
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL+"/gateway")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeMoonshot),
		"model_mapping": `{"gpt-provider-contract":"kimi-k2.5"}`,
	}).Error)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-provider-contract","max_tokens":64,"temperature":0.4,
		"system":"follow rules","messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "native moonshot")

	wire := <-observed
	assert.Equal(t, "/gateway/anthropic/v1/messages", wire.Path)
	assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
	assert.Empty(t, wire.APIKey)
	assert.Equal(t, "kimi-k2.5", wire.Body["model"])
	assert.EqualValues(t, 64, wire.Body["max_tokens"])
	assert.EqualValues(t, 0.4, wire.Body["temperature"])
	assert.Equal(t, "follow rules", wire.Body["system"])

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 6, log.PromptTokens)
	assert.Equal(t, 3, log.CompletionTokens)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}

func TestMoonshotNativeClaudeStreamPreservesSSEUsageAndSettlement(t *testing.T) {
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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/anthropic/v1/messages", r.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", r.Header.Get("Authorization"))
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg-stream","type":"message","role":"assistant","model":"kimi-k2.5","content":[],"usage":{"input_tokens":8,"output_tokens":0}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native stream"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = io.WriteString(w, "event: message\ndata: "+event+"\n\n")
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeMoonshot),
		"model_mapping": `{"gpt-provider-contract":"kimi-k2.5"}`,
	}).Error)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-provider-contract","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"text":"native stream"`)
	assert.Contains(t, recorder.Body.String(), `"output_tokens":5`)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 8, log.PromptTokens)
	assert.Equal(t, 5, log.CompletionTokens)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
}
