package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRouterCORSSeparatesDashboardCookiesFromBearerUtilities(t *testing.T) {
	t.Setenv("FRONTEND_BASE_URL", "https://console.example.com")
	router := SetUpRouter()

	for _, path := range []string{"/api/log/token", "/api/usage/token/"} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "https://third-party.example")
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)

		assert.Equal(t, http.StatusNoContent, recorder.Code, path)
		assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"), path)
		assert.NotEqual(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"), path)
	}

	dashboardReq := httptest.NewRequest(http.MethodOptions, "/api/status", nil)
	dashboardReq.Header.Set("Origin", "https://third-party.example")
	dashboardReq.Header.Set("Access-Control-Request-Method", http.MethodGet)
	dashboardRecorder := httptest.NewRecorder()
	router.ServeHTTP(dashboardRecorder, dashboardReq)
	assert.Equal(t, http.StatusForbidden, dashboardRecorder.Code)
	assert.Empty(t, dashboardRecorder.Header().Get("Access-Control-Allow-Origin"))
}
