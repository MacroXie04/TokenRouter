package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func initPasskeyDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.PasskeyCredential{}, &model.AuthFlow{}))
	model.DB = db
	model.LOG_DB = db
}

func TestWebAuthnInitAndBeginRegistration(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)

	require.NoError(t, InitWebAuthn())
	options, flowToken, err := BeginPasskeyRegistration(u.Id)
	require.NoError(t, err)
	assert.NotNil(t, options)
	assert.NotEmpty(t, flowToken)

	// The challenge/session must be persisted as a one-time auth flow.
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", sha256hex(flowToken)).First(&flow).Error)
	assert.Equal(t, PasskeyPurposeRegister, flow.Purpose)
}

func TestBeginPasskeyLogin(t *testing.T) {
	initPasskeyDB(t)
	require.NoError(t, InitWebAuthn())

	options, flowToken, err := BeginPasskeyLogin()
	require.NoError(t, err)
	assert.NotNil(t, options)
	assert.NotEmpty(t, flowToken)
}

func TestPasskeyEnabled(t *testing.T) {
	initPasskeyDB(t)
	u := newUser(t, 0)

	assert.False(t, PasskeyEnabled(u.Id))
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{UserID: u.Id, CredentialID: "abc", PublicKey: "def"}).Error)
	assert.True(t, PasskeyEnabled(u.Id))
}

func sha256hex(s string) string {
	return common.SHA256Hex(s)
}
