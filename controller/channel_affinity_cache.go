package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/service"
)

func GetChannelAffinityCacheStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    service.GetChannelAffinityCacheStats(),
	})
}

func ClearChannelAffinityCache(c *gin.Context) {
	if strings.TrimSpace(c.Query("all")) == "true" {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "",
			"data":    gin.H{"deleted": service.ClearChannelAffinityCacheAll()},
		})
		return
	}
	ruleName := strings.TrimSpace(c.Query("rule_name"))
	if ruleName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "缺少参数：rule_name，或使用 all=true 清空全部",
		})
		return
	}
	deleted, err := service.ClearChannelAffinityCacheByRuleName(ruleName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    gin.H{"deleted": deleted},
	})
}

func GetChannelAffinityUsageCacheStats(c *gin.Context) {
	ruleName := strings.TrimSpace(c.Query("rule_name"))
	if ruleName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "missing param: rule_name"})
		return
	}
	keyFingerprint := strings.TrimSpace(c.Query("key_fp"))
	if keyFingerprint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "missing param: key_fp"})
		return
	}
	usingGroup := strings.TrimSpace(c.Query("using_group"))
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    service.GetChannelAffinityUsageCacheStats(ruleName, usingGroup, keyFingerprint),
	})
}
