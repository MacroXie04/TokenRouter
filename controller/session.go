package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetLoginSessions lists the authenticated user's login sessions.
func GetLoginSessions(c *gin.Context) {
	sessions := service.GetUserSessions(common.GetUserId(c))
	c.JSON(http.StatusOK, dto.Ok(sessions))
}

// DeleteLoginSession revokes a specific session by id.
func DeleteLoginSession(c *gin.Context) {
	sid := c.Param("sid")
	if sid == "" {
		c.JSON(http.StatusBadRequest, dto.Fail("缺少会话 ID"))
		return
	}
	if err := service.RevokeSession(sid); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("撤销失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("已撤销"))
}

// RevokeOtherSessions revokes every session of the user except the current one.
func RevokeOtherSessions(c *gin.Context) {
	sid, ok := currentSid(c)
	if !ok {
		c.JSON(http.StatusBadRequest, dto.Fail("缺少刷新令牌"))
		return
	}
	if err := service.RevokeOtherSessions(common.GetUserId(c), sid); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("撤销失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("其他会话已撤销"))
}

// currentSid extracts the current session id from the refresh cookie.
func currentSid(c *gin.Context) (string, bool) {
	cookie, err := c.Cookie(refreshCookie)
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", false
	}
	return parts[0], true
}
