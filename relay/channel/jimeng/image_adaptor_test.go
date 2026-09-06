package jimeng

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func jimengImageMeta() *relaycommon.Meta {
	one := 1
	request := &protocolkit.GeneralOpenAIRequest{
		Model: "client-image-model", Prompt: "a fox in snow", N: &one,
		Extra: map[string]any{
			"model": "client-image-model", "prompt": "a fox in snow", "n": float64(1),
		},
	}
	return &relaycommon.Meta{
		Channel: &model.Channel{Type: int(constant.ChannelTypeJimeng)},
		Mode:    constant.RelayModeImagesGenerations, Format: constant.RelayFormatOpenAIImage,
		OriginalModelName: "client-image-model", ModelName: ImageModel,
		BaseURL: defaultImageBaseURL, APIKey: "access-example|secret-example",
		Request: request, RawBody: []byte(`{"model":"client-image-model","prompt":"a fox in snow","n":1}`),
		RequestContentType: "application/json", PromptTokens: 8,
	}
}

func TestJimengImageCredentialTransportRequiresHTTPSBase(t *testing.T) {
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	common.InitSSRF()

	_, err := validatedImageBaseURL("http://provider.example")
	require.ErrorContains(t, err, "must use HTTPS")
	base, err := validatedImageBaseURL("https://provider.example/base")
	require.NoError(t, err)
	require.Equal(t, "https://provider.example/base", base)
}

func newJimengImageContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	return context, recorder
}

func TestJimengImageModelCatalogIsExactAndOwned(t *testing.T) {
	models := ModelList()
	assert.Equal(t, []string{ImageModel}, models)
	models[0] = "mutated"
	assert.Equal(t, []string{ImageModel}, ModelList())
}

func TestJimengImageURLConversionAndDeterministicSigning(t *testing.T) {
	meta := jimengImageMeta()
	meta.Request.Extra["seed"] = float64(42)
	meta.Request.Extra["size"] = "512x768"
	meta.Request.Extra["use_pre_llm"] = false
	meta.Request.Extra["use_sr"] = true
	meta.Request.Extra["response_format"] = "url"
	meta.Request.Extra["logo_info"] = map[string]any{
		"add_logo": true, "position": float64(2), "language": float64(1), "opacity": 0.5, "logo_text_content": "TokenRouter",
	}
	meta.RawBody = []byte(`{"model":"client-image-model","prompt":"a fox in snow","n":1,"seed":42,"size":"512x768","use_pre_llm":false,"use_sr":true,"response_format":"url","logo_info":{"add_logo":true,"position":2,"language":1,"opacity":0.5,"logo_text_content":"TokenRouter"}}`)
	fixed := time.Date(2026, time.August, 14, 12, 34, 56, 0, time.UTC)
	adaptor := &Adaptor{Now: func() time.Time { return fixed }}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	parsedRequest, err := http.NewRequest(http.MethodPost, requestURL, nil)
	require.NoError(t, err)
	assert.Equal(t, "/", parsedRequest.URL.Path)
	assert.Equal(t, imageAction, parsedRequest.URL.Query().Get("Action"))
	assert.Equal(t, APIVersion, parsedRequest.URL.Query().Get("Version"))

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"req_key":"jimeng_high_aes_general_v21_L","prompt":"a fox in snow","seed":42,
		"width":512,"height":768,"use_pre_llm":false,"use_sr":true,"return_url":true,
		"logo_info":{"add_logo":true,"position":2,"language":1,"opacity":0.5,"logo_text_content":"TokenRouter"}
	}`, string(body))
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	assert.Equal(t, "20260814T123456Z", request.Header.Get("X-Date"))
	assertValidAuthorization(t, request, "access-example", "secret-example", fixed)
	assert.NotContains(t, request.Header.Get("Authorization"), "secret-example")

	gateway := jimengImageMeta()
	gateway.APIKey = "sk-gateway-token"
	gateway.BaseURL = "https://gateway.example/base"
	gatewayAdaptor := &Adaptor{}
	gatewayAdaptor.Init(gateway)
	gatewayURL, err := gatewayAdaptor.GetRequestURL(gateway)
	require.NoError(t, err)
	assert.Contains(t, gatewayURL, "https://gateway.example/base/jimeng/")
	gatewayBody, err := gatewayAdaptor.ConvertRequest(gateway)
	require.NoError(t, err)
	gatewayRequest, err := http.NewRequest(http.MethodPost, gatewayURL, bytes.NewReader(gatewayBody))
	require.NoError(t, err)
	require.NoError(t, gatewayAdaptor.SetupRequestHeader(gatewayRequest, gateway))
	assert.Equal(t, "Bearer sk-gateway-token", gatewayRequest.Header.Get("Authorization"))
	assert.Empty(t, gatewayRequest.Header.Get("X-Date"))
}

func TestJimengImageConversionValidatesProviderOptionsAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"wrong mode", func(meta *relaycommon.Meta) { meta.Mode = constant.RelayModeChatCompletions }},
		{"wrong format", func(meta *relaycommon.Meta) { meta.Format = constant.RelayFormatOpenAI }},
		{"wrong model", func(meta *relaycommon.Meta) { meta.ModelName = "jimeng-other" }},
		{"bad credential", func(meta *relaycommon.Meta) { meta.APIKey = "access-only" }},
		{"base credentials", func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@visual.example" }},
		{"empty prompt", func(meta *relaycommon.Meta) { meta.Request.Prompt = ""; meta.Request.Extra["prompt"] = "" }},
		{"multiple count", func(meta *relaycommon.Meta) { count := 2; meta.Request.N = &count }},
		{"bad response format", func(meta *relaycommon.Meta) { meta.Request.Extra["response_format"] = "binary" }},
		{"bad size", func(meta *relaycommon.Meta) { meta.Request.Extra["size"] = "1024x1024" }},
		{"conflicting size", func(meta *relaycommon.Meta) {
			meta.Request.Extra["size"] = "512x512"
			meta.Request.Extra["width"] = float64(768)
			meta.Request.Extra["height"] = float64(512)
		}},
		{"seed overflow", func(meta *relaycommon.Meta) { meta.Request.Extra["seed"] = float64(math.MaxInt32) + 1 }},
		{"invalid base64", func(meta *relaycommon.Meta) { meta.Request.Extra["binary_data_base64"] = []any{"not-base64"} }},
		{"invalid image url", func(meta *relaycommon.Meta) { meta.Request.Extra["image_urls"] = []any{"file:///tmp/image"} }},
		{"invalid logo", func(meta *relaycommon.Meta) {
			meta.Request.Extra["logo_info"] = map[string]any{"opacity": 2.0}
		}},
		{"unknown field", func(meta *relaycommon.Meta) { meta.Request.Extra["credential"] = "must-not-cross" }},
		{"duplicate input key", func(meta *relaycommon.Meta) {
			meta.RawBody = []byte(`{"model":"one","model":"two","prompt":"fox"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := jimengImageMeta()
			test.mutate(meta)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			_, urlErr := adaptor.GetRequestURL(meta)
			_, bodyErr := adaptor.ConvertRequest(meta)
			assert.True(t, urlErr != nil || bodyErr != nil)
		})
	}
}

func TestJimengImageResponseConversionAndAmbiguityClassification(t *testing.T) {
	meta := jimengImageMeta()
	adaptor := &Adaptor{Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	adaptor.Init(meta)
	encodedImage := base64.StdEncoding.EncodeToString([]byte("image-bytes"))
	context, recorder := newJimengImageContext()
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"code":10000,"message":"success","data":{"binary_data_base64":["` + encodedImage + `"],"image_urls":["https://cdn.example/image.png"]}}`,
	))}
	usage, err := adaptor.DoResponse(context, response, meta)
	require.NoError(t, err)
	assert.Nil(t, usage)
	var output openAIImageResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &output))
	assert.Equal(t, int64(1_700_000_000), output.Created)
	require.Len(t, output.Data, 2)
	assert.Equal(t, encodedImage, output.Data[0].B64JSON)
	assert.Equal(t, "https://cdn.example/image.png", output.Data[1].URL)

	context, _ = newJimengImageContext()
	rejected := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"code":50400,"message":"secret-example rejected"}`,
	))}
	usage, err = adaptor.DoResponse(context, rejected, meta)
	require.Error(t, err)
	assert.Nil(t, usage, "an explicit provider rejection is refundable")
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusBadRequest, upstream.StatusCode)

	for name, body := range map[string]string{
		"malformed":      `{`,
		"duplicate":      `{"code":10000,"code":50400,"data":{}}`,
		"missing code":   `{"data":{"image_urls":["https://cdn.example/image.png"]}}`,
		"missing output": `{"code":10000,"data":{}}`,
		"invalid output": `{"code":10000,"data":{"image_urls":["file:///tmp/image"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			context, recorder := newJimengImageContext()
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			usage, err := adaptor.DoResponse(context, response, meta)
			require.Error(t, err)
			require.NotNil(t, usage, "an accepted ambiguous response must remain billable")
			assert.Positive(t, usage.TotalTokens)
			assert.Empty(t, recorder.Body.String())
		})
	}
}

func TestJimengImageResponseAndRequestAggregateLimits(t *testing.T) {
	meta := jimengImageMeta()
	meta.Request.Extra["binary_data_base64"] = []any{
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'a'}, MaxImageBytes)),
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'b'}, MaxImageBytes)),
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'c'}, MaxImageBytes)),
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'d'}, MaxImageBytes)),
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	_, err := adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "aggregate")

	context, _ := newJimengImageContext()
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"code":10000,"data":{"binary_data_base64":["not-base64"]}}`)),
	}
	usage, err := adaptor.DoResponse(context, response, jimengImageMeta())
	require.Error(t, err)
	require.NotNil(t, usage)
}
