package relay

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func paddedNativeJSON(size int64) []byte {
	prefix := []byte(`{"padding":"`)
	suffix := []byte(`"}`)
	if size < int64(len(prefix)+len(suffix)) {
		return []byte(`{}`)
	}
	body := make([]byte, 0, size)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("x"), int(size)-len(prefix)-len(suffix))...)
	body = append(body, suffix...)
	return body
}

func nativeResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func nativeResponseContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	return c, recorder
}

func TestNativeNonStreamResponseReadersRejectOversizeWithoutPartialWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	over := paddedNativeJSON(relaycommon.MaxUpstreamJSONBodyBytes + 1)
	for _, test := range []struct {
		name string
		read func(*gin.Context, *http.Response) (*protocolkit.Usage, error)
	}{
		{name: "Gemini native", read: geminiNativeNonStreamResponse},
		{name: "Gemini from OpenAI", read: geminiNonStreamFromOpenAI},
		{name: "Claude native", read: claudeNativeNonStreamResponse},
		{name: "Claude from OpenAI", read: claudeNonStreamFromOpenAI},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, recorder := nativeResponseContext()
			_, err := test.read(c, nativeResponse(over))
			require.Error(t, err)
			assert.True(t, errors.Is(err, relaycommon.ErrUpstreamResponseTooLarge))
			assert.Zero(t, recorder.Body.Len())
		})
	}
}

func TestGeminiAndClaudeNativeReadersAcceptExactLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	exact := paddedNativeJSON(relaycommon.MaxUpstreamJSONBodyBytes)
	for _, read := range []func(*gin.Context, *http.Response) (*protocolkit.Usage, error){
		geminiNativeNonStreamResponse,
		claudeNativeNonStreamResponse,
	} {
		c, recorder := nativeResponseContext()
		_, err := read(c, nativeResponse(exact))
		require.NoError(t, err)
		assert.Equal(t, exact, recorder.Body.Bytes())
	}
}

func TestNativeConversionStreamsRejectOversizedEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	line := bytes.Repeat([]byte("x"), relaycommon.MaxUpstreamSSEEventBytes+1)
	stream := append([]byte("data: "), line...)
	for _, test := range []struct {
		name string
		read func(*gin.Context, *http.Response) (*protocolkit.Usage, error)
	}{
		{name: "Gemini native", read: geminiNativeStreamResponse},
		{name: "Gemini conversion", read: geminiStreamFromOpenAI},
		{name: "Claude conversion", read: func(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
			return claudeStreamFromOpenAI(c, resp, &RelayInfo{ModelName: "model", PromptTokens: 1})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := nativeResponseContext()
			_, err := test.read(c, nativeResponse(stream))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "maximum event")
		})
	}
}
