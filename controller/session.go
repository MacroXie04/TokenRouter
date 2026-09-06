package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetLoginSessions lists the authenticated user's login sessions.
func GetLoginSessions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	sessions, err := service.GetUserSessionsForCurrent(identity.UserID, identity.SessionID)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": sessions})
}

// DeleteLoginSession revokes a specific session by id.
func DeleteLoginSession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	sid := strings.TrimSpace(c.Param("sid"))
	if sid == "" || len(sid) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_ID_REQUIRED",
			"message": "session id is required",
		})
		return
	}
	revoked, err := service.RevokeUserSession(identity.UserID, sid, "user_revoked")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if !revoked {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_NOT_FOUND",
			"message": "session not found",
		})
		return
	}
	if sid == identity.SessionID {
		clearAuthCookies(c)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    gin.H{"revoked_sid": sid, "current": sid == identity.SessionID},
	})
}

// RevokeOtherSessions revokes every session of the user except the current one.
func RevokeOtherSessions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	count, err := service.RevokeOtherSessionsWithCount(identity.UserID, identity.SessionID)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    gin.H{"revoked_count": count},
	})
}

func requireLoginSession(c *gin.Context) (service.SessionIdentity, bool) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_REQUIRED",
			"message": "需要登录会话",
		})
	}
	return identity, ok
}

// currentSid resolves the already-validated access-token session identity.
// It never trusts a caller-controlled refresh-cookie SID.
func currentSid(c *gin.Context) (string, bool) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	return identity.SessionID, ok
}

func writeAuthSessionError(c *gin.Context, err error) {
	c.Header("Cache-Control", "no-store")
	status, code := service.AuthSessionErrorCode(err)
	if status == http.StatusInternalServerError {
		common.SysError(fmt.Sprintf("auth session request failed (%s %s): %v", c.Request.Method, c.Request.URL.Path, err))
	}
	c.JSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": http.StatusText(status),
	})
}
