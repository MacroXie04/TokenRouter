package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestStatusPublishesBrandAndNavigationPolicies(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.SystemNameOption:          "Acme Router",
		setting.LogoOption:                "/assets/acme-logo.png?version=2",
		setting.HeaderNavModulesOption:    `{"pricing":{"enabled":false},"rankings":{"requireAuth":true}}`,
		setting.SidebarModulesAdminOption: `{"chat":{"playground":false}}`,
	}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			SystemName          string `json:"system_name"`
			SiteName            string `json:"site_name"`
			Logo                string `json:"logo"`
			HeaderNavModules    string `json:"HeaderNavModules"`
			SidebarModulesAdmin string `json:"SidebarModulesAdmin"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.Equal(t, "Acme Router", payload.Data.SystemName)
	assert.Equal(t, "Acme Router", payload.Data.SiteName)
	assert.Equal(t, "/assets/acme-logo.png?version=2", payload.Data.Logo)
	assert.JSONEq(t,
		`{"pricing":{"enabled":false},"rankings":{"requireAuth":true}}`,
		payload.Data.HeaderNavModules)
	assert.JSONEq(t, `{"chat":{"playground":false}}`, payload.Data.SidebarModulesAdmin)
}

func TestStatusPublishesValidatedAnnouncementsOnlyWhenEnabled(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ConsoleAnnouncementsEnabledOption: "true",
		setting.ConsoleAnnouncementsOption: `[` +
			`{"id":"older","type":"warning","content":"Older","publishDate":"2026-01-01T00:00:00Z"},` +
			`{"id":2,"type":"success","content":"Newer","extra":"Details","publishDate":"2026-02-01T00:00:00Z"}` +
			`]`,
	}))

	router := gin.New()
	router.GET("/api/status", GetStatus)
	request := func() map[string]any {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		var payload struct {
			Success bool           `json:"success"`
			Data    map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		return payload.Data
	}

	data := request()
	assert.Equal(t, true, data["announcements_enabled"])
	items, ok := data["announcements"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	assert.Equal(t, "Newer", items[0].(map[string]any)["content"])

	require.NoError(t, setting.UpdateOption(setting.ConsoleAnnouncementsEnabledOption, "false"))
	data = request()
	assert.Equal(t, false, data["announcements_enabled"])
	assert.NotContains(t, data, "announcements")
}

func TestStatusPublishesPersistedOperationModes(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.SelfUseModeEnabledOption:       "true",
		setting.DemoSiteEnabledOption:          "false",
		setting.RegistrationEnabledOption:      "false",
		setting.PasswordLoginEnabledOption:     "false",
		setting.PasswordRegisterEnabledOption:  "true",
		setting.EmailVerificationEnabledOption: "true",
	}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			SelfUseModeEnabled      bool `json:"self_use_mode_enabled"`
			DemoSiteEnabled         bool `json:"demo_site_enabled"`
			RegisterEnabled         bool `json:"register_enabled"`
			PasswordLoginEnabled    bool `json:"password_login_enabled"`
			PasswordRegisterEnabled bool `json:"password_register_enabled"`
			EmailVerification       bool `json:"email_verification"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.True(t, payload.Data.SelfUseModeEnabled)
	assert.False(t, payload.Data.DemoSiteEnabled)
	assert.False(t, payload.Data.RegisterEnabled)
	assert.False(t, payload.Data.PasswordLoginEnabled)
	assert.True(t, payload.Data.PasswordRegisterEnabled)
	assert.True(t, payload.Data.EmailVerification)
}

func TestStatusPublishesBoundedChatPresetsAndServerAddress(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ServerAddressOption: "https://router.example.test",
		setting.ChatsOption:         `[{"Hosted chat":"https://chat.example.test/?key={key}"}]`,
	}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			ServerAddress string              `json:"server_address"`
			Chats         []map[string]string `json:"chats"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.Equal(t, "https://router.example.test", payload.Data.ServerAddress)
	assert.Equal(t, []map[string]string{{"Hosted chat": "https://chat.example.test/?key={key}"}}, payload.Data.Chats)
}

func TestStatusPublishesOnlyCompleteBuiltInOAuthMethods(t *testing.T) {
	initSetupTestDB(t)
	t.Setenv("GITHUB_CLIENT_ID", "github-client")
	t.Setenv("GITHUB_CLIENT_SECRET", "github-secret")
	t.Setenv("DISCORD_CLIENT_ID", "")
	t.Setenv("DISCORD_CLIENT_SECRET", "")
	t.Setenv("LINUXDO_CLIENT_ID", "linuxdo-client")
	t.Setenv("LINUXDO_CLIENT_SECRET", "linuxdo-secret")
	t.Setenv("OIDC_CLIENT_ID", "oidc-client")
	t.Setenv("OIDC_CLIENT_SECRET", "oidc-secret")
	t.Setenv("OIDC_AUTH_URL", "")
	t.Setenv("OIDC_TOKEN_URL", "")
	t.Setenv("OIDC_USER_INFO_URL", "")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)

	requestStatus := func() (bool, bool, bool, bool, string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		var payload struct {
			Data struct {
				GitHub  bool `json:"github_oauth"`
				Discord bool `json:"discord_oauth"`
				OIDC    bool `json:"oidc_enabled"`
				LinuxDO bool `json:"linuxdo_oauth"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		return payload.Data.GitHub, payload.Data.Discord, payload.Data.OIDC,
			payload.Data.LinuxDO, recorder.Body.String()
	}

	github, discord, oidc, linuxDO, body := requestStatus()
	assert.True(t, github)
	assert.False(t, discord)
	assert.False(t, oidc, "credentials alone cannot advertise an unusable OIDC flow")
	assert.True(t, linuxDO)
	assert.Contains(t, body, `"github_client_id":"github-client"`)
	assert.Contains(t, body, `"linuxdo_client_id":"linuxdo-client"`)
	assert.NotContains(t, body, `"discord_client_id"`)
	assert.NotContains(t, body, `"oidc_client_id"`)
	assert.NotContains(t, body, "github-secret")
	assert.NotContains(t, body, "linuxdo-secret")
	assert.NotContains(t, body, "oidc-secret")

	t.Setenv("OIDC_AUTH_URL", "https://identity.example.test/authorize")
	t.Setenv("OIDC_TOKEN_URL", "https://identity.example.test/token")
	t.Setenv("OIDC_USER_INFO_URL", "https://identity.example.test/userinfo")
	_, _, oidc, _, body = requestStatus()
	assert.True(t, oidc)
	assert.Contains(t, body, `"oidc_client_id":"oidc-client"`)
	assert.Contains(t, body, `"oidc_authorization_endpoint":"https://identity.example.test/authorize"`)
	assert.Contains(t, body, `"oidc_display_name":"OIDC"`)
}

func TestStatusPublishesOnlyRunnablePasskeyConfiguration(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PasskeyEnabledOption:             "true",
		setting.PasskeyRPDisplayNameOption:       "Acme Passkeys",
		setting.PasskeyRPIDOption:                "example.test",
		setting.PasskeyOriginsOption:             "https://login.example.test",
		setting.PasskeyUserVerificationOption:    "required",
		setting.PasskeyAllowInsecureOriginOption: "false",
	}))

	router := gin.New()
	router.GET("/api/status", GetStatus)
	request := func() map[string]any {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		var payload struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		return payload.Data
	}

	data := request()
	assert.Equal(t, true, data["passkey_login"])
	assert.Equal(t, "Acme Passkeys", data["passkey_display_name"])
	assert.Equal(t, "example.test", data["passkey_rp_id"])
	assert.Equal(t, "https://login.example.test", data["passkey_origins"])
	assert.Equal(t, "required", data["passkey_user_verification"])

	t.Setenv("WEBAUTHN_ORIGINS", "http://public.example.test")
	data = request()
	assert.Equal(t, false, data["passkey_login"])
	assert.NotContains(t, data, "passkey_rp_id")
	assert.NotContains(t, data, "passkey_origins")
}

func TestStatusPublishesOnlyUsableTelegramLoginMetadata(t *testing.T) {
	initSetupTestDB(t)
	t.Setenv("TELEGRAM_BOT_NAME", "@TokenRouterBot")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123456:"+strings.Repeat("A", 32))
	require.NoError(t, setting.UpdateOption(setting.TelegramOAuthEnabledOption, "true"))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)

	requestStatus := func() (bool, string, string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var payload struct {
			Data struct {
				TelegramOAuth   bool   `json:"telegram_oauth"`
				TelegramBotName string `json:"telegram_bot_name"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		return payload.Data.TelegramOAuth, payload.Data.TelegramBotName, recorder.Body.String()
	}

	enabled, botName, body := requestStatus()
	assert.True(t, enabled)
	assert.Equal(t, "TokenRouterBot", botName)
	assert.NotContains(t, body, strings.Repeat("A", 32))

	// The option alone cannot advertise a login flow that will fail at the
	// callback boundary. Invalid credentials also suppress the bot name.
	t.Setenv("TELEGRAM_BOT_TOKEN", "not-a-valid-token")
	enabled, botName, _ = requestStatus()
	assert.False(t, enabled)
	assert.Empty(t, botName)

	require.NoError(t, setting.UpdateOption(setting.TelegramOAuthEnabledOption, "false"))
	t.Setenv("TELEGRAM_BOT_TOKEN", "123456:"+strings.Repeat("B", 32))
	enabled, botName, _ = requestStatus()
	assert.False(t, enabled)
	assert.Empty(t, botName)
}

func TestStatusFailsClosedForUnsafeOrOversizedPublicConfiguration(t *testing.T) {
	initSetupTestDB(t)
	t.Setenv("NODE_NAME", strings.Repeat("n", maxStatusNameBytes+1))
	t.Setenv("WECHAT_QRCODE_URL", "javascript:alert(1)")
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.SystemNameOption:          strings.Repeat("s", maxStatusNameBytes+1),
		setting.LogoOption:                "javascript:alert(1)",
		setting.ServerAddressOption:       "https://user:password@router.example.test",
		setting.TurnstileSiteKeyOption:    "site\u202ekey",
		setting.HeaderNavModulesOption:    strings.Repeat("x", maxStatusNavigationBytes+1),
		setting.SidebarModulesAdminOption: strings.Repeat("y", maxStatusNavigationBytes+1),
	}))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Data struct {
			SystemName       string `json:"system_name"`
			SiteName         string `json:"site_name"`
			Logo             string `json:"logo"`
			NodeName         string `json:"node_name"`
			WeChatQRCode     string `json:"wechat_qrcode"`
			TurnstileSiteKey string `json:"turnstile_site_key"`
			ServerAddress    string `json:"server_address"`
			HeaderNav        string `json:"HeaderNavModules"`
			SidebarAdmin     string `json:"SidebarModulesAdmin"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, common.ProductName, payload.Data.SystemName)
	assert.Equal(t, common.ProductName, payload.Data.SiteName)
	assert.Empty(t, payload.Data.Logo)
	assert.Equal(t, "tokenrouter-node-1", payload.Data.NodeName)
	assert.Empty(t, payload.Data.WeChatQRCode)
	assert.Empty(t, payload.Data.TurnstileSiteKey)
	assert.Empty(t, payload.Data.ServerAddress)
	assert.Empty(t, payload.Data.HeaderNav)
	assert.Empty(t, payload.Data.SidebarAdmin)
}

func TestStatusAllowsSafePublicImageSources(t *testing.T) {
	initSetupTestDB(t)
	t.Setenv("WECHAT_QRCODE_URL", "/assets/wechat.png?version=1")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/status", GetStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Data struct {
			WeChatQRCode string `json:"wechat_qrcode"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.Equal(t, "/assets/wechat.png?version=1", payload.Data.WeChatQRCode)
}
