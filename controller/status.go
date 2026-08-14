// Package controller implements TokenRouter's HTTP handlers.
package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/setting"
)

// StartTime is the process start timestamp (set in main.go).
var StartTime = common.NowTimestamp()

// GetStatus returns the health/status payload.
func GetStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"version":    common.Version,
			"start_time": StartTime,
			"app_name":   common.ProductName,
			"site_name":  setting.GetSiteName(),
			"node_name":  common.GetEnv("NODE_NAME", "tokenrouter-node-1"),
		},
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
