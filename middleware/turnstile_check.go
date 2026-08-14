package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// TurnstileCheck gates bot-protected endpoints with Cloudflare Turnstile.
// When Turnstile is not configured it fails open. The token is carried as the
// "turnstile" query parameter, matching the reference contract; rejection
// messages mirror the reference verbatim.
func TurnstileCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !service.TurnstileEnabled() {
			c.Next()
			return
		}
		response := c.Query("turnstile")
		if response == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "Turnstile token 为空",
			})
			c.Abort()
			return
		}
		ok, err := service.VerifyTurnstile(response, c.ClientIP())
		if err != nil {
			common.Logger.Warn("turnstile verify error", "err", err.Error())
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			c.Abort()
			return
		}
		if !ok {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "Turnstile 校验失败，请刷新重试！",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
