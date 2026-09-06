package accounts

import (
	"errors"
	"github.com/gin-gonic/gin"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
)

// WeChatAuth handles GET /api/oauth/wechat: it exchanges the authorization
// code for an openid, then logs the bound user in or registers a new account.
func WeChatAuth(c *gin.Context) {
	if !authsvc.WeChatAuthEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "管理员未开启通过微信登录以及注册", "success": false})
		return
	}
	query, ok := boundedIdentityQuery(c)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"message": "微信登录失败", "success": false})
		return
	}
	wechatId, err := authsvc.GetWeChatIdByCode(query.Get("code"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "微信登录失败", "success": false})
		return
	}
	user, _, err := wechatLoginOrRegister(wechatId)
	if err != nil {
		if errors.Is(err, errWeChatRegistrationDisabled) {
			c.JSON(http.StatusOK, gin.H{"message": "管理员关闭了新用户注册", "success": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "微信登录失败", "success": false})
		return
	}
	if user.Status != model.UserStatusEnabled {
		c.JSON(http.StatusOK, gin.H{"message": "用户已被封禁", "success": false})
		return
	}
	sid, access, refresh, accessExpiresAt, err := authsvc.CompleteLoginWithExpiry(
		user, c.ClientIP(), c.GetHeader("User-Agent"), "wechat",
	)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	setAuthCookies(c, sid, access, refresh)
	c.JSON(http.StatusOK, gin.H{
		"message": "",
		"success": true,
		"data": gin.H{
			"access_token":      access,
			"token_type":        "Bearer",
			"access_expires_at": accessExpiresAt,
			"session":           sid,
			"user":              userResponse(user),
		},
	})
}

var errWeChatRegistrationDisabled = errors.New("WeChat registration is disabled")

// wechatLoginOrRegister returns the user bound to the openid, or registers a
// new account when registration is enabled.
func wechatLoginOrRegister(wechatId string) (*model.User, bool, error) {
	if user, err := authsvc.FindUserByProviderIdentity("wechat", wechatId); err == nil {
		return user, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if !setting.GetOptionBool(setting.RegistrationEnabledOption, true) {
		return nil, false, errWeChatRegistrationDisabled
	}
	usernameSuffix, err := cryptoutil.SecureRandomAlphanumeric(8)
	if err != nil {
		return nil, false, err
	}
	user, created, err := authsvc.LoginOrBindUser("wechat", &authsvc.ProviderUser{
		ProviderID: wechatId, Username: "wechat_" + usernameSuffix, DisplayName: "WeChat User",
	})
	if errors.Is(err, authsvc.ErrRegistrationDisabled) {
		return nil, false, errWeChatRegistrationDisabled
	}
	return user, created, err
}

type wechatBindRequest struct {
	Code string `json:"code"`
}

// WeChatBind handles POST /api/oauth/wechat/bind: it atomically binds a WeChat
// openid in the canonical ownership table and its compatibility user column.
func WeChatBind(c *gin.Context) {
	identity, ok := requireLoginSession(c)
	if !ok {
		return
	}
	if !authsvc.WeChatAuthEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "管理员未开启通过微信登录以及注册", "success": false})
		return
	}
	var req wechatBindRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "无效的请求", "success": false})
		return
	}
	wechatId, err := authsvc.GetWeChatIdByCode(req.Code)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "微信绑定失败", "success": false})
		return
	}
	userId := identity.UserID
	if err := authsvc.BindProviderToSession("wechat", &authsvc.ProviderUser{ProviderID: wechatId}, userId, identity.SessionID); err != nil {
		if errors.Is(err, authsvc.ErrBindingTaken) {
			c.JSON(http.StatusOK, gin.H{"message": "该微信账号已被绑定", "success": false})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "微信绑定失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
