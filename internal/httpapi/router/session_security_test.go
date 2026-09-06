package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginSessionEndpointsEnforceOwnership(t *testing.T) {
	_, do, actorID, currentSID := setupSelfTest(t)
	var actor model.User
	require.NoError(t, model.DB.First(&actor, actorID).Error)
	ownedSID, _, err := authsvc.CreateSession(&actor, "127.0.0.2", "owned", "test")
	require.NoError(t, err)

	foreign := model.User{Username: "session-foreign", Password: "pw", Role: 1,
		Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&foreign).Error)
	foreignSID, _, err := authsvc.CreateSession(&foreign, "127.0.0.3", "foreign", "test")
	require.NoError(t, err)

	// A foreign and a nonexistent SID are indistinguishable and neither can
	// be revoked through the actor's endpoint.
	foreignResponse := do(http.MethodDelete, "/api/user/sessions/"+foreignSID, "")
	missingResponse := do(http.MethodDelete, "/api/user/sessions/missing-sid", "")
	assert.Equal(t, http.StatusNotFound, foreignResponse.Code)
	assert.Equal(t, http.StatusNotFound, missingResponse.Code)
	assert.Equal(t, decodeBody(t, foreignResponse)["message"], decodeBody(t, missingResponse)["message"])

	var foreignSession model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", foreignSID).First(&foreignSession).Error)
	assert.Equal(t, authsvc.SessionStatusActive, foreignSession.Status)

	// An owned session can be revoked and remains as a tombstone.
	revokeBody := decodeOK(t, do(http.MethodDelete, "/api/user/sessions/"+ownedSID, ""))
	revokeData := revokeBody["data"].(map[string]any)
	assert.Equal(t, ownedSID, revokeData["revoked_sid"])
	assert.Equal(t, false, revokeData["current"])
	var ownedSession model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", ownedSID).First(&ownedSession).Error)
	assert.Equal(t, authsvc.SessionStatusRevoked, ownedSession.Status)
	assert.NotZero(t, ownedSession.RevokedAt)

	// The list exposes only the actor's still-live sessions.
	body := decodeOK(t, do(http.MethodGet, "/api/user/sessions", ""))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	sessionView := data[0].(map[string]any)
	assert.Equal(t, currentSID, sessionView["sid"])
	assert.Equal(t, true, sessionView["current"])
	for _, internalField := range []string{
		"user_id", "version", "user_auth_version", "status", "refresh_hash",
		"previous_refresh_hash", "previous_valid_until", "revoked_at", "revoked_reason",
	} {
		assert.NotContains(t, sessionView, internalField)
	}
	assert.IsType(t, float64(0), sessionView["created_at"])

	revokeOthersBody := decodeOK(t, do(http.MethodPost, "/api/user/sessions/revoke-others", ""))
	revokeOthersData := revokeOthersBody["data"].(map[string]any)
	assert.EqualValues(t, 0, revokeOthersData["revoked_count"])

	missingBody := decodeBody(t, missingResponse)
	assert.Equal(t, "AUTH_SESSION_NOT_FOUND", missingBody["code"])
}

func TestRevokedSessionJWTFailsImmediatelyWhilePATRemainsIndependent(t *testing.T) {
	handler, do, userID, sid := setupSelfTest(t)
	pat, err := authsvc.GenerateUserAccessToken(userID)
	require.NoError(t, err)
	revoked, err := authsvc.RevokeUserSession(userID, sid, "test")
	require.NoError(t, err)
	require.True(t, revoked)

	// The still-unexpired access JWT is denied immediately after its session
	// row is revoked.
	rec := do(http.MethodGet, "/api/user/self", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// The opaque personal access token is a separate credential and remains
	// valid, but it does not impersonate a browser session for session controls.
	patRequest := httptest.NewRequest(http.MethodGet, "/api/user/self", nil)
	patRequest.Header.Set("Authorization", "Bearer "+pat)
	patResponse := httptest.NewRecorder()
	handler.ServeHTTP(patResponse, patRequest)
	assert.Equal(t, http.StatusOK, patResponse.Code, patResponse.Body.String())

	patSessionsRequest := httptest.NewRequest(http.MethodGet, "/api/user/sessions", nil)
	patSessionsRequest.Header.Set("Authorization", "Bearer "+pat)
	patSessionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(patSessionsResponse, patSessionsRequest)
	assert.Equal(t, http.StatusForbidden, patSessionsResponse.Code)
	assert.Equal(t, "AUTH_SESSION_REQUIRED", decodeBody(t, patSessionsResponse)["code"])
}

func TestLogoutCannotRevokeArbitrarySID(t *testing.T) {
	handler, _, _, _ := setupSelfTest(t)
	foreign := model.User{Username: "logout-foreign", Password: "pw", Role: 1,
		Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&foreign).Error)
	foreignSID, foreignRefresh, err := authsvc.CreateSession(&foreign, "127.0.0.4", "foreign", "test")
	require.NoError(t, err)

	logout := func(refresh string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: foreignSID + "." + refresh})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusOK, logout("wrong-secret").Code)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", foreignSID).First(&session).Error)
	assert.Equal(t, authsvc.SessionStatusActive, session.Status)

	assert.Equal(t, http.StatusOK, logout(foreignRefresh).Code)
	require.NoError(t, model.DB.Where("sid = ?", foreignSID).First(&session).Error)
	assert.Equal(t, authsvc.SessionStatusRevoked, session.Status)

	// A signed access token also scopes logout to its own user/session when a
	// refresh cookie is unavailable (for API clients using bearer access).
	bearerSID, bearerAccess, _, err := authsvc.CompleteLogin(&foreign, "127.0.0.5", "bearer", "test")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+bearerAccess)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	session = model.UserSession{}
	require.NoError(t, model.DB.Where("sid = ?", bearerSID).First(&session).Error)
	assert.Equal(t, authsvc.SessionStatusRevoked, session.Status)
}

func TestLogoutPropagatesDatabaseFailure(t *testing.T) {
	_, do, _, _ := setupSelfTest(t)
	sqlDB, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	rec := do(http.MethodPost, "/api/user/auth/logout", "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
