package zhipu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
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
	"testing"
	"time"
)

func zhipuMeta(channelType channelcatalog.ChannelType, mode channelcatalog.RelayMode, format channelcatalog.RelayFormat, modelName string) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel: &model.Channel{Type: int(channelType)}, Mode: mode, Format: format,
		OriginalModelName: "client-model", ModelName: modelName,
		BaseURL: "https://zhipu.example/gateway", APIKey: "provider-secret",
		RequestContentType: "application/json", PromptTokens: 5,
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "client-model", Extra: map[string]any{"model": "client-model"},
		},
	}
}

func zhipuContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return context, recorder
}

func zhipuResponse(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(body)}
}

func decodeZhipuBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	return decoded
}

func TestZhipuPersistedCatalogURLsAndAuthentication(t *testing.T) {
	legacyExpected := []string{"chatglm_turbo", "chatglm_pro", "chatglm_std", "chatglm_lite"}
	v4Expected := []string{
		"glm-4", "glm-4v", "glm-3-turbo", "glm-4-alltools", "glm-4-plus", "glm-4-0520", "glm-4-air",
		"glm-4-airx", "glm-4-long", "glm-4-flash", "glm-4v-plus", "glm-4.6", "glm-4.6v", "glm-4.7",
		"glm-4.7-flash", "glm-5",
	}
	assert.Equal(t, legacyExpected, LegacyModelList())
	assert.Equal(t, v4Expected, V4ModelList())
	legacyCopy := LegacyModelList()
	legacyCopy[0] = "mutated"
	assert.Equal(t, "chatglm_turbo", LegacyModelList()[0])
	v4Copy := V4ModelList()
	v4Copy[0] = "mutated"
	assert.Equal(t, "glm-4", V4ModelList()[0])

	legacyURL, err := LegacyRequestURL("https://zhipu.example/gateway/", "chatglm/pro", false)
	require.NoError(t, err)
	assert.Equal(t, "https://zhipu.example/gateway/api/paas/v3/model-api/chatglm%2Fpro/invoke", legacyURL)
	legacyStreamURL, err := LegacyRequestURL("https://zhipu.example/gateway", "chatglm_pro", true)
	require.NoError(t, err)
	assert.Equal(t, "https://zhipu.example/gateway/api/paas/v3/model-api/chatglm_pro/sse-invoke", legacyStreamURL)

	for _, test := range []struct {
		name, base, expected string
		mode                 channelcatalog.RelayMode
		format               channelcatalog.RelayFormat
	}{
		{name: "chat", base: "https://zhipu.example/gateway", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatOpenAI, expected: "https://zhipu.example/gateway/api/paas/v4/chat/completions"},
		{name: "embedding", base: "https://zhipu.example", mode: channelcatalog.RelayModeEmbeddings, format: channelcatalog.RelayFormatEmbedding, expected: "https://zhipu.example/api/paas/v4/embeddings"},
		{name: "image", base: "https://zhipu.example", mode: channelcatalog.RelayModeImagesGenerations, format: channelcatalog.RelayFormatOpenAIImage, expected: "https://zhipu.example/api/paas/v4/images/generations"},
		{name: "Claude", base: "https://zhipu.example", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatClaude, expected: "https://zhipu.example/api/anthropic/v1/messages"},
		{name: "GLM coding OpenAI", base: "glm-coding-plan", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatOpenAI, expected: "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"},
		{name: "GLM coding Claude", base: "glm-coding-plan", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatClaude, expected: "https://open.bigmodel.cn/api/anthropic/v1/messages"},
		{name: "international embedding", base: "glm-coding-plan-international", mode: channelcatalog.RelayModeEmbeddings, format: channelcatalog.RelayFormatEmbedding, expected: "https://api.z.ai/api/coding/paas/v4/embeddings"},
		{name: "Kimi Claude", base: "kimi-coding-plan", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatClaude, expected: "https://api.kimi.com/coding/v1/messages"},
		{name: "Doubao image", base: "doubao-coding-plan", mode: channelcatalog.RelayModeImagesGenerations, format: channelcatalog.RelayFormatOpenAIImage, expected: "https://ark.cn-beijing.volces.com/api/coding/v3/images/generations"},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := V4RequestURL(test.base, test.mode, test.format)
			require.NoError(t, err)
			assert.Equal(t, test.expected, actual)
		})
	}

	tokenString, err := LegacyAuthorization("provider-id.provider-secret")
	require.NoError(t, err)
	parsed, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		assert.Equal(t, jwt.SigningMethodHS256.Alg(), token.Method.Alg())
		assert.Equal(t, "SIGN", token.Header["sign_type"])
		return []byte("provider-secret"), nil
	}, jwt.WithoutClaimsValidation())
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	claims := parsed.Claims.(jwt.MapClaims)
	assert.Equal(t, "provider-id", claims["api_key"])
	timestamp := claims["timestamp"].(float64)
	expiry := claims["exp"].(float64)
	assert.InDelta(t, 24*time.Hour/time.Millisecond, expiry-timestamp, 1)

	legacy := zhipuMeta(channelcatalog.ChannelTypeZhipu, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "chatglm_pro")
	legacy.APIKey = "provider-id.provider-secret"
	legacyAdaptor := &LegacyAdaptor{}
	legacyAdaptor.Init(legacy)
	request := httptest.NewRequest(http.MethodPost, "https://zhipu.example", nil)
	require.NoError(t, legacyAdaptor.SetupRequestHeader(request, legacy))
	assert.NotEmpty(t, request.Header.Get("Authorization"))
	assert.NotContains(t, request.Header.Get("Authorization"), "Bearer ")
	assert.Equal(t, "application/json", request.Header.Get("Accept"))

	v4 := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "glm-4-plus")
	v4Adaptor := &V4Adaptor{}
	v4Adaptor.Init(v4)
	request = httptest.NewRequest(http.MethodPost, "https://zhipu.example", nil)
	require.NoError(t, v4Adaptor.SetupRequestHeader(request, v4))
	assert.Equal(t, "Bearer provider-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))

	for _, invalid := range []string{"", "one-part", "a.b.c", strings.Repeat("x", maxCredentialSize+1)} {
		_, err := LegacyAuthorization(invalid)
		require.Error(t, err)
	}
	v4.APIKey = "bad\r\ncredential"
	require.Error(t, v4Adaptor.SetupRequestHeader(request, v4))
	_, err = LegacyRequestURL("file:///tmp/provider", "chatglm_pro", false)
	require.ErrorContains(t, err, "http")
	_, err = V4RequestURL("https://user:secret@zhipu.example", channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	require.ErrorContains(t, err, "credentials")
}

func TestZhipuLegacyRequestResponseAndStreamConversion(t *testing.T) {
	topP := 1.0
	temperature := 0.25
	meta := zhipuMeta(channelcatalog.ChannelTypeZhipu, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "chatglm_pro")
	meta.APIKey = "provider-id.provider-secret"
	meta.Request.Messages = []protocolkit.Message{
		{Role: "system", Content: "follow policy"},
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "hello"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://image.example"}},
			map[string]any{"type": "text", "text": " world"},
		}},
	}
	meta.Request.TopP = &topP
	meta.Request.Temperature = &temperature
	adaptor := &LegacyAdaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	converted := decodeZhipuBody(t, body)
	assert.EqualValues(t, 0.99, converted["top_p"])
	assert.EqualValues(t, 0.25, converted["temperature"])
	assert.NotContains(t, converted, "model")
	assert.NotContains(t, converted, "incremental")
	prompt := converted["prompt"].([]any)
	require.Len(t, prompt, 3)
	assert.Equal(t, "system", prompt[0].(map[string]any)["role"])
	assert.Equal(t, "Okay", prompt[1].(map[string]any)["content"])
	assert.Equal(t, "hello world", prompt[2].(map[string]any)["content"])

	context, recorder := zhipuContext()
	usage, err := adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		`{"code":200,"msg":"ok","success":true,"data":{"task_id":"task-1","request_id":"request-1","task_status":"SUCCESS","choices":[{"role":"assistant","content":"\"first\""},{"role":"assistant","content":"second"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}}`,
	)), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	var output protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, "task-1", output.Id)
	require.Len(t, output.Choices, 2)
	assert.Equal(t, "first", output.Choices[0].Message.Content)
	assert.Empty(t, output.Choices[0].FinishReason)
	assert.Equal(t, "stop", output.Choices[1].FinishReason)

	meta.IsStream = true
	meta.Request.Stream = true
	adaptor.Init(meta)
	context, recorder = zhipuContext()
	usage, err = adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		"event:add\ndata:hello\ndata: world\nmeta:{\"request_id\":\"request-stream\",\"task_id\":\"task-stream\",\"task_status\":\"SUCCESS\",\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n",
	)), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 8, usage.PromptTokens)
	assert.Equal(t, 4, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Contains(t, recorder.Body.String(), `"content":" world"`)
	assert.Contains(t, recorder.Body.String(), `"id":"request-stream"`)
	assert.True(t, strings.HasSuffix(recorder.Body.String(), "data: [DONE]\n\n"))
}

func TestZhipuV4RequestConversionAndFailClosedModes(t *testing.T) {
	topP := 1.0
	maxTokens := 17
	maxCompletionTokens := 9
	meta := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "glm-4v-plus")
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.TopP = &topP
	meta.Request.MaxTokens = &maxTokens
	meta.Request.MaxCompletionTokens = &maxCompletionTokens
	meta.Request.Stop = "END"
	meta.Request.Messages = []protocolkit.Message{{
		Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "describe"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aW1hZ2U=", "detail": "high"}},
		},
	}}
	meta.Request.Extra = map[string]any{
		"model": "client-model", "stream": true, "thinking": map[string]any{"type": "enabled"},
		"provider_extension": "drop", "group": "dashboard-only",
	}
	adaptor := &V4Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	converted := decodeZhipuBody(t, body)
	assert.Equal(t, "glm-4v-plus", converted["model"])
	assert.EqualValues(t, 0.99, converted["top_p"])
	assert.EqualValues(t, 9, converted["max_tokens"])
	assert.Equal(t, []any{"END"}, converted["stop"])
	assert.Equal(t, true, converted["stream_options"].(map[string]any)["include_usage"])
	assert.Equal(t, "enabled", converted["thinking"].(map[string]any)["type"])
	assert.NotContains(t, converted, "provider_extension")
	assert.NotContains(t, converted, "group")
	imagePart := converted["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)
	assert.Equal(t, "aW1hZ2U=", imagePart["image_url"].(map[string]any)["url"])

	embedding := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding, "embedding-3")
	embedding.Request.Extra = map[string]any{
		"model": "client-model", "input": []any{"one", "two"}, "encoding_format": "float", "dimensions": 128.0,
		"provider_extension": "drop", "group": "drop",
	}
	adaptor.Init(embedding)
	body, err = adaptor.ConvertRequest(embedding)
	require.NoError(t, err)
	converted = decodeZhipuBody(t, body)
	assert.Equal(t, "embedding-3", converted["model"])
	assert.Equal(t, []any{"one", "two"}, converted["input"])
	assert.EqualValues(t, 128, converted["dimensions"])
	assert.NotContains(t, converted, "provider_extension")
	assert.NotContains(t, converted, "group")

	n := 2
	image := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "cogview-4")
	image.Request.N = &n
	image.Request.Prompt = "draw a cat"
	image.Request.Extra = map[string]any{
		"model": "client-model", "prompt": "draw a cat", "n": 2.0, "size": "1024x1024",
		"watermark_enabled": false, "user_id": "user-1", "unknown": "drop",
	}
	adaptor.Init(image)
	body, err = adaptor.ConvertRequest(image)
	require.NoError(t, err)
	converted = decodeZhipuBody(t, body)
	assert.Equal(t, "cogview-4", converted["model"])
	assert.Equal(t, "draw a cat", converted["prompt"])
	assert.Equal(t, false, converted["watermark_enabled"])
	assert.Equal(t, "user-1", converted["user_id"])
	assert.NotContains(t, converted, "unknown")

	claude := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatClaude, "glm-4.7")
	claude.RawBody = []byte(`{"model":"client-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}],"stream":false,"group":"dashboard-only","output_config":{"effort":"high"}}`)
	adaptor.Init(claude)
	body, err = adaptor.ConvertRequest(claude)
	require.NoError(t, err)
	converted = decodeZhipuBody(t, body)
	assert.Equal(t, "glm-4.7", converted["model"])
	assert.Equal(t, "high", converted["output_config"].(map[string]any)["effort"])
	assert.NotContains(t, converted, "group")

	invalid := []*relaycommon.Meta{
		zhipuMeta(channelcatalog.ChannelTypeZhipu, channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding, "chatglm_pro"),
		zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI, "glm-4"),
		zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeGemini, channelcatalog.RelayFormatGemini, "glm-4"),
		zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeImagesEdits, channelcatalog.RelayFormatOpenAIImage, "cogview-4"),
		zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses, "glm-4"),
	}
	for _, candidate := range invalid {
		if candidate.Channel.Type == int(channelcatalog.ChannelTypeZhipu) {
			legacyAdaptor := &LegacyAdaptor{}
			legacyAdaptor.Init(candidate)
			_, err = legacyAdaptor.GetRequestURL(candidate)
		} else {
			adaptor.Init(candidate)
			_, err = adaptor.GetRequestURL(candidate)
		}
		require.Error(t, err)
	}
	image.RequestContentType = "multipart/form-data; boundary=test"
	adaptor.Init(image)
	_, err = adaptor.ConvertRequest(image)
	require.ErrorContains(t, err, "application/json")
	image.RequestContentType = "application/json"
	image.Request.Extra["n"] = 129.0
	adaptor.Init(image)
	_, err = adaptor.ConvertRequest(image)
	require.ErrorContains(t, err, "integer")
}

func TestZhipuResponseUsageErrorsAndResourceBounds(t *testing.T) {
	v4 := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "glm-4-plus")
	adaptor := &V4Adaptor{}
	adaptor.Init(v4)
	context, recorder := zhipuContext()
	usage, err := adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		`{"id":"chat-v4","object":"chat.completion","model":"glm-4-plus","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`,
	)), v4)
	require.NoError(t, err)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Contains(t, recorder.Body.String(), `"id":"chat-v4"`)

	v4.Channel.StatusCodeMapping = `{"429":"503"}`
	context, _ = zhipuContext()
	_, err = adaptor.DoResponse(context, zhipuResponse(http.StatusTooManyRequests, strings.NewReader(
		`{"error":{"message":"limited","type":"rate_limit","code":"quota"}}`,
	)), v4)
	var upstreamError *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstreamError)
	assert.Equal(t, http.StatusServiceUnavailable, upstreamError.StatusCode)
	assert.NotContains(t, err.Error(), "limited")

	legacy := zhipuMeta(channelcatalog.ChannelTypeZhipu, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "chatglm_pro")
	legacy.APIKey = "provider-id.provider-secret"
	legacy.Channel.StatusCodeMapping = `{"400":503}`
	legacyAdaptor := &LegacyAdaptor{}
	legacyAdaptor.Init(legacy)
	context, _ = zhipuContext()
	_, err = legacyAdaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		`{"code":1001,"msg":"provider rejected","success":false,"request_id":"request-error"}`,
	)), legacy)
	require.ErrorAs(t, err, &upstreamError)
	assert.Equal(t, http.StatusServiceUnavailable, upstreamError.StatusCode)

	context, _ = zhipuContext()
	_, err = legacyAdaptor.DoResponse(context, zhipuResponse(http.StatusBadRequest, strings.NewReader(
		strings.Repeat("private-upstream", int(relaycommon.MaxUpstreamErrorBodyBytes)),
	)), legacy)
	require.Error(t, err)
	assert.True(t, errors.Is(err, relaycommon.ErrUpstreamResponseTooLarge), err)
	assert.NotContains(t, err.Error(), "private-upstream")

	legacy.IsStream = true
	legacy.Request.Stream = true
	legacyAdaptor.Init(legacy)
	context, _ = zhipuContext()
	_, err = legacyAdaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		"data:"+strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1)+"\n",
	)), legacy)
	require.ErrorContains(t, err, "maximum event")

	claude := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatClaude, "glm-4.7")
	claude.RawBody = []byte(`{"model":"glm-4.7","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`)
	adaptor.Init(claude)
	context, recorder = zhipuContext()
	usage, err = adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		`{"id":"message-1","type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","model":"glm-4.7","usage":{"input_tokens":6,"output_tokens":4}}`,
	)), claude)
	require.NoError(t, err)
	assert.Equal(t, 6, usage.PromptTokens)
	assert.Equal(t, 4, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"type":"message"`)

	claude.IsStream = true
	adaptor.Init(claude)
	context, recorder = zhipuContext()
	usage, err = adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)), claude)
	require.NoError(t, err)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), "event: message_start")
	assert.Contains(t, recorder.Body.String(), `"text":"hello"`)

	claude.Channel.StatusCodeMapping = `{"502":503}`
	adaptor.Init(claude)
	context, _ = zhipuContext()
	_, err = adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n",
	)), claude)
	require.ErrorAs(t, err, &upstreamError)
	assert.Equal(t, http.StatusServiceUnavailable, upstreamError.StatusCode)

	claude.IsStream = false
	adaptor.Init(claude)
	context, _ = zhipuContext()
	_, err = adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		strings.Repeat("x", int(relaycommon.MaxUpstreamJSONBodyBytes)+1),
	)), claude)
	require.Error(t, err)
	assert.True(t, errors.Is(err, relaycommon.ErrUpstreamResponseTooLarge), err)
}

type zhipuRoundTripFunc func(*http.Request) (*http.Response, error)

func (function zhipuRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestZhipuV4ImageNormalizationUsesBoundedCredentialFreeFetch(t *testing.T) {
	meta := zhipuMeta(channelcatalog.ChannelTypeZhipuV4, channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "cogview-4")
	meta.PromptTokens = 4
	n := 2
	meta.Request.N = &n
	meta.Request.Prompt = "draw"
	meta.Request.Extra = map[string]any{"model": "client-model", "prompt": "draw", "n": 2.0}
	adaptor := &V4Adaptor{auxClient: &http.Client{Transport: zhipuRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://cdn.example/image.png", request.URL.String())
		assert.Empty(t, request.Header.Get("Authorization"))
		return zhipuResponse(http.StatusOK, strings.NewReader("image-bytes")), nil
	})}}
	adaptor.Init(meta)
	context, recorder := zhipuContext()
	usage, err := adaptor.DoResponse(context, zhipuResponse(http.StatusOK, strings.NewReader(
		`{"created":123,"data":[{"url":"https://cdn.example/image.png"},{"b64_image":"aW5saW5l"}]}`,
	)), meta)
	require.NoError(t, err)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	var output openAIImageResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.EqualValues(t, 123, output.Created)
	require.Len(t, output.Data, 2)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("image-bytes")), output.Data[0].B64JSON)
	assert.Equal(t, "aW5saW5l", output.Data[1].B64JSON)

	context, _ = zhipuContext()
	_, err = adaptor.DoResponse(context, zhipuResponse(http.StatusOK, bytes.NewBufferString(
		`{"error":{"code":"content_filter","message":"blocked"},"request_id":"image-request"}`,
	)), meta)
	require.Error(t, err)
	var upstreamError *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstreamError)
	assert.Equal(t, http.StatusBadRequest, upstreamError.StatusCode)
}
