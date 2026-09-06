package auth

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"testing"
)

func initAuthzDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.CasbinRule{}))
	model.DB = db
	model.LOG_DB = db
}

func TestAuthorizeRoleMatrix(t *testing.T) {
	initAuthzDB(t)
	require.NoError(t, InitCasbin())

	// Root can access anything.
	ok, err := Authorize(roles.RoleRootUser, "/api/anything", "GET")
	require.NoError(t, err)
	assert.True(t, ok)

	// Admin can access /api/*.
	ok, _ = Authorize(roles.RoleAdminUser, "/api/channel", "GET")
	assert.True(t, ok)

	// Admin cannot access a non-/api resource.
	ok, _ = Authorize(roles.RoleAdminUser, "/secret", "GET")
	assert.False(t, ok)

	// Ordinary user has no admin policy.
	ok, _ = Authorize(roles.RoleCommonUser, "/api/channel", "GET")
	assert.False(t, ok)
}

func TestAddPolicyPersistsAndEnforces(t *testing.T) {
	initAuthzDB(t)
	require.NoError(t, InitCasbin())

	// Grant the "user" role access to its own resources.
	_, err := AddPolicy("user", "/api/user/*", "GET")
	require.NoError(t, err)

	ok, err := Authorize(roles.RoleCommonUser, "/api/user/self", "GET")
	require.NoError(t, err)
	assert.True(t, ok)

	// Still denied for admin-only paths.
	ok, _ = Authorize(roles.RoleCommonUser, "/api/channel", "GET")
	assert.False(t, ok)
}

func TestAuthorizeUninitialized(t *testing.T) {
	casbinEnforcer = nil
	_, err := Authorize(roles.RoleRootUser, "/x", "GET")
	assert.Error(t, err)
}
