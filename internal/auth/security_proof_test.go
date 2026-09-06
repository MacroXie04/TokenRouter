package auth

import (
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"testing"
	"time"
)

func setupSecurityProofClock(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
}

func testIdentity() SessionIdentity {
	return SessionIdentity{UserID: 7, SessionID: "sid-abc", UserAuthVersion: 3, SessionVersion: 12}
}

func TestIssueAndVerifySecurityProof(t *testing.T) {
	setupSecurityProofClock(t)
	identity := testIdentity()
	raw, expiresAt, err := IssueSecurityProof(identity, SecurityProofMethod2FA, []string{SecurityProofScopeChannelKeyRead})
	require.NoError(t, err)
	assert.Greater(t, expiresAt, time.Now().Unix())

	method, err := VerifySecurityProof(raw, identity, SecurityProofScopeChannelKeyRead,
		[]string{SecurityProofMethod2FA, SecurityProofMethodPasskey})
	require.NoError(t, err)
	assert.Equal(t, SecurityProofMethod2FA, method)
}

func TestVerifySecurityProofScopeAndMethod(t *testing.T) {
	setupSecurityProofClock(t)
	identity := testIdentity()
	raw, _, err := IssueSecurityProof(identity, SecurityProofMethodPasskey, []string{SecurityProofScopePasskeyDelete})
	require.NoError(t, err)

	// Wrong scope.
	_, err = VerifySecurityProof(raw, identity, SecurityProofScopeChannelKeyRead,
		[]string{SecurityProofMethod2FA, SecurityProofMethodPasskey})
	assert.ErrorIs(t, err, ErrProofScope)

	// Wrong method.
	_, err = VerifySecurityProof(raw, identity, SecurityProofScopePasskeyDelete,
		[]string{SecurityProofMethod2FA})
	assert.ErrorIs(t, err, ErrProofMethod)
}

func TestVerifySecurityProofSessionBinding(t *testing.T) {
	setupSecurityProofClock(t)
	identity := testIdentity()
	raw, _, err := IssueSecurityProof(identity, SecurityProofMethod2FA, []string{SecurityProofScopeChannelKeyRead})
	require.NoError(t, err)

	// Different session.
	other := identity
	other.SessionID = "sid-other"
	_, err = VerifySecurityProof(raw, other, SecurityProofScopeChannelKeyRead, nil)
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)

	// Different user.
	other = identity
	other.UserID = 8
	_, err = VerifySecurityProof(raw, other, SecurityProofScopeChannelKeyRead, nil)
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)

	// Auth-version bump invalidates proofs.
	other = identity
	other.UserAuthVersion = 4
	_, err = VerifySecurityProof(raw, other, SecurityProofScopeChannelKeyRead, nil)
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)

	// Tampered token.
	_, err = VerifySecurityProof(raw+"x", identity, SecurityProofScopeChannelKeyRead, nil)
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)
}

func TestVerifySecurityProofExpired(t *testing.T) {
	setupSecurityProofClock(t)
	identity := testIdentity()
	claims := securityProofClaims{
		SessionID:       identity.SessionID,
		UserAuthVersion: identity.UserAuthVersion,
		SessionVersion:  identity.SessionVersion,
		Method:          SecurityProofMethod2FA,
		Scopes:          []string{SecurityProofScopeChannelKeyRead},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "7",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		},
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(securityProofSigningKey())
	require.NoError(t, err)

	_, err = VerifySecurityProof(raw, identity, SecurityProofScopeChannelKeyRead, nil)
	assert.ErrorIs(t, err, ErrSecurityProofExpired)
}

func TestIssueSecurityProofRejectsIncompleteIdentity(t *testing.T) {
	_, _, err := IssueSecurityProof(SessionIdentity{UserID: 1}, SecurityProofMethod2FA, []string{"scope"})
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)
	_, _, err = IssueSecurityProof(testIdentity(), "", []string{"scope"})
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)
	_, _, err = IssueSecurityProof(testIdentity(), SecurityProofMethod2FA, nil)
	assert.ErrorIs(t, err, ErrSecurityProofInvalid)
}

func TestSecurityProofUsesOneAuthoritativeClockSnapshot(t *testing.T) {
	identity := testIdentity()
	now := time.Unix(2_000_000_000, 0).UTC()
	raw, expiresAt, err := issueSecurityProofAt(identity, SecurityProofMethod2FA,
		[]string{SecurityProofScopeChannelKeyRead}, now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(SecurityProofTTL).Unix(), expiresAt)

	_, err = verifySecurityProofAt(raw, identity, SecurityProofScopeChannelKeyRead, nil,
		now.Add(SecurityProofTTL-time.Second))
	require.NoError(t, err)
	_, err = verifySecurityProofAt(raw, identity, SecurityProofScopeChannelKeyRead, nil,
		now.Add(SecurityProofTTL))
	assert.ErrorIs(t, err, ErrSecurityProofExpired)
}
