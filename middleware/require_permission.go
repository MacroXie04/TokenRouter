package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// RequirePermission gates a route on a fine-grained permission. It must run
// after an authentication middleware that populates id/role in the context
// (AdminAuth or RootAuth). A superuser passes unconditionally; other subjects
// are checked against per-user overrides and role baselines.
func RequirePermission(permission service.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := common.GetRole(c)
		userID := common.GetUserId(c)
		if service.Can(userID, role, permission) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "无权限"})
	}
}
