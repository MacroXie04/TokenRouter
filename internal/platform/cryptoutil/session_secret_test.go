package cryptoutil

import (
	"encoding/hex"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	"strings"
	"testing"
	"time"
)

func TestValidateConfiguredSessionSecret(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantErr string
	}{
		{name: "random hex", secret: "9f4c14d78e20b3a65d01f87c42ea9bd6"},
		{name: "long passphrase", secret: "correct-horse-battery-staple-with-extra-entropy"},
		{name: "too short", secret: "short", wantErr: "at least 32"},
		{name: "old built-in placeholder", secret: "tokenrouter-dev-session-secret-change-me", wantErr: "placeholder"},
		{name: "compose placeholder", secret: "change-me-to-a-random-string", wantErr: "placeholder"},
		{name: "placeholder variant", secret: "PLEASE_use_a_PLACEHOLDER_value_123456789", wantErr: "placeholder"},
		{name: "surrounding whitespace", secret: " 9f4c14d78e20b3a65d01f87c42ea9bd6", wantErr: "whitespace"},
		{name: "low diversity", secret: strings.Repeat("a", 32), wantErr: "variation"},
		{name: "repeated pattern", secret: strings.Repeat("0123456789abcdef", 2), wantErr: "variation"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateConfiguredSessionSecret(test.secret)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestInitializeSessionSecretConfiguration(t *testing.T) {
	t.Run("production rejects missing secret", func(t *testing.T) {
		t.Setenv("SESSION_SECRET", "")
		t.Setenv("DEBUG", "false")
		err := InitializeSessionSecret()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SESSION_SECRET is required")
	})

	t.Run("debug permits cryptographically random ephemeral secret", func(t *testing.T) {
		t.Setenv("SESSION_SECRET", "")
		t.Setenv("DEBUG", "true")
		require.NoError(t, InitializeSessionSecret())
		first := SessionSecret()
		second := SessionSecret()
		assert.Equal(t, first, second)
		assert.Len(t, first, minSessionSecretBytes*2)
		_, err := hex.DecodeString(first)
		require.NoError(t, err)
	})

	t.Run("debug still rejects configured placeholder", func(t *testing.T) {
		t.Setenv("SESSION_SECRET", "change-me-to-a-random-string")
		t.Setenv("DEBUG", "true")
		err := InitializeSessionSecret()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "placeholder")
	})

	t.Run("production accepts strong configured secret", func(t *testing.T) {
		const secret = "9f4c14d78e20b3a65d01f87c42ea9bd6"
		t.Setenv("SESSION_SECRET", secret)
		t.Setenv("DEBUG", "false")
		require.NoError(t, InitializeSessionSecret())
		assert.Equal(t, secret, SessionSecret())
	})
}

func TestSessionSecretProtectsJWTAndAES(t *testing.T) {
	const secret = "ed53bf9237024adc81b47e8c6099fa16"
	t.Setenv("SESSION_SECRET", secret)
	require.NoError(t, InitializeSessionSecret())

	token, err := GenerateJWT(42, roles.RoleAdminUser, "", SessionSecret(), time.Minute)
	require.NoError(t, err)
	claims, err := ParseJWT(token, SessionSecret())
	require.NoError(t, err)
	assert.Equal(t, 42, claims.UserID)
	assert.Equal(t, roles.RoleAdminUser, claims.Role)
	_, err = ParseJWT(token, "f2fcd76c156f4feba29352a3022178d0")
	require.Error(t, err)

	ciphertext, err := EncryptByAES("jimeng-upstream-credential")
	require.NoError(t, err)
	assert.NotContains(t, ciphertext, "jimeng-upstream-credential")
	t.Setenv("SESSION_SECRET", "f2fcd76c156f4feba29352a3022178d0")
	_, err = DecryptByAES(ciphertext)
	require.Error(t, err)
	t.Setenv("SESSION_SECRET", secret)
	plaintext, err := DecryptByAES(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "jimeng-upstream-credential", plaintext)
}

func TestSessionJWTCanUseOneAuthoritativeClockForIssueAndValidation(t *testing.T) {
	const secret = "ed53bf9237024adc81b47e8c6099fa16"
	issuedAt := time.Unix(2_000_000_000, 0).UTC()
	token, err := GenerateSessionJWTAt(7, roles.RoleCommonUser, "session-clock", 2, 3, secret, time.Minute, issuedAt)
	require.NoError(t, err)

	claims, err := ParseJWTAt(token, secret, issuedAt.Add(59*time.Second))
	require.NoError(t, err)
	assert.Equal(t, "session-clock", claims.SessionID)
	_, err = ParseJWTAt(token, secret, issuedAt.Add(time.Minute))
	assert.ErrorIs(t, err, jwt.ErrTokenExpired)
}
