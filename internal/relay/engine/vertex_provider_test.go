package engine

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type vertexRelayObservation struct {
	host                  string
	path                  string
	query                 string
	authorization         string
	googleAPIKeyHeader    string
	anthropicBeta         string
	body                  []byte
	reservationDispatched bool
}

type vertexRelayRoundTripFunc func(*http.Request) (*http.Response, error)

func (function vertexRelayRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestVertexRelayMapsRegionAndModelBeforeIOAndSettlesGeminiUsage(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeVertexAi),
		"key":           "AIza-vertex-integration",
		"base_url":      "",
		"other":         "europe-west4",
		"settings":      `{"vertex_key_type":"api_key"}`,
		"model_mapping": `{"accounting-model":"gemini-2.5-flash"}`,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"accounting-model": {Prompt: 1, Completion: 1},
	})

	observed := make(chan vertexRelayObservation, 1)
	previousClient := relayHTTPClient
	relayHTTPClient = &http.Client{Transport: vertexRelayRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var reservation model.RelayQuotaReservationRecord
		dispatched := model.DB.Order("id DESC").First(&reservation).Error == nil &&
			reservation.Status == model.RelayQuotaReservationStatusDispatched
		observed <- vertexRelayObservation{
			host: request.URL.Host, path: request.URL.EscapedPath(), query: request.URL.RawQuery,
			authorization:      request.Header.Get("Authorization"),
			googleAPIKeyHeader: request.Header.Get("x-goog-api-key"), body: body,
			reservationDispatched: dispatched,
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"candidates":[{"content":{"role":"model","parts":[{"text":"vertex lifecycle reply"}]},"finishReason":"STOP","index":0}],
				"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"totalTokenCount":15}
			}`)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { relayHTTPClient = previousClient })

	context, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	info.Request.Messages = []protocolkit.Message{{Role: "user", Content: "hello from the lifecycle"}}
	info.RawBody = []byte(`{"model":"accounting-model","messages":[{"role":"user","content":"hello from the lifecycle"}],"max_tokens":64}`)
	context.Request.Header.Set("Content-Type", "application/json")
	require.NoError(t, relayAndSettle(context, middleware.CaptureRelayRequestState(context), info))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), "vertex lifecycle reply")
	assert.Contains(t, recorder.Body.String(), `"model":"accounting-model"`)

	wire := <-observed
	assert.Equal(t, "europe-west4-aiplatform.googleapis.com", wire.host)
	assert.Equal(t, "/v1/publishers/google/models/gemini-2.5-flash:generateContent", wire.path)
	assert.Equal(t, "key=AIza-vertex-integration", wire.query)
	assert.Empty(t, wire.authorization)
	assert.Empty(t, wire.googleAPIKeyHeader)
	assert.True(t, wire.reservationDispatched, "quota must be reserved before Vertex observes I/O")
	var providerRequest map[string]any
	require.NoError(t, json.Unmarshal(wire.body, &providerRequest))
	assert.NotContains(t, providerRequest, "model")
	contents, ok := providerRequest["contents"].([]any)
	require.True(t, ok)
	require.Len(t, contents, 1)

	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 11, log.PromptTokens)
	assert.Equal(t, 4, log.CompletionTokens)
	assert.Equal(t, 15, log.PromptTokens+log.CompletionTokens)
	assert.Greater(t, log.Quota, 0)
	assert.Equal(t, 100_000-log.Quota, user.Quota)
	assert.Equal(t, 100_000-log.Quota, token.RemainQuota)
	assert.Equal(t, int64(log.Quota), channel.UsedQuota)
}

func TestVertexNativeClaudeMapsRequestAndSettlesNativeUsage(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeVertexAi),
		"key":           "AIza-vertex-claude",
		"base_url":      "https://vertex.example.test/gateway",
		"other":         "us-east5",
		"settings":      `{"vertex_key_type":"api_key"}`,
		"model_mapping": `{"accounting-model":"claude-sonnet-4-20250514"}`,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"accounting-model": {Prompt: 1, Completion: 1},
	})

	observed := make(chan vertexRelayObservation, 1)
	previousClient := relayHTTPClient
	relayHTTPClient = &http.Client{Transport: vertexRelayRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var reservation model.RelayQuotaReservationRecord
		dispatched := model.DB.Order("id DESC").First(&reservation).Error == nil &&
			reservation.Status == model.RelayQuotaReservationStatusDispatched
		observed <- vertexRelayObservation{
			host: request.URL.Host, path: request.URL.EscapedPath(), query: request.URL.RawQuery,
			authorization: request.Header.Get("Authorization"), googleAPIKeyHeader: request.Header.Get("x-goog-api-key"),
			anthropicBeta: request.Header.Get("anthropic-beta"), body: body, reservationDispatched: dispatched,
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"msg_vertex","type":"message","role":"assistant","model":"claude-sonnet-4@20250514",
				"content":[{"type":"text","text":"native Vertex Claude reply"}],"stop_reason":"end_turn",
				"usage":{"input_tokens":9,"output_tokens":3}
			}`)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { relayHTTPClient = previousClient })

	rawBody := []byte(`{
		"model":"accounting-model","max_tokens":64,"stream":false,
		"messages":[{"role":"user","content":"native claude request"}],
		"output_config":{"effort":"high"}
	}`)
	var claudeRequest protocolkit.ClaudeRequest
	require.NoError(t, json.Unmarshal(rawBody, &claudeRequest))
	openAIRequest := protocolkit.ClaudeRequestToOpenAIRequest(&claudeRequest)
	context, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(rawBody)))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	info.Format = channelcatalog.RelayFormatClaude
	info.Request = openAIRequest
	info.ClaudeRequest = &claudeRequest
	info.RawBody = rawBody
	info.ModelName = claudeRequest.Model
	require.NoError(t, relayAndSettleWithDispatch(context, middleware.CaptureRelayRequestState(context), info, dispatchClaudeUpstream))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"type":"message"`)
	assert.Contains(t, recorder.Body.String(), "native Vertex Claude reply")
	assert.NotContains(t, recorder.Body.String(), `"choices"`)

	wire := <-observed
	assert.Equal(t, "vertex.example.test", wire.host)
	assert.Equal(t, "/gateway/v1/publishers/anthropic/models/claude-sonnet-4@20250514:rawPredict", wire.path)
	assert.Equal(t, "key=AIza-vertex-claude", wire.query)
	assert.Empty(t, wire.authorization)
	assert.Empty(t, wire.googleAPIKeyHeader)
	assert.Equal(t, "prompt-caching-2024-07-31", wire.anthropicBeta)
	assert.True(t, wire.reservationDispatched)
	var providerRequest map[string]any
	require.NoError(t, json.Unmarshal(wire.body, &providerRequest))
	assert.NotContains(t, providerRequest, "model")
	assert.Equal(t, "vertex-2023-10-16", providerRequest["anthropic_version"])
	assert.Contains(t, providerRequest, "output_config")

	var reservation model.RelayQuotaReservationRecord
	var log model.Log
	require.NoError(t, model.DB.First(&reservation).Error)
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, 9, log.PromptTokens)
	assert.Equal(t, 3, log.CompletionTokens)
}
