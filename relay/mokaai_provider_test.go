package relay_test

import (
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

func withMokaAIPrices(t *testing.T) {
	t.Helper()
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"moka-client-model": {Prompt: 1, Completion: 2},
	})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})
}

func configureMokaAIIntegration(t *testing.T, upstreamURL string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/moka")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{"moka-client-model": "m3e-large"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type": int(constant.ChannelTypeMokaAI), "model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "moka-client-model", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, service.InitAbilityCache())
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func TestMokaAIEmbeddingWireResponseAndSettlementEndToEnd(t *testing.T) {
	withMokaAIPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/moka/embeddings", request.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "m3e-large", body["model"])
		assert.Equal(t, []any{"first", "second"}, body["input"])
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"object":"list","model":"ignored","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]},{"object":"embedding","index":1,"embedding":[0.3]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`)
	}))
	defer upstream.Close()

	key, userID, channel := configureMokaAIIntegration(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(
		`{"model":"moka-client-model","input":["first","second"]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.EqualValues(t, 1, calls.Load())
	var normalized map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &normalized))
	assert.Equal(t, "baidu-embedding", normalized["model"])
	assert.Len(t, normalized["data"], 2)

	expectedQuota := service.ComputeQuota("moka-client-model", "default", 7, 0)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-expectedQuota, user.Quota)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, "moka-client-model", log.ModelName)
	assert.Equal(t, 7, log.PromptTokens)
	assert.Zero(t, log.CompletionTokens)
	assert.Equal(t, expectedQuota, log.Quota)
	assert.Equal(t, channel.Id, log.ChannelId)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
}

func TestMokaAIProviderErrorIsSanitizedAndRefunded(t *testing.T) {
	withMokaAIPrices(t)
	t.Setenv("RETRY_TIMES", "0")
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"message":"credential sk-upstream rejected"}}`)
	}))
	defer upstream.Close()

	key, userID, _ := configureMokaAIIntegration(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(
		`{"model":"moka-client-model","input":"first"}`,
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
}
