package relay

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestRelayTransportsUseDirectSSRFSafeDialing(t *testing.T) {
	transport, ok := relayHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy, "relay HTTP must not delegate destination resolution to an environment proxy")
	require.NotNil(t, transport.DialContext)
	assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())

	assert.Nil(t, wsDialer.Proxy, "relay WebSocket must not delegate destination resolution to an environment proxy")
	require.NotNil(t, wsDialer.NetDialContext)
	assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(wsDialer.NetDialContext).Pointer())
}

func TestRelayHTTPClientRefusesCredentialedRedirects(t *testing.T) {
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()

	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls++
	}))
	t.Cleanup(destination.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	request, err := http.NewRequest(http.MethodPost, redirector.URL, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer must-not-be-replayed")
	response, err := relayHTTPClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	assert.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	assert.Zero(t, destinationCalls)
	assert.ErrorIs(t, relayHTTPClient.CheckRedirect(request, nil), http.ErrUseLastResponse)
}
