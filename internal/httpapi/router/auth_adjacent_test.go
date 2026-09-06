package router_test

import (
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- POST /api/oauth/state ---

func TestGenerateOAuthStateLogin(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	rec := do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"login","aff":"mycode"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	assert.NotEmpty(t, data["flow_token"])
	assert.NotZero(t, data["expires_at"])

	// The flow carries provider, intent and the affiliate payload.
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", auth.AuthFlowPurposeOAuth).Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, "github", flows[0].Provider)
	assert.Equal(t, "login", flows[0].Intent)
	assert.Equal(t, `{"affiliate_code":"mycode"}`, flows[0].Payload)
}

func TestGenerateOAuthStateBindsSafeReturnTarget(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	rec := do(http.MethodPost, "/api/oauth/state",
		`{"provider":"github","intent":"login","aff":"partner","redirect":"/wallet?section=topup"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", auth.AuthFlowPurposeOAuth).First(&flow).Error)
	assert.JSONEq(t, `{"affiliate_code":"partner","return_to":"/wallet?section=topup"}`, flow.Payload)

	var before int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&before).Error)
	for _, redirect := range []string{
		"https://attacker.example/",
		"//attacker.example/",
		"/oauth/github",
		"/dashboard/../wallet",
		"/wallet\\next",
	} {
		rec = do(http.MethodPost, "/api/oauth/state",
			`{"provider":"github","intent":"login","redirect":`+strconv.Quote(redirect)+`}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, redirect)
	}
	var after int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&after).Error)
	assert.Equal(t, before, after)
}

func TestTelegramLoginRequiresOneTimeStateAndUsesBoundReturn(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	t.Setenv("TELEGRAM_BOT_TOKEN", telegramControllerBotToken)
	t.Setenv("TELEGRAM_BOT_NAME", "TokenRouter_bot")
	require.NoError(t, setting.UpdateOption(setting.TelegramOAuthEnabledOption, "true"))

	stateResponse := do(http.MethodPost, "/api/oauth/state",
		`{"provider":"telegram","intent":"login","redirect":"/wallet?section=topup"}`)
	require.Equal(t, http.StatusOK, stateResponse.Code, stateResponse.Body.String())
	stateData := decodeBody(t, stateResponse)["data"].(map[string]any)
	flowToken := stateData["flow_token"].(string)
	authDate := strconv.FormatInt(time.Now().Unix(), 10)
	hash := telegramTestHash(t, "42", "Alice", authDate)
	callback := "/api/oauth/telegram/login?" + url.Values{
		"flow_token": {flowToken},
		"id":         {"42"},
		"first_name": {"Alice"},
		"auth_date":  {authDate},
		"hash":       {hash},
	}.Encode()

	completed := do(http.MethodGet, callback, "")
	require.Equal(t, http.StatusFound, completed.Code, completed.Body.String())
	assert.Equal(t, "/wallet?section=topup", completed.Header().Get("Location"))
	assert.NotEmpty(t, completed.Result().Cookies())

	replayed := do(http.MethodGet, callback, "")
	require.Equal(t, http.StatusFound, replayed.Code)
	assert.Equal(t, "/login?error=oauth_state", replayed.Header().Get("Location"))
}

func TestOAuthEntryOptionalAuthenticationRejectsOnlyPresentedInvalidCredentials(t *testing.T) {
	handler, _, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	request := httptest.NewRequest(http.MethodPost, "/api/oauth/state",
		strings.NewReader(`{"provider":"github","intent":"login"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	request = httptest.NewRequest(http.MethodPost, "/api/oauth/state",
		strings.NewReader(`{"provider":"github","intent":"login"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer invalid-presented-credential")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	var flowCount int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&flowCount).Error)
	assert.EqualValues(t, 1, flowCount, "an invalid credential must not create an anonymous OAuth flow")
}

func TestOAuthAndRefreshRoutesDisableCaching(t *testing.T) {
	handler, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	telegram := httptest.NewRecorder()
	handler.ServeHTTP(telegram, httptest.NewRequest(http.MethodGet, "/api/oauth/telegram/login", nil))
	assert.Equal(t, http.StatusFound, telegram.Code)
	assert.Contains(t, telegram.Header().Get("Cache-Control"), "no-store")

	callback := httptest.NewRecorder()
	handler.ServeHTTP(callback, httptest.NewRequest(http.MethodGet,
		"/api/oauth/github/callback?state=invalid&code=invalid", nil))
	assert.Equal(t, http.StatusFound, callback.Code)
	assert.Contains(t, callback.Header().Get("Cache-Control"), "no-store")

	refresh := do(http.MethodPost, "/api/user/auth/refresh", "")
	require.Equal(t, http.StatusOK, refresh.Code, refresh.Body.String())
	assert.Contains(t, refresh.Header().Get("Cache-Control"), "no-store")
}

func TestGenerateOAuthStateValidation(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	// Unknown provider.
	rec := do(http.MethodPost, "/api/oauth/state", `{"provider":"myspace","intent":"login"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Unknown intent.
	rec = do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"impersonate"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Aff too long.
	rec = do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"login","aff":"`+strings.Repeat("x", 33)+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Bind + aff is forbidden.
	rec = do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"bind","aff":"x"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGenerateOAuthStateBindRequiresSession(t *testing.T) {
	// Without a session, a bind-intent state is rejected.
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.Join(t.TempDir(), "authadj2.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}, &model.AuthFlow{}))
	model.DB = db
	model.LOG_DB = db
	r := router.SetUpRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"github","intent":"bind"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// With a session the flow is bound to the user + session.
	_, do, userId, sid := setupAuthAdjacent(t, roles.RoleCommonUser)
	rec = do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"bind"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", auth.AuthFlowPurposeOAuth).Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, userId, flows[0].UserId)
	assert.Equal(t, sid, flows[0].SessionId)
}

// --- POST /api/oauth/email/bind ---

func createEmailFlow(t *testing.T, email, code string) {
	t.Helper()
	_, err := auth.CreateAuthFlow(auth.EmailVerificationPurpose, "email", email, 0, "", code, 15*60_000_000_000)
	require.NoError(t, err)
}

func TestEmailBind(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	createEmailFlow(t, "bound@example.com", "123456")

	// The code is keyed to the email: wrong email fails even with the right code.
	rec := do(http.MethodPost, "/api/oauth/email/bind", `{"email":"other@example.com","code":"123456"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Case-insensitive email matches and binds.
	rec = do(http.MethodPost, "/api/oauth/email/bind", `{"email":"  BOUND@example.com ","code":"123456"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, "bound@example.com", user.Email)
	assert.True(t, user.EmailVerified)

	// The code is single-use.
	rec = do(http.MethodPost, "/api/oauth/email/bind", `{"email":"bound@example.com","code":"123456"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Wrong code.
	createEmailFlow(t, "fresh@example.com", "654321")
	rec = do(http.MethodPost, "/api/oauth/email/bind", `{"email":"fresh@example.com","code":"000000"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmailBindTaken(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "owner", Password: "x", Role: roles.RoleCommonUser, Status: model.UserStatusEnabled,
		Email: "taken@example.com", EmailVerified: true, AuthVersion: 1,
	}).Error)
	createEmailFlow(t, "taken@example.com", "123456")

	rec := do(http.MethodPost, "/api/oauth/email/bind", `{"email":"taken@example.com","code":"123456"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "邮箱已被其他用户使用")

	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.Equal(t, "", user.Email)
}

func TestEmailBindRequiresBrowserSession(t *testing.T) {
	handler, _, userId, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	createEmailFlow(t, "pat-bind@example.com", "123456")
	pat, err := auth.GenerateUserAccessToken(userId)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/oauth/email/bind",
		strings.NewReader(`{"email":"pat-bind@example.com","code":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pat)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "AUTH_SESSION_REQUIRED", decodeBody(t, rec)["code"])

	var user model.User
	require.NoError(t, model.DB.First(&user, userId).Error)
	assert.False(t, user.EmailVerified)
}

// --- Admin reset passkey / 2FA ---

func TestAdminResetPasskey(t *testing.T) {
	_, do, adminId, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "victim", Password: "x", Role: roles.RoleCommonUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var victim model.User
	require.NoError(t, model.DB.Where("username = ?", "victim").First(&victim).Error)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: victim.Id, CredentialID: "cred-1", PublicKey: "pk", Attachment: "platform",
	}).Error)

	// A root cannot use an admin route to target itself or another root.
	rec := do(http.MethodDelete, "/api/user/"+textutil.Int2Str(adminId)+"/reset_passkey", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Reset the victim's passkeys and revoke all their sessions.
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "victim-sid", UserID: victim.Id, Version: 1, UserAuthVersion: 1, Status: "active", RefreshHash: "h",
		ExpiresAt: wallclock.NowTimestamp() + 3600,
	}).Error)
	rec = do(http.MethodDelete, "/api/user/"+textutil.Int2Str(victim.Id)+"/reset_passkey", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Passkey 已重置", decodeBody(t, rec)["message"])
	var count int64
	model.DB.Model(&model.PasskeyCredential{}).Where("user_id = ?", victim.Id).Count(&count)
	assert.Zero(t, count)
	var session model.UserSession
	err := model.DB.Where("sid = ?", "victim-sid").First(&session).Error
	require.NoError(t, err)
	assert.NotZero(t, session.RevokedAt)
}

func TestAdminResetPasskeyRoleGuard(t *testing.T) {
	// A plain admin cannot reset another admin's passkeys.
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleAdminUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "peer", Password: "x", Role: roles.RoleAdminUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var peer model.User
	require.NoError(t, model.DB.Where("username = ?", "peer").First(&peer).Error)

	rec := do(http.MethodDelete, "/api/user/"+textutil.Int2Str(peer.Id)+"/reset_passkey", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "no permission")
}

func TestAdmin2FAStats(t *testing.T) {
	_, do, adminId, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: adminId, Secret: "S", IsEnabled: true}).Error)

	rec := do(http.MethodGet, "/api/user/2fa/stats", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total_users"])
	assert.Equal(t, float64(1), data["enabled_users"])
	assert.Equal(t, "100.0%", data["enabled_rate"])
}

func TestAdminDisable2FA(t *testing.T) {
	_, do, adminId, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "victim2", Password: "x", Role: roles.RoleCommonUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var victim model.User
	require.NoError(t, model.DB.Where("username = ?", "victim2").First(&victim).Error)
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: victim.Id, Secret: "S", IsEnabled: true}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "v2-sid", UserID: victim.Id, Version: 1, UserAuthVersion: 1, Status: "active", RefreshHash: "h",
		ExpiresAt: wallclock.NowTimestamp() + 3600,
	}).Error)

	// Even a root may not target itself: role management is strictly ordered.
	rec := do(http.MethodDelete, "/api/user/"+textutil.Int2Str(adminId)+"/2fa", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Contains(t, rec.Body.String(), "无权操作同级或更高级用户")

	// A privileged dashboard session alone cannot disable the victim's factor.
	rec = do(http.MethodDelete, "/api/user/"+textutil.Int2Str(victim.Id)+"/2fa", "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "SECURITY_PROOF_REQUIRED", decodeBody(t, rec)["code"])
	assert.True(t, auth.TwoFAStatus(victim.Id))
	var active model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "v2-sid").First(&active).Error)
	assert.Zero(t, active.RevokedAt)

	// The actor can obtain a reset-scoped proof using their own factor, then
	// disable the target and revoke the target's sessions.
	adminSecret := "JBSWY3DPEHPK3PXP"
	require.NoError(t, model.DB.Create(&model.TwoFA{
		UserId: adminId, Secret: adminSecret, IsEnabled: true,
	}).Error)
	verify := do(http.MethodPost, "/api/verify",
		`{"method":"2fa","code":"`+validTOTP(adminSecret)+`","scope":"twofa.reset"}`)
	require.Equal(t, http.StatusOK, verify.Code, "body: %s", verify.Body.String())
	proof := decodeBody(t, verify)["data"].(map[string]any)["proof_token"].(string)
	rec = do(http.MethodDelete, "/api/user/"+textutil.Int2Str(victim.Id)+"/2fa", "",
		map[string]string{"X-Security-Proof": proof})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "用户2FA已被强制禁用", decodeBody(t, rec)["message"])
	assert.False(t, auth.TwoFAStatus(victim.Id))
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "v2-sid").First(&session).Error)
	assert.NotZero(t, session.RevokedAt)

	// Same-level guard: a plain admin cannot disable a fellow admin's 2FA.
	_, do2, _, _ := setupAuthAdjacent(t, roles.RoleAdminUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "peer2", Password: "x", Role: roles.RoleAdminUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var peer model.User
	require.NoError(t, model.DB.Where("username = ?", "peer2").First(&peer).Error)
	rec = do2(http.MethodDelete, "/api/user/"+textutil.Int2Str(peer.Id)+"/2fa", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Contains(t, rec.Body.String(), "无权操作同级或更高级用户的2FA设置")
}
