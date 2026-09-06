package relaycommon

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tokenrouter/tokenrouter/protocolkit"
)

const (
	GrokViolationErrorCode          = "violation_fee.grok.csam"
	GrokCSAMViolationMarker         = "Failed check: SAFETY_CHECK_TYPE"
	GrokContentUsageViolationMarker = "Content violates usage guidelines"
	grokViolationPublicMessage      = "Request rejected by the xAI safety policy."
)

// NormalizeGrokViolationError recognizes only xAI's exact, case-sensitive
// safety markers inside the provider error field. It replaces the complete
// provider-controlled body with a stable, content-free error and marks even a
// 5xx response as non-retryable so a policy rejection cannot be replayed to a
// second channel.
func NormalizeGrokViolationError(err error) error {
	if err == nil {
		return nil
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream == nil {
		return err
	}
	if upstream.NormalizedCode == GrokViolationErrorCode {
		copy := *upstream
		copy.SkipRetry = true
		return &copy
	}
	if upstream.Cause != nil || !grokViolationBody(upstream.Body) {
		return err
	}
	status := upstream.StatusCode
	if status <= 0 {
		status = http.StatusBadRequest
	}
	body, marshalErr := protocolkit.MarshalJSON(map[string]any{
		"error": protocolkit.OpenAIError{
			Message: grokViolationPublicMessage,
			Type:    GrokViolationErrorCode,
			Code:    GrokViolationErrorCode,
		},
	})
	if marshalErr != nil {
		// The envelope contains constants only; retain a safe generic body if an
		// encoder failure is injected rather than ever falling back to upstream
		// content.
		body = []byte(`{"error":{"message":"Request rejected by the xAI safety policy.","type":"violation_fee.grok.csam","code":"violation_fee.grok.csam"}}`)
	}
	return &UpstreamError{
		StatusCode:     status,
		Body:           string(body),
		SkipRetry:      true,
		NormalizedCode: GrokViolationErrorCode,
	}
}

// IsGrokViolationError reports whether an error was normalized from a trusted
// xAI channel's exact safety marker. Provider-supplied error codes alone never
// satisfy this check.
func IsGrokViolationError(err error) bool {
	var upstream *UpstreamError
	return errors.As(err, &upstream) && upstream != nil &&
		upstream.NormalizedCode == GrokViolationErrorCode && upstream.SkipRetry
}

func grokViolationBody(body string) bool {
	if body == "" || int64(len(body)) > MaxUpstreamErrorBodyBytes {
		return false
	}
	containsMarker := func(message string) bool {
		return strings.Contains(message, GrokCSAMViolationMarker) ||
			strings.Contains(message, GrokContentUsageViolationMarker)
	}

	// Accept the two provider shapes observed at the xAI boundary: an OpenAI
	// error object and an error string. Do not recursively scan arbitrary JSON,
	// where a marker could merely be echoed from request content or metadata.
	var envelope struct {
		Error any `json:"error"`
	}
	if protocolkit.UnmarshalJSON([]byte(body), &envelope) == nil && envelope.Error != nil {
		switch value := envelope.Error.(type) {
		case string:
			return containsMarker(value)
		case map[string]any:
			message, _ := value["message"].(string)
			return containsMarker(message)
		}
		return false
	}

	// Some xAI-compatible gateways return a plain-text error. Only a body that
	// consists of that bounded provider error is inspected.
	trimmed := strings.TrimSpace(body)
	return trimmed != "" && containsMarker(trimmed)
}
