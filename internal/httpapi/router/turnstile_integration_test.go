package router_test

import (
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegisterTurnstileGate proves the TurnstileCheck middleware is wired on
// POST /api/user/register: with Turnstile enabled the request is rejected
// without a valid query token and accepted with one.
func TestRegisterTurnstileGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("response") == "good-token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TURNSTILE_VERIFY_URL", srv.URL)

	dsn := "file:" + filepath.Join(t.TempDir(), "turnstile.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db

	r := router.SetUpRouter()
	body := `{"username":"turnstile_user","password":"password123"}`

	// No token: rejected with the reference message (HTTP 200, success:false).
	req := httptest.NewRequest(http.MethodPost, "/api/user/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Turnstile token 为空")

	// Valid token: registration proceeds.
	req = httptest.NewRequest(http.MethodPost, "/api/user/register?turnstile=good-token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "注册成功")
	var count int64
	model.DB.Model(&model.User{}).Where("username = ?", "turnstile_user").Count(&count)
	assert.Equal(t, int64(1), count)
}
