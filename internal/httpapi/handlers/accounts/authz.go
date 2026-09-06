package accounts

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"net/http"
)

// GetPermissionCatalog returns the permission schema used by the client to
// render the permission editor: the registry of resources with their actions
// and display label keys, plus the roles with their baseline grant matrices
// (reference contract).
func GetPermissionCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"resources": auth.PermissionCatalog(),
			"roles":     auth.PermissionRoles(),
		},
	})
}
