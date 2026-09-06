package controller

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomOAuthDiscoveryCannotBypassSSRFGuardThroughEnvironmentProxy(t *testing.T) {
	transport, ok := customOAuthDiscoveryClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy, "untrusted discovery destinations must not be delegated to an environment proxy")
	assert.NotNil(t, transport.DialContext)
}
