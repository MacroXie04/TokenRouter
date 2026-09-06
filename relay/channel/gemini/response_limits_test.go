package gemini

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

var errInjectedGeminiRead = errors.New("injected Gemini read failure")

type geminiPartialErrorReader struct {
	sent bool
}

func (r *geminiPartialErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "{}"), nil
	}
	return 0, errInjectedGeminiRead
}

func geminiTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	return ctx, recorder
}

func geminiTestResponse(reader io.Reader) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader)}
}

func paddedGeminiJSON(size int64) []byte {
	body := bytes.Repeat([]byte{' '}, int(size))
	copy(body, "{}")
	return body
}

func TestGeminiBufferedResponseBoundaryAndReadFailure(t *testing.T) {
	adaptor := &Adaptor{Mode: constant.RelayModeChatCompletions}
	meta := &relaycommon.Meta{}

	ctx, _ := geminiTestContext()
	exact := paddedGeminiJSON(relaycommon.MaxUpstreamJSONBodyBytes)
	_, err := adaptor.DoResponse(ctx, geminiTestResponse(bytes.NewReader(exact)), meta)
	require.NoError(t, err)

	ctx, recorder := geminiTestContext()
	over := paddedGeminiJSON(relaycommon.MaxUpstreamJSONBodyBytes + 1)
	_, err = adaptor.DoResponse(ctx, geminiTestResponse(bytes.NewReader(over)), meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Zero(t, recorder.Body.Len())

	ctx, recorder = geminiTestContext()
	_, err = adaptor.DoResponse(ctx, geminiTestResponse(&geminiPartialErrorReader{}), meta)
	assert.ErrorIs(t, err, errInjectedGeminiRead)
	assert.Zero(t, recorder.Body.Len())
}

func TestGeminiEmbeddingResponseUsesLargeJSONLimit(t *testing.T) {
	adaptor := &Adaptor{Mode: constant.RelayModeEmbeddings}
	ctx, recorder := geminiTestContext()
	body := strings.Repeat(" ", int(relaycommon.MaxUpstreamLargeJSONBodyBytes)+1)
	_, err := adaptor.DoResponse(ctx, geminiTestResponse(strings.NewReader(body)), &relaycommon.Meta{})
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Zero(t, recorder.Body.Len())
}

func TestGeminiStreamRejectsOversizedEvent(t *testing.T) {
	adaptor := &Adaptor{Mode: constant.RelayModeChatCompletions}
	ctx, _ := geminiTestContext()
	line := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	_, err := adaptor.DoResponse(ctx, geminiTestResponse(strings.NewReader(line)), &relaycommon.Meta{IsStream: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read Gemini event stream")
}

func TestGeminiStreamPreservesFunctionCallReasoningFinishAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"functionCall":{"name":"get_weather","args":{"city":"sf"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`,
		"",
	}, "\n\n")
	adaptor := &Adaptor{Mode: constant.RelayModeChatCompletions}
	ctx, recorder := geminiTestContext()
	usage, err := adaptor.DoResponse(ctx, geminiTestResponse(strings.NewReader(stream)), &relaycommon.Meta{IsStream: true})
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 8, usage.TotalTokens)

	body := recorder.Body.String()
	assert.Contains(t, body, `"role":"assistant"`)
	assert.Contains(t, body, `"reasoning_content":"thinking"`)
	assert.Contains(t, body, `"tool_calls"`)
	assert.Contains(t, body, `"id":"call_get_weather"`)
	assert.Contains(t, body, `"name":"get_weather"`)
	assert.Contains(t, body, `\"city\":\"sf\"`)
	assert.Contains(t, body, `"finish_reason":"tool_calls"`)
	assert.Equal(t, 1, strings.Count(body, "data: [DONE]"))
}
