package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateRedirectURLBoundsTrustAndTransport(t *testing.T) {
	t.Setenv("TRUSTED_REDIRECT_DOMAINS", "example.test,example.test,127.0.0.1,invalid/path")
	t.Setenv("SERVER_ADDRESS", "http://localhost:3000")

	for _, candidate := range []string{
		"https://example.test/complete",
		"https://checkout.example.test/complete?order=1#done",
		"http://localhost:3000/complete",
		"http://127.0.0.1:8080/complete",
	} {
		assert.NoError(t, ValidateRedirectURL(candidate), candidate)
	}

	for _, candidate := range []string{
		"https://evil-example.test/complete",
		"https://example.test.evil.test/complete",
		"https://user:secret@example.test/complete",
		"http://example.test/complete",
		"javascript:alert(1)",
		"https:\\example.test\\complete",
		"https://example.test/\u202ecomplete",
		" https://example.test/complete",
		strings.Repeat("x", maxRedirectURLBytes+1),
	} {
		assert.Error(t, ValidateRedirectURL(candidate), candidate)
	}

	assert.Equal(t, []string{"example.test", "127.0.0.1", "localhost"}, TrustedRedirectDomains())
}
