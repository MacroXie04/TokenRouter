package tasks

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAsyncTaskCiphertextSupportsRotationAndRejectsTampering(t *testing.T) {
	oldSecret := "0123456789abcdef0123456789abcdef"
	newSecret := "fedcba9876543210fedcba9876543210"
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "old="+oldSecret)
	oldCiphertext, err := asyncTaskEncrypt("provider-task-secret")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(oldCiphertext, asyncTaskCiphertextPrefix+"old:"))
	assert.NotContains(t, oldCiphertext, "provider-task-secret")

	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "new="+newSecret+",old="+oldSecret)
	plaintext, err := asyncTaskDecrypt(oldCiphertext)
	require.NoError(t, err)
	assert.Equal(t, "provider-task-secret", plaintext)
	newCiphertext, err := asyncTaskEncrypt("provider-task-new")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(newCiphertext, asyncTaskCiphertextPrefix+"new:"))

	separatorIndex := strings.LastIndexByte(newCiphertext, ':')
	require.Greater(t, separatorIndex, 0)
	ciphertextBytes, err := base64.RawURLEncoding.DecodeString(newCiphertext[separatorIndex+1:])
	require.NoError(t, err)
	require.NotEmpty(t, ciphertextBytes)
	ciphertextBytes[len(ciphertextBytes)-1] ^= 0x01
	tampered := newCiphertext[:separatorIndex+1] + base64.RawURLEncoding.EncodeToString(ciphertextBytes)
	_, err = asyncTaskDecrypt(tampered)
	assert.Error(t, err)
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "new="+newSecret)
	_, err = asyncTaskDecrypt(oldCiphertext)
	assert.ErrorContains(t, err, "key is unavailable")
}

func TestAsyncTaskCiphertextRejectsInvalidKeyrings(t *testing.T) {
	for _, keyring := range []string{
		"missing-separator",
		"bad key=0123456789abcdef0123456789abcdef",
		"short=too-short",
		"same=0123456789abcdef0123456789abcdef,same=fedcba9876543210fedcba9876543210",
	} {
		t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", keyring)
		_, err := asyncTaskEncrypt("provider-task")
		assert.Error(t, err, keyring)
	}
}

func TestAsyncTaskBoundCiphertextRejectsMetadataTransplant(t *testing.T) {
	t.Setenv("ASYNC_TASK_ENCRYPTION_KEYS", "current=0123456789abcdef0123456789abcdef")
	binding := videoProviderTaskBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "reservation-a", 7, 11)
	ciphertext, err := asyncTaskEncryptBound("provider-task-secret", binding)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ciphertext, asyncTaskBoundPrefix+"current:"))
	assert.NotContains(t, ciphertext, "provider-task-secret")

	plaintext, err := asyncTaskDecryptBound(ciphertext, binding)
	require.NoError(t, err)
	assert.Equal(t, "provider-task-secret", plaintext)

	for _, transplantedBinding := range []string{
		videoProviderTaskBinding("task_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "reservation-a", 7, 11),
		videoProviderTaskBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "reservation-b", 7, 11),
		videoProviderTaskBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "reservation-a", 8, 11),
		videoProviderTaskBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "reservation-a", 7, 12),
	} {
		_, err = asyncTaskDecryptBound(ciphertext, transplantedBinding)
		assert.Error(t, err)
	}

	credentialBinding := videoChannelCredentialBinding(
		"task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7, 11, "https://video.example/v1",
	)
	credentialCiphertext, err := asyncTaskEncryptBound("channel-secret", credentialBinding)
	require.NoError(t, err)
	for _, transplantedBinding := range []string{
		videoChannelCredentialBinding("task_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 7, 11, "https://video.example/v1"),
		videoChannelCredentialBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 8, 11, "https://video.example/v1"),
		videoChannelCredentialBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7, 12, "https://video.example/v1"),
		videoChannelCredentialBinding("task_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7, 11, "https://other.example/v1"),
	} {
		_, err = asyncTaskDecryptBound(credentialCiphertext, transplantedBinding)
		assert.Error(t, err)
	}
}
