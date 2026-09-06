package router_test

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// setupWeChatTest prepares a fresh SQLite database, enables WeChat login, and
// points the WeChat server address at a mock that maps codes to openids.
func setupWeChatTest(t *testing.T, wechatData map[string]string, enable bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	dsn := "file:" + filepath.Join(t.TempDir(), "wechat.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Option{}, &model.UserSession{}, &model.Log{}, &model.ExternalIdentityClaim{}))
	model.DB = db
	model.LOG_DB = db

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		data, ok := wechatData[code]
		if !ok {
			fmt.Fprint(w, `{"success":false,"message":"bad code"}`)
			return
		}
		fmt.Fprintf(w, `{"success":true,"message":"","data":%q}`, data)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)
	t.Setenv("WECHAT_SERVER_TOKEN", "test-token")
	// The critical-path limiter counts per IP in a shared store; raise the
	// per-test limit so independent tests do not bleed into each other.
	t.Setenv("CRITICAL_RATE_LIMIT", "1000")
	require.NoError(t, setting.UpdateOption(setting.WeChatAuthEnabledOption, fmt.Sprintf("%t", enable)))
	require.NoError(t, setting.UpdateOption(setting.RegistrationEnabledOption, "true"))
}

func newWeChatRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "198.51.100.84:1234"
	return req
}

func decodeWeChatBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestWeChatAuthRegistersAndLogsIn(t *testing.T) {
	setupWeChatTest(t, map[string]string{"code-1": "openid-1"}, true)
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=code-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	require.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	data := body["data"].(map[string]any)
	accessToken, ok := data["access_token"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, accessToken)
	assert.NotEmpty(t, data["session"])
	claims, err := cryptoutil.ParseJWTSigned(accessToken, cryptoutil.SessionSecret())
	require.NoError(t, err)
	require.NotNil(t, claims.ExpiresAt)
	assert.EqualValues(t, claims.ExpiresAt.Time.Unix(), data["access_expires_at"],
		"response metadata must expose the exact database-clock expiry signed into the JWT")
	userData := data["user"].(map[string]any)
	assert.Contains(t, userData["username"], "wechat_")

	// The identity is persisted and cookies are set.
	var got model.User
	require.NoError(t, model.DB.Where("wechat_id = ?", "openid-1").First(&got).Error)
	assert.Equal(t, model.UserStatusEnabled, got.Status)
	cookies := rec.Result().Cookies()
	assert.NotEmpty(t, cookies, "login must set auth cookies")

	// A second login with the same code signs into the same account.
	var before int64
	model.DB.Model(&model.User{}).Count(&before)
	req2 := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=code-1", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	body2 := decodeWeChatBody(t, rec2)
	require.Equal(t, true, body2["success"])
	var after int64
	model.DB.Model(&model.User{}).Count(&after)
	assert.Equal(t, before, after, "re-login must not create a duplicate user")
}

func TestWeChatAuthDisabled(t *testing.T) {
	setupWeChatTest(t, map[string]string{"code-1": "openid-1"}, false)
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=code-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "未开启")
}

func TestWeChatAuthRedactsProviderFailureDetails(t *testing.T) {
	setupWeChatTest(t, map[string]string{}, true)
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=secret-provider-code", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "微信登录失败", body["message"])
	assert.NotContains(t, rec.Body.String(), "bad code")
	assert.NotContains(t, rec.Body.String(), "secret-provider-code")
}

func TestWeChatAuthRejectsDuplicateCodeParameters(t *testing.T) {
	setupWeChatTest(t, map[string]string{"code-1": "openid-1"}, true)
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=code-1&code=code-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "微信登录失败", body["message"])
	var users int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&users).Error)
	assert.Zero(t, users)
}

func TestWeChatAuthRegisterDisabled(t *testing.T) {
	setupWeChatTest(t, map[string]string{"code-2": "openid-2"}, true)
	require.NoError(t, setting.UpdateOption(setting.RegistrationEnabledOption, "false"))
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=code-2", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "注册")
}

func TestWeChatAuthBannedUser(t *testing.T) {
	setupWeChatTest(t, map[string]string{"code-3": "openid-3"}, true)
	user := model.User{Username: "wxban", Password: "x", Role: 1, Status: model.UserStatusDisabled, WeChatId: "openid-3", AuthVersion: 1}
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		return model.ClaimUserExternalIdentitiesWithTx(tx, &user)
	}))
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodGet, "/api/oauth/wechat?code=code-3", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "封禁")
}

func TestWeChatBind(t *testing.T) {
	setupWeChatTest(t, map[string]string{"bind-1": "openid-bind"}, true)
	user := model.User{Username: "binder", Password: "x", Role: 1, Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	_, access, _, err := authsvc.CompleteLogin(&user, "127.0.0.1", "ua", "test")
	require.NoError(t, err)
	r := router.SetUpRouter()

	req := newWeChatRequest(http.MethodPost, "/api/oauth/wechat/bind", strings.NewReader(`{"code":"bind-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWeChatBody(t, rec)
	require.Equal(t, true, body["success"], "body: %s", rec.Body.String())

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, "openid-bind", got.WeChatId)

	// The same openid cannot be bound again.
	user2 := model.User{Username: "binder2", Password: "x", Role: 1, Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user2).Error)
	_, access2, _, err := authsvc.CompleteLogin(&user2, "127.0.0.1", "ua", "test")
	require.NoError(t, err)
	req2 := newWeChatRequest(http.MethodPost, "/api/oauth/wechat/bind", strings.NewReader(`{"code":"bind-1"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(&http.Cookie{Name: "access_token", Value: access2})
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	body2 := decodeWeChatBody(t, rec2)
	assert.Equal(t, false, body2["success"])
	assert.Contains(t, body2["message"], "已被绑定")

	// Unauthenticated bind is rejected.
	req3 := newWeChatRequest(http.MethodPost, "/api/oauth/wechat/bind", strings.NewReader(`{"code":"bind-1"}`))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	require.Equal(t, http.StatusUnauthorized, rec3.Code)

	// A dashboard PAT is not a browser session and cannot add a new login
	// identity to the account.
	pat, err := authsvc.GenerateUserAccessToken(user.Id)
	require.NoError(t, err)
	req4 := newWeChatRequest(http.MethodPost, "/api/oauth/wechat/bind", strings.NewReader(`{"code":"bind-1"}`))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Authorization", "Bearer "+pat)
	rec4 := httptest.NewRecorder()
	r.ServeHTTP(rec4, req4)
	require.Equal(t, http.StatusForbidden, rec4.Code)
}
