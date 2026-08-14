package controller_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

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
		&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}))
	model.DB = db
	model.LOG_DB = db
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))

	user := model.User{Username: "selfuser", Password: "pw", Role: 1, Status: model.UserStatusEnabled,
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
	body := decodeOK(t, do(http.MethodPost, "/api/user/2fa/backup_codes", ""))
	codes := body["data"].(map[string]any)["backup_codes"].([]any)
	require.Len(t, codes, 8)
	var stored int64
	model.DB.Model(&model.TwoFABackupCode{}).Where("user_id = ?", userId).Count(&stored)
	assert.Equal(t, int64(8), stored)
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

func TestRevokeOtherSessions(t *testing.T) {
	_, do, userId, sid := setupSelfTest(t)
	// A second session exists.
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	_, _, _, err := service.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)

	decodeOK(t, do(http.MethodPost, "/api/user/sessions/revoke-others", ""))
	var sessions []model.UserSession
	require.NoError(t, model.DB.Where("user_id = ?", userId).Find(&sessions).Error)
	require.Len(t, sessions, 1)
	assert.Equal(t, sid, sessions[0].SID, "only the current session must survive")
}

func TestTelegramBindFlow(t *testing.T) {
	_, do, userId, _ := setupSelfTest(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123456:ABC-DEF")

	// Start the ceremony.
	body := decodeOK(t, do(http.MethodPost, "/api/oauth/telegram/bind/start", ""))
	token := body["data"].(map[string]any)["flow_token"].(string)
	require.NotEmpty(t, token)

	// A forged attempt burns the one-time token (anti-replay): the valid
	// attempt therefore starts a fresh ceremony.
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/oauth/telegram/bind/%s?id=42&first_name=A&auth_date=1700000000&hash=deadbeef", token), nil)
	rec := httptest.NewRecorder()
	ginEngineForBind(t).ServeHTTP(rec, req)
	assert.Contains(t, rec.Header().Get("Location"), "telegram_bound=error")

	body2 := decodeOK(t, do(http.MethodPost, "/api/oauth/telegram/bind/start", ""))
	token2 := body2["data"].(map[string]any)["flow_token"].(string)

	// A valid hash binds the identity (computed with the same algorithm as the service).
	hash := telegramTestHash(t, "42", "Alice", "1700000000")
	req2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/oauth/telegram/bind/%s?id=42&first_name=Alice&auth_date=1700000000&hash=%s", token2, hash), nil)
	rec2 := httptest.NewRecorder()
	ginEngineForBind(t).ServeHTTP(rec2, req2)
	assert.Contains(t, rec2.Header().Get("Location"), "telegram_bound=1")
	var got model.User
	require.NoError(t, model.DB.First(&got, userId).Error)
	assert.Equal(t, "42", got.TelegramId)
}

func ginEngineForBind(t *testing.T) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return router.SetUpRouter()
}

func telegramTestHash(t *testing.T, id, firstName, authDate string) string {
	t.Helper()
	// Mirrors Telegram's data-check-string HMAC-SHA256 over sorted k=v lines.
	data := fmt.Sprintf("auth_date=%s\nfirst_name=%s\nid=%s", authDate, firstName, id)
	secret := sha256.Sum256([]byte("123456:ABC-DEF"))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(data))
	return fmt.Sprintf("%x", mac.Sum(nil))
}
