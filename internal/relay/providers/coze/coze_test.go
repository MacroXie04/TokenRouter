package coze

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func cozeMeta() *relaycommon.Meta {
	return &relaycommon.Meta{
		Context: context.Background(),
		Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeCoze), Other: "bot_123"},
		Mode:    channelcatalog.RelayModeChatCompletions, Format: channelcatalog.RelayFormatOpenAI,
		OriginalModelName: "client-model", ModelName: "mapped-model",
		BaseURL: "https://api.coze.test/gateway", APIKey: "coze-secret",
		RequestContentType: "application/json",
		RawBody:            []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}]}`),
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "client-model", Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
		PromptTokens: 4,
	}
}

func cozeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCozeCatalogURLHeadersAndSupportedSurface(t *testing.T) {
	wantModels := []string{
		"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k", "Baichuan4",
		"abab6.5s-chat-pro", "glm-4-0520", "qwen-max", "deepseek-r1", "deepseek-v3",
		"deepseek-r1-distill-qwen-32b", "deepseek-r1-distill-qwen-7b", "step-1v-8k",
		"step-1.5v-mini", "Doubao-pro-32k", "Doubao-pro-256k", "Doubao-lite-128k",
		"Doubao-lite-32k", "Doubao-vision-lite-32k", "Doubao-vision-pro-32k",
		"Doubao-1.5-pro-vision-32k", "Doubao-1.5-lite-32k", "Doubao-1.5-pro-32k",
		"Doubao-1.5-thinking-pro", "Doubao-1.5-pro-256k",
	}
	assert.Equal(t, wantModels, ModelList())
	mutated := ModelList()
	mutated[0] = "changed"
	assert.Equal(t, wantModels, ModelList())

	meta := cozeMeta()
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://api.coze.test/gateway/v3/chat", requestURL)

	request := httptest.NewRequest(http.MethodPost, requestURL, nil)
	request.Header.Set("api-key", "client-value")
	request.Header.Set("x-api-key", "client-value")
	request.Header.Set("x-goog-api-key", "client-value")
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "Bearer coze-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Empty(t, request.Header.Get("api-key"))
	assert.Empty(t, request.Header.Get("x-api-key"))
	assert.Empty(t, request.Header.Get("x-goog-api-key"))
	meta.IsStream = true
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))

	meta.BaseURL = ""
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://api.coze.cn/v3/chat", requestURL)

	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"completion mode", func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeCompletions }},
		{"embedding mode", func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeEmbeddings }},
		{"image mode", func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeImagesGenerations }},
		{"responses mode", func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeResponses }},
		{"Claude format", func(meta *relaycommon.Meta) { meta.Format = channelcatalog.RelayFormatClaude }},
		{"missing key", func(meta *relaycommon.Meta) { meta.APIKey = "" }},
		{"header key", func(meta *relaycommon.Meta) { meta.APIKey = "bad\r\nkey" }},
		{"missing bot", func(meta *relaycommon.Meta) { meta.Channel.Other = "" }},
		{"invalid bot", func(meta *relaycommon.Meta) { meta.Channel.Other = "bot id" }},
		{"missing model", func(meta *relaycommon.Meta) { meta.ModelName = "" }},
		{"base credentials", func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@coze.test" }},
		{"base query", func(meta *relaycommon.Meta) { meta.BaseURL = "https://coze.test?token=value" }},
		{"base fragment", func(meta *relaycommon.Meta) { meta.BaseURL = "https://coze.test/#fragment" }},
		{"base scheme", func(meta *relaycommon.Meta) { meta.BaseURL = "file:///tmp/coze" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := cozeMeta()
			test.mutate(invalid)
			candidate := &Adaptor{}
			candidate.Init(invalid)
			_, err := candidate.GetRequestURL(invalid)
			assert.Error(t, err)
		})
	}
}

func TestCozeRequestConversionMatchesBotContract(t *testing.T) {
	meta := cozeMeta()
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.User = "stable-user"
	meta.Request.Messages = []protocolkit.Message{
		{Role: "system", Content: "bot owns this"},
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "hello "},
			protocolkit.MediaContent{Type: protocolkit.ContentTypeText, Text: "world"},
		}},
		{Role: "assistant", Content: "ignored history"},
		{Role: "user", Content: "again"},
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var request chatRequest
	require.NoError(t, json.Unmarshal(body, &request))
	assert.Equal(t, "bot_123", request.BotID)
	assert.Equal(t, "stable-user", request.UserID)
	assert.True(t, request.Stream)
	assert.Equal(t, []enterMessage{
		{Role: "user", Content: "hello world", ContentType: "text"},
		{Role: "user", Content: "again", ContentType: "text"},
	}, request.AdditionalMessages)
	assert.NotContains(t, string(body), "mapped-model")
	assert.NotContains(t, string(body), "bot owns this")

	meta = cozeMeta()
	meta.Request.User = ""
	adaptor.Init(meta)
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &request))
	assert.Regexp(t, `^tokenrouter-[0-9a-f]{32}$`, request.UserID)
}

func TestCozeRequestRejectsUnsupportedAndUnboundedInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"wrong content type", func(meta *relaycommon.Meta) { meta.RequestContentType = "text/plain" }},
		{"duplicate JSON", func(meta *relaycommon.Meta) { meta.RawBody = []byte(`{"model":"a","model":"b"}`) }},
		{"oversized body", func(meta *relaycommon.Meta) { meta.RawBody = bytes.Repeat([]byte{'x'}, maxRequestBodyBytes+1) }},
		{"tools", func(meta *relaycommon.Meta) { meta.Request.Tools = []protocolkit.ToolCallRequest{{Type: "function"}} }},
		{"tool choice", func(meta *relaycommon.Meta) { meta.Request.ToolChoice = "auto" }},
		{"response format", func(meta *relaycommon.Meta) {
			meta.Request.ResponseFormat = &protocolkit.ResponseFormat{Type: "json_object"}
		}},
		{"multiple responses", func(meta *relaycommon.Meta) { n := 2; meta.Request.N = &n }},
		{"no user message", func(meta *relaycommon.Meta) {
			meta.Request.Messages = []protocolkit.Message{{Role: "system", Content: "only"}}
		}},
		{"empty user", func(meta *relaycommon.Meta) { meta.Request.Messages[0].Content = "  " }},
		{"non-text part", func(meta *relaycommon.Meta) {
			meta.Request.Messages[0].Content = []any{map[string]any{"type": "image_url"}}
		}},
		{"bad user ID", func(meta *relaycommon.Meta) { meta.Request.User = "bad\nuser" }},
		{"oversized message", func(meta *relaycommon.Meta) {
			meta.Request.Messages[0].Content = strings.Repeat("x", maxMessageBytes+1)
		}},
		{"too many messages", func(meta *relaycommon.Meta) {
			meta.Request.Messages = make([]protocolkit.Message, maxMessages+1)
			for index := range meta.Request.Messages {
				meta.Request.Messages[index] = protocolkit.Message{Role: "user", Content: "x"}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := cozeMeta()
			test.mutate(meta)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			_, err := adaptor.ConvertRequest(meta)
			assert.Error(t, err)
		})
	}
}

func TestCozeBlockingCreationPollDetailAndUsage(t *testing.T) {
	meta := cozeMeta()
	var statusCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "Bearer coze-secret", request.Header.Get("Authorization"))
		assert.Equal(t, "conv&A", request.URL.Query().Get("conversation_id"))
		assert.Equal(t, "chat/1", request.URL.Query().Get("chat_id"))
		switch request.URL.Path {
		case "/gateway/v3/chat/retrieve":
			call := statusCalls.Add(1)
			if call == 1 {
				return cozeHTTPResponse(http.StatusOK, `{"code":0,"data":{"status":"processing"}}`), nil
			}
			return cozeHTTPResponse(http.StatusOK, `{"code":0,"data":{"id":"chat/1","conversation_id":"conv&A","status":"completed","usage":{"input_count":5,"output_count":3,"token_count":7}}}`), nil
		case "/gateway/v3/chat/message/list":
			return cozeHTTPResponse(http.StatusOK, `{"code":0,"data":[{"type":"follow_up","content":"\"ignore\""},{"type":"answer","chat_id":"chat/1","conversation_id":"conv&A","content":"hello from Coze","created_at":1234}]}`), nil
		default:
			return cozeHTTPResponse(http.StatusNotFound, "missing"), nil
		}
	})}
	adaptor := &Adaptor{pollClient: client, pollInterval: time.Nanosecond, maxPollAttempts: 3}
	adaptor.Init(meta)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoResponse(context, cozeHTTPResponse(http.StatusOK,
		`{"code":0,"data":{"id":"chat/1","conversation_id":"conv&A","status":"created"}}`), meta)
	require.NoError(t, err)
	assert.Equal(t, int32(2), statusCalls.Load())
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}, usage)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var output protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Regexp(t, `^chatcmpl-coze-[0-9a-f]{32}$`, output.Id)
	assert.Equal(t, "client-model", output.Model)
	assert.EqualValues(t, "hello from Coze", output.Choices[0].Message.Content)
	assert.Equal(t, int64(1234), output.Created)
}

func TestCozeBlockingRejectionRefundsButAcceptedFailuresReportUsage(t *testing.T) {
	meta := cozeMeta()
	adaptor := &Adaptor{maxPollAttempts: 1, pollInterval: time.Nanosecond}
	adaptor.Init(meta)

	for _, test := range []struct {
		name      string
		body      string
		client    *http.Client
		wantUsage bool
	}{
		{"provider rejection", `{"code":4001,"msg":"bad bot"}`, nil, false},
		{"malformed accepted response", `{`, nil, true},
		{"accepted missing IDs", `{"code":0,"data":{}}`, nil, true},
		{"accepted poll transport failure", `{"code":0,"data":{"id":"chat","conversation_id":"conv"}}`, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("coze-secret network failure")
		})}, true},
		{"accepted terminal failure", `{"code":0,"data":{"id":"chat","conversation_id":"conv"}}`, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return cozeHTTPResponse(http.StatusOK, `{"code":0,"data":{"status":"failed","last_error":{"code":9,"message":"failed"}}}`), nil
		})}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := *adaptor
			if test.client != nil {
				candidate.pollClient = test.client
			}
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			usage, err := candidate.DoResponse(context, cozeHTTPResponse(http.StatusOK, test.body), meta)
			assert.Error(t, err)
			if test.wantUsage {
				require.NotNil(t, usage)
				assert.Positive(t, usage.TotalTokens)
			} else {
				assert.Nil(t, usage)
			}
		})
	}
}

func TestCozePollingHonorsContextAndUsesHardenedClient(t *testing.T) {
	client := newPollClient()
	assert.Equal(t, pollRequestTimeout, client.Timeout)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	request := httptest.NewRequest(http.MethodGet, "https://coze.test", nil)
	assert.ErrorIs(t, client.CheckRedirect(request, nil), http.ErrUseLastResponse)

	ctx, cancel := context.WithCancel(context.Background())
	meta := cozeMeta()
	meta.Context = ctx
	pollClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return cozeHTTPResponse(http.StatusOK, `{"code":0,"data":{"status":"processing"}}`), nil
	})}
	adaptor := &Adaptor{pollClient: pollClient, pollInterval: time.Hour, maxPollAttempts: 2}
	adaptor.Init(meta)
	_, err := adaptor.pollUntilComplete(meta, "conv", "chat")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCozeStreamingConversionUsageAndFailureSemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := cozeMeta()
	meta.IsStream = true
	meta.Request.Stream = true
	stream := strings.Join([]string{
		"event: conversation.message.delta\n",
		"data: {\"id\":\"message-1\",\"type\":\"answer\",\"content\":\"hello \"}\n\n",
		"event: conversation.message.delta\n",
		"data: {\"id\":\"message-2\",\"type\":\"answer\",\"content\":\"world\"}\n\n",
		"event: conversation.chat.completed\n",
		"data: {\"id\":\"chat-1\",\"status\":\"completed\",\"usage\":{\"input_count\":6,\"output_count\":2,\"token_count\":8}}\n\n",
	}, "")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	usage, err := adaptor.DoResponse(context, cozeHTTPResponse(http.StatusOK, stream), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 6, CompletionTokens: 2, TotalTokens: 8}, usage)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, recorder.Body.String(), `"content":"hello "`)
	assert.Contains(t, recorder.Body.String(), `"content":"world"`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
	assert.Contains(t, recorder.Body.String(), `data: [DONE]`)
	assert.Contains(t, recorder.Body.String(), `"model":"client-model"`)

	tests := []struct {
		name      string
		stream    string
		wantUsage bool
		wantBody  string
	}{
		{"explicit early rejection", "event: error\ndata: {\"code\":4001,\"message\":\"rejected\"}\n\n", false, ""},
		{"malformed accepted stream", "event: conversation.message.delta\ndata: {\n\n", true, ""},
		{"accepted stream without terminal", "event: heartbeat\ndata: {}\n\n", true, ""},
		{"late error", "event: conversation.message.delta\ndata: {\"content\":\"accepted\"}\n\nevent: error\ndata: {\"code\":9,\"message\":\"late\"}\n\n", true, `"content":"accepted"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			usage, err := adaptor.DoResponse(context, cozeHTTPResponse(http.StatusOK, test.stream), meta)
			assert.Error(t, err)
			if test.wantUsage {
				require.NotNil(t, usage)
				assert.Positive(t, usage.TotalTokens)
			} else {
				assert.Nil(t, usage)
			}
			if test.wantBody != "" {
				assert.Contains(t, recorder.Body.String(), test.wantBody)
			}
		})
	}
}

func TestCozeResponseBoundsAndUsageValidation(t *testing.T) {
	meta := cozeMeta()
	invalidUsage := []cozeUsage{
		{InputCount: -1},
		{OutputCount: int(quotamath.MaxQuota) + 1},
		{TokenCount: int(quotamath.MaxQuota) + 1},
		{InputCount: int(quotamath.MaxQuota), OutputCount: 1},
	}
	for _, provider := range invalidUsage {
		usage, err := normalizedCozeUsage(provider, meta, "answer")
		assert.Error(t, err)
		assert.Nil(t, usage)
	}
	usage, err := normalizedCozeUsage(cozeUsage{}, meta, "answer")
	require.NoError(t, err)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)

	_, _, err = cozeAnswer(messageListEnvelope{Data: []messageDetail{{Type: "answer", Content: json.RawMessage(`123`)}}}, "conv", "chat")
	assert.Error(t, err)
	_, _, err = cozeAnswer(messageListEnvelope{Data: []messageDetail{{Type: "answer", Content: json.RawMessage(`"ok"`), ChatID: "other"}}}, "conv", "chat")
	assert.Error(t, err)
	_, _, err = cozeAnswer(messageListEnvelope{}, "conv", "chat")
	assert.Error(t, err)

	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	usage, err = adaptor.DoResponse(context, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(io.LimitReader(&repeatingReader{value: 'x'}, maxProviderResponseBody+1)),
	}, meta)
	assert.Error(t, err)
	require.NotNil(t, usage)
	assert.Positive(t, usage.TotalTokens)

	usage, err = adaptor.DoResponse(context, cozeHTTPResponse(http.StatusTooManyRequests, `{"error":{"message":"coze-secret rejected"}}`), meta)
	assert.Error(t, err)
	assert.Nil(t, usage)
}

type repeatingReader struct{ value byte }

func (reader *repeatingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = reader.value
	}
	return len(buffer), nil
}
