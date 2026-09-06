package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func dashboardCORSTestRouter() *gin.Engine {
	router := gin.New()
	router.Use(DashboardCORS())
	router.Any("/*path", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

func TestDashboardCORSAllowsOnlyExactConfiguredOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_BASE_URL", "https://console.example.com/app")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://ops.example.com, http://localhost:5173")
	router := dashboardCORSTestRouter()

	for _, origin := range []string{
		"https://console.example.com",
		"https://ops.example.com",
		"http://localhost:5173",
		"http://api.example.com",
	} {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "http://api.example.com/api/user/self", nil)
			req.Host = "api.example.com"
			req.Header.Set("Origin", origin)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusNoContent, recorder.Code)
			assert.Equal(t, origin, recorder.Header().Get("Access-Control-Allow-Origin"))
			assert.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
			assert.Contains(t, recorder.Header().Get("Access-Control-Allow-Headers"), "X-Security-Proof")
		})
	}
}

func TestDashboardCORSRejectsUntrustedAndMalformedOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_BASE_URL", "https://console.example.com")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "")
	router := dashboardCORSTestRouter()

	for _, origin := range []string{
		"https://evil.example",
		"https://console.example.com.evil.example",
		"https://console.example.com/path",
		"null",
	} {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://api.example.com/api/user/self", nil)
			req.Host = "api.example.com"
			req.Header.Set("Origin", origin)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

func TestDashboardCORSDoesNotAffectRelayOrNonBrowserRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_BASE_URL", "https://console.example.com")
	router := dashboardCORSTestRouter()

	relayReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	relayReq.Header.Set("Origin", "https://untrusted.example")
	relayRecorder := httptest.NewRecorder()
	router.ServeHTTP(relayRecorder, relayReq)
	assert.Equal(t, http.StatusOK, relayRecorder.Code)
	assert.Empty(t, relayRecorder.Header().Get("Access-Control-Allow-Origin"))

	dashboardReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	dashboardRecorder := httptest.NewRecorder()
	router.ServeHTTP(dashboardRecorder, dashboardReq)
	assert.Equal(t, http.StatusOK, dashboardRecorder.Code)
	assert.Empty(t, dashboardRecorder.Header().Get("Access-Control-Allow-Origin"))

	for _, bearerPath := range []string{"/api/log/token", "/api/usage/token/"} {
		req := httptest.NewRequest(http.MethodGet, bearerPath, nil)
		req.Header.Set("Origin", "https://untrusted.example")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusOK, recorder.Code)
	}
}
