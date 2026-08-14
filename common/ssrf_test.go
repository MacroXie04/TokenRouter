package common

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
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

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	assert.NotNil(t, ip, "failed to parse %s", s)
	return ip
}
