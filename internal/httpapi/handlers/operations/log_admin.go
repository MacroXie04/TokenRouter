package operations

import (
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/handlers/pagination"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/url"
	"strconv"
)

const maxLogQueryBytes = 16 << 10

var logScalarQueryKeys = map[string]struct{}{
	"p": {}, "page_size": {}, "ps": {}, "size": {},
	"type": {}, "start_timestamp": {}, "end_timestamp": {},
	"token_name": {}, "username": {}, "model_name": {}, "channel": {},
	"group": {}, "request_id": {}, "upstream_request_id": {},
}

// validateLogQueryShape rejects oversized and ambiguous scalar query strings
// before the controller asks the database to aggregate or scan log rows.
func validateLogQueryShape(c *gin.Context) error {
	if c == nil || c.Request == nil || c.Request.URL == nil || len(c.Request.URL.RawQuery) > maxLogQueryBytes {
		return errors.New("查询参数过大")
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return errors.New("查询参数无效")
	}
	for key, items := range values {
		if _, scalar := logScalarQueryKeys[key]; scalar && len(items) != 1 {
			return errors.New("查询参数不能重复")
		}
	}
	return nil
}

func requireValidLogQuery(c *gin.Context) bool {
	if err := validateLogQueryShape(c); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return false
	}
	return true
}

// GetLogsStat aggregates consumed quota and the trailing-minute rpm/tpm for
// the admin console (reference contract).
func GetLogsStat(c *gin.Context) {
	if !requireValidLogQuery(c) {
		return
	}
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := billingsvc.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
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
	if !requireValidLogQuery(c) {
		return
	}
	user, err := userssvc.GetUserByID(requestctx.GetUserId(c))
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
	stat, err := billingsvc.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, user.Username, tokenName, channel, group)
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
	if !requireValidLogQuery(c) {
		return
	}
	pi := pagination.FromContext(c)
	userId := requestctx.GetUserId(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := billingsvc.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName,
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
	tokenId := c.GetInt(requestctx.ContextKeyTokenId)
	if tokenId == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无效的令牌"})
		return
	}
	logs, err := billingsvc.GetLogByTokenId(tokenId)
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
	if !requireValidLogQuery(c) {
		return
	}
	pi := pagination.FromContext(c)
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
	logs, total, err := billingsvc.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName,
		(pi.Page-1)*pi.PageSize, pi.PageSize, channel, group, requestId, upstreamRequestId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	pi.Total = int(total)
	pi.Items = logs
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": pi})
}
