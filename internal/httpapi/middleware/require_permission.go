package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"net/http"
)

// RequirePermission gates a route on a fine-grained permission. It must run
// after an authentication middleware that populates id/role in the context
// (AdminAuth or RootAuth). A superuser passes unconditionally; other subjects
// are checked against per-user overrides and role baselines.
func RequirePermission(permission auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := requestctx.GetRole(c)
		userID := requestctx.GetUserId(c)
		if auth.Can(userID, role, permission) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
	}
}
