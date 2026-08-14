package service

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func initCustomOAuthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.CustomOAuthProvider{}, &model.UserOAuthBinding{}))
	model.DB = db
	model.LOG_DB = db
	// The custom OAuth clients dial through the SSRF guard; allow loopback
	// httptest upstreams.
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
}

const policyTestBody = `{
	"sub": "u-1",
	"plan": "pro",
	"level": 7,
	"active": true,
	"tags": ["staff", "beta"],
	"org": {"name": "acme", "seats": 3},
	"nothing": null
}`

func TestEvaluateAccessPolicyOps(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"eq string", `{"conditions":[{"field":"plan","op":"eq","value":"pro"}]}`, true},
		{"eq mismatch", `{"conditions":[{"field":"plan","op":"eq","value":"free"}]}`, false},
		{"ne", `{"conditions":[{"field":"plan","op":"ne","value":"free"}]}`, true},
		{"numeric gt", `{"conditions":[{"field":"level","op":"gt","value":5}]}`, true},
		{"numeric gte equal", `{"conditions":[{"field":"level","op":"gte","value":7}]}`, true},
		{"numeric lt fails", `{"conditions":[{"field":"level","op":"lt","value":7}]}`, false},
		{"numeric lte", `{"conditions":[{"field":"level","op":"lte","value":7}]}`, true},
		{"numeric string coercion", `{"conditions":[{"field":"level","op":"gt","value":"05"}]}`, true},
		{"in", `{"conditions":[{"field":"plan","op":"in","value":["free","pro"]}]}`, true},
		{"not_in", `{"conditions":[{"field":"plan","op":"not_in","value":["free"]}]}`, true},
		{"in miss", `{"conditions":[{"field":"plan","op":"in","value":["free"]}]}`, false},
		{"contains substring", `{"conditions":[{"field":"org.name","op":"contains","value":"cm"}]}`, true},
		{"contains array element", `{"conditions":[{"field":"tags","op":"contains","value":"staff"}]}`, true},
		{"not_contains", `{"conditions":[{"field":"tags","op":"not_contains","value":"admin"}]}`, true},
		{"exists", `{"conditions":[{"field":"org.seats","op":"exists"}]}`, true},
		{"exists null field", `{"conditions":[{"field":"nothing","op":"exists"}]}`, true},
		{"not_exists", `{"conditions":[{"field":"missing","op":"not_exists"}]}`, true},
		{"bool eq", `{"conditions":[{"field":"active","op":"eq","value":true}]}`, true},
		{"and needs all", `{"conditions":[{"field":"plan","op":"eq","value":"pro"},{"field":"level","op":"gt","value":10}]}`, false},
		{"or needs one", `{"logic":"or","conditions":[{"field":"plan","op":"eq","value":"free"},{"field":"level","op":"gt","value":5}]}`, true},
		{"nested groups", `{"logic":"or","conditions":[{"field":"plan","op":"eq","value":"free"}],"groups":[{"conditions":[{"field":"active","op":"eq","value":true},{"field":"org.seats","op":"gte","value":2}]}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := model.ParseAccessPolicy(tc.raw)
			require.NoError(t, err)
			allowed, failure := evaluateAccessPolicy(policyTestBody, policy)
			assert.Equal(t, tc.want, allowed)
			if !tc.want {
				require.NotNil(t, failure, "denials must report the failing condition")
			}
		})
	}

	// OR reports the FIRST failure when nothing passes.
	policy, err := model.ParseAccessPolicy(`{"logic":"or","conditions":[{"field":"plan","op":"eq","value":"free"},{"field":"level","op":"gt","value":10}]}`)
	require.NoError(t, err)
	allowed, failure := evaluateAccessPolicy(policyTestBody, policy)
	require.False(t, allowed)
	require.NotNil(t, failure)
	assert.Equal(t, "plan", failure.Field)
	assert.Equal(t, "eq", failure.Op)
	assert.Equal(t, "pro", failure.Current)
}

func TestRenderAccessDeniedMessage(t *testing.T) {
	failure := &accessPolicyFailure{Field: "plan", Op: "eq", Expected: "enterprise", Current: "pro"}

	// Blank template falls back to the fixed default.
	assert.Equal(t,
		"Access denied: your account does not meet this provider's access requirements.",
		renderAccessDeniedMessage("  ", "Corp", policyTestBody, failure))

	// Simple replacements.
	got := renderAccessDeniedMessage(
		"{{provider}}: {{field}} {{op}} {{required}} but got {{current}}",
		"Corp", policyTestBody, failure)
	assert.Equal(t, "Corp: plan eq enterprise but got pro", got)

	// {{current.<path>}} pulls from the userinfo body; {{required.<path>}}
	// renders the expected value only for the failing field.
	got = renderAccessDeniedMessage(
		"org={{current.org.name}} need {{required.plan}} not {{required.level}}",
		"Corp", policyTestBody, failure)
	assert.Equal(t, "org=acme need enterprise not", got)
}

func TestCustomExchangeCodeAuthStyles(t *testing.T) {
	initCustomOAuthDB(t)
	for _, tc := range []struct {
		style      int
		wantHeader bool
	}{
		{OAuthAuthStyleAutoDetect, false},
		{OAuthAuthStyleInParams, false},
		{OAuthAuthStyleInHeader, true},
	} {
		t.Run(fmt.Sprintf("style_%d", tc.style), func(t *testing.T) {
			var gotForm url.Values
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, r.ParseForm())
				gotForm = r.PostForm
				gotAuth = r.Header.Get("Authorization")
				assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"tok-x","token_type":"bearer"}`))
			}))
			defer srv.Close()

			cfg := &OAuthConfig{
				ClientID: "cid", ClientSecret: "sec", TokenURL: srv.URL,
				Custom: &model.CustomOAuthProvider{AuthStyle: tc.style},
			}
			token, err := customExchangeCode(cfg, "the-code", "http://cb")
			require.NoError(t, err)
			assert.Equal(t, "tok-x", token.AccessToken)
			assert.Equal(t, "bearer", token.TokenType)
			assert.Equal(t, "authorization_code", gotForm.Get("grant_type"))
			assert.Equal(t, "the-code", gotForm.Get("code"))
			assert.Equal(t, "http://cb", gotForm.Get("redirect_uri"))
			if tc.wantHeader {
				expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:sec"))
				assert.Equal(t, expected, gotAuth)
				assert.Empty(t, gotForm.Get("client_id"))
				assert.Empty(t, gotForm.Get("client_secret"))
			} else {
				assert.Empty(t, gotAuth)
				assert.Equal(t, "cid", gotForm.Get("client_id"))
				assert.Equal(t, "sec", gotForm.Get("client_secret"))
			}
		})
	}
}

func TestCustomExchangeCodeResponses(t *testing.T) {
	initCustomOAuthDB(t)
	serve := func(body string) *OAuthConfig {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return &OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: srv.URL,
			Custom: &model.CustomOAuthProvider{}}
	}

	// GitHub-style urlencoded token response.
	token, err := customExchangeCode(serve("access_token=tok-q&token_type=mac"), "c", "r")
	require.NoError(t, err)
	assert.Equal(t, "tok-q", token.AccessToken)
	assert.Equal(t, "mac", token.TokenType)

	// error field fails with both codes.
	_, err = customExchangeCode(serve(`{"error":"invalid_grant","error_description":"expired"}`), "c", "r")
	require.EqualError(t, err, "token exchange failed: invalid_grant expired")

	// Empty access_token fails.
	_, err = customExchangeCode(serve(`{"token_type":"bearer"}`), "c", "r")
	require.EqualError(t, err, "token exchange returned no access_token")
}

func TestCustomFetchUserInfoMapping(t *testing.T) {
	initCustomOAuthDB(t)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"uid":9001},"login":"alice","profile":{"display":"Alice A"},"contact":{"mail":"a@example.com"}}`))
	}))
	defer srv.Close()

	provider := &model.CustomOAuthProvider{
		Id: 7, Name: "Corp", UserInfoEndpoint: srv.URL,
		UserIdField: "data.uid", UsernameField: "login",
		DisplayNameField: "profile.display", EmailField: "contact.mail",
	}
	cfg := &OAuthConfig{UserInfoURL: srv.URL, Custom: provider}

	// Numeric ids stringify; empty token_type normalizes to Bearer.
	pu, err := customFetchUserInfo(cfg, &OAuthToken{AccessToken: "tok-1"})
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok-1", gotAuth)
	assert.Equal(t, "9001", pu.ProviderID)
	assert.Equal(t, "alice", pu.Username)
	assert.Equal(t, "Alice A", pu.DisplayName)
	assert.Equal(t, "a@example.com", pu.Email)
	assert.Equal(t, 7, pu.CustomProviderId)

	// Non-bearer token types pass through verbatim.
	_, err = customFetchUserInfo(cfg, &OAuthToken{AccessToken: "tok-2", TokenType: "MAC"})
	require.NoError(t, err)
	assert.Equal(t, "MAC tok-2", gotAuth)

	// Missing id field fails with the field name.
	provider.UserIdField = "data.missing"
	_, err = customFetchUserInfo(cfg, &OAuthToken{AccessToken: "tok-3"})
	require.EqualError(t, err, "provider user has no id (field: data.missing)")
}

func TestCustomFetchUserInfoPolicy(t *testing.T) {
	initCustomOAuthDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"u-1","plan":"free"}`))
	}))
	defer srv.Close()

	provider := &model.CustomOAuthProvider{
		Id: 3, Name: "Corp", UserInfoEndpoint: srv.URL, UserIdField: "sub",
		UsernameField: "login", DisplayNameField: "name", EmailField: "email",
		AccessPolicy:        `{"conditions":[{"field":"plan","op":"eq","value":"pro"}]}`,
		AccessDeniedMessage: "{{provider}} requires plan {{required}}, you have {{current}}",
	}
	cfg := &OAuthConfig{UserInfoURL: srv.URL, Custom: provider}

	// Policy denial surfaces the rendered template as OAuthAccessDeniedError.
	_, err := customFetchUserInfo(cfg, &OAuthToken{AccessToken: "t"})
	var denied *OAuthAccessDeniedError
	require.ErrorAs(t, err, &denied)
	assert.Equal(t, "Corp requires plan pro, you have free", denied.Message)

	// A corrupt stored policy fails closed with the fixed message.
	provider.AccessPolicy = "{broken"
	_, err = customFetchUserInfo(cfg, &OAuthToken{AccessToken: "t"})
	require.EqualError(t, err, "invalid access policy configuration")

	// A passing policy admits the user.
	provider.AccessPolicy = `{"conditions":[{"field":"plan","op":"eq","value":"free"}]}`
	pu, err := customFetchUserInfo(cfg, &OAuthToken{AccessToken: "t"})
	require.NoError(t, err)
	assert.Equal(t, "u-1", pu.ProviderID)
}

// newCustomIdP builds a mock identity provider plus its DB row and returns the
// provider row (token endpoint /token, userinfo /user).
func newCustomIdP(t *testing.T, userinfo string) *model.CustomOAuthProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"tok-flow","token_type":"bearer"}`))
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
		UserIdField:           "sub", UsernameField: "preferred_username",
		DisplayNameField: "name", EmailField: "email",
		Scopes: "openid profile email",
	}
	require.NoError(t, model.CreateCustomOAuthProvider(provider))
	return provider
}

func TestCustomOAuthFullLoginFlow(t *testing.T) {
	initCustomOAuthDB(t)
	provider := newCustomIdP(t, `{"sub":"ext-9","preferred_username":"casey","name":"Casey Q","email":"c@example.com"}`)

	// Slug resolution: enabled custom config with the provider attached.
	cfg := GetOAuthConfig("corp-sso")
	require.NotNil(t, cfg.Custom)
	assert.True(t, cfg.Known)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, provider.Id, cfg.Custom.Id)
	assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Scopes)

	// Built-in slugs and unknown slugs never resolve as custom.
	assert.Nil(t, GetOAuthConfig("github").Custom)
	notFound := GetOAuthConfig("nope-provider")
	assert.Nil(t, notFound.Custom)
	assert.False(t, notFound.Known)

	// Code exchange + userinfo through the custom path.
	token, err := ExchangeCode(cfg, "code-1", "http://cb")
	require.NoError(t, err)
	pu, err := FetchUserInfo(cfg, "corp-sso", token)
	require.NoError(t, err)
	assert.Equal(t, provider.Id, pu.CustomProviderId)

	// First login creates the user and its binding row atomically.
	user, isNew, err := LoginOrBindUserWithAff("corp-sso", pu, "")
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.Equal(t, "casey", user.Username)
	assert.Equal(t, "Casey Q", user.DisplayName)
	var binding model.UserOAuthBinding
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&binding).Error)
	assert.Equal(t, provider.Id, binding.ProviderId)
	assert.Equal(t, "ext-9", binding.ProviderUserId)

	// Second login resolves the same user through the binding.
	again, isNew2, err := LoginOrBindUserWithAff("corp-sso", pu, "")
	require.NoError(t, err)
	assert.False(t, isNew2)
	assert.Equal(t, user.Id, again.Id)

	// Binding the same identity to another user is rejected.
	other := model.User{Username: "other", Password: "pw", Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&other).Error)
	assert.ErrorIs(t, BindProviderToUser("corp-sso", pu, other.Id), ErrBindingTaken)

	// Binding a fresh identity to the other user upserts a row.
	fresh := &ProviderUser{ProviderID: "ext-10", CustomProviderId: provider.Id}
	require.NoError(t, BindProviderToUser("corp-sso", fresh, other.Id))
	var otherBinding model.UserOAuthBinding
	require.NoError(t, model.DB.Where("user_id = ?", other.Id).First(&otherBinding).Error)
	assert.Equal(t, "ext-10", otherBinding.ProviderUserId)
}

func TestCustomOAuthLoginWithoutUsername(t *testing.T) {
	initCustomOAuthDB(t)
	provider := newCustomIdP(t, `{"sub":"anon-1"}`)

	cfg := GetOAuthConfig("corp-sso")
	require.NotNil(t, cfg.Custom)
	token, err := ExchangeCode(cfg, "code", "http://cb")
	require.NoError(t, err)
	pu, err := FetchUserInfo(cfg, "corp-sso", token)
	require.NoError(t, err)

	// No username/display name mapped: falls back to slug-scoped username.
	user, isNew, err := LoginOrBindUserWithAff("corp-sso", pu, "")
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.Equal(t, "corp-sso_anon-1", user.Username)
	assert.Equal(t, "corp-sso_anon-1", user.DisplayName)
	_ = provider
}

func TestResolveCustomOAuthConfigGuards(t *testing.T) {
	initCustomOAuthDB(t)
	newCustomIdP(t, `{}`)

	// Built-in slugs, malformed slugs, and unknown slugs return nil.
	assert.Nil(t, resolveCustomOAuthConfig("github"))
	assert.Nil(t, resolveCustomOAuthConfig("wechat"))
	assert.Nil(t, resolveCustomOAuthConfig("Corp-SSO"))
	assert.Nil(t, resolveCustomOAuthConfig("corp sso"))
	assert.Nil(t, resolveCustomOAuthConfig("unknown-slug"))
	assert.NotNil(t, resolveCustomOAuthConfig("corp-sso"))

	// Disabled providers still resolve (Known) but stay disabled, so the
	// authorize handler rejects them.
	require.NoError(t, model.DB.Model(&model.CustomOAuthProvider{}).Where("slug = ?", "corp-sso").
		Update("enabled", false).Error)
	cfg := resolveCustomOAuthConfig("corp-sso")
	require.NotNil(t, cfg)
	assert.True(t, cfg.Known)
	assert.False(t, cfg.Enabled)
}
