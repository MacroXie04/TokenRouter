package relay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	asyncTaskCiphertextPrefix = "async-task-v1:"
	asyncTaskBoundPrefix      = "async-task-v2:"
	asyncTaskSessionKeyID     = "session"
)

type asyncTaskEncryptionKey struct {
	id     string
	secret string
}

// ASYNC_TASK_ENCRYPTION_KEYS is a current-first keyring such as
// "2026q3=<secret>,2026q2=<old-secret>". SESSION_SECRET is always retained as
// the versioned fallback unless a key with id "session" is supplied.
func asyncTaskEncryptionKeyring() ([]asyncTaskEncryptionKey, error) {
	raw := strings.TrimSpace(os.Getenv("ASYNC_TASK_ENCRYPTION_KEYS"))
	keys := make([]asyncTaskEncryptionKey, 0, 4)
	seen := make(map[string]struct{})
	if raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			id, secret, ok := strings.Cut(strings.TrimSpace(entry), "=")
			id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
			if !ok || !validAsyncTaskKeyID(id) || len(secret) < 32 {
				return nil, errors.New("invalid ASYNC_TASK_ENCRYPTION_KEYS entry")
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("duplicate ASYNC_TASK_ENCRYPTION_KEYS id")
			}
			seen[id] = struct{}{}
			keys = append(keys, asyncTaskEncryptionKey{id: id, secret: secret})
		}
	}
	if _, exists := seen[asyncTaskSessionKeyID]; !exists {
		keys = append(keys, asyncTaskEncryptionKey{id: asyncTaskSessionKeyID, secret: common.SessionSecret()})
	}
	return keys, nil
}

func validAsyncTaskKeyID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, character := range id {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func asyncTaskEncrypt(plaintext string) (string, error) {
	keys, err := asyncTaskEncryptionKeyring()
	if err != nil || len(keys) == 0 {
		return "", errors.Join(errors.New("async task encryption key is unavailable"), err)
	}
	gcm, err := asyncTaskGCM(keys[0].secret)
	if err != nil {
		return "", err
	}
	nonce, err := common.SecureRandomBytes(gcm.NonceSize())
	if err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return asyncTaskCiphertextPrefix + keys[0].id + ":" +
		base64.RawURLEncoding.EncodeToString(sealed), nil
}

func asyncTaskDecrypt(encoded string) (string, error) {
	if !strings.HasPrefix(encoded, asyncTaskCiphertextPrefix) {
		return "", errors.New("invalid async task ciphertext")
	}
	keyID, payload, ok := strings.Cut(strings.TrimPrefix(encoded, asyncTaskCiphertextPrefix), ":")
	if !ok || payload == "" {
		return "", errors.New("invalid async task ciphertext")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("invalid async task ciphertext")
	}
	keys, err := asyncTaskEncryptionKeyring()
	if err != nil {
		return "", errors.New("async task encryption key is unavailable")
	}
	for _, key := range keys {
		if key.id != keyID {
			continue
		}
		gcm, err := asyncTaskGCM(key.secret)
		if err != nil || len(data) < gcm.NonceSize() {
			return "", errors.New("invalid async task ciphertext")
		}
		plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
		if err != nil {
			return "", errors.New("decrypt async task ciphertext")
		}
		return string(plaintext), nil
	}
	return "", errors.New("async task ciphertext key is unavailable")
}

func asyncTaskEncryptBound(plaintext, binding string) (string, error) {
	if binding == "" {
		return "", errors.New("async task ciphertext binding is missing")
	}
	keys, err := asyncTaskEncryptionKeyring()
	if err != nil || len(keys) == 0 {
		return "", errors.Join(errors.New("async task encryption key is unavailable"), err)
	}
	gcm, err := asyncTaskGCM(keys[0].secret)
	if err != nil {
		return "", err
	}
	nonce, err := common.SecureRandomBytes(gcm.NonceSize())
	if err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(binding))
	return asyncTaskBoundPrefix + keys[0].id + ":" +
		base64.RawURLEncoding.EncodeToString(sealed), nil
}

func asyncTaskDecryptBound(encoded, binding string) (string, error) {
	if binding == "" || !strings.HasPrefix(encoded, asyncTaskBoundPrefix) {
		return "", errors.New("invalid bound async task ciphertext")
	}
	keyID, payload, ok := strings.Cut(strings.TrimPrefix(encoded, asyncTaskBoundPrefix), ":")
	if !ok || payload == "" {
		return "", errors.New("invalid bound async task ciphertext")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("invalid bound async task ciphertext")
	}
	keys, err := asyncTaskEncryptionKeyring()
	if err != nil {
		return "", errors.New("async task encryption key is unavailable")
	}
	for _, key := range keys {
		if key.id != keyID {
			continue
		}
		gcm, err := asyncTaskGCM(key.secret)
		if err != nil || len(data) < gcm.NonceSize() {
			return "", errors.New("invalid bound async task ciphertext")
		}
		plaintext, err := gcm.Open(
			nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte(binding),
		)
		if err != nil {
			return "", errors.New("decrypt bound async task ciphertext")
		}
		return string(plaintext), nil
	}
	return "", errors.New("async task ciphertext key is unavailable")
}

func asyncTaskGCM(secret string) (cipher.AEAD, error) {
	digest := sha256.Sum256([]byte("tokenrouter:async-task:v1:" + secret))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
