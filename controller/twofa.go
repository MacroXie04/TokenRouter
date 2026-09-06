package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/service"
)

var twoFAResetProofMethods = []string{
	service.SecurityProofMethod2FA,
	service.SecurityProofMethodPasskey,
}

// GetTwoFAStatus returns whether 2FA is enabled for the user.
func GetTwoFAStatus(c *gin.Context) {
	userId := common.GetUserId(c)
	enabled, err := service.TwoFAStatusChecked(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询 2FA 状态失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"enabled": enabled,
	}))
}

// StartTwoFA generates a TOTP secret and returns the provisioning URL.
func StartTwoFA(c *gin.Context) {
	userId := common.GetUserId(c)
	// TOTP setup is a browser-session ceremony. In particular, a dashboard
	// bearer token has no session identity to which a reset proof can bind.
	if _, ok := middleware.GetSessionAuthIdentity(c); !ok {
		middleware.RequireSecurityProof(c, service.SecurityProofScopeTwoFAReset, twoFAResetProofMethods)
		return
	}
	user, err := service.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusUnauthorized, dto.Fail("用户不存在"))
		return
	}
	secret, url, err := service.GenerateTwoFASecret(userId, user.Username)
	if errors.Is(err, service.ErrTwoFAResetProofRequired) {
		if !middleware.RequireSecurityProof(c, service.SecurityProofScopeTwoFAReset, twoFAResetProofMethods) {
			return
		}
		secret, url, err = service.ResetTwoFASecret(userId, user.Username)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("生成密钥失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"secret": secret, "otpauth_url": url}))
}

// EnableTwoFA verifies a TOTP code and enables 2FA, returning backup codes.
func EnableTwoFA(c *gin.Context) {
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	sid, ok := currentSid(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, dto.Fail("当前认证方式不支持启用两步验证"))
		return
	}
	codes, access, err := service.EnableTwoFAAndRotateSessionWithAccess(common.GetUserId(c), req.Code, sid)
	if err != nil {
		writeTwoFAMutationError(c, err)
		return
	}
	setAccessCookie(c, access)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "两步验证启用成功",
		"data": gin.H{
			"backup_codes": codes,
			"access_token": access,
			"token_type":   "Bearer",
		},
	})
}

// DisableTwoFA verifies a code and disables 2FA.
func DisableTwoFA(c *gin.Context) {
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	sid, ok := currentSid(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, dto.Fail("当前认证方式不支持禁用两步验证"))
		return
	}
	access, err := service.DisableTwoFAWithCodeAndRotateSessionWithAccess(common.GetUserId(c), req.Code, sid)
	if err != nil {
		writeTwoFAMutationError(c, err)
		return
	}
	setAccessCookie(c, access)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "已禁用两步验证",
		"data": gin.H{
			"access_token": access,
			"token_type":   "Bearer",
		},
	})
}

func writeTwoFAMutationError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrTwoFANotEnabled) ||
		errors.Is(err, service.ErrTwoFAInvalidCode) ||
		errors.Is(err, service.ErrTwoFALocked) {
		c.JSON(http.StatusBadRequest, dto.Fail("验证码错误"))
		return
	}
	if errors.Is(err, service.ErrSessionRevoked) {
		clearAuthCookies(c)
	}
	writeAuthSessionError(c, err)
}

// VerifyTwoFAForAction verifies a code for a sensitive action (security proof).
func VerifyTwoFAForAction(c *gin.Context) {
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	userId := common.GetUserId(c)
	if err := service.VerifyTwoFA(userId, req.Code); err != nil {
		if err == service.ErrTwoFAInvalidCode && service.VerifyBackupCode(userId, req.Code) == nil {
			// accepted via backup code
		} else {
			c.JSON(http.StatusBadRequest, dto.Fail("验证码错误"))
			return
		}
	}
	c.JSON(http.StatusOK, dto.OkMessage("验证通过"))
}
