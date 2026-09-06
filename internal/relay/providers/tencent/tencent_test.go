package tencent

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testNativeCredential = "1300000000|AKIDexample|secret-example"

func tencentMeta(credential string) *relaycommon.Meta {
	request := &protocolkit.GeneralOpenAIRequest{
		Model: "client-model",
		Messages: []protocolkit.Message{
			{Role: "system", Content: "follow the rules"},
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "prior answer"},
			{Role: "user", Content: "continue"},
		},
	}
	return &relaycommon.Meta{
		Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeTencent)},
		Mode:    channelcatalog.RelayModeChatCompletions, Format: channelcatalog.RelayFormatOpenAI,
		OriginalModelName: "client-model", ModelName: "hunyuan-pro",
		BaseURL: defaultNativeBaseURL, APIKey: credential,
		Request: request, PromptTokens: 9,
	}
}

func newTencentContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return context, recorder
}

func TestTencentModelCatalogIsExactAndOwned(t *testing.T) {
	expected := []string{"hunyuan-lite", "hunyuan-standard", "hunyuan-standard-256K", "hunyuan-pro"}
	models := ModelList()
	assert.Equal(t, expected, models)
	models[0] = "mutated"
	assert.Equal(t, expected, ModelList())
}

func TestTencentDispatchesTokenHubAndNativeCredentials(t *testing.T) {
	tokenMeta := tencentMeta("tokenhub-key")
	tokenAdaptor := &Adaptor{}
	tokenAdaptor.Init(tokenMeta)
	tokenURL, err := tokenAdaptor.GetRequestURL(tokenMeta)
	require.NoError(t, err)
	assert.Equal(t, tokenHubBaseURL+"/v1/chat/completions", tokenURL)
	tokenBody, err := tokenAdaptor.ConvertRequest(tokenMeta)
	require.NoError(t, err)
	var tokenPayload map[string]any
	require.NoError(t, json.Unmarshal(tokenBody, &tokenPayload))
	assert.Equal(t, "hunyuan-pro", tokenPayload["model"])
	tokenRequest, err := http.NewRequest(http.MethodPost, tokenURL, bytes.NewReader(tokenBody))
	require.NoError(t, err)
	require.NoError(t, tokenAdaptor.SetupRequestHeader(tokenRequest, tokenMeta))
	assert.Equal(t, "Bearer tokenhub-key", tokenRequest.Header.Get("Authorization"))
	assert.Empty(t, tokenRequest.Header.Get("X-TC-Action"))

	nativeMeta := tencentMeta(testNativeCredential)
	nativeAdaptor := &Adaptor{}
	nativeAdaptor.Init(nativeMeta)
	nativeURL, err := nativeAdaptor.GetRequestURL(nativeMeta)
	require.NoError(t, err)
	assert.Equal(t, defaultNativeBaseURL+"/", nativeURL)
	nativeBody, err := nativeAdaptor.ConvertRequest(nativeMeta)
	require.NoError(t, err)
	assert.Contains(t, string(nativeBody), `"Model":"hunyuan-pro"`)
	assert.Contains(t, string(nativeBody), `"Messages"`)
	assert.NotContains(t, string(nativeBody), "AKIDexample")

	invalidToken := tencentMeta(" tokenhub-key ")
	invalidAdaptor := &Adaptor{}
	invalidAdaptor.Init(invalidToken)
	_, err = invalidAdaptor.GetRequestURL(invalidToken)
	assert.Error(t, err)
}

func TestTencentNativeRequestConversionAndDeterministicTC3Signature(t *testing.T) {
	temperature := 0.8
	topP := 0.7
	meta := tencentMeta(testNativeCredential)
	meta.Request.Temperature = &temperature
	meta.Request.TopP = &topP
	fixed := time.Unix(1_735_785_845, 0).UTC()
	adaptor := &Adaptor{Now: func() time.Time { return fixed }}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"Model":"hunyuan-pro",
		"Messages":[
			{"Role":"system","Content":"follow the rules"},
			{"Role":"user","Content":"hello"},
			{"Role":"assistant","Content":"prior answer"},
			{"Role":"user","Content":"continue"}
		],
		"Stream":false,"TopP":0.7,"Temperature":0.8
	}`, string(body))
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "ChatCompletions", request.Header.Get("X-TC-Action"))
	assert.Equal(t, "2023-09-01", request.Header.Get("X-TC-Version"))
	assert.Equal(t, "1735785845", request.Header.Get("X-TC-Timestamp"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	assert.Equal(t,
		"TC3-HMAC-SHA256 Credential=AKIDexample/2025-01-02/hunyuan/tc3_request, SignedHeaders=content-type;host;x-tc-action, Signature=56409070f9a992e5692eee4bc142492d162474b548f21553b46d4b7813e87387",
		request.Header.Get("Authorization"),
	)
	assert.NotContains(t, request.Header.Get("Authorization"), "secret-example")
}

func TestTencentNativeValidationRejectsAmbiguousOrUnsupportedInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"credential segment count", func(meta *relaycommon.Meta) { meta.APIKey = "app|secret" }},
		{"non-positive app id", func(meta *relaycommon.Meta) { meta.APIKey = "0|secret-id|secret-key" }},
		{"credential control", func(meta *relaycommon.Meta) { meta.APIKey = "1|secret-id|secret\nkey" }},
		{"base credentials", func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@hunyuan.example" }},
		{"base query", func(meta *relaycommon.Meta) { meta.BaseURL = "https://hunyuan.example?key=value" }},
		{"native base path", func(meta *relaycommon.Meta) { meta.BaseURL = "https://hunyuan.example/proxy" }},
		{"too many messages", func(meta *relaycommon.Meta) {
			meta.Request.Messages = make([]protocolkit.Message, maxMessages+1)
		}},
		{"wrong alternation", func(meta *relaycommon.Meta) {
			meta.Request.Messages = []protocolkit.Message{{Role: "user", Content: "one"}, {Role: "user", Content: "two"}}
		}},
		{"ends with assistant", func(meta *relaycommon.Meta) {
			meta.Request.Messages = []protocolkit.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}}
		}},
		{"multimodal", func(meta *relaycommon.Meta) {
			meta.Request.Messages = []protocolkit.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid"}}}}}
		}},
		{"temperature nan", func(meta *relaycommon.Meta) {
			value := math.NaN()
			meta.Request.Temperature = &value
		}},
		{"top p range", func(meta *relaycommon.Meta) {
			value := 1.1
			meta.Request.TopP = &value
		}},
		{"tools", func(meta *relaycommon.Meta) {
			meta.Request.Tools = []protocolkit.ToolCallRequest{{Type: "function"}}
		}},
		{"unknown extension", func(meta *relaycommon.Meta) {
			meta.Request.Extra = map[string]any{"unknown": true}
		}},
		{"stream mismatch", func(meta *relaycommon.Meta) { meta.Request.Stream = true }},
		{"wrong mode", func(meta *relaycommon.Meta) { meta.Mode = channelcatalog.RelayModeEmbeddings }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := tencentMeta(testNativeCredential)
			test.mutate(meta)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			_, urlErr := adaptor.GetRequestURL(meta)
			_, bodyErr := adaptor.ConvertRequest(meta)
			assert.Error(t, errorsJoin(urlErr, bodyErr))
		})
	}
}

func errorsJoin(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func TestTencentNonStreamResponseConversionAndAccountingEvidence(t *testing.T) {
	meta := tencentMeta(testNativeCredential)
	adaptor := &Adaptor{Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	adaptor.Init(meta)
	context, recorder := newTencentContext()
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(`{
			"Response":{
				"Choices":[{"FinishReason":"stop","Message":{"Role":"assistant","Content":"answer"}}],
				"Created":1699999999,"Id":"chat-1",
				"Usage":{"PromptTokens":7,"CompletionTokens":3,"TotalTokens":10}
			}
		}`)),
	}
	usage, err := adaptor.DoResponse(context, response, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}, usage)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var output protocolkit.ChatCompletionsResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, "chat-1", output.Id)
	assert.Equal(t, "client-model", output.Model)
	require.Len(t, output.Choices, 1)
	assert.Equal(t, "answer", output.Choices[0].Message.Content)
}

func TestTencentUsageNormalizesTotalsWithoutUnderbilling(t *testing.T) {
	meta := tencentMeta(testNativeCredential)
	usage, err := normalizedUsage(tencentUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 10}, meta, "answer")
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}, usage)

	usage, err = normalizedUsage(tencentUsage{PromptTokens: 5, CompletionTokens: 2}, meta, "answer")
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, usage)
}

func TestTencentProviderRejectionIsRefundableButAmbiguousSuccessIsBillable(t *testing.T) {
	meta := tencentMeta(testNativeCredential)
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	context, _ := newTencentContext()
	rejected := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"Response":{"Error":{"Code":4001,"Message":"secret-example was rejected"}}}`,
	))}
	usage, err := adaptor.DoResponse(context, rejected, meta)
	require.Error(t, err)
	assert.Nil(t, usage, "a definitive provider rejection is safe to refund")
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusBadRequest, upstream.StatusCode)

	for name, body := range map[string]string{
		"malformed":     `{`,
		"duplicate":     `{"Response":{"Choices":[]},"Response":{"Choices":[]}}`,
		"invalid usage": `{"Response":{"Choices":[{"Message":{"Content":"accepted"}}],"Usage":{"PromptTokens":2147483647,"CompletionTokens":1,"TotalTokens":2147483647}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			context, recorder := newTencentContext()
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			usage, err := adaptor.DoResponse(context, response, meta)
			require.Error(t, err)
			require.NotNil(t, usage, "an accepted but ambiguous response must settle before surfacing its error")
			assert.Positive(t, usage.TotalTokens)
			assert.Empty(t, recorder.Body.String())
		})
	}
}

func TestTencentStreamConversionUsageAndBounds(t *testing.T) {
	meta := tencentMeta(testNativeCredential)
	meta.IsStream = true
	meta.Request.Stream = true
	adaptor := &Adaptor{Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	adaptor.Init(meta)
	context, recorder := newTencentContext()
	stream := strings.Join([]string{
		`data: {"Choices":[{"Delta":{"Role":"assistant","Content":"hello "}}],"Created":1699999999,"Id":"chat-1"}`,
		"",
		`data: {"Choices":[{"FinishReason":"stop","Delta":{"Content":"world"}}],"Created":1699999999,"Id":"chat-1","Usage":{"PromptTokens":6,"CompletionTokens":2,"TotalTokens":8}}`,
		"",
	}, "\n")
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream))}
	usage, err := adaptor.DoResponse(context, response, meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 6, CompletionTokens: 2, TotalTokens: 8}, usage)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), `"content":"hello "`)
	assert.Contains(t, recorder.Body.String(), `"content":"world"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))

	context, _ = newTencentContext()
	oversized := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	response = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(oversized))}
	usage, err = adaptor.DoResponse(context, response, meta)
	require.Error(t, err)
	require.NotNil(t, usage)
	assert.Positive(t, usage.TotalTokens)
}

func TestTencentTokenHubMalformedAcceptedResponseRetainsUsage(t *testing.T) {
	meta := tencentMeta("tokenhub-key")
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, _ := newTencentContext()
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":`))}
	usage, err := adaptor.DoResponse(context, response, meta)
	require.Error(t, err)
	require.NotNil(t, usage)
	assert.Positive(t, usage.TotalTokens)
}
