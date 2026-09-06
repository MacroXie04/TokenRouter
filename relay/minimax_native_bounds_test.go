package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func TestMiniMaxNativeClaudeStreamTotalBound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: " + strings.Repeat("x", 128) + "\n\n",
		)),
	}
	usage, err := miniMaxClaudeNativeStreamResponseWithLimit(context, response, &RelayInfo{PromptTokens: 3}, 64)
	require.Error(t, err)
	assert.True(t, errors.Is(err, relaycommon.ErrUpstreamResponseTooLarge))
	require.NotNil(t, usage)
	assert.Equal(t, 3, usage.PromptTokens)
	assert.LessOrEqual(t, recorder.Body.Len(), 66)
}
