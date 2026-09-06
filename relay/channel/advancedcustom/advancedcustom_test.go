package advancedcustom

import (
	"bytes"
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
	advancedconfig "github.com/tokenrouter/tokenrouter/pkg/advancedcustom"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func advancedMeta(settings string) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel: &model.Channel{
			Type:           int(constant.ChannelTypeAdvancedCustom),
			OtherSettings:  settings,
			HeaderOverride: `{"*":"","X-Final":"{api_key}","X-Client":"{client_header:X-Trace}"}`,
		},
		Mode:              constant.RelayModeChatCompletions,
		Format:            constant.RelayFormatOpenAI,
		RequestPath:       "/v1/chat/completions",
		OriginalModelName: "client-model",
		ModelName:         "provider-model",
		BaseURL:           "https://gateway.example/root",
		APIKey:            "secret",
		ClientHeaders: http.Header{
			"Accept":        []string{"application/json"},
			"Authorization": []string{"Bearer client-token"},
			"Cookie":        []string{"private=1"},
			"X-Trace":       []string{"trace-42"},
		},
		Request: &protocolkit.GeneralOpenAIRequest{
			Model:    "client-model",
			Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
			Extra:    map[string]any{"model": "client-model", "provider_extension": "kept", "group": "local-only"},
		},
	}
}

func TestAdaptorRoutesByOriginalModelAndAppliesFinalHeaders(t *testing.T) {
	meta := advancedMeta(`{"advanced_custom":{"advanced_routes":[
		{"incoming_path":"/v1/chat/completions","upstream_path":"/special/{model}?existing=1","models":["client-model"],"auth":{"type":"header","name":"X-Upstream-Key","value":"prefix-{api_key}"}},
		{"incoming_path":"/v1/chat/completions","upstream_path":"/fallback"}
	]}}`)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.example/root/special/provider-model?existing=1", requestURL)

	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(nil))
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "prefix-secret", request.Header.Get("X-Upstream-Key"))
	assert.Equal(t, "secret", request.Header.Get("X-Final"))
	assert.Equal(t, "trace-42", request.Header.Get("X-Client"))
	assert.Equal(t, "trace-42", request.Header.Get("X-Trace"))
	assert.Empty(t, request.Header.Get("Authorization"), "wildcard passthrough cannot leak the client token")
	assert.Empty(t, request.Header.Get("Cookie"))
}

func TestAdaptorFailClosedWhenNoPathAndModelRouteMatches(t *testing.T) {
	meta := advancedMeta(`{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat","models":["different"]}]}}`)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	_, err := adaptor.GetRequestURL(meta)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support request path")
}

func TestAdaptorConvertsOpenAIChatToClaudeAndGemini(t *testing.T) {
	tests := []struct {
		name      string
		converter string
		path      string
		check     func(*testing.T, []byte)
	}{
		{
			name: "Claude", converter: "openai_chat_completions_to_anthropic_messages", path: "/claude/{model}",
			check: func(t *testing.T, body []byte) {
				var request protocolkit.ClaudeRequest
				require.NoError(t, protocolkit.UnmarshalJSON(body, &request))
				assert.Equal(t, "provider-model", request.Model)
				require.Len(t, request.Messages, 1)
			},
		},
		{
			name: "Gemini", converter: "openai_chat_completions_to_gemini_generate_content", path: "/models/{model}:generateContent",
			check: func(t *testing.T, body []byte) {
				var request protocolkit.GeminiChatRequest
				require.NoError(t, protocolkit.UnmarshalJSON(body, &request))
				assert.Empty(t, request.Model, "the mapped model belongs in the route URL")
				require.Len(t, request.Contents, 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := advancedMeta(`{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"` + test.path + `","converter":"` + test.converter + `"}]}}`)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			body, err := adaptor.ConvertRequest(meta)
			require.NoError(t, err)
			test.check(t, body)
		})
	}
}

func TestAdaptorRejectsCRLFHeaderOverride(t *testing.T) {
	meta := advancedMeta(`{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat"}]}}`)
	meta.Channel.HeaderOverride = `{"X-Bad":"one\r\ntwo"}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	require.NoError(t, err)
	assert.Error(t, adaptor.SetupRequestHeader(request, meta))
}

func TestAdaptorRejectsCaseInsensitiveDuplicateHeaderOverrides(t *testing.T) {
	meta := advancedMeta(`{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat"}]}}`)
	meta.Channel.HeaderOverride = `{"X-Upstream":"one","x-upstream":"two"}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	require.NoError(t, err)
	err = adaptor.SetupRequestHeader(request, meta)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "differ only by case")
}

func TestAdaptorRejectsUnboundedResolvedCredentials(t *testing.T) {
	tests := []struct {
		name     string
		settings string
	}{
		{
			name:     "default bearer",
			settings: `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat"}]}}`,
		},
		{
			name:     "header auth",
			settings: `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat","auth":{"type":"header","name":"X-Key","value":"prefix-{api_key}"}}]}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := advancedMeta(test.settings)
			meta.APIKey = strings.Repeat("k", advancedconfig.MaxAuthValueBytes+1)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			request, err := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
			require.NoError(t, err)
			assert.Error(t, adaptor.SetupRequestHeader(request, meta))
		})
	}
}

func TestChatStreamToResponsesPreservesSparseToolCallIndexes(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"id":"chat-sparse","model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":3,"id":"call-3","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
			`data: {"id":"chat-sparse","model":"provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":3,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))),
	}

	usage, err := chatStreamToResponses(context, response, &relaycommon.Meta{ModelName: "provider-model", PromptTokens: 2})
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 3, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"call_id":"call-3"`)
	assert.Contains(t, recorder.Body.String(), `"arguments":"{\"q\":\"x\"}"`)
}

func TestConvertedResponsesStreamsPreserveUsageAndTerminalEvents(t *testing.T) {
	t.Run("Responses to chat", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp-stream","model":"provider-model","status":"in_progress"}}`,
				`data: {"type":"response.output_text.delta","delta":"answer"}`,
				`data: {"type":"response.completed","response":{"id":"resp-stream","model":"provider-model","status":"completed","usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}`,
				``,
			}, "\n\n"))),
		}
		usage, err := responsesStreamToChat(context, response, &relaycommon.Meta{IsStream: true, ModelName: "provider-model"})
		require.NoError(t, err)
		require.NotNil(t, usage)
		assert.Equal(t, 7, usage.PromptTokens)
		assert.Equal(t, 2, usage.CompletionTokens)
		assert.Contains(t, recorder.Body.String(), `"content":"answer"`)
		assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
	})

	t.Run("Gemini to Responses", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(strings.Join([]string{
				`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"gemini answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":3,"totalTokenCount":9}}`,
				``,
			}, "\n\n"))),
		}
		usage, err := geminiStreamToResponses(context, response, &relaycommon.Meta{IsStream: true, ModelName: "provider-model"})
		require.NoError(t, err)
		require.NotNil(t, usage)
		assert.Equal(t, 6, usage.PromptTokens)
		assert.Equal(t, 3, usage.CompletionTokens)
		assert.Contains(t, recorder.Body.String(), `"type":"response.output_text.delta"`)
		assert.Contains(t, recorder.Body.String(), `"delta":"gemini answer"`)
		assert.Contains(t, recorder.Body.String(), `"type":"response.completed"`)
	})
}
