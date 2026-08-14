package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetUptimeKumaStatus returns a small status payload for Uptime Kuma HTTP
// monitoring (200 when the gateway is up).
func GetUptimeKumaStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "up",
		"version":   common.Version,
		"node_name": service.NodeName(),
		"uptime":    common.NowTimestamp() - StartTime,
	})
}

// GetPerfMetricsSummary aggregates recent relay usage into summary metrics.
func GetPerfMetricsSummary(c *gin.Context) {
	cutoff := common.NowTimestamp() - 86400
	type summary struct {
		RequestCount  int64 `json:"request_count"`
		TotalTokens   int64 `json:"total_tokens"`
		TotalQuota    int64 `json:"total_quota"`
	}
	var s summary
	model.LOG_DB.Model(&model.Log{}).
		Where("created_at > ? AND type = ?", cutoff, service.LogTypeConsume).
		Select("COUNT(*) as request_count, COALESCE(SUM(prompt_tokens + completion_tokens),0) as total_tokens, COALESCE(SUM(quota),0) as total_quota").
		Scan(&s)
	c.JSON(http.StatusOK, dto.Ok(s))
}
