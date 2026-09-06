package codex

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"testing"
)

func codexMeta(mode channelcatalog.RelayMode) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(channelcatalog.ChannelTypeCodex)},
		Mode:      mode,
		ModelName: "gpt-5-codex",
		APIKey:    `{"access_token":"access-secret","account_id":"acct-123","refresh_token":"must-not-forward"}`,
		Request: &protocolkit.GeneralOpenAIRequest{Extra: map[string]any{
			"model": "client-model", "input": "hello", "max_output_tokens": 128,
			"temperature": 1.0, "frequency_penalty": 0.5, "presence_penalty": 0.5, "group": "dashboard-only",
		}},
	}
}

func TestCodexResponsesContract(t *testing.T) {
	meta := codexMeta(channelcatalog.RelayModeResponses)
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://chatgpt.com/backend-api/codex/responses", requestURL)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(body, &decoded))
	assert.Equal(t, "gpt-5-codex", decoded["model"])
	assert.Equal(t, "", decoded["instructions"])
	assert.Equal(t, false, decoded["store"])
	assert.NotContains(t, decoded, "max_output_tokens")
	assert.NotContains(t, decoded, "temperature")
	assert.NotContains(t, decoded, "frequency_penalty")
	assert.NotContains(t, decoded, "presence_penalty")
	assert.NotContains(t, decoded, "group")

	req, err := http.NewRequest(http.MethodPost, requestURL, nil)
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Equal(t, "Bearer access-secret", req.Header.Get("Authorization"))
	assert.Equal(t, "acct-123", req.Header.Get("chatgpt-account-id"))
	assert.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
	assert.Equal(t, "codex_cli_rs", req.Header.Get("originator"))
	assert.NotContains(t, req.Header.Get("Authorization"), "refresh_token")
}

func TestCodexModeAndCredentialValidation(t *testing.T) {
	meta := codexMeta(channelcatalog.RelayModeChatCompletions)
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	_, err := adaptor.GetRequestURL(meta)
	assert.Error(t, err)

	meta.Mode = channelcatalog.RelayModeResponses
	adaptor.Init(meta)
	meta.APIKey = "ordinary-api-key"
	req, err := http.NewRequest(http.MethodPost, defaultBaseURL, nil)
	require.NoError(t, err)
	err = adaptor.SetupRequestHeader(req, meta)
	require.Error(t, err)
	assert.Empty(t, req.Header.Get("Authorization"))

	meta.APIKey = `{"access_token":"access-secret"}`
	err = adaptor.SetupRequestHeader(req, meta)
	require.Error(t, err)
	assert.Empty(t, req.Header.Get("Authorization"), "partial credentials must not be installed before validation succeeds")
}

func TestCodexCompactAndAlphaSearchPaths(t *testing.T) {
	for _, test := range []struct {
		mode channelcatalog.RelayMode
		path string
	}{
		{channelcatalog.RelayModeResponsesCompact, "/backend-api/codex/responses/compact"},
		{channelcatalog.RelayModeAlphaSearch, "/backend-api/codex/alpha/search"},
	} {
		meta := codexMeta(test.mode)
		meta.BaseURL = "https://codex-proxy.example/"
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		requestURL, err := adaptor.GetRequestURL(meta)
		require.NoError(t, err)
		assert.Equal(t, "https://codex-proxy.example"+test.path, requestURL)
	}
}
