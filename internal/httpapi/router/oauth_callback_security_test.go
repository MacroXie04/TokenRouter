package router_test

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type oauthCallbackProviderBehavior struct {
	tokenStatus    atomic.Int32
	userInfoStatus atomic.Int32
	tokenCalls     atomic.Int32
	userInfoCalls  atomic.Int32
}

func newOAuthCallbackTestProvider(t *testing.T, behavior *oauthCallbackProviderBehavior) *model.CustomOAuthProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			behavior.tokenCalls.Add(1)
			if status := behavior.tokenStatus.Load(); status != 0 {
				w.WriteHeader(int(status))
				_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"callback-token","token_type":"bearer"}`))
		case "/user":
			behavior.userInfoCalls.Add(1)
			if status := behavior.userInfoStatus.Load(); status != 0 {
				w.WriteHeader(int(status))
				_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"sub":"callback-subject","preferred_username":"callback-user"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	provider := &model.CustomOAuthProvider{
		Name: "Callback SSO", Slug: "callback-sso", Enabled: true,
		ClientId: "client", ClientSecret: "secret",
		AuthorizationEndpoint: srv.URL + "/authorize",
		TokenEndpoint:         srv.URL + "/token",
		UserInfoEndpoint:      srv.URL + "/user",
	}
	require.NoError(t, model.CreateCustomOAuthProvider(provider))
	return provider
}

func createOAuthBindFlow(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder) string {
	t.Helper()
	rec := do(http.MethodPost, "/api/oauth/state", `{"provider":"callback-sso","intent":"bind"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"])
	flowToken, ok := body["data"].(map[string]any)["flow_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, flowToken)
	return flowToken
}

func assertOAuthFlowPending(t *testing.T, token string) *model.AuthFlow {
	t.Helper()
	flow, err := authsvc.PeekAuthFlow(token, authsvc.AuthFlowPurposeOAuth)
	require.NoError(t, err)
	require.Nil(t, flow.ConsumedAt)
	return flow
}

func TestOAuthBindCallbackRequiresExactLiveRequestSession(t *testing.T) {
	handler, doOwner, ownerID := setupCustomOAuthTest(t)
	behavior := &oauthCallbackProviderBehavior{}
	provider := newOAuthCallbackTestProvider(t, behavior)
	flowToken := createOAuthBindFlow(t, doOwner)
	callbackPath := "/api/oauth/callback-sso/callback?state=" + url.QueryEscape(flowToken) + "&code=code"

	// A callback without the dashboard session that created the flow is denied
	// before any provider request and does not consume state.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, callbackPath, nil))
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "error=oauth_state")
	assertOAuthFlowPending(t, flowToken)
	assert.Zero(t, behavior.tokenCalls.Load())
	assert.Zero(t, behavior.userInfoCalls.Load())

	// Even another live session belonging to the same user is not the session
	// that created this ceremony.
	var owner model.User
	require.NoError(t, model.DB.First(&owner, ownerID).Error)
	otherOwnerSID, otherOwnerAccess, otherOwnerRefresh, err := authsvc.CompleteLogin(
		&owner, "127.0.0.7", "same-user-other-session", "test",
	)
	require.NoError(t, err)
	rec = doAsUser(t, handler, otherOwnerSID, otherOwnerAccess, otherOwnerRefresh)(http.MethodGet, callbackPath, "")
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "error=oauth_state")
	assertOAuthFlowPending(t, flowToken)
	assert.Zero(t, behavior.tokenCalls.Load())
	assert.Zero(t, behavior.userInfoCalls.Load())

	// A different live user/session is equally invalid and still cannot burn
	// the owner's ceremony.
	other := model.User{Username: "oauth-callback-other", Password: "pw", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&other).Error)
	sid, access, refresh, err := authsvc.CompleteLogin(&other, "127.0.0.8", "other", "test")
	require.NoError(t, err)
	rec = doAsUser(t, handler, sid, access, refresh)(http.MethodGet, callbackPath, "")
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "error=oauth_state")
	assertOAuthFlowPending(t, flowToken)
	assert.Zero(t, behavior.tokenCalls.Load())
	assert.Zero(t, behavior.userInfoCalls.Load())

	// The exact creating session succeeds once. A replay is rejected before a
	// second provider exchange.
	rec = doOwner(http.MethodGet, callbackPath, "")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/?oauth_bound=1", rec.Header().Get("Location"))
	_, err = authsvc.PeekAuthFlow(flowToken, authsvc.AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, authsvc.ErrInvalidFlowToken)
	bound, err := model.GetUserByOAuthBinding(provider.Id, "callback-subject")
	require.NoError(t, err)
	assert.Equal(t, ownerID, bound.Id)
	assert.EqualValues(t, 1, behavior.tokenCalls.Load())
	assert.EqualValues(t, 1, behavior.userInfoCalls.Load())

	rec = doOwner(http.MethodGet, callbackPath, "")
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "error=oauth_state")
	assert.EqualValues(t, 1, behavior.tokenCalls.Load())
	assert.EqualValues(t, 1, behavior.userInfoCalls.Load())
}

func TestOAuthCallbackProviderFailuresLeaveLoginAndBindFlowsRetryable(t *testing.T) {
	tests := []struct {
		intent  string
		failure string
	}{
		{intent: "login", failure: "token"},
		{intent: "login", failure: "userinfo"},
		{intent: "bind", failure: "token"},
		{intent: "bind", failure: "userinfo"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%s_%s", test.intent, test.failure), func(t *testing.T) {
			handler, doOwner, _ := setupCustomOAuthTest(t)
			behavior := &oauthCallbackProviderBehavior{}
			if test.failure == "token" {
				behavior.tokenStatus.Store(http.StatusServiceUnavailable)
			} else {
				behavior.userInfoStatus.Store(http.StatusServiceUnavailable)
			}
			newOAuthCallbackTestProvider(t, behavior)

			var flowToken string
			if test.intent == "bind" {
				flowToken = createOAuthBindFlow(t, doOwner)
			} else {
				flowToken = startCustomOAuthLogin(t, handler, "callback-sso")
			}
			callbackPath := "/api/oauth/callback-sso/callback?state=" + url.QueryEscape(flowToken) + "&code=code"
			perform := func() *httptest.ResponseRecorder {
				if test.intent == "bind" {
					return doOwner(http.MethodGet, callbackPath, "")
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, callbackPath, nil))
				return rec
			}

			rec := perform()
			require.Equal(t, http.StatusFound, rec.Code)
			if test.intent == "bind" {
				assert.Equal(t, "/?oauth_bound=error", rec.Header().Get("Location"))
			} else {
				assert.Contains(t, rec.Header().Get("Location"), "error=oauth_"+test.failure)
			}
			pending := assertOAuthFlowPending(t, flowToken)
			assert.Equal(t, test.intent, pending.Intent)

			// Once the transient upstream error clears, the same unconsumed flow
			// can finish normally and is then unavailable for replay.
			behavior.tokenStatus.Store(0)
			behavior.userInfoStatus.Store(0)
			rec = perform()
			require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
			if test.intent == "bind" {
				assert.Equal(t, "/?oauth_bound=1", rec.Header().Get("Location"))
			} else {
				assert.Equal(t, "/", rec.Header().Get("Location"))
			}
			_, err := authsvc.PeekAuthFlow(flowToken, authsvc.AuthFlowPurposeOAuth)
			assert.ErrorIs(t, err, authsvc.ErrInvalidFlowToken)
		})
	}
}

func TestOAuthCallbackRejectsDuplicateAndHighCardinalityQueriesBeforeExchange(t *testing.T) {
	for _, test := range []struct {
		name       string
		buildQuery func(string) string
	}{
		{
			name: "duplicate state",
			buildQuery: func(state string) string {
				return "state=" + url.QueryEscape(state) + "&state=" + url.QueryEscape(state) + "&code=code"
			},
		},
		{
			name: "duplicate code",
			buildQuery: func(state string) string {
				return "state=" + url.QueryEscape(state) + "&code=first&code=second"
			},
		},
		{
			name: "too many pairs",
			buildQuery: func(state string) string {
				query := url.Values{"state": {state}, "code": {"code"}}
				for index := 0; index < 15; index++ {
					query.Set(fmt.Sprintf("extra-%02d", index), "value")
				}
				return query.Encode()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _, _ := setupCustomOAuthTest(t)
			behavior := &oauthCallbackProviderBehavior{}
			newOAuthCallbackTestProvider(t, behavior)
			flowToken := startCustomOAuthLogin(t, handler, "callback-sso")
			path := "/api/oauth/callback-sso/callback?" + test.buildQuery(flowToken)

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusFound, recorder.Code)
			assert.Contains(t, recorder.Header().Get("Location"), "error=invalid_oauth")
			assertOAuthFlowPending(t, flowToken)
			assert.Zero(t, behavior.tokenCalls.Load())
			assert.Zero(t, behavior.userInfoCalls.Load())
		})
	}
}

func TestOAuthStartCarriesOnlyOneBoundedAffiliateCode(t *testing.T) {
	handler, _, _ := setupCustomOAuthTest(t)
	behavior := &oauthCallbackProviderBehavior{}
	newOAuthCallbackTestProvider(t, behavior)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/oauth/callback-sso?aff=partner-code&redirect=%2Fdashboard%2Fmodels", nil))
	require.Equal(t, http.StatusFound, recorder.Code, recorder.Body.String())
	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)
	flow, err := authsvc.PeekAuthFlow(location.Query().Get("state"), authsvc.AuthFlowPurposeOAuth)
	require.NoError(t, err)
	assert.JSONEq(t, `{"affiliate_code":"partner-code","return_to":"/dashboard/models"}`, flow.Payload)

	var before int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&before).Error)
	for _, query := range []string{
		"aff=one&aff=two",
		"aff=" + strings.Repeat("x", 33),
		"unexpected=value",
		"redirect=https%3A%2F%2Fattacker.example%2F",
		"redirect=%2Foauth%2Fcallback-sso",
		"redirect=%2Fdashboard%2F..%2Fwallet",
	} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
			"/api/oauth/callback-sso?"+query, nil))
		assert.Equal(t, http.StatusBadRequest, recorder.Code, query)
	}
	var after int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&after).Error)
	assert.Equal(t, before, after, "invalid login parameters must not allocate state rows")
}

func TestOAuthCallbackReturnsOnlyToStateBoundLocalTarget(t *testing.T) {
	handler, _, _ := setupCustomOAuthTest(t)
	behavior := &oauthCallbackProviderBehavior{}
	newOAuthCallbackTestProvider(t, behavior)

	start := func(path string) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusFound, recorder.Code, recorder.Body.String())
		location, err := url.Parse(recorder.Header().Get("Location"))
		require.NoError(t, err)
		return location.Query().Get("state")
	}

	boundState := start("/api/oauth/callback-sso?redirect=%2Fdashboard%2Fmodels%3Fwindow%3Dweek")
	boundCallback := httptest.NewRecorder()
	handler.ServeHTTP(boundCallback, httptest.NewRequest(http.MethodGet,
		"/api/oauth/callback-sso/callback?state="+url.QueryEscape(boundState)+"&code=code", nil))
	require.Equal(t, http.StatusFound, boundCallback.Code, boundCallback.Body.String())
	assert.Equal(t, "/dashboard/models?window=week", boundCallback.Header().Get("Location"))

	defaultState := start("/api/oauth/callback-sso")
	untrustedCallback := httptest.NewRecorder()
	handler.ServeHTTP(untrustedCallback, httptest.NewRequest(http.MethodGet,
		"/api/oauth/callback-sso/callback?state="+url.QueryEscape(defaultState)+
			"&code=code&redirect=https%3A%2F%2Fattacker.example%2F", nil))
	require.Equal(t, http.StatusFound, untrustedCallback.Code, untrustedCallback.Body.String())
	assert.Equal(t, "/", untrustedCallback.Header().Get("Location"))
}

func TestOAuthCallbackProviderDenialConsumesStateWithoutReflectingDescription(t *testing.T) {
	handler, _, _ := setupCustomOAuthTest(t)
	behavior := &oauthCallbackProviderBehavior{}
	newOAuthCallbackTestProvider(t, behavior)

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet,
		"/api/oauth/callback-sso?redirect=%2Fwallet", nil))
	require.Equal(t, http.StatusFound, start.Code, start.Body.String())
	providerLocation, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	state := providerLocation.Query().Get("state")
	require.NotEmpty(t, state)

	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet,
		"/api/oauth/callback-sso/callback?state="+url.QueryEscape(state)+
			"&error=access_denied&error_description="+url.QueryEscape("private provider detail"), nil))
	require.Equal(t, http.StatusFound, denied.Code)
	location, err := url.Parse(denied.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/login", location.Path)
	assert.Equal(t, "oauth_denied", location.Query().Get("error"))
	assert.Equal(t, "/wallet", location.Query().Get("redirect"))
	assert.NotContains(t, denied.Header().Get("Location"), "private")
	assert.Zero(t, behavior.tokenCalls.Load())
	assert.Zero(t, behavior.userInfoCalls.Load())
	_, err = authsvc.PeekAuthFlow(state, authsvc.AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, authsvc.ErrInvalidFlowToken)
}

func TestOAuthBindCallbackUsesStateBoundProfileReturn(t *testing.T) {
	_, doOwner, _ := setupCustomOAuthTest(t)
	behavior := &oauthCallbackProviderBehavior{}
	newOAuthCallbackTestProvider(t, behavior)

	stateResponse := doOwner(http.MethodPost, "/api/oauth/state",
		`{"provider":"callback-sso","intent":"bind","redirect":"/profile"}`)
	require.Equal(t, http.StatusOK, stateResponse.Code, stateResponse.Body.String())
	state := decodeBody(t, stateResponse)["data"].(map[string]any)["flow_token"].(string)
	callback := doOwner(http.MethodGet,
		"/api/oauth/callback-sso/callback?state="+url.QueryEscape(state)+"&code=code", "")
	require.Equal(t, http.StatusFound, callback.Code, callback.Body.String())
	assert.Equal(t, "/profile?oauth_bound=1", callback.Header().Get("Location"))
}

func TestOAuthCallbackRegistrationDisabledRollsBackState(t *testing.T) {
	handler, _, _ := setupCustomOAuthTest(t)
	behavior := &oauthCallbackProviderBehavior{}
	newOAuthCallbackTestProvider(t, behavior)
	require.NoError(t, setting.UpdateOption(setting.RegistrationEnabledOption, "false"))
	flowToken := startCustomOAuthLogin(t, handler, "callback-sso")
	callbackPath := "/api/oauth/callback-sso/callback?state=" + url.QueryEscape(flowToken) + "&code=code"

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, callbackPath, nil))
	require.Equal(t, http.StatusFound, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Location"), "error=register_disabled")
	assertOAuthFlowPending(t, flowToken)
	var bindingCount int64
	require.NoError(t, model.DB.Model(&model.UserOAuthBinding{}).
		Where("provider_user_id = ?", "callback-subject").Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}
