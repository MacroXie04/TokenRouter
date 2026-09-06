package palm

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func palmMeta() *relaycommon.Meta {
	temperature := 0.25
	topP := 0.8
	two := 2
	return &relaycommon.Meta{
		Mode: channelcatalog.RelayModeChatCompletions, Format: channelcatalog.RelayFormatOpenAI,
		OriginalModelName: "client-palm", ModelName: "PaLM-2", BaseURL: "https://palm.example/root", APIKey: "palm-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "client-palm", Temperature: &temperature, TopP: &topP, N: &two,
			Messages: []protocolkit.Message{
				{Role: "system", Content: "be concise"},
				{Role: "user", Content: []any{map[string]any{"type": "text", "text": "hello"}}},
				{Role: "assistant", Content: "hi"},
			},
			Extra: map[string]any{"top_k": float64(17)},
		},
	}
}

func TestPaLMGenerateMessageURLHeadersAndConversion(t *testing.T) {
	meta := palmMeta()
	adapter := &Adaptor{}
	adapter.Init(meta)
	requestURL, err := adapter.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://palm.example/root/v1beta2/models/chat-bison-001:generateMessage", requestURL)

	body, err := adapter.ConvertRequest(meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"prompt":{"messages":[
			{"author":"0","content":"be concise"},
			{"author":"0","content":"hello"},
			{"author":"1","content":"hi"}
		]},
		"temperature":0.25,"candidateCount":2,"topP":0.8,"topK":17
	}`, string(body))

	request := httptest.NewRequest(http.MethodPost, requestURL, nil)
	request.Header.Set("Authorization", "must-remove")
	require.NoError(t, adapter.SetupRequestHeader(request, meta))
	assert.Equal(t, "palm-secret", request.Header.Get("x-goog-api-key"))
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
}

func TestPaLMFailsClosedOnUnsupportedAndInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"embedding mode", func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeEmbeddings }},
		{"native format", func(meta *relaycommon.Meta) { meta.Format = channelcatalog.RelayFormatClaude }},
		{"empty messages", func(meta *relaycommon.Meta) { meta.Request.Messages = nil }},
		{"tool role", func(meta *relaycommon.Meta) { meta.Request.Messages[0].Role = "tool" }},
		{"image part", func(meta *relaycommon.Meta) {
			meta.Request.Messages[0].Content = []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image"}}}
		}},
		{"nan temperature", func(meta *relaycommon.Meta) { value := math.NaN(); meta.Request.Temperature = &value }},
		{"fractional top k", func(meta *relaycommon.Meta) { meta.Request.Extra["top_k"] = 1.5 }},
		{"too many candidates", func(meta *relaycommon.Meta) { value := maxCandidates + 1; meta.Request.N = &value }},
		{"tool calls", func(meta *relaycommon.Meta) { meta.Request.Tools = []protocolkit.ToolCallRequest{{}} }},
		{"credential URL", func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@palm.example" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := palmMeta()
			test.mutate(meta)
			adapter := &Adaptor{}
			adapter.Init(meta)
			if test.name == "credential URL" {
				_, err := adapter.GetRequestURL(meta)
				require.Error(t, err)
				return
			}
			_, err := adapter.ConvertRequest(meta)
			require.Error(t, err)
		})
	}

	meta := palmMeta()
	meta.APIKey = "bad\nkey"
	adapter := &Adaptor{}
	adapter.Init(meta)
	err := adapter.SetupRequestHeader(httptest.NewRequest(http.MethodPost, "https://palm.example", nil), meta)
	require.Error(t, err)
}

func TestPaLMBlockingAndStreamingResponseConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := palmMeta()
	meta.PromptTokens = 5
	adapter := &Adaptor{}
	adapter.Init(meta)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	usage, err := adapter.DoResponse(context, palmResponse(http.StatusOK,
		`{"candidates":[{"author":"1","content":"first answer"},{"author":"1","content":"second answer"}]}`), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"model":"client-palm"`)
	assert.Contains(t, recorder.Body.String(), `"content":"first answer"`)
	assert.Contains(t, recorder.Body.String(), `"content":"second answer"`)

	meta.IsStream = true
	recorder = httptest.NewRecorder()
	context, _ = gin.CreateTestContext(recorder)
	usage, err = adapter.DoResponse(context, palmResponse(http.StatusOK,
		`{"candidates":[{"author":"1","content":"streamed answer"}]}`), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), `"object":"chat.completion.chunk"`)
	assert.Contains(t, recorder.Body.String(), `"content":"streamed answer"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
}

func TestPaLMResponseErrorsAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := palmMeta()
	adapter := &Adaptor{}
	adapter.Init(meta)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())

	for _, response := range []*http.Response{
		palmResponse(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`),
		palmResponse(http.StatusOK, `not-json`),
		palmResponse(http.StatusOK, `{"candidates":[]}`),
		palmResponse(http.StatusOK, `{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`),
	} {
		_, err := adapter.DoResponse(context, response, meta)
		require.Error(t, err)
	}

	tooLarge := io.LimitReader(strings.NewReader(strings.Repeat("x", int(relaycommon.MaxUpstreamJSONBodyBytes)+1)), relaycommon.MaxUpstreamJSONBodyBytes+1)
	_, err := adapter.DoResponse(context, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(tooLarge)}, meta)
	require.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestPaLMModelCatalogIsOwned(t *testing.T) {
	models := ModelList()
	require.Equal(t, []string{"PaLM-2"}, models)
	models[0] = "changed"
	assert.Equal(t, []string{"PaLM-2"}, ModelList())
}

func palmResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
