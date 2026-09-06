package controller_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func requestWithDashboardPAT(
	t *testing.T,
	handler http.Handler,
	pat, method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertSessionRequired(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	assert.Equal(t, "AUTH_SESSION_REQUIRED", decodeBody(t, recorder)["code"])
}

func TestManagementPATCannotRotateItself(t *testing.T) {
	handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)

	recorder := requestWithDashboardPAT(t, handler, pat, http.MethodGet, "/api/user/token", "")
	assertSessionRequired(t, recorder)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	require.NotNil(t, user.AccessToken)
	assert.Equal(t, pat, *user.AccessToken, "a rejected PAT request must not rotate its credential")
}

func TestManagementPATCannotDeleteAccount(t *testing.T) {
	handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)

	recorder := requestWithDashboardPAT(t, handler, pat, http.MethodDelete, "/api/user/self", "")
	assertSessionRequired(t, recorder)

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, model.UserStatusEnabled, user.Status)
}

func TestManagementPATCannotEnrollPasskey(t *testing.T) {
	handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)

	begin := requestWithDashboardPAT(t, handler, pat, http.MethodPost,
		"/api/user/passkey/register/begin", "")
	assertSessionRequired(t, begin)

	finish := requestWithDashboardPAT(t, handler, pat, http.MethodPost,
		"/api/user/passkey/register/finish", `{}`)
	assertSessionRequired(t, finish)

	var count int64
	require.NoError(t, model.DB.Model(&model.PasskeyCredential{}).
		Where("user_id = ?", userID).Count(&count).Error)
	assert.Zero(t, count)
}

func TestExistingIdentityAndSessionMutationsRejectManagementPAT(t *testing.T) {
	handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "password", method: http.MethodPut, path: "/api/user/self", body: `{"old_password":"pw","password":"new-password"}`},
		{name: "email", method: http.MethodPost, path: "/api/user/email/bind", body: `{"email":"new@example.com","code":"123456"}`},
		{name: "oauth email", method: http.MethodPost, path: "/api/oauth/email/bind", body: `{"email":"new@example.com","code":"123456"}`},
		{name: "wechat", method: http.MethodPost, path: "/api/oauth/wechat/bind", body: `{"code":"provider-code"}`},
		{name: "telegram", method: http.MethodPost, path: "/api/oauth/telegram/bind/start"},
		{name: "oauth unbind", method: http.MethodDelete, path: "/api/user/oauth/bindings/1"},
		{name: "sessions", method: http.MethodGet, path: "/api/user/sessions"},
		{name: "session revoke", method: http.MethodDelete, path: "/api/user/sessions/not-owned"},
		{name: "revoke others", method: http.MethodPost, path: "/api/user/sessions/revoke-others"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := requestWithDashboardPAT(t, handler, pat, test.method, test.path, test.body)
			assertSessionRequired(t, recorder)
		})
	}

	// OAuth bind-state creation is outside the UserAuth group, but it resolves
	// the same live-session identity directly and must not treat a PAT as one.
	state := requestWithDashboardPAT(t, handler, pat, http.MethodPost, "/api/oauth/state",
		`{"provider":"github","intent":"bind"}`)
	assert.Equal(t, http.StatusUnauthorized, state.Code, state.Body.String())
}

func TestRootCannotDeleteOwnAccount(t *testing.T) {
	_, do, userID, sid := setupAuthAdjacent(t, constant.RoleRootUser)

	recorder := do(http.MethodDelete, "/api/user/self", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Contains(t, body["message"], "不能删除超级管理员账户")

	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, constant.RoleRootUser, user.Role)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, service.SessionStatusActive, session.Status)
}

func TestBrowserSessionAccountDeletionRevokesCredentials(t *testing.T) {
	_, do, userID, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
	token := model.Token{
		UserId: userID, Key: "sk-delete-self", Name: "delete-self",
		Status: service.TokenStatusEnabled, RemainQuota: 10,
	}
	require.NoError(t, model.DB.Create(&token).Error)

	recorder := do(http.MethodDelete, "/api/user/self", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, true, decodeBody(t, recorder)["success"])

	var user model.User
	require.NoError(t, model.DB.Unscoped().First(&user, userID).Error)
	assert.True(t, user.DeletedAt.Valid)
	var storedToken model.Token
	require.NoError(t, model.DB.Unscoped().First(&storedToken, token.Id).Error)
	assert.True(t, storedToken.DeletedAt.Valid)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, service.SessionStatusRevoked, session.Status)
	assert.Equal(t, "account_deleted", session.RevokedReason)
}

func TestManagementPATRetainsReferenceTokenCRUDContract(t *testing.T) {
	handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)

	created := requestWithDashboardPAT(t, handler, pat, http.MethodPost, "/api/token/",
		`{"name":"pat-managed","unlimited_quota":true}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	assert.Equal(t, true, decodeBody(t, created)["success"])

	listed := requestWithDashboardPAT(t, handler, pat, http.MethodGet, "/api/token/", "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Contains(t, listed.Body.String(), "pat-managed")
}
