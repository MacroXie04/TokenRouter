package ollama

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func ollamaMeta(mode constant.RelayMode) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel: &model.Channel{Type: int(constant.ChannelTypeOllama)},
		Mode:    mode, ModelName: "llama3.2:latest", BaseURL: "https://ollama.example.test/prefix/",
		APIKey: "secret-key", Request: &protocolkit.GeneralOpenAIRequest{Model: "client-model", Extra: map[string]any{}},
	}
}

func TestOllamaRequestURLsHeadersAndModeGate(t *testing.T) {
	for _, test := range []struct {
		mode constant.RelayMode
		path string
	}{
		{constant.RelayModeChatCompletions, "/prefix/api/chat"},
		{constant.RelayModeCompletions, "/prefix/api/generate"},
		{constant.RelayModeEmbeddings, "/prefix/api/embed"},
	} {
		t.Run(constant.RelayModeName(test.mode), func(t *testing.T) {
			meta := ollamaMeta(test.mode)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			parsed, err := http.NewRequest(http.MethodPost, requestURL, nil)
			require.NoError(t, err)
			assert.Equal(t, test.path, parsed.URL.Path)
			require.NoError(t, adaptor.SetupRequestHeader(parsed, meta))
			assert.Equal(t, "application/json", parsed.Header.Get("Content-Type"))
			assert.Equal(t, "application/json", parsed.Header.Get("Accept"))
			assert.Equal(t, "Bearer secret-key", parsed.Header.Get("Authorization"))
		})
	}

	meta := ollamaMeta(constant.RelayModeImagesGenerations)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	_, err := adaptor.GetRequestURL(meta)
	require.ErrorContains(t, err, "does not support")
	_, err = adaptor.ConvertRequest(meta)
	require.ErrorContains(t, err, "does not support")

	for _, invalid := range []string{
		"file:///tmp/ollama.sock", "https://user:password@example.test", "https://example.test?q=secret", "https://example.test/#fragment",
	} {
		meta := ollamaMeta(constant.RelayModeChatCompletions)
		meta.BaseURL = invalid
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		require.Error(t, err, invalid)
	}

	meta = ollamaMeta(constant.RelayModeChatCompletions)
	meta.BaseURL = ""
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, defaultBaseURL+"/api/chat", requestURL)

	meta.APIKey = ""
	req := httptest.NewRequest(http.MethodPost, requestURL, nil)
	req.Header.Set("Authorization", "must-be-removed")
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Empty(t, req.Header.Get("Authorization"))
}

func TestOllamaChatRequestConversionPreservesNativeSemantics(t *testing.T) {
	temperature := 0.25
	topP := 0.8
	frequency := 0.1
	presence := 0.2
	seed := int64(7)
	maximum := 128
	strict := true
	meta := ollamaMeta(constant.RelayModeChatCompletions)
	meta.IsStream = true
	meta.Request = &protocolkit.GeneralOpenAIRequest{
		Model: "client-model", Stream: true, Temperature: &temperature, TopP: &topP,
		FrequencyPenalty: &frequency, PresencePenalty: &presence, Seed: &seed, MaxCompletionTokens: &maximum,
		Stop: []any{"END", "STOP"}, Reasoning: &protocolkit.Reasoning{Effort: "high"},
		ResponseFormat: &protocolkit.ResponseFormat{Type: "json_schema", JsonSchema: &protocolkit.FormatJsonSchema{
			Name: "answer", Strict: &strict, Schema: map[string]any{"type": "object", "required": []any{"answer"}},
		}},
		Tools: []protocolkit.ToolCallRequest{{Type: "function", Function: &protocolkit.FunctionRequest{
			Name: "weather", Description: "get weather", Parameters: map[string]any{"type": "object"},
		}}},
		Messages: []protocolkit.Message{
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "look"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,YWJj"}},
			}},
			{Role: "assistant", Content: "checking", ReasoningContent: "thinking", ToolCalls: []protocolkit.ToolCallRequest{{
				Id: "call-weather", Type: "function", Function: &protocolkit.FunctionRequest{Name: "weather", Arguments: `{"city":"sf"}`},
			}}},
			{Role: "tool", ToolCallId: "call-weather", Content: `{"temperature":72}`},
		},
		Extra: map[string]any{"top_k": float64(40), "keep_alive": "10m", "group": "must-not-leak"},
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "llama3.2:latest", got["model"])
	assert.Equal(t, true, got["stream"])
	assert.Equal(t, "high", got["think"])
	assert.Equal(t, "10m", got["keep_alive"])
	assert.NotContains(t, got, "group")
	assert.Equal(t, map[string]any{"type": "object", "required": []any{"answer"}}, got["format"])
	options := got["options"].(map[string]any)
	assert.EqualValues(t, 0.25, options["temperature"])
	assert.EqualValues(t, 0.8, options["top_p"])
	assert.EqualValues(t, 40, options["top_k"])
	assert.EqualValues(t, 128, options["num_predict"])
	assert.Equal(t, []any{"END", "STOP"}, options["stop"])

	messages := got["messages"].([]any)
	assert.Equal(t, "look", messages[0].(map[string]any)["content"])
	assert.Equal(t, []any{"YWJj"}, messages[0].(map[string]any)["images"])
	assert.Equal(t, "thinking", messages[1].(map[string]any)["thinking"])
	toolCalls := messages[1].(map[string]any)["tool_calls"].([]any)
	assert.Equal(t, "sf", toolCalls[0].(map[string]any)["function"].(map[string]any)["arguments"].(map[string]any)["city"])
	assert.Equal(t, "weather", messages[2].(map[string]any)["tool_name"])

	tools := got["tools"].([]any)
	assert.Equal(t, "weather", tools[0].(map[string]any)["function"].(map[string]any)["name"])
}

func TestOllamaGenerateAndEmbeddingRequestConversion(t *testing.T) {
	maximum := 32
	meta := ollamaMeta(constant.RelayModeCompletions)
	meta.Request = &protocolkit.GeneralOpenAIRequest{
		Prompt: []any{"hello", " world"}, Suffix: "!", MaxTokens: &maximum,
		ResponseFormat: &protocolkit.ResponseFormat{Type: "json_object"},
		Extra:          map[string]any{"keep_alive": float64(60)},
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var generate map[string]any
	require.NoError(t, json.Unmarshal(body, &generate))
	assert.Equal(t, "hello world", generate["prompt"])
	assert.Equal(t, "!", generate["suffix"])
	assert.Equal(t, "json", generate["format"])
	assert.EqualValues(t, 32, generate["options"].(map[string]any)["num_predict"])

	meta = ollamaMeta(constant.RelayModeEmbeddings)
	meta.Request.Extra = map[string]any{
		"input": []any{"alpha", "beta"}, "dimensions": float64(256), "encoding_format": "base64", "provider_extension": "drop",
	}
	adaptor.Init(meta)
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var embedding map[string]any
	require.NoError(t, json.Unmarshal(body, &embedding))
	assert.Equal(t, []any{"alpha", "beta"}, embedding["input"])
	assert.EqualValues(t, 256, embedding["dimensions"])
	assert.EqualValues(t, 256, embedding["options"].(map[string]any)["dimensions"])
	assert.NotContains(t, embedding, "encoding_format")
	assert.NotContains(t, embedding, "provider_extension")
}

func TestOllamaRequestConversionRejectsMalformedProviderFields(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    constant.RelayMode
		request *protocolkit.GeneralOpenAIRequest
	}{
		{
			name: "remote image", mode: constant.RelayModeChatCompletions,
			request: &protocolkit.GeneralOpenAIRequest{
				Messages: []protocolkit.Message{{
					Role: "user",
					Content: []any{map[string]any{
						"type": "image_url", "image_url": map[string]any{"url": "https://metadata.invalid/image"},
					}},
				}},
				Extra: map[string]any{},
			},
		},
		{
			name: "invalid tool arguments", mode: constant.RelayModeChatCompletions,
			request: &protocolkit.GeneralOpenAIRequest{
				Messages: []protocolkit.Message{{
					Role: "assistant",
					ToolCalls: []protocolkit.ToolCallRequest{{
						Function: &protocolkit.FunctionRequest{Name: "f", Arguments: "{"},
					}},
				}},
				Extra: map[string]any{},
			},
		},
		{name: "fractional top k", mode: constant.RelayModeChatCompletions, request: &protocolkit.GeneralOpenAIRequest{Extra: map[string]any{"top_k": 1.5}}},
		{name: "bad reasoning", mode: constant.RelayModeChatCompletions, request: &protocolkit.GeneralOpenAIRequest{ReasoningEffort: "extreme", Extra: map[string]any{}}},
		{name: "numeric embedding", mode: constant.RelayModeEmbeddings, request: &protocolkit.GeneralOpenAIRequest{Extra: map[string]any{"input": []any{"ok", float64(1)}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := ollamaMeta(test.mode)
			meta.Request = test.request
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			_, err := adaptor.ConvertRequest(meta)
			require.Error(t, err)
		})
	}
}

func testContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	return context, recorder
}

func testResponse(body io.Reader) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}
}

func TestOllamaNonStreamChatGenerateAndEmbeddingNormalization(t *testing.T) {
	chatBody := strings.Join([]string{
		`{"model":"llama3.2:latest","created_at":"2026-01-02T03:04:05Z","message":{"role":"assistant","thinking":"plan ","content":"hello ","tool_calls":[{"function":{"name":"weather","arguments":{"city":"sf"}}}]},"done":false}`,
		`{"model":"llama3.2:latest","created_at":"2026-01-02T03:04:05Z","message":{"role":"assistant","thinking":"done","content":"world"},"done":true,"done_reason":"stop","prompt_eval_count":4,"eval_count":6}`,
	}, "\n")
	meta := ollamaMeta(constant.RelayModeChatCompletions)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, recorder := testContext()
	usage, err := adaptor.DoResponse(context, testResponse(strings.NewReader(chatBody)), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10}, usage)
	var chat map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &chat))
	assert.Equal(t, "chat.completion", chat["object"])
	assert.EqualValues(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix(), chat["created"])
	choice := chat["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "tool_calls", choice["finish_reason"])
	message := choice["message"].(map[string]any)
	assert.Equal(t, "hello world", message["content"])
	assert.Equal(t, "plan done", message["reasoning_content"])
	call := message["tool_calls"].([]any)[0].(map[string]any)
	assert.Equal(t, "call_0", call["id"])
	assert.JSONEq(t, `{"city":"sf"}`, call["function"].(map[string]any)["arguments"].(string))

	meta = ollamaMeta(constant.RelayModeCompletions)
	adaptor.Init(meta)
	context, recorder = testContext()
	usage, err = adaptor.DoResponse(context, testResponse(strings.NewReader(
		`{"model":"qwen","response":"answer","done":true,"done_reason":"length","prompt_eval_count":2,"eval_count":3}`,
	)), meta)
	require.NoError(t, err)
	assert.Equal(t, 5, usage.TotalTokens)
	var completion map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &completion))
	assert.Equal(t, "text_completion", completion["object"])
	assert.Equal(t, "answer", completion["choices"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "length", completion["choices"].([]any)[0].(map[string]any)["finish_reason"])

	meta = ollamaMeta(constant.RelayModeEmbeddings)
	meta.Request.Extra = map[string]any{"input": []any{"alpha", "beta"}}
	adaptor.Init(meta)
	context, recorder = testContext()
	usage, err = adaptor.DoResponse(context, testResponse(strings.NewReader(
		`{"model":"nomic-embed","embeddings":[[0.1,0.2],[0.3,0.4]],"prompt_eval_count":7}`,
	)), meta)
	require.NoError(t, err)
	assert.Equal(t, 7, usage.PromptTokens)
	var embedding map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &embedding))
	assert.Equal(t, "list", embedding["object"])
	assert.Len(t, embedding["data"], 2)
	assert.Equal(t, "embedding", embedding["data"].([]any)[0].(map[string]any)["object"])
}

func TestOllamaNDJSONStreamNormalizationIncludesToolsReasoningUsageAndDone(t *testing.T) {
	stream := strings.Join([]string{
		`{"model":"llama-stream","created_at":"2026-01-02T03:04:05Z","message":{"role":"assistant","thinking":"reason","content":"hello"},"done":false}`,
		`{"model":"llama-stream","created_at":"2026-01-02T03:04:06Z","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"weather","arguments":{"city":"sf"}}}]},"done":false}`,
		`{"model":"llama-stream","created_at":"2026-01-02T03:04:07Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":3}`,
	}, "\n")
	meta := ollamaMeta(constant.RelayModeChatCompletions)
	meta.IsStream = true
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, recorder := testContext()
	usage, err := adaptor.DoResponse(context, testResponse(strings.NewReader(stream)), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}, usage)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	body := recorder.Body.String()
	assert.Contains(t, body, `"role":"assistant"`)
	assert.Contains(t, body, `"content":"hello"`)
	assert.Contains(t, body, `"reasoning_content":"reason"`)
	assert.Contains(t, body, `"finish_reason":"tool_calls"`)
	assert.Contains(t, body, `"prompt_tokens":5`)
	assert.Contains(t, body, `"completion_tokens":3`)
	assert.Contains(t, body, `"index":0`)
	assert.Contains(t, body, "data: [DONE]\n\n")
}

func TestOllamaResponsesFailClosedAtBoundsAndMalformedUsage(t *testing.T) {
	tests := []struct {
		name   string
		mode   constant.RelayMode
		stream bool
		body   io.Reader
		want   string
	}{
		{name: "malformed JSON", mode: constant.RelayModeChatCompletions, body: strings.NewReader(`{"done":`), want: "decode Ollama response"},
		{name: "missing terminal", mode: constant.RelayModeChatCompletions, body: strings.NewReader(`{"message":{"content":"x"},"done":false}`), want: "missing a terminal"},
		{name: "negative usage", mode: constant.RelayModeChatCompletions, body: strings.NewReader(`{"done":true,"prompt_eval_count":-1,"eval_count":2}`), want: "invalid usage"},
		{name: "usage overflow", mode: constant.RelayModeChatCompletions, body: strings.NewReader(`{"done":true,"prompt_eval_count":` + strconv.Itoa(math.MaxInt) + `,"eval_count":1}`), want: "invalid usage"},
		{name: "data after terminal", mode: constant.RelayModeChatCompletions, body: strings.NewReader("{\"done\":true}\n{\"response\":\"late\",\"done\":false}"), want: "after its terminal"},
		{name: "oversized buffered", mode: constant.RelayModeChatCompletions, body: io.LimitReader(zeroReader{}, relaycommon.MaxUpstreamJSONBodyBytes+1), want: "exceeds limit"},
		{name: "malformed first stream", mode: constant.RelayModeChatCompletions, stream: true, body: strings.NewReader("not-json\n"), want: "decode Ollama stream"},
		{name: "negative first stream usage", mode: constant.RelayModeChatCompletions, stream: true, body: strings.NewReader(`{"done":true,"prompt_eval_count":-1,"eval_count":2}`), want: "invalid usage"},
		{name: "oversized stream line", mode: constant.RelayModeChatCompletions, stream: true, body: io.LimitReader(zeroReader{}, int64(relaycommon.MaxUpstreamSSEEventBytes+2)), want: "maximum line"},
		{name: "empty embedding response", mode: constant.RelayModeEmbeddings, body: strings.NewReader(`{"embeddings":[]}`), want: "no vectors"},
		{name: "ragged embedding response", mode: constant.RelayModeEmbeddings, body: strings.NewReader(`{"embeddings":[[0.1],[0.2,0.3]]}`), want: "inconsistent vector dimensions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := ollamaMeta(test.mode)
			meta.IsStream = test.stream
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			context, recorder := testContext()
			usage, err := adaptor.DoResponse(context, testResponse(test.body), meta)
			assert.Nil(t, usage)
			require.ErrorContains(t, err, test.want)
			assert.False(t, context.Writer.Written(), recorder.Body.String())
		})
	}

	meta := ollamaMeta(constant.RelayModeChatCompletions)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, _ := testContext()
	_, err := adaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"denied"}`)),
	}, meta)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusUnauthorized, upstream.StatusCode)

	context, recorder := testContext()
	_, err = adaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://redirect.invalid/"}},
		Body: io.NopCloser(strings.NewReader("redirect refused")),
	}, meta)
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusTemporaryRedirect, upstream.StatusCode)
	assert.False(t, context.Writer.Written(), recorder.Body.String())
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}
