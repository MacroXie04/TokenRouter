package common

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type entropyErrorReader struct{}

func (entropyErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("injected entropy failure")
}

type rejectedEntropyReader struct{}

func (rejectedEntropyReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0xff
	}
	return len(p), nil
}

func TestSecurityRandomGeneratorsFailClosed(t *testing.T) {
	restore := SetSecureRandomReaderForTesting(entropyErrorReader{})
	t.Cleanup(restore)

	bytes, err := SecureRandomBytes(32)
	assert.ErrorIs(t, err, ErrSecureRandomUnavailable)
	assert.Nil(t, bytes)

	key, err := GenerateKey(16)
	assert.ErrorIs(t, err, ErrSecureRandomUnavailable)
	assert.Empty(t, key)

	alpha, err := SecureRandomAlphanumeric(64)
	assert.ErrorIs(t, err, ErrSecureRandomUnavailable)
	assert.Empty(t, alpha)

	numeric, err := SecureRandomNumeric(8)
	assert.ErrorIs(t, err, ErrSecureRandomUnavailable)
	assert.Empty(t, numeric)

	uuid, err := SecureRandomUUID()
	assert.ErrorIs(t, err, ErrSecureRandomUnavailable)
	assert.Empty(t, uuid)
}

func TestSecurityRandomGeneratorBoundsBrokenReader(t *testing.T) {
	restore := SetSecureRandomReaderForTesting(rejectedEntropyReader{})
	t.Cleanup(restore)

	value, err := SecureRandomAlphanumeric(16)
	assert.ErrorIs(t, err, ErrSecureRandomUnavailable)
	assert.Empty(t, value)
}

func TestBestEffortRandomIsExplicitlyNonSecretFallback(t *testing.T) {
	restore := SetSecureRandomReaderForTesting(entropyErrorReader{})
	t.Cleanup(restore)

	value := BestEffortRandomAlphanumeric(32)
	require.Len(t, value, 32)
	assert.NotEqual(t, strings.Repeat("a", 32), value)
	for _, char := range value {
		assert.Contains(t, alphanumericAlphabet, string(char))
	}
	assert.Len(t, BestEffortUUID(), 36)
}
