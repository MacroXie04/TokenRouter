package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strconv"
	"strings"
	"time"
)

// Security-proof tokens are short-lived JWTs bound to a specific dashboard
// session. They are issued after a step-up verification (2FA code or passkey
// assertion) and required before sensitive operations such as reading channel
// keys or managing passkeys.

const (
	// SecurityProofTTL is the lifetime of a security proof.
	SecurityProofTTL = 5 * time.Minute

	// Verification methods.
	SecurityProofMethod2FA     = "2fa"
	SecurityProofMethodPasskey = "passkey"

	// Verification scopes.
	SecurityProofScopeChannelKeyRead  = "channel.key.read"
	SecurityProofScopePasskeyRegister = "passkey.register"
	SecurityProofScopePasskeyDelete   = "passkey.delete"
	SecurityProofScopeTwoFAReset      = "twofa.reset"
	SecurityProofScopeBackupCodeReset = "twofa.backup_codes.regenerate"
)

// SessionIdentity identifies the authenticated dashboard session a proof is
// bound to. Proofs are invalidated by session rotation, revocation, or a
// user-level auth-version bump.
type SessionIdentity struct {
	UserID          int
	SessionID       string
	UserAuthVersion int64
	SessionVersion  int64
}

var (
	ErrSecurityProofInvalid = errors.New("security proof invalid")
	ErrSecurityProofExpired = errors.New("security proof expired")
	ErrProofScope           = errors.New("security proof scope mismatch")
	ErrProofMethod          = errors.New("security proof method mismatch")
)

const securityProofTokenUse = "security_proof"

type securityProofClaims struct {
	SessionID       string   `json:"sid"`
	UserAuthVersion int64    `json:"uav"`
	SessionVersion  int64    `json:"sv"`
	Method          string   `json:"method"`
	Scopes          []string `json:"scopes"`
	jwt.RegisteredClaims
}

func securityProofSigningKey() []byte {
	return []byte(cryptoutil.SessionSecret() + ":" + securityProofTokenUse)
}

// IssueSecurityProof mints a proof token for the given session identity,
// verification method, and scopes.
func IssueSecurityProof(identity SessionIdentity, method string, scopes []string) (string, int64, error) {
	method = strings.TrimSpace(method)
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 ||
		identity.SessionVersion <= 0 || method == "" || len(scopes) == 0 {
		return "", 0, ErrSecurityProofInvalid
	}
	nowUnix, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return "", 0, err
	}
	return issueSecurityProofAt(identity, method, scopes, time.Unix(nowUnix, 0).UTC())
}

func issueSecurityProofAt(identity SessionIdentity, method string, scopes []string, now time.Time) (string, int64, error) {
	method = strings.TrimSpace(method)
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 ||
		identity.SessionVersion <= 0 || method == "" || len(scopes) == 0 || now.IsZero() {
		return "", 0, ErrSecurityProofInvalid
	}
	expiresAt := now.Add(SecurityProofTTL)
	proofID, err := cryptoutil.SecureRandomAlphanumeric(16)
	if err != nil {
		return "", 0, err
	}
	claims := securityProofClaims{
		SessionID:       identity.SessionID,
		UserAuthVersion: identity.UserAuthVersion,
		SessionVersion:  identity.SessionVersion,
		Method:          method,
		Scopes:          append([]string(nil), scopes...),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "tokenrouter",
			Subject:   strconv.Itoa(identity.UserID),
			Audience:  jwt.ClaimStrings{"tokenrouter-security-proof"},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        proofID,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(securityProofSigningKey())
	if err != nil {
		return "", 0, err
	}
	return signed, expiresAt.Unix(), nil
}

// VerifySecurityProof validates a proof against the current session identity,
// a required scope, and the allowed verification methods. It returns the
// proof's method on success.
func VerifySecurityProof(raw string, identity SessionIdentity, requiredScope string, allowedMethods []string) (string, error) {
	claims, err := parseSignedSecurityProof(raw)
	if err != nil {
		return "", ErrSecurityProofInvalid
	}
	nowUnix, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return "", err
	}
	return verifySecurityProofClaimsAt(claims, identity, requiredScope, allowedMethods, time.Unix(nowUnix, 0).UTC())
}

func verifySecurityProofAt(raw string, identity SessionIdentity, requiredScope string, allowedMethods []string, now time.Time) (string, error) {
	claims, err := parseSignedSecurityProof(raw)
	if err != nil {
		return "", ErrSecurityProofInvalid
	}
	return verifySecurityProofClaimsAt(claims, identity, requiredScope, allowedMethods, now)
}

func parseSignedSecurityProof(raw string) (*securityProofClaims, error) {
	claims := &securityProofClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrSecurityProofInvalid
		}
		return securityProofSigningKey(), nil
	}, jwt.WithoutClaimsValidation(), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}
	if !token.Valid || claims.SessionID == "" {
		return nil, ErrSecurityProofInvalid
	}
	return claims, nil
}

func verifySecurityProofClaimsAt(claims *securityProofClaims, identity SessionIdentity, requiredScope string, allowedMethods []string, now time.Time) (string, error) {
	if claims == nil || now.IsZero() {
		return "", ErrSecurityProofInvalid
	}
	validator := jwt.NewValidator(
		jwt.WithIssuer("tokenrouter"),
		jwt.WithAudience("tokenrouter-security-proof"),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err := validator.Validate(claims); err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", ErrSecurityProofExpired
		}
		return "", ErrSecurityProofInvalid
	}
	userID, err := strconv.Atoi(claims.Subject)
	if err != nil || userID != identity.UserID ||
		claims.SessionID != identity.SessionID ||
		claims.UserAuthVersion != identity.UserAuthVersion ||
		claims.SessionVersion != identity.SessionVersion {
		return "", ErrSecurityProofInvalid
	}
	scopeOK := false
	for _, scope := range claims.Scopes {
		if scope == requiredScope {
			scopeOK = true
			break
		}
	}
	if !scopeOK {
		return "", ErrProofScope
	}
	methodOK := len(allowedMethods) == 0
	for _, method := range allowedMethods {
		if hmac.Equal([]byte(claims.Method), []byte(method)) {
			methodOK = true
			break
		}
	}
	if !methodOK {
		return "", ErrProofMethod
	}
	return claims.Method, nil
}
