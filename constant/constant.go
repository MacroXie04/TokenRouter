package constant

// Rate-limit key prefixes (global / critical / user / search / email / model).
const (
	RateLimitPrefixGlobal   = "global"
	RateLimitPrefixCritical = "critical"
	RateLimitPrefixUser     = "user"
	RateLimitPrefixSearch   = "search"
	RateLimitPrefixEmail    = "email"
	RateLimitPrefixModel    = "model"
)

// Common HTTP content types.
const (
	ContentTypeJSON          = "application/json"
	ContentTypeStream        = "text/event-stream"
	ContentTypeEventStream   = "text/event-stream"
)

// Common relay error messages (stable, user-facing).
const (
	ErrorCodeInsufficientQuota = "insufficient_quota"
	ErrorCodeInvalidRequest    = "invalid_request_error"
	ErrorCodeChannelNotAvailable = "channel_not_available"
	ErrorCodeRateLimitExceeded = "rate_limit_exceeded"
	ErrorCodeUnauthorized      = "unauthorized"
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

// Roles as stored on tokens (aliases of common role constants).
const (
	TokenRoleUser  = 1
	TokenRoleAdmin = 10
	TokenRoleRoot  = 100
)

// Role constants for authorization checks.
const (
	RoleCommonUser = 1
	RoleAdminUser  = 10
	RoleRootUser   = 100
)
