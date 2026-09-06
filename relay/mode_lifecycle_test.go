package relay_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

func TestOpenAIModeLifecycleWireResponseAndAccounting(t *testing.T) {
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{"gpt-4": {Prompt: 1, Completion: 3}})
	service.SetGroupRatios(map[string]float64{"default": 1})
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})

	tests := []struct {
		name                string
		clientPath          string
		upstreamPath        string
		requestBody         string
		responseContentType string
		responseBody        string
		responseMarker      string
	}{
		{
			name: "Responses non-stream", clientPath: "/v1/responses", upstreamPath: "/v1/responses",
			requestBody:         `{"model":"gpt-4","input":"hello","provider_extension":"kept"}`,
			responseContentType: "application/json",
			responseBody:        `{"id":"resp_1","object":"response","status":"completed","model":"gpt-4","output":[],"usage":{"input_tokens":4,"output_tokens":6,"total_tokens":10}}`,
			responseMarker:      "resp_1",
		},
		{
			name: "Completions", clientPath: "/v1/completions", upstreamPath: "/v1/completions",
			requestBody:         `{"model":"gpt-4","prompt":"hello","max_tokens":8,"provider_extension":"kept"}`,
			responseContentType: "application/json",
			responseBody:        `{"id":"cmpl_1","object":"text_completion","model":"gpt-4","choices":[{"index":0,"text":"ok","finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
			responseMarker:      "cmpl_1",
		},
		{
			name: "Moderations", clientPath: "/v1/moderations", upstreamPath: "/v1/moderations",
			requestBody:         `{"model":"gpt-4","input":"safe text","provider_extension":"kept"}`,
			responseContentType: "application/json",
			responseBody:        `{"id":"modr_1","model":"gpt-4","results":[{"flagged":false}],"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`,
			responseMarker:      "modr_1",
		},
		{
			name: "Image generation", clientPath: "/v1/images/generations", upstreamPath: "/v1/images/generations",
			requestBody:         `{"model":"gpt-4","prompt":"a blue square","n":1,"provider_extension":"kept"}`,
			responseContentType: "application/json",
			responseBody:        `{"created":1,"data":[{"url":"https://cdn.example/image.png"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			responseMarker:      "image.png",
		},
		{
			name: "Rerank", clientPath: "/v1/rerank", upstreamPath: "/v1/rerank",
			requestBody:         `{"model":"gpt-4","query":"hello","documents":["one","two"],"provider_extension":"kept"}`,
			responseContentType: "application/json",
			responseBody:        `{"results":[{"index":0,"relevance_score":0.9}],"usage":{"prompt_tokens":5,"completion_tokens":0,"total_tokens":5}}`,
			responseMarker:      "relevance_score",
		},
		{
			name: "Audio speech", clientPath: "/v1/audio/speech", upstreamPath: "/v1/audio/speech",
			requestBody:         `{"model":"gpt-4","input":"speak this","voice":"alloy","provider_extension":"kept"}`,
			responseContentType: "audio/mpeg",
			responseBody:        "mock-mp3-bytes",
			responseMarker:      "mock-mp3-bytes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var received map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.upstreamPath, request.URL.Path)
				assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
				require.NoError(t, json.NewDecoder(request.Body).Decode(&received))
				w.Header().Set("Content-Type", test.responseContentType)
				_, _ = io.WriteString(w, test.responseBody)
			}))
			defer upstream.Close()

			key, userID := setupRelayIntegration(t, upstream.URL)
			handler := router.SetUpRouter()
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.requestBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), test.responseMarker)
			assert.Equal(t, "gpt-4", received["model"])
			assert.Equal(t, "kept", received["provider_extension"])

			var log model.Log
			require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
			assert.Greater(t, log.Quota, 0)
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, log.Quota, reservation.ActualQuota)
		})
	}
}

func TestResponsesStreamLifecycleUsesTerminalUsage(t *testing.T) {
	previousPrices := service.ExportedModelPrices()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{"gpt-4": {Prompt: 1, Completion: 3}})
	t.Cleanup(func() { service.SetModelPriceRegistry(previousPrices) })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/responses", request.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4\",\"usage\":{\"input_tokens\":4,\"output_tokens\":6,\"total_tokens\":10}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	key, userID := setupRelayIntegration(t, upstream.URL)
	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4","input":"hello","stream":true}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, recorder.Body.String(), "resp_stream")
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, service.LogTypeConsume).First(&log).Error)
	assert.Equal(t, 4, log.PromptTokens)
	assert.Equal(t, 6, log.CompletionTokens)
	assert.Greater(t, log.Quota, 0)
}

func TestLegacyEngineEmbeddingGetsModelFromPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/embeddings", request.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		assert.Equal(t, "text-embedding-3-small", body["model"])
		_, _ = io.WriteString(w, `{"object":"list","data":[],"usage":{"prompt_tokens":2,"completion_tokens":0,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	key, _ := setupRelayIntegration(t, upstream.URL)
	handler := router.SetUpRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/engines/text-embedding-3-small/embeddings", strings.NewReader(`{"input":"hello"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}
