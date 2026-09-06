package router_test

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestGenerateAccessTokenAndAuth(t *testing.T) {
	r, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	// Generate a PAT via the reference route.
	rec := do(http.MethodGet, "/api/user/token", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	key := body.Data
	assert.GreaterOrEqual(t, len(key), 28)
	assert.LessOrEqual(t, len(key), 32)

	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, key, *user.AccessToken)

	// The PAT authenticates dashboard requests as that user.
	req, err := http.NewRequest(http.MethodGet, "/api/user/self", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+key)
	rec = doRequest(r, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"username":"adjuser"`)

	// A wrong PAT is rejected.
	req, err = http.NewRequest(http.MethodGet, "/api/user/self", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec = doRequest(r, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Regenerating replaces the token: the old one stops working.
	rec = do(http.MethodGet, "/api/user/token", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body2 struct {
		Data string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body2))
	require.NotEqual(t, key, body2.Data)

	req, err = http.NewRequest(http.MethodGet, "/api/user/self", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+key)
	rec = doRequest(r, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "old PAT must stop working after regeneration")

	req, err = http.NewRequest(http.MethodGet, "/api/user/self", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+body2.Data)
	rec = doRequest(r, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAccessTokenAuthenticatesAdminRoutes(t *testing.T) {
	r, do, _, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	rec := do(http.MethodGet, "/api/user/token", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	// Admin-guarded route via PAT.
	req, err := http.NewRequest(http.MethodGet, "/api/channel", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+body.Data)
	rec = doRequest(r, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGenerateAccessTokenUserRateLimit(t *testing.T) {
	// 1 request per window per user. The limiters read their env when the
	// router is built, so the values must be in place before SetUpRouter.
	t.Setenv("CRITICAL_RATE_LIMIT_ENABLE", "true")
	t.Setenv("CRITICAL_RATE_LIMIT", "1")
	t.Setenv("CRITICAL_RATE_LIMIT_DURATION", "20")
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "patrl.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	user := model.User{Username: "patrl", Password: "pw", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	sid, access, refresh, err := authsvc.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)
	r := router.SetUpRouter()
	doPat := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/user/token", nil)
		// Distinct remote address: rate-limit counters are keyed by IP and
		// live in a package-global store shared across routers in this
		// process.
		req.RemoteAddr = "10.77.77.77:1"
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusOK, doPat().Code)
	assert.Equal(t, http.StatusTooManyRequests, doPat().Code)
}
