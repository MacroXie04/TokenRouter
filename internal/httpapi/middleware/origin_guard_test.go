package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestOriginGuardEnforcesTrustedOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://example.com")

	r := gin.New()
	r.POST("/refresh", OriginGuard(), func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	// Allowed exact origin.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.Header.Set("Origin", "https://example.com")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Disallowed origin.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.Header.Set("Origin", "https://evil.com")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// Missing origin (same-origin) is allowed.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/refresh", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestOriginGuardDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://example.com")

	r := gin.New()
	r.POST("/refresh", OriginGuard(), func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	// Guard is off in local HTTP mode: any origin passes.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.Header.Set("Origin", "https://evil.com")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
