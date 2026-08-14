package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// ResetModelRatio restores the built-in pricing baseline and updates both the
// persisted options and live billing registry.
func ResetModelRatio(c *gin.Context) {
	if err := service.ResetModelPricingDefaults(); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage, "option.reset_ratio")
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "重置模型倍率成功"})
}
