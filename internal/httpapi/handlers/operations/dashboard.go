package operations

import (
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
)

// GetDashboardData returns admin dashboard counters (TokenRouter extension at
// /api/dashboard/stats — the reference /api/data serves the quota histogram).
func GetDashboardData(c *gin.Context) {
	var userCount, tokenCount, channelCount, requestCount int64
	if err := model.DB.Model(&model.User{}).Count(&userCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	if err := model.DB.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	if err := model.DB.Model(&model.Channel{}).Count(&channelCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	if err := model.LOG_DB.Model(&model.Log{}).Where("type = ?", billingsvc.LogTypeConsume).Count(&requestCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询仪表盘数据失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"user_count":    userCount,
		"token_count":   tokenCount,
		"channel_count": channelCount,
		"request_count": requestCount,
	}))
}

// GetSystemInstances returns the registered cluster nodes (admin).
func GetSystemInstances(c *gin.Context) {
	instances, err := operationssvc.GetSystemInstances()
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询实例失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(instances))
}
