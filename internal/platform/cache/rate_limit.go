package cache

// Rate-limit key prefixes (global / critical / user / search / email / model).
const (
	RateLimitPrefixGlobal   = "global"
	RateLimitPrefixCritical = "critical"
	RateLimitPrefixUser     = "user"
	RateLimitPrefixSearch   = "search"
	RateLimitPrefixEmail    = "email"
	RateLimitPrefixModel    = "model"
)
