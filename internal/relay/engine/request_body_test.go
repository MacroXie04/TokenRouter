package engine

import (
	"errors"
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

func TestParseRequestBodyLimitBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := `{"model":"boundary-model"}`
	exact := base + strings.Repeat(" ", int(maxRelayRequestBodyBytes)-len(base))

	contextFor := func(body string) *gin.Context {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		return context
	}

	request, raw, err := parseRequest(contextFor(exact))
	require.NoError(t, err)
	assert.Equal(t, "boundary-model", request.Model)
	assert.Len(t, raw, int(maxRelayRequestBodyBytes))

	request, raw, err = parseRequest(contextFor(exact + " "))
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	assert.Nil(t, request)
	assert.Nil(t, raw)

	readerErr := errors.New("read failed")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	context.Request.Body = io.NopCloser(errorReader{err: readerErr})
	_, _, err = parseRequest(context)
	assert.EqualError(t, err, "读取请求体失败")
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
