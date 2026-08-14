package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func initAuthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}))
	model.DB = db
	model.LOG_DB = db
}

func seedPasswordUser(t *testing.T, username, password string) *model.User {
	t.Helper()
	hash, err := common.PasswordHash(password)
	require.NoError(t, err)
	u := &model.User{
		Username: username, Password: hash, Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, model.DB.Create(u).Error)
	return u
}

func TestLoginAndRefreshRotation(t *testing.T) {
	initAuthDB(t)
	u := seedPasswordUser(t, "alice", "password123")

	user, sid, access, refresh, err := Login("alice", "password123", "127.0.0.1", "test-agent")
	require.NoError(t, err)
	assert.Equal(t, u.Id, user.Id)
	assert.NotEmpty(t, sid)
	assert.NotEmpty(t, access)
	assert.NotEmpty(t, refresh)

	// Rotation: refresh yields new access + refresh tokens.
	access2, refresh2, _, err := RefreshSession(sid, refresh)
	require.NoError(t, err)
	assert.NotEmpty(t, access2)
	assert.NotEmpty(t, refresh2)
	assert.NotEqual(t, refresh, refresh2)

	// Replay of the previous refresh token must be rejected.
	_, _, _, err = RefreshSession(sid, refresh)
	assert.Error(t, err)
}

func TestRevokeSession(t *testing.T) {
	initAuthDB(t)
	seedPasswordUser(t, "bob", "password123")

	_, sid, _, refresh, err := Login("bob", "password123", "127.0.0.1", "test")
	require.NoError(t, err)

	require.NoError(t, RevokeSession(sid))
	_, _, _, err = RefreshSession(sid, refresh)
	assert.Error(t, err, "refresh after revocation must fail")
}

func TestLoginWrongPassword(t *testing.T) {
	initAuthDB(t)
	seedPasswordUser(t, "carol", "password123")
	_, _, _, _, err := Login("carol", "wrong", "127.0.0.1", "test")
	assert.Equal(t, ErrInvalidCredentials, err)
}
