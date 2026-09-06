package engine_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cohereWireObservation struct {
	Path          string
	Authorization string
	ContentType   string
	Accept        string
	Body          map[string]any
}

func withCoherePrices(t *testing.T) {
	t.Helper()
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
}

func configureCohereIntegration(t *testing.T, upstreamURL, upstreamModel string) (string, int, model.Channel) {
	t.Helper()
	key, userID := setupRelayIntegration(t, upstreamURL+"/cohere")
	var channel model.Channel
	require.NoError(t, model.DB.Where("name = ?", "mock").First(&channel).Error)
	mapping, err := json.Marshal(map[string]string{"gpt-provider-contract": upstreamModel})
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"type":          int(channelcatalog.ChannelTypeCohere),
		"model_mapping": string(mapping),
	}).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	return key, userID, channel
}

func assertCohereSettled(t *testing.T, key string, userID int, channel model.Channel, prompt, completion int) {
	t.Helper()
	expectedQuota := billingsvc.ComputeQuota("gpt-provider-contract", "default", prompt, completion)
	var consumeLog model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).First(&consumeLog).Error)
	assert.Equal(t, "gpt-provider-contract", consumeLog.ModelName)
	assert.Equal(t, prompt, consumeLog.PromptTokens)
	assert.Equal(t, completion, consumeLog.CompletionTokens)
	assert.Equal(t, expectedQuota, consumeLog.Quota)
	assert.Equal(t, channel.Id, consumeLog.ChannelId)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 500000-expectedQuota, user.Quota)
	assert.Equal(t, expectedQuota, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	var token model.Token
	require.NoError(t, model.DB.Where("key = ?", key).First(&token).Error)
	assert.Equal(t, 500000-expectedQuota, token.RemainQuota)
	assert.Equal(t, expectedQuota, token.UsedQuota)
	var settledChannel model.Channel
	require.NoError(t, model.DB.First(&settledChannel, channel.Id).Error)
	assert.EqualValues(t, expectedQuota, settledChannel.UsedQuota)
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("user_id = ?", userID).First(&reservation).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, reservation.Status)
	assert.Equal(t, expectedQuota, reservation.ActualQuota)
	assert.Positive(t, reservation.RequestedQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
	assert.NotEmpty(t, reservation.ReservationID)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(consumeLog.Other), &other))
	assert.Equal(t, reservation.ReservationID, other["relay_reservation_id"])
}

func assertCohereRefunded(t *testing.T, key string, userID int) {
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
	assert.Positive(t, reservation.RequestedQuota)
	assert.Positive(t, reservation.ReservedQuota)
	assert.Equal(t, reservation.ReservedQuota, reservation.TokenReserved)
	var consumeLogs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", userID, billingsvc.LogTypeConsume).Count(&consumeLogs).Error)
	assert.Zero(t, consumeLogs)
}

func TestCohereChatAndRerankWireResponseAndSettlementEndToEnd(t *testing.T) {
	withCoherePrices(t)
	t.Setenv("RETRY_TIMES", "0")

	for _, test := range []struct {
		name             string
		clientPath       string
		clientBody       string
		upstreamPath     string
		upstreamModel    string
		upstreamResponse string
		promptTokens     int
		completionTokens int
		checkWire        func(*testing.T, map[string]any)
		checkResponse    func(*testing.T, map[string]any)
	}{
		{
			name: "chat", clientPath: "/v1/chat/completions", upstreamPath: "/cohere/v1/chat",
			upstreamModel: "command-r-plus", promptTokens: 40, completionTokens: 6,
			clientBody: `{
				"model":"gpt-provider-contract","max_tokens":3,"max_completion_tokens":7,
				"group":"dashboard-only","provider_extension":"must-strip",
				"messages":[
					{"role":"system","content":"follow rules"},
					{"role":"user","content":"old question"},
					{"role":"assistant","content":"old answer"},
					{"role":"user","content":"latest question"}
				]
			}`,
			upstreamResponse: `{"response_id":"cohere-chat","finish_reason":"COMPLETE","text":"chat answer","meta":{"billed_units":{"input_tokens":40,"output_tokens":6}}}`,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "latest question", body["message"])
				assert.Equal(t, false, body["stream"])
				assert.EqualValues(t, 7, body["max_tokens"])
				history := body["chat_history"].([]any)
				require.Len(t, history, 2)
				assert.Equal(t, "SYSTEM", history[0].(map[string]any)["role"])
				assert.Equal(t, "CHATBOT", history[1].(map[string]any)["role"])
				assert.NotContains(t, body, "group")
				assert.NotContains(t, body, "provider_extension")
			},
			checkResponse: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "cohere-chat", body["id"])
				assert.Equal(t, "chat.completion", body["object"])
				assert.Equal(t, "command-r-plus", body["model"])
				choice := body["choices"].([]any)[0].(map[string]any)
				assert.Equal(t, "stop", choice["finish_reason"])
				assert.Equal(t, "chat answer", choice["message"].(map[string]any)["content"])
			},
		},
		{
			name: "rerank", clientPath: "/v1/rerank", upstreamPath: "/cohere/v1/rerank",
			upstreamModel: "rerank-multilingual-v3.0", promptTokens: 20, completionTokens: 1,
			clientBody: `{
				"model":"gpt-provider-contract","query":"needle","documents":["one",{"text":"two"}],
				"top_n":0,"return_documents":false,"group":"dashboard-only","provider_extension":"must-strip"
			}`,
			upstreamResponse: `{"results":[{"index":1,"relevance_score":0.92,"document":{"text":"two"}}],"meta":{"billed_units":{"input_tokens":20,"output_tokens":1}}}`,
			checkWire: func(t *testing.T, body map[string]any) {
				assert.Equal(t, "needle", body["query"])
				assert.Len(t, body["documents"], 2)
				assert.EqualValues(t, 1, body["top_n"])
				assert.Equal(t, true, body["return_documents"])
				assert.NotContains(t, body, "group")
				assert.NotContains(t, body, "provider_extension")
			},
			checkResponse: func(t *testing.T, body map[string]any) {
				result := body["results"].([]any)[0].(map[string]any)
				assert.EqualValues(t, 1, result["index"])
				assert.EqualValues(t, 0.92, result["relevance_score"])
				usage := body["usage"].(map[string]any)
				assert.EqualValues(t, 20, usage["prompt_tokens"])
				assert.EqualValues(t, 1, usage["completion_tokens"])
				assert.EqualValues(t, 21, usage["total_tokens"])
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan cohereWireObservation, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				assert.NoError(t, err)
				var decoded map[string]any
				assert.NoError(t, json.Unmarshal(body, &decoded))
				observed <- cohereWireObservation{
					Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
					ContentType: request.Header.Get("Content-Type"), Accept: request.Header.Get("Accept"), Body: decoded,
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.upstreamResponse)
			}))
			defer upstream.Close()

			key, userID, channel := configureCohereIntegration(t, upstream.URL, test.upstreamModel)
			request := httptest.NewRequest(http.MethodPost, test.clientPath, strings.NewReader(test.clientBody))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())

			wire := <-observed
			assert.Equal(t, test.upstreamPath, wire.Path)
			assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
			assert.Equal(t, "application/json", wire.ContentType)
			assert.Equal(t, "application/json", wire.Accept)
			assert.Equal(t, test.upstreamModel, wire.Body["model"])
			test.checkWire(t, wire.Body)

			var normalized map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &normalized))
			test.checkResponse(t, normalized)
			assertCohereSettled(t, key, userID, channel, test.promptTokens, test.completionTokens)
		})
	}
}

func TestCohereRawJSONStreamUsageAndSettlementEndToEnd(t *testing.T) {
	withCoherePrices(t)
	t.Setenv("RETRY_TIMES", "0")
	observed := make(chan cohereWireObservation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		observed <- cohereWireObservation{
			Path: request.URL.Path, Authorization: request.Header.Get("Authorization"),
			Accept: request.Header.Get("Accept"), Body: decoded,
		}
		w.Header().Set("Content-Type", "application/json")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, `{"is_finished":false,"event_type":"text-generation","text":"hello"}`+"\n")
		flusher.Flush()
		_, _ = io.WriteString(w, `{"is_finished":true,"event_type":"stream-end","finish_reason":"COMPLETE","response":{"response_id":"cohere-stream","text":"hello","meta":{"billed_units":{"input_tokens":5,"output_tokens":3}}}}`+"\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	key, userID, channel := configureCohereIntegration(t, upstream.URL, "command-r-plus")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-provider-contract","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	wire := <-observed
	assert.Equal(t, "/cohere/v1/chat", wire.Path)
	assert.Equal(t, "Bearer sk-upstream", wire.Authorization)
	assert.Equal(t, "text/event-stream", wire.Accept)
	assert.Equal(t, "command-r-plus", wire.Body["model"])
	assert.Equal(t, true, wire.Body["stream"])
	assert.EqualValues(t, 8, wire.Body["max_tokens"])
	assert.Contains(t, response.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, response.Body.String(), `"content":"hello"`)
	assert.Contains(t, response.Body.String(), `"finish_reason":"stop"`)
	assert.Equal(t, 1, strings.Count(response.Body.String(), "data: [DONE]"))
	assertCohereSettled(t, key, userID, channel, 5, 3)
}

func TestCohereRerankMissingBilledUsageFallsBackAndSettlesEndToEnd(t *testing.T) {
	withCoherePrices(t)
	t.Setenv("RETRY_TIMES", "0")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/cohere/v1/rerank", request.URL.Path)
		assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[],"meta":{"billed_units":{"input_tokens":0,"output_tokens":0}}}`)
	}))
	defer upstream.Close()

	key, userID, channel := configureCohereIntegration(t, upstream.URL, "rerank-english-v3.0")
	request := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(
		`{"model":"gpt-provider-contract","query":"needle","documents":["one","two"]}`,
	))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	expectedPrompt := relaycommon.CountTokens("one\ntwo\nneedle")
	var normalized map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &normalized))
	usage := normalized["usage"].(map[string]any)
	assert.EqualValues(t, expectedPrompt, usage["prompt_tokens"])
	assert.Zero(t, usage["completion_tokens"])
	assert.EqualValues(t, expectedPrompt, usage["total_tokens"])
	assertCohereSettled(t, key, userID, channel, expectedPrompt, 0)
}

func TestCohereErrorsAreMappedSanitizedAndRefundedEndToEnd(t *testing.T) {
	withCoherePrices(t)
	t.Setenv("RETRY_TIMES", "0")

	for _, test := range []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name: "provider error", status: http.StatusTooManyRequests,
			body: `{"message":"Cohere busy for sk-upstream"}`, wantStatus: http.StatusServiceUnavailable,
			wantText: "Cohere busy",
		},
		{
			name: "malformed success", status: http.StatusOK,
			body: `{"response_id":`, wantStatus: http.StatusInternalServerError,
			wantText: "upstream",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, "/cohere/v1/chat", request.URL.Path)
				assert.Equal(t, "Bearer sk-upstream", request.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()

			key, userID, channel := configureCohereIntegration(t, upstream.URL, "command-r-plus")
			if test.status == http.StatusTooManyRequests {
				require.NoError(t, model.DB.Model(&channel).Update("status_code_mapping", `{"429":503}`).Error)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
				`{"model":"gpt-provider-contract","messages":[{"role":"user","content":"hello"}]}`,
			))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(response, request)
			require.Equal(t, test.wantStatus, response.Code, response.Body.String())
			assert.Contains(t, strings.ToLower(response.Body.String()), strings.ToLower(test.wantText))
			assert.NotContains(t, response.Body.String(), "sk-upstream")
			if test.status == http.StatusTooManyRequests {
				assert.Contains(t, response.Body.String(), `"code":"cohere_error"`)
			}
			assertCohereRefunded(t, key, userID)
		})
	}
}
