package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

// SendEmailVerification sends a one-time code to verify an email address.
func SendEmailVerification(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.SendEmailVerificationCode(req.Email); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("发送失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("验证码已发送"))
}

// BindEmail verifies a code and binds the email to the authenticated user.
func BindEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.VerifyAndBindEmail(common.GetUserId(c), req.Email, req.Code); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("邮箱验证成功"))
}

// SendPasswordResetEmail sends a one-time reset code to the user's email.
func SendPasswordResetEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	// Do not reveal whether the email exists; return success regardless.
	_ = service.SendPasswordResetEmail(req.Email)
	c.JSON(http.StatusOK, dto.OkMessage("如果该邮箱已注册，验证码已发送"))
}

// ResetPassword resets a password using the emailed code.
func ResetPassword(c *gin.Context) {
	var req struct {
		Email       string `json:"email" binding:"required"`
		Code        string `json:"code" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8,max=64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.ResetPassword(req.Email, req.Code, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("密码重置成功"))
}
