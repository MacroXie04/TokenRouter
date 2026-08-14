package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
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

func GetPerfMetricsSummary(c *gin.Context) {
	result, err := service.QueryPerfMetricsSummary(perfMetricHours(c), service.ActivePerfMetricGroups())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func GetPerfMetrics(c *gin.Context) {
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "model is required"})
		return
	}
	result, err := service.QueryPerfMetrics(modelName, c.Query("group"), perfMetricHours(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	result.Groups = service.FilterActivePerfMetricGroups(result.Groups)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func perfMetricHours(c *gin.Context) int {
	hours := 24
	if raw := c.Query("hours"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			hours = parsed
		}
	}
	return hours
}
