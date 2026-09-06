package dify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func difyMeta() *relaycommon.Meta {
	return &relaycommon.Meta{
		Context:           context.Background(),
		Channel:           &model.Channel{},
		Mode:              constant.RelayModeChatCompletions,
		Format:            constant.RelayFormatOpenAI,
		OriginalModelName: "client-model",
		ModelName:         "mapped-model-is-not-sent",
		BaseURL:           "https://api.dify.test/gateway",
		APIKey:            "dify-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "client-model",
			Messages: []protocolkit.Message{
				{Role: "user", Content: "hello"},
			},
		},
		PromptTokens: 3,
	}
}

func difyResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDifyModelListURLHeadersAndUnsupportedModes(t *testing.T) {
	assert.Empty(t, ModelList())
	models := ModelList()
	models = append(models, "mutated")
	assert.Empty(t, ModelList())

	meta := difyMeta()
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://api.dify.test/gateway/v1/chat-messages", requestURL)
	meta.BaseURL = ""
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://api.dify.ai/v1/chat-messages", requestURL)

	req := httptest.NewRequest(http.MethodPost, requestURL, nil)
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Equal(t, "Bearer dify-secret", req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", req.Header.Get("Accept"))
	meta.IsStream = true
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))

	for _, badBase := range []string{
		"ftp://api.dify.test", "https://user:pass@api.dify.test", "https://api.dify.test?q=1",
		"https://api.dify.test/#fragment", "http:///missing-host",
	} {
		bad := difyMeta()
		bad.BaseURL = badBase
		adapter := &Adaptor{}
		adapter.Init(bad)
		_, err := adapter.GetRequestURL(bad)
		assert.Error(t, err, badBase)
	}

	for _, key := range []string{"", "bad\r\nkey", strings.Repeat("k", maxDifyCredentialBytes+1)} {
		bad := difyMeta()
		bad.APIKey = key
		adapter := &Adaptor{}
		adapter.Init(bad)
		request := httptest.NewRequest(http.MethodPost, "https://api.dify.test", nil)
		assert.Error(t, adapter.SetupRequestHeader(request, bad), key)
	}

	for _, tc := range []struct {
		mode   constant.RelayMode
		format constant.RelayFormat
	}{
		{constant.RelayModeCompletions, constant.RelayFormatOpenAI},
		{constant.RelayModeEmbeddings, constant.RelayFormatEmbedding},
		{constant.RelayModeImagesGenerations, constant.RelayFormatOpenAIImage},
		{constant.RelayModeRerank, constant.RelayFormatRerank},
		{constant.RelayModeResponses, constant.RelayFormatOpenAIResponses},
		{constant.RelayModeChatCompletions, constant.RelayFormatClaude},
		{constant.RelayModeChatCompletions, constant.RelayFormatGemini},
	} {
		bad := difyMeta()
		bad.Mode, bad.Format = tc.mode, tc.format
		adapter := &Adaptor{}
		adapter.Init(bad)
		_, err := adapter.GetRequestURL(bad)
		assert.Error(t, err, "mode=%d format=%s", tc.mode, tc.format)
		_, err = adapter.ConvertRequest(bad)
		assert.Error(t, err, "mode=%d format=%s", tc.mode, tc.format)
	}
}

func TestDifyRequestConversionMatchesReferenceChatFlow(t *testing.T) {
	meta := difyMeta()
	meta.Request.User = "stable-user"
	meta.Request.Stream = true
	meta.IsStream = true
	meta.Request.Messages = []protocolkit.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "look"},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "https://images.example.test/a.png?signature=ok", "mime_type": "image/png",
			}},
		}},
		{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "answer"}}},
		{Role: "tool", Content: "tool output"},
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, map[string]any{}, got["inputs"])
	assert.Equal(t, false, got["auto_generate_name"])
	assert.Equal(t, "stable-user", got["user"])
	assert.Equal(t, "streaming", got["response_mode"])
	assert.Equal(t, "SYSTEM: \nrules\nUSER: \nlook\nASSISTANT: \nanswer\nUSER: \ntool output\n", got["query"])
	assert.NotContains(t, got, "model", "Dify applications, not relay model mappings, own the model")
	files := got["files"].([]any)
	require.Len(t, files, 1)
	file := files[0].(map[string]any)
	assert.Equal(t, "image/png", file["type"])
	assert.Equal(t, "remote_url", file["transfer_mode"])
	assert.Equal(t, "https://images.example.test/a.png?signature=ok", file["url"])

	meta = difyMeta()
	meta.Request.User = ""
	meta.ClientHeaders = http.Header{"X-Request-Id": []string{"request-id-for-dify"}}
	adaptor = &Adaptor{}
	adaptor.Init(meta)
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "request-id-for-dify", got["user"])
	assert.Equal(t, "blocking", got["response_mode"])
	assert.Equal(t, []any{}, got["files"])
}

func TestDifyInlineImageUploadIsBoundedAndAuthenticated(t *testing.T) {
	type contextKey string
	const requestMarker contextKey = "request-marker"
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		assert.Equal(t, "preserved", request.Context().Value(requestMarker))
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "https://api.dify.test/gateway/v1/files/upload", request.URL.String())
		assert.Equal(t, "Bearer dify-secret", request.Header.Get("Authorization"))
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		require.NoError(t, err)
		assert.Equal(t, "multipart/form-data", mediaType)
		reader, err := request.MultipartReader()
		require.NoError(t, err)
		var user, filename string
		var uploaded []byte
		for {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				break
			}
			require.NoError(t, partErr)
			value, readErr := io.ReadAll(part)
			require.NoError(t, readErr)
			if part.FormName() == "user" {
				user = string(value)
			} else if part.FormName() == "file" {
				filename, uploaded = part.FileName(), value
			}
		}
		assert.Equal(t, "upload-user", user)
		assert.Equal(t, "image.png", filename)
		assert.Equal(t, []byte("png-bytes"), uploaded)
		return difyResponse(http.StatusCreated, "application/json", `{"id":"uploaded-1"}`), nil
	})}

	meta := difyMeta()
	meta.Context = context.WithValue(context.Background(), requestMarker, "preserved")
	meta.Request.User = "upload-user"
	meta.Request.Messages[0].Content = []any{map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url":       "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes")),
			"mime_type": "image/png",
		},
	}}
	adaptor := &Adaptor{uploadClient: client}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())
	var got struct {
		Files []difyFile `json:"files"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got.Files, 1)
	assert.Equal(t, difyFile{Type: "image", TransferMode: "local_file", UploadFileID: "uploaded-1"}, got.Files[0])
}

func TestDifyUploadClientUsesSafeBoundedTransportAndRejectsRedirects(t *testing.T) {
	client := newUploadClient()
	assert.Equal(t, difyUploadRequestTimeout, client.Timeout)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotNil(t, transport.DialContext)
	assert.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 20*time.Second, transport.ResponseHeaderTimeout)
	request := httptest.NewRequest(http.MethodPost, "https://api.dify.test/v1/files/upload", nil)
	assert.ErrorIs(t, client.CheckRedirect(request, nil), http.ErrUseLastResponse)
}

func TestDifyUploadResponseIsBounded(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       io.NopCloser(io.LimitReader(&repeatingReader{value: 'x'}, maxDifyUploadResponseBytes+1)),
		}, nil
	})}
	meta := difyMeta()
	meta.Request.Messages[0].Content = []any{map[string]any{
		"type": "image_url", "image_url": map[string]any{
			"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("image")),
		},
	}}
	adaptor := &Adaptor{uploadClient: client}
	adaptor.Init(meta)
	_, err := adaptor.ConvertRequest(meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestDifyRequestRejectsMalformedOrExcessiveMediaBeforeUpload(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return difyResponse(http.StatusOK, "application/json", `{"id":"unexpected"}`), nil
	})}

	tests := []struct {
		name    string
		content any
	}{
		{name: "bad base64", content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,%%%"}}}},
		{name: "unsafe remote", content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "file:///private/image.png"}}}},
		{name: "remote credentials", content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://user:pass@example.test/image.png"}}}},
		{name: "unsupported audio", content: []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "abc", "format": "wav"}}}},
		{name: "invalid part", content: []any{"not-an-object"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := difyMeta()
			meta.Request.Messages[0].Content = test.content
			adaptor := &Adaptor{uploadClient: client}
			adaptor.Init(meta)
			_, err := adaptor.ConvertRequest(meta)
			assert.Error(t, err)
		})
	}

	meta := difyMeta()
	parts := make([]any, maxDifyFiles+1)
	for index := range parts {
		parts[index] = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}
	}
	meta.Request.Messages[0].Content = parts
	adaptor := &Adaptor{uploadClient: client}
	adaptor.Init(meta)
	_, err := adaptor.ConvertRequest(meta)
	assert.Error(t, err)
	assert.Zero(t, calls.Load())

	meta = difyMeta()
	tooLarge := bytes.Repeat([]byte{'x'}, maxDifyInlineFileBytes+1)
	meta.Request.Messages[0].Content = []any{map[string]any{
		"type": "image_url", "image_url": map[string]any{
			"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(tooLarge),
		},
	}}
	adaptor = &Adaptor{uploadClient: client}
	adaptor.Init(meta)
	_, err = adaptor.ConvertRequest(meta)
	assert.Error(t, err)
	assert.Zero(t, calls.Load())

	meta = difyMeta()
	meta.Request.User = strings.Repeat("u", maxDifyUserBytes+1)
	adaptor = &Adaptor{uploadClient: client}
	adaptor.Init(meta)
	_, err = adaptor.ConvertRequest(meta)
	assert.Error(t, err)
	assert.Zero(t, calls.Load())
}

func TestDifyBlockingResponseNormalizesUsageAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := difyMeta()
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoResponse(ctx, difyResponse(http.StatusOK, "application/json",
		`{"conversation_id":"conversation-1","answer":"answer","create_at":123,"metadata":{"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}}`), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, usage)
	var got protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &got))
	assert.Equal(t, "conversation-1", got.Id)
	assert.Equal(t, "", got.Model)
	assert.Equal(t, "assistant", got.Choices[0].Message.Role)
	assert.Equal(t, "answer", got.Choices[0].Message.Content)
	assert.Equal(t, "stop", got.Choices[0].FinishReason)

	meta.PromptTokens = 4
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	usage, err = adaptor.DoResponse(ctx, difyResponse(http.StatusOK, "application/json",
		`{"conversation_id":"fallback","answer":"four token-ish words","metadata":{"usage":{}}}`), meta)
	require.NoError(t, err)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Positive(t, usage.CompletionTokens)
	assert.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	_, err = adaptor.DoResponse(ctx, difyResponse(http.StatusOK, "application/json",
		`{"conversation_id":"bad","answer":"answer","metadata":{"usage":{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}}}`), meta)
	assert.Error(t, err)
	assert.Empty(t, recorder.Body.String())

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	_, err = adaptor.DoResponse(ctx, difyResponse(http.StatusOK, "application/json",
		`{"conversation_id":"bad-details","answer":"answer","metadata":{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":-1}}}}`), meta)
	assert.Error(t, err)
	assert.Empty(t, recorder.Body.String())

	oversized := io.LimitReader(&repeatingReader{value: ' '}, relaycommon.MaxUpstreamJSONBodyBytes+1)
	_, err = adaptor.DoResponse(ctx, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(oversized)}, meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestDifyStreamConvertsEventsDebugUsageAndTerminator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("DIFY_DEBUG", "true")
	meta := difyMeta()
	meta.IsStream = true
	meta.Request.Stream = true
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	stream := strings.Join([]string{
		`data: {"event":"message","conversation_id":"c","answer":"<details style=\"color:gray;background-color: #f8f8f8;padding: 8px;border-radius: 4px;\" open> <summary> Thinking... </summary>\n"}`,
		`data: {"event":"agent_message","conversation_id":"c","answer":"hello"}`,
		`data: {"event":"node_finished","data":{"node_type":"llm","status":"succeeded"}}`,
		`data: {"event":"message_end","metadata":{"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}}`,
		`data: {"event":"message","answer":"must-not-appear"}`,
	}, "\n\n") + "\n\n"
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoResponse(ctx, difyResponse(http.StatusOK, "text/event-stream", stream), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}, usage)
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, recorder.Body.String(), `"content":"\u003cthink\u003e"`)
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Contains(t, recorder.Body.String(), `"reasoning_content":"Node: llm succeeded\n"`)
	assert.NotContains(t, recorder.Body.String(), "must-not-appear")
	assert.True(t, strings.HasSuffix(recorder.Body.String(), "data: [DONE]\n\n"))
}

func TestDifyStreamErrorsAreBoundedAndRefundableBeforeFirstChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := difyMeta()
	meta.IsStream = true
	meta.Request.Stream = true
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	for _, stream := range []string{
		`data: {"event":"error","message":"provider rejected request","code":"bad_request"}` + "\n\n",
		"data: not-json\n\n",
		"data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n",
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		_, err := adaptor.DoResponse(ctx, difyResponse(http.StatusOK, "text/event-stream", stream), meta)
		assert.Error(t, err)
		assert.False(t, recorder.Result().Body == nil)
		assert.False(t, recorder.Flushed, "headers must remain retractable before a valid chunk")
		assert.Empty(t, recorder.Body.String())
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	response := difyResponse(http.StatusOK, "text/event-stream", strings.Repeat(": keepalive", 20))
	_, err := difyStreamResponseWithLimit(ctx, response, meta, 64)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.False(t, recorder.Flushed)
	assert.Empty(t, recorder.Body.String())
}

func TestDifyHTTPErrorMapsStatusAndOpenAIEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := difyMeta()
	meta.Channel.StatusCodeMapping = `{"429":"503"}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	_, err := adaptor.DoResponse(ctx, difyResponse(http.StatusTooManyRequests, "application/json",
		`{"code":"rate_limit","status":429,"message":"slow down"}`), meta)
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.JSONEq(t, `{"error":{"message":"slow down","type":"upstream_error","code":"rate_limit"}}`, upstream.Body)
	assert.Empty(t, recorder.Body.String())

	tooLarge := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(
		io.LimitReader(&repeatingReader{value: 'x'}, relaycommon.MaxUpstreamErrorBodyBytes+1),
	)}
	_, err = adaptor.DoResponse(ctx, tooLarge, meta)
	require.Error(t, err)
	require.ErrorAs(t, err, &upstream)
	assert.Empty(t, upstream.Body)
}

type repeatingReader struct{ value byte }

func (reader *repeatingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = reader.value
	}
	return len(buffer), nil
}
