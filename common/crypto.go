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
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	alphanumericAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	numericAlphabet      = "0123456789"
	maxSecureRandomBytes = 1 << 20

	// PasswordBcryptTargetCost is the one online password-work class accepted
	// for newly generated credentials. Login may temporarily accept cheaper
	// legacy hashes, but it pads them to this work class and upgrades them after
	// successful verification. More expensive imported hashes are rejected
	// before online comparison so one account cannot amplify anonymous CPU work.
	PasswordBcryptTargetCost = 12
)

// ErrSecureRandomUnavailable means a credential must not be issued because
// the operating system's cryptographic entropy source could not be read.
var ErrSecureRandomUnavailable = errors.New("secure random source unavailable")

var (
	// ErrPasswordHashInvalid identifies a value that is not one exact bcrypt
	// modular-crypt encoding accepted by the online password verifier.
	ErrPasswordHashInvalid = errors.New("invalid bcrypt password hash")
	// ErrPasswordHashCostUnsupported identifies a syntactically valid bcrypt
	// hash whose work factor exceeds the bounded online authentication policy.
	ErrPasswordHashCostUnsupported = errors.New("unsupported bcrypt password hash cost")
)

type secureRandomReaderState struct {
	reader io.Reader
}

var secureRandomReader atomic.Value

var bestEffortRandomCounter atomic.Uint64

func init() {
	secureRandomReader.Store(secureRandomReaderState{reader: rand.Reader})
}

// PasswordHash produces a bcrypt hash of the given password. The cost is
// bounded so hashing remains fast enough for a gateway under load.
func PasswordHash(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), PasswordBcryptTargetCost)
	return string(bytes), err
}

// PasswordVerify reports whether the plaintext password matches the hash.
func PasswordVerify(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ParsePasswordBcryptCost validates the complete serialized bcrypt envelope and
// returns its declared work factor. bcrypt.Cost intentionally parses only the
// header, so callers must use this helper before treating an imported value as
// a supported online credential.
func ParsePasswordBcryptCost(hash string) (int, error) {
	if len(hash) != 60 || hash[0] != '$' || hash[1] != '2' || hash[3] != '$' || hash[6] != '$' {
		return 0, ErrPasswordHashInvalid
	}
	switch hash[2] {
	case 'a', 'b', 'y':
	default:
		return 0, ErrPasswordHashInvalid
	}
	for index := 7; index < len(hash); index++ {
		character := hash[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '/' {
			continue
		}
		return 0, ErrPasswordHashInvalid
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return 0, ErrPasswordHashInvalid
	}
	return cost, nil
}

// ValidatePasswordBcryptHash applies the bounded online-authentication cost
// policy to a complete bcrypt encoding. Empty passwords are an explicit OAuth-
// only account representation and are handled by the caller, not by this API.
func ValidatePasswordBcryptHash(hash string) (int, error) {
	cost, err := ParsePasswordBcryptCost(hash)
	if err != nil {
		return 0, err
	}
	if cost > PasswordBcryptTargetCost {
		return 0, fmt.Errorf("%w: %d (maximum %d)", ErrPasswordHashCostUnsupported, cost, PasswordBcryptTargetCost)
	}
	return cost, nil
}

// SetSecureRandomReaderForTesting temporarily replaces the entropy source.
// It exists solely for deterministic failure-path tests; callers must invoke
// the returned restore function and must not use it concurrently with tests
// that issue credentials.
func SetSecureRandomReaderForTesting(reader io.Reader) (restore func()) {
	if reader == nil {
		panic("nil secure random reader")
	}
	previous := secureRandomReader.Load().(secureRandomReaderState)
	secureRandomReader.Store(secureRandomReaderState{reader: reader})
	var once sync.Once
	return func() {
		once.Do(func() { secureRandomReader.Store(previous) })
	}
}

// SecureRandomBytes reads exactly n bytes from the process cryptographic
// entropy source. Security-sensitive callers must propagate its error and
// abort before persisting or returning a credential.
func SecureRandomBytes(n int) ([]byte, error) {
	if n < 0 || n > maxSecureRandomBytes {
		return nil, fmt.Errorf("%w: invalid byte length %d", ErrSecureRandomUnavailable, n)
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil
	}
	reader := secureRandomReader.Load().(secureRandomReaderState).reader
	if _, err := io.ReadFull(reader, buf); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSecureRandomUnavailable, err)
	}
	return buf, nil
}

// GenerateKey returns a cryptographically random hex string containing 2*n
// characters. It never substitutes predictable material when entropy fails.
func GenerateKey(n int) (string, error) {
	buf, err := SecureRandomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SecureRandomUUID returns a cryptographically random RFC 4122 UUID v4. It is
// the required API for durable identifiers, idempotency keys, and lease/fencing
// tokens because entropy failure is returned to the caller.
func SecureRandomUUID() (string, error) {
	raw, err := SecureRandomBytes(16)
	if err != nil {
		return "", err
	}
	return formatUUIDv4(raw), nil
}

// BestEffortUUID returns a UUID-shaped non-secret correlation identifier. If
// the system entropy source fails, a process-local time/counter digest is used;
// callers must not rely on this value for authorization, durable uniqueness,
// idempotency, accounting, or lease fencing.
func BestEffortUUID() string {
	if value, err := SecureRandomUUID(); err == nil {
		return value
	}
	digest := sha256.Sum256([]byte(BestEffortRandomAlphanumeric(64)))
	return formatUUIDv4(digest[:16])
}

func formatUUIDv4(raw []byte) string {
	value := append([]byte(nil), raw...)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// SecureRandomAlphanumeric returns an unbiased cryptographically random
// alphanumeric string. Credential and authorization-token callers must use
// this error-returning API and fail closed.
func SecureRandomAlphanumeric(n int) (string, error) {
	return secureRandomString(alphanumericAlphabet, n)
}

// SecureRandomNumeric returns an unbiased cryptographically random numeric
// string. It is intended for OTP and recovery-code generation.
func SecureRandomNumeric(n int) (string, error) {
	return secureRandomString(numericAlphabet, n)
}

func secureRandomString(alphabet string, n int) (string, error) {
	if n < 0 || n > maxSecureRandomBytes || len(alphabet) < 2 || len(alphabet) > 256 {
		return "", fmt.Errorf("%w: invalid string parameters", ErrSecureRandomUnavailable)
	}
	if n == 0 {
		return "", nil
	}

	// Rejection sampling avoids modulo bias while retaining batched reads. The
	// sample budget is far above the expected requirement for either supported
	// alphabet and protects against a broken reader that returns only rejected
	// bytes forever.
	limit := 256 - (256 % len(alphabet))
	out := make([]byte, 0, n)
	maxSamples := n*4 + 4096
	samplesRead := 0
	for len(out) < n && samplesRead < maxSamples {
		remaining := n - len(out)
		batchSize := remaining*2 + 16
		if batchSize > 4096 {
			batchSize = 4096
		}
		if batchSize > maxSamples-samplesRead {
			batchSize = maxSamples - samplesRead
		}
		random, err := SecureRandomBytes(batchSize)
		if err != nil {
			return "", err
		}
		samplesRead += len(random)
		for _, value := range random {
			if int(value) >= limit {
				continue
			}
			out = append(out, alphabet[int(value)%len(alphabet)])
			if len(out) == n {
				break
			}
		}
	}
	if len(out) != n {
		return "", fmt.Errorf("%w: rejection limit exceeded", ErrSecureRandomUnavailable)
	}
	return string(out), nil
}

// BestEffortRandomAlphanumeric creates a non-secret correlation identifier.
// Its fallback is intentionally allowed to be predictable so logging, task,
// and provider display IDs can remain available during entropy-source failure.
// Never use this function for credentials, OTPs, recovery codes, session IDs,
// API keys, authorization state, or uniqueness relied on for access control.
func BestEffortRandomAlphanumeric(n int) string {
	if n <= 0 {
		return ""
	}
	if value, err := SecureRandomAlphanumeric(n); err == nil {
		return value
	}

	result := make([]byte, 0, n)
	seed := strconv.FormatInt(time.Now().UnixNano(), 10) + ":" +
		strconv.FormatUint(bestEffortRandomCounter.Add(1), 10)
	for block := uint64(0); len(result) < n; block++ {
		digest := sha256.Sum256([]byte(seed + ":" + strconv.FormatUint(block, 10)))
		for _, value := range digest {
			result = append(result, alphanumericAlphabet[int(value)%len(alphanumericAlphabet)])
			if len(result) == n {
				break
			}
		}
	}
	return string(result)
}

// JWTClaims is the access-token claims payload for relay/dashboard access.
type JWTClaims struct {
	UserID          int    `json:"id"`
	Role            int    `json:"role"`
	TokenName       string `json:"token_name,omitempty"`
	TokenUse        string `json:"token_use,omitempty"`
	SessionID       string `json:"sid,omitempty"`
	UserAuthVersion int64  `json:"uav,omitempty"`
	SessionVersion  int64  `json:"sv,omitempty"`
	jwt.RegisteredClaims
}

const dashboardAccessTokenUse = "access"

// GenerateJWT signs an HS256 access token with the given secret and TTL.
func GenerateJWT(userID, role int, tokenName string, secret string, ttl time.Duration) (string, error) {
	return generateJWTAt(JWTClaims{
		UserID:    userID,
		Role:      role,
		TokenName: tokenName,
	}, secret, ttl, time.Now())
}

// GenerateSessionJWT signs a dashboard access token bound to one server-side
// session snapshot. All four identity fields are validated again on every
// authenticated dashboard request.
func GenerateSessionJWT(userID, role int, sid string, userAuthVersion, sessionVersion int64, secret string, ttl time.Duration) (string, error) {
	return GenerateSessionJWTAt(userID, role, sid, userAuthVersion, sessionVersion, secret, ttl, time.Now())
}

// GenerateSessionJWTAt signs a dashboard access token against a trusted clock
// snapshot. Session services pass the primary-database time so nodes with
// skewed process clocks issue identical validity windows.
func GenerateSessionJWTAt(userID, role int, sid string, userAuthVersion, sessionVersion int64, secret string, ttl time.Duration, now time.Time) (string, error) {
	if userID <= 0 || sid == "" || userAuthVersion <= 0 || sessionVersion <= 0 {
		return "", errors.New("invalid session token identity")
	}
	return generateJWTAt(JWTClaims{
		UserID:          userID,
		Role:            role,
		TokenUse:        dashboardAccessTokenUse,
		SessionID:       sid,
		UserAuthVersion: userAuthVersion,
		SessionVersion:  sessionVersion,
	}, secret, ttl, now)
}

func generateJWTAt(claims JWTClaims, secret string, ttl time.Duration, now time.Time) (string, error) {
	if now.IsZero() {
		return "", errors.New("invalid token clock")
	}
	claims.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		Issuer:    ProductName,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseJWT validates an HS256 access token and returns its claims.
func ParseJWT(tokenStr, secret string) (*JWTClaims, error) {
	return ParseJWTAt(tokenStr, secret, time.Now())
}

// ParseJWTSigned verifies structure, algorithm, and signature without using a
// process clock. Callers that obtain an authoritative clock separately can
// then validate the registered claims with ValidateJWTClaimsAt.
func ParseJWTSigned(tokenStr, secret string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &JWTClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithoutClaimsValidation(), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token claims")
}

// ValidateJWTClaimsAt applies the registered-claim policy at one trusted time.
func ValidateJWTClaimsAt(claims *JWTClaims, now time.Time) error {
	if claims == nil || now.IsZero() {
		return errors.New("invalid token claims or clock")
	}
	validator := jwt.NewValidator(
		jwt.WithIssuer(ProductName),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	return validator.Validate(claims)
}

// ParseJWTAt verifies an access JWT and applies the registered-claim policy at
// the supplied trusted clock snapshot.
func ParseJWTAt(tokenStr, secret string, now time.Time) (*JWTClaims, error) {
	claims, err := ParseJWTSigned(tokenStr, secret)
	if err != nil {
		return nil, err
	}
	if err := ValidateJWTClaimsAt(claims, now); err != nil {
		return nil, err
	}
	return claims, nil
}

// IsDashboardAccessToken reports whether the claims belong to a
// session-backed dashboard access token rather than a generic JWT.
func IsDashboardAccessToken(claims *JWTClaims) bool {
	return claims != nil && claims.TokenUse == dashboardAccessTokenUse
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
	nonce, err := SecureRandomBytes(gcm.NonceSize())
	if err != nil {
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
