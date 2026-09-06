package auth

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxWeChatResponseBytes   = int64(1 << 20)
	maxWeChatCodeBytes       = maxOAuthEndpointQueryValueBytes
	maxWeChatAddressBytes    = 2048
	maxWeChatRequestURLBytes = 16 << 10
	maxWeChatTokenBytes      = maxOAuthClientSecretBytes
	maxWeChatMessageBytes    = 512
	maxWeChatProviderIDBytes = maxOAuthBuiltInSubjectBytes
)

var weChatHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	// This destination is operator-configurable. Do not honor environment
	// proxies, because a proxy could resolve the destination again after the
	// application has validated it and thereby bypass the SSRF policy.
	Transport: &http.Transport{DialContext: httpx.SafeDialContext},
	// The request contains both an Authorization secret and a one-time login
	// code. No redirect is part of the configured exchange contract.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// WeChatAuthEnabled reports whether WeChat login/registration is enabled.
func WeChatAuthEnabled() bool {
	return setting.GetOptionBool(setting.WeChatAuthEnabledOption, false)
}

// WeChatServerAddress returns the configured WeChat login server base address
// (admin option with environment fallback).
func WeChatServerAddress() string {
	return env.GetEnv("WECHAT_SERVER_ADDRESS", setting.GetOptionOrDefault(setting.WeChatServerAddressOption, ""))
}

// WeChatServerToken returns the authorization token for the WeChat login server.
func WeChatServerToken() string {
	return env.GetEnv("WECHAT_SERVER_TOKEN", setting.GetOptionOrDefault(setting.WeChatServerTokenOption, ""))
}

// WeChatQRCodeURL returns the public WeChat QR code image URL shown on the
// login/bind dialogs.
func WeChatQRCodeURL() string {
	return env.GetEnv("WECHAT_QRCODE_URL", setting.GetOptionOrDefault(setting.WeChatQRCodeOption, ""))
}

type weChatLoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// GetWeChatIdByCode exchanges a WeChat authorization code for the openid via
// the admin-configured WeChat login server.
func GetWeChatIdByCode(code string) (string, error) {
	if err := validateWeChatText(code, maxWeChatCodeBytes, false); err != nil {
		return "", errors.New("无效的参数")
	}
	addr := WeChatServerAddress()
	if addr == "" {
		return "", errors.New("微信登录服务未配置")
	}
	endpoint, err := weChatUserEndpoint(addr, code)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.New("微信登录服务请求失败")
	}
	token := WeChatServerToken()
	if err := validateWeChatText(token, maxWeChatTokenBytes, true); err != nil || token != strings.TrimSpace(token) {
		return "", errors.New("微信登录服务令牌无效")
	}
	req.Header.Set("Authorization", token)
	resp, err := weChatHTTPClient.Do(req)
	if err != nil {
		return "", errors.New("微信登录服务请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("微信登录服务请求失败（状态码 %d）", resp.StatusCode)
	}
	body, err := httpx.ReadAllLimited(resp.Body, maxWeChatResponseBytes)
	if err != nil {
		return "", fmt.Errorf("read WeChat response: %w", err)
	}
	raw, err := decodeBoundedOAuthJSONObject(body)
	if err != nil {
		return "", errors.New("微信登录服务响应无效")
	}
	res := weChatLoginResponse{}
	var ok bool
	if res.Success, ok = raw["success"].(bool); !ok {
		return "", errors.New("微信登录服务响应无效")
	}
	if value, present := raw["message"]; present {
		res.Message, ok = value.(string)
		if !ok {
			return "", errors.New("微信登录服务响应无效")
		}
	}
	if value, present := raw["data"]; present {
		res.Data, ok = value.(string)
		if !ok {
			return "", errors.New("微信登录服务响应无效")
		}
	}
	if err := validateWeChatText(res.Message, maxWeChatMessageBytes, true); err != nil {
		return "", errors.New("微信登录服务响应无效")
	}
	if !res.Success {
		return "", errors.New("验证码错误或已过期")
	}
	providerID := strings.TrimSpace(res.Data)
	if providerID != res.Data || validateWeChatText(providerID, maxWeChatProviderIDBytes, false) != nil {
		return "", errors.New("验证码错误或已过期")
	}
	return providerID, nil
}

func weChatUserEndpoint(baseAddress, code string) (string, error) {
	baseAddress = strings.TrimSpace(baseAddress)
	if validateWeChatText(baseAddress, maxWeChatAddressBytes, false) != nil ||
		validateWeChatText(code, maxWeChatCodeBytes, false) != nil {
		return "", errors.New("微信登录服务地址无效")
	}
	base, err := validateOAuthURL(baseAddress, maxWeChatAddressBytes, false)
	if err != nil {
		return "", errors.New("微信登录服务地址无效")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/wechat/user"
	base.RawPath = ""
	query := base.Query()
	query.Set("code", code)
	base.RawQuery = query.Encode()
	endpoint, err := validateOAuthURL(base.String(), maxWeChatRequestURLBytes, true)
	if err != nil {
		return "", errors.New("微信登录服务地址无效")
	}
	return endpoint.String(), nil
}

func validateWeChatText(value string, maximumBytes int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) {
		return errors.New("value exceeds safe limits")
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return errors.New("value contains unsafe characters")
		}
	}
	return nil
}

// FindUserByWeChatId returns the canonical owner of a WeChat openid.
func FindUserByWeChatId(id string) (*model.User, error) {
	return FindUserByProviderIdentity("wechat", id)
}

// FindUserByTelegramId returns the canonical owner of a Telegram id.
func FindUserByTelegramId(id string) (*model.User, error) {
	return FindUserByProviderIdentity("telegram", id)
}

// NextUserId returns max(id)+1 across users (used for WeChat auto-registration
// usernames), or 1 when no users exist.
func NextUserId() int {
	var maxID int
	model.DB.Model(&model.User{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID)
	return maxID + 1
}
