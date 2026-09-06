package engine

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

var errInjectedInboundRead = errors.New("injected inbound read failure")

type inboundErrorReader struct{}

func (inboundErrorReader) Read([]byte) (int, error) { return 0, errInjectedInboundRead }

func paddedInboundJSON(base string, size int64) string {
	return base + strings.Repeat(" ", int(size)-len(base))
}

func callInboundHandler(handler gin.HandlerFunc, path string, body io.Reader) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, path, body)
	handler(ctx)
	return recorder
}

func TestClaudeMessagesInboundBodyLimit(t *testing.T) {
	exact := paddedInboundJSON(`{"model":""}`, maxClaudeMessagesRequestBodyBytes)
	recorder := callInboundHandler(RelayClaudeMessages, "/v1/messages", strings.NewReader(exact))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "model", "an exact-limit body must reach semantic validation")

	over := paddedInboundJSON(`{"model":""}`, maxClaudeMessagesRequestBodyBytes+1)
	recorder = callInboundHandler(RelayClaudeMessages, "/v1/messages", strings.NewReader(over))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "请求体过大")

	recorder = callInboundHandler(RelayClaudeMessages, "/v1/messages", inboundErrorReader{})
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "读取请求体失败")
}

func TestGeminiNativeInboundBodyLimit(t *testing.T) {
	exact := paddedInboundJSON(`{}`, maxGeminiNativeRequestBodyBytes)
	recorder := callInboundHandler(RelayGeminiNative, "/v1beta/not-a-model-path", strings.NewReader(exact))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "模型名称不能为空", "an exact-limit body must reach path validation")

	over := paddedInboundJSON(`{}`, maxGeminiNativeRequestBodyBytes+1)
	recorder = callInboundHandler(RelayGeminiNative, "/v1beta/models/gemini:test", strings.NewReader(over))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "请求体过大")

	recorder = callInboundHandler(RelayGeminiNative, "/v1beta/models/gemini:test", inboundErrorReader{})
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "读取请求体失败")
}
