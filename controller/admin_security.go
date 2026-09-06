package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// EmailBind binds a code-verified email to the signed-in user (POST
// /api/oauth/email/bind). The code must have been sent to that address.
func EmailBind(c *gin.Context) {
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("invalid request body"))
		return
	}
	userId := identity.UserID
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "not authenticated"})
		return
	}
	if err := service.VerifyAndBindEmail(userId, req.Email, req.Code); err != nil {
		switch {
		case errors.Is(err, service.ErrEmailAlreadyTaken):
			c.JSON(http.StatusBadRequest, dto.Fail("邮箱已被其他用户使用"))
		default:
			c.JSON(http.StatusBadRequest, dto.Fail("验证码错误或已过期"))
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

// canManageTargetRole reports whether an operator role is strictly higher than
// the target role. A root may manage lower roles but never itself or another
// root account.
func canManageTargetRole(myRole, targetRole int) bool {
	return myRole > targetRole
}

// AdminResetPasskey removes a target user's passkeys and revokes all of their
// sessions (DELETE /api/user/:id/reset_passkey).
func AdminResetPasskey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的用户 ID"))
		return
	}
	target, err := service.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	if !canManageTargetRole(common.GetRole(c), target.Role) {
		c.JSON(http.StatusForbidden, dto.Fail("no permission"))
		return
	}
	passkeyEnabled, err := service.PasskeyEnabledChecked(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询 Passkey 状态失败"))
		return
	}
	if !passkeyEnabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该用户尚未绑定 Passkey"})
		return
	}
	if err := service.ResetPasskeysAndRevokeSessions(id); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("重置失败"))
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"user.reset_passkey id="+common.Int2Str(id))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Passkey 已重置"})
}

// Admin2FAStats reports 2FA adoption statistics (GET /api/user/2fa/stats).
func Admin2FAStats(c *gin.Context) {
	var totalUsers, enabledUsers int64
	if err := model.DB.Model(&model.User{}).Count(&totalUsers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("统计失败"))
		return
	}
	if err := model.DB.Model(&model.TwoFA{}).Where("is_enabled = true").Count(&enabledUsers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("统计失败"))
		return
	}
	rate := 0.0
	if totalUsers > 0 {
		rate = float64(enabledUsers) / float64(totalUsers) * 100
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"total_users":   totalUsers,
			"enabled_users": enabledUsers,
			"enabled_rate":  strconv.FormatFloat(rate, 'f', 1, 64) + "%",
		},
	})
}

// AdminDisable2FA force-disables a target user's 2FA and revokes all of their
// sessions (DELETE /api/user/:id/2fa).
func AdminDisable2FA(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户ID格式错误"})
		return
	}
	target, err := service.GetUserByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, dto.Fail("用户不存在"))
		return
	}
	if !canManageTargetRole(common.GetRole(c), target.Role) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "无权操作同级或更高级用户的2FA设置"})
		return
	}
	twoFAEnabled, err := service.TwoFAStatusChecked(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询 2FA 状态失败"))
		return
	}
	if !twoFAEnabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "用户未启用2FA"})
		return
	}
	if !middleware.RequireSecurityProof(c, service.SecurityProofScopeTwoFAReset,
		[]string{service.SecurityProofMethod2FA, service.SecurityProofMethodPasskey}) {
		return
	}
	if err := service.DisableTwoFAAndRevokeSessions(id); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("禁用失败"))
		return
	}
	service.RecordSystemLog(common.GetUserId(c), service.LogTypeManage,
		"user.2fa_disable id="+common.Int2Str(id))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "用户2FA已被强制禁用"})
}
