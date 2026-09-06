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
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestRequestLoggerExcludesSensitiveRequestData(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var sink bytes.Buffer
	previous := common.Logger
	common.SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { common.SetLogger(previous) })

	router := gin.New()
	router.Use(RequestID(), RequestLogger(), Recovery())
	router.POST("/oauth/provider/callback", func(c *gin.Context) {
		_, _ = c.Writer.WriteString("ok")
	})

	const (
		querySecret  = "query-code-must-not-be-logged"
		headerSecret = "bearer-secret-must-not-be-logged"
		cookieSecret = "cookie-secret-must-not-be-logged"
		bodySecret   = "body-secret-must-not-be-logged"
		unsafeRID    = "unsafe request id with spaces"
	)
	req := httptest.NewRequest(http.MethodPost,
		"/oauth/provider/callback?code="+querySecret+"&state=state-secret",
		strings.NewReader(`{"password":"`+bodySecret+`"}`),
	)
	req.Header.Set("Authorization", "Bearer "+headerSecret)
	req.Header.Set("Cookie", "session="+cookieSecret)
	req.Header.Set("X-Request-Id", unsafeRID)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	logged := sink.String()
	assert.Contains(t, logged, `"msg":"request completed"`)
	assert.Contains(t, logged, `"method":"POST"`)
	assert.Contains(t, logged, `"path":"/oauth/provider/callback"`)
	assert.Contains(t, logged, `"status":200`)
	assert.Contains(t, logged, `"request_id":`)
	for _, secret := range []string{querySecret, headerSecret, cookieSecret, bodySecret, unsafeRID, "state-secret"} {
		assert.NotContains(t, logged, secret)
	}
	assert.NotEqual(t, unsafeRID, recorder.Header().Get("X-Request-Id"))
}

func TestRequestIDAcceptsLogSafeCallerValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "018f5f7c-7c84-7d7a-9d1f-123456789abc")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, "018f5f7c-7c84-7d7a-9d1f-123456789abc", recorder.Header().Get("X-Request-Id"))
}

func TestRequestLoggerUsesRouteTemplateForSecretPathParameters(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var sink bytes.Buffer
	previous := common.Logger
	common.SetLogger(slog.New(slog.NewJSONHandler(&sink, nil)))
	t.Cleanup(func() { common.SetLogger(previous) })

	router := gin.New()
	router.Use(RequestID(), RequestLogger(), Recovery())
	router.GET("/api/oauth/telegram/bind/:flow_token", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	const flowToken = "one-time-flow-token-must-not-be-logged"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, "/api/oauth/telegram/bind/"+flowToken, nil,
	))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	logged := sink.String()
	assert.Contains(t, logged, `"path":"/api/oauth/telegram/bind/:flow_token"`)
	assert.NotContains(t, logged, flowToken)
}
