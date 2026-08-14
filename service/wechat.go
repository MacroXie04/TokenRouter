package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// WeChatAuthEnabled reports whether WeChat login/registration is enabled.
func WeChatAuthEnabled() bool {
	return setting.GetOptionBool(setting.WeChatAuthEnabledOption, false)
}

// WeChatServerAddress returns the configured WeChat login server base address
// (admin option with environment fallback).
func WeChatServerAddress() string {
	return common.GetEnv("WECHAT_SERVER_ADDRESS", setting.GetOptionOrDefault(setting.WeChatServerAddressOption, ""))
}

// WeChatServerToken returns the authorization token for the WeChat login server.
func WeChatServerToken() string {
	return common.GetEnv("WECHAT_SERVER_TOKEN", setting.GetOptionOrDefault(setting.WeChatServerTokenOption, ""))
}

// WeChatQRCodeURL returns the public WeChat QR code image URL shown on the
// login/bind dialogs.
func WeChatQRCodeURL() string {
	return common.GetEnv("WECHAT_QRCODE_URL", setting.GetOptionOrDefault(setting.WeChatQRCodeOption, ""))
}

type weChatLoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// GetWeChatIdByCode exchanges a WeChat authorization code for the openid via
// the admin-configured WeChat login server.
func GetWeChatIdByCode(code string) (string, error) {
	if code == "" {
		return "", errors.New("无效的参数")
	}
	addr := WeChatServerAddress()
	if addr == "" {
		return "", errors.New("微信登录服务未配置")
	}
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/wechat/user?code=%s", addr, url.QueryEscape(code)), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", WeChatServerToken())
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var res weChatLoginResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", errors.New("微信登录服务响应无效")
	}
	if !res.Success {
		return "", errors.New(res.Message)
	}
	if res.Data == "" {
		return "", errors.New("验证码错误或已过期")
	}
	return res.Data, nil
}

// FindUserByWeChatId returns the user bound to the given WeChat openid.
func FindUserByWeChatId(id string) *model.User {
	return findUserByProviderField("wechat", id)
}

// FindUserByTelegramId returns the user bound to the given Telegram id.
func FindUserByTelegramId(id string) *model.User {
	return findUserByProviderField("telegram", id)
}

// NextUserId returns max(id)+1 across users (used for WeChat auto-registration
// usernames), or 1 when no users exist.
func NextUserId() int {
	var maxID int
	model.DB.Model(&model.User{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID)
	return maxID + 1
}
