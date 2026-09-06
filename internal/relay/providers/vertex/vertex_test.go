package vertex

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type vertexRoundTripFunc func(*http.Request) (*http.Response, error)

func (function vertexRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func apiKeyMeta() *relaycommon.Meta {
	temperature := 0.4
	topP := 0.8
	maxTokens := 128
	request := &protocolkit.GeneralOpenAIRequest{
		Model: "client-gemini", Stream: false, Temperature: &temperature, TopP: &topP, MaxTokens: &maxTokens,
		Messages: []protocolkit.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hello"},
		},
	}
	raw, _ := protocolkit.MarshalJSON(request)
	return &relaycommon.Meta{
		Context: context.Background(),
		Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeVertexAi), Other: "us-central1",
			OtherSettings: `{"vertex_key_type":"api_key"}`},
		Mode: channelcatalog.RelayModeChatCompletions, Format: channelcatalog.RelayFormatGemini,
		RequestPath: "/v1/chat/completions", OriginalModelName: "client-gemini", ModelName: "gemini-2.5-flash",
		APIKey: "AIza-vertex-test", Request: request, RawBody: raw, PromptTokens: 7,
	}
}

func serviceAccountMeta(t *testing.T) (*relaycommon.Meta, Credentials, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	credentials := Credentials{
		Type: "service_account", ProjectID: "vertex-project-123", PrivateKeyID: "key-id-1",
		PrivateKey: privatePEM, ClientEmail: "vertex-test@vertex-project-123.iam.gserviceaccount.com",
		ClientID: "123456789", TokenURI: googleOAuthTokenURL,
	}
	raw, err := json.Marshal(credentials)
	require.NoError(t, err)
	meta := apiKeyMeta()
	meta.Channel.OtherSettings = `{"vertex_key_type":"json"}`
	meta.APIKey = string(raw)
	return meta, credentials, privateKey
}

func TestVertexModelCatalogAndClassification(t *testing.T) {
	models := ModelList()
	require.Len(t, models, len(supportedModels))
	assert.Equal(t, "meta/llama3-405b-instruct-maas", models[0])
	assert.Contains(t, models, "claude-opus-4-8")
	assert.Contains(t, models, "gemini-3.1-pro-preview")
	assert.Contains(t, models, "veo-3.1-fast-generate-preview")
	models[0] = "mutated"
	assert.Equal(t, "meta/llama3-405b-instruct-maas", ModelList()[0])
	assert.Equal(t, requestModeOpenSource, modeForModel("meta/llama3-405b-instruct-maas"))
	assert.Equal(t, requestModeClaude, modeForModel("claude-opus-4-8-high"))
	assert.Equal(t, requestModeGemini, modeForModel("gemini-2.5-flash"))
	assert.Equal(t, "claude-custom-high", vertexClaudeModel("claude-custom-high"))
}

func TestVertexAPIKeyURLHeadersAndGeminiConversion(t *testing.T) {
	meta := apiKeyMeta()
	meta.BaseURL = "https://vertex.example/gateway"
	adapter := &Adaptor{}
	adapter.Init(meta)
	requestURL, err := adapter.GetRequestURL(meta)
	require.NoError(t, err)
	parsed, err := url.Parse(requestURL)
	require.NoError(t, err)
	assert.Equal(t, "/gateway/v1/publishers/google/models/gemini-2.5-flash:generateContent", parsed.Path)
	assert.Equal(t, "AIza-vertex-test", parsed.Query().Get("key"))

	body, err := adapter.ConvertRequest(meta)
	require.NoError(t, err)
	var converted protocolkit.GeminiChatRequest
	require.NoError(t, strictJSON(body, &converted, false))
	assert.Empty(t, converted.Model)
	require.Len(t, converted.Contents, 1)
	assert.Equal(t, "user", converted.Contents[0].Role)
	assert.Equal(t, "hello", converted.Contents[0].Parts[0].Text)
	require.NotNil(t, converted.SystemInstruction)
	assert.Equal(t, "be concise", converted.SystemInstruction.Parts[0].Text)

	request := httptest.NewRequest(http.MethodPost, requestURL, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "must-remove")
	request.Header.Set("x-goog-api-key", "must-remove")
	require.NoError(t, adapter.SetupRequestHeader(request, meta))
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Empty(t, request.Header.Get("x-goog-api-key"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))

	meta.IsStream = true
	meta.Request.Stream = true
	requestURL, err = adapter.GetRequestURL(meta)
	require.NoError(t, err)
	parsed, err = url.Parse(requestURL)
	require.NoError(t, err)
	assert.Equal(t, "/gateway/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent", parsed.Path)
	assert.Equal(t, "sse", parsed.Query().Get("alt"))
	assert.Equal(t, "AIza-vertex-test", parsed.Query().Get("key"))
}

func TestVertexServiceAccountRegionalURLsAndOAuthHeaders(t *testing.T) {
	meta, credentials, _ := serviceAccountMeta(t)
	meta.Channel.Other = `{"client-gemini":"europe-west4","default":"global"}`
	adapter := &Adaptor{tokenProvider: func(ctx context.Context, raw string, got Credentials) (string, error) {
		assert.NotEmpty(t, raw)
		assert.Equal(t, credentials.ProjectID, got.ProjectID)
		return "ya29.test-access-token", nil
	}}
	adapter.Init(meta)
	requestURL, err := adapter.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t,
		"https://europe-west4-aiplatform.googleapis.com/v1/projects/vertex-project-123/locations/europe-west4/publishers/google/models/gemini-2.5-flash:generateContent",
		requestURL)

	request := httptest.NewRequest(http.MethodPost, requestURL, nil)
	require.NoError(t, adapter.SetupRequestHeader(request, meta))
	assert.Equal(t, "Bearer ya29.test-access-token", request.Header.Get("Authorization"))
	assert.Equal(t, credentials.ProjectID, request.Header.Get("x-goog-user-project"))
	assert.Empty(t, request.Header.Get("x-goog-api-key"))

	meta.ModelName = "claude-3-5-sonnet-20241022"
	meta.OriginalModelName = meta.ModelName
	meta.Channel.Other = "us-east5"
	adapter.Init(meta)
	adapter.tokenProvider = func(context.Context, string, Credentials) (string, error) { return "token", nil }
	requestURL, err = adapter.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Contains(t, requestURL, "/publishers/anthropic/models/claude-3-5-sonnet-v2@20241022:rawPredict")

	meta.ModelName = "meta/llama3-405b-instruct-maas"
	meta.OriginalModelName = meta.ModelName
	adapter.Init(meta)
	requestURL, err = adapter.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t,
		"https://us-east5-aiplatform.googleapis.com/v1beta1/projects/vertex-project-123/locations/us-east5/endpoints/openapi/chat/completions",
		requestURL)
}

func TestVertexSignedJWTAndBoundedOAuthExchange(t *testing.T) {
	_, credentials, privateKey := serviceAccountMeta(t)
	now := time.Unix(1_800_000_000, 0)
	signed, err := createSignedJWT(credentials, now)
	require.NoError(t, err)
	token, err := jwt.Parse(signed, func(token *jwt.Token) (any, error) {
		assert.Equal(t, jwt.SigningMethodRS256.Alg(), token.Method.Alg())
		return &privateKey.PublicKey, nil
	}, jwt.WithTimeFunc(func() time.Time { return now }))
	require.NoError(t, err)
	require.True(t, token.Valid)
	claims := token.Claims.(jwt.MapClaims)
	assert.Equal(t, credentials.ClientEmail, claims["iss"])
	assert.Equal(t, googleCloudScope, claims["scope"])
	assert.Equal(t, googleOAuthTokenURL, claims["aud"])
	assert.EqualValues(t, now.Add(defaultOAuthJWTLifetime).Unix(), claims["exp"])
	assert.Equal(t, credentials.PrivateKeyID, token.Header["kid"])

	var gotGrant, gotAssertion string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPost, request.Method)
		require.NoError(t, request.ParseForm())
		gotGrant = request.Form.Get("grant_type")
		gotAssertion = request.Form.Get("assertion")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"oauth-token","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	accessToken, err := exchangeServiceAccountTokenWithClient(context.Background(), credentials, server.URL, server.Client(), now)
	require.NoError(t, err)
	assert.Equal(t, "oauth-token", accessToken.value)
	assert.Equal(t, now.Add(time.Hour), accessToken.expiresAt)
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", gotGrant)
	assert.NotEmpty(t, gotAssertion)

	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"access_token":"first","access_token":"second","expires_in":3600}`)
	}))
	defer badServer.Close()
	_, err = exchangeServiceAccountTokenWithClient(context.Background(), credentials, badServer.URL, badServer.Client(), now)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "first")

	overflowServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"access_token":"token","expires_in":9223372036854775807}`)
	}))
	defer overflowServer.Close()
	_, err = exchangeServiceAccountTokenWithClient(context.Background(), credentials, overflowServer.URL, overflowServer.Client(), now)
	require.Error(t, err)
}

func TestVertexMalformedPrivateKeyFailsBeforeOAuthNetwork(t *testing.T) {
	meta := apiKeyMeta()
	meta.Channel.OtherSettings = `{"vertex_key_type":"json"}`
	meta.APIKey = `{"type":"service_account","project_id":"vertex-project-123","private_key":"must-not-leave","client_email":"credential@example.test"}`
	adapter := &Adaptor{}
	adapter.Init(meta)
	requestURL, err := adapter.GetRequestURL(meta)
	require.NoError(t, err)

	var calls atomic.Int32
	previousClient := oauthHTTPClient
	oauthHTTPClient = &http.Client{Transport: vertexRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("OAuth transport must not be reached")
	})}
	t.Cleanup(func() { oauthHTTPClient = previousClient })

	request := httptest.NewRequest(http.MethodPost, requestURL, nil)
	err = adapter.SetupRequestHeader(request, meta)
	require.Error(t, err)
	assert.Zero(t, calls.Load())
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.NotContains(t, err.Error(), "must-not-leave")
	assert.NotContains(t, err.Error(), "credential@example.test")
}

func TestVertexServiceAccountTokenCacheSingleFlights(t *testing.T) {
	meta, credentials, _ := serviceAccountMeta(t)
	rawCredential := meta.APIKey
	cacheKey := sha256.Sum256([]byte(rawCredential))
	serviceTokenCache.Lock()
	delete(serviceTokenCache.entries, cacheKey)
	delete(serviceTokenCache.inFlight, cacheKey)
	serviceTokenCache.Unlock()
	t.Cleanup(func() {
		serviceTokenCache.Lock()
		delete(serviceTokenCache.entries, cacheKey)
		delete(serviceTokenCache.inFlight, cacheKey)
		serviceTokenCache.Unlock()
	})

	var calls atomic.Int32
	previousClient := oauthHTTPClient
	oauthHTTPClient = &http.Client{Transport: vertexRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"single-flight-token","token_type":"Bearer","expires_in":3600}`)),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { oauthHTTPClient = previousClient })

	const workers = 16
	start := make(chan struct{})
	results := make(chan string, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			<-start
			token, err := cachedServiceAccountToken(context.Background(), rawCredential, credentials)
			results <- token
			errorsFound <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}
	for token := range results {
		assert.Equal(t, "single-flight-token", token)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestVertexServiceAccountTokenCacheBoundsConcurrentRefreshes(t *testing.T) {
	_, credentials, _ := serviceAccountMeta(t)
	serviceTokenCache.Lock()
	savedInFlight := serviceTokenCache.inFlight
	serviceTokenCache.inFlight = make(map[[sha256.Size]byte]*tokenCall, maxTokenCacheEntries)
	for index := 0; index < maxTokenCacheEntries; index++ {
		key := sha256.Sum256([]byte(fmt.Sprintf("credential-%d", index)))
		serviceTokenCache.inFlight[key] = &tokenCall{done: make(chan struct{})}
	}
	serviceTokenCache.Unlock()
	t.Cleanup(func() {
		serviceTokenCache.Lock()
		serviceTokenCache.inFlight = savedInFlight
		serviceTokenCache.Unlock()
	})

	_, err := cachedServiceAccountToken(nil, "overflow-credential", credentials)
	require.EqualError(t, err, "Vertex AI OAuth token cache is busy")
}

func TestVertexClaudeAndNativeGeminiConversion(t *testing.T) {
	meta := apiKeyMeta()
	meta.ModelName = "claude-opus-4-20250514"
	meta.OriginalModelName = "client-claude"
	meta.Request.Model = "client-claude"
	meta.RawBody, _ = protocolkit.MarshalJSON(meta.Request)
	adapter := &Adaptor{}
	adapter.Init(meta)
	body, err := adapter.ConvertRequest(meta)
	require.NoError(t, err)
	var claudeBody map[string]any
	require.NoError(t, strictJSON(body, &claudeBody, false))
	assert.Equal(t, vertexAnthropicVersion, claudeBody["anthropic_version"])
	assert.NotContains(t, claudeBody, "model")
	assert.Equal(t, float64(128), claudeBody["max_tokens"])

	nativeRaw := []byte(`{"model":"models/gemini-2.5-flash","cachedContent":"cachedContents/owned","contents":[{"role":"user","parts":[{"text":"native hello"}]}]}`)
	native := apiKeyMeta()
	native.Mode = channelcatalog.RelayModeGemini
	native.RequestPath = "/v1beta/models/gemini-2.5-flash:generateContent"
	native.RawBody = nativeRaw
	nativeAdapter := &Adaptor{}
	nativeAdapter.Init(native)
	body, err = nativeAdapter.ConvertRequest(native)
	require.NoError(t, err)
	var geminiBody map[string]any
	require.NoError(t, strictJSON(body, &geminiBody, false))
	assert.NotContains(t, geminiBody, "model")
	assert.Equal(t, "cachedContents/owned", geminiBody["cachedContent"])
	assert.Contains(t, string(body), "native hello")
}

func TestVertexClaudePseudoModelsApplyReferenceThinkingSemantics(t *testing.T) {
	t.Run("Opus 4.8 effort", func(t *testing.T) {
		meta := apiKeyMeta()
		meta.ModelName = "claude-opus-4-8-high"
		meta.OriginalModelName = "client-claude"
		adapter := &Adaptor{}
		adapter.Init(meta)

		requestURL, err := adapter.GetRequestURL(meta)
		require.NoError(t, err)
		assert.Contains(t, requestURL, "/publishers/anthropic/models/claude-opus-4-8:rawPredict")
		body, err := adapter.ConvertRequest(meta)
		require.NoError(t, err)
		var converted map[string]any
		require.NoError(t, strictJSON(body, &converted, false))
		assert.Equal(t, map[string]any{"type": "adaptive", "display": "summarized"}, converted["thinking"])
		assert.Equal(t, map[string]any{"effort": "high"}, converted["output_config"])
		assert.NotContains(t, converted, "temperature")
		assert.NotContains(t, converted, "top_p")
	})

	t.Run("Opus 4.6 effort", func(t *testing.T) {
		meta := apiKeyMeta()
		meta.ModelName = "claude-opus-4-6-low"
		adapter := &Adaptor{}
		adapter.Init(meta)
		body, err := adapter.ConvertRequest(meta)
		require.NoError(t, err)
		var converted map[string]any
		require.NoError(t, strictJSON(body, &converted, false))
		assert.Equal(t, map[string]any{"type": "adaptive"}, converted["thinking"])
		assert.Equal(t, map[string]any{"effort": "low"}, converted["output_config"])
		assert.Equal(t, float64(1), converted["temperature"])
		assert.NotContains(t, converted, "top_p")
	})

	t.Run("legacy thinking alias", func(t *testing.T) {
		meta := apiKeyMeta()
		meta.ModelName = "claude-sonnet-4-20250514-thinking"
		adapter := &Adaptor{}
		adapter.Init(meta)
		requestURL, err := adapter.GetRequestURL(meta)
		require.NoError(t, err)
		assert.Contains(t, requestURL, "/publishers/anthropic/models/claude-sonnet-4@20250514:rawPredict")
		body, err := adapter.ConvertRequest(meta)
		require.NoError(t, err)
		var converted map[string]any
		require.NoError(t, strictJSON(body, &converted, false))
		assert.Equal(t, float64(1280), converted["max_tokens"])
		assert.Equal(t, map[string]any{"type": "enabled", "budget_tokens": float64(1024)}, converted["thinking"])
		assert.Equal(t, float64(1), converted["temperature"])
		assert.NotContains(t, converted, "top_p")
	})

	t.Run("native Opus 4.8 thinking alias", func(t *testing.T) {
		meta := apiKeyMeta()
		meta.ModelName = "claude-opus-4-8-thinking"
		meta.Format = channelcatalog.RelayFormatClaude
		meta.RequestPath = "/v1/messages"
		meta.RawBody = []byte(`{"model":"client","max_tokens":128,"temperature":0.2,"top_p":0.8,"top_k":10,"messages":[{"role":"user","content":"hello"}]}`)
		adapter := &Adaptor{}
		adapter.Init(meta)
		body, err := adapter.ConvertRequest(meta)
		require.NoError(t, err)
		var converted map[string]any
		require.NoError(t, strictJSON(body, &converted, false))
		assert.Equal(t, map[string]any{"type": "adaptive", "display": "summarized"}, converted["thinking"])
		assert.Equal(t, map[string]any{"effort": "high"}, converted["output_config"])
		assert.NotContains(t, converted, "temperature")
		assert.NotContains(t, converted, "top_p")
		assert.NotContains(t, converted, "top_k")
	})
}

func TestVertexClientCannotOverrideChannelRegion(t *testing.T) {
	meta := apiKeyMeta()
	meta.Channel.Other = ""
	meta.APIVersion = "europe-west4"
	adapter := &Adaptor{}
	adapter.Init(meta)
	requestURL, err := adapter.GetRequestURL(meta)
	require.NoError(t, err)
	parsed, err := url.Parse(requestURL)
	require.NoError(t, err)
	assert.Equal(t, "aiplatform.googleapis.com", parsed.Host)
	assert.NotContains(t, requestURL, "europe-west4")
}

func TestVertexRejectsAmbiguousCredentialsURLsAndInputs(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"duplicate key type", func(meta *relaycommon.Meta) {
			meta.Channel.OtherSettings = `{"vertex_key_type":"json","vertex_key_type":"api_key"}`
		}},
		{"unknown key type", func(meta *relaycommon.Meta) { meta.Channel.OtherSettings = `{"vertex_key_type":"magic"}` }},
		{"credential control", func(meta *relaycommon.Meta) { meta.APIKey = "bad\nkey" }},
		{"invalid region", func(meta *relaycommon.Meta) { meta.Channel.Other = "us-central1/../../metadata" }},
		{"plaintext base", func(meta *relaycommon.Meta) { meta.BaseURL = "http://vertex.example" }},
		{"base query", func(meta *relaycommon.Meta) { meta.BaseURL = "https://vertex.example?target=internal" }},
		{"base userinfo", func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@vertex.example" }},
		{"model traversal", func(meta *relaycommon.Meta) { meta.ModelName = "../metadata" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			meta := apiKeyMeta()
			test.mutate(meta)
			adapter := &Adaptor{}
			adapter.Init(meta)
			_, err := adapter.GetRequestURL(meta)
			require.Error(t, err)
		})
	}

	meta := apiKeyMeta()
	meta.RawBody = []byte(`{"model":"client-gemini","model":"attacker","messages":[{"role":"user","content":"hello"}]}`)
	adapter := &Adaptor{}
	adapter.Init(meta)
	_, err := adapter.ConvertRequest(meta)
	require.Error(t, err)

	meta = apiKeyMeta()
	value := math.NaN()
	meta.Request.Temperature = &value
	meta.RawBody = nil
	adapter.Init(meta)
	_, err = adapter.ConvertRequest(meta)
	require.Error(t, err)
}

func TestVertexGeminiBlockingNativeAndAcceptedFailureSemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	providerBody := `{
		"candidates":[{"content":{"role":"model","parts":[{"text":"vertex reply"}]},"finishReason":"STOP","index":0}],
		"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}
	}`
	meta := apiKeyMeta()
	adapter := &Adaptor{}
	adapter.Init(meta)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adapter.DoResponse(ctx, vertexResponse(http.StatusOK, providerBody), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"model":"client-gemini"`)
	assert.Contains(t, recorder.Body.String(), "vertex reply")

	native := apiKeyMeta()
	native.Mode = channelcatalog.RelayModeGemini
	native.RequestPath = "/v1beta/models/gemini-2.5-flash:generateContent"
	adapter.Init(native)
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	usage, err = adapter.DoResponse(ctx, vertexResponse(http.StatusOK, providerBody), native)
	require.NoError(t, err)
	assert.JSONEq(t, providerBody, recorder.Body.String())
	assert.Equal(t, 10, usage.TotalTokens)

	adapter.Init(meta)
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	usage, err = adapter.DoResponse(ctx, vertexResponse(http.StatusOK, `{"candidates":`), meta)
	require.Error(t, err)
	require.NotNil(t, usage, "malformed 2xx response must prevent retry/refund")
	assert.Equal(t, 7, usage.PromptTokens)

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	usage, err = adapter.DoResponse(ctx, vertexResponse(http.StatusOK,
		`{"error":{"code":400,"message":"definitive rejection","status":"INVALID_ARGUMENT"}}`), meta)
	require.Error(t, err)
	assert.Nil(t, usage, "definitive provider rejection remains refundable")
}

func TestVertexGeminiStreamingConversionAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := apiKeyMeta()
	meta.IsStream = true
	meta.Request.Stream = true
	adapter := &Adaptor{}
	adapter.Init(meta)
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello "}]},"index":0}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"world"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":9}}`,
		"", "",
	}, "\n")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adapter.DoResponse(ctx, vertexResponse(http.StatusOK, stream), meta)
	require.NoError(t, err)
	assert.Equal(t, 9, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"role":"assistant"`)
	assert.Contains(t, recorder.Body.String(), `"content":"hello "`)
	assert.Contains(t, recorder.Body.String(), `"content":"world"`)
	assert.Contains(t, recorder.Body.String(), "data: [DONE]")

	oversized := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n\n"
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	usage, err = adapter.DoResponse(ctx, vertexResponse(http.StatusOK, oversized), meta)
	require.Error(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, err.Error(), "maximum event")
}

func TestVertexClaudeStreamingConversionUsageAndStrictEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := apiKeyMeta()
	meta.ModelName = "claude-opus-4-20250514"
	meta.OriginalModelName = "client-claude"
	meta.IsStream = true
	meta.Request.Stream = true
	adapter := &Adaptor{}
	adapter.Init(meta)
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"vertex claude"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`,
		"", "",
	}, "\n")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adapter.DoResponse(ctx, vertexResponse(http.StatusOK, stream), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	assert.Equal(t, 7, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"model":"client-claude"`)
	assert.Contains(t, recorder.Body.String(), `"content":"vertex claude"`)
	assert.Contains(t, recorder.Body.String(), "data: [DONE]")

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	usage, err = adapter.DoResponse(ctx, vertexResponse(http.StatusOK,
		`data: {"type":"message_start","type":"message_stop"}`+"\n\n"), meta)
	require.Error(t, err)
	require.NotNil(t, usage, "a malformed accepted stream must remain settled")
}

func TestVertexNativeClaudeRequestAndStreamRemainNative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := apiKeyMeta()
	meta.ModelName = "claude-sonnet-4-20250514"
	meta.OriginalModelName = "client-claude"
	meta.Format = channelcatalog.RelayFormatClaude
	meta.RequestPath = "/v1/messages"
	meta.IsStream = true
	meta.RawBody = []byte(`{
		"model":"client-claude","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"hello"}],
		"output_config":{"effort":"high"}
	}`)
	adapter := &Adaptor{}
	adapter.Init(meta)
	body, err := adapter.ConvertRequest(meta)
	require.NoError(t, err)
	var providerRequest map[string]any
	require.NoError(t, json.Unmarshal(body, &providerRequest))
	assert.NotContains(t, providerRequest, "model")
	assert.Equal(t, vertexAnthropicVersion, providerRequest["anthropic_version"])
	assert.Contains(t, providerRequest, "output_config")

	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native claude"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"", "",
	}, "\n")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adapter.DoResponse(ctx, vertexResponse(http.StatusOK, stream), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), "event: message_start")
	assert.Contains(t, recorder.Body.String(), `"type":"content_block_delta"`)
	assert.NotContains(t, recorder.Body.String(), `"choices"`)
}

func vertexResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
