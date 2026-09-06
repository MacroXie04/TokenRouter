package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestRoleGuards verifies AdminAuth and RootAuth enforcement: common users are
// denied on both, admins pass AdminAuth but not RootAuth, roots pass both.
func TestRoleGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "guards.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CasbinRule{}))
	model.DB = db
	require.NoError(t, auth.InitCasbin())

	mkUser := func(username string, role int) (int, string) {
		user := model.User{Username: username, Password: "x", Role: role, Status: model.UserStatusEnabled, AuthVersion: 1}
		require.NoError(t, model.DB.Create(&user).Error)
		_, access, _, err := auth.CompleteLogin(&user, "127.0.0.1", "ua", "test")
		require.NoError(t, err)
		return user.Id, access
	}
	_, commonAccess := mkUser("common", roles.RoleCommonUser)
	_, adminAccess := mkUser("admin", roles.RoleAdminUser)
	_, rootAccess := mkUser("root", roles.RoleRootUser)

	r := gin.New()
	r.GET("/admin-only", AdminAuth(), func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/root-only", RootAuth(), func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	do := func(path, access string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if access != "" {
			req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusUnauthorized, do("/admin-only", ""), "no credentials must be rejected")
	assert.Equal(t, http.StatusForbidden, do("/admin-only", commonAccess), "common user must not reach admin route")
	assert.Equal(t, http.StatusForbidden, do("/root-only", commonAccess), "common user must not reach root route")
	assert.Equal(t, http.StatusOK, do("/admin-only", adminAccess), "admin must pass AdminAuth")
	assert.Equal(t, http.StatusForbidden, do("/root-only", adminAccess), "admin must not pass RootAuth")
	assert.Equal(t, http.StatusOK, do("/admin-only", rootAccess), "root must pass AdminAuth")
	assert.Equal(t, http.StatusOK, do("/root-only", rootAccess), "root must pass RootAuth")
}

func TestAdminAuthFailsClosedWhenAuthorizationEngineErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "guards-error.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	user := model.User{Username: "admin-error", Password: "x", Role: roles.RoleAdminUser, Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&user).Error)
	_, access, _, err := auth.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	previous := authorizeAdminRequest
	authorizeAdminRequest = func(int, string, string) (bool, error) { return false, assert.AnError }
	t.Cleanup(func() { authorizeAdminRequest = previous })

	router := gin.New()
	router.GET("/admin-only", AdminAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/admin-only", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
