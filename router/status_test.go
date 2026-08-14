package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func setupStatusTest(t *testing.T, role int) (http.Handler, func(method, path string) *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "status.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}))
	model.DB = db
	model.LOG_DB = db

	user := model.User{Username: "statustest", Password: "pw", Role: role, Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	_, access, refresh, err := service.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := SetUpRouter()
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "s." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return r, do
}

// TestStatusTestEndpoint covers GET /api/status/test: DB ping success under
// an admin session and a 401 for unauthenticated clients.
func TestStatusTestEndpoint(t *testing.T) {
	_, do := setupStatusTest(t, constant.RoleAdminUser)

	rec := do(http.MethodGet, "/api/status/test")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	assert.Equal(t, "Server is running", out["message"])

	// Without a session the route is guarded.
	r := SetUpRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/status/test", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

// TestSubscriptionSelfRoute covers the reference path GET /api/subscription/self:
// 200 for a signed-in user, 401 anonymously. The former TokenRouter-only alias
// /api/user/subscription is gone.
func TestSubscriptionSelfRoute(t *testing.T) {
	r, do := setupStatusTest(t, constant.RoleCommonUser)

	rec := do(http.MethodGet, "/api/subscription/self")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/api/subscription/self", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)

	// The removed alias no longer serves subscription data: the path now
	// resolves to the admin GET /api/user/:id route (id="subscription"),
	// which rejects a non-admin with 403 — the reference routes identically.
	rec3 := do(http.MethodGet, "/api/user/subscription")
	assert.Equal(t, http.StatusForbidden, rec3.Code, "removed alias must not serve the user")
}
