package controller

import (
	"net/http"

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
