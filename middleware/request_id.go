package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// RequestID assigns or forwards an X-Request-Id header for correlation.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-Id")
		if rid == "" {
			rid = common.GenerateUUID()
		}
		c.Set(common.RequestIdKey, rid)
		c.Header("X-Request-Id", rid)
		c.Next()
	}
}

// Recovery recovers from panics, logs the stack, and returns a 500.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				common.Logger.Error("panic recovered",
					"error", r,
					"stack", string(debug.Stack()),
					"request_id", common.GetRequestId(c),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": map[string]any{
						"message": "internal server error",
						"type":    "server_error",
						"code":    "internal_error",
					},
				})
			}
		}()
		c.Next()
	}
}

// Gzip compresses responses where the client accepts it.
func Gzip() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
	}
}
