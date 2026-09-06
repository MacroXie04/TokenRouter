package cryptoutil

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"strings"
	"testing"
)

const testLegacyCost10PasswordHash = "$2y$10$WGIVWvPB3dHTKpgaZwRTAuLd.wsNOOvf1vW2ZwYuho14bIlqDCElW"

func TestPasswordHashUsesBoundedTargetCost(t *testing.T) {
	hash, err := PasswordHash("correct horse battery staple")
	require.NoError(t, err)
	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, PasswordBcryptTargetCost, cost)
	assert.True(t, PasswordVerify("correct horse battery staple", hash))
}

func TestPasswordBcryptHashValidation(t *testing.T) {
	cost, err := ValidatePasswordBcryptHash(testLegacyCost10PasswordHash)
	require.NoError(t, err)
	assert.Equal(t, 10, cost)

	cost12 := strings.Replace(testLegacyCost10PasswordHash, "$10$", "$12$", 1)
	cost, err = ValidatePasswordBcryptHash(cost12)
	require.NoError(t, err)
	assert.Equal(t, PasswordBcryptTargetCost, cost)

	cost13 := strings.Replace(testLegacyCost10PasswordHash, "$10$", "$13$", 1)
	_, err = ValidatePasswordBcryptHash(cost13)
	assert.True(t, errors.Is(err, ErrPasswordHashCostUnsupported))

	for _, invalid := range []string{
		"",
		"not-a-bcrypt-hash",
		strings.Replace(testLegacyCost10PasswordHash, "$2y$", "$2x$", 1),
		testLegacyCost10PasswordHash[:7] + strings.Repeat("!", 53),
	} {
		_, err = ValidatePasswordBcryptHash(invalid)
		assert.True(t, errors.Is(err, ErrPasswordHashInvalid), invalid)
	}
}
