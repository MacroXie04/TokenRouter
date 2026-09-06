package claude

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

var errInjectedClaudeRead = errors.New("injected Claude read failure")

type claudePartialErrorReader struct {
	sent bool
}

func (r *claudePartialErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "{}"), nil
	}
	return 0, errInjectedClaudeRead
}

func claudeTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	return ctx, recorder
}

func claudeTestResponse(reader io.Reader) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader)}
}

func paddedClaudeJSON(size int64) []byte {
	body := bytes.Repeat([]byte{' '}, int(size))
	copy(body, "{}")
	return body
}

func TestClaudeBufferedResponseBoundaryAndReadFailure(t *testing.T) {
	adaptor := &Adaptor{Mode: constant.RelayModeChatCompletions}
	meta := &relaycommon.Meta{}

	ctx, _ := claudeTestContext()
	exact := paddedClaudeJSON(relaycommon.MaxUpstreamJSONBodyBytes)
	_, err := adaptor.DoResponse(ctx, claudeTestResponse(bytes.NewReader(exact)), meta)
	require.NoError(t, err)

	ctx, recorder := claudeTestContext()
	over := paddedClaudeJSON(relaycommon.MaxUpstreamJSONBodyBytes + 1)
	_, err = adaptor.DoResponse(ctx, claudeTestResponse(bytes.NewReader(over)), meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Zero(t, recorder.Body.Len())

	ctx, recorder = claudeTestContext()
	_, err = adaptor.DoResponse(ctx, claudeTestResponse(&claudePartialErrorReader{}), meta)
	assert.ErrorIs(t, err, errInjectedClaudeRead)
	assert.Zero(t, recorder.Body.Len())
}

func TestClaudeStreamRejectsOversizedEvent(t *testing.T) {
	adaptor := &Adaptor{Mode: constant.RelayModeChatCompletions}
	ctx, _ := claudeTestContext()
	line := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	_, err := adaptor.DoResponse(ctx, claudeTestResponse(strings.NewReader(line)), &relaycommon.Meta{IsStream: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read Claude event stream")
}

func TestClaudeStreamPreservesToolUseDeltasFinishReasonAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":2}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"sf\"}"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n\n")
	adaptor := &Adaptor{Mode: constant.RelayModeChatCompletions}
	ctx, recorder := claudeTestContext()
	usage, err := adaptor.DoResponse(ctx, claudeTestResponse(strings.NewReader(stream)), &relaycommon.Meta{
		IsStream: true, PromptTokens: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 10, usage.TotalTokens)

	body := recorder.Body.String()
	assert.Contains(t, body, `"tool_calls"`)
	assert.Contains(t, body, `"id":"toolu_1"`)
	assert.Contains(t, body, `"name":"get_weather"`)
	assert.Contains(t, body, `\"city\":\"sf\"`)
	assert.Contains(t, body, `"finish_reason":"tool_calls"`)
	assert.Equal(t, 1, strings.Count(body, "data: [DONE]"))
}
