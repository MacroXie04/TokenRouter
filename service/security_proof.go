package service

import (
	"crypto/hmac"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tokenrouter/tokenrouter/common"
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
	return []byte(common.SessionSecret() + ":" + securityProofTokenUse)
}

// IssueSecurityProof mints a proof token for the given session identity,
// verification method, and scopes.
func IssueSecurityProof(identity SessionIdentity, method string, scopes []string) (string, int64, error) {
	method = strings.TrimSpace(method)
	if identity.UserID <= 0 || identity.SessionID == "" || identity.UserAuthVersion <= 0 ||
		identity.SessionVersion <= 0 || method == "" || len(scopes) == 0 {
		return "", 0, ErrSecurityProofInvalid
	}
	now := time.Now()
	expiresAt := now.Add(SecurityProofTTL)
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
			ID:        common.RandomAlphanumeric(16),
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
	claims := &securityProofClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrSecurityProofInvalid
		}
		return securityProofSigningKey(), nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", ErrSecurityProofExpired
		}
		return "", ErrSecurityProofInvalid
	}
	if !token.Valid || claims.SessionID == "" {
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
