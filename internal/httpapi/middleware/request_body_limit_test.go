package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func buildBodyLimitRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/limited", AnonymousRequestBodyLimit(), func(c *gin.Context) {
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.JSON(http.StatusOK, gin.H{"got": body, "length": c.Request.ContentLength})
	})
	return r
}

func postBody(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAnonymousRequestBodyLimitAllowsUnderLimit(t *testing.T) {
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "1")
	r := buildBodyLimitRouter()
	rec := postBody(t, r, `{"hello":"world"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"hello":"world"`)
	// ContentLength is normalized to the buffered body length.
	assert.Contains(t, rec.Body.String(), `"length":17`)
}

func TestAnonymousRequestBodyLimitRejectsOversize(t *testing.T) {
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "1")
	r := buildBodyLimitRouter()
	rec := postBody(t, r, `{"pad":"`+strings.Repeat("x", 2048)+`"}`)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestAnonymousRequestBodyLimitDisabled(t *testing.T) {
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "0")
	r := buildBodyLimitRouter()
	rec := postBody(t, r, `{"pad":"`+strings.Repeat("x", 4096)+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAnonymousRequestBodyLimitNegativeFallsBackToDefault(t *testing.T) {
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "-1")
	r := buildBodyLimitRouter()
	// Default is 512 KB: 300 KB passes.
	rec := postBody(t, r, `{"pad":"`+strings.Repeat("x", 300*1024)+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	// 600 KB is rejected.
	rec = postBody(t, r, `{"pad":"`+strings.Repeat("x", 600*1024)+`"}`)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestAnonymousRequestBodyLimitConfigurationCannotOverflowOrExceedGlobalCeiling(t *testing.T) {
	for _, value := range []string{
		"-1",
		"16385",
		"9223372036854775807",
		"9223372036854775808",
		"not-a-number",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", value)
			assert.Equal(t, int64(512<<10), httpx.GetAnonymousRequestBodyLimitBytes())
		})
	}

	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "16384")
	assert.Equal(t, int64(16<<20), httpx.GetAnonymousRequestBodyLimitBytes())
}

func TestAnonymousRequestBodyLimitUnreadableBody(t *testing.T) {
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "10")
	r := buildBodyLimitRouter()
	req := httptest.NewRequest(http.MethodPost, "/limited", errReader{})
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRequestBodyLimitRejectsKnownOversizeBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	r := gin.New()
	r.Use(RequestBodyLimit(8))
	r.POST("/limited", func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("123456789"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.False(t, called)
}

func TestRequestBodyLimitBoundsChunkedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestBodyLimit(8))
	r.POST("/limited", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if httpx.IsRequestBodyTooLargeError(err) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("123456789"))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestRequestBodyLimitAllowsExactBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestBodyLimit(8))
	r.POST("/limited", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		c.String(http.StatusOK, string(body))
	})
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("12345678"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "12345678", rec.Body.String())
}

// errReader is an io.Reader that always fails.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, http.ErrBodyReadAfterClose }
