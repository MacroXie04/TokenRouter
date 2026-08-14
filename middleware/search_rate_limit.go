package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// SearchRateLimit bounds search-endpoint load per user (SEARCH_RATE_LIMIT per
// minute, default 10; disable with SEARCH_RATE_LIMIT_ENABLE=false), matching
// the reference's SearchRateLimit on /api/token/search.
func SearchRateLimit() gin.HandlerFunc {
	if !common.GetEnvBool("SEARCH_RATE_LIMIT_ENABLE", true) {
		return func(c *gin.Context) { c.Next() }
	}
	limit := common.GetEnvInt("SEARCH_RATE_LIMIT", 10)
	return buildRateLimit("SR", limit, time.Minute, func(c *gin.Context) string {
		if uid := common.GetUserId(c); uid != 0 {
			return common.Int2Str(uid)
		}
		return c.ClientIP()
	})
}
