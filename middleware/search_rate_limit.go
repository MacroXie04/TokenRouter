package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	defaultSearchRateLimit                = 10
	defaultSearchRateLimitDurationSeconds = 60
)

func searchRateLimitConfig() rateLimitConfig {
	return loadRateLimitConfig(
		"SEARCH_RATE_LIMIT_ENABLE",
		"SEARCH_RATE_LIMIT",
		"SEARCH_RATE_LIMIT_DURATION",
		defaultSearchRateLimit,
		defaultSearchRateLimitDurationSeconds,
	)
}

// SearchRateLimit bounds search-endpoint load per user (SEARCH_RATE_LIMIT per
// minute, default 10; disable with SEARCH_RATE_LIMIT_ENABLE=false), matching
// the reference's SearchRateLimit on /api/token/search.
func SearchRateLimit() gin.HandlerFunc {
	config := searchRateLimitConfig()
	if !config.enabled {
		return func(c *gin.Context) { c.Next() }
	}
	return buildRateLimit("SR", config.limit, config.window, func(c *gin.Context) string {
		if uid := common.GetUserId(c); uid != 0 {
			return common.Int2Str(uid)
		}
		return c.ClientIP()
	})
}
