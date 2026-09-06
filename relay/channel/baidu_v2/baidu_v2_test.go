package baidu_v2

import (
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
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func v2Meta() *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(constant.ChannelTypeBaiduV2)},
		Mode:      constant.RelayModeChatCompletions,
		Format:    constant.RelayFormatOpenAI,
		ModelName: "ernie-4.0-turbo-8k",
		BaseURL:   "https://qianfan.baidubce.com",
		APIKey:    "authorization-token|application-id",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "client-model", Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
			Extra: map[string]any{"model": "client-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}},
		},
	}
}

func TestBaiduV2URLHeadersCatalogAndFailClosedModes(t *testing.T) {
	meta := v2Meta()
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://qianfan.baidubce.com/v2/chat/completions", requestURL)
	req, err := http.NewRequest(http.MethodPost, requestURL, nil)
	require.NoError(t, err)
	req.Header.Set("x-api-key", "must-remove")
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Equal(t, "Bearer authorization-token", req.Header.Get("Authorization"))
	assert.Equal(t, "application-id", req.Header.Get("appid"))
	assert.Empty(t, req.Header.Get("x-api-key"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", req.Header.Get("Accept"))

	meta.APIKey = "authorization-token"
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Empty(t, req.Header.Get("appid"))
	meta.APIKey = "|appid"
	assert.ErrorContains(t, adaptor.SetupRequestHeader(req, meta), "authorization token")
	meta.APIKey = "token\r\n|appid"
	assert.ErrorContains(t, adaptor.SetupRequestHeader(req, meta), "invalid")

	for _, mode := range []constant.RelayMode{
		constant.RelayModeEmbeddings,
		constant.RelayModeImagesGenerations,
		constant.RelayModeImagesEdits,
		constant.RelayModeRerank,
		constant.RelayModeAudioSpeech,
	} {
		unsupported := v2Meta()
		unsupported.Mode = mode
		unsupported.Format = relaycommon.GetRelayFormat(constant.ChannelTypeBaiduV2, mode)
		adaptor.Init(unsupported)
		_, err = adaptor.GetRequestURL(unsupported)
		assert.ErrorContains(t, err, "does not support", mode)
		_, err = adaptor.ConvertRequest(unsupported)
		assert.ErrorContains(t, err, "does not support", mode)
	}

	badBase := v2Meta()
	badBase.BaseURL = "https://user:password@qianfan.example"
	adaptor.Init(badBase)
	_, err = adaptor.GetRequestURL(badBase)
	assert.ErrorContains(t, err, "must not contain credentials")

	models := ModelList()
	require.Contains(t, models, "ernie-4.0-8k-latest")
	require.Contains(t, models, "deepseek-r1")
	models[0] = "mutated"
	assert.Equal(t, "ernie-4.0-8k-latest", ModelList()[0])
}

func TestBaiduV2RequestConversionSearchMappingAndBounds(t *testing.T) {
	meta := v2Meta()
	meta.ModelName = "ernie-4.0-turbo-8k-search"
	meta.Request.Extra["group"] = "dashboard-only"
	meta.Request.Extra["provider_extension"] = "preserve"
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, "ernie-4.0-turbo-8k", decoded["model"])
	assert.Equal(t, "preserve", decoded["provider_extension"])
	assert.NotContains(t, decoded, "group")
	assert.Equal(t, map[string]any{
		"enable": true, "enable_citation": true, "enable_trace": true, "enable_status": false,
	}, decoded["web_search"])
	assert.Equal(t, "client-model", meta.Request.Model)
	assert.NotContains(t, meta.Request.Extra, "web_search", "conversion must not mutate the accounting/retry snapshot")

	meta.Request.Extra["web_search"] = map[string]any{"enable": false}
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, map[string]any{"enable": false}, decoded["web_search"])

	meta.ModelName = "ernie-4.0-turbo-8k"
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, "ernie-4.0-turbo-8k", decoded["model"])

	meta.RawBody = make([]byte, maxBaiduV2RequestBodyBytes+1)
	_, err = adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "exceeds")

	meta = v2Meta()
	meta.IsStream = true
	meta.Request.Stream = false
	adaptor.Init(meta)
	_, err = adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "stream flag")
}

func TestBaiduV2OpenAIResponsesUsageStreamAndErrorMapping(t *testing.T) {
	meta := v2Meta()
	meta.Channel.StatusCodeMapping = `{"429":503}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
		`{"id":"qianfan-1","object":"chat.completion","model":"ernie-4.0-turbo-8k","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10}}`,
	))}
	usage, err := adaptor.DoResponse(ctx, response, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}, usage)
	assert.Contains(t, recorder.Body.String(), "qianfan-1")

	meta.IsStream = true
	meta.Request.Stream = true
	adaptor.Init(meta)
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	response = &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
		"data: {\"id\":\"qianfan-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\n" +
			"data: [DONE]\n\n",
	))}
	usage, err = adaptor.DoResponse(ctx, response, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12}, usage)
	assert.Contains(t, recorder.Body.String(), `"content":"hi"`)

	meta.IsStream = false
	meta.Request.Stream = false
	adaptor.Init(meta)
	ctx, _ = gin.CreateTestContext(httptest.NewRecorder())
	response = &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
		`{"error":{"message":"busy for authorization-token","type":"rate_limit","code":"quota"}}`,
	))}
	_, err = adaptor.DoResponse(ctx, response, meta)
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
}
