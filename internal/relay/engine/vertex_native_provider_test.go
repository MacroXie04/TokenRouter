package engine_test

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type vertexNativeObservation struct {
	path          string
	query         string
	authorization string
	body          []byte
}

func TestVertexNativeGeminiV1AndV1BetaRoutesMapAndSettle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name         string
		incomingPath string
		upstreamPath string
		stream       bool
		response     string
	}{
		{
			name:         "v1 blocking",
			incomingPath: "/v1/models/vertex-native-client:generateContent",
			upstreamPath: "/gateway/v1/publishers/google/models/gemini-2.5-flash:generateContent",
			response: `{
				"candidates":[{"content":{"role":"model","parts":[{"text":"native v1 reply"}]},"finishReason":"STOP","index":0}],
				"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}
			}`,
		},
		{
			name:         "v1beta streaming",
			incomingPath: "/v1beta/models/vertex-native-client:streamGenerateContent?alt=sse",
			upstreamPath: "/gateway/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
			stream:       true,
			response: strings.Join([]string{
				`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"native "}]},"index":0}]}`,
				`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"stream"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
				"", "",
			}, "\n"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan vertexNativeObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				observed <- vertexNativeObservation{
					path: request.URL.EscapedPath(), query: request.URL.RawQuery,
					authorization: request.Header.Get("Authorization"), body: body,
				}
				if test.stream {
					writer.Header().Set("Content-Type", "text/event-stream")
				} else {
					writer.Header().Set("Content-Type", "application/json")
				}
				_, _ = io.WriteString(writer, test.response)
			}))
			defer upstream.Close()

			key := setupChannelIntegration(t, upstream.URL+"/gateway", channelcatalog.ChannelTypeVertexAi, "vertex-native-client")
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
				"key":           "AIza-native-vertex",
				"other":         "us-central1",
				"settings":      `{"vertex_key_type":"api_key"}`,
				"model_mapping": `{"vertex-native-client":"gemini-2.5-flash"}`,
			}).Error)
			previousPrices := billingsvc.ExportedModelPrices()
			previousRatios := billingsvc.ExportedGroupRatios()
			billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
				"vertex-native-client": {Prompt: 1, Completion: 1},
			})
			billingsvc.SetGroupRatios(map[string]float64{"default": 1})
			t.Cleanup(func() {
				billingsvc.SetModelPriceRegistry(previousPrices)
				billingsvc.SetGroupRatios(previousRatios)
			})

			body := `{"model":"models/vertex-native-client","cachedContent":"cachedContents/owned","contents":[{"role":"user","parts":[{"text":"native request"}]}]}`
			request := httptest.NewRequest(http.MethodPost, test.incomingPath, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "native ")
			assert.Contains(t, recorder.Body.String(), `"candidates"`)
			assert.NotContains(t, recorder.Body.String(), `"choices"`)
			wire := <-observed
			assert.Equal(t, test.upstreamPath, wire.path)
			query, err := url.ParseQuery(wire.query)
			require.NoError(t, err)
			assert.Equal(t, "AIza-native-vertex", query.Get("key"))
			if test.stream {
				assert.Equal(t, "sse", query.Get("alt"))
			} else {
				assert.Empty(t, query.Get("alt"))
			}
			assert.Empty(t, wire.authorization)
			var providerRequest map[string]any
			require.NoError(t, json.Unmarshal(wire.body, &providerRequest))
			assert.NotContains(t, providerRequest, "model")
			assert.Equal(t, "cachedContents/owned", providerRequest["cachedContent"])

			var user model.User
			var reservation model.RelayQuotaReservationRecord
			var log model.Log
			require.NoError(t, model.DB.Where("username = ?", "guser").First(&user).Error)
			require.NoError(t, model.DB.Order("id DESC").First(&reservation).Error)
			require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
			assert.Equal(t, 7, log.PromptTokens)
			assert.Equal(t, 3, log.CompletionTokens)
			assert.Less(t, user.Quota, 500_000)
		})
	}
}

func TestVertexMalformedServiceAccountRefundsWithoutNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()

	key := setupChannelIntegration(t, upstream.URL, channelcatalog.ChannelTypeVertexAi, "vertex-native-client")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
		"key":      `{"type":"service_account","project_id":"vertex-project-123","private_key":"must-never-leave","client_email":"credential@example.test"}`,
		"other":    "us-central1",
		"settings": `{"vertex_key_type":"json"}`,
	}).Error)

	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/vertex-native-client:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"must not dispatch"}]}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code, recorder.Body.String())
	assert.Zero(t, calls.Load())
	assert.NotContains(t, recorder.Body.String(), "must-never-leave")
	assert.NotContains(t, recorder.Body.String(), "credential@example.test")
	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("username = ?", "guser").First(&user).Error)
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	require.NoError(t, model.DB.Order("id DESC").First(&reservation).Error)
	assert.Equal(t, 500_000, user.Quota)
	assert.Equal(t, 500_000, token.RemainQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
}
