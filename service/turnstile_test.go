package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func allowTurnstileLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
}

func TestVerifyTurnstile(t *testing.T) {
	allowTurnstileLoopback(t)
	// Mock Cloudflare siteverify endpoint.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		valid := r.Form.Get("response") == "valid-token"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": valid})
	}))
	defer mock.Close()

	t.Setenv("TURNSTILE_SECRET_KEY", "test-secret")
	t.Setenv("TURNSTILE_VERIFY_URL", mock.URL)

	assert.True(t, TurnstileEnabled())
	ok, err := VerifyTurnstile("valid-token", "1.2.3.4")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = VerifyTurnstile("bad-token", "1.2.3.4")
	require.NoError(t, err)
	assert.False(t, ok)

	// Empty token fails closed.
	ok, err = VerifyTurnstile("", "1.2.3.4")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVerifyTurnstileResponseIsBounded(t *testing.T) {
	allowTurnstileLoopback(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxTurnstileResponseBytes)+1)))
	}))
	defer mock.Close()
	t.Setenv("TURNSTILE_SECRET_KEY", "test-secret")
	t.Setenv("TURNSTILE_VERIFY_URL", mock.URL)

	ok, err := VerifyTurnstile("valid-token", "1.2.3.4")
	assert.False(t, ok)
	require.Error(t, err)
	assert.True(t, errors.Is(err, common.ErrBodyTooLarge))
}

func TestTurnstileClientUsesDirectSSRFSafeTransport(t *testing.T) {
	transport, ok := turnstileHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
	assert.NotNil(t, turnstileHTTPClient.CheckRedirect)
}

func TestTurnstileClientBlocksUnsafeOverride(t *testing.T) {
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	common.InitSSRF()
	t.Setenv("TURNSTILE_SECRET_KEY", "test-secret")
	t.Setenv("TURNSTILE_VERIFY_URL", "http://127.0.0.1:65535/siteverify")

	ok, err := VerifyTurnstile("valid-token", "192.0.2.1")
	assert.False(t, ok)
	require.ErrorContains(t, err, "ssrf: blocked address")
}

func TestVerifyTurnstileDisabledFailsOpen(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	assert.False(t, TurnstileEnabled())
	ok, err := VerifyTurnstile("", "1.2.3.4")
	require.NoError(t, err)
	assert.True(t, ok, "turnstile disabled must fail open")
}
