package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
)

// EmailVerificationRateLimit bounds verification-email sends per client IP
// (2 per 30-second window) to slow mail-bombing abuse. The window lives in the
// shared KV store so multi-node deployments share it when Redis is configured.
const (
	emailVerificationRateLimitMark = "EV"
	emailVerificationMaxRequests   = 2
	emailVerificationWindowSeconds = 30
)

// emailVerificationFallback is the process-local limiter used when the shared
// store errors (mirroring the reference's memory fallback).
var emailVerificationFallback = common.NewRateLimiter(
	emailVerificationMaxRequests,
	emailVerificationWindowSeconds*time.Second,
	"rate:"+emailVerificationRateLimitMark,
)

// EmailVerificationRateLimit is the gin middleware for the limit above.
func EmailVerificationRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := context.Background()
		storeKey := fmt.Sprintf("rate:%s:%s", emailVerificationRateLimitMark, c.ClientIP())
		count, err := common.Store.Incr(ctx, storeKey)
		if err == nil {
			if count == 1 {
				_ = common.Store.Expire(ctx, storeKey, emailVerificationWindowSeconds*time.Second)
			}
			if count <= emailVerificationMaxRequests {
				c.Next()
				return
			}
			ttl, _ := common.Store.TTL(ctx, storeKey)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"success": false,
				"message": fmt.Sprintf("发送过于频繁，请等待 %d 秒后再试", int64(ttl.Seconds())+1),
			})
			c.Abort()
			return
		}
		allowed, _ := emailVerificationFallback.Allow(ctx, c.ClientIP())
		if allowed {
			c.Next()
			return
		}
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"message": "发送过于频繁，请稍后再试",
		})
		c.Abort()
	}
}
