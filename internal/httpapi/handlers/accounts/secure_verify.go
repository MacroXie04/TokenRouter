package accounts

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"net/http"
	"strings"
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
	if request.Method != auth.SecurityProofMethod2FA {
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
	twoFAEnabled, err := auth.TwoFAStatusChecked(identity.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("验证失败"))
		return
	}
	if !twoFAEnabled {
		c.JSON(http.StatusBadRequest, dto.Fail("用户未启用2FA"))
		return
	}
	if err := auth.VerifyTwoFA(identity.UserID, request.Code); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("验证失败，请检查验证码"))
		return
	}
	proofToken, expiresAt, err := auth.IssueSecurityProof(identity, request.Method, []string{request.Scope})
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	billingsvc.RecordSystemLog(identity.UserID, billingsvc.LogTypeManage, "通用安全验证成功 (验证方式: 2FA)")
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
	case auth.SecurityProofScopeChannelKeyRead,
		auth.SecurityProofScopePasskeyRegister,
		auth.SecurityProofScopePasskeyDelete,
		auth.SecurityProofScopeTwoFAReset,
		auth.SecurityProofScopeBackupCodeReset:
		return true
	default:
		return false
	}
}
