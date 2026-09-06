package contract

// Common relay error messages (stable, user-facing).
const (
	ErrorCodeInsufficientQuota   = "insufficient_quota"
	ErrorCodeInvalidRequest      = "invalid_request_error"
	ErrorCodeChannelNotAvailable = "channel_not_available"
	ErrorCodeRateLimitExceeded   = "rate_limit_exceeded"
	ErrorCodeUnauthorized        = "unauthorized"
)

// Finish reasons.
const (
	FinishReasonStop          = "stop"
	FinishReasonLength        = "length"
	FinishReasonToolCalls     = "tool_calls"
	FinishReasonContentFilter = "content_filter"
	FinishReasonFunctionCall  = "function_call"
)

// Token type limits (see validators; these are safety bounds).
const (
	MaxTokenCount = 1 << 30 // 1 Gi tokens, far beyond any real request
)
