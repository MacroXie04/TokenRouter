package controller

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/service"
)

// readPasskeyBody reads the raw request body once and extracts the flow token.
func readPasskeyBody(c *gin.Context) ([]byte, string, bool) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, "", false
	}
	var req struct {
		FlowToken string `json:"flow_token"`
	}
	if err := common.Unmarshal(body, &req); err != nil || req.FlowToken == "" {
		return nil, "", false
	}
	return body, req.FlowToken, true
}

// PasskeyStatus reports whether the user has a registered passkey.
func PasskeyStatus(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(gin.H{"enabled": service.PasskeyEnabled(common.GetUserId(c))}))
}

// PasskeyRegisterBegin starts a passkey registration. When the user has 2FA
// enabled, a 2FA security proof for the passkey.register scope is required
// (matching the reference's step-up gate).
func PasskeyRegisterBegin(c *gin.Context) {
	userId := common.GetUserId(c)
	if service.TwoFAStatus(userId) && !middleware.RequireSecurityProof(c,
		service.SecurityProofScopePasskeyRegister, []string{service.SecurityProofMethod2FA}) {
		return
	}
	options, flowToken, err := service.BeginPasskeyRegistration(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"options": options, "flow_token": flowToken}))
}

// PasskeyVerifyBegin starts a passkey step-up assertion; the resulting proof
// authorizes the requested scope for sensitive operations.
func PasskeyVerifyBegin(c *gin.Context) {
	userId := common.GetUserId(c)
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "当前认证方式不支持安全验证"})
		return
	}
	var req struct {
		Scope string `json:"scope"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的 Passkey 验证请求"))
		return
	}
	if !IsAllowedSecurityProofScope(req.Scope) {
		c.JSON(http.StatusBadRequest, dto.Fail("不支持的安全验证范围"))
		return
	}
	if !service.PasskeyEnabled(userId) {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该用户尚未绑定 Passkey"})
		return
	}
	options, flowToken, err := service.BeginPasskeyVerify(userId, identity.SessionID, req.Scope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    options,
			"flow_token": flowToken,
			"expires_at": common.NowTimestamp() + 5*60,
		},
	})
}

// PasskeyVerifyFinish validates the step-up assertion and issues a passkey
// security proof for the scope requested at begin time.
func PasskeyVerifyFinish(c *gin.Context) {
	userId := common.GetUserId(c)
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "当前认证方式不支持安全验证"})
		return
	}
	body, flowToken, ok := readPasskeyBody(c)
	if !ok {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	response, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的凭据响应"))
		return
	}
	scope, err := service.FinishPasskeyVerify(userId, identity.SessionID, flowToken, response)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("验证失败"))
		return
	}
	proofToken, expiresAt, err := service.IssueSecurityProof(identity, service.SecurityProofMethodPasskey, []string{scope})
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 验证成功",
		"data": gin.H{
			"proof_token": proofToken,
			"expires_at":  expiresAt,
			"method":      service.SecurityProofMethodPasskey,
			"scope":       scope,
		},
	})
}

// PasskeyRegisterFinish completes a passkey registration.
func PasskeyRegisterFinish(c *gin.Context) {
	body, flowToken, ok := readPasskeyBody(c)
	if !ok {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	response, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的凭据响应"))
		return
	}
	if err := service.FinishPasskeyRegistration(common.GetUserId(c), flowToken, response); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("注册失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("Passkey 注册成功"))
}

// PasskeyLoginBegin starts a discoverable passkey login.
func PasskeyLoginBegin(c *gin.Context) {
	options, flowToken, err := service.BeginPasskeyLogin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"options": options, "flow_token": flowToken}))
}

// PasskeyLoginFinish completes a passkey login.
func PasskeyLoginFinish(c *gin.Context) {
	body, flowToken, ok := readPasskeyBody(c)
	if !ok {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	response, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的凭据响应"))
		return
	}
	user, err := service.FinishPasskeyLogin(flowToken, response)
	if err != nil {
		c.JSON(http.StatusUnauthorized, dto.Fail("登录失败"))
		return
	}
	sid, access, refresh, err := service.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), "passkey")
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	setAuthCookies(c, sid, access, refresh)
	c.JSON(http.StatusOK, dto.Ok(userResponse(user)))
}
