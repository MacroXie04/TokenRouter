package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/service"
)

// universalVerifyRequest is the step-up verification request body.
type universalVerifyRequest struct {
	Method string `json:"method"`
	Code   string `json:"code,omitempty"`
	Scope  string `json:"scope"`
}

// UniversalVerify issues a security proof after a 2FA step-up verification
// (POST /api/verify). Passkey step-ups go through the passkey verify flow.
func UniversalVerify(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "当前认证方式不支持安全验证"})
		return
	}
	var request universalVerifyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误: "+err.Error()))
		return
	}
	if request.Method != service.SecurityProofMethod2FA {
		c.JSON(http.StatusBadRequest, dto.Fail("Passkey 验证必须使用 Passkey verify 流程"))
		return
	}
	if !IsAllowedSecurityProofScope(request.Scope) {
		c.JSON(http.StatusBadRequest, dto.Fail("不支持的安全验证范围"))
		return
	}
	if strings.TrimSpace(request.Code) == "" {
		c.JSON(http.StatusBadRequest, dto.Fail("验证码不能为空"))
		return
	}
	if !service.TwoFAStatus(identity.UserID) {
		c.JSON(http.StatusBadRequest, dto.Fail("用户未启用2FA"))
		return
	}
	if err := service.VerifyTwoFA(identity.UserID, request.Code); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("验证失败，请检查验证码"))
		return
	}
	proofToken, expiresAt, err := service.IssueSecurityProof(identity, request.Method, []string{request.Scope})
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	service.RecordSystemLog(identity.UserID, service.LogTypeManage, "通用安全验证成功 (验证方式: 2FA)")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "验证成功",
		"data": gin.H{
			"proof_token": proofToken,
			"expires_at":  expiresAt,
			"method":      request.Method,
			"scope":       request.Scope,
		},
	})
}

// IsAllowedSecurityProofScope reports whether a scope is a known step-up scope.
func IsAllowedSecurityProofScope(scope string) bool {
	switch scope {
	case service.SecurityProofScopeChannelKeyRead,
		service.SecurityProofScopePasskeyRegister,
		service.SecurityProofScopePasskeyDelete:
		return true
	default:
		return false
	}
}
