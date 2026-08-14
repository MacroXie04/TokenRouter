package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// PasswordHash produces a bcrypt hash of the given password. The cost is
// bounded so hashing remains fast enough for a gateway under load.
func PasswordHash(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// PasswordVerify reports whether the plaintext password matches the hash.
func PasswordVerify(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GenerateKey returns a random hex string of the given length in bytes (so the
// resulting string is 2*n characters).
func GenerateKey(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read never fails on modern platforms; fall back to a
		// timestamp-derived value only in the impossible error path.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// GenerateUUID returns a random UUID v4 string.
func GenerateUUID() string {
	return uuid.NewString()
}

// RandomAlphanumeric returns a random string of length n using the alphanumeric
// alphabet. Used for session IDs, OTP codes, and redemption keys.
func RandomAlphanumeric(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	return randomString(alphabet, n)
}

// RandomNumeric returns a random numeric string of length n. Used for OTP codes.
func RandomNumeric(n int) string {
	const digits = "0123456789"
	return randomString(digits, n)
}

func randomString(alphabet string, n int) string {
	var sb strings.Builder
	sb.Grow(n)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			idx = big.NewInt(0)
		}
		sb.WriteByte(alphabet[idx.Int64()])
	}
	return sb.String()
}

// JWTClaims is the access-token claims payload for relay/dashboard access.
type JWTClaims struct {
	UserID    int    `json:"id"`
	Role      int    `json:"role"`
	TokenName string `json:"token_name,omitempty"`
	jwt.RegisteredClaims
}

// GenerateJWT signs an HS256 access token with the given secret and TTL.
func GenerateJWT(userID, role int, tokenName string, secret string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := JWTClaims{
		UserID:    userID,
		Role:      role,
		TokenName: tokenName,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    ProductName,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseJWT validates an HS256 access token and returns its claims.
func ParseJWT(tokenStr, secret string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &JWTClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token claims")
}

// SessionSecret derives a stable 32-byte key from the configured secret so AES
// encryption/decryption of session cookies is consistent across restarts.
func SessionSecret() string {
	return GetEnv("SESSION_SECRET", "tokenrouter-dev-session-secret-change-me")
}

// aesKey derives a 32-byte AES key from the session secret.
func aesKey() []byte {
	sum := sha256.Sum256([]byte(SessionSecret()))
	return sum[:]
}

// EncryptByAES encrypts plaintext with AES-256-GCM and returns a base64-encoded
// "nonce|ciphertext" string.
func EncryptByAES(plaintext string) (string, error) {
	block, err := aes.NewCipher(aesKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// DecryptByAES decrypts a value produced by EncryptByAES.
func DecryptByAES(encoded string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(aesKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// SHA256Hex returns the hex SHA-256 digest of the input.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
