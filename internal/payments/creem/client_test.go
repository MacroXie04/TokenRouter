package creem

import (
	"crypto/tls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net/http"
	"reflect"
	"testing"
)

func TestCreemProductionTransportIsDirectBoundedAndSSRFSafe(t *testing.T) {
	transport, ok := currentCreemTransport().(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	assert.Positive(t, transport.ResponseHeaderTimeout)
	assert.Positive(t, transport.MaxResponseHeaderBytes)
}
