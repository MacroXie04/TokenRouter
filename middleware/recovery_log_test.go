package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestRecoveryLogRedactsPanicValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var sink bytes.Buffer
	previous := common.Logger
	common.SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { common.SetLogger(previous) })

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
