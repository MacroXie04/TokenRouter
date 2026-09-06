package xunfei

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const testCredential = "app-id-1234|api-secret-5678|api-key-9012"

var fixedTime = time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)

func xunfeiMeta(model string, stream bool) *relaycommon.Meta {
	maxTokens, maxCompletionTokens, n := 17, 23, 4
	temperature := 0.7
	request := &protocolkit.GeneralOpenAIRequest{
		Model: model, Stream: stream, Temperature: &temperature, N: &n,
		MaxTokens: &maxTokens, MaxCompletionTokens: &maxCompletionTokens,
		Messages: []protocolkit.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hello"},
		},
	}
	return &relaycommon.Meta{
		Context: context.Background(), Channel: nil, Mode: constant.RelayModeChatCompletions,
		Format: constant.RelayFormatOpenAI, OriginalModelName: model, ModelName: model,
		BaseURL: "https://attacker.invalid/credential-collector", APIKey: testCredential,
		Request: request, IsStream: stream,
	}
}

func TestModelCatalogAndFixedProviderEndpoints(t *testing.T) {
	want := []string{"SparkDesk", "SparkDesk-v1.1", "SparkDesk-v2.1", "SparkDesk-v3.1", "SparkDesk-v3.5", "SparkDesk-v4.0"}
	assert.Equal(t, want, ModelList())
	mutated := ModelList()
	mutated[0] = "mutated"
	assert.Equal(t, "SparkDesk", ModelList()[0])
	assert.Equal(t, "xunfei", ChannelName)

	adaptor := &Adaptor{}
	for _, test := range []struct {
		model, apiVersion, version, domain string
	}{
		{"SparkDesk", "", "v1.1", "lite"},
		{"SparkDesk-v1.1", "", "v1.1", "lite"},
		{"SparkDesk-v2.1", "", "v2.1", "generalv2"},
		{"SparkDesk-v3.1", "", "v3.1", "generalv3"},
		{"SparkDesk-v3.5", "", "v3.5", "generalv3.5"},
		{"SparkDesk-v4.0", "", "v4.0", "4.0Ultra"},
		{"SparkDesk-v1.1", "v4.0", "v4.0", "4.0Ultra"},
	} {
		meta := xunfeiMeta(test.model, false)
		meta.APIVersion = test.apiVersion
		adaptor.Init(meta)
		got, err := adaptor.GetRequestURL(meta)
		require.NoError(t, err)
		assert.Equal(t, "wss://spark-api.xf-yun.com/"+test.version+"/chat", got)
		assert.NotContains(t, got, "attacker.invalid")
		version, domain, err := resolveVersion(meta)
		require.NoError(t, err)
		assert.Equal(t, test.version, version)
		assert.Equal(t, test.domain, domain)
	}
}

func TestConvertOpenAIRequestToExactSparkWireContract(t *testing.T) {
	adaptor := &Adaptor{}
	meta := xunfeiMeta("SparkDesk-v4.0", false)
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "app-id-1234", got["header"].(map[string]any)["app_id"])
	chat := got["parameter"].(map[string]any)["chat"].(map[string]any)
	assert.Equal(t, "4.0Ultra", chat["domain"])
	assert.EqualValues(t, 0.7, chat["temperature"])
	assert.EqualValues(t, 4, chat["top_k"])
	assert.EqualValues(t, 23, chat["max_tokens"], "max_completion_tokens has reference precedence")
	texts := got["payload"].(map[string]any)["message"].(map[string]any)["text"].([]any)
	require.Len(t, texts, 3)
	assert.Equal(t, map[string]any{"role": "user", "content": "be concise"}, texts[0])
	assert.Equal(t, map[string]any{"role": "assistant", "content": "Okay"}, texts[1])
	assert.Equal(t, map[string]any{"role": "user", "content": "hello"}, texts[2])
	assert.NotContains(t, string(body), "api-secret-5678")
	assert.NotContains(t, string(body), "api-key-9012")
	assert.NotContains(t, got, "model")
	assert.NotContains(t, got, "stream")

	meta35 := xunfeiMeta("SparkDesk-v3.5", false)
	adaptor.Init(meta35)
	body35, err := adaptor.ConvertRequest(meta35)
	require.NoError(t, err)
	var wire35 sparkRequest
	require.NoError(t, strictJSON(body35, &wire35))
	require.Len(t, wire35.Payload.Message.Text, 2)
	assert.Equal(t, sparkMessage{Role: "system", Content: "be concise"}, wire35.Payload.Message.Text[0])
	assert.Equal(t, "generalv3.5", wire35.Parameter.Chat.Domain)
}

func TestAuthURLUsesExactHMACWireContract(t *testing.T) {
	endpoint := "wss://spark-api.xf-yun.com/v3.5/chat"
	got, err := buildAuthURL(endpoint, "api-key-9012", "api-secret-5678", fixedTime)
	require.NoError(t, err)
	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "wss", parsed.Scheme)
	assert.Equal(t, providerHost, parsed.Host)
	assert.Equal(t, "/v3.5/chat", parsed.Path)
	assert.Equal(t, fixedTime.Format(time.RFC1123), parsed.Query().Get("date"))
	assert.Equal(t, providerHost, parsed.Query().Get("host"))
	authorizationBytes, err := base64.StdEncoding.DecodeString(parsed.Query().Get("authorization"))
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte("api-secret-5678"))
	_, _ = io.WriteString(mac, "host: spark-api.xf-yun.com\ndate: "+fixedTime.Format(time.RFC1123)+"\nGET /v3.5/chat HTTP/1.1")
	expectedSignature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	assert.Equal(t,
		`hmac username="api-key-9012", algorithm="hmac-sha256", headers="host date request-line", signature="`+expectedSignature+`"`,
		string(authorizationBytes),
	)
	assert.NotContains(t, got, "api-secret-5678")
}

type websocketFixture struct {
	server        *httptest.Server
	dial          DialContextFunc
	signedURLs    chan string
	requestBodies chan []byte
}

func newWebsocketFixture(t *testing.T, frames []string) websocketFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	requests := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_, body, err := connection.ReadMessage()
		if err != nil {
			return
		}
		requests <- append([]byte(nil), body...)
		for _, frame := range frames {
			if err := connection.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	localURL := "ws" + strings.TrimPrefix(server.URL, "http")
	signedURLs := make(chan string, 1)
	dial := func(ctx context.Context, requested string, header http.Header) (*websocket.Conn, *http.Response, error) {
		signedURLs <- requested
		return websocket.DefaultDialer.DialContext(ctx, localURL, header)
	}
	return websocketFixture{server: server, dial: dial, signedURLs: signedURLs, requestBodies: requests}
}

func sparkFrame(sequence, status, promptTokens, completionTokens, totalTokens int, content string) string {
	body, _ := json.Marshal(map[string]any{
		"header": map[string]any{"code": 0, "message": "", "sid": "sid-123", "status": status},
		"payload": map[string]any{
			"choices": map[string]any{
				"status": status, "seq": sequence,
				"text": []any{map[string]any{"content": content, "role": "assistant", "index": 0}},
			},
			"usage": map[string]any{"text": map[string]any{
				"question_tokens": promptTokens, "prompt_tokens": promptTokens,
				"completion_tokens": completionTokens, "total_tokens": totalTokens,
			}},
		},
	})
	return string(body)
}

func TestDirectWebSocketNonStreamWireResponseAndUsage(t *testing.T) {
	fixture := newWebsocketFixture(t, []string{
		sparkFrame(0, 0, 0, 0, 0, "hello "),
		sparkFrame(1, 2, 7, 3, 10, "world"),
	})
	meta := xunfeiMeta("SparkDesk-v4.0", false)
	adaptor := &Adaptor{DialContext: fixture.dial, Now: func() time.Time { return fixedTime }}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoDirectRequest(c, requestURL, body, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}, usage)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var response protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "chat.completion", response.Object)
	assert.Equal(t, fixedTime.Unix(), response.Created)
	require.Len(t, response.Choices, 1)
	assert.Equal(t, "hello world", response.Choices[0].Message.Content)
	assert.Equal(t, "assistant", response.Choices[0].Message.Role)
	assert.Equal(t, "stop", response.Choices[0].FinishReason)
	assert.Empty(t, response.Id)
	assert.Empty(t, response.Model)

	signed := <-fixture.signedURLs
	assert.True(t, strings.HasPrefix(signed, "wss://spark-api.xf-yun.com/v4.0/chat?"))
	assert.NotContains(t, signed, "app-id-1234")
	assert.NotContains(t, signed, "api-secret-5678")
	assert.Equal(t, body, <-fixture.requestBodies)
}

func TestDirectWebSocketStreamExactSSEAndUsage(t *testing.T) {
	fixture := newWebsocketFixture(t, []string{
		sparkFrame(0, 0, 0, 0, 0, "one"),
		sparkFrame(1, 2, 5, 2, 7, "two"),
	})
	meta := xunfeiMeta("SparkDesk-v2.1", true)
	adaptor := &Adaptor{DialContext: fixture.dial, Now: func() time.Time { return fixedTime }}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoDirectRequest(c, requestURL, body, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, usage)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	stream := recorder.Body.String()
	assert.Equal(t, 1, strings.Count(stream, "data: [DONE]\n\n"))
	assert.Contains(t, stream, `"content":"one"`)
	assert.Contains(t, stream, `"content":"two"`)
	assert.Contains(t, stream, `"finish_reason":"stop"`)
	assert.Equal(t, 2, strings.Count(stream, `"model":"SparkDesk"`))
	assert.True(t, strings.HasSuffix(stream, "data: [DONE]\n\n"))
}

func TestStrictRequestValidationAndBounds(t *testing.T) {
	adaptor := &Adaptor{}
	valid := xunfeiMeta("SparkDesk", false)
	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"unknown model", func(meta *relaycommon.Meta) { meta.ModelName = "spark-unknown" }},
		{"unknown version", func(meta *relaycommon.Meta) { meta.APIVersion = "v9.9" }},
		{"wrong mode", func(meta *relaycommon.Meta) { meta.Mode = constant.RelayModeEmbeddings }},
		{"wrong format", func(meta *relaycommon.Meta) { meta.Format = constant.RelayFormatClaude }},
		{"stream mismatch", func(meta *relaycommon.Meta) { meta.IsStream = true }},
		{"missing messages", func(meta *relaycommon.Meta) { meta.Request.Messages = nil }},
		{"bad role", func(meta *relaycommon.Meta) { meta.Request.Messages[0].Role = "tool" }},
		{"media content", func(meta *relaycommon.Meta) {
			meta.Request.Messages[0].Content = []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test"}}}
		}},
		{"control content", func(meta *relaycommon.Meta) { meta.Request.Messages[0].Content = "bad\x00text" }},
		{"oversized content", func(meta *relaycommon.Meta) {
			meta.Request.Messages[0].Content = strings.Repeat("x", maxMessageTextBytes+1)
		}},
		{"bad temperature", func(meta *relaycommon.Meta) { value := 1.1; meta.Request.Temperature = &value }},
		{"nan temperature", func(meta *relaycommon.Meta) { value := math.NaN(); meta.Request.Temperature = &value }},
		{"bad top k", func(meta *relaycommon.Meta) { value := maxTopK + 1; meta.Request.N = &value }},
		{"bad max tokens", func(meta *relaycommon.Meta) { value := maxOutputTokens + 1; meta.Request.MaxTokens = &value }},
		{"bad max completion", func(meta *relaycommon.Meta) { value := -1; meta.Request.MaxCompletionTokens = &value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyMeta := *valid
			copyRequest := *valid.Request
			copyRequest.Messages = append([]protocolkit.Message(nil), valid.Request.Messages...)
			copyMeta.Request = &copyRequest
			test.mutate(&copyMeta)
			adaptor.Init(&copyMeta)
			_, err := adaptor.ConvertRequest(&copyMeta)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "api-secret-5678")
			assert.NotContains(t, err.Error(), "api-key-9012")
		})
	}

	for _, credential := range []string{
		"", "one|two", "one|two|three|four", "app|sec|key", "appid|secret\n|apikey",
		"app id|secret-value|apikey-value", `appid-value|secret-value|api"key`, `appid-value|secret-value|api\key`,
	} {
		meta := xunfeiMeta("SparkDesk", false)
		meta.APIKey = credential
		adaptor.Init(meta)
		_, err := adaptor.ConvertRequest(meta)
		require.Error(t, err, credential)
		if credential != "" {
			assert.NotContains(t, err.Error(), credential)
		}
	}
}

func TestMalformedOversizedAndCredentialBearingResponsesFailClosed(t *testing.T) {
	valid := sparkFrame(0, 2, 1, 1, 2, "ok")
	tests := []struct {
		name  string
		frame string
	}{
		{"unknown field", strings.Replace(valid, `"code":0`, `"code":0,"unexpected":true`, 1)},
		{"duplicate field", strings.Replace(valid, `"code":0`, `"code":0,"code":0`, 1)},
		{"negative usage", strings.Replace(valid, `"prompt_tokens":1`, `"prompt_tokens":-1`, 1)},
		{"credential", strings.Replace(valid, `"content":"ok"`, `"content":"api-secret-5678"`, 1)},
		{"too large", `{"header":{"code":0},"payload":{"choices":{"status":2,"seq":0,"text":[{"content":"` + strings.Repeat("x", int(maxResponseFrameBytes)+1) + `","role":"assistant","index":0}]},"usage":{"text":{}}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWebsocketFixture(t, []string{test.frame})
			meta := xunfeiMeta("SparkDesk", false)
			adaptor := &Adaptor{DialContext: fixture.dial, Now: func() time.Time { return fixedTime }}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			body, err := adaptor.ConvertRequest(meta)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			_, err = adaptor.DoDirectRequest(c, requestURL, body, meta)
			require.Error(t, err)
			assert.Empty(t, recorder.Body.String())
			assert.NotContains(t, err.Error(), "api-secret-5678")
			assert.NotContains(t, err.Error(), "api-key-9012")
		})
	}
}

func TestProviderRejectionAndHandshakeErrorsAreSanitized(t *testing.T) {
	rejection := `{"header":{"code":10013,"message":"api-secret-5678","sid":"sid","status":2},"payload":{"choices":{"status":2,"seq":0,"text":[]},"usage":{"text":{}}}}`
	fixture := newWebsocketFixture(t, []string{rejection})
	meta := xunfeiMeta("SparkDesk", false)
	adaptor := &Adaptor{DialContext: fixture.dial, Now: func() time.Time { return fixedTime }}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	_, err = adaptor.DoDirectRequest(c, requestURL, body, meta)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "api-secret-5678")
	assert.NotContains(t, err.Error(), "api-key-9012")

	adaptor.DialContext = func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error) {
		return nil, &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(testCredential))},
			errors.New("dial failed: " + testCredential)
	}
	_, err = adaptor.DoDirectRequest(c, requestURL, body, meta)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "app-id-1234")
	assert.NotContains(t, err.Error(), "api-secret-5678")
	assert.NotContains(t, err.Error(), "api-key-9012")
}

type closeTrackingBody struct {
	closed atomic.Bool
}

func (*closeTrackingBody) Read([]byte) (int, error) { return 0, io.EOF }
func (body *closeTrackingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestUnavailableConnectionClosesHandshakeResponse(t *testing.T) {
	meta := xunfeiMeta("SparkDesk", false)
	responseBody := &closeTrackingBody{}
	adaptor := &Adaptor{DialContext: func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error) {
		return nil, &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: responseBody}, nil
	}}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	_, err = adaptor.DoDirectRequest(c, requestURL, body, meta)
	require.Error(t, err)
	assert.True(t, responseBody.closed.Load())
}

func TestProductionDialerIsBoundedDirectAndSSRFSafe(t *testing.T) {
	dialer := newProductionDialer()
	assert.Nil(t, dialer.Proxy)
	assert.Nil(t, dialer.TLSClientConfig)
	assert.False(t, dialer.EnableCompression)
	assert.Equal(t, websocketHandshakeTimeout, dialer.HandshakeTimeout)
	assert.Equal(t, reflect.ValueOf(appcommon.SafeDialContext).Pointer(), reflect.ValueOf(dialer.NetDialContext).Pointer())

	meta := xunfeiMeta("SparkDesk", false)
	meta.BaseURL = "http://127.0.0.1:1"
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "wss://spark-api.xf-yun.com/v1.1/chat", requestURL)
}

func TestSetupHeadersNeverCopiesCredentials(t *testing.T) {
	meta := xunfeiMeta("SparkDesk", false)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	request := httptest.NewRequest(http.MethodPost, "https://example.test", nil)
	request.Header.Set("Authorization", "Bearer client-secret")
	request.Header.Set("x-api-key", "client-key")
	request.Header.Set("api-key", "provider-key")
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Empty(t, request.Header.Get("Authorization"))
	assert.Empty(t, request.Header.Get("x-api-key"))
	assert.Empty(t, request.Header.Get("api-key"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
}

func TestDirectRequestRejectsTamperedConvertedBody(t *testing.T) {
	meta := xunfeiMeta("SparkDesk", false)
	adaptor := &Adaptor{
		DialContext: func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error) {
			t.Fatal("tampered body must fail before dialing")
			return nil, nil, nil
		},
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	body = append(append([]byte(nil), body...), ' ')
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	_, err = adaptor.DoDirectRequest(c, requestURL, body, meta)
	require.Error(t, err)
}

func TestResponseSequenceMustBeMonotonicAndTerminal(t *testing.T) {
	for _, frames := range [][]string{
		{sparkFrame(0, 0, 0, 0, 0, "one")},
		{sparkFrame(0, 0, 0, 0, 0, "one"), sparkFrame(0, 2, 1, 1, 2, "two")},
		{sparkFrame(0, 1, 0, 0, 0, "one"), sparkFrame(1, 0, 1, 1, 2, "two")},
	} {
		fixture := newWebsocketFixture(t, frames)
		meta := xunfeiMeta("SparkDesk", false)
		adaptor := &Adaptor{DialContext: fixture.dial}
		adaptor.Init(meta)
		requestURL, err := adaptor.GetRequestURL(meta)
		require.NoError(t, err)
		body, err := adaptor.ConvertRequest(meta)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		_, err = adaptor.DoDirectRequest(c, requestURL, body, meta)
		require.Error(t, err)
		assert.Empty(t, recorder.Body.String())
	}
}

func TestNoWebSocketRedirectFollowingAtAdaptorBoundary(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	dialer := &websocket.Dialer{HandshakeTimeout: time.Second}
	_, response, err := dialer.Dial("ws"+strings.TrimPrefix(redirect.URL, "http"), nil)
	require.Error(t, err)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	assert.Zero(t, targetHits.Load())
}
