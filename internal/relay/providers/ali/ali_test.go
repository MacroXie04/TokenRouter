package ali

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func aliMeta(mode channelcatalog.RelayMode, format channelcatalog.RelayFormat, modelName string) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeAli)},
		Mode:    mode, Format: format, OriginalModelName: modelName, ModelName: modelName,
		BaseURL: "https://dashscope.example/gateway", APIKey: "ali-secret",
		RequestContentType: "application/json", Request: &protocolkit.GeneralOpenAIRequest{
			Model: modelName, Extra: map[string]any{"model": modelName},
		},
	}
}

func initializedAdaptor(meta *relaycommon.Meta) *Adaptor {
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	return adaptor
}

func TestAliURLsHeadersModesAndModelCatalog(t *testing.T) {
	tests := []struct {
		name, model, path string
		mode              channelcatalog.RelayMode
		format            channelcatalog.RelayFormat
		async             bool
	}{
		{name: "chat", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatOpenAI, model: "qwen-plus", path: "/gateway/compatible-mode/v1/chat/completions"},
		{name: "completions", mode: channelcatalog.RelayModeCompletions, format: channelcatalog.RelayFormatOpenAI, model: "qwen-plus", path: "/gateway/compatible-mode/v1/completions"},
		{name: "embeddings", mode: channelcatalog.RelayModeEmbeddings, format: channelcatalog.RelayFormatEmbedding, model: "text-embedding-v1", path: "/gateway/compatible-mode/v1/embeddings"},
		{name: "rerank", mode: channelcatalog.RelayModeRerank, format: channelcatalog.RelayFormatRerank, model: "gte-rerank-v2", path: "/gateway/api/v1/services/rerank/text-rerank/text-rerank"},
		{name: "responses", mode: channelcatalog.RelayModeResponses, format: channelcatalog.RelayFormatOpenAIResponses, model: "qwen-plus", path: "/gateway/api/v2/apps/protocols/compatible-mode/v1/responses"},
		{name: "sync generation", mode: channelcatalog.RelayModeImagesGenerations, format: channelcatalog.RelayFormatOpenAIImage, model: "qwen-image", path: "/gateway/api/v1/services/aigc/multimodal-generation/generation"},
		{name: "async generation", mode: channelcatalog.RelayModeImagesGenerations, format: channelcatalog.RelayFormatOpenAIImage, model: "wanx-v1", path: "/gateway/api/v1/services/aigc/text2image/image-synthesis", async: true},
		{name: "old Wan edit", mode: channelcatalog.RelayModeImagesEdits, format: channelcatalog.RelayFormatOpenAIImage, model: "wanx-style-repaint-v1", path: "/gateway/api/v1/services/aigc/image2image/image-synthesis", async: true},
		{name: "new Wan edit", mode: channelcatalog.RelayModeImagesEdits, format: channelcatalog.RelayFormatOpenAIImage, model: "wan2.6-image", path: "/gateway/api/v1/services/aigc/image-generation/generation", async: true},
		{name: "Qwen edit", mode: channelcatalog.RelayModeImagesEdits, format: channelcatalog.RelayFormatOpenAIImage, model: "qwen-image-edit", path: "/gateway/api/v1/services/aigc/multimodal-generation/generation"},
		{name: "native Claude", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatClaude, model: "Qwen3-Max", path: "/gateway/apps/anthropic/v1/messages"},
		{name: "converted Claude", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatClaude, model: "other-model", path: "/gateway/compatible-mode/v1/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := aliMeta(test.mode, test.format, test.model)
			if test.mode == channelcatalog.RelayModeChatCompletions {
				meta.IsStream = true
				meta.Request.Stream = true
			}
			adaptor := initializedAdaptor(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, "https://dashscope.example"+test.path, requestURL)
			request := httptest.NewRequest(http.MethodPost, requestURL, nil)
			require.NoError(t, adaptor.SetupRequestHeader(request, meta))
			assert.Equal(t, "Bearer ali-secret", request.Header.Get("Authorization"))
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			if meta.IsStream {
				assert.Equal(t, "enable", request.Header.Get("X-DashScope-SSE"))
				assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))
			}
			assert.Equal(t, test.async, request.Header.Get("X-DashScope-Async") == "enable")
		})
	}

	models := ModelList()
	require.Equal(t, []string{"qwen-turbo", "qwen-plus", "qwen-max", "qwen-max-longcontext", "qwq-32b", "qwen3-235b-a22b", "text-embedding-v1", "gte-rerank-v2"}, models)
	models[0] = "mutated"
	assert.Equal(t, "qwen-turbo", ModelList()[0])
	t.Setenv(aliAnthropicMessagesModelsEnv, " custom-claude , ")
	assert.True(t, SupportsAnthropicMessages("Vendor/CUSTOM-Claude-1"))
	assert.False(t, SupportsAnthropicMessages("qwen-plus"))
}

func TestAliPluginHeaderCredentialAndFailClosedModes(t *testing.T) {
	meta := aliMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "qwen-plus")
	meta.Channel.Other = "plugin-name"
	adaptor := initializedAdaptor(meta)
	request := httptest.NewRequest(http.MethodPost, "https://dashscope.example", nil)
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "plugin-name", request.Header.Get("X-DashScope-Plugin"))

	meta.APIKey = ""
	require.ErrorContains(t, adaptor.SetupRequestHeader(request, meta), "API key")
	meta.APIKey = "ali-secret"
	meta.Channel.Other = strings.Repeat("x", maxDashScopePluginHeaderBytes+1)
	require.ErrorContains(t, adaptor.SetupRequestHeader(request, meta), "plugin header")

	for _, invalid := range []*relaycommon.Meta{
		aliMeta(channelcatalog.RelayModeModerations, channelcatalog.RelayFormatOpenAI, "qwen-plus"),
		aliMeta(channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio, "qwen-plus"),
		aliMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatGemini, "qwen-plus"),
		aliMeta(channelcatalog.RelayModeResponsesCompact, channelcatalog.RelayFormatOpenAIResponsesCompaction, "qwen-plus"),
		aliMeta(channelcatalog.RelayModeRerank, channelcatalog.RelayFormatRerank, "gte-rerank-v2"),
	} {
		if invalid.Mode == channelcatalog.RelayModeRerank {
			invalid.IsStream = true
		}
		adaptor := initializedAdaptor(invalid)
		_, err := adaptor.GetRequestURL(invalid)
		require.Error(t, err)
		_, err = adaptor.ConvertRequest(invalid)
		require.Error(t, err)
	}

	badBase := aliMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "qwen-plus")
	badBase.BaseURL = "https://user:secret@dashscope.example/path"
	_, err := initializedAdaptor(badBase).GetRequestURL(badBase)
	require.ErrorContains(t, err, "credentials")
}

func TestAliTextEmbeddingResponsesAndRerankConversion(t *testing.T) {
	textMeta := aliMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "alias")
	textMeta.ModelName = "qwen-plus"
	textMeta.IsStream = true
	textMeta.Request = &protocolkit.GeneralOpenAIRequest{
		Model: "alias", Stream: true,
		Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		Extra: map[string]any{
			"model": "alias", "top_p": 1.0, "thinking_budget": 0.0,
			"enable_thinking": true, "provider_extension": "kept", "group": "dashboard-only",
		},
	}
	body, err := initializedAdaptor(textMeta).ConvertRequest(textMeta)
	require.NoError(t, err)
	var converted map[string]any
	require.NoError(t, json.Unmarshal(body, &converted))
	assert.Equal(t, "qwen-plus", converted["model"])
	assert.EqualValues(t, 0.99, converted["top_p"])
	assert.EqualValues(t, 0, converted["thinking_budget"])
	assert.Equal(t, true, converted["enable_thinking"])
	assert.Equal(t, true, converted["stream_options"].(map[string]any)["include_usage"])
	assert.Equal(t, "kept", converted["provider_extension"])
	assert.NotContains(t, converted, "group")

	textMeta.ModelName = "deepseek-r1"
	body, err = initializedAdaptor(textMeta).ConvertRequest(textMeta)
	require.NoError(t, err)
	converted = nil
	require.NoError(t, json.Unmarshal(body, &converted))
	assert.NotContains(t, converted, "thinking_budget")

	for _, test := range []struct {
		mode   channelcatalog.RelayMode
		format channelcatalog.RelayFormat
		extra  map[string]any
	}{
		{channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding, map[string]any{"model": "alias", "input": []any{"one", "two"}, "group": "drop"}},
		{channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses, map[string]any{"model": "alias", "input": "hello", "instructions": "brief", "group": "drop"}},
	} {
		meta := aliMeta(test.mode, test.format, "alias")
		meta.ModelName = "qwen-upstream"
		meta.Request.Extra = test.extra
		body, err := initializedAdaptor(meta).ConvertRequest(meta)
		require.NoError(t, err)
		converted = nil
		require.NoError(t, json.Unmarshal(body, &converted))
		assert.Equal(t, "qwen-upstream", converted["model"])
		assert.NotContains(t, converted, "group")
	}

	rerank := aliMeta(channelcatalog.RelayModeRerank, channelcatalog.RelayFormatRerank, "alias")
	rerank.ModelName = "gte-rerank-v2"
	rerank.Request.Extra = map[string]any{
		"query": "needle", "documents": []any{"one", map[string]any{"text": "two"}},
		"top_n": 0.0, "return_documents": false, "provider_extension": "drop",
	}
	body, err = initializedAdaptor(rerank).ConvertRequest(rerank)
	require.NoError(t, err)
	converted = nil
	require.NoError(t, json.Unmarshal(body, &converted))
	assert.Equal(t, "gte-rerank-v2", converted["model"])
	assert.Equal(t, "needle", converted["input"].(map[string]any)["query"])
	assert.EqualValues(t, 0, converted["parameters"].(map[string]any)["top_n"])
	assert.Equal(t, false, converted["parameters"].(map[string]any)["return_documents"])
	assert.NotContains(t, converted, "provider_extension")

	delete(rerank.Request.Extra, "return_documents")
	body, err = initializedAdaptor(rerank).ConvertRequest(rerank)
	require.NoError(t, err)
	converted = nil
	require.NoError(t, json.Unmarshal(body, &converted))
	assert.Equal(t, true, converted["parameters"].(map[string]any)["return_documents"])
}

func TestAliNativeClaudeRequestUsesMappedModel(t *testing.T) {
	meta := aliMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatClaude, "client-model")
	meta.ModelName = "qwen3-max"
	meta.RawBody = []byte(`{"model":"client-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"group":"drop"}`)
	body, err := initializedAdaptor(meta).ConvertRequest(meta)
	require.NoError(t, err)
	var converted map[string]any
	require.NoError(t, json.Unmarshal(body, &converted))
	assert.Equal(t, "qwen3-max", converted["model"])
	assert.EqualValues(t, 32, converted["max_tokens"])
	assert.NotContains(t, converted, "group")
}

func TestAliImageRequestConversion(t *testing.T) {
	sync := aliMeta(channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "qwen-image")
	n := 2
	sync.Request.N = &n
	sync.Request.Prompt = "blue square"
	sync.Request.Extra = map[string]any{
		"model": "qwen-image", "prompt": "blue square", "size": "1024x768",
		"n": 2.0, "watermark": true, "response_format": "url",
	}
	body, err := initializedAdaptor(sync).ConvertRequest(sync)
	require.NoError(t, err)
	var request map[string]any
	require.NoError(t, json.Unmarshal(body, &request))
	assert.Equal(t, "qwen-image", request["model"])
	parameters := request["parameters"].(map[string]any)
	assert.Equal(t, "1024*768", parameters["size"])
	assert.EqualValues(t, 2, parameters["n"])
	content := request["input"].(map[string]any)["messages"].([]any)[0].(map[string]any)["content"].([]any)
	assert.Equal(t, "blue square", content[0].(map[string]any)["text"])

	async := aliMeta(channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "wanx-v1")
	async.Request.Prompt = "cat"
	async.Request.Extra = map[string]any{
		"model": "wanx-v1", "prompt": "cat",
		"parameters": map[string]any{"n": 3.0, "prompt_extend": true},
	}
	body, err = initializedAdaptor(async).ConvertRequest(async)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &request))
	assert.Equal(t, "cat", request["input"].(map[string]any)["prompt"])
	assert.Equal(t, true, request["parameters"].(map[string]any)["prompt_extend"])

	async.Request.Extra["parameters"] = map[string]any{"n": float64(maxAliImageCount + 1)}
	_, err = initializedAdaptor(async).ConvertRequest(async)
	require.ErrorContains(t, err, "parameters.n")
}

func TestAliMultipartImageEditConversion(t *testing.T) {
	for _, test := range []struct {
		model string
		old   bool
	}{
		{model: "wanx-style-repaint-v1", old: true},
		{model: "qwen-image-edit", old: false},
	} {
		t.Run(test.model, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("model", test.model))
			require.NoError(t, writer.WriteField("prompt", "make it blue"))
			require.NoError(t, writer.WriteField("negative_prompt", "red"))
			part, err := writer.CreateFormFile("image", "sample.png")
			require.NoError(t, err)
			_, err = part.Write([]byte("fake-png-data"))
			require.NoError(t, err)
			require.NoError(t, writer.Close())

			meta := aliMeta(channelcatalog.RelayModeImagesEdits, channelcatalog.RelayFormatOpenAIImage, test.model)
			meta.RequestContentType = writer.FormDataContentType()
			meta.RawBody = body.Bytes()
			meta.Request.Prompt = "make it blue"
			meta.Request.Extra = map[string]any{"model": test.model, "prompt": "make it blue", "negative_prompt": "red"}
			converted, err := initializedAdaptor(meta).ConvertRequest(meta)
			require.NoError(t, err)
			var request map[string]any
			require.NoError(t, json.Unmarshal(converted, &request))
			input := request["input"].(map[string]any)
			if test.old {
				assert.Equal(t, "red", input["negative_prompt"])
				images := input["images"].([]any)
				require.Len(t, images, 1)
				assert.True(t, strings.HasPrefix(images[0].(string), "data:text/plain; charset=utf-8;base64,"))
			} else {
				contents := input["messages"].([]any)[0].(map[string]any)["content"].([]any)
				require.Len(t, contents, 2)
				assert.True(t, strings.HasPrefix(contents[0].(map[string]any)["image"].(string), "data:text/plain; charset=utf-8;base64,"))
				assert.Equal(t, "make it blue", contents[1].(map[string]any)["text"])
			}
		})
	}
}

func TestAliRerankResponseUsageErrorMappingAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := aliMeta(channelcatalog.RelayModeRerank, channelcatalog.RelayFormatRerank, "gte-rerank-v2")
	adaptor := initializedAdaptor(meta)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"output":{"results":[{"index":1,"relevance_score":0.9}]},"usage":{"total_tokens":17}}`,
	))}
	usage, err := adaptor.DoResponse(ctx, response, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 17, TotalTokens: 17}, usage)
	assert.Contains(t, recorder.Body.String(), `"relevance_score":0.9`)

	meta.Channel.StatusCodeMapping = `{"400":503}`
	response = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"code":"InvalidParameter","message":"bad request for ali-secret","request_id":"req-1"}`,
	))}
	_, err = adaptor.DoResponse(ctx, response, meta)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "InvalidParameter")

	response = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.LimitReader(
		strings.NewReader(strings.Repeat("x", int(relaycommon.MaxUpstreamJSONBodyBytes)+1)),
		relaycommon.MaxUpstreamJSONBodyBytes+1,
	))}
	_, err = adaptor.DoResponse(ctx, response, meta)
	require.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestAliCompatibleStreamIsBoundedAndExtractsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := aliMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "qwen-plus")
	meta.IsStream = true
	meta.Request.Stream = true
	adaptor := initializedAdaptor(meta)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\n" +
			"data: [DONE]\n\n",
	))}
	usage, err := adaptor.DoResponse(ctx, response, meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	oversized := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n\n"
	response = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(oversized))}
	_, err = adaptor.DoResponse(ctx, response, meta)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum event")
}

func TestAliAsyncImagePollAndB64FetchAreBoundedAndCredentialScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/gateway/api/v1/tasks/task-1":
			assert.Equal(t, "Bearer ali-secret", request.Header.Get("Authorization"))
			_, _ = io.WriteString(w, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","results":[{"url":"`+server.URL+`/image.png"}]},"usage":{"image_count":2}}`)
		case "/image.png":
			assert.Empty(t, request.Header.Get("Authorization"), "provider credentials must not be sent to result URLs")
			_, _ = w.Write([]byte("image-bytes"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	meta := aliMeta(channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "wanx-v1")
	meta.BaseURL = server.URL + "/gateway"
	meta.PromptTokens = 4
	meta.Request.Prompt = "cat"
	meta.Request.Extra = map[string]any{"model": "wanx-v1", "prompt": "cat", "response_format": "b64_json"}
	adaptor := initializedAdaptor(meta)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"output":{"task_id":"task-1","task_status":"PENDING"}}`,
	))}
	usage, err := adaptor.DoResponse(ctx, response, meta)
	require.NoError(t, err)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	assert.Equal(t, 2, usage.CompletionTokensDetails.ImageTokens)
	var clientResponse map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &clientResponse))
	data := clientResponse["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, "aW1hZ2UtYnl0ZXM=", data[0].(map[string]any)["b64_json"])
	assert.Contains(t, clientResponse, "metadata")

	transport, ok := adaptor.auxiliaryClient().Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	redirectRequest := httptest.NewRequest(http.MethodGet, server.URL, nil)
	assert.ErrorIs(t, adaptor.auxiliaryClient().CheckRedirect(redirectRequest, nil), http.ErrUseLastResponse)
}

type aliRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn aliRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestAliTaskPollingHonorsCancellationAndValidatesTaskID(t *testing.T) {
	meta := aliMeta(channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "wanx-v1")
	adaptor := initializedAdaptor(meta)
	var calls int
	adaptor.auxClient = &http.Client{Transport: aliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"output":{"task_status":"RUNNING"}}`)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := adaptor.waitForImageTask(ctx, meta, "task-1")
	require.ErrorIs(t, err, context.Canceled)
	assert.LessOrEqual(t, calls, 1)

	_, _, err = adaptor.waitForImageTask(context.Background(), meta, "../credential-leak")
	require.ErrorContains(t, err, "unsupported characters")
	assert.LessOrEqual(t, calls, 1)

	meta.Channel.StatusCodeMapping = `{"401":403}`
	calls = 0
	adaptor.auxClient = &http.Client{Transport: aliRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"code":"InvalidApiKey","message":"denied","request_id":"req-poll"}`)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	_, _, err = adaptor.waitForImageTask(context.Background(), meta, "task-2")
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusForbidden, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "InvalidApiKey")
	assert.Equal(t, 1, calls, "non-retryable polling errors must fail without sleeping or redispatching")
}
