package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/middleware"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// OAuth flow intents (mirroring the reference's AuthFlowIntent values).
const (
	oauthIntentLogin = "login"
	oauthIntentBind  = "bind"
)

// oauthAuthFlowTTL is the lifetime of an OAuth state flow.
const oauthAuthFlowTTL = 10 * time.Minute

// GenerateOAuthCode creates a one-time OAuth state (flow token) for a
// provider + intent. Login flows may carry an affiliate code; bind flows are
// bound to the signed-in user's session and must not carry one.
func GenerateOAuthCode(c *gin.Context) {
	var request struct {
		Provider string `json:"provider"`
		Intent   string `json:"intent"`
		Aff      string `json:"aff"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.Intent = strings.TrimSpace(request.Intent)
	request.Aff = strings.TrimSpace(request.Aff)
	cfg := service.GetOAuthConfig(request.Provider)
	if !cfg.Known || (request.Intent != oauthIntentLogin && request.Intent != oauthIntentBind) ||
		len(request.Aff) > 32 || (request.Intent == oauthIntentBind && request.Aff != "") {
		c.JSON(http.StatusBadRequest, dto.Fail("参数错误"))
		return
	}
	userId := 0
	sessionId := ""
	if request.Intent == oauthIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "绑定操作需要登录"})
			return
		}
		userId = identity.UserID
		sessionId = identity.SessionID
	}
	payload, err := common.Marshal(map[string]string{"affiliate_code": request.Aff})
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	state, err := service.CreateAuthFlow(service.AuthFlowPurposeOAuth, request.Provider, request.Intent, userId, sessionId, string(payload), oauthAuthFlowTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"flow_token": state,
			"expires_at": time.Now().Add(oauthAuthFlowTTL).Unix(),
		},
	})
}

// oauthRedirectURI builds the callback URL for a provider.
func oauthRedirectURI(c *gin.Context, provider string) string {
	base := common.GetEnv("FRONTEND_BASE_URL", "http://localhost:3000")
	return base + "/api/oauth/" + provider + "/callback"
}

// HandleOAuth starts an OAuth login: it stores a one-time state and redirects
// the browser to the provider's authorization endpoint.
func HandleOAuth(c *gin.Context) {
	provider := c.Param("provider")
	cfg := service.GetOAuthConfig(provider)
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, dto.Fail("该登录方式未启用"))
		return
	}
	state, err := service.CreateAuthFlow(service.AuthFlowPurposeOAuth, provider, oauthIntentLogin, 0, "", "", 10*time.Minute)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("登录失败"))
		return
	}
	redirectURI := oauthRedirectURI(c, provider)
	authURL, err := service.BuildAuthorizationURL(cfg, state, redirectURI)
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail(err.Error()))
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// TelegramLogin completes a Telegram Login Widget authentication: it verifies
// the hash and signs the user in (or binds the identity).
func TelegramLogin(c *gin.Context) {
	data := map[string]string{}
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			data[k] = v[0]
		}
	}
	if !service.VerifyTelegramLogin(data) {
		c.Redirect(http.StatusFound, "/login?error=telegram")
		return
	}
	pu := &service.ProviderUser{
		ProviderID:  data["id"],
		Username:    data["username"],
		DisplayName: firstNonEmpty(data["first_name"], data["username"]),
	}
	if pu.ProviderID == "" {
		c.Redirect(http.StatusFound, "/login?error=telegram")
		return
	}
	user, _, err := service.LoginOrBindUser("telegram", pu)
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=telegram")
		return
	}
	sid, access, refresh, err := service.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), "telegram")
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=telegram")
		return
	}
	setAuthCookies(c, sid, access, refresh)
	c.Redirect(http.StatusFound, "/")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// TelegramBindStart begins a Telegram bind ceremony for the signed-in user and
// returns the one-time flow token.
func TelegramBindStart(c *gin.Context) {
	token, err := service.CreateAuthFlow(service.AuthFlowPurposeTelegramBind, "telegram", "",
		common.GetUserId(c), "", "", 10*time.Minute)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("发起失败"))
		return
	}
	c.JSON(http.StatusOK, dto.Ok(gin.H{"flow_token": token}))
}

// TelegramBindFinish completes the bind ceremony with Telegram Login Widget
// data and binds the identity to the flow's user.
func TelegramBindFinish(c *gin.Context) {
	fail := func() {
		c.Redirect(http.StatusFound, "/?telegram_bound=error")
	}
	flow, err := service.ConsumeAuthFlow(c.Param("flow_token"), service.AuthFlowPurposeTelegramBind)
	if err != nil || flow.UserId == 0 {
		fail()
		return
	}
	data := map[string]string{}
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			data[k] = v[0]
		}
	}
	if !service.VerifyTelegramLogin(data) || data["id"] == "" {
		fail()
		return
	}
	if service.FindUserByTelegramId(data["id"]) != nil {
		c.Redirect(http.StatusFound, "/?telegram_bound=taken")
		return
	}
	if err := model.DB.Model(&model.User{}).Where("id = ?", flow.UserId).
		Update("telegram_id", data["id"]).Error; err != nil {
		fail()
		return
	}
	c.Redirect(http.StatusFound, "/?telegram_bound=1")
}

// OAuthCallback completes the OAuth2 code flow: it consumes the one-time
// state, exchanges the code for a token, fetches the provider identity, and
// signs the bound user in.
func OAuthCallback(c *gin.Context) {
	provider := c.Param("provider")
	cfg := service.GetOAuthConfig(provider)
	if !cfg.Enabled {
		c.Redirect(http.StatusFound, "/login?error=oauth_disabled")
		return
	}
	state := c.Query("state")
	code := c.Query("code")
	if state == "" || code == "" {
		c.Redirect(http.StatusFound, "/login?error=invalid_oauth")
		return
	}
	flow, err := service.ConsumeAuthFlow(state, service.AuthFlowPurposeOAuth)
	if err != nil || flow.Provider != provider {
		c.Redirect(http.StatusFound, "/login?error=oauth_state")
		return
	}
	token, err := service.ExchangeCode(cfg, code, oauthRedirectURI(c, provider))
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=oauth_token")
		return
	}
	pu, err := service.FetchUserInfo(cfg, provider, token)
	if err != nil {
		// A policy denial carries a rendered, user-facing message; surface it
		// to the login page instead of the generic userinfo failure.
		var denied *service.OAuthAccessDeniedError
		if errors.As(err, &denied) {
			c.Redirect(http.StatusFound, "/login?error=oauth_access_denied&message="+url.QueryEscape(denied.Message))
			return
		}
		c.Redirect(http.StatusFound, "/login?error=oauth_userinfo")
		return
	}
	// Bind intent: attach the provider identity to the user who created the
	// state flow (the flow itself carries the user, so no session is needed).
	if flow.Intent == oauthIntentBind {
		if flow.UserId == 0 {
			c.Redirect(http.StatusFound, "/login?error=oauth_state")
			return
		}
		if err := service.BindProviderToUser(provider, pu, flow.UserId); err != nil {
			c.Redirect(http.StatusFound, "/?oauth_bound=taken")
			return
		}
		c.Redirect(http.StatusFound, "/?oauth_bound=1")
		return
	}

	var affCode string
	if flow.Payload != "" {
		var payload struct {
			AffiliateCode string `json:"affiliate_code"`
		}
		if err := common.UnmarshalJsonStr(flow.Payload, &payload); err == nil {
			affCode = payload.AffiliateCode
		}
	}
	user, _, err := service.LoginOrBindUserWithAff(provider, pu, affCode)
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=oauth_user")
		return
	}
	sid, access, refresh, err := service.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), "oauth:"+provider)
	if err != nil {
		c.Redirect(http.StatusFound, "/login?error=oauth_session")
		return
	}
	setAuthCookies(c, sid, access, refresh)
	c.Redirect(http.StatusFound, "/")
}
