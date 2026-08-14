package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
)

// GlobalRateLimit bounds the total request rate per client IP. Best-effort: a
// store failure fails open (logged) rather than taking the gateway down.
func GlobalRateLimit() gin.HandlerFunc {
	limit := common.GetEnvInt("GLOBAL_API_RATE_LIMIT", 120)
	return buildRateLimit(constant.RateLimitPrefixGlobal, limit, time.Minute, func(c *gin.Context) string {
		return c.ClientIP()
	})
}

// CriticalRateLimit is a tighter limiter for sensitive endpoints (login,
// register, password reset, OAuth) to slow brute-force and abuse.
func CriticalRateLimit() gin.HandlerFunc {
	limit := common.GetEnvInt("CRITICAL_RATE_LIMIT", 10)
	return buildRateLimit(constant.RateLimitPrefixCritical, limit, time.Minute, func(c *gin.Context) string {
		return c.ClientIP()
	})
}

// UserRateLimit bounds the per-user request rate on a named resource.
func UserRateLimit(name string, limit int) gin.HandlerFunc {
	return buildRateLimit(constant.RateLimitPrefixUser+":"+name, limit, time.Minute, func(c *gin.Context) string {
		return common.Int2Str(common.GetUserId(c))
	})
}

// buildRateLimit constructs a fixed-window counter limiter middleware.
func buildRateLimit(prefix string, limit int, window time.Duration, keyFunc func(*gin.Context) string) gin.HandlerFunc {
	limiter := common.NewRateLimiter(limit, window, prefix)
	return func(c *gin.Context) {
		key := keyFunc(c)
		if key == "" {
			key = "unknown"
		}
		allowed, err := limiter.Allow(c.Request.Context(), key)
		if err != nil {
			common.Logger.Warn("rate limiter store error; failing open", "err", err.Error())
			c.Next()
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": map[string]any{
					"message": "请求过于频繁，请稍后再试",
					"type":    "rate_limit_error",
					"code":    constant.ErrorCodeRateLimitExceeded,
				},
			})
			return
		}
		c.Next()
	}
}
