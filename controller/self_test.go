package controller_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const telegramControllerBotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"

// setupSelfTest prepares a DB, a signed-in user, and returns an authed request
// factory plus the user id and current sid.
func setupSelfTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "self.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}, &model.UserSession{},
		&model.PasskeyCredential{}, &model.TwoFA{}, &model.TwoFABackupCode{}, &model.AuthFlow{},
		&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}, &model.ExternalIdentityClaim{}))
	model.DB = db
	model.LOG_DB = db
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))

	passwordHash, err := common.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{Username: "selfuser", Password: passwordHash, Role: 1, Status: model.UserStatusEnabled,
		Quota: 1000, AffQuota: 1000000, Email: "self@example.com", EmailVerified: true, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	sid, access, refresh, err := service.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	r := router.SetUpRouter()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return r, do, user.Id, sid
}

func TestUpdateSelfPasswordRotatesAuthAndKeepsCurrentSession(t *testing.T) {
	_, do, userId, sid := setupSelfTest(t)
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	otherSID, _, _, err := service.CompleteLogin(&user, "127.0.0.2", "other", "test")
	require.NoError(t, err)

	rec := do(http.MethodPut, "/api/user/self",
		`{"display_name":"Updated","old_password":"old-password","password":"new-password"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, strings.Join(rec.Header().Values("Set-Cookie"), ";"), "access_token=")

	var updated model.User
	require.NoError(t, model.DB.First(&updated, userId).Error)
	assert.True(t, common.PasswordVerify("new-password", updated.Password))
	assert.Equal(t, "Updated", updated.DisplayName)
	assert.EqualValues(t, 2, updated.AuthVersion)

	var current model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&current).Error)
	assert.Equal(t, service.SessionStatusActive, current.Status)
	assert.EqualValues(t, 2, current.UserAuthVersion)
	var other model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", otherSID).First(&other).Error)
	assert.Equal(t, service.SessionStatusRevoked, other.Status)
	assert.Equal(t, "password_changed", other.RevokedReason)
}

func TestUpdateSelfRejectsUnverifiedEmailAndBadPasswordProof(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)

	rec := do(http.MethodPut, "/api/user/self", `{"email":"attacker@example.com"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "邮箱验证码")

	rec = do(http.MethodPut, "/api/user/self",
		`{"display_name":"Must Not Persist","old_password":"wrong","password":"new-password"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, "", user.DisplayName)
	assert.Equal(t, "self@example.com", user.Email)
	assert.True(t, common.PasswordVerify("old-password", user.Password))
	assert.EqualValues(t, 1, user.AuthVersion)
}

func TestUpdateSelfPersistsBoundedLanguageAndSidebarPreferences(t *testing.T) {
	_, do, userID, _ := setupSelfTest(t)
	_, err := service.UpdateUserBillingPreference(userID, service.BillingPreferenceWalletFirst)
	require.NoError(t, err)

	decodeOK(t, do(http.MethodPut, "/api/user/self", `{"language":"zh-TW"}`))
	modules := `{"chat":{"enabled":true,"playground":false,"chat":true},` +
		`"console":{"enabled":true,"detail":true,"token":true,"log":false,"midjourney":true,"task":true},` +
		`"personal":{"enabled":true,"topup":true,"personal":true}}`
	decodeOK(t, do(http.MethodPut, "/api/user/self",
		fmt.Sprintf(`{"sidebar_modules":%q}`, modules)))

	var stored model.User
	require.NoError(t, model.DB.First(&stored, userID).Error)
	var settings map[string]any
	require.NoError(t, json.Unmarshal([]byte(stored.Setting), &settings))
	assert.Equal(t, "zh-TW", settings["language"])
	assert.Equal(t, service.BillingPreferenceWalletFirst, settings["billing_preference"],
		"profile preferences must not clobber billing settings")
	storedModules, ok := settings["sidebar_modules"].(string)
	require.True(t, ok)
	assert.JSONEq(t, modules, storedModules)

	body := decodeOK(t, do(http.MethodGet, "/api/user/self", ""))
	data := body["data"].(map[string]any)
	assert.Equal(t, storedModules, data["sidebar_modules"])
	safeSetting, ok := data["setting"].(string)
	require.True(t, ok)
	var visibleSettings map[string]any
	require.NoError(t, json.Unmarshal([]byte(safeSetting), &visibleSettings))
	assert.Equal(t, "zh-TW", visibleSettings["language"])
	assert.Equal(t, service.BillingPreferenceWalletFirst, visibleSettings["billing_preference"])
	assert.Equal(t, storedModules, visibleSettings["sidebar_modules"])
	assert.Equal(t, false, visibleSettings["webhook_secret_configured"])
	assert.Equal(t, false, visibleSettings["gotify_token_configured"])
	assert.Equal(t, true, data["email_verified"])
	assert.NotContains(t, data, "password")
	assert.NotContains(t, data, "access_token")
}

func TestUpdateSelfRejectsInvalidOrMixedProfilePreferences(t *testing.T) {
	_, do, userID, _ := setupSelfTest(t)

	for _, body := range []string{
		`{"language":"de"}`,
		`{"language":" zh"}`,
		`{"sidebar_modules":"{} trailing"}`,
		`{"sidebar_modules":"{\"admin\":{\"setting\":true}}"}`,
		`{"sidebar_modules":"{\"chat\":{\"unknown\":true}}"}`,
		`{"language":"en","display_name":"must-not-persist"}`,
	} {
		recorder := do(http.MethodPut, "/api/user/self", body)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "body=%s response=%s", body, recorder.Body.String())
	}

	var stored model.User
	require.NoError(t, model.DB.First(&stored, userID).Error)
	assert.Empty(t, stored.DisplayName)
	assert.NotContains(t, stored.Setting, "language")
	assert.NotContains(t, stored.Setting, "sidebar_modules")
}

func decodeOK(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, true, body["success"])
	return body
}

func TestSelfAffAndTransfer(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PaymentComplianceConfirmedOption:    "true",
		setting.PaymentComplianceTermsVersionOption: service.CurrentPaymentComplianceTermsVersion,
	}))

	// Affiliate info returns the own code + stats.
	body := decodeOK(t, do(http.MethodGet, "/api/user/aff", ""))
	data := body["data"].(map[string]any)
	assert.NotEmpty(t, data["aff_code"])
	assert.Equal(t, float64(1000000), data["aff_quota"])

	// Transfer below the minimum is rejected.
	rec := do(http.MethodPost, "/api/user/aff_transfer", `{"quota":1000}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Transferring more than available is rejected.
	rec = do(http.MethodPost, "/api/user/aff_transfer", `{"quota":1500000}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "不足")

	// A valid transfer moves quota.
	decodeOK(t, do(http.MethodPost, "/api/user/aff_transfer", `{"quota":500000}`))
	var got model.User
	require.NoError(t, model.DB.First(&got, userId).Error)
	assert.Equal(t, 500000, got.AffQuota)
	assert.Equal(t, 501000, got.Quota)
}

func TestSelfPasskeyListAndDelete(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: userId, CredentialID: "cred-1", PublicKey: "pk", Attachment: "platform",
	}).Error)

	body := decodeOK(t, do(http.MethodGet, "/api/user/passkey", ""))
	list := body["data"].([]any)
	require.Len(t, list, 1)

	decodeOK(t, do(http.MethodDelete, "/api/user/passkey", ""))
	var count int64
	model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", userId).Count(&count)
	assert.Zero(t, count)
}

func TestRegenerateBackupCodes(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)
	// 2FA must be enabled first.
	rec := do(http.MethodPost, "/api/user/2fa/backup_codes", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: userId, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
	rec = do(http.MethodPost, "/api/user/2fa/backup_codes", "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", decodeBody(t, rec)["code"])
	var stored int64
	model.DB.Model(&model.TwoFABackupCode{}).Where("user_id = ?", userId).Count(&stored)
	assert.Zero(t, stored)
}

func TestOAuthBindingsListAndUnbind(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)
	// Built-in identities (user columns) are NOT listed here — the endpoint is
	// custom-provider-only (reference semantics).
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userId).
		Update("github_id", "gh-1").Error)

	body := decodeOK(t, do(http.MethodGet, "/api/user/oauth/bindings", ""))
	bindings, ok := body["data"].([]any)
	require.True(t, ok, "data must be an array, got: %T", body["data"])
	require.Empty(t, bindings)

	provider := model.CustomOAuthProvider{
		Name: "Corp SSO", Slug: "corp-sso", Icon: "icon-url", Enabled: true,
		ClientId: "cid", ClientSecret: "sec",
		AuthorizationEndpoint: "https://sso.example.com/authorize",
		TokenEndpoint:         "https://sso.example.com/token",
		UserInfoEndpoint:      "https://sso.example.com/userinfo",
	}
	require.NoError(t, model.DB.Create(&provider).Error)
	require.NoError(t, model.DB.Create(&model.UserOAuthBinding{
		UserId: userId, ProviderId: provider.Id, ProviderUserId: "ext-42",
	}).Error)

	body = decodeOK(t, do(http.MethodGet, "/api/user/oauth/bindings", ""))
	bindings = body["data"].([]any)
	require.Len(t, bindings, 1)
	b := bindings[0].(map[string]any)
	assert.Equal(t, float64(provider.Id), b["provider_id"])
	assert.Equal(t, "Corp SSO", b["provider_name"])
	assert.Equal(t, "corp-sso", b["provider_slug"])
	assert.Equal(t, "icon-url", b["provider_icon"])
	assert.Equal(t, "ext-42", b["provider_user_id"])

	// A non-numeric provider id is rejected.
	rec := do(http.MethodDelete, "/api/user/oauth/bindings/nope", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "无效的提供商 ID")

	body = decodeOK(t, do(http.MethodDelete, fmt.Sprintf("/api/user/oauth/bindings/%d", provider.Id), ""))
	assert.Equal(t, "解绑成功", body["message"])
	var count int64
	model.DB.Model(&model.UserOAuthBinding{}).Where("user_id = ?", userId).Count(&count)
	assert.Zero(t, count)

	// Unbinding an already-unbound provider still succeeds (no existence
	// check — reference semantics), and built-in columns are untouched.
	decodeOK(t, do(http.MethodDelete, fmt.Sprintf("/api/user/oauth/bindings/%d", provider.Id), ""))
	var got model.User
	require.NoError(t, model.DB.First(&got, userId).Error)
	assert.Equal(t, "gh-1", got.GitHubId)
}

func TestUpdateUserSettingAndReminderOverride(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)

	// Invalid threshold / type are rejected.
	rec := do(http.MethodPut, "/api/user/setting", `{"quota_warning_threshold":0}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = do(http.MethodPut, "/api/user/setting", `{"quota_warning_threshold":100,"quota_warning_type":"pigeon"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	decodeOK(t, do(http.MethodPut, "/api/user/setting", `{"quota_warning_threshold":5000,"quota_warning_type":"email"}`))
	var got model.User
	require.NoError(t, model.DB.First(&got, userId).Error)
	assert.Equal(t, 5000, service.QuotaWarningThreshold(&got))
}

func TestUpdateUserSettingPersistsCompleteContractAndRedactsSecrets(t *testing.T) {
	_, do, userID, _ := setupSelfTest(t)
	payload := `{"quota_warning_threshold":4321,"notify_type":"webhook",` +
		`"notification_email":"notify@example.test","webhook_url":"https://hooks.example.test/quota",` +
		`"webhook_secret":"hook-secret","bark_url":"https://bark.example.test/key/{{title}}/{{content}}",` +
		`"gotify_url":"https://gotify.example.test/base","gotify_token":"gotify-secret",` +
		`"gotify_priority":7,"accept_unset_model_ratio_model":true,"record_ip_log":true,` +
		`"upstream_model_update_notify_enabled":true}`
	decodeOK(t, do(http.MethodPut, "/api/user/setting", payload))

	var stored model.User
	require.NoError(t, model.DB.First(&stored, userID).Error)
	assert.Contains(t, stored.Setting, "hook-secret")
	assert.Contains(t, stored.Setting, "gotify-secret")
	assert.NotContains(t, stored.Setting, "upstream_model_update_notify_enabled",
		"ordinary users cannot enable the administrator watcher")

	body := decodeOK(t, do(http.MethodGet, "/api/user/self", ""))
	data := body["data"].(map[string]any)
	safeSetting, ok := data["setting"].(string)
	require.True(t, ok)
	assert.NotContains(t, safeSetting, "hook-secret")
	assert.NotContains(t, safeSetting, "gotify-secret")
	var visible map[string]any
	require.NoError(t, json.Unmarshal([]byte(safeSetting), &visible))
	assert.Equal(t, "webhook", visible["notify_type"])
	assert.Equal(t, float64(4321), visible["quota_warning_threshold"])
	assert.Equal(t, true, visible["webhook_secret_configured"])
	assert.Equal(t, true, visible["gotify_token_configured"])
	assert.Equal(t, true, visible["accept_unset_model_ratio_model"])
	assert.Equal(t, true, visible["record_ip_log"])
	assert.NotContains(t, visible, "webhook_secret")
	assert.NotContains(t, visible, "gotify_token")

	before := stored.Setting
	for _, invalid := range []string{
		`{"quota_warning_threshold":10,"notify_type":"webhook","webhook_url":"http://public.example.test/hook"}`,
		`{"quota_warning_threshold":10,"notify_type":"email","quota_warning_type":"gotify"}`,
		`{"quota_warning_threshold":10,"notify_type":"gotify","gotify_url":"https://new-gotify.example.test"}`,
	} {
		recorder := do(http.MethodPut, "/api/user/setting", invalid)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "body=%s response=%s", invalid, recorder.Body.String())
		var after model.User
		require.NoError(t, model.DB.First(&after, userID).Error)
		assert.Equal(t, before, after.Setting, "invalid updates must be atomic")
	}
}

func TestUpdateUserSettingUpstreamNotificationIsAdminOnly(t *testing.T) {
	t.Run("ordinary user cannot forge opt-in", func(t *testing.T) {
		_, do, userID, _ := setupSelfTest(t)
		decodeOK(t, do(http.MethodPut, "/api/user/setting",
			`{"quota_warning_threshold":5000,"notify_type":"email","upstream_model_update_notify_enabled":true}`))
		var user model.User
		require.NoError(t, model.DB.First(&user, userID).Error)
		assert.NotContains(t, user.Setting, "upstream_model_update_notify_enabled")
	})

	t.Run("administrator can opt in", func(t *testing.T) {
		_, do, userID := setupChannelRead(t, constant.RoleRootUser)
		body := decodeBody(t, do(http.MethodPut, "/api/user/setting",
			`{"quota_warning_threshold":5000,"notify_type":"email","upstream_model_update_notify_enabled":true}`))
		assert.Equal(t, true, body["success"])
		var user model.User
		require.NoError(t, model.DB.First(&user, userID).Error)
		assert.Contains(t, user.Setting, `"upstream_model_update_notify_enabled":true`)
	})
}

func TestRevokeOtherSessions(t *testing.T) {
	_, do, userId, sid := setupSelfTest(t)
	// A second session exists.
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	otherSID, _, _, err := service.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	decodeOK(t, do(http.MethodPost, "/api/user/sessions/revoke-others", ""))
	var sessions []model.UserSession
	require.NoError(t, model.DB.Where("user_id = ?", userId).Find(&sessions).Error)
	require.Len(t, sessions, 2, "revoked session tombstones must be retained")
	bySID := map[string]model.UserSession{sessions[0].SID: sessions[0], sessions[1].SID: sessions[1]}
	assert.Equal(t, service.SessionStatusActive, bySID[sid].Status, "current session must survive")
	assert.Equal(t, service.SessionStatusRevoked, bySID[otherSID].Status)
	assert.Equal(t, "revoke_others", bySID[otherSID].RevokedReason)
	assert.NotZero(t, bySID[otherSID].RevokedAt)
}

func TestTelegramBindFlow(t *testing.T) {
	_, do, userId, sid := setupSelfTest(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", telegramControllerBotToken)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.TelegramOAuthEnabledOption: "true",
		"TelegramBotName":                  "sample_bot",
	}))

	// Start the ceremony.
	body := decodeOK(t, do(http.MethodPost, "/api/oauth/telegram/bind/start", ""))
	token := body["data"].(map[string]any)["flow_token"].(string)
	require.NotEmpty(t, token)
	var telegramFlow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", common.SHA256Hex(token)).First(&telegramFlow).Error)
	assert.Equal(t, sid, telegramFlow.SessionId)

	// A forged attempt cannot mutate or consume the session-bound ceremony.
	authDate := strconv.FormatInt(time.Now().Unix(), 10)
	rec := do(http.MethodGet, fmt.Sprintf("/api/oauth/telegram/bind/%s?id=42&first_name=A&auth_date=%s&hash=deadbeef", token, authDate), "")
	assert.Contains(t, rec.Header().Get("Location"), "telegram_bound=error")

	// A valid hash binds the identity (computed with the same algorithm as the service).
	hash := telegramTestHash(t, "42", "Alice", authDate)
	rec2 := do(http.MethodGet, fmt.Sprintf("/api/oauth/telegram/bind/%s?id=42&first_name=Alice&auth_date=%s&hash=%s", token, authDate, hash), "")
	assert.Contains(t, rec2.Header().Get("Location"), "telegram_bound=1")
	var got model.User
	require.NoError(t, model.DB.First(&got, userId).Error)
	assert.Equal(t, "42", got.TelegramId)
	claim, err := model.FindExternalIdentityClaimWithTx(model.DB, "telegram", "42")
	require.NoError(t, err)
	assert.Equal(t, userId, claim.UserId)
}

func telegramTestHash(t *testing.T, id, firstName, authDate string) string {
	t.Helper()
	// Mirrors Telegram's data-check-string HMAC-SHA256 over sorted k=v lines.
	data := fmt.Sprintf("auth_date=%s\nfirst_name=%s\nid=%s", authDate, firstName, id)
	secret := sha256.Sum256([]byte(telegramControllerBotToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(data))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func TestTelegramBindStartFailsClosedWhenDisabled(t *testing.T) {
	_, do, _, _ := setupSelfTest(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", telegramControllerBotToken)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.TelegramOAuthEnabledOption: "false",
		"TelegramBotName":                  "sample_bot",
	}))

	rec := do(http.MethodPost, "/api/oauth/telegram/bind/start", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var flows int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&flows).Error)
	assert.Zero(t, flows)
}
