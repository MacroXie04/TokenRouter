package operations

import (
	"github.com/gin-gonic/gin"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	"net/http"
	"strconv"
)

func GetPerfMetricsSummary(c *gin.Context) {
	result, err := operationssvc.QueryPerfMetricsSummary(perfMetricHours(c), operationssvc.ActivePerfMetricGroups())
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
	result, err := operationssvc.QueryPerfMetrics(modelName, c.Query("group"), perfMetricHours(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	result.Groups = operationssvc.FilterActivePerfMetricGroups(result.Groups)
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
