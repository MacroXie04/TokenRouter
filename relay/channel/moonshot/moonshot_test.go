package moonshot

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func moonshotMeta(mode constant.RelayMode, format constant.RelayFormat) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(constant.ChannelTypeMoonshot)},
		Mode:      mode,
		Format:    format,
		ModelName: "kimi-k2.5",
		APIKey:    "moonshot-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model:    "client-model",
			Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
	}
}

func moonshotContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	return ctx, recorder
}

func moonshotResponse(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(body)}
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(body, &decoded))
	return decoded
}

func TestMoonshotURLsAuthenticationAndModeGates(t *testing.T) {
	tests := []struct {
		name       string
		mode       constant.RelayMode
		format     constant.RelayFormat
		base       string
		expected   string
		wantAccept string
	}{
		{name: "default chat", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatOpenAI, expected: "https://api.moonshot.cn/v1/chat/completions", wantAccept: "application/json"},
		{name: "v1 custom base", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatOpenAI, base: "https://proxy.example/v1/", expected: "https://proxy.example/v1/chat/completions", wantAccept: "application/json"},
		{name: "completions", mode: constant.RelayModeCompletions, format: constant.RelayFormatOpenAI, expected: "https://api.moonshot.cn/v1/completions", wantAccept: "application/json"},
		{name: "embeddings", mode: constant.RelayModeEmbeddings, format: constant.RelayFormatEmbedding, expected: "https://api.moonshot.cn/v1/embeddings", wantAccept: "application/json"},
		{name: "rerank", mode: constant.RelayModeRerank, format: constant.RelayFormatRerank, expected: "https://api.moonshot.cn/v1/rerank", wantAccept: "application/json"},
		{name: "Claude protocol", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatClaude, expected: "https://api.moonshot.cn/anthropic/v1/messages", wantAccept: "application/json"},
		{name: "Kimi coding OpenAI", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatOpenAI, base: kimiCodingPlanBase, expected: "https://api.kimi.com/coding/v1/chat/completions", wantAccept: "application/json"},
		{name: "Kimi coding Claude", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatClaude, base: kimiCodingPlanBase, expected: "https://api.kimi.com/coding/v1/messages", wantAccept: "application/json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := moonshotMeta(test.mode, test.format)
			meta.BaseURL = test.base
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.expected, requestURL)

			req, err := http.NewRequest(http.MethodPost, requestURL, nil)
			require.NoError(t, err)
			require.NoError(t, adaptor.SetupRequestHeader(req, meta))
			assert.Equal(t, "Bearer moonshot-secret", req.Header.Get("Authorization"))
			assert.Empty(t, req.Header.Get("x-api-key"))
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, test.wantAccept, req.Header.Get("Accept"))
			assert.Empty(t, req.Header.Get("anthropic-version"))
		})
	}

	streamMeta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	streamMeta.IsStream = true
	streamAdaptor := &Adaptor{}
	streamAdaptor.Init(streamMeta)
	req, err := http.NewRequest(http.MethodPost, defaultBaseURL, nil)
	require.NoError(t, err)
	require.NoError(t, streamAdaptor.SetupRequestHeader(req, streamMeta))
	assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))

	missingKey := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	missingKey.APIKey = " "
	missingKeyAdaptor := &Adaptor{}
	missingKeyAdaptor.Init(missingKey)
	require.Error(t, missingKeyAdaptor.SetupRequestHeader(req, missingKey))

	for _, invalidBase := range []string{
		"ftp://moonshot.example",
		"https://user:password@moonshot.example",
		"https://moonshot.example?secret=query",
		"https://moonshot.example#fragment",
	} {
		meta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
		meta.BaseURL = invalidBase
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err, invalidBase)
	}

	unsupported := moonshotMeta(constant.RelayModeResponses, constant.RelayFormatOpenAIResponses)
	adaptor := &Adaptor{}
	adaptor.Init(unsupported)
	_, err = adaptor.GetRequestURL(unsupported)
	assert.Error(t, err)
	_, err = adaptor.ConvertRequest(unsupported)
	assert.Error(t, err)
}

func TestMoonshotRequestConversionUsesMappedModelAndKimiTemperatureContract(t *testing.T) {
	temperature := 0.2
	meta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	meta.ModelName = "KIMI-K2.6"
	meta.Request.Temperature = &temperature
	meta.Request.Stream = true
	meta.Request.StreamOptions = &protocolkit.StreamOptions{IncludeUsage: true}
	meta.Request.Extra = map[string]any{"provider_extension": "kept", "group": "dashboard-only"}
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeBody(t, body)
	assert.Equal(t, "KIMI-K2.6", decoded["model"])
	assert.EqualValues(t, 1, decoded["temperature"])
	assert.Equal(t, true, decoded["stream_options"].(map[string]any)["include_usage"])
	assert.Equal(t, "kept", decoded["provider_extension"])
	assert.NotContains(t, decoded, "group")
	assert.InDelta(t, 0.2, *meta.Request.Temperature, 0.0001, "conversion must not mutate the retry/accounting snapshot")

	meta.Request.Temperature = nil
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.NotContains(t, decodeBody(t, body), "temperature")

	meta.ModelName = "kimi-k2.5"
	meta.Request.Temperature = &temperature
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.InDelta(t, 0.2, decodeBody(t, body)["temperature"], 0.0001)
}

func TestMoonshotClaudeRequestConversionUsesMappedModel(t *testing.T) {
	maxTokens := 64
	meta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatClaude)
	meta.ModelName = "kimi-claude-upstream"
	meta.Request.MaxTokens = &maxTokens
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var converted protocolkit.ClaudeRequest
	require.NoError(t, protocolkit.UnmarshalJSON(body, &converted))
	assert.Equal(t, "kimi-claude-upstream", converted.Model)
	assert.Equal(t, 64, converted.MaxTokens)
	require.Len(t, converted.Messages, 1)
	assert.Equal(t, "user", converted.Messages[0].Role)
}

func TestMoonshotNonStreamUsageNormalizationAndFallback(t *testing.T) {
	meta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	meta.PromptTokens = 11
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	t.Run("nested cached tokens feed billing without rewriting valid provider body", func(t *testing.T) {
		body := `{"id":"chatcmpl-moonshot","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop","usage":{"cached_tokens":4}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
		ctx, recorder := moonshotContext()
		usage, err := adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, strings.NewReader(body)), meta)
		require.NoError(t, err)
		require.NotNil(t, usage)
		assert.Equal(t, 10, usage.PromptTokens)
		assert.Equal(t, 2, usage.CompletionTokens)
		require.NotNil(t, usage.PromptTokensDetails)
		assert.Equal(t, 4, usage.PromptTokensDetails.CachedTokens)
		assert.JSONEq(t, body, recorder.Body.String())
	})

	t.Run("standard cached details take precedence", func(t *testing.T) {
		body := `{"choices":[{"usage":{"cached_tokens":9}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":3}}}`
		ctx, _ := moonshotContext()
		usage, err := adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, strings.NewReader(body)), meta)
		require.NoError(t, err)
		require.NotNil(t, usage.PromptTokensDetails)
		assert.Equal(t, 3, usage.PromptTokensDetails.CachedTokens)
	})

	t.Run("input detail cache survives missing-prompt fallback and precedes nested cache", func(t *testing.T) {
		body := `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"usage":{"cached_tokens":9}}],"usage":{"prompt_tokens":0,"completion_tokens":2,"total_tokens":2,"input_tokens_details":{"cached_tokens":5}}}`
		ctx, _ := moonshotContext()
		usage, err := adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, strings.NewReader(body)), meta)
		require.NoError(t, err)
		require.NotNil(t, usage.PromptTokensDetails)
		assert.Equal(t, 5, usage.PromptTokensDetails.CachedTokens)
	})

	t.Run("missing text usage is estimated and returned to the client", func(t *testing.T) {
		body := `{"id":"chatcmpl-fallback","choices":[{"index":0,"message":{"role":"assistant","content":"four token-ish words"},"finish_reason":"stop"}]}`
		ctx, recorder := moonshotContext()
		usage, err := adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, strings.NewReader(body)), meta)
		require.NoError(t, err)
		require.NotNil(t, usage)
		assert.Equal(t, 11, usage.PromptTokens)
		assert.Greater(t, usage.CompletionTokens, 0)
		assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
		decoded := decodeBody(t, recorder.Body.Bytes())
		assert.Contains(t, decoded, "usage")
	})
}

func TestMoonshotStreamNormalizesNestedCacheUsageAndPreservesSSE(t *testing.T) {
	meta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	meta.IsStream = true
	meta.PromptTokens = 99
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	stream := strings.Join([]string{
		`data: {"id":"moonshot-stream","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop","usage":{"cached_tokens":4}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	ctx, recorder := moonshotContext()
	usage, err := adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, strings.NewReader(stream)), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 10, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	require.NotNil(t, usage.PromptTokensDetails)
	assert.Equal(t, 4, usage.PromptTokensDetails.CachedTokens)
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
}

var errInjectedMoonshotRead = errors.New("injected Moonshot read failure")

type moonshotPartialErrorReader struct {
	sent bool
}

func (r *moonshotPartialErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "{}"), nil
	}
	return 0, errInjectedMoonshotRead
}

func TestMoonshotErrorsAndResponseBounds(t *testing.T) {
	meta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	meta.Channel.StatusCodeMapping = `{"429":"503"}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	ctx, recorder := moonshotContext()
	_, err := adaptor.DoResponse(ctx, moonshotResponse(http.StatusTooManyRequests, strings.NewReader(`{"error":{"message":"busy","type":"rate_limit"}}`)), meta)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.Zero(t, recorder.Body.Len())

	ctx, recorder = moonshotContext()
	over := bytes.Repeat([]byte{' '}, int(relaycommon.MaxUpstreamJSONBodyBytes+1))
	copy(over, []byte("{}"))
	_, err = adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, bytes.NewReader(over)), meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Zero(t, recorder.Body.Len())

	ctx, recorder = moonshotContext()
	_, err = adaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, &moonshotPartialErrorReader{}), meta)
	assert.ErrorIs(t, err, errInjectedMoonshotRead)
	assert.Zero(t, recorder.Body.Len())

	streamMeta := moonshotMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI)
	streamMeta.IsStream = true
	streamAdaptor := &Adaptor{}
	streamAdaptor.Init(streamMeta)
	ctx, recorder = moonshotContext()
	oversizedEvent := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	_, err = streamAdaptor.DoResponse(ctx, moonshotResponse(http.StatusOK, strings.NewReader(oversizedEvent)), streamMeta)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum event")
}

func TestMoonshotModelListReturnsIndependentCopy(t *testing.T) {
	first := ModelList()
	second := ModelList()
	require.NotEmpty(t, first)
	first[0] = "mutated"
	assert.NotEqual(t, first[0], second[0])
}
