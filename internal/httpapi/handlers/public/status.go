// Package public implements TokenRouter's public status handlers.
package public

import (
	"github.com/gin-gonic/gin"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/buildinfo"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// StartTime is the process start timestamp (set in main.go).
var StartTime = wallclock.NowTimestamp()

const (
	maxStatusNameBytes       = 128
	maxStatusServerURLBytes  = 2048
	maxStatusImageURLBytes   = 4096
	maxStatusSiteKeyBytes    = 256
	maxStatusOAuthIDBytes    = 256
	maxStatusOAuthURLBytes   = 2048
	maxStatusPasskeyBytes    = 16 << 10
	maxStatusNavigationBytes = 64 << 10
)

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
	githubConfig := authsvc.GetOAuthConfig("github")
	discordConfig := authsvc.GetOAuthConfig("discord")
	oidcConfig := authsvc.GetOAuthConfig("oidc")
	linuxDOConfig := authsvc.GetOAuthConfig("linuxdo")
	passkeyEnabled := false
	var passkeyConfig setting.PasskeySetting
	if setting.GetAuthenticationSetting().Passkey.Enabled {
		if effective, err := authsvc.EffectivePasskeySetting(); err == nil {
			passkeyConfig = effective
			passkeyEnabled = true
		}
	}
	telegramOAuth := authsvc.TelegramOAuthEnabled()
	telegramBotName := ""
	if telegramOAuth {
		telegramBotName = authsvc.TelegramBotName()
	}
	systemName := boundedPublicStatusText(setting.GetSiteName(), maxStatusNameBytes, buildinfo.ProductName)
	consoleContent := setting.GetConsoleContentSetting()
	data := gin.H{
		"version":                   buildinfo.Version,
		"start_time":                StartTime,
		"app_name":                  buildinfo.ProductName,
		"system_name":               systemName,
		"site_name":                 systemName,
		"logo":                      publicStatusImageURL(setting.GetOption(setting.LogoOption)),
		"node_name":                 boundedPublicStatusText(env.GetEnv("NODE_NAME", "tokenrouter-node-1"), maxStatusNameBytes, "tokenrouter-node-1"),
		"wechat_login":              authsvc.WeChatAuthEnabled(),
		"wechat_qrcode":             publicStatusImageURL(authsvc.WeChatQRCodeURL()),
		"turnstile_check":           authsvc.TurnstileEnabled(),
		"turnstile_site_key":        boundedPublicStatusText(authsvc.TurnstileSiteKey(), maxStatusSiteKeyBytes, ""),
		"passkey_login":             passkeyEnabled,
		"github_oauth":              githubConfig.Enabled,
		"discord_oauth":             discordConfig.Enabled,
		"oidc_enabled":              oidcConfig.Enabled,
		"linuxdo_oauth":             linuxDOConfig.Enabled,
		"telegram_oauth":            telegramOAuth,
		"telegram_bot_name":         telegramBotName,
		"checkin_enabled":           setting.GetCheckinSetting().Enabled,
		"self_use_mode_enabled":     setting.GetOptionBool(setting.SelfUseModeEnabledOption, false),
		"demo_site_enabled":         setting.GetOptionBool(setting.DemoSiteEnabledOption, false),
		"register_enabled":          setting.GetOptionBool(setting.RegistrationEnabledOption, true),
		"password_login_enabled":    setting.GetOptionBool(setting.PasswordLoginEnabledOption, true),
		"password_register_enabled": setting.GetOptionBool(setting.PasswordRegisterEnabledOption, true),
		"email_verification":        setting.GetOptionBool(setting.EmailVerificationEnabledOption, false),
		"default_collapse_sidebar":  setting.GetOperationsSetting().DefaultCollapseSidebar,
		"server_address":            publicStatusServerURL(setting.GetOption(setting.ServerAddressOption)),
		"chats":                     setting.GetChatPresetMaps(),
		// Expose the persisted public navigation policy so the browser applies the
		// same conditional visibility and authentication rules as the API.
		"HeaderNavModules": boundedPublicStatusText(
			setting.GetOptionOrDefault(setting.HeaderNavModulesOption, ""),
			maxStatusNavigationBytes,
			"",
		),
		"SidebarModulesAdmin": boundedPublicStatusText(
			setting.GetOptionOrDefault(setting.SidebarModulesAdminOption, ""),
			maxStatusNavigationBytes,
			"",
		),
		"api_info_enabled":      consoleContent.APIInfoEnabled,
		"faq_enabled":           consoleContent.FAQEnabled,
		"uptime_kuma_enabled":   consoleContent.UptimeKumaEnabled,
		"announcements_enabled": consoleContent.AnnouncementsEnabled,
	}
	// Provider metadata is advertised only alongside a runnable provider. A
	// malformed environment override therefore cannot publish a dead sign-in
	// button or stale client identifier. Secrets are never included.
	if githubConfig.Enabled {
		data["github_client_id"] = boundedPublicStatusText(githubConfig.ClientID, maxStatusOAuthIDBytes, "")
	}
	if discordConfig.Enabled {
		data["discord_client_id"] = boundedPublicStatusText(discordConfig.ClientID, maxStatusOAuthIDBytes, "")
	}
	if linuxDOConfig.Enabled {
		data["linuxdo_client_id"] = boundedPublicStatusText(linuxDOConfig.ClientID, maxStatusOAuthIDBytes, "")
		data["linuxdo_minimum_trust_level"] = linuxDOConfig.MinimumTrustLevel
	}
	if oidcConfig.Enabled {
		data["oidc_client_id"] = boundedPublicStatusText(oidcConfig.ClientID, maxStatusOAuthIDBytes, "")
		data["oidc_authorization_endpoint"] = boundedPublicStatusText(oidcConfig.AuthURL, maxStatusOAuthURLBytes, "")
		data["oidc_display_name"] = boundedPublicStatusText(oidcConfig.DisplayName, maxStatusNameBytes, "OIDC")
	}
	if passkeyEnabled {
		data["passkey_display_name"] = boundedPublicStatusText(passkeyConfig.RPDisplayName, maxStatusNameBytes, buildinfo.ProductName)
		data["passkey_rp_id"] = boundedPublicStatusText(passkeyConfig.RPID, 253, "")
		data["passkey_origins"] = boundedPublicStatusText(strings.Join(passkeyConfig.Origins, ","), maxStatusPasskeyBytes, "")
		data["passkey_allow_insecure"] = passkeyConfig.AllowInsecureOrigin
		data["passkey_user_verification"] = passkeyConfig.UserVerification
		data["passkey_attachment"] = passkeyConfig.AttachmentPreference
	}
	if consoleContent.APIInfoEnabled {
		data["api_info"] = consoleContent.APIInfo
	}
	if consoleContent.FAQEnabled {
		data["faq"] = consoleContent.FAQ
	}
	if consoleContent.AnnouncementsEnabled {
		data["announcements"] = consoleContent.Announcements
	}
	data["user_agreement_enabled"] = setting.GetOption("legal.user_agreement") != ""
	data["privacy_policy_enabled"] = setting.GetOption("legal.privacy_policy") != ""
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
				Icon:                  publicStatusImageURL(provider.Icon),
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

func boundedPublicStatusText(value string, maximumBytes int, fallback string) string {
	if len(value) > maximumBytes || !utf8.ValidString(value) {
		return fallback
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return fallback
		}
	}
	return value
}

func publicStatusServerURL(raw string) string {
	return safePublicStatusURL(raw, maxStatusServerURLBytes, false, false)
}

func publicStatusImageURL(raw string) string {
	return safePublicStatusURL(raw, maxStatusImageURLBytes, true, true)
}

func safePublicStatusURL(raw string, maximumBytes int, allowRelative, allowQuery bool) string {
	if raw == "" {
		return ""
	}
	if raw != strings.TrimSpace(raw) || strings.Contains(raw, `\`) ||
		boundedPublicStatusText(raw, maximumBytes, "") == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	if allowRelative && parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(parsed.Path, "/") &&
		!strings.HasPrefix(parsed.Path, "//") {
		return raw
	}
	if !parsed.IsAbs() || parsed.Hostname() == "" || !allowQuery && parsed.RawQuery != "" {
		return ""
	}
	if parsed.Scheme == "https" {
		return raw
	}
	if parsed.Scheme != "http" {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || address != nil && address.IsLoopback() {
		return raw
	}
	return ""
}

// GetNotice returns the public notice content.
func GetNotice(c *gin.Context) {
	writePublicContent(c, "Notice")
}

// GetAbout returns the about-page content.
func GetAbout(c *gin.Context) {
	writePublicContent(c, "About")
}

// Reference compatibility: About is the configured string itself, while
// process metadata remains available from /api/status. All five public
// documents therefore share the same bounded response shape.

// GetHomePageContent returns the home-page content blocks.
func GetHomePageContent(c *gin.Context) {
	writePublicContent(c, "HomePageContent")
}

// GetPricing returns the pricing page configuration.
func GetPricing(c *gin.Context) {
	userGroup := ""
	if userID := requestctx.GetUserId(c); userID > 0 {
		user, err := userssvc.GetUserByID(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": "unable to resolve pricing access",
			})
			return
		}
		userGroup = user.Group
		if userGroup == "" {
			userGroup = userssvc.GroupDefault
		}
	}
	catalog, err := billingsvc.BuildPricingCatalog(userGroup)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "unable to build pricing catalog",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":            true,
		"message":            "",
		"data":               catalog.Items,
		"vendors":            catalog.Vendors,
		"group_ratio":        catalog.GroupRatio,
		"usable_group":       catalog.UsableGroup,
		"supported_endpoint": catalog.SupportedEndpoint,
		"auto_groups":        catalog.AutoGroups,
		"pricing_version":    catalog.Version,
	})
}

// GetUserAgreement returns the user agreement text.
func GetUserAgreement(c *gin.Context) {
	writePublicContent(c, "legal.user_agreement")
}

// GetPrivacyPolicy returns the privacy policy text.
func GetPrivacyPolicy(c *gin.Context) {
	writePublicContent(c, "legal.privacy_policy")
}

// publicContentMaxBytes keeps a bad or unexpectedly large database option from
// turning an unauthenticated endpoint into an unbounded response. The browser
// independently applies a one-million-character ceiling before rendering.
const publicContentMaxBytes = 1_000_000

func writePublicContent(c *gin.Context, optionKey string) {
	content := setting.GetOption(optionKey)
	if len(content) > publicContentMaxBytes {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "public content exceeds the safe display limit",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    content,
	})
}
