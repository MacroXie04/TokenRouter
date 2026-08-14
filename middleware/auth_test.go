package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func ginTestContext(headerKey, headerVal string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(headerKey, headerVal)
	c.Request = req
	return c
}

func TestIPInList(t *testing.T) {
	// Exact match.
	assert.True(t, ipInList("1.2.3.4", "1.2.3.4"))
	// Member of a comma-separated list (with whitespace tolerance).
	assert.True(t, ipInList("1.2.3.4", "1.1.1.1, 1.2.3.4, 5.6.7.8"))
	// Non-member.
	assert.False(t, ipInList("1.2.3.5", "1.2.3.4"))
	// Empty allow-list denies.
	assert.False(t, ipInList("1.2.3.4", ""))
	// Whitespace-only list denies.
	assert.False(t, ipInList("1.2.3.4", "  ,  "))
}

func TestExtractTokenKey(t *testing.T) {
	// The bearer prefix is stripped.
	r := ginTestContext("Authorization", "Bearer sk-abc123")
	assert.Equal(t, "sk-abc123", extractTokenKey(r))
}
