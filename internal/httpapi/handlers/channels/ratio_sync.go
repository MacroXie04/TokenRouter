package channels

import (
	"errors"
	"github.com/gin-gonic/gin"
	catalogsvc "github.com/tokenrouter/tokenrouter/internal/catalog"
	"net/http"
)

// GetSyncableChannels returns a root-only, credential-free list of channel
// pricing sources plus the official and models.dev presets.
func GetSyncableChannels(c *gin.Context) {
	channels, err := catalogsvc.ListRatioSyncChannels(c.Request.Context())
	if err != nil {
		// Database details can include DSNs or driver state. Keep them behind the
		// server boundary rather than reflecting them into the admin dashboard.
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "查询渠道失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    channels,
	})
}

// FetchUpstreamRatios fetches selected upstream pricing data and returns a
// comparison against TokenRouter's live pricing source of truth.
func FetchUpstreamRatios(c *gin.Context) {
	var request catalogsvc.RatioSyncRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "请求参数格式错误",
		})
		return
	}

	data, err := catalogsvc.FetchRatioSyncData(c.Request.Context(), request)
	if err != nil {
		switch {
		case errors.Is(err, catalogsvc.ErrRatioSyncNoUpstreams):
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "无有效上游渠道"})
		case errors.Is(err, catalogsvc.ErrRatioSyncInvalidRequest):
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请求参数无效"})
		case errors.Is(err, catalogsvc.ErrRatioSyncChannelQuery):
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "查询渠道失败"})
		case errors.Is(err, catalogsvc.ErrRatioSyncOutputTooLarge):
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "上游倍率差异过大"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "获取上游倍率失败"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    data,
	})
}
