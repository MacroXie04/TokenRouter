package advancedcustom

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAndMatchRoutesByOriginalModel(t *testing.T) {
	config := &Config{Routes: []Route{
		{IncomingPath: "/v1/chat/completions", UpstreamPath: "/claude/{model}", Converter: ConverterOpenAIChatToClaude, Models: []string{"re:^claude-"}},
		{IncomingPath: "/v1/chat/completions", UpstreamPath: "/openai/{model}", Models: []string{"gpt-4"}},
		{IncomingPath: "/v1/chat/completions", UpstreamPath: "/fallback/{model}"},
		{IncomingPath: "/v1beta/models/{model}:generateContent", UpstreamPath: "/gemini/{model}"},
	}}
	_, err := Validate(config)
	require.NoError(t, err)

	route, ok := Match(config, "/v1/chat/completions?ignored=1", "claude-sonnet")
	require.True(t, ok)
	assert.Equal(t, "/claude/{model}", route.UpstreamPath)
	route, ok = Match(config, "/v1/chat/completions", "other")
	require.True(t, ok)
	assert.Equal(t, "/fallback/{model}", route.UpstreamPath)
	_, ok = Match(config, "/v1beta/models/gemini-2:streamGenerateContent", "gemini-2")
	assert.True(t, ok, "Gemini generateContent routes also match the streaming spelling")
}

func TestValidateRejectsAmbiguousAndUnboundedConfiguration(t *testing.T) {
	tests := map[string]*Config{
		"catch-all before model route": {Routes: []Route{
			{IncomingPath: "/v1/chat/completions", UpstreamPath: "/all"},
			{IncomingPath: "/v1/chat/completions", UpstreamPath: "/specific", Models: []string{"m"}},
		}},
		"duplicate model rule": {Routes: []Route{
			{IncomingPath: "/v1/chat/completions", UpstreamPath: "/one", Models: []string{"m"}},
			{IncomingPath: "/v1/chat/completions", UpstreamPath: "/two", Models: []string{"m"}},
		}},
		"bad regex":               {Routes: []Route{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/one", Models: []string{"re:["}}}},
		"credential URL":          {Routes: []Route{{IncomingPath: "/v1/chat/completions", UpstreamPath: "https://user:pass@example.com/one"}}},
		"control model":           {Routes: []Route{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/one", Models: []string{"bad\nmodel"}}}},
		"multiple template slots": {Routes: []Route{{IncomingPath: "/v1/{model}/{model}", UpstreamPath: "/one"}}},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Validate(config)
			assert.Error(t, err)
		})
	}

	oversized := strings.Repeat("m", MaxModelNameBytes+1)
	_, err := Validate(&Config{Routes: []Route{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/one", Models: []string{oversized}}}})
	assert.Error(t, err)
}

func TestResolveURLPreservesBasePathRouteQueryAndStreamAuth(t *testing.T) {
	route := Route{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1beta/models/{model}:generateContent?existing=1",
		Converter:    ConverterOpenAIChatToGemini,
		Auth:         &Auth{Type: AuthQuery, Name: "key", Value: "prefix-{api_key}"},
	}
	resolved, err := ResolveURL("https://gateway.example/root", route, "gemini-pro", true, "secret")
	require.NoError(t, err)
	parsed, err := url.Parse(resolved)
	require.NoError(t, err)
	assert.Equal(t, "/root/v1beta/models/gemini-pro:streamGenerateContent", parsed.Path)
	assert.Equal(t, "1", parsed.Query().Get("existing"))
	assert.Equal(t, "sse", parsed.Query().Get("alt"))
	assert.Equal(t, "prefix-secret", parsed.Query().Get("key"))
}

func TestParseSettingsAndResolveURLRejectBoundViolations(t *testing.T) {
	_, err := ParseSettings(strings.Repeat("x", MaxSettingsBytes+1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceed")

	route := Route{IncomingPath: "/v1/chat/completions", UpstreamPath: "/models/{model}"}
	for name, mappedModel := range map[string]string{
		"empty":        "",
		"query":        "model?key=leak",
		"fragment":     "model#fragment",
		"control":      "model\nheader",
		"oversized":    strings.Repeat("m", MaxModelNameBytes+1),
		"invalid utf8": string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveURL("https://gateway.example", route, mappedModel, false, "secret")
			assert.Error(t, err)
		})
	}

	queryRoute := Route{
		IncomingPath: "/v1/chat/completions", UpstreamPath: "/chat",
		Auth: &Auth{Type: AuthQuery, Name: "key", Value: APIKeyPlaceholder},
	}
	_, err = ResolveURL("https://gateway.example", queryRoute, "model", false, strings.Repeat("k", MaxAuthValueBytes+1))
	assert.Error(t, err)
}

func TestIncomingPathsForModelHonorsRouteRulesAndOrder(t *testing.T) {
	config := &Config{Routes: []Route{
		{IncomingPath: "/v1/messages", UpstreamPath: "/anthropic", Models: []string{"re:^claude-"}},
		{IncomingPath: "/v1/responses", UpstreamPath: "/responses", Models: []string{"gpt-5"}},
		{IncomingPath: "/v1/chat/completions", UpstreamPath: "/specific-chat", Models: []string{"claude-sonnet"}},
		{IncomingPath: "/v1/chat/completions", UpstreamPath: "/chat"},
	}}
	paths, err := IncomingPathsForModel(config, "claude-sonnet")
	require.NoError(t, err)
	assert.Equal(t, []string{"/v1/messages", "/v1/chat/completions"}, paths)

	_, err = IncomingPathsForModel(&Config{}, "model")
	assert.Error(t, err)
}
