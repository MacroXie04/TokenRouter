package accounts

import (
	"bytes"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
)

const maxPasskeyRequestBodyBytes int64 = 1 << 20

// readPasskeyBody reads the raw request body once and extracts the flow token.
func readPasskeyBody(c *gin.Context) ([]byte, string, error) {
	body, err := httpx.ReadAllLimited(c.Request.Body, maxPasskeyRequestBodyBytes)
	if err != nil {
		return nil, "", err
	}
	var req struct {
		FlowToken string `json:"flow_token"`
	}
	if err := jsonutil.Unmarshal(body, &req); err != nil || req.FlowToken == "" {
		return nil, "", errors.New("invalid passkey request body")
	}
	return body, req.FlowToken, nil
}

func rejectPasskeyBody(c *gin.Context, err error) {
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, dto.Fail("请求体过大"))
		return
	}
	c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
}

// PasskeyStatus reports whether the user has a registered passkey.
func PasskeyStatus(c *gin.Context) {
	enabled, err := auth.PasskeyEnabledChecked(requestctx.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询 Passkey 状态失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"enabled": enabled}))
}

// PasskeyRegisterBegin starts a passkey registration. When the user has 2FA
// enabled, a 2FA security proof for the passkey.register scope is required
// (matching the reference's step-up gate).
func PasskeyRegisterBegin(c *gin.Context) {
	// Registration mutates browser-bound authentication state. Resolve the live
	// login session before checking feature configuration so a management PAT
	// cannot use the endpoint or distinguish whether Passkey is enabled.
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	twoFAEnabled, err := auth.TwoFAStatusChecked(identity.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询 2FA 状态失败"))
		return
	}
	if twoFAEnabled && !middleware.RequireSecurityProof(c,
		auth.SecurityProofScopePasskeyRegister, []string{auth.SecurityProofMethod2FA}) {
		return
	}
	if !setting.GetAuthenticationSetting().Passkey.Enabled {
		c.JSON(http.StatusOK, dto.Fail("管理员未启用 Passkey 登录"))
		return
	}
	options, flowToken, err := auth.BeginPasskeyRegistration(identity.UserID, identity.SessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"options": options, "flow_token": flowToken}))
}

// PasskeyVerifyBegin starts a passkey step-up assertion; the resulting proof
// authorizes the requested scope for sensitive operations.
func PasskeyVerifyBegin(c *gin.Context) {
	userId := requestctx.GetUserId(c)
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
	passkeyEnabled, err := auth.PasskeyEnabledChecked(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("查询 Passkey 状态失败"))
		return
	}
	if !passkeyEnabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "该用户尚未绑定 Passkey"})
		return
	}
	options, flowToken, expiresAt, err := auth.BeginPasskeyVerifyWithExpiry(userId, identity.SessionID, req.Scope)
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
			"expires_at": expiresAt,
		},
	})
}

// PasskeyVerifyFinish validates the step-up assertion and issues a passkey
// security proof for the scope requested at begin time.
func PasskeyVerifyFinish(c *gin.Context) {
	userId := requestctx.GetUserId(c)
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "当前认证方式不支持安全验证"})
		return
	}
	body, flowToken, err := readPasskeyBody(c)
	if err != nil {
		rejectPasskeyBody(c, err)
		return
	}
	response, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的凭据响应"))
		return
	}
	scope, err := auth.FinishPasskeyVerify(userId, identity.SessionID, flowToken, response)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("验证失败"))
		return
	}
	proofToken, expiresAt, err := auth.IssueSecurityProof(identity, auth.SecurityProofMethodPasskey, []string{scope})
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
			"method":      auth.SecurityProofMethodPasskey,
			"scope":       scope,
		},
	})
}

// PasskeyRegisterFinish completes a passkey registration.
func PasskeyRegisterFinish(c *gin.Context) {
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	if !setting.GetAuthenticationSetting().Passkey.Enabled {
		c.JSON(http.StatusOK, dto.Fail("管理员未启用 Passkey 登录"))
		return
	}
	body, flowToken, err := readPasskeyBody(c)
	if err != nil {
		rejectPasskeyBody(c, err)
		return
	}
	response, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的凭据响应"))
		return
	}
	if err := auth.FinishPasskeyRegistration(identity.UserID, identity.SessionID, flowToken, response); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("注册失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("Passkey 注册成功"))
}

// PasskeyLoginBegin starts a discoverable passkey login.
func PasskeyLoginBegin(c *gin.Context) {
	if !setting.GetAuthenticationSetting().Passkey.Enabled {
		c.JSON(http.StatusOK, dto.Fail("管理员未启用 Passkey 登录"))
		return
	}
	options, flowToken, expiresAt, err := auth.BeginPasskeyLoginWithExpiry()
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options": options, "flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

// PasskeyLoginFinish completes a passkey login.
func PasskeyLoginFinish(c *gin.Context) {
	if !setting.GetAuthenticationSetting().Passkey.Enabled {
		c.JSON(http.StatusOK, dto.Fail("管理员未启用 Passkey 登录"))
		return
	}
	body, flowToken, err := readPasskeyBody(c)
	if err != nil {
		rejectPasskeyBody(c, err)
		return
	}
	response, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("无效的凭据响应"))
		return
	}
	user, err := auth.FinishPasskeyLogin(flowToken, response)
	if err != nil {
		c.JSON(http.StatusUnauthorized, dto.Fail("登录失败"))
		return
	}
	if user.Status != model.UserStatusEnabled {
		c.JSON(http.StatusUnauthorized, dto.Fail("该用户已被禁用"))
		return
	}
	sid, access, refresh, err := auth.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), "passkey")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	setAuthCookies(c, sid, access, refresh)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": userResponse(user)})
}
