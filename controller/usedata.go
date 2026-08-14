package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/service"
)

// maxFlowQuotaSpanSeconds is the reference 1-month (30-day) span limit for
// self-service flow/quota data queries.
const maxFlowQuotaSpanSeconds = 2592000

// parseFlowQuotaTimeRange parses and validates the reference
// start_timestamp/end_timestamp pair (both required, end >= start).
func parseFlowQuotaTimeRange(c *gin.Context) (int64, int64, bool) {
	startTimestamp, err := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	if err != nil || startTimestamp <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid start_timestamp"})
		return 0, 0, false
	}
	endTimestamp, err := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	if err != nil || endTimestamp <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid end_timestamp"})
		return 0, 0, false
	}
	if endTimestamp < startTimestamp {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid time range"})
		return 0, 0, false
	}
	return startTimestamp, endTimestamp, true
}

// GetAllQuotaDates returns the admin quota histogram (reference /api/data/).
func GetAllQuotaDates(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	dates, err := service.GetAllQuotaDates(startTimestamp, endTimestamp, username)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": dates})
}

// GetQuotaDatesByUser returns the admin per-(username, hour) histogram
// (reference /api/data/users).
func GetQuotaDatesByUser(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	dates, err := service.GetQuotaDataGroupByUser(startTimestamp, endTimestamp)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": dates})
}

// GetUserQuotaDates returns the authenticated user's per-(model, hour)
// histogram with the reference 1-month span limit (reference /api/data/self).
func GetUserQuotaDates(c *gin.Context) {
	userId := common.GetUserId(c)
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	if endTimestamp-startTimestamp > maxFlowQuotaSpanSeconds {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "时间跨度不能超过 1 个月"})
		return
	}
	dates, err := service.GetQuotaDataByUserId(userId, startTimestamp, endTimestamp)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": dates})
}

// GetAllFlowQuotaDates returns the role-scoped flow histogram for the admin
// console (reference /api/data/flow).
func GetAllFlowQuotaDates(c *gin.Context) {
	startTimestamp, endTimestamp, ok := parseFlowQuotaTimeRange(c)
	if !ok {
		return
	}
	username := c.Query("username")
	dates, err := service.GetFlowQuotaData(startTimestamp, endTimestamp, username, 0, common.GetRole(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": dates})
}

// GetUserFlowQuotaDates returns the authenticated user's flow histogram with
// the reference validations (reference /api/data/flow/self).
func GetUserFlowQuotaDates(c *gin.Context) {
	userId := common.GetUserId(c)
	startTimestamp, endTimestamp, ok := parseFlowQuotaTimeRange(c)
	if !ok {
		return
	}
	if endTimestamp-startTimestamp > maxFlowQuotaSpanSeconds {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "时间跨度不能超过 1 个月"})
		return
	}
	dates, err := service.GetFlowQuotaData(startTimestamp, endTimestamp, "", userId, constant.RoleCommonUser)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": dates})
}
