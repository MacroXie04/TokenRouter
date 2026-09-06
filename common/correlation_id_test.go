package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeProviderCorrelationID(t *testing.T) {
	assert.Empty(t, NormalizeProviderCorrelationID(""))

	for _, providerValue := range []string{
		"req_01HZX9.safe:1",
		"abcdefghijklmnopqrstuvwx",
		"sk-test-super-secret-provider-key",
		"access|secret",
		"line-one\nline-two",
		strings.Repeat("a", maxProviderCorrelationIDBytes+1),
	} {
		normalized := NormalizeProviderCorrelationID(providerValue)
		assert.Regexp(t, `^sha256:[0-9a-f]{32}$`, normalized)
		assert.NotContains(t, normalized, providerValue)
		assert.Equal(t, normalized, NormalizeProviderCorrelationID(providerValue))
		assert.Equal(t, normalized, NormalizeProviderCorrelationID(normalized), "fingerprints must be idempotent")
	}
}
