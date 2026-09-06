package app

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/relay/engine"
	"net/http"
	"reflect"
	"testing"
)

func TestHTTPServerHasSlowClientBoundsAndStreamingSafeWrites(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newHTTPServer(":3000", handler)
	require.NotNil(t, server)
	assert.Equal(t, ":3000", server.Addr)
	assert.NotNil(t, server.Handler)
	assert.Equal(t, httpReadHeaderTimeout, server.ReadHeaderTimeout)
	assert.Equal(t, httpReadTimeout, server.ReadTimeout)
	assert.Equal(t, httpIdleTimeout, server.IdleTimeout)
	assert.Equal(t, httpMaxHeaderBytes, server.MaxHeaderBytes)
	assert.Zero(t, server.WriteTimeout, "streaming responses must not receive one global write deadline")
}

func TestInjectAnalytics(t *testing.T) {
	html := `<html><head><title>X</title></head><body></body></html>`

	// No analytics configured -> unchanged.
	t.Setenv("GOOGLE_ANALYTICS_ID", "")
	t.Setenv("UMAMI_WEBSITE_ID", "")
	t.Setenv("UMAMI_SCRIPT_URL", "")
	assert.Equal(t, html, injectAnalytics(html))

	// Google Analytics + Umami -> injected before </head>.
	t.Setenv("GOOGLE_ANALYTICS_ID", "G-XXX")
	t.Setenv("UMAMI_WEBSITE_ID", "umami-id")
	out := injectAnalytics(html)
	assert.Contains(t, out, "googletagmanager.com/gtag/js?id=G-XXX")
	assert.Contains(t, out, "data-website-id=\"umami-id\"")
	assert.Contains(t, out, "</head>")
}

func TestRunRuntimeInitializersStopsAtFirstFailure(t *testing.T) {
	want := errors.New("broken policy store")
	called := make([]string, 0, 3)
	err := runRuntimeInitializers([]runtimeInitializer{
		{name: "settings", run: func() error {
			called = append(called, "settings")
			return nil
		}},
		{name: "authorization", run: func() error {
			called = append(called, "authorization")
			return want
		}},
		{name: "pricing", run: func() error {
			called = append(called, "pricing")
			return nil
		}},
	})
	require.ErrorIs(t, err, want)
	assert.EqualError(t, err, "authorization: broken policy store")
	assert.Equal(t, []string{"settings", "authorization"}, called,
		"no later subsystem may publish after a critical initializer fails")
}

func TestRunRuntimeInitializersRejectsInvalidEntries(t *testing.T) {
	for _, initializers := range [][]runtimeInitializer{
		{{name: "", run: func() error { return nil }}},
		{{name: "missing"}},
	} {
		require.EqualError(t, runRuntimeInitializers(initializers), "invalid runtime initializer")
	}
}

func TestDefaultRuntimeInitializersConfigureRelayTransportAfterSettings(t *testing.T) {
	initializers := defaultRuntimeInitializers()
	settingsIndex := -1
	auditPrivacyIndex := -1
	relayIndex := -1
	for index, initializer := range initializers {
		switch initializer.name {
		case "settings":
			settingsIndex = index
		case "audit outbox privacy":
			auditPrivacyIndex = index
			require.NotNil(t, initializer.run)
		case "relay HTTP transport":
			relayIndex = index
			require.NotNil(t, initializer.run)
			assert.Equal(t, reflect.ValueOf(engine.InitHTTPClient).Pointer(), reflect.ValueOf(initializer.run).Pointer())
		}
	}
	require.NotEqual(t, -1, settingsIndex)
	require.NotEqual(t, -1, auditPrivacyIndex)
	require.NotEqual(t, -1, relayIndex)
	assert.Less(t, settingsIndex, auditPrivacyIndex)
	assert.Less(t, auditPrivacyIndex, relayIndex)
	assert.Less(t, settingsIndex, relayIndex)
}
