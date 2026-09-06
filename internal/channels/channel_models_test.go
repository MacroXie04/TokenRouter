package channels

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchUpstreamModelsUsesProviderContractAndLeastCredential(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("CODEX_CLIENT_VERSION", "1.2.3")
	httpx.InitSSRF()

	tests := []struct {
		name        string
		channel     model.Channel
		wantPath    string
		wantQuery   string
		wantHeader  string
		wantValue   string
		response    string
		wantModels  []string
		forbidden   []string
		assertExtra func(*testing.T, *http.Request)
	}{
		{
			name: "OpenAI current multi-key",
			channel: model.Channel{Type: int(channelcatalog.ChannelTypeOpenAI), Key: "disabled-key\nenabled-key",
				ChannelInfo: `{"is_multi_key":true,"multi_key_size":2,"multi_key_status_list":{"0":2,"1":1}}`},
			wantPath: "/v1/models", wantHeader: "Authorization", wantValue: "Bearer enabled-key",
			response: `{"data":[{"id":"gpt-4"},{"id":"gpt-4"},{"id":""}]}`, wantModels: []string{"gpt-4"},
			forbidden: []string{"disabled-key", "\n"},
		},
		{
			name: "Anthropic", channel: model.Channel{Type: int(channelcatalog.ChannelTypeAnthropic), Key: "anthropic-key"},
			wantPath: "/v1/models", wantHeader: "x-api-key", wantValue: "anthropic-key",
			response: `{"data":[{"id":"claude-3"}]}`, wantModels: []string{"claude-3"},
			assertExtra: func(t *testing.T, request *http.Request) {
				assert.Equal(t, "2023-06-01", request.Header.Get("anthropic-version"))
				assert.Empty(t, request.Header.Get("Authorization"))
			},
		},
		{
			name: "Gemini", channel: model.Channel{Type: int(channelcatalog.ChannelTypeGemini), Key: "gemini-key"},
			wantPath: "/v1beta/models", wantHeader: "x-goog-api-key", wantValue: "gemini-key",
			response:   `{"models":[{"name":"models/gemini-pro"},{"name":"models/gemini-pro"},{"name":""}]}`,
			wantModels: []string{"gemini-pro"},
		},
		{
			name: "Azure", channel: model.Channel{Type: int(channelcatalog.ChannelTypeAzure), Key: "azure-key", Other: "2025-04-01-preview"},
			wantPath: "/openai/models", wantQuery: "api-version=2025-04-01-preview",
			wantHeader: "api-key", wantValue: "azure-key",
			response: `{"data":[{"id":"deployment-a"}]}`, wantModels: []string{"deployment-a"},
			assertExtra: func(t *testing.T, request *http.Request) { assert.Empty(t, request.Header.Get("Authorization")) },
		},
		{
			name: "Codex",
			channel: model.Channel{Type: int(channelcatalog.ChannelTypeCodex),
				Key: `{"access_token":"codex-access","account_id":"acct-1","refresh_token":"codex-refresh"}`},
			wantPath: "/backend-api/codex/models", wantQuery: "client_version=1.2.3",
			wantHeader: "Authorization", wantValue: "Bearer codex-access",
			response:   `{"models":[{"slug":"gpt-5-codex"},{"slug":"gpt-5-codex"},{"slug":""}]}`,
			wantModels: []string{"gpt-5-codex"}, forbidden: []string{"codex-refresh", "refresh_token", `{"access_token"`},
			assertExtra: func(t *testing.T, request *http.Request) {
				assert.Equal(t, "acct-1", request.Header.Get("chatgpt-account-id"))
				assert.Equal(t, "codex-cli/1.2.3", request.Header.Get("User-Agent"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.wantPath, request.URL.Path)
				assert.Equal(t, test.wantQuery, request.URL.RawQuery)
				assert.Equal(t, test.wantValue, request.Header.Get(test.wantHeader))
				wire := request.URL.String() + " " + request.Header.Get("Authorization") + " " +
					request.Header.Get("x-api-key") + " " + request.Header.Get("x-goog-api-key") + " " +
					request.Header.Get("chatgpt-account-id")
				for _, secret := range test.forbidden {
					assert.NotContains(t, wire, secret)
				}
				if test.assertExtra != nil {
					test.assertExtra(t, request)
				}
				response.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(response, test.response)
				require.NoError(t, err)
			}))
			defer upstream.Close()
			test.channel.BaseURL = upstream.URL

			models, err := FetchUpstreamModelsForChannel(&test.channel)
			require.NoError(t, err)
			assert.Equal(t, test.wantModels, models)
		})
	}
}

func TestFetchUpstreamModelsFailsClosedForUnsupportedProvider(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()

	_, err := FetchUpstreamModelsForChannel(&model.Channel{
		Type: int(channelcatalog.ChannelTypeJimeng), BaseURL: upstream.URL,
		Key: "jimeng-secret", Models: "jimeng-video",
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "not supported")
	assert.Zero(t, calls.Load())
}
