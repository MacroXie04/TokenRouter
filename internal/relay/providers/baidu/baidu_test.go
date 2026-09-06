package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type baiduRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn baiduRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func legacyMeta(mode channelcatalog.RelayMode) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(channelcatalog.ChannelTypeBaidu)},
		Mode:      mode,
		Format:    relaycommon.GetRelayFormat(channelcatalog.ChannelTypeBaidu, mode),
		ModelName: "ERNIE-4.0-8K",
		BaseURL:   "https://aip.baidubce.com",
		APIKey:    "client-id|client-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model:    "client-model",
			Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
		PromptTokens: 5,
	}
}

func baiduTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func baiduTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	return ctx, recorder
}

func TestBaiduLegacyURLsOAuthHeadersAndModelCatalog(t *testing.T) {
	tests := []struct {
		model string
		mode  channelcatalog.RelayMode
		path  string
	}{
		{model: "ERNIE-4.0-8K", mode: channelcatalog.RelayModeChatCompletions, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/completions_pro"},
		{model: "ERNIE-3.5-8K", mode: channelcatalog.RelayModeChatCompletions, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/completions"},
		{model: "ERNIE-Speed-128K", mode: channelcatalog.RelayModeChatCompletions, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/ernie-speed-128k"},
		{model: "Embedding-V1", mode: channelcatalog.RelayModeEmbeddings, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/embeddings/embedding-v1"},
		{model: "bge-large-zh", mode: channelcatalog.RelayModeEmbeddings, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/embeddings/bge_large_zh"},
		{model: "tao-8k", mode: channelcatalog.RelayModeEmbeddings, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/embeddings/tao_8k"},
		{model: "custom-model", mode: channelcatalog.RelayModeChatCompletions, path: "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/custom-model"},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			meta := legacyMeta(test.mode)
			meta.ModelName = test.model
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, "https://aip.baidubce.com"+test.path, requestURL)
		})
	}

	var tokenCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tokenCalls.Add(1)
		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "/oauth/2.0/token", req.URL.Path)
		assert.Equal(t, "client_credentials", req.URL.Query().Get("grant_type"))
		assert.Equal(t, "client id", req.URL.Query().Get("client_id"))
		assert.Equal(t, "secret/+", req.URL.Query().Get("client_secret"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", req.Header.Get("Accept"))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "oauth-token", "expires_in": 7200})
	}))
	defer upstream.Close()
	manager := newAccessTokenManager(upstream.Client())
	meta := legacyMeta(channelcatalog.RelayModeChatCompletions)
	meta.BaseURL = upstream.URL
	meta.APIKey = "client id|secret/+"
	adaptor := &Adaptor{tokens: manager}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, requestURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer must-remove")
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Equal(t, "oauth-token", req.URL.Query().Get("access_token"))
	assert.Empty(t, req.Header.Get("Authorization"), "the client secret must not reach the inference endpoint")
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", req.Header.Get("Accept"))
	assert.Equal(t, int32(1), tokenCalls.Load())

	bad := legacyMeta(channelcatalog.RelayModeChatCompletions)
	bad.APIKey = "not-a-pair"
	request, err := http.NewRequest(http.MethodPost, "https://aip.baidubce.com/rpc", nil)
	require.NoError(t, err)
	adaptor = &Adaptor{tokens: manager}
	adaptor.Init(bad)
	assert.ErrorContains(t, adaptor.SetupRequestHeader(request, bad), "client_id|client_secret")

	unsupported := legacyMeta(channelcatalog.RelayModeImagesGenerations)
	adaptor.Init(unsupported)
	_, err = adaptor.GetRequestURL(unsupported)
	assert.ErrorContains(t, err, "does not support")

	models := ModelList()
	require.Contains(t, models, "ERNIE-4.0-8K")
	require.Contains(t, models, "Embedding-V1")
	models[0] = "mutated"
	assert.Equal(t, "ERNIE-4.0-8K", ModelList()[0])
}

func TestBaiduAccessTokenCacheSingleflightAndExpiry(t *testing.T) {
	var calls atomic.Int32
	var failRefresh atomic.Bool
	var nowMu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		call := calls.Add(1)
		if failRefresh.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"temporarily_unavailable","error_description":"secret must not be logged"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-" + string(rune('0'+call)),
			"expires_in":   100,
		})
	}))
	defer server.Close()
	manager := newAccessTokenManager(server.Client())
	manager.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	manager.maxRefreshBefore = 10 * time.Second

	getConcurrently := func(want string) {
		const workers = 32
		start := make(chan struct{})
		errs := make(chan error, workers)
		var group sync.WaitGroup
		for range workers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				token, err := manager.token(context.Background(), server.URL, "id|secret")
				if err == nil && token != want {
					err = assert.AnError
				}
				errs <- err
			}()
		}
		close(start)
		group.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
	}

	getConcurrently("token-1")
	assert.Equal(t, int32(1), calls.Load(), "concurrent cold misses must coalesce")
	nowMu.Lock()
	now = now.Add(89 * time.Second)
	nowMu.Unlock()
	getConcurrently("token-1")
	assert.Equal(t, int32(1), calls.Load())
	nowMu.Lock()
	now = now.Add(2 * time.Second)
	nowMu.Unlock()
	getConcurrently("token-2")
	assert.Equal(t, int32(2), calls.Load(), "refresh-window misses must coalesce")

	manager.mu.Lock()
	for key, entry := range manager.entries {
		entry.refreshAt = now.Add(-time.Second)
		entry.expiresAt = now.Add(time.Minute)
		manager.entries[key] = entry
	}
	manager.mu.Unlock()
	failRefresh.Store(true)
	token, err := manager.token(t.Context(), server.URL, "id|secret")
	require.NoError(t, err, "a refresh failure may use the still-valid cached token")
	assert.Equal(t, "token-2", token)
}

func TestBaiduOAuthRejectsUnsafeLifetimeAndRedactsTransportURLs(t *testing.T) {
	transport := errors.New("dial failed for client-secret")
	manager := newAccessTokenManager(&http.Client{Transport: baiduRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transport
	})})
	_, err := manager.fetch(t.Context(), "https://aip.baidubce.com", "client-id", "client-secret")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "client-secret")
	assert.NotErrorIs(t, err, transport, "the credential-bearing transport error must not be unwrapped")

	manager = newAccessTokenManager(&http.Client{Transport: baiduRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"access_token":"token","expires_in":9223372036854775807}`)),
		}, nil
	})})
	_, err = manager.fetch(t.Context(), "https://aip.baidubce.com", "client-id", "client-secret")
	require.ErrorContains(t, err, "invalid token lifetime")
}

func TestBaiduLegacyRequestConversion(t *testing.T) {
	maxTokens := 20
	maxCompletion := 1
	temperature := 0.7
	topP := 0.8
	frequency := 1.2
	meta := legacyMeta(channelcatalog.RelayModeChatCompletions)
	meta.ModelName = "ERNIE-3.5-8K"
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.MaxTokens = &maxTokens
	meta.Request.MaxCompletionTokens = &maxCompletion
	meta.Request.Temperature = &temperature
	meta.Request.TopP = &topP
	meta.Request.FrequencyPenalty = &frequency
	meta.Request.User = "stable-user"
	meta.Request.Messages = []protocolkit.Message{
		{Role: "system", Content: "first policy"},
		{Role: "system", Content: []any{map[string]any{"type": "text", "text": "effective policy"}}},
		{Role: "user", Content: []any{map[string]any{"type": "text", "text": "hello "}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/image"}}, map[string]any{"type": "text", "text": "world"}}},
		{Role: "assistant", Content: "answer"},
	}
	meta.Request.Extra = map[string]any{"group": "dashboard-only", "provider_extension": "drop"}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.NotContains(t, decoded, "model")
	assert.NotContains(t, decoded, "group")
	assert.NotContains(t, decoded, "provider_extension")
	assert.Equal(t, "effective policy", decoded["system"])
	assert.Equal(t, true, decoded["stream"])
	assert.EqualValues(t, 2, decoded["max_output_tokens"], "the legacy endpoint rejects a one-token output limit")
	assert.EqualValues(t, 0.7, decoded["temperature"])
	assert.EqualValues(t, 0.8, decoded["top_p"])
	assert.EqualValues(t, 1.2, decoded["penalty_score"])
	assert.Equal(t, "stable-user", decoded["user_id"])
	messages := decoded["messages"].([]any)
	require.Len(t, messages, 2)
	assert.Equal(t, "hello world", messages[0].(map[string]any)["content"])
	assert.Equal(t, "client-model", meta.Request.Model)

	embedding := legacyMeta(channelcatalog.RelayModeEmbeddings)
	embedding.ModelName = "bge-large-en"
	embedding.Request.Messages = nil
	embedding.Request.Extra = map[string]any{"input": []any{"first", 3.0, "second"}, "model": "client-model"}
	adaptor.Init(embedding)
	body, err = adaptor.ConvertRequest(embedding)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, []any{"first", "second"}, decoded["input"])
	assert.NotContains(t, decoded, "model")

	embedding.IsStream = true
	embedding.Request.Stream = true
	adaptor.Init(embedding)
	_, err = adaptor.ConvertRequest(embedding)
	assert.ErrorContains(t, err, "stream")

	tooLarge := legacyMeta(channelcatalog.RelayModeChatCompletions)
	tooLarge.RawBody = make([]byte, maxBaiduRequestBodyBytes+1)
	adaptor.Init(tooLarge)
	_, err = adaptor.ConvertRequest(tooLarge)
	assert.ErrorContains(t, err, "exceeds")
}

func TestBaiduLegacyResponsesAndStreamUsage(t *testing.T) {
	meta := legacyMeta(channelcatalog.RelayModeChatCompletions)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	ctx, recorder := baiduTestContext()
	usage, err := adaptor.DoResponse(ctx, baiduTestResponse(http.StatusOK,
		`{"id":"ernie-1","object":"chat.completion","created":123,"result":"answer","usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}, usage)
	var output map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, "ernie-1", output["id"])
	assert.Equal(t, "ERNIE-4.0-8K", output["model"])
	assert.Equal(t, "answer", output["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])

	embedding := legacyMeta(channelcatalog.RelayModeEmbeddings)
	embedding.ModelName = "Embedding-V1"
	adaptor.Init(embedding)
	ctx, recorder = baiduTestContext()
	usage, err = adaptor.DoResponse(ctx, baiduTestResponse(http.StatusOK,
		`{"id":"embedding-1","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`), embedding)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 4, TotalTokens: 4}, usage)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, "list", output["object"])
	assert.Equal(t, "baidu-embedding", output["model"])

	stream := legacyMeta(channelcatalog.RelayModeChatCompletions)
	stream.IsStream = true
	stream.Request.Stream = true
	adaptor.Init(stream)
	ctx, recorder = baiduTestContext()
	usage, err = adaptor.DoResponse(ctx, baiduTestResponse(http.StatusOK,
		"data: {\"id\":\"stream-1\",\"created\":124,\"result\":\"hello\",\"sentence_id\":0,\"is_end\":false}\n\n"+
			"data: {\"id\":\"stream-1\",\"created\":124,\"result\":\" world\",\"sentence_id\":1,\"is_end\":true,\"usage\":{\"prompt_tokens\":8,\"total_tokens\":13}}\n\n"+
			"data: [DONE]\n\n"), stream)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 8, CompletionTokens: 5, TotalTokens: 13}, usage)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
}

func TestBaiduLegacyErrorsAndResponseBounds(t *testing.T) {
	meta := legacyMeta(channelcatalog.RelayModeChatCompletions)
	meta.Channel.StatusCodeMapping = `{"429":503}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	ctx, _ := baiduTestContext()
	_, err := adaptor.DoResponse(ctx, baiduTestResponse(http.StatusTooManyRequests,
		`{"error_code":18,"error_msg":"busy for client-secret"}`), meta)
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "busy")

	ctx, _ = baiduTestContext()
	_, err = adaptor.DoResponse(ctx, baiduTestResponse(http.StatusOK,
		`{"error_code":17,"error_msg":"quota exhausted"}`), meta)
	require.Error(t, err)
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusBadRequest, upstream.StatusCode)

	ctx, _ = baiduTestContext()
	_, err = adaptor.DoResponse(ctx, baiduTestResponse(http.StatusOK,
		`{"result":"`+strings.Repeat("x", int(relaycommon.MaxUpstreamJSONBodyBytes))+`"}`), meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)

	stream := legacyMeta(channelcatalog.RelayModeChatCompletions)
	stream.IsStream = true
	stream.Request.Stream = true
	adaptor.Init(stream)
	ctx, _ = baiduTestContext()
	_, err = adaptor.DoResponse(ctx, baiduTestResponse(http.StatusOK,
		"data: {\"result\":\""+strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes)+"\"}\n"), stream)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum event")
}
