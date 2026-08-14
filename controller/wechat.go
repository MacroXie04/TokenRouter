package controller

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// WeChatAuth handles GET /api/oauth/wechat: it exchanges the authorization
// code for an openid, then logs the bound user in or registers a new account.
func WeChatAuth(c *gin.Context) {
	if !service.WeChatAuthEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "管理员未开启通过微信登录以及注册", "success": false})
		return
	}
	wechatId, err := service.GetWeChatIdByCode(c.Query("code"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": err.Error(), "success": false})
		return
	}
	user, _, err := wechatLoginOrRegister(wechatId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": err.Error(), "success": false})
		return
	}
	if user.Status != model.UserStatusEnabled {
		c.JSON(http.StatusOK, gin.H{"message": "用户已被封禁", "success": false})
		return
	}
	sid, access, refresh, err := service.CompleteLogin(user, c.ClientIP(), c.GetHeader("User-Agent"), "wechat")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "登录失败", "success": false})
		return
	}
	setAuthCookies(c, sid, access, refresh)
	c.JSON(http.StatusOK, gin.H{
		"message": "",
		"success": true,
		"data": gin.H{
			"access_token":      access,
			"token_type":        "Bearer",
			"access_expires_at": time.Now().Add(service.AccessTokenTTL).Unix(),
			"session":           sid,
			"user":              userResponse(user),
		},
	})
}

// wechatLoginOrRegister returns the user bound to the openid, or registers a
// new account when registration is enabled.
func wechatLoginOrRegister(wechatId string) (*model.User, bool, error) {
	if user := service.FindUserByWeChatId(wechatId); user != nil {
		return user, false, nil
	}
	if !setting.GetOptionBool(setting.RegistrationEnabledOption, true) {
		return nil, false, errors.New("管理员关闭了新用户注册")
	}
	user := &model.User{
		Username:    "wechat_" + strconv.Itoa(service.NextUserId()),
		Password:    "", // WeChat users authenticate via the WeChat server, not a password
		DisplayName: "WeChat User",
		Role:        constant.RoleCommonUser,
		Status:      model.UserStatusEnabled,
		WeChatId:    wechatId,
		Group:       setting.GetOptionOrDefault(setting.DefaultGroupOption, service.GroupDefault),
		Quota:       setting.GetOptionIntOrDefault(setting.InitialQuotaOption, 500000),
		CreatedAt:   common.NowTimestamp(),
		AuthVersion: 1,
	}
	if err := model.DB.Create(user).Error; err != nil {
		return nil, false, err
	}
	return user, true, nil
}

type wechatBindRequest struct {
	Code string `json:"code"`
}

// WeChatBind handles POST /api/oauth/wechat/bind: it binds a WeChat openid to
// the signed-in user's account, updating only the wechat_id column.
func WeChatBind(c *gin.Context) {
	if !service.WeChatAuthEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "管理员未开启通过微信登录以及注册", "success": false})
		return
	}
	var req wechatBindRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "无效的请求", "success": false})
		return
	}
	wechatId, err := service.GetWeChatIdByCode(req.Code)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": err.Error(), "success": false})
		return
	}
	if service.FindUserByWeChatId(wechatId) != nil {
		c.JSON(http.StatusOK, gin.H{"message": "该微信账号已被绑定", "success": false})
		return
	}
	userId := common.GetUserId(c)
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	// Update only the bind column so concurrent ban/role/group changes survive.
	if err := model.DB.Model(&model.User{}).Where("id = ?", userId).
		Update("wechat_id", wechatId).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
