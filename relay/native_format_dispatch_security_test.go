package relay_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
)

func TestGeminiNativeViaAzureUsesProviderContractAndMappedDeployment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		requestedModel = "gemini-client-model"
		deployment     = "azure-gemini-deployment"
	)
	var gotPath, gotQuery, gotAPIKey, gotAuthorization string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		gotAuthorization = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-azure", "object": "chat.completion", "model": deployment,
			"choices": []map[string]any{{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "azure reply"}, "finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6},
		})
	}))
	defer mock.Close()

	key := setupChannelIntegration(t, mock.URL, constant.ChannelTypeAzure, requestedModel)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Updates(map[string]any{
		"key":           "azure-secret",
		"other":         "2025-04-01-preview",
		"model_mapping": `{"gemini-client-model":"azure-gemini-deployment"}`,
		"created_time":  int64(1_800_000_000),
	}).Error)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+requestedModel+":generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.SetUpRouter().ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "/openai/deployments/"+deployment+"/chat/completions", gotPath)
	assert.Equal(t, "api-version=2025-04-01-preview", gotQuery)
	assert.Equal(t, "azure-secret", gotAPIKey)
	assert.Empty(t, gotAuthorization, "Azure credentials must not be sent as a bearer token")
	assert.Equal(t, deployment, gotBody["model"])
	assert.Contains(t, recorder.Body.String(), "azure reply")

	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "guser").First(&user).Error)
	assert.Less(t, user.Quota, 500000, "native-format conversion must still settle usage")
}

func TestNativeFormatsRejectUnsupportedProviderBeforeCredentialedNetwork(t *testing.T) {
	tests := []struct {
		name        string
		channelType constant.ChannelType
		modelName   string
		claude      bool
		request     func(string) *http.Request
	}{
		{
			name:        "Gemini does not treat Vertex credentials as API keys",
			channelType: constant.ChannelTypeVertexAi,
			modelName:   "gemini-vertex-model",
			request: func(key string) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-vertex-model:generateContent",
					strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				return req
			},
		},
		{
			name:        "Claude does not send AWS credentials as a bearer token",
			channelType: constant.ChannelTypeAws,
			modelName:   "claude-aws-model",
			claude:      true,
			request: func(key string) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/v1/messages",
					strings.NewReader(`{"model":"claude-aws-model","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				return req
			},
		},
		{
			name:        "Gemini does not use the Perplexity chat-only profile",
			channelType: constant.ChannelTypePerplexity,
			modelName:   "sonar-pro",
			request: func(key string) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/v1beta/models/sonar-pro:generateContent",
					strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				return req
			},
		},
		{
			name:        "Gemini does not use the Jina rerank and embedding profile",
			channelType: constant.ChannelTypeJina,
			modelName:   "jina-reranker-v2-base-multilingual",
			request: func(key string) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/v1beta/models/jina-reranker-v2-base-multilingual:generateContent",
					strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				return req
			},
		},
		{
			name:        "Claude does not use the Submodel OpenAI-only profile",
			channelType: constant.ChannelTypeSubmodel,
			modelName:   "openai/gpt-oss-120b",
			claude:      true,
			request: func(key string) *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/v1/messages",
					strings.NewReader(`{"model":"openai/gpt-oss-120b","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				return req
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusTeapot)
			}))
			defer mock.Close()

			var key string
			if test.claude {
				key, _ = setupClaudeRelay(t, mock.URL, int(test.channelType), test.modelName)
			} else {
				key = setupChannelIntegration(t, mock.URL, test.channelType, test.modelName)
			}
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "mock").Update("key",
				`{"private_key":"must-never-leave","client_email":"credential@example.test"}`).Error)

			recorder := httptest.NewRecorder()
			router.SetUpRouter().ServeHTTP(recorder, test.request(key))

			require.Equal(t, http.StatusInternalServerError, recorder.Code, recorder.Body.String())
			assert.Zero(t, calls.Load(), "unsupported native formats must fail before any upstream request")
			assert.NotContains(t, recorder.Body.String(), "must-never-leave")
			assert.NotContains(t, recorder.Body.String(), "credential@example.test")

			var user model.User
			require.NoError(t, model.DB.First(&user).Error)
			assert.Equal(t, 500000, user.Quota, "a pre-dispatch rejection must refund the user")
			var token model.Token
			require.NoError(t, model.DB.First(&token).Error)
			assert.Equal(t, 500000, token.RemainQuota, "a pre-dispatch rejection must refund the token")
			var reservation model.RelayQuotaReservationRecord
			require.NoError(t, model.DB.Order("id DESC").First(&reservation).Error)
			assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
		})
	}
}
