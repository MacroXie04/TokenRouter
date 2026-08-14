package controller

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/service"
)

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
	state, err := service.CreateAuthFlow(service.AuthFlowPurposeOAuth, provider, "", 0, "", "", 10*time.Minute)
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

// OAuthCallback completes an OAuth login: it validates state, exchanges the
// code, fetches the identity, and signs the user in.
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
	if _, err := service.ConsumeAuthFlow(state, service.AuthFlowPurposeOAuth); err != nil {
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
		c.Redirect(http.StatusFound, "/login?error=oauth_userinfo")
		return
	}
	user, _, err := service.LoginOrBindUser(provider, pu)
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
