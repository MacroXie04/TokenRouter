package auth

import (
	"encoding/json"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func initOAuthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "oauth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserOAuthBinding{}, &model.ExternalIdentityClaim{}, &model.Option{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
}

func allowOAuthLoopback(t *testing.T) {
	t.Helper()
	// Register restoration before t.Setenv so the environment is restored first
	// and InitSSRF then republishes the production-safe value.
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
}

func clearBuiltInOAuthEnvironment(t *testing.T, provider string) {
	t.Helper()
	for _, key := range oauthEnvironmentKeys(provider) {
		value, present := os.LookupEnv(key)
		require.NoError(t, os.Unsetenv(key))
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func TestOAuthResponsesAreBounded(t *testing.T) {
	allowOAuthLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxOAuthResponseBytes)+1)))
	}))
	defer srv.Close()
	cfg := &OAuthConfig{ClientID: "cid", ClientSecret: "secret", TokenURL: srv.URL, UserInfoURL: srv.URL}

	_, err := ExchangeCode(cfg, "code", "http://localhost/callback")
	require.Error(t, err)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	_, err = FetchUserInfo(cfg, "github", &OAuthToken{AccessToken: "token"})
	require.Error(t, err)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

func newMockOAuthServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	allowOAuthLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-123"})
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    float64(42),
				"login": "alice",
				"name":  "Alice Example",
				"email": "alice@example.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/token", srv.URL + "/user"
}

func TestBuiltInOAuthClientUsesDirectSSRFSafeTransport(t *testing.T) {
	transport, ok := oauthHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
}

func TestBuiltInOAuthClientBlocksUnsafeConfiguredEndpoints(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	httpx.InitSSRF()
	cfg := &OAuthConfig{
		ClientID: "client", ClientSecret: "secret",
		TokenURL: "http://127.0.0.1:65535/token", UserInfoURL: "http://127.0.0.1:65535/user",
	}

	_, err := ExchangeCode(cfg, "code", "https://console.example/callback")
	require.EqualError(t, err, "OAuth token endpoint is invalid")
	_, err = FetchUserInfo(cfg, "oidc", &OAuthToken{AccessToken: "access-token"})
	require.EqualError(t, err, "OAuth userinfo endpoint is invalid")
}

func TestBuiltInOAuthClientDoesNotFollowCredentialedRedirects(t *testing.T) {
	allowOAuthLoopback(t)
	var redirectedRequests int
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests++
	}))
	defer sink.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", sink.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfg := &OAuthConfig{
		ClientID: "client", ClientSecret: "secret",
		TokenURL: redirector.URL, UserInfoURL: redirector.URL,
	}

	_, err := ExchangeCode(cfg, "code", "https://console.example/callback")
	require.Error(t, err)
	_, err = FetchUserInfo(cfg, "oidc", &OAuthToken{AccessToken: "access-token"})
	require.Error(t, err)
	assert.Zero(t, redirectedRequests)
}

func TestOAuthCodeFlow(t *testing.T) {
	initOAuthDB(t)
	srv, tokenURL, userURL := newMockOAuthServer(t)
	_ = srv
	cfg := &OAuthConfig{
		ClientID: "cid", ClientSecret: "secret",
		TokenURL: tokenURL, UserInfoURL: userURL,
		Scopes: []string{"read:user"},
	}

	token, err := ExchangeCode(cfg, "code", "http://localhost/callback")
	require.NoError(t, err)
	assert.Equal(t, "tok-123", token.AccessToken)

	pu, err := FetchUserInfo(cfg, "github", token)
	require.NoError(t, err)
	assert.Equal(t, "42", pu.ProviderID)
	assert.Equal(t, "alice", pu.Username)
	assert.Equal(t, "alice@example.com", pu.Email)
}

func TestLoginOrBindUser(t *testing.T) {
	initOAuthDB(t)
	pu := &ProviderUser{ProviderID: "42", Username: "alice", Email: "alice@example.com", DisplayName: "Alice Example"}

	user, isNew, err := LoginOrBindUser("github", pu)
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.Equal(t, "42", user.GitHubId)
	assert.Equal(t, "alice", user.Username)

	// Second login binds to the existing user.
	user2, isNew2, err := LoginOrBindUser("github", pu)
	require.NoError(t, err)
	assert.False(t, isNew2)
	assert.Equal(t, user.Id, user2.Id)
}

func TestLoginOrBindUserUsesCanonicalClaimsForEveryBuiltInProvider(t *testing.T) {
	providers := []string{"github", "discord", "oidc", "wechat", "telegram", "linuxdo"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			initOAuthDB(t)
			pu := &ProviderUser{
				ProviderID: provider + "-subject", Username: provider + "-user", DisplayName: provider + " user",
			}
			user, created, err := LoginOrBindUser(provider, pu)
			require.NoError(t, err)
			assert.True(t, created)
			claim, err := model.FindExternalIdentityClaimWithTx(model.DB, provider, pu.ProviderID)
			require.NoError(t, err)
			assert.Equal(t, user.Id, claim.UserId)

			again, created, err := LoginOrBindUser(provider, pu)
			require.NoError(t, err)
			assert.False(t, created)
			assert.Equal(t, user.Id, again.Id)
		})
	}
}

func TestBuiltInIdentityLookupRejectsEmptyAndDeletedOwners(t *testing.T) {
	initOAuthDB(t)
	blank := model.User{Username: "blank-provider-user", Password: "password"}
	require.NoError(t, model.DB.Create(&blank).Error)
	_, err := FindUserByProviderIdentity("github", "")
	assert.ErrorIs(t, err, model.ErrInvalidPersistentIdentifier)
	_, _, err = LoginOrBindUser("github", &ProviderUser{ProviderID: "", Username: "empty"})
	assert.Error(t, err)

	owner, _, err := LoginOrBindUser("github", &ProviderUser{ProviderID: "deleted-subject", Username: "deleted-owner"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Delete(owner).Error)
	_, _, err = LoginOrBindUser("github", &ProviderUser{ProviderID: "deleted-subject", Username: "replacement"})
	assert.ErrorIs(t, err, model.ErrExternalIdentityOwnerInvalid)
	var count int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Count(&count).Error)
	assert.EqualValues(t, 2, count, "a deleted owner's identity must never create a replacement account")
}

func TestBuiltInIdentityBindingReplacesSlotAtomically(t *testing.T) {
	initOAuthDB(t)
	owner := model.User{Username: "binding-owner", Password: "password"}
	competitor := model.User{Username: "binding-competitor", Password: "password"}
	require.NoError(t, model.DB.Create(&owner).Error)
	require.NoError(t, model.DB.Create(&competitor).Error)

	require.NoError(t, BindProviderToUser("github", &ProviderUser{ProviderID: "first-subject"}, owner.Id))
	require.NoError(t, BindProviderToUser("github", &ProviderUser{ProviderID: "second-subject"}, owner.Id))
	_, err := model.FindExternalIdentityClaimWithTx(model.DB, "github", "first-subject")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	claim, err := model.FindExternalIdentityClaimWithTx(model.DB, "github", "second-subject")
	require.NoError(t, err)
	assert.Equal(t, owner.Id, claim.UserId)
	assert.ErrorIs(t, BindProviderToUser("github", &ProviderUser{ProviderID: "second-subject"}, owner.Id), ErrBindingTaken)
	assert.ErrorIs(t, BindProviderToUser("github", &ProviderUser{ProviderID: "second-subject"}, competitor.Id), ErrBindingTaken)
}

func TestGitHubLegacyLoginClaimMigratesAtomically(t *testing.T) {
	initOAuthDB(t)
	owner := model.User{Username: "legacy-github-owner", Password: "password", GitHubId: "legacy-login"}
	require.NoError(t, model.DB.Create(&owner).Error)
	require.NoError(t, model.ClaimExternalIdentityWithTx(model.DB, "github", owner.GitHubId, owner.Id))

	resolved, created, err := LoginOrBindUser("github", &ProviderUser{
		ProviderID: "123456", LegacyProviderID: "legacy-login", Username: "legacy-login",
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, owner.Id, resolved.Id)
	assert.Equal(t, "123456", resolved.GitHubId)
	_, err = model.FindExternalIdentityClaimWithTx(model.DB, "github", "legacy-login")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	claim, err := model.FindExternalIdentityClaimWithTx(model.DB, "github", "123456")
	require.NoError(t, err)
	assert.Equal(t, owner.Id, claim.UserId)
}

func TestGitHubBindRejectsClaimedLegacyLogin(t *testing.T) {
	initOAuthDB(t)
	legacyOwner := model.User{Username: "github-legacy-owner", Password: "password", GitHubId: "claimed-login"}
	actor := model.User{Username: "github-bind-actor", Password: "password"}
	require.NoError(t, model.DB.Create(&legacyOwner).Error)
	require.NoError(t, model.DB.Create(&actor).Error)
	require.NoError(t, model.ClaimExternalIdentityWithTx(model.DB, "github", legacyOwner.GitHubId, legacyOwner.Id))

	err := BindProviderToUser("github", &ProviderUser{
		ProviderID: "987654", LegacyProviderID: "claimed-login",
	}, actor.Id)
	assert.ErrorIs(t, err, ErrBindingTaken)
	var reloaded model.User
	require.NoError(t, model.DB.First(&reloaded, actor.Id).Error)
	assert.Empty(t, reloaded.GitHubId)
}

func TestConcurrentBuiltInLoginConvergesOnOneOwner(t *testing.T) {
	initOAuthDB(t)
	start := make(chan struct{})
	type result struct {
		user    *model.User
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			user, created, err := LoginOrBindUser("discord", &ProviderUser{
				ProviderID: "shared-discord-subject", Username: "shared-discord-user",
			})
			results <- result{user: user, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	ownerID := 0
	createdCount := 0
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.user)
		if ownerID == 0 {
			ownerID = result.user.Id
		}
		assert.Equal(t, ownerID, result.user.Id)
		if result.created {
			createdCount++
		}
	}
	assert.Equal(t, 1, createdCount)
	var users, claims int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&users).Error)
	require.NoError(t, model.DB.Model(&model.ExternalIdentityClaim{}).Count(&claims).Error)
	assert.EqualValues(t, 1, users)
	assert.EqualValues(t, 1, claims)
}

func TestConcurrentBuiltInBindHasOneOwner(t *testing.T) {
	initOAuthDB(t)
	users := []model.User{
		{Username: "bind-racer-one", Password: "password"},
		{Username: "bind-racer-two", Password: "password"},
	}
	for i := range users {
		require.NoError(t, model.DB.Create(&users[i]).Error)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range users {
		wg.Add(1)
		go func(userID int) {
			defer wg.Done()
			<-start
			errs <- BindProviderToUser("oidc", &ProviderUser{ProviderID: "shared-oidc-subject"}, userID)
		}(users[i].Id)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, taken := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrBindingTaken):
			taken++
		default:
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, taken)
	var claims int64
	require.NoError(t, model.DB.Model(&model.ExternalIdentityClaim{}).Count(&claims).Error)
	assert.EqualValues(t, 1, claims)
}

func TestBuiltInIdentityCreationFailureRollsBackUserAndClaim(t *testing.T) {
	initOAuthDB(t)
	forced := errors.New("forced claim failure")
	const callback = "test:fail-external-identity-claim"
	require.NoError(t, model.DB.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "external_identity_claims" {
			tx.AddError(forced)
		}
	}))
	_, _, err := LoginOrBindUser("linuxdo", &ProviderUser{ProviderID: "rollback-subject", Username: "rollback-user"})
	require.ErrorIs(t, err, forced)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))

	var users, claims int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&users).Error)
	require.NoError(t, model.DB.Model(&model.ExternalIdentityClaim{}).Count(&claims).Error)
	assert.Zero(t, users)
	assert.Zero(t, claims)
}

func TestBuiltInIdentityMirrorFailureRollsBackClaim(t *testing.T) {
	initOAuthDB(t)
	user := model.User{Username: "mirror-failure-user", Password: "password"}
	require.NoError(t, model.DB.Create(&user).Error)
	forced := errors.New("forced mirror update failure")
	const callback = "test:fail-external-identity-mirror"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(forced)
		}
	}))
	err := BindProviderToUser("telegram", &ProviderUser{ProviderID: "rollback-telegram"}, user.Id)
	require.ErrorIs(t, err, forced)
	require.NoError(t, model.DB.Callback().Update().Remove(callback))

	var claims int64
	require.NoError(t, model.DB.Model(&model.ExternalIdentityClaim{}).Count(&claims).Error)
	assert.Zero(t, claims)
	var reloaded model.User
	require.NoError(t, model.DB.First(&reloaded, user.Id).Error)
	assert.Empty(t, reloaded.TelegramId)
}

func TestBuildAuthorizationURL(t *testing.T) {
	allowOAuthLoopback(t)
	cfg := &OAuthConfig{ClientID: "cid", AuthURL: "https://github.com/login/oauth/authorize", Scopes: []string{"read:user"}}
	u, err := BuildAuthorizationURL(cfg, "state123", "http://localhost/callback")
	require.NoError(t, err)
	assert.Contains(t, u, "client_id=cid")
	assert.Contains(t, u, "state=state123")
	assert.Contains(t, u, "scope=read%3Auser")
	assert.Contains(t, u, "response_type=code")
}

func TestGetOAuthConfigAdvertisesOnlyCompleteBuiltInProviders(t *testing.T) {
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_AUTH_URL", "")
	t.Setenv("OIDC_TOKEN_URL", "")
	t.Setenv("OIDC_USER_INFO_URL", "")
	assert.False(t, GetOAuthConfig("oidc").Enabled,
		"OIDC credentials without all three endpoints must remain hidden")

	t.Setenv("OIDC_AUTH_URL", "https://identity.example.test/authorize")
	t.Setenv("OIDC_TOKEN_URL", "https://identity.example.test/token")
	t.Setenv("OIDC_USER_INFO_URL", "https://identity.example.test/userinfo")
	cfg := GetOAuthConfig("oidc")
	require.True(t, cfg.Enabled)
	assert.Equal(t, "https://identity.example.test/authorize", cfg.AuthURL)
	assert.Equal(t, "https://identity.example.test/token", cfg.TokenURL)
	assert.Equal(t, "https://identity.example.test/userinfo", cfg.UserInfoURL)

	t.Setenv("GITHUB_CLIENT_ID", "client")
	t.Setenv("GITHUB_CLIENT_SECRET", "secret")
	assert.True(t, GetOAuthConfig("github").Enabled,
		"fixed built-in endpoints make a credentialed GitHub provider complete")
	t.Setenv("GITHUB_CLIENT_SECRET", "")
	assert.False(t, GetOAuthConfig("github").Enabled)
}

func TestGetOAuthConfigUsesAtomicOptionsAndDoesNotMixEnvironmentOverrides(t *testing.T) {
	for _, provider := range []string{"github", "discord", "linuxdo", "oidc"} {
		clearBuiltInOAuthEnvironment(t, provider)
	}
	initOAuthDB(t)
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GitHubClientIDOption:            "option-github-client",
		setting.GitHubClientSecretOption:        "option-github-secret",
		setting.GitHubOAuthEnabledOption:        "true",
		setting.DiscordClientIDOption:           "option-discord-client",
		setting.DiscordClientSecretOption:       "option-discord-secret",
		setting.DiscordOptionEnabled:            "true",
		setting.LinuxDOClientIDOption:           "option-linuxdo-client",
		setting.LinuxDOClientSecretOption:       "option-linuxdo-secret",
		setting.LinuxDOOAuthEnabledOption:       "true",
		setting.LinuxDOMinimumTrustLevelOption:  "3",
		setting.OIDCClientIDOption:              "option-oidc-client",
		setting.OIDCClientSecretOption:          "option-oidc-secret",
		setting.OIDCDisplayNameOption:           "Workforce SSO",
		setting.OIDCAuthorizationEndpointOption: "https://identity.example/authorize",
		setting.OIDCTokenEndpointOption:         "https://identity.example/token",
		setting.OIDCUserInfoEndpointOption:      "https://identity.example/userinfo",
		setting.OIDCOAuthEnabledOption:          "true",
	}))

	github := GetOAuthConfig("github")
	require.True(t, github.Enabled)
	assert.Equal(t, "option-github-client", github.ClientID)
	assert.Equal(t, "option-github-secret", github.ClientSecret)
	linuxDO := GetOAuthConfig("linuxdo")
	require.True(t, linuxDO.Enabled)
	assert.Equal(t, 3, linuxDO.MinimumTrustLevel)
	oidc := GetOAuthConfig("oidc")
	require.True(t, oidc.Enabled)
	assert.Equal(t, "Workforce SSO", oidc.DisplayName)
	assert.Equal(t, "https://identity.example/authorize", oidc.AuthURL)

	// Presence of any provider-specific deployment override selects one
	// complete environment configuration. It never fills a partial override
	// with a database secret.
	t.Setenv("GITHUB_CLIENT_ID", "environment-client-only")
	github = GetOAuthConfig("github")
	assert.False(t, github.Enabled)
	assert.Equal(t, "environment-client-only", github.ClientID)
	assert.Empty(t, github.ClientSecret)

	// A deployment-owned display-name override selects the whole OIDC
	// environment domain too; it cannot silently combine with DB credentials.
	t.Setenv("OIDC_DISPLAY_NAME", "Environment SSO")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	oidc = GetOAuthConfig("oidc")
	assert.False(t, oidc.Enabled)
	assert.Equal(t, "Environment SSO", oidc.DisplayName)
	assert.Empty(t, oidc.ClientID)

	clearBuiltInOAuthEnvironment(t, "github")
	clearBuiltInOAuthEnvironment(t, "oidc")
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GitHubClientSecretOption: "",
		setting.OIDCTokenEndpointOption:  "",
	}))
	assert.False(t, GetOAuthConfig("github").Enabled,
		"an incomplete option-backed provider must not be advertised")
	assert.False(t, GetOAuthConfig("oidc").Enabled,
		"OIDC must remain hidden until every explicit runtime endpoint is valid")
}

func TestLinuxDOTrustLevelIsEnforcedBeforeIdentityNormalization(t *testing.T) {
	allowOAuthLoopback(t)
	userinfo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"username":"member","trust_level":1}`))
	}))
	t.Cleanup(userinfo.Close)

	_, err := FetchUserInfo(&OAuthConfig{
		UserInfoURL: userinfo.URL, MinimumTrustLevel: 2,
	}, "linuxdo", &OAuthToken{AccessToken: "token"})
	assert.ErrorIs(t, err, ErrLinuxDOTrustLevel)

	user, err := FetchUserInfo(&OAuthConfig{
		UserInfoURL: userinfo.URL, MinimumTrustLevel: 1,
	}, "linuxdo", &OAuthToken{AccessToken: "token"})
	require.NoError(t, err)
	assert.Equal(t, "42", user.ProviderID)
}
