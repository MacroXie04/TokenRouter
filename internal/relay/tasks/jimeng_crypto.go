package tasks

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"os"
	"strings"
)

const (
	jimengCiphertextPrefix = "jimeng-key-v1:"
	jimengSessionKeyID     = "session"
)

type jimengEncryptionKey struct {
	id     string
	secret string
}

type jimengEncryptionKeyUnavailableError struct{ cause error }

func (e *jimengEncryptionKeyUnavailableError) Error() string {
	return "required Jimeng encryption key is not configured"
}

func (e *jimengEncryptionKeyUnavailableError) Unwrap() error { return e.cause }

func isJimengEncryptionKeyUnavailable(err error) bool {
	var unavailable *jimengEncryptionKeyUnavailableError
	return errors.As(err, &unavailable)
}

// JIMENG_ENCRYPTION_KEYS is a comma-separated current-first keyring such as
// "2026q3=<secret>,2026q2=<old-secret>". SESSION_SECRET remains a built-in
// versioned fallback and legacy unversioned ciphertext is tried against every
// configured old secret, permitting a staged migration and key rotation.
func jimengEncryptionKeyring() ([]jimengEncryptionKey, error) {
	raw := strings.TrimSpace(os.Getenv("JIMENG_ENCRYPTION_KEYS"))
	keys := make([]jimengEncryptionKey, 0, 4)
	seen := make(map[string]struct{})
	if raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			id, secret, ok := strings.Cut(strings.TrimSpace(entry), "=")
			id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
			if !ok || !validJimengKeyID(id) || len(secret) < 32 {
				return nil, errors.New("invalid JIMENG_ENCRYPTION_KEYS entry")
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("duplicate JIMENG_ENCRYPTION_KEYS id")
			}
			seen[id] = struct{}{}
			keys = append(keys, jimengEncryptionKey{id: id, secret: secret})
		}
	}
	if _, configured := seen[jimengSessionKeyID]; !configured {
		keys = append(keys, jimengEncryptionKey{id: jimengSessionKeyID, secret: cryptoutil.SessionSecret()})
	}
	return keys, nil
}

func validJimengKeyID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, character := range id {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func jimengEncrypt(plaintext string) (string, error) {
	keys, err := jimengEncryptionKeyring()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("Jimeng encryption keyring is empty")
	}
	encoded, err := sealJimengValue(plaintext, keys[0].secret, true)
	if err != nil {
		return "", err
	}
	return jimengCiphertextPrefix + keys[0].id + ":" + encoded, nil
}

func jimengDecrypt(encoded string) (string, error) {
	keys, err := jimengEncryptionKeyring()
	if err != nil {
		return "", &jimengEncryptionKeyUnavailableError{cause: err}
	}
	if strings.HasPrefix(encoded, jimengCiphertextPrefix) {
		keyID, payload, ok := strings.Cut(strings.TrimPrefix(encoded, jimengCiphertextPrefix), ":")
		if !ok || payload == "" {
			return "", errors.New("invalid versioned Jimeng ciphertext")
		}
		for _, key := range keys {
			if key.id == keyID {
				return openJimengValue(payload, key.secret, true)
			}
		}
		return "", &jimengEncryptionKeyUnavailableError{cause: errors.New("ciphertext key id is absent")}
	}
	// Read-only compatibility for ciphertext written before the Jimeng keyring.
	// Try the live SESSION_SECRET first, then explicitly retained old secrets.
	if plaintext, legacyErr := cryptoutil.DecryptByAES(encoded); legacyErr == nil {
		return plaintext, nil
	}
	for _, key := range keys {
		if plaintext, legacyErr := openJimengValue(encoded, key.secret, false); legacyErr == nil {
			return plaintext, nil
		}
	}
	return "", errors.New("decrypt Jimeng ciphertext")
}

func sealJimengValue(plaintext, secret string, domainSeparated bool) (string, error) {
	gcm, err := jimengGCM(secret, domainSeparated)
	if err != nil {
		return "", err
	}
	nonce, err := cryptoutil.SecureRandomBytes(gcm.NonceSize())
	if err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func openJimengValue(encoded, secret string, domainSeparated bool) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	gcm, err := jimengGCM(secret, domainSeparated)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("Jimeng ciphertext is too short")
	}
	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func jimengGCM(secret string, domainSeparated bool) (cipher.AEAD, error) {
	keyMaterial := secret
	if domainSeparated {
		keyMaterial = "tokenrouter:jimeng:v1:" + secret
	}
	digest := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, fmt.Errorf("initialize Jimeng encryption: %w", err)
	}
	return cipher.NewGCM(block)
}
