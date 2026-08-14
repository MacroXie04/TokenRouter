package controller_test

import (
	"encoding/json"
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

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupAuthAdjacent(t *testing.T, role int) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	dsn := "file:" + filepath.Join(t.TempDir(), "authadj.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{},
		&model.Channel{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.AuthFlow{}, &model.Log{}, &model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, "500000"))

	user := model.User{Username: "adjuser", Password: "pw", Role: role, Status: model.UserStatusEnabled,
		Quota: 1000, AuthVersion: 1}
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

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), "body: %s", rec.Body.String())
	return m
}

// --- POST /api/oauth/state ---

func TestGenerateOAuthStateLogin(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)

	rec := do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"login","aff":"mycode"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	assert.NotEmpty(t, data["flow_token"])
	assert.NotZero(t, data["expires_at"])

	// The flow carries provider, intent and the affiliate payload.
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", service.AuthFlowPurposeOAuth).Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, "github", flows[0].Provider)
	assert.Equal(t, "login", flows[0].Intent)
	assert.Equal(t, `{"affiliate_code":"mycode"}`, flows[0].Payload)
}

func TestGenerateOAuthStateValidation(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)

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
	_, do, userId, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
	rec = do(http.MethodPost, "/api/oauth/state", `{"provider":"github","intent":"bind"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", service.AuthFlowPurposeOAuth).Find(&flows).Error)
	require.Len(t, flows, 1)
	assert.Equal(t, userId, flows[0].UserId)
	assert.Equal(t, sid, flows[0].SessionId)
}

// --- POST /api/oauth/email/bind ---

func createEmailFlow(t *testing.T, email, code string) {
	t.Helper()
	_, err := service.CreateAuthFlow(service.EmailVerificationPurpose, "email", email, 0, "", code, 15*60_000_000_000)
	require.NoError(t, err)
}

func TestEmailBind(t *testing.T) {
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
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
	_, do, userId, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "owner", Password: "x", Role: constant.RoleCommonUser, Status: model.UserStatusEnabled,
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

// --- Admin reset passkey / 2FA ---

func TestAdminResetPasskey(t *testing.T) {
	_, do, adminId, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "victim", Password: "x", Role: constant.RoleCommonUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var victim model.User
	require.NoError(t, model.DB.Where("username = ?", "victim").First(&victim).Error)
	require.NoError(t, model.DB.Create(&model.PasskeyCredential{
		UserID: victim.Id, CredentialID: "cred-1", PublicKey: "pk", Attachment: "platform",
	}).Error)

	// No passkey → success:false.
	rec := do(http.MethodDelete, "/api/user/"+common.Int2Str(adminId)+"/reset_passkey", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	// Reset the victim's passkeys and revoke all their sessions.
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "victim-sid", UserID: victim.Id, Version: 1, UserAuthVersion: 1, Status: "active", RefreshHash: "h",
	}).Error)
	rec = do(http.MethodDelete, "/api/user/"+common.Int2Str(victim.Id)+"/reset_passkey", "")
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
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleAdminUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "peer", Password: "x", Role: constant.RoleAdminUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var peer model.User
	require.NoError(t, model.DB.Where("username = ?", "peer").First(&peer).Error)

	rec := do(http.MethodDelete, "/api/user/"+common.Int2Str(peer.Id)+"/reset_passkey", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "no permission")
}

func TestAdmin2FAStats(t *testing.T) {
	_, do, adminId, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: adminId, Secret: "S", IsEnabled: true}).Error)

	rec := do(http.MethodGet, "/api/user/2fa/stats", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total_users"])
	assert.Equal(t, float64(1), data["enabled_users"])
	assert.Equal(t, "100.0%", data["enabled_rate"])
}

func TestAdminDisable2FA(t *testing.T) {
	_, do, adminId, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "victim2", Password: "x", Role: constant.RoleCommonUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var victim model.User
	require.NoError(t, model.DB.Where("username = ?", "victim2").First(&victim).Error)
	require.NoError(t, model.DB.Create(&model.TwoFA{UserId: victim.Id, Secret: "S", IsEnabled: true}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "v2-sid", UserID: victim.Id, Version: 1, UserAuthVersion: 1, Status: "active", RefreshHash: "h",
	}).Error)

	// Not enabled → success:false.
	rec := do(http.MethodDelete, "/api/user/"+common.Int2Str(adminId)+"/2fa", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	// Disable the victim's 2FA and revoke sessions.
	rec = do(http.MethodDelete, "/api/user/"+common.Int2Str(victim.Id)+"/2fa", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "用户2FA已被强制禁用", decodeBody(t, rec)["message"])
	assert.False(t, service.TwoFAStatus(victim.Id))
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "v2-sid").First(&session).Error)
	assert.NotZero(t, session.RevokedAt)

	// Same-level guard: a plain admin cannot disable a fellow admin's 2FA.
	_, do2, _, _ := setupAuthAdjacent(t, constant.RoleAdminUser)
	require.NoError(t, model.DB.Create(&model.User{
		Username: "peer2", Password: "x", Role: constant.RoleAdminUser, Status: model.UserStatusEnabled,
		Group: "default", Quota: 100, AuthVersion: 1,
	}).Error)
	var peer model.User
	require.NoError(t, model.DB.Where("username = ?", "peer2").First(&peer).Error)
	rec = do2(http.MethodDelete, "/api/user/"+common.Int2Str(peer.Id)+"/2fa", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Contains(t, rec.Body.String(), "无权操作同级或更高级用户的2FA设置")
}
