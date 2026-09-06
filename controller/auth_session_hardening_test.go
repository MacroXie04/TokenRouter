package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func performRefreshRequest(handler http.Handler, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/user/auth/refresh", nil)
	request.RemoteAddr = "198.51.100.44:4321"
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertStableAuthFailure(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, code, body["code"])
	assert.Equal(t, http.StatusText(status), body["message"])
	assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")
}

func assertAuthCookiesCleared(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	headers := recorder.Header().Values("Set-Cookie")
	require.Len(t, headers, 2)
	for _, header := range headers {
		assert.Contains(t, header, "Max-Age=0")
		assert.Contains(t, header, "HttpOnly")
		assert.Contains(t, header, "SameSite=Lax")
	}
}

func enableControllerTwoFA(t *testing.T, userID int) (*model.User, string, string) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	secret, _, err := service.GenerateTwoFASecret(user.Id, user.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	_, err = service.EnableTwoFA(user.Id, code)
	require.NoError(t, err)
	flowToken, err := service.BeginTwoFALogin(&user)
	require.NoError(t, err)
	return &user, flowToken, secret
}

func TestLogin2FAUsesStableExpectedAndInternalErrorEnvelopes(t *testing.T) {
	t.Run("invalid auth flow", func(t *testing.T) {
		handler, _, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
		recorder := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
			fmt.Sprintf(`{"flow_token":%q,"code":"invalid"}`, strings.Repeat("A", 64)))
		assertStableAuthFailure(t, recorder, http.StatusUnauthorized, "AUTH_2FA_INVALID")
		assert.NotContains(t, recorder.Body.String(), service.ErrInvalidFlowToken.Error())
	})

	t.Run("invalid factor", func(t *testing.T) {
		handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
		_, flowToken, _ := enableControllerTwoFA(t, userID)
		recorder := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
			fmt.Sprintf(`{"flow_token":%q,"code":"not-a-factor"}`, flowToken))
		assertStableAuthFailure(t, recorder, http.StatusUnauthorized, "AUTH_2FA_INVALID")
		assert.NotContains(t, recorder.Body.String(), service.ErrTwoFAInvalidCode.Error())
	})

	t.Run("unexpected storage failure", func(t *testing.T) {
		handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
		_, flowToken, _ := enableControllerTwoFA(t, userID)
		require.NoError(t, model.DB.Migrator().DropTable(&model.TwoFA{}))
		recorder := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
			fmt.Sprintf(`{"flow_token":%q,"code":"123456"}`, flowToken))
		assertStableAuthFailure(t, recorder, http.StatusInternalServerError, "AUTH_INTERNAL_ERROR")
		assert.NotContains(t, recorder.Body.String(), "two_fas")
		assert.NotContains(t, recorder.Body.String(), "no such table")
	})
}

func TestLogin2FAPreservesSessionPolicyCodes(t *testing.T) {
	handler, _, userID, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	_, flowToken, secret := enableControllerTwoFA(t, userID)
	t.Setenv("USER_SESSION_ACTIVE_LIMIT", "1")
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	recorder := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
		fmt.Sprintf(`{"flow_token":%q,"code":%q}`, flowToken, code))
	assertStableAuthFailure(t, recorder, http.StatusConflict, "AUTH_SESSION_LIMIT")
}

func TestRefreshAuthMissingMalformedAndInvalidCredentialsAreCodedAndCleared(t *testing.T) {
	handler, _, _, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
	tests := []struct {
		name    string
		cookies []*http.Cookie
		code    string
	}{
		{name: "missing", code: "AUTH_UNAUTHORIZED"},
		{name: "no separator", cookies: []*http.Cookie{{Name: "refresh_token", Value: "invalid"}}, code: "AUTH_UNAUTHORIZED"},
		{name: "empty sid", cookies: []*http.Cookie{{Name: "refresh_token", Value: ".token"}}, code: "AUTH_UNAUTHORIZED"},
		{name: "extra separator", cookies: []*http.Cookie{{Name: "refresh_token", Value: "sid.token.extra"}}, code: "AUTH_UNAUTHORIZED"},
		{name: "oversized", cookies: []*http.Cookie{{Name: "refresh_token", Value: "sid." + strings.Repeat("x", 318)}}, code: "AUTH_UNAUTHORIZED"},
		{name: "duplicate cookies", cookies: []*http.Cookie{
			{Name: "refresh_token", Value: "sid.token"},
			{Name: "refresh_token", Value: "sid.other"},
		}, code: "AUTH_UNAUTHORIZED"},
		{name: "invalid credential", cookies: []*http.Cookie{{
			Name: "refresh_token", Value: sid + "." + strings.Repeat("Z", 64),
		}}, code: "AUTH_UNAUTHORIZED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := performRefreshRequest(handler, test.cookies...)
			assertStableAuthFailure(t, recorder, http.StatusUnauthorized, test.code)
			assertAuthCookiesCleared(t, recorder)
		})
	}
}

func TestRefreshAuthRaceAndReplayUseStableCodes(t *testing.T) {
	t.Run("revoked session clears credentials", func(t *testing.T) {
		_, do, userID, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
		revoked, err := service.RevokeUserSession(userID, sid, "test_revocation")
		require.NoError(t, err)
		require.True(t, revoked)

		response := do(http.MethodPost, "/api/user/auth/refresh", "")
		assertStableAuthFailure(t, response, http.StatusUnauthorized, "AUTH_SESSION_REVOKED")
		assertAuthCookiesCleared(t, response)
	})

	t.Run("race remains retryable without clearing cookies", func(t *testing.T) {
		_, do, _, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
		first := do(http.MethodPost, "/api/user/auth/refresh", "")
		require.Equal(t, http.StatusOK, first.Code, first.Body.String())
		require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
			UpdateColumn("refresh_hash", strings.Repeat("a", 64)).Error)

		raced := do(http.MethodPost, "/api/user/auth/refresh", "")
		assertStableAuthFailure(t, raced, http.StatusConflict, "AUTH_REFRESH_RACE")
		assert.Empty(t, raced.Header().Values("Set-Cookie"))
	})

	t.Run("replay revokes and clears", func(t *testing.T) {
		_, do, _, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
		first := do(http.MethodPost, "/api/user/auth/refresh", "")
		require.Equal(t, http.StatusOK, first.Code, first.Body.String())
		require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
			UpdateColumn("previous_valid_until", common.NowTimestamp()-1).Error)

		replayed := do(http.MethodPost, "/api/user/auth/refresh", "")
		assertStableAuthFailure(t, replayed, http.StatusUnauthorized, "AUTH_SESSION_REVOKED")
		assertAuthCookiesCleared(t, replayed)
	})
}

func TestRefreshAuthUsesRemainingAbsoluteSessionLifetime(t *testing.T) {
	_, do, _, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
	databaseNow, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	wantExpiry := databaseNow + int64(time.Hour/time.Second)
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
		UpdateColumn("expires_at", wantExpiry).Error)

	recorder := do(http.MethodPost, "/api/user/auth/refresh", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var refreshCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "refresh_token" {
			refreshCookie = cookie
		}
	}
	require.NotNil(t, refreshCookie)
	assert.InDelta(t, int(time.Hour/time.Second), refreshCookie.MaxAge, 1)
	assert.Less(t, refreshCookie.MaxAge, int(service.RefreshTokenTTL/time.Second))
	assert.Equal(t, wantExpiry, refreshCookie.Expires.Unix())
	assert.True(t, refreshCookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, refreshCookie.SameSite)
}

func TestRefreshAuthUnexpectedAndInvalidExpiryFailuresAreGeneric(t *testing.T) {
	t.Run("storage failure", func(t *testing.T) {
		_, do, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
		require.NoError(t, model.DB.Migrator().DropTable(&model.UserSession{}))
		recorder := do(http.MethodPost, "/api/user/auth/refresh", "")
		assertStableAuthFailure(t, recorder, http.StatusInternalServerError, "AUTH_INTERNAL_ERROR")
		assert.NotContains(t, recorder.Body.String(), "user_sessions")
		assert.NotContains(t, recorder.Body.String(), "no such table")
	})

	t.Run("invalid absolute expiry", func(t *testing.T) {
		_, do, _, sid := setupAuthAdjacent(t, constant.RoleCommonUser)
		var before model.UserSession
		require.NoError(t, model.DB.Where("sid = ?", sid).First(&before).Error)
		databaseNow, err := model.DatabaseUnixTimestamp(model.DB)
		require.NoError(t, err)
		invalidExpiry := databaseNow + int64(service.RefreshTokenTTL/time.Second) + 60
		require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).
			UpdateColumn("expires_at", invalidExpiry).Error)

		recorder := do(http.MethodPost, "/api/user/auth/refresh", "")
		assertStableAuthFailure(t, recorder, http.StatusInternalServerError, "AUTH_INTERNAL_ERROR")
		assertAuthCookiesCleared(t, recorder)
		var after model.UserSession
		require.NoError(t, model.DB.Where("sid = ?", sid).First(&after).Error)
		assert.Equal(t, before.Version, after.Version)
		assert.Equal(t, before.RefreshHash, after.RefreshHash)
	})
}
