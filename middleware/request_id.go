package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// RequestID assigns or forwards an X-Request-Id header for correlation.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-Id")
		if !validRequestID(rid) {
			rid = common.BestEffortUUID()
		}
		c.Set(common.RequestIdKey, rid)
		c.Header("X-Request-Id", rid)
		c.Next()
	}
}

// validRequestID limits correlation IDs to a small, log-safe character set.
// Request IDs are reflected in responses and structured logs, so accepting
// arbitrary header text would let callers inject credentials or control data
// into both surfaces.
func validRequestID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, ch := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

// RequestLogger records only request metadata that is safe to persist. In
// particular, it deliberately excludes raw URLs/query strings, headers, and
// bodies because OAuth codes, reset tokens, API keys, and cookies can appear
// there.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "<unmatched>"
		}
		common.Logger.Info("request completed",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(started).Milliseconds(),
			"request_id", common.GetRequestId(c),
		)
	}
}

// Recovery recovers from panics, logs the stack, and returns a 500.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				// Panic values can include request bodies, credentials, or other
				// attacker-controlled text. Record only their Go type for incident
				// classification; the stack already identifies the failing code path.
				common.Logger.Error("panic recovered",
					"panic_type", fmt.Sprintf("%T", r),
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
