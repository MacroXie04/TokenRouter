package newapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func TestAdaptorPreservesGatewayPathsAndDynamicModelCatalog(t *testing.T) {
	tests := []struct {
		mode constant.RelayMode
		path string
	}{
		{constant.RelayModeChatCompletions, "/v1/chat/completions"},
		{constant.RelayModeEmbeddings, "/v1/embeddings"},
		{constant.RelayModeImagesGenerations, "/v1/images/generations"},
		{constant.RelayModeResponses, "/v1/responses"},
		{constant.RelayModeResponsesCompact, "/v1/responses/compact"},
		{constant.RelayModeAlphaSearch, "/v1/alpha/search"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			meta := &relaycommon.Meta{
				Channel: &model.Channel{Type: int(constant.ChannelTypeNewAPI)},
				Mode:    test.mode, RequestPath: test.path, BaseURL: "https://gateway.example/proxy", APIKey: "secret",
				ModelName: "upstream-model", Request: &protocolkit.GeneralOpenAIRequest{Model: "client-model"},
			}
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, "https://gateway.example/proxy"+test.path, requestURL)
		})
	}
	assert.Empty(t, ModelList())
	models := ModelList()
	models = append(models, "mutation")
	assert.Empty(t, ModelList())
}

func TestAdaptorRejectsMismatchedAndUnsupportedGatewayPaths(t *testing.T) {
	for _, test := range []struct {
		mode constant.RelayMode
		path string
	}{
		{constant.RelayModeResponses, "/v1/chat/completions"},
		{constant.RelayModeRerank, "/v1/rerank"},
		{constant.RelayModeAudioSpeech, "/v1/audio/speech"},
	} {
		meta := &relaycommon.Meta{Mode: test.mode, RequestPath: test.path, BaseURL: "https://gateway.example"}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		require.Error(t, err)
	}
}

func TestGatewayHeadersRequireCredentialAndPreserveNativeAuth(t *testing.T) {
	tests := []struct {
		name       string
		format     constant.RelayFormat
		client     http.Header
		wantHeader map[string]string
	}{
		{
			name: "Claude defaults version", format: constant.RelayFormatClaude,
			wantHeader: map[string]string{"Authorization": "Bearer gateway-key", "x-api-key": "gateway-key", "anthropic-version": defaultAnthropicVersion},
		},
		{
			name: "Claude preserves bounded version", format: constant.RelayFormatClaude,
			client:     http.Header{"Anthropic-Version": []string{"2024-01-01"}},
			wantHeader: map[string]string{"Authorization": "Bearer gateway-key", "x-api-key": "gateway-key", "anthropic-version": "2024-01-01"},
		},
		{
			name: "Gemini dual auth", format: constant.RelayFormatGemini,
			wantHeader: map[string]string{"Authorization": "Bearer gateway-key", "x-goog-api-key": "gateway-key"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := &relaycommon.Meta{
				Channel: &model.Channel{Type: int(constant.ChannelTypeNewAPI)},
				Mode:    constant.RelayModeChatCompletions, Format: test.format, APIKey: "gateway-key", ClientHeaders: test.client,
			}
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			request, err := http.NewRequest(http.MethodPost, "https://gateway.example/v1/messages", strings.NewReader("{}"))
			require.NoError(t, err)
			require.NoError(t, adaptor.SetupRequestHeader(request, meta))
			for name, want := range test.wantHeader {
				assert.Equal(t, want, request.Header.Get(name))
			}
		})
	}

	meta := &relaycommon.Meta{Channel: &model.Channel{Type: int(constant.ChannelTypeNewAPI)}, Mode: constant.RelayModeChatCompletions}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	request, err := http.NewRequest(http.MethodPost, "https://gateway.example/v1/chat/completions", nil)
	require.NoError(t, err)
	require.Error(t, adaptor.SetupRequestHeader(request, meta))
}

func TestRequestURLCredentialAndHeaderValidation(t *testing.T) {
	requestURL, err := RequestURL("https://gateway.example/base/", "/v1/messages")
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.example/base/v1/messages", requestURL)

	requestURL, err = RequestURL("http://gateway.example", "/v1beta/models/gemini:streamGenerateContent?alt=sse")
	require.NoError(t, err)
	assert.Equal(t, "http://gateway.example/v1beta/models/gemini:streamGenerateContent?alt=sse", requestURL)

	badBases := []string{"", "ftp://gateway.example", "https://user:pass@gateway.example", "https://gateway.example?key=x", "https://gateway.example/#x", "https://gateway.example/a/../b"}
	for _, base := range badBases {
		_, err := RequestURL(base, "/v1/messages")
		assert.Error(t, err, base)
	}
	badPaths := []string{"", "v1/messages", "//evil.example/v1/messages", "/v1/../admin", "/v1/%2e%2e/admin", "/v1/messages?key=secret", "/v1/messages\\next"}
	for _, requestPath := range badPaths {
		_, err := RequestURL("https://gateway.example", requestPath)
		assert.Error(t, err, requestPath)
	}

	assert.Equal(t, "gateway-key", mustCredential(t, " gateway-key "))
	for _, credential := range []string{"", "with space", "line\nbreak", strings.Repeat("x", maxGatewayCredentialLen+1)} {
		_, err := ValidateCredential(credential)
		assert.Error(t, err)
	}
	assert.NoError(t, ValidateAnthropicVersion("2023-06-01"))
	assert.Error(t, ValidateAnthropicVersion("bad version"))
	assert.Error(t, ValidateAnthropicVersion(strings.Repeat("x", maxAnthropicVersionLen+1)))
}

func mustCredential(t *testing.T, raw string) string {
	t.Helper()
	credential, err := ValidateCredential(raw)
	require.NoError(t, err)
	return credential
}
