package httpx

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"net"
	"testing"
)

func TestIsUnsafeIP(t *testing.T) {
	unsafe := []string{
		"127.0.0.1", "::1", "0.0.0.0", "::",
		"10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "169.254.1.1",
		"fe80::1", "fc00::1", "224.0.0.1", "ff02::1",
	}
	for _, s := range unsafe {
		assert.True(t, IsUnsafeIP(parseIP(t, s)), "expected %s to be unsafe", s)
	}
	safe := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range safe {
		assert.False(t, IsUnsafeIP(parseIP(t, s)), "expected %s to be safe", s)
	}
}

func TestValidateURLRejectsUnsafeSchemesAndHosts(t *testing.T) {
	// Unsupported scheme.
	assert.Error(t, ValidateURL("file:///etc/passwd"))
	assert.Error(t, ValidateURL("ftp://example.com"))
	// Embedded credentials.
	assert.Error(t, ValidateURL("http://user:pass@example.com"))

	// Loopback / metadata hosts are rejected (resolution may fail in sandboxes,
	// but the literal IP forms must be blocked deterministically).
	assert.Error(t, ValidateURL("http://127.0.0.1:8080/"))
	assert.Error(t, ValidateURL("http://169.254.169.254/"))
	assert.Error(t, ValidateURL("http://[::1]/"))
	assert.Error(t, ValidateURL("http://192.168.1.10/"))
	assert.Error(t, ValidateURL("http://10.0.0.5/"))
}

func TestSafeDialContextBlocksLoopback(t *testing.T) {
	// SSRF protection is enabled by default in tests (InitSSRF not called, or
	// env unset). Dialing a loopback address must be blocked.
	prev := ssrfDisabled
	ssrfDisabled = false
	defer func() { ssrfDisabled = prev }()

	_, err := SafeDialContext(t.Context(), "tcp", "127.0.0.1:80")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ssrf")
}

type staticSSRFResolver struct {
	addresses []net.IPAddr
	err       error
	calls     int
}

func (resolver *staticSSRFResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	resolver.calls++
	return resolver.addresses, resolver.err
}

func TestSafeDialContextPinsValidatedResolution(t *testing.T) {
	resolver := &staticSSRFResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	var dialed string
	dialFailure := errors.New("injected dial failure")

	_, err := safeDialContext(
		t.Context(),
		"tcp",
		"safe.example:443",
		resolver,
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialed = address
			return nil, dialFailure
		},
	)

	assert.ErrorIs(t, err, dialFailure)
	assert.Equal(t, 1, resolver.calls)
	assert.Equal(t, "8.8.8.8:443", dialed)
	assert.NotEqual(t, "safe.example:443", dialed)
}

func TestSafeDialContextRejectsMixedUnsafeResolutionBeforeDial(t *testing.T) {
	resolver := &staticSSRFResolver{addresses: []net.IPAddr{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("127.0.0.1")},
	}}
	dialCalls := 0

	_, err := safeDialContext(
		t.Context(),
		"tcp",
		"rebinding.example:80",
		resolver,
		func(context.Context, string, string) (net.Conn, error) {
			dialCalls++
			return nil, nil
		},
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blocked address 127.0.0.1")
	assert.Equal(t, 0, dialCalls)
}

func TestSafeDialContextTriesOnlyResolvedAddresses(t *testing.T) {
	resolver := &staticSSRFResolver{addresses: []net.IPAddr{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("1.1.1.1")},
	}}
	var dialed []string

	_, err := safeDialContext(
		t.Context(),
		"tcp4",
		"safe.example:8443",
		resolver,
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("offline")
		},
	)

	assert.Error(t, err)
	assert.Equal(t, []string{"8.8.8.8:8443", "1.1.1.1:8443"}, dialed)
}

func TestSafeDialContextRejectsUnsupportedNetworkAndMalformedAddress(t *testing.T) {
	resolver := &staticSSRFResolver{}
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dial must not be called")
		return nil, nil
	}

	_, err := safeDialContext(t.Context(), "unix", "/tmp/private.sock", resolver, dial)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported network")

	_, err = safeDialContext(t.Context(), "tcp", "missing-port.example", resolver, dial)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid address")
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	assert.NotNil(t, ip, "failed to parse %s", s)
	return ip
}
