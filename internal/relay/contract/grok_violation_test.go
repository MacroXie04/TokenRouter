package contract

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGrokViolationErrorUsesStableContentFreeEnvelopeAndSkipsRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, body := range map[string]string{
		"OpenAI error object":   `{"error":{"message":"private prompt: Failed check: SAFETY_CHECK_TYPE secret-tail","type":"invalid_request_error"}}`,
		"provider error string": `{"error":"Content violates usage guidelines: private payload"}`,
		"plain text gateway":    `Failed check: SAFETY_CHECK_TYPE private plaintext`,
	} {
		t.Run(name, func(t *testing.T) {
			normalized := NormalizeGrokViolationError(&UpstreamError{StatusCode: http.StatusBadGateway, Body: body})
			require.True(t, IsGrokViolationError(normalized))
			assert.False(t, IsRetryableUpstreamError(normalized), "a violation must not retry even when xAI used a 5xx status")
			assert.NotContains(t, normalized.Error(), "private")

			var upstream *UpstreamError
			require.True(t, errors.As(normalized, &upstream))
			assert.Equal(t, http.StatusBadGateway, upstream.StatusCode)
			assert.Equal(t, GrokViolationErrorCode, upstream.NormalizedCode)
			assert.NotContains(t, upstream.Body, "private")
			assert.NotContains(t, upstream.Body, GrokCSAMViolationMarker)
			assert.Contains(t, upstream.Body, `"code":"violation_fee.grok.csam"`)

			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			WriteUpstreamError(context, normalized)
			assert.Equal(t, http.StatusBadGateway, recorder.Code)
			assert.NotContains(t, recorder.Body.String(), "private")
			assert.Contains(t, recorder.Body.String(), `"type":"violation_fee.grok.csam"`)
		})
	}
}

func TestNormalizeGrokViolationErrorRejectsUntrustedLookalikes(t *testing.T) {
	lookalikes := []error{
		fmt.Errorf("transport wrapper: %s", GrokCSAMViolationMarker),
		&UpstreamError{StatusCode: 400, Cause: fmt.Errorf("read failed: %s", GrokCSAMViolationMarker)},
		&UpstreamError{StatusCode: 400, Body: `{"metadata":{"message":"Failed check: SAFETY_CHECK_TYPE"},"error":{"message":"ordinary rejection"}}`},
		&UpstreamError{StatusCode: 400, Body: `{"error":{"message":"failed check: safety_check_type"}}`},
		&UpstreamError{StatusCode: 400, Body: `{"error":{"message":"ordinary rejection","code":"violation_fee.grok.csam"}}`},
		&UpstreamError{StatusCode: 400, Body: strings.Repeat("x", int(MaxUpstreamErrorBodyBytes)+1) + GrokCSAMViolationMarker},
	}
	for _, original := range lookalikes {
		normalized := NormalizeGrokViolationError(original)
		assert.False(t, IsGrokViolationError(normalized), "%T: %v", original, original)
	}
}

func TestNormalizeGrokViolationErrorIsIdempotent(t *testing.T) {
	first := NormalizeGrokViolationError(&UpstreamError{
		StatusCode: 400,
		Body:       `{"error":{"message":"Content violates usage guidelines"}}`,
	})
	second := NormalizeGrokViolationError(first)
	require.True(t, IsGrokViolationError(second))
	var firstUpstream, secondUpstream *UpstreamError
	require.True(t, errors.As(first, &firstUpstream))
	require.True(t, errors.As(second, &secondUpstream))
	assert.Equal(t, firstUpstream.Body, secondUpstream.Body)
}
