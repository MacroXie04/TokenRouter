package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
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
	ok, err := Authorize(constant.RoleRootUser, "/api/anything", "GET")
	require.NoError(t, err)
	assert.True(t, ok)

	// Admin can access /api/*.
	ok, _ = Authorize(constant.RoleAdminUser, "/api/channel", "GET")
	assert.True(t, ok)

	// Admin cannot access a non-/api resource.
	ok, _ = Authorize(constant.RoleAdminUser, "/secret", "GET")
	assert.False(t, ok)

	// Ordinary user has no admin policy.
	ok, _ = Authorize(constant.RoleCommonUser, "/api/channel", "GET")
	assert.False(t, ok)
}

func TestAddPolicyPersistsAndEnforces(t *testing.T) {
	initAuthzDB(t)
	require.NoError(t, InitCasbin())

	// Grant the "user" role access to its own resources.
	_, err := AddPolicy("user", "/api/user/*", "GET")
	require.NoError(t, err)

	ok, err := Authorize(constant.RoleCommonUser, "/api/user/self", "GET")
	require.NoError(t, err)
	assert.True(t, ok)

	// Still denied for admin-only paths.
	ok, _ = Authorize(constant.RoleCommonUser, "/api/channel", "GET")
	assert.False(t, ok)
}

func TestAuthorizeUninitialized(t *testing.T) {
	casbinEnforcer = nil
	_, err := Authorize(constant.RoleRootUser, "/x", "GET")
	assert.Error(t, err)
}
