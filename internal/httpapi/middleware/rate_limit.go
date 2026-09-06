package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"math"
	"net/http"
	"time"
)

const (
	defaultGlobalAPIRateLimit                = 360
	defaultGlobalAPIRateLimitDurationSeconds = 180
	defaultCriticalRateLimit                 = 20
	defaultCriticalRateLimitDurationSeconds  = 20 * 60
	maxRateLimitWindowSeconds                = (math.MaxInt64 - int64(time.Second)) / int64(time.Second)
)

type rateLimitConfig struct {
	enabled bool
	limit   int
	window  time.Duration
}

func loadRateLimitConfig(enableEnv, limitEnv, durationEnv string, defaultLimit, defaultDurationSeconds int) rateLimitConfig {
	limit := env.GetEnvInt(limitEnv, defaultLimit)
	if limit <= 0 {
		limit = defaultLimit
	}

	durationSeconds := env.GetEnvInt(durationEnv, defaultDurationSeconds)
	if durationSeconds <= 0 || int64(durationSeconds) > maxRateLimitWindowSeconds {
		durationSeconds = defaultDurationSeconds
	}

	return rateLimitConfig{
		enabled: env.GetEnvBool(enableEnv, true),
		limit:   limit,
		window:  time.Duration(durationSeconds) * time.Second,
	}
}

func globalAPIRateLimitConfig() rateLimitConfig {
	return loadRateLimitConfig(
		"GLOBAL_API_RATE_LIMIT_ENABLE",
		"GLOBAL_API_RATE_LIMIT",
		"GLOBAL_API_RATE_LIMIT_DURATION",
		defaultGlobalAPIRateLimit,
		defaultGlobalAPIRateLimitDurationSeconds,
	)
}

func criticalRateLimitConfig() rateLimitConfig {
	return loadRateLimitConfig(
		"CRITICAL_RATE_LIMIT_ENABLE",
		"CRITICAL_RATE_LIMIT",
		"CRITICAL_RATE_LIMIT_DURATION",
		defaultCriticalRateLimit,
		defaultCriticalRateLimitDurationSeconds,
	)
}

func rateLimitDisabled() gin.HandlerFunc {
	return func(c *gin.Context) { c.Next() }
}

// GlobalRateLimit bounds the total request rate per client IP. Best-effort: a
// store failure fails open (logged) rather than taking the gateway down.
func GlobalRateLimit() gin.HandlerFunc {
	config := globalAPIRateLimitConfig()
	if !config.enabled {
		return rateLimitDisabled()
	}
	return buildRateLimit(cache.RateLimitPrefixGlobal, config.limit, config.window, func(c *gin.Context) string {
		return c.ClientIP()
	})
}

// CriticalRateLimit is a tighter limiter for sensitive endpoints (login,
// register, password reset, OAuth) to slow brute-force and abuse.
func CriticalRateLimit() gin.HandlerFunc {
	config := criticalRateLimitConfig()
	if !config.enabled {
		return rateLimitDisabled()
	}
	return buildRateLimit(cache.RateLimitPrefixCritical, config.limit, config.window, func(c *gin.Context) string {
		return c.ClientIP()
	})
}

// UserRateLimit bounds the per-user request rate on a named resource.
func UserRateLimit(name string, limit int) gin.HandlerFunc {
	return buildRateLimit(cache.RateLimitPrefixUser+":"+name, limit, time.Minute, func(c *gin.Context) string {
		return textutil.Int2Str(requestctx.GetUserId(c))
	})
}

// buildRateLimit constructs a fixed-window counter limiter middleware.
func buildRateLimit(prefix string, limit int, window time.Duration, keyFunc func(*gin.Context) string) gin.HandlerFunc {
	limiter := cache.NewRateLimiter(limit, window, prefix)
	return func(c *gin.Context) {
		key := keyFunc(c)
		if key == "" {
			key = "unknown"
		}
		allowed, err := limiter.Allow(c.Request.Context(), key)
		if err != nil {
			logging.Logger.Warn("rate limiter store error; failing open", "err", err.Error())
			c.Next()
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": map[string]any{
					"message": "请求过于频繁，请稍后再试",
					"type":    "rate_limit_error",
					"code":    contract.ErrorCodeRateLimitExceeded,
				},
			})
			return
		}
		c.Next()
	}
}

// UserCriticalRateLimit bounds a per-user critical action (e.g. access-token
// generation) to CRITICAL_RATE_LIMIT requests per
// CRITICAL_RATE_LIMIT_DURATION seconds (reference: 20 per 20 minutes). Disabled with
// CRITICAL_RATE_LIMIT_ENABLE=false.
func UserCriticalRateLimit(scope string) gin.HandlerFunc {
	config := criticalRateLimitConfig()
	if !config.enabled {
		return rateLimitDisabled()
	}
	return buildRateLimit("UC:"+scope, config.limit, config.window, func(c *gin.Context) string {
		if uid := requestctx.GetUserId(c); uid != 0 {
			return textutil.Int2Str(uid)
		}
		return c.ClientIP()
	})
}
