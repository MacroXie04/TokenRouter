package relaycommon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/protocolkit"
)

var errInjectedUpstreamRead = errors.New("injected upstream read failure")

type partialErrorReader struct {
	sent bool
}

func (r *partialErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "attacker-controlled-partial-body"), nil
	}
	return 0, errInjectedUpstreamRead
}

func TestReadUpstreamBodyBoundaryAndReadFailure(t *testing.T) {
	const limit int64 = 32

	exact, err := ReadUpstreamBody(bytes.NewReader(bytes.Repeat([]byte("x"), int(limit))), limit)
	require.NoError(t, err)
	assert.Len(t, exact, int(limit))

	over, err := ReadUpstreamBody(bytes.NewReader(bytes.Repeat([]byte("x"), int(limit)+1)), limit)
	assert.Nil(t, over)
	assert.ErrorIs(t, err, ErrUpstreamResponseTooLarge)

	partial, err := ReadUpstreamBody(&partialErrorReader{}, limit)
	assert.Nil(t, partial)
	assert.ErrorIs(t, err, errInjectedUpstreamRead)
}

func TestHandleErrorResponseBoundsAndSanitizesBody(t *testing.T) {
	exactPayload := strings.Repeat("e", int(MaxUpstreamErrorBodyBytes))
	exact := HandleErrorResponse(&http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(exactPayload)),
	})
	var exactUpstream *UpstreamError
	require.ErrorAs(t, exact, &exactUpstream)
	assert.Len(t, exactUpstream.Body, int(MaxUpstreamErrorBodyBytes))
	assert.NoError(t, exactUpstream.Cause)

	oversizedPayload := "do-not-reflect:" + strings.Repeat("x", int(MaxUpstreamErrorBodyBytes))
	oversized := HandleErrorResponse(&http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader(oversizedPayload)),
	})
	assert.ErrorIs(t, oversized, ErrUpstreamResponseTooLarge)
	assert.NotContains(t, oversized.Error(), "do-not-reflect")

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	WriteUpstreamError(ctx, oversized)
	assert.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "do-not-reflect")

	unreadable := HandleErrorResponse(&http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(&partialErrorReader{}),
	})
	assert.ErrorIs(t, unreadable, errInjectedUpstreamRead)
	assert.NotContains(t, unreadable.Error(), "attacker-controlled")

	direct := &UpstreamError{
		StatusCode: http.StatusBadRequest,
		Body:       "direct-do-not-reflect:" + strings.Repeat("z", int(MaxUpstreamErrorBodyBytes)),
	}
	assert.NotContains(t, direct.Error(), "direct-do-not-reflect")
	directRecorder := httptest.NewRecorder()
	directContext, _ := gin.CreateTestContext(directRecorder)
	WriteUpstreamError(directContext, direct)
	assert.Equal(t, http.StatusBadRequest, directRecorder.Code)
	assert.NotContains(t, directRecorder.Body.String(), "direct-do-not-reflect")

	fromEnvelope := UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: strings.Repeat("private-upstream-detail", int(MaxUpstreamErrorBodyBytes)),
	}, http.StatusBadRequest)
	assert.ErrorIs(t, fromEnvelope, ErrUpstreamResponseTooLarge)
	assert.NotContains(t, fromEnvelope.Error(), "private-upstream-detail")
}

func TestUpstreamErrorStringAndClientResponseOmitReflectedCredentials(t *testing.T) {
	const upstreamSecret = "sk-live-upstream-echoed-secret"
	err := &UpstreamError{
		StatusCode: http.StatusBadRequest,
		Body:       `{"error":{"message":"` + upstreamSecret + `"}}`,
	}
	assert.NotContains(t, err.Error(), upstreamSecret)
	assert.Equal(t, "upstream returned status 400", err.Error())

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	WriteUpstreamError(ctx, err)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), upstreamSecret)
	assert.Contains(t, recorder.Body.String(), "[REDACTED")

	opaqueSecret := "opaque-provider-credential"
	sanitized := SanitizeUpstreamError(&UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       `{"error":{"message":"rate limited: ` + opaqueSecret + `"}}`,
	}, opaqueSecret)
	opaqueRecorder := httptest.NewRecorder()
	opaqueContext, _ := gin.CreateTestContext(opaqueRecorder)
	WriteUpstreamError(opaqueContext, sanitized)
	assert.Equal(t, http.StatusTooManyRequests, opaqueRecorder.Code)
	assert.NotContains(t, opaqueRecorder.Body.String(), opaqueSecret)
	assert.Contains(t, opaqueRecorder.Body.String(), "rate limited")

	compound := SanitizeUpstreamError(&UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       `{"error":{"message":"token-piece and application-piece"}}`,
	}, "token-piece|application-piece")
	compoundRecorder := httptest.NewRecorder()
	compoundContext, _ := gin.CreateTestContext(compoundRecorder)
	WriteUpstreamError(compoundContext, compound)
	assert.NotContains(t, compoundRecorder.Body.String(), "token-piece")
	assert.NotContains(t, compoundRecorder.Body.String(), "application-piece")
}

func TestSanitizeTransportErrorDropsCredentialBearingURL(t *testing.T) {
	unsafe := errors.New(`Post "https://provider.invalid/infer?access_token=provider-secret": dial failed`)
	sanitized := SanitizeTransportError(unsafe)
	assert.ErrorIs(t, sanitized, ErrUpstreamTransportFailed)
	assert.NotContains(t, sanitized.Error(), "provider-secret")
	assert.NotErrorIs(t, sanitized, unsafe)

	deadline := fmt.Errorf("request with secret: %w", context.DeadlineExceeded)
	sanitized = SanitizeTransportError(deadline)
	assert.ErrorIs(t, sanitized, ErrUpstreamTransportFailed)
	assert.ErrorIs(t, sanitized, context.DeadlineExceeded)
	assert.NotContains(t, sanitized.Error(), "secret")
}

func TestUpstreamSSEScannerBoundary(t *testing.T) {
	exact := NewUpstreamSSEScanner(strings.NewReader(strings.Repeat("x", MaxUpstreamSSEEventBytes) + "\n"))
	require.True(t, exact.Scan())
	assert.Len(t, exact.Bytes(), MaxUpstreamSSEEventBytes)
	assert.False(t, exact.Scan())
	assert.NoError(t, exact.Err())

	over := NewUpstreamSSEScanner(strings.NewReader(strings.Repeat("x", MaxUpstreamSSEEventBytes+1) + "\n"))
	assert.False(t, over.Scan())
	assert.Error(t, over.Err())
}
