package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	maxPasswordResetEmailBytes = 254
	maxPasswordResetTokenBytes = 256
	generatedPasswordLength    = 16
)

// SendEmailVerification sends a one-time code to verify an email address.
func SendEmailVerification(c *gin.Context) {
	email, ok := emailFromRequest(c)
	if !ok {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	normalized, _, err := model.NormalizeVerifiedEmail(email)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("邮箱格式无效"))
		return
	}
	if err := setting.ValidateEmailRegistrationPolicy(normalized); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	if err := service.SendEmailVerificationCode(normalized); err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("发送失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("验证码已发送"))
}

// BindEmail verifies a code and binds the email to the authenticated user.
func BindEmail(c *gin.Context) {
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email" binding:"required"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	if err := service.VerifyAndBindEmail(identity.UserID, req.Email, req.Code); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("邮箱验证成功"))
}

// SendPasswordResetEmail sends a one-time reset code to the user's email.
func SendPasswordResetEmail(c *gin.Context) {
	email, ok := emailFromRequest(c)
	if !ok {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	// Do not reveal whether the email exists; return success regardless.
	_ = service.SendPasswordResetEmail(email)
	c.JSON(http.StatusOK, dto.OkMessage("如果该邮箱已注册，验证码已发送"))
}

// emailFromRequest accepts the reference query-string contract while keeping
// TokenRouter's JSON POST compatibility surface. It intentionally returns only
// whether a non-empty value was supplied; canonical validation remains in the
// service layer for both methods.
func emailFromRequest(c *gin.Context) (string, bool) {
	if c.Request.Method == http.MethodGet {
		email := c.Query("email")
		return email, email != ""
	}
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		return "", false
	}
	return req.Email, true
}

// ResetPassword resets a password using the emailed code.
func ResetPassword(c *gin.Context) {
	var req dto.ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || len(req.Email) > maxPasswordResetEmailBytes ||
		len(req.Token) > maxPasswordResetTokenBytes || len(req.Code) > maxPasswordResetTokenBytes ||
		(req.Token != "" && req.Code != "" && req.Token != req.Code) {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	credential := req.Token
	if credential == "" {
		credential = req.Code
	}
	if credential == "" {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}

	password := req.NewPassword
	generated := password == ""
	if generated {
		var err error
		password, err = common.SecureRandomAlphanumeric(generatedPasswordLength)
		if err != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("密码重置失败"))
			return
		}
	} else if len(password) < 8 || len(password) > 64 {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}

	if err := service.ResetPassword(req.Email, credential, password); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	if generated {
		c.JSON(http.StatusOK, dto.Ok(password))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("密码重置成功"))
}
