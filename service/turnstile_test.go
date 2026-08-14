package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyTurnstile(t *testing.T) {
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

func TestVerifyTurnstileDisabledFailsOpen(t *testing.T) {
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	assert.False(t, TurnstileEnabled())
	ok, err := VerifyTurnstile("", "1.2.3.4")
	require.NoError(t, err)
	assert.True(t, ok, "turnstile disabled must fail open")
}
