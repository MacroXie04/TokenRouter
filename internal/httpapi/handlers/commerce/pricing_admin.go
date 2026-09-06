package commerce

import (
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"net/http"
)

// ResetModelRatio restores the built-in pricing baseline and updates both the
// persisted options and live billing registry.
func ResetModelRatio(c *gin.Context) {
	if err := billingsvc.ResetModelPricingDefaults(); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage, "option.reset_ratio")
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "重置模型倍率成功"})
}
