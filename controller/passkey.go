package controller

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
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

// PasskeyRegisterBegin starts a passkey registration.
func PasskeyRegisterBegin(c *gin.Context) {
	options, flowToken, err := service.BeginPasskeyRegistration(common.GetUserId(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail(err.Error()))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"options": options, "flow_token": flowToken}))
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
