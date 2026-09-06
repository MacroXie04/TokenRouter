package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func buildEmailRateLimitRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/send", EmailVerificationRateLimit(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"sent": true})
	})
	return r
}

func postEmailVerification(t *testing.T, r *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/send", nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestEmailVerificationRateLimitWindow(t *testing.T) {
	r := buildEmailRateLimitRouter()
	// Two sends in the 30s window pass; the third is throttled.
	assert.Equal(t, http.StatusOK, postEmailVerification(t, r, "10.1.1.1:1000").Code)
	assert.Equal(t, http.StatusOK, postEmailVerification(t, r, "10.1.1.1:1000").Code)
	rec := postEmailVerification(t, r, "10.1.1.1:1000")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)
	assert.Contains(t, rec.Body.String(), "发送过于频繁")
}

func TestEmailVerificationRateLimitPerIP(t *testing.T) {
	r := buildEmailRateLimitRouter()
	assert.Equal(t, http.StatusOK, postEmailVerification(t, r, "10.2.2.2:2000").Code)
	assert.Equal(t, http.StatusOK, postEmailVerification(t, r, "10.2.2.2:2000").Code)
	// A different IP has its own window.
	assert.Equal(t, http.StatusOK, postEmailVerification(t, r, "10.3.3.3:3000").Code)
	assert.Equal(t, http.StatusOK, postEmailVerification(t, r, "10.3.3.3:3000").Code)
	assert.Equal(t, http.StatusTooManyRequests, postEmailVerification(t, r, "10.2.2.2:2000").Code)
}
