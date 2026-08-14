package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

const customOAuthPayload = `{
	"name":"Corp SSO",
	"slug":"corp-sso",
	"enabled":true,
	"client_id":"client-123",
	"client_secret":"secret-abc",
	"authorization_endpoint":"https://sso.example.com/authorize",
	"token_endpoint":"https://sso.example.com/token",
	"user_info_endpoint":"https://sso.example.com/userinfo",
	"scopes":"openid profile"
}`

func setupCustomOAuthTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	t.Helper()
	handler, do, uid := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}))
	return handler, do, uid
}

func TestCustomOAuthProviderCrudContract(t *testing.T) {
	_, do, _ := setupCustomOAuthTest(t)

	// Validation: required fields produce the reference binding error.
	rec := do(http.MethodPost, "/api/custom-oauth-provider/", `{"name":"x"}`)
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.True(t, strings.HasPrefix(body["message"].(string), "无效的请求参数: "), "message: %v", body["message"])

	// Built-in slug conflicts are rejected.
	builtIn := strings.Replace(customOAuthPayload, `"corp-sso"`, `"github"`, 1)
	rec = do(http.MethodPost, "/api/custom-oauth-provider/", builtIn)
	assert.Equal(t, "该 Slug 与内置 OAuth 提供商冲突", decodeBody(t, rec)["message"])

	// Create succeeds; the response never carries the client secret.
	rec = do(http.MethodPost, "/api/custom-oauth-provider/", customOAuthPayload)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "创建成功", body["message"])
	assert.NotContains(t, rec.Body.String(), "secret-abc")
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	providerId := int(data["id"].(float64))
	assert.Equal(t, "Corp SSO", data["name"])
	assert.Equal(t, "corp-sso", data["slug"])
	assert.Equal(t, true, data["enabled"])
	assert.Equal(t, "client-123", data["client_id"])

	// The secret is stored despite not being returned.
	stored, err := model.GetCustomOAuthProviderById(providerId)
	require.NoError(t, err)
	assert.Equal(t, "secret-abc", stored.ClientSecret)

	// Duplicate slug rejected.
	rec = do(http.MethodPost, "/api/custom-oauth-provider/", customOAuthPayload)
	assert.Equal(t, "该 Slug 已被使用", decodeBody(t, rec)["message"])

	// Uppercase slugs are normalized to lowercase.
	upper := strings.Replace(customOAuthPayload, `"corp-sso"`, `"My-Slug"`, 1)
	rec = do(http.MethodPost, "/api/custom-oauth-provider/", upper)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "my-slug", body["data"].(map[string]any)["slug"])

	// Get by id; invalid and missing ids.
	rec = do(http.MethodGet, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(providerId), body["data"].(map[string]any)["id"])
	assert.NotContains(t, rec.Body.String(), "secret-abc")
	rec = do(http.MethodGet, "/api/custom-oauth-provider/abc", "")
	assert.Equal(t, "无效的 ID", decodeBody(t, rec)["message"])
	rec = do(http.MethodGet, "/api/custom-oauth-provider/999999", "")
	assert.Equal(t, "未找到该 OAuth 提供商", decodeBody(t, rec)["message"])

	// List returns every provider, secrets excluded.
	rec = do(http.MethodGet, "/api/custom-oauth-provider/", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	list := body["data"].([]any)
	assert.Len(t, list, 2)
	assert.NotContains(t, rec.Body.String(), "secret-abc")

	// Partial update keeps untouched fields (including the secret).
	rec = do(http.MethodPut, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), `{"name":"Renamed SSO"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "更新成功", body["message"])
	assert.Equal(t, "Renamed SSO", body["data"].(map[string]any)["name"])
	stored, err = model.GetCustomOAuthProviderById(providerId)
	require.NoError(t, err)
	assert.Equal(t, "secret-abc", stored.ClientSecret)
	assert.Equal(t, "corp-sso", stored.Slug)

	// Enabled toggle + slug change.
	rec = do(http.MethodPut, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId),
		`{"enabled":false,"slug":"renamed-sso"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, false, body["data"].(map[string]any)["enabled"])
	assert.Equal(t, "renamed-sso", body["data"].(map[string]any)["slug"])

	// Slug conflict on update is rejected.
	rec = do(http.MethodPut, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), `{"slug":"my-slug"}`)
	assert.Equal(t, "该 Slug 已被使用", decodeBody(t, rec)["message"])

	// Invalid access policy JSON is rejected by validation.
	rec = do(http.MethodPut, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), `{"access_policy":"not-json"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "access_policy")
}

func TestCustomOAuthProviderDeleteGuard(t *testing.T) {
	_, do, uid := setupCustomOAuthTest(t)

	rec := do(http.MethodPost, "/api/custom-oauth-provider/", customOAuthPayload)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	providerId := int(decodeBody(t, rec)["data"].(map[string]any)["id"].(float64))

	// With a user binding, deletion is blocked.
	require.NoError(t, model.DB.Create(&model.UserOAuthBinding{
		UserId: uid, ProviderId: providerId, ProviderUserId: "ext-42",
	}).Error)
	rec = do(http.MethodDelete, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), "")
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "该 OAuth 提供商还有用户绑定，无法删除。请先解除所有用户绑定。", body["message"])

	// After unbinding, deletion succeeds.
	require.NoError(t, model.DeleteUserOAuthBinding(uid, providerId))
	rec = do(http.MethodDelete, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "删除成功", body["message"])

	rec = do(http.MethodGet, fmt.Sprintf("/api/custom-oauth-provider/%d", providerId), "")
	assert.Equal(t, "未找到该 OAuth 提供商", decodeBody(t, rec)["message"])
}

func TestCustomOAuthDiscoveryContract(t *testing.T) {
	_, do, _ := setupCustomOAuthTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"https://sso.example.com","authorization_endpoint":"https://sso.example.com/authorize"}`))
	}))
	defer upstream.Close()

	// Well-known URL directly.
	rec := do(http.MethodPost, "/api/custom-oauth-provider/discovery",
		fmt.Sprintf(`{"well_known_url":%q}`, upstream.URL))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, upstream.URL, data["well_known_url"])
	discovery, ok := data["discovery"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://sso.example.com", discovery["issuer"])

	// Issuer URL appends the well-known path.
	rec = do(http.MethodPost, "/api/custom-oauth-provider/discovery",
		fmt.Sprintf(`{"issuer_url":%q}`, upstream.URL))
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, upstream.URL+"/.well-known/openid-configuration",
		decodeBody(t, rec)["data"].(map[string]any)["well_known_url"])

	// Validation failures.
	rec = do(http.MethodPost, "/api/custom-oauth-provider/discovery", `{}`)
	assert.Equal(t, "请先填写 Discovery URL 或 Issuer URL", decodeBody(t, rec)["message"])
	rec = do(http.MethodPost, "/api/custom-oauth-provider/discovery", `{"well_known_url":"ftp://x/y"}`)
	assert.Equal(t, "Discovery URL 无效，仅支持 http/https", decodeBody(t, rec)["message"])

	// Non-200 upstream surfaces the body.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer bad.Close()
	rec = do(http.MethodPost, "/api/custom-oauth-provider/discovery",
		fmt.Sprintf(`{"well_known_url":%q}`, bad.URL))
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "获取 Discovery 配置失败: boom", body["message"])
}

func TestAdminUserOAuthBindingsContract(t *testing.T) {
	handler, doRoot, uid := setupCustomOAuthTest(t)

	// A provider plus one binding for the root user.
	rec := doRoot(http.MethodPost, "/api/custom-oauth-provider/", customOAuthPayload)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	providerId := int(decodeBody(t, rec)["data"].(map[string]any)["id"].(float64))
	require.NoError(t, model.DB.Create(&model.UserOAuthBinding{
		UserId: uid, ProviderId: providerId, ProviderUserId: "ext-42",
	}).Error)

	// An admin-role session (cannot manage the root target).
	adminUser := model.User{Username: "oauthadmin", Password: "pw", Role: constant.RoleAdminUser,
		Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&adminUser).Error)
	sid, access, refresh, err := service.CompleteLogin(&adminUser, "127.0.0.7", "ua", "test")
	require.NoError(t, err)
	doAdmin := doAsUser(t, handler, sid, access, refresh)

	// Root lists the target's bindings with provider metadata.
	rec = doRoot(http.MethodGet, fmt.Sprintf("/api/user/%d/oauth/bindings", uid), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	bindings := body["data"].([]any)
	require.Len(t, bindings, 1)
	b := bindings[0].(map[string]any)
	assert.Equal(t, float64(providerId), b["provider_id"])
	assert.Equal(t, "Corp SSO", b["provider_name"])
	assert.Equal(t, "corp-sso", b["provider_slug"])
	assert.Equal(t, "ext-42", b["provider_user_id"])

	// Admin (non-root) targeting the root user is denied.
	rec = doAdmin(http.MethodGet, fmt.Sprintf("/api/user/%d/oauth/bindings", uid), "")
	assert.Equal(t, "no permission", decodeBody(t, rec)["message"])
	rec = doAdmin(http.MethodDelete, fmt.Sprintf("/api/user/%d/oauth/bindings/%d", uid, providerId), "")
	assert.Equal(t, "no permission", decodeBody(t, rec)["message"])

	// Root unbinds; the reference success message.
	rec = doRoot(http.MethodDelete, fmt.Sprintf("/api/user/%d/oauth/bindings/%d", uid, providerId), "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "success", body["message"])
	var count int64
	model.DB.Model(&model.UserOAuthBinding{}).Where("user_id = ?", uid).Count(&count)
	assert.Zero(t, count)

	// Invalid ids.
	rec = doRoot(http.MethodGet, "/api/user/abc/oauth/bindings", "")
	assert.Equal(t, "invalid user id", decodeBody(t, rec)["message"])
	rec = doRoot(http.MethodDelete, fmt.Sprintf("/api/user/%d/oauth/bindings/abc", uid), "")
	assert.Equal(t, "invalid provider id", decodeBody(t, rec)["message"])
}

// newCustomIdPForController stands up a mock IdP and its provider row.
func newCustomIdPForController(t *testing.T, userinfo, accessPolicy, deniedMessage string) *model.CustomOAuthProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"tok-ctrl","token_type":"bearer"}`))
		case "/user":
			_, _ = w.Write([]byte(userinfo))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	provider := &model.CustomOAuthProvider{
		Name: "Corp SSO", Slug: "corp-sso", Enabled: true,
		ClientId: "cid", ClientSecret: "sec",
		AuthorizationEndpoint: srv.URL + "/authorize",
		TokenEndpoint:         srv.URL + "/token",
		UserInfoEndpoint:      srv.URL + "/user",
		AccessPolicy:          accessPolicy,
		AccessDeniedMessage:   deniedMessage,
	}
	require.NoError(t, model.CreateCustomOAuthProvider(provider))
	return provider
}

// startCustomOAuthLogin drives GET /api/oauth/:slug and returns the state.
func startCustomOAuthLogin(t *testing.T, handler http.Handler, slug string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/"+slug, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

func TestCustomOAuthCallbackLoginFlow(t *testing.T) {
	handler, _, _ := setupCustomOAuthTest(t)
	provider := newCustomIdPForController(t,
		`{"sub":"ext-77","preferred_username":"dana","name":"Dana D","email":"d@example.com"}`, "", "")

	state := startCustomOAuthLogin(t, handler, "corp-sso")
	req := httptest.NewRequest(http.MethodGet,
		"/api/oauth/corp-sso/callback?state="+url.QueryEscape(state)+"&code=any", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/", rec.Header().Get("Location"))

	// The login set session cookies and created user + binding.
	cookies := rec.Result().Cookies()
	var hasAccess bool
	for _, c := range cookies {
		if c.Name == "access_token" && c.Value != "" {
			hasAccess = true
		}
	}
	assert.True(t, hasAccess, "callback must sign the user in")
	user, err := model.GetUserByOAuthBinding(provider.Id, "ext-77")
	require.NoError(t, err)
	assert.Equal(t, "dana", user.Username)

	// A replayed state is rejected.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/oauth/corp-sso/callback?state="+url.QueryEscape(state)+"&code=any", nil))
	assert.Contains(t, rec.Header().Get("Location"), "error=oauth_state")
}

func TestCustomOAuthCallbackPolicyDenied(t *testing.T) {
	handler, _, _ := setupCustomOAuthTest(t)
	newCustomIdPForController(t, `{"sub":"ext-88","plan":"free"}`,
		`{"conditions":[{"field":"plan","op":"eq","value":"pro"}]}`,
		"{{provider}} needs plan {{required}}")

	state := startCustomOAuthLogin(t, handler, "corp-sso")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/oauth/corp-sso/callback?state="+url.QueryEscape(state)+"&code=any", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	loc := rec.Header().Get("Location")
	assert.Contains(t, loc, "/login?error=oauth_access_denied")
	assert.Contains(t, loc, "message="+url.QueryEscape("Corp SSO needs plan pro"))

	// No user was created.
	var count int64
	model.DB.Model(&model.UserOAuthBinding{}).Count(&count)
	assert.Zero(t, count)
}

func TestStatusExposesCustomOAuthProviders(t *testing.T) {
	_, do, _ := setupCustomOAuthTest(t)

	// Without enabled providers the key is absent.
	body := decodeBody(t, do(http.MethodGet, "/api/status", ""))
	data := body["data"].(map[string]any)
	_, present := data["custom_oauth_providers"]
	assert.False(t, present)

	rec := do(http.MethodPost, "/api/custom-oauth-provider/", customOAuthPayload)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A disabled provider stays hidden.
	disabled := strings.Replace(strings.Replace(customOAuthPayload, `"corp-sso"`, `"hidden-sso"`, 1),
		`"enabled":true`, `"enabled":false`, 1)
	rec = do(http.MethodPost, "/api/custom-oauth-provider/", disabled)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body = decodeBody(t, do(http.MethodGet, "/api/status", ""))
	list := body["data"].(map[string]any)["custom_oauth_providers"].([]any)
	require.Len(t, list, 1)
	p := list[0].(map[string]any)
	assert.Equal(t, "corp-sso", p["slug"])
	assert.Equal(t, "Corp SSO", p["name"])
	assert.Equal(t, "client-123", p["client_id"])
	assert.Equal(t, "https://sso.example.com/authorize", p["authorization_endpoint"])
	assert.Equal(t, "openid profile", p["scopes"])
	_, hasSecret := p["client_secret"]
	assert.False(t, hasSecret, "status must never leak the client secret")
}

func TestCustomOAuthRoutesRequireRoot(t *testing.T) {
	handler, _, uid := setupCustomOAuthTest(t)
	plain := model.User{Username: "oauthplain", Password: "pw", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&plain).Error)
	sid, access, refresh, err := service.CompleteLogin(&plain, "127.0.0.6", "ua", "test")
	require.NoError(t, err)
	doPlain := doAsUser(t, handler, sid, access, refresh)
	_ = uid

	rec := doPlain(http.MethodGet, "/api/custom-oauth-provider/", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = doPlain(http.MethodPost, "/api/custom-oauth-provider/", customOAuthPayload)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = doPlain(http.MethodPost, "/api/custom-oauth-provider/discovery", `{}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
