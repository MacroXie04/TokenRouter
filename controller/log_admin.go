package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetLogsStat aggregates consumed quota and the trailing-minute rpm/tpm for
// the admin console (reference contract).
func GetLogsStat(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := service.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
}

// GetLogsSelfStat aggregates the authenticated user's consumed quota and the
// trailing-minute rpm/tpm (reference contract).
func GetLogsSelfStat(c *gin.Context) {
	user, err := service.GetUserByID(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户不存在"})
		return
	}
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := service.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, user.Username, tokenName, channel, group)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
}

// SearchAllLogs is deprecated in the reference (the frontend uses the
// filtered list endpoint); the reference response is reproduced.
func SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": false, "message": "该接口已废弃"})
}

// SearchUserLogs is deprecated in the reference; the reference response is
// reproduced.
func SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": false, "message": "该接口已废弃"})
}

// GetUserLogs lists the authenticated user's logs with the reference filter
// contract and the redacted user-facing view.
func GetUserLogs(c *gin.Context) {
	pi := getPageQuery(c)
	userId := common.GetUserId(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := service.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName,
		(pi.Page-1)*pi.PageSize, pi.PageSize, group, requestId, upstreamRequestId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	pi.Total = int(total)
	pi.Items = logs
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": pi})
}

// GetLogByKey lists the recent logs produced with the authenticated relay
// token (reference contract).
func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt(common.ContextKeyTokenId)
	if tokenId == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无效的令牌"})
		return
	}
	logs, err := service.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": logs})
}

// GetLogs lists logs for the admin console with the reference filter
// contract (type, timestamps, username, token/model names, channel, group,
// request ids) and pageInfo paging.
func GetLogs(c *gin.Context) {
	pi := getPageQuery(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := service.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName,
		(pi.Page-1)*pi.PageSize, pi.PageSize, channel, group, requestId, upstreamRequestId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	pi.Total = int(total)
	pi.Items = logs
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": pi})
}
