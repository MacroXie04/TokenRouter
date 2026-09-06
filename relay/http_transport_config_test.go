package relay

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func relayMapLookup(values map[string]string) relayEnvironmentLookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadRelayHTTPTransportConfigDefaultsAndOverrides(t *testing.T) {
	config, err := loadRelayHTTPTransportConfig(relayMapLookup(nil))
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, config.idleConnTimeout)
	assert.Equal(t, 500, config.maxIdleConns)
	assert.Equal(t, 100, config.maxIdleConnsPerHost)
	assert.False(t, config.insecureSkipVerify)

	config, err = loadRelayHTTPTransportConfig(relayMapLookup(map[string]string{
		"RELAY_IDLE_CONN_TIMEOUT":       "37",
		"RELAY_MAX_IDLE_CONNS":          "701",
		"RELAY_MAX_IDLE_CONNS_PER_HOST": "79",
		"TLS_INSECURE_SKIP_VERIFY":      "true",
	}))
	require.NoError(t, err)
	assert.Equal(t, 37*time.Second, config.idleConnTimeout)
	assert.Equal(t, 701, config.maxIdleConns)
	assert.Equal(t, 79, config.maxIdleConnsPerHost)
	assert.True(t, config.insecureSkipVerify)
}

func TestLoadRelayHTTPTransportConfigAcceptsInclusiveBounds(t *testing.T) {
	config, err := loadRelayHTTPTransportConfig(relayMapLookup(map[string]string{
		"RELAY_IDLE_CONN_TIMEOUT":       "1",
		"RELAY_MAX_IDLE_CONNS":          "1",
		"RELAY_MAX_IDLE_CONNS_PER_HOST": "1",
	}))
	require.NoError(t, err)
	assert.Equal(t, time.Second, config.idleConnTimeout)
	assert.Equal(t, 1, config.maxIdleConns)
	assert.Equal(t, 1, config.maxIdleConnsPerHost)

	config, err = loadRelayHTTPTransportConfig(relayMapLookup(map[string]string{
		"RELAY_IDLE_CONN_TIMEOUT":       "86400",
		"RELAY_MAX_IDLE_CONNS":          "10000",
		"RELAY_MAX_IDLE_CONNS_PER_HOST": "10000",
	}))
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, config.idleConnTimeout)
	assert.Equal(t, 10_000, config.maxIdleConns)
	assert.Equal(t, 10_000, config.maxIdleConnsPerHost)
}

func TestLoadRelayHTTPTransportConfigRejectsMalformedOrUnboundedValues(t *testing.T) {
	testCases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "empty", key: "RELAY_IDLE_CONN_TIMEOUT", value: ""},
		{name: "zero idle lifetime", key: "RELAY_IDLE_CONN_TIMEOUT", value: "0"},
		{name: "negative pool", key: "RELAY_MAX_IDLE_CONNS", value: "-1"},
		{name: "oversized pool", key: "RELAY_MAX_IDLE_CONNS_PER_HOST", value: "10001"},
		{name: "leading zero", key: "RELAY_MAX_IDLE_CONNS", value: "0500"},
		{name: "leading plus", key: "RELAY_MAX_IDLE_CONNS", value: "+500"},
		{name: "whitespace", key: "RELAY_MAX_IDLE_CONNS", value: " 500"},
		{name: "fractional", key: "RELAY_MAX_IDLE_CONNS", value: "2.5"},
		{name: "integer overflow", key: "RELAY_MAX_IDLE_CONNS", value: "999999999999999999999999"},
		{name: "invalid TLS boolean", key: "TLS_INSECURE_SKIP_VERIFY", value: "sometimes"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := loadRelayHTTPTransportConfig(relayMapLookup(map[string]string{
				testCase.key: testCase.value,
			}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.key)
		})
	}
}

func TestNewRelayHTTPClientPreservesSecurityAndPhaseBounds(t *testing.T) {
	client := newRelayHTTPClient(relayHTTPTransportConfig{
		idleConnTimeout:     71 * time.Second,
		maxIdleConns:        909,
		maxIdleConnsPerHost: 81,
		insecureSkipVerify:  true,
	})
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy, "provider destinations must not be delegated to environment proxies")
	require.NotNil(t, transport.DialContext)
	assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	assert.Equal(t, 71*time.Second, transport.IdleConnTimeout)
	assert.Equal(t, 909, transport.MaxIdleConns)
	assert.Equal(t, 81, transport.MaxIdleConnsPerHost)
	assert.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 60*time.Second, transport.ResponseHeaderTimeout)
	assert.Equal(t, time.Second, transport.ExpectContinueTimeout)
	assert.True(t, transport.ForceAttemptHTTP2)
	require.NotNil(t, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	request, err := http.NewRequest(http.MethodGet, "https://redirect.invalid", nil)
	require.NoError(t, err)
	assert.ErrorIs(t, client.CheckRedirect(request, nil), http.ErrUseLastResponse)
}

func TestInitHTTPClientPublishesOnlyACompleteValidCandidate(t *testing.T) {
	previous := relayHTTPClient
	t.Cleanup(func() {
		relayHTTPClient.CloseIdleConnections()
		relayHTTPClient = previous
	})

	t.Setenv("RELAY_IDLE_CONN_TIMEOUT", "invalid")
	require.Error(t, InitHTTPClient())
	assert.Same(t, previous, relayHTTPClient, "invalid configuration must retain the previous client")

	t.Setenv("RELAY_IDLE_CONN_TIMEOUT", "19")
	t.Setenv("RELAY_MAX_IDLE_CONNS", "607")
	t.Setenv("RELAY_MAX_IDLE_CONNS_PER_HOST", "53")
	t.Setenv("TLS_INSECURE_SKIP_VERIFY", "false")
	require.NoError(t, InitHTTPClient())
	assert.NotSame(t, previous, relayHTTPClient)
	validClient := relayHTTPClient
	transport, ok := relayHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 19*time.Second, transport.IdleConnTimeout)
	assert.Equal(t, 607, transport.MaxIdleConns)
	assert.Equal(t, 53, transport.MaxIdleConnsPerHost)

	t.Setenv("RELAY_IDLE_CONN_TIMEOUT", "20")
	t.Setenv("RELAY_MAX_IDLE_CONNS_PER_HOST", "10001")
	require.Error(t, InitHTTPClient())
	assert.Same(t, validClient, relayHTTPClient, "later invalid fields must not partially publish earlier fields")
}
