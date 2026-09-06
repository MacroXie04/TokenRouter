package middleware

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryLogRedactsPanicValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var sink bytes.Buffer
	previous := logging.Logger
	logging.SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { logging.SetLogger(previous) })

	router := gin.New()
	router.Use(RequestID(), Recovery())
	router.GET("/panic", func(*gin.Context) {
		panic("api-key=panic-secret-marker")
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	logged := sink.String()
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, logged, `"msg":"panic recovered"`)
	assert.Contains(t, logged, `"panic_type":"string"`)
	assert.Contains(t, logged, `"request_id":`)
	assert.False(t, strings.Contains(logged, "panic-secret-marker"), logged)
}
