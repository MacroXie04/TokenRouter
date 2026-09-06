package channels

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func enableUnsafeModelDiscoveryForTest(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	t.Cleanup(httpx.InitSSRF)
}

func TestDifyDoesNotAdvertiseAnUpstreamModelCatalog(t *testing.T) {
	assert.False(t, channelSupportsUpstreamModelDiscovery(channelcatalog.ChannelTypeDify),
		"Dify applications own their model and do not expose the OpenAI /v1/models contract")
}

func advancedCustomDiscoverySettings(upstreamPath string, auth string) string {
	return `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/models","upstream_path":` +
		quoteJSONString(upstreamPath) + `,"converter":"none"` + auth + `}]}}`
}

func quoteJSONString(value string) string {
	encoded, _ := jsonutil.Marshal(value)
	return string(encoded)
}

func TestAdvancedCustomModelDiscoveryUsesRouteAuthAndFinalHeaderOverride(t *testing.T) {
	enableUnsafeModelDiscoveryForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/provider/models", r.URL.Path)
		assert.Equal(t, "models.internal", r.Host)
		assert.Equal(t, "override secret-key", r.Header.Get("x-api-key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[{"id":" alpha "},{"id":"alpha"},{"id":"beta"}]}`)
	}))
	t.Cleanup(server.Close)

	channel := &model.Channel{
		Type:           int(channelcatalog.ChannelTypeAdvancedCustom),
		BaseURL:        server.URL,
		Key:            "secret-key",
		OtherSettings:  advancedCustomDiscoverySettings("/provider/models", `,"auth":{"type":"header","name":"x-api-key","value":"route {api_key}"}`),
		HeaderOverride: `{"x-api-key":"override {api_key}","Host":"models.internal"}`,
	}
	models, err := FetchUpstreamModelsForChannel(channel)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, models)
}

func TestAdvancedCustomModelDiscoverySupportsQueryAndDefaultBearerAuth(t *testing.T) {
	enableUnsafeModelDiscoveryForTest(t)
	t.Run("query auth on an absolute route", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "1", r.URL.Query().Get("existing"))
			assert.Equal(t, "token secret-key", r.URL.Query().Get("access_key"))
			assert.Empty(t, r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, `{"data":[{"id":"query-model"}]}`)
		}))
		t.Cleanup(server.Close)
		channel := &model.Channel{
			Type: int(channelcatalog.ChannelTypeAdvancedCustom), BaseURL: "https://base.invalid", Key: "secret-key",
			OtherSettings: advancedCustomDiscoverySettings(server.URL+"/models?existing=1",
				`,"auth":{"type":"query","name":"access_key","value":"token {api_key}"}`),
		}
		models, err := FetchUpstreamModelsForChannel(channel)
		require.NoError(t, err)
		assert.Equal(t, []string{"query-model"}, models)
	})

	t.Run("nil auth defaults to bearer and a client placeholder is skipped", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer secret-key", r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, `{"data":[{"id":"bearer-model"}]}`)
		}))
		t.Cleanup(server.Close)
		channel := &model.Channel{
			Type: int(channelcatalog.ChannelTypeAdvancedCustom), BaseURL: server.URL, Key: "secret-key",
			OtherSettings:  advancedCustomDiscoverySettings("/models", ""),
			HeaderOverride: `{"Authorization":"{client_header:Authorization}"}`,
		}
		models, err := FetchUpstreamModelsForChannel(channel)
		require.NoError(t, err)
		assert.Equal(t, []string{"bearer-model"}, models)
	})
}

func TestAdvancedCustomModelDiscoveryValidationAndStrictResponse(t *testing.T) {
	enableUnsafeModelDiscoveryForTest(t)
	validRoute := func(response string) *model.Channel {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, response)
		}))
		t.Cleanup(server.Close)
		return &model.Channel{
			Type: int(channelcatalog.ChannelTypeAdvancedCustom), BaseURL: server.URL, Key: "secret-key",
			OtherSettings: advancedCustomDiscoverySettings("/models", ""),
		}
	}
	for name, response := range map[string]string{
		"missing data":        `{}`,
		"null data":           `{"data":null}`,
		"empty data":          `{"data":[]}`,
		"invalid JSON":        `{`,
		"control in model ID": `{"data":[{"id":"bad\nmodel"}]}`,
		"oversized model ID":  `{"data":[{"id":` + quoteJSONString(strings.Repeat("m", maxChannelUpstreamModelNameBytes+1)) + `}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := FetchUpstreamModelsForChannel(validRoute(response))
			require.Error(t, err)
		})
	}

	invalidSettings := map[string]string{
		"missing model route": `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/chat"}]}}`,
		"duplicate model route": `{"advanced_custom":{"advanced_routes":[` +
			`{"incoming_path":"/v1/models","upstream_path":"/a"},{"incoming_path":"/v1/models","upstream_path":"/b"}]}}`,
		"model converter":     advancedCustomDiscoverySettings("/models", `,"converter":"openai_chat_completions_to_anthropic_messages"`),
		"model filter":        `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/models","upstream_path":"/models","models":["x"]}]}}`,
		"empty auth type":     advancedCustomDiscoverySettings("/models", `,"auth":{"type":"","name":"x-api-key","value":"x"}`),
		"uppercase auth type": advancedCustomDiscoverySettings("/models", `,"auth":{"type":"HEADER","name":"x-api-key","value":"x"}`),
		"empty auth value":    advancedCustomDiscoverySettings("/models", `,"auth":{"type":"header","name":"x-api-key","value":""}`),
		"converter path mismatch": `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/messages","upstream_path":"/messages",` +
			`"converter":"openai_chat_completions_to_anthropic_messages"}]}}`,
		"URL user info": advancedCustomDiscoverySettings("https://user:pass@example.com/models", ""),
		"URL fragment":  advancedCustomDiscoverySettings("https://example.com/models#fragment", ""),
	}
	for name, settings := range invalidSettings {
		t.Run(name, func(t *testing.T) {
			_, err := FetchUpstreamModelsForChannel(&model.Channel{
				Type: int(channelcatalog.ChannelTypeAdvancedCustom), BaseURL: "https://base.invalid", Key: "secret-key", OtherSettings: settings,
			})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-key")
		})
	}
}

func TestAdvancedCustomQueryTransportErrorRedactsCredential(t *testing.T) {
	enableUnsafeModelDiscoveryForTest(t)
	secret := "secret-key-with-spaces"
	channel := &model.Channel{
		Type: int(channelcatalog.ChannelTypeAdvancedCustom), BaseURL: "http://127.0.0.1:1", Key: secret,
		OtherSettings: advancedCustomDiscoverySettings("/models",
			`,"auth":{"type":"query","name":"access_key","value":"{api_key}"}`),
	}
	_, err := FetchUpstreamModelsForChannelContext(context.Background(), channel)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), strings.ReplaceAll(secret, " ", "+"))
}

func TestProviderSpecificModelDiscoveryPaths(t *testing.T) {
	enableUnsafeModelDiscoveryForTest(t)
	tests := []struct {
		channelType channelcatalog.ChannelType
		wantPath    string
	}{
		{channelcatalog.ChannelTypeAli, "/compatible-mode/v1/models"},
		{channelcatalog.ChannelTypeZhipuV4, "/api/paas/v4/models"},
		{channelcatalog.ChannelTypeVolcEngine, "/v1/models"},
		{channelcatalog.ChannelTypeMoonshot, "/v1/models"},
	}
	for _, test := range tests {
		t.Run(channelcatalog.ChannelTypeName(test.channelType), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, test.wantPath, r.URL.Path)
				assert.Equal(t, "Bearer key", r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, `{"data":[{"id":"provider-model"}]}`)
			}))
			t.Cleanup(server.Close)
			models, err := FetchUpstreamModelsForChannel(&model.Channel{Type: int(test.channelType), BaseURL: server.URL, Key: "key"})
			require.NoError(t, err)
			assert.Equal(t, []string{"provider-model"}, models)
		})
	}
}

func TestSpecialProviderModelDiscoveryPlans(t *testing.T) {
	tests := []struct {
		channelType channelcatalog.ChannelType
		base        string
		wantURL     string
	}{
		{channelcatalog.ChannelTypeZhipuV4, "glm-coding-plan", "https://open.bigmodel.cn/api/coding/paas/v4/models"},
		{channelcatalog.ChannelTypeMoonshot, "kimi-coding-plan", "https://api.kimi.com/coding/v1/models"},
		{channelcatalog.ChannelTypeVolcEngine, "doubao-coding-plan", "https://ark.cn-beijing.volces.com/api/coding/v3/v1/models"},
	}
	for _, test := range tests {
		plan, err := buildChannelModelDiscoveryPlan(context.Background(), &model.Channel{Type: int(test.channelType)}, test.base, "key")
		require.NoError(t, err)
		assert.Equal(t, test.wantURL, plan.requestURL)
	}
}

func TestModelDiscoveryHeaderOverrideValidation(t *testing.T) {
	base := &model.Channel{Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: "https://example.com", Key: "secret"}
	for name, override := range map[string]string{
		"not an object":       `[]`,
		"non-string value":    `{"x-test":1}`,
		"blocked cookie":      `{"Cookie":"session=x"}`,
		"newline injection":   `{"x-test":"one\ntwo"}`,
		"invalid host":        `{"Host":"bad/host"}`,
		"invalid header name": `{"bad header":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			channel := *base
			channel.HeaderOverride = override
			_, err := buildChannelModelDiscoveryPlan(context.Background(), &channel, channel.BaseURL, channel.Key)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), channel.Key)
		})
	}
}
