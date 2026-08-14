package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

// GetTwoFAStatus returns whether 2FA is enabled for the user.
func GetTwoFAStatus(c *gin.Context) {
	userId := common.GetUserId(c)
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"enabled": service.TwoFAStatus(userId),
	}))
}

// StartTwoFA generates a TOTP secret and returns the provisioning URL.
func StartTwoFA(c *gin.Context) {
	userId := common.GetUserId(c)
	user, err := service.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusUnauthorized, dto.Fail("用户不存在"))
		return
	}
	secret, url, err := service.GenerateTwoFASecret(userId, user.Username)
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
	codes, err := service.EnableTwoFA(common.GetUserId(c), req.Code)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("验证码错误"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"backup_codes": codes}))
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
	userId := common.GetUserId(c)
	if err := service.VerifyTwoFA(userId, req.Code); err != nil {
		if err == service.ErrTwoFAInvalidCode && service.VerifyBackupCode(userId, req.Code) == nil {
			// accepted via backup code
		} else {
			c.JSON(http.StatusBadRequest, dto.Fail("验证码错误"))
			return
		}
	}
	if err := service.DisableTwoFA(userId); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("禁用失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("已禁用两步验证"))
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
