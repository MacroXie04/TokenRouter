package mokaai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func mokaMeta() *relaycommon.Meta {
	return &relaycommon.Meta{
		Mode: constant.RelayModeEmbeddings, Format: constant.RelayFormatEmbedding,
		ModelName: "m3e-large", BaseURL: "https://moka.example/root", APIKey: "moka-secret",
		Request: &protocolkit.GeneralOpenAIRequest{Extra: map[string]any{"input": "hello"}},
	}
}

func TestMokaAIEmbeddingURLHeadersAndRequest(t *testing.T) {
	meta := mokaMeta()
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://moka.example/root/embeddings", requestURL)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{"input":["hello"],"model":"m3e-large"}`, string(body))

	request := httptest.NewRequest(http.MethodPost, requestURL, nil)
	request.Header.Set("x-api-key", "must-remove")
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "Bearer moka-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", request.Header.Get("Accept"))
	assert.Empty(t, request.Header.Get("x-api-key"))
}

func TestMokaAIFailsClosedOnUnsupportedInputsAndModes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*relaycommon.Meta)
	}{
		{"chat", func(meta *relaycommon.Meta) { meta.Mode = constant.RelayModeChatCompletions }},
		{"wrong model", func(meta *relaycommon.Meta) { meta.ModelName = "other-model" }},
		{"empty array", func(meta *relaycommon.Meta) { meta.Request.Extra["input"] = []any{} }},
		{"numeric token input", func(meta *relaycommon.Meta) { meta.Request.Extra["input"] = []any{1.0} }},
		{"oversized item", func(meta *relaycommon.Meta) { meta.Request.Extra["input"] = strings.Repeat("x", maxInputItemBytes+1) }},
		{"credential URL", func(meta *relaycommon.Meta) { meta.BaseURL = "https://user:pass@moka.example" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := mokaMeta()
			test.mutate(meta)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			if test.name == "credential URL" {
				_, err := adaptor.GetRequestURL(meta)
				require.Error(t, err)
				return
			}
			_, err := adaptor.ConvertRequest(meta)
			require.Error(t, err)
		})
	}
}

func TestMokaAIEmbeddingResponseConversionAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := mokaMeta()
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"object":"list","model":"m3e-large","data":[{"object":"embedding","index":0,"embedding":[0.25,-0.5]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`,
		)),
	}
	usage, err := adaptor.DoResponse(context, response, meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 7, usage.TotalTokens)
	assert.JSONEq(t,
		`{"object":"list","model":"baidu-embedding","data":[{"object":"embedding","index":0,"embedding":[0.25,-0.5]}],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7}}`,
		recorder.Body.String(),
	)

	for _, body := range []string{
		`not-json`,
		`{"error":{"message":"failed"}}`,
		`{"data":[{"index":-1,"embedding":[1]}]}`,
	} {
		bad := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
		_, err := adaptor.DoResponse(httptestContext(), bad, meta)
		require.Error(t, err)
	}
}

func httptestContext() *gin.Context {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	return context
}

func TestMokaAIModelCatalogIsOwned(t *testing.T) {
	models := ModelList()
	require.Equal(t, []string{"m3e-large", "m3e-base", "m3e-small"}, models)
	models[0] = "changed"
	assert.Equal(t, "m3e-large", ModelList()[0])
}
