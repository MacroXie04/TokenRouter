package relay

import (
	contextpkg "context"
	"encoding/json"
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
)

type volcEngineClaudeRoundTripFunc func(*http.Request) (*http.Response, error)

func (function volcEngineClaudeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func volcEngineClaudeContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *RelayInfo) {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	request := &protocolkit.ClaudeRequest{
		Model: "client-model", MaxTokens: 32,
		Messages: []protocolkit.ClaudeMessage{{Role: "user", Content: "hello"}},
	}
	return context, recorder, &RelayInfo{
		Mode: constant.RelayModeChatCompletions, Format: constant.RelayFormatClaude,
		ModelName: "client-model", ClaudeRequest: request,
		Request: protocolkit.ClaudeRequestToOpenAIRequest(request),
		Channel: &model.Channel{
			Type:         int(constant.ChannelTypeVolcEngine),
			ModelMapping: `{"client-model":"upstream-model"}`,
		},
	}
}

func TestVolcEngineClaudeStandardBaseUsesOpenAIConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, recorder, info := volcEngineClaudeContext(t)
	client := &http.Client{Transport: volcEngineClaudeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "https://volc.example.test/api/v3/chat/completions", request.URL.String())
		assert.Equal(t, "Bearer upstream-secret", request.Header.Get("Authorization"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var converted map[string]any
		require.NoError(t, json.Unmarshal(body, &converted))
		assert.Equal(t, "upstream-model", converted["model"])
		assert.Contains(t, converted, "messages")
		assert.NotContains(t, converted, "max_tokens_to_sample")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl-volc","object":"chat.completion","model":"upstream-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"converted"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
			}`)),
			Request: request,
		}, nil
	})}
	previousClient := relayHTTPClient
	relayHTTPClient = client
	t.Cleanup(func() { relayHTTPClient = previousClient })

	usage, err := sendClaudeViaVolcEngine(context, info, "https://volc.example.test", "upstream-secret")
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"type":"message"`)
	assert.Contains(t, recorder.Body.String(), `"text":"converted"`)
}

func TestVolcEngineClaudeCodingPlanPreservesNativeMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, recorder, info := volcEngineClaudeContext(t)
	client := &http.Client{Transport: volcEngineClaudeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "https://ark.cn-beijing.volces.com/api/coding/v1/messages", request.URL.String())
		assert.Equal(t, "Bearer upstream-secret", request.Header.Get("Authorization"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var native protocolkit.ClaudeRequest
		require.NoError(t, json.Unmarshal(body, &native))
		assert.Equal(t, "upstream-model", native.Model)
		assert.Equal(t, 32, native.MaxTokens)
		require.Len(t, native.Messages, 1)
		assert.Equal(t, "hello", native.Messages[0].Content)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"msg_volc","type":"message","role":"assistant","model":"upstream-model",
				"content":[{"type":"text","text":"native"}],"stop_reason":"end_turn",
				"usage":{"input_tokens":4,"output_tokens":1}
			}`)),
			Request: request,
		}, nil
	})}
	previousClient := relayHTTPClient
	relayHTTPClient = client
	t.Cleanup(func() { relayHTTPClient = previousClient })

	usage, err := sendClaudeViaVolcEngine(context, info, "doubao-coding-plan", "upstream-secret")
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.TotalTokens)
	assert.JSONEq(t, `{
		"id":"msg_volc","type":"message","role":"assistant","model":"upstream-model",
		"content":[{"type":"text","text":"native"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":4,"output_tokens":1}
	}`, recorder.Body.String())
}

func TestVolcEngineClaudeHelperHonorsCanceledContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _, info := volcEngineClaudeContext(t)
	canceled, cancel := contextpkg.WithCancel(context.Request.Context())
	cancel()
	context.Request = context.Request.WithContext(canceled)

	previousClient := relayHTTPClient
	relayHTTPClient = &http.Client{Transport: volcEngineClaudeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})}
	t.Cleanup(func() { relayHTTPClient = previousClient })

	_, err := sendClaudeViaVolcEngine(context, info, "https://volc.example.test", "upstream-secret")
	require.Error(t, err)
}
