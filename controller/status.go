// Package controller implements TokenRouter's HTTP handlers.
package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// StartTime is the process start timestamp (set in main.go).
var StartTime = common.NowTimestamp()

// TestStatus reports database connectivity for monitoring probes. The
// reference includes HTTP request counters in http_stats; TokenRouter does not
// track those counters, so the field is an empty object.
func TestStatus(c *gin.Context) {
	if model.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "数据库连接失败",
		})
		return
	}
	sqlDB, err := model.DB.DB()
	if err != nil || sqlDB.Ping() != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"message": "数据库连接失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"message":    "Server is running",
		"http_stats": gin.H{},
	})
}

// GetStatus returns the health/status payload.
func GetStatus(c *gin.Context) {
	data := gin.H{
		"version":            common.Version,
		"start_time":         StartTime,
		"app_name":           common.ProductName,
		"site_name":          setting.GetSiteName(),
		"node_name":          common.GetEnv("NODE_NAME", "tokenrouter-node-1"),
		"wechat_login":       service.WeChatAuthEnabled(),
		"wechat_qrcode":      service.WeChatQRCodeURL(),
		"turnstile_check":    service.TurnstileEnabled(),
		"turnstile_site_key": service.TurnstileSiteKey(),
	}
	// Enabled custom OAuth providers surface their public login metadata
	// (never the client secret); the key is absent when none are enabled.
	if providers, err := model.GetEnabledCustomOAuthProviders(); err == nil && len(providers) > 0 {
		type customOAuthInfo struct {
			Id                    int    `json:"id"`
			Name                  string `json:"name"`
			Slug                  string `json:"slug"`
			Icon                  string `json:"icon"`
			ClientId              string `json:"client_id"`
			AuthorizationEndpoint string `json:"authorization_endpoint"`
			Scopes                string `json:"scopes"`
		}
		providersInfo := make([]customOAuthInfo, 0, len(providers))
		for _, provider := range providers {
			providersInfo = append(providersInfo, customOAuthInfo{
				Id:                    provider.Id,
				Name:                  provider.Name,
				Slug:                  provider.Slug,
				Icon:                  provider.Icon,
				ClientId:              provider.ClientId,
				AuthorizationEndpoint: provider.AuthorizationEndpoint,
				Scopes:                provider.Scopes,
			})
		}
		data["custom_oauth_providers"] = providersInfo
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

// GetNotice returns the public notice content.
func GetNotice(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(setting.GetOptionOrDefault("Notice", "")))
}

// GetAbout returns the about-page content.
func GetAbout(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(gin.H{
		"version":  common.Version,
		"app_name": common.ProductName,
		"about":    setting.GetOptionOrDefault("About", ""),
	}))
}

// GetHomePageContent returns the home-page content blocks.
func GetHomePageContent(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(setting.GetOptionOrDefault("HomePageContent", "")))
}

// GetPricing returns the pricing page configuration.
func GetPricing(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(setting.GetOptionOrDefault("Pricing", "{}")))
}

// GetUserAgreement returns the user agreement text.
func GetUserAgreement(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(setting.GetOptionOrDefault("UserAgreement", "")))
}

// GetPrivacyPolicy returns the privacy policy text.
func GetPrivacyPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, dto.Ok(setting.GetOptionOrDefault("PrivacyPolicy", "")))
}
