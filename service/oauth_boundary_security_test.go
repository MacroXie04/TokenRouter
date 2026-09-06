package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

func useProductionOAuthURLPolicy(t *testing.T) {
	t.Helper()
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	common.InitSSRF()
}

func TestOAuthURLAndRedirectBoundaries(t *testing.T) {
	useProductionOAuthURLPolicy(t)

	manyQuery := url.Values{}
	for index := 0; index <= maxOAuthEndpointQueryPairs; index++ {
		manyQuery.Set("p"+string(rune('a'+index)), "v")
	}
	invalid := []string{
		"http://identity.example.com/token",
		"https://user:secret@identity.example.com/token",
		"https://identity.example.com/token#fragment",
		"https://identity.example.com/a/../token",
		"https://identity.example.com/%5cadmin",
		"https://identity.example.com:65536/token",
		"https://identity.example.com/token?dup=1&dup=2",
		"https://identity.example.com/token?",
		"https://127.0.0.1/token",
		"https://identity.example.com/token?" + manyQuery.Encode(),
		"file:///tmp/token",
	}
	for _, raw := range invalid {
		_, err := validateOAuthURL(raw, maxOAuthEndpointBytes, true)
		assert.Error(t, err, raw)
	}

	redirect, err := BuildOAuthRedirectURI("https://console.example/base", "corp-sso")
	require.NoError(t, err)
	assert.Equal(t, "https://console.example/base/api/oauth/corp-sso/callback", redirect)
	for _, base := range []string{
		"http://console.example", "https://user:secret@console.example",
		"https://console.example?next=x", "https://console.example/#fragment",
	} {
		_, err := BuildOAuthRedirectURI(base, "corp-sso")
		assert.Error(t, err, base)
	}
	_, err = BuildOAuthRedirectURI("https://console.example", "Corp SSO")
	assert.Error(t, err)
	_, err = BuildOAuthDiscoveryURL("https://id.example/.well-known/openid-configuration", "https://id.example")
	assert.Error(t, err, "two discovery sources must be ambiguous")
	_, err = BuildOAuthDiscoveryURL("", "https://id.example?tenant=x")
	assert.Error(t, err, "issuer queries must not affect well-known path construction")

	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
	_, err = validateOAuthURL("http://localhost:8080/token", maxOAuthEndpointBytes, false)
	assert.NoError(t, err)
	_, err = validateOAuthURL("http://identity.example.com/token", maxOAuthEndpointBytes, false)
	assert.Error(t, err, "plaintext remains forbidden for non-loopback hosts in development")
}

func oauthBoundaryServer(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestOAuthTokenResponsesAreBoundedUnambiguousAndRedacted(t *testing.T) {
	allowOAuthLoopback(t)
	redirect := "http://localhost/callback"

	duplicate := oauthBoundaryServer(t, http.StatusOK,
		`{"access_token":"first","access_token":"second","token_type":"bearer"}`)
	_, err := ExchangeCode(&OAuthConfig{ClientID: "client", ClientSecret: "secret", TokenURL: duplicate}, "code", redirect)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "first")
	assert.NotContains(t, err.Error(), "second")

	oversized := oauthBoundaryServer(t, http.StatusOK,
		`{"access_token":"`+strings.Repeat("t", maxOAuthAccessTokenBytes+1)+`"}`)
	_, err = ExchangeCode(&OAuthConfig{ClientID: "client", ClientSecret: "secret", TokenURL: oversized}, "code", redirect)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), strings.Repeat("t", 32))

	marker := "upstream-secret-body-marker"
	rejectedURL := oauthBoundaryServer(t, http.StatusBadRequest, marker)
	cfg := &OAuthConfig{ClientID: "client", ClientSecret: "client-secret-marker", TokenURL: rejectedURL}
	_, err = ExchangeCode(cfg, "authorization-code-marker", redirect)
	require.EqualError(t, err, "OAuth token exchange was rejected")
	for _, secret := range []string{marker, cfg.ClientSecret, "authorization-code-marker", rejectedURL} {
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestOAuthUserInfoPreservesNumericSubjectsAndNormalizesProfile(t *testing.T) {
	allowOAuthLoopback(t)
	userinfo := oauthBoundaryServer(t, http.StatusOK,
		`{"id":9007199254740993,"login":"  Alice   Example  ","name":"  Alice   A  ","email":" USER@Example.COM "}`)
	user, err := FetchUserInfo(&OAuthConfig{UserInfoURL: userinfo}, "discord", &OAuthToken{AccessToken: "token"})
	require.NoError(t, err)
	assert.Equal(t, "9007199254740993", user.ProviderID)
	assert.Equal(t, "Alice Example", user.Username)
	assert.Equal(t, "Alice A", user.DisplayName)
	assert.Equal(t, "user@example.com", user.Email)

	longName := strings.Repeat("界", 40)
	userinfo = oauthBoundaryServer(t, http.StatusOK,
		`{"id":"subject","username":"`+longName+`","email":"not-an-address"}`)
	user, err = FetchUserInfo(&OAuthConfig{UserInfoURL: userinfo}, "discord", &OAuthToken{AccessToken: "token"})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(user.Username), maxOAuthUsernameBytes)
	assert.True(t, utf8.ValidString(user.Username))
	assert.Empty(t, user.Email, "an unverified malformed provider email is only a hint and must be dropped")

	for _, body := range []string{
		`{"id":"one","id":"two"}`,
		`{"id":"` + strings.Repeat("i", maxOAuthBuiltInSubjectBytes+1) + `"}`,
		`{"id":"subject","unknown":"` + strings.Repeat("x", maxOAuthJSONStringBytes+1) + `"}`,
	} {
		userinfo = oauthBoundaryServer(t, http.StatusOK, body)
		_, err = FetchUserInfo(&OAuthConfig{UserInfoURL: userinfo}, "discord", &OAuthToken{AccessToken: "token"})
		assert.Error(t, err)
	}
}

func TestOAuthIdentityNormalizationOccursBeforePersistence(t *testing.T) {
	initOAuthDB(t)
	providerUser := &ProviderUser{
		ProviderID:  "  normalized-subject  ",
		Username:    strings.Repeat("界", 40),
		DisplayName: "  Display    Name  ",
		Email:       " PERSON@Example.COM ",
	}
	user, created, err := LoginOrBindUser("oidc", providerUser)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "normalized-subject", user.OidcId)
	assert.LessOrEqual(t, len(user.Username), maxOAuthUsernameBytes)
	assert.True(t, utf8.ValidString(user.Username))
	assert.Equal(t, "Display Name", user.DisplayName)
	assert.Equal(t, "person@example.com", user.Email)

	again, created, err := LoginOrBindUser("oidc", &ProviderUser{ProviderID: "normalized-subject"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, user.Id, again.Id)

	_, _, err = LoginOrBindUser("oidc", &ProviderUser{ProviderID: strings.Repeat("s", maxOAuthBuiltInSubjectBytes+1)})
	assert.Error(t, err)
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestProviderLoginFlowRollsBackStateAndIdentityTogether(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Option{})
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login"}

	injected := errors.New("injected identity claim failure")
	const callback = "test:oauth-flow-claim-failure"
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "external_identity_claims" {
			tx.AddError(injected)
		}
	}))
	_, _, _, err = ConsumeProviderLoginFlow(token, match, "github", &ProviderUser{
		ProviderID: "flow-subject", Username: "flow-user",
	}, "")
	require.ErrorIs(t, err, injected)
	require.NoError(t, db.Callback().Create().Remove(callback))
	assertOAuthFlowStillPending(t, token)
	var users, claims int64
	require.NoError(t, db.Model(&model.User{}).Count(&users).Error)
	require.NoError(t, db.Model(&model.ExternalIdentityClaim{}).Count(&claims).Error)
	assert.Zero(t, users)
	assert.Zero(t, claims)

	_, user, created, err := ConsumeProviderLoginFlow(token, match, "github", &ProviderUser{
		ProviderID: "flow-subject", Username: "flow-user",
	}, "")
	require.NoError(t, err)
	assert.True(t, created)
	require.NotNil(t, user)
	_, err = PeekAuthFlow(token, AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, ErrInvalidFlowToken)
}

func assertOAuthFlowStillPending(t *testing.T, token string) {
	t.Helper()
	flow, err := PeekAuthFlow(token, AuthFlowPurposeOAuth)
	require.NoError(t, err)
	assert.Nil(t, flow.ConsumedAt)
}

func TestProviderBindFlowConsumesTakenClaimAndRevalidatesSession(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Option{})
	owner := model.User{Username: "claim-owner", Password: "password", Status: model.UserStatusEnabled, AuthVersion: 1}
	actor := model.User{Username: "bind-actor", Password: "password", Status: model.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&actor).Error)
	require.NoError(t, BindProviderToUser("github", &ProviderUser{ProviderID: "taken-subject"}, owner.Id))
	sid, _, err := CreateSession(&actor, "127.0.0.1", "test", "test")
	require.NoError(t, err)

	takenToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "bind", actor.Id, sid, "", time.Minute)
	require.NoError(t, err)
	match := AuthFlowMatch{
		Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "bind", UserId: actor.Id, SessionId: sid,
	}
	_, err = ConsumeProviderBindFlow(takenToken, match, "github", &ProviderUser{ProviderID: "taken-subject"})
	assert.ErrorIs(t, err, ErrBindingTaken)
	_, err = PeekAuthFlow(takenToken, AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, ErrInvalidFlowToken, "a terminal ownership conflict must consume the proof")

	revokedToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "bind", actor.Id, sid, "", time.Minute)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", sid).
		Updates(map[string]any{"status": SessionStatusRevoked, "revoked_at": time.Now().Unix()}).Error)
	_, err = ConsumeProviderBindFlow(revokedToken, match, "github", &ProviderUser{ProviderID: "fresh-subject"})
	assert.ErrorIs(t, err, ErrSessionRevoked)
	assertOAuthFlowStillPending(t, revokedToken)
	var reloaded model.User
	require.NoError(t, db.First(&reloaded, actor.Id).Error)
	assert.Empty(t, reloaded.GitHubId)
}

func TestCustomProviderLoginFlowCommitsBindingWithState(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.AuthFlow{}, &model.CustomOAuthProvider{}, &model.UserOAuthBinding{}, &model.Option{})
	provider := model.CustomOAuthProvider{
		Name: "Boundary SSO", Slug: "boundary-sso", Enabled: true,
		ClientId: "client", ClientSecret: "secret",
		AuthorizationEndpoint: "https://identity.example/authorize",
		TokenEndpoint:         "https://identity.example/token",
		UserInfoEndpoint:      "https://identity.example/userinfo",
	}
	require.NoError(t, model.CreateCustomOAuthProvider(&provider))
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, provider.Slug, "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: provider.Slug, Intent: "login"}
	_, user, created, err := ConsumeProviderLoginFlow(token, match, provider.Slug, &ProviderUser{
		ProviderID: " custom-subject ", CustomProviderId: provider.Id,
	}, "")
	require.NoError(t, err)
	assert.True(t, created)
	require.NotNil(t, user)
	binding, err := model.GetUserByOAuthBinding(provider.Id, "custom-subject")
	require.NoError(t, err)
	assert.Equal(t, user.Id, binding.Id)
	_, err = PeekAuthFlow(token, AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, ErrInvalidFlowToken)

	require.NoError(t, db.Model(&model.CustomOAuthProvider{}).Where("id = ?", provider.Id).Update("enabled", false).Error)
	_, _, err = LoginOrBindUser(provider.Slug, &ProviderUser{ProviderID: "other", CustomProviderId: provider.Id})
	assert.Error(t, err, "disabled custom providers cannot mint or resolve login identities")
}

func TestProviderLoginFlowEnforcesRegistrationGateAtomically(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Option{})
	existing := model.User{
		Username: "existing-oauth-user", Password: "", Status: model.UserStatusEnabled,
		AuthVersion: 1, GitHubId: "existing-subject",
	}
	require.NoError(t, db.Create(&existing).Error)
	require.NoError(t, model.ClaimExternalIdentityWithTx(db, "github", "existing-subject", existing.Id))
	require.NoError(t, db.Create(&model.Option{Key: setting.RegistrationEnabledOption, Value: "false"}).Error)

	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login"}
	newToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	_, _, _, err = ConsumeProviderLoginFlow(newToken, match, "github", &ProviderUser{
		ProviderID: "new-subject", Username: "new-user",
	}, "")
	assert.ErrorIs(t, err, ErrRegistrationDisabled)
	assertOAuthFlowStillPending(t, newToken)
	var users int64
	require.NoError(t, db.Model(&model.User{}).Count(&users).Error)
	assert.EqualValues(t, 1, users)
	_, err = model.FindExternalIdentityClaimWithTx(db, "github", "new-subject")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Disabling registration never locks out an identity that already owns an
	// account; its state still consumes exactly once.
	existingToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	_, owner, created, err := ConsumeProviderLoginFlow(existingToken, match, "github", &ProviderUser{
		ProviderID: "existing-subject",
	}, "")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, existing.Id, owner.Id)
	_, err = PeekAuthFlow(existingToken, AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, ErrInvalidFlowToken)

	// Re-enabling registration makes the previously rolled-back ceremony
	// retryable without creating a partial account or identity claim.
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", setting.RegistrationEnabledOption).
		Update("value", "true").Error)
	_, owner, created, err = ConsumeProviderLoginFlow(newToken, match, "github", &ProviderUser{
		ProviderID: "new-subject", Username: "new-user",
	}, "")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "new-subject", owner.GitHubId)
}

func TestProviderLoginDefaultTokenEntropyIsNewUserOnlyAndFlowAtomic(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.Token{}, &model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Option{})
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.InitialQuotaOption:        "900",
		setting.QuotaForNewUserOption:     "41",
		setting.DefaultUseAutoGroupOption: "true",
		setting.DefaultGroupOption:        "oauth-default",
	}))

	existing := model.User{
		Username: "existing-default-token-user", Status: model.UserStatusEnabled,
		AuthVersion: 1, GitHubId: "existing-token-subject", Quota: 77,
	}
	require.NoError(t, db.Create(&existing).Error)
	require.NoError(t, model.ClaimExternalIdentityWithTx(db, "github", "existing-token-subject", existing.Id))
	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login"}
	existingFlow, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	newFlow, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)

	restoreEntropy := common.SetSecureRandomReaderForTesting(entropyFailureReader{})
	t.Cleanup(restoreEntropy)
	_, owner, created, err := ConsumeProviderLoginFlow(existingFlow, match, "github", &ProviderUser{
		ProviderID: "existing-token-subject",
	}, "")
	require.NoError(t, err, "existing-user login must not depend on default-token entropy")
	assert.False(t, created)
	assert.Equal(t, existing.Id, owner.Id)

	_, _, _, err = ConsumeProviderLoginFlow(newFlow, match, "github", &ProviderUser{
		ProviderID: "new-token-subject", Username: "new-default-token-user",
	}, "")
	require.ErrorIs(t, err, ErrDefaultTokenEntropy)
	require.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assertOAuthFlowStillPending(t, newFlow)
	var userCount, tokenCount int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "new-default-token-user").Count(&userCount).Error)
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, userCount)
	assert.Zero(t, tokenCount)
	_, err = model.FindExternalIdentityClaimWithTx(db, "github", "new-token-subject")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	restoreEntropy()
	flow, owner, created, err := ConsumeProviderLoginFlow(newFlow, match, "github", &ProviderUser{
		ProviderID: "new-token-subject", Username: "new-default-token-user",
	}, "")
	require.NoError(t, err)
	assert.True(t, created)
	assert.NotNil(t, flow.ConsumedAt)
	assert.Equal(t, 41, owner.Quota)
	assert.Equal(t, "oauth-default", owner.Group)
	var token model.Token
	require.NoError(t, db.Where("user_id = ?", owner.Id).First(&token).Error)
	assert.Equal(t, GroupAuto, token.Group)
	assert.True(t, token.UnlimitedQuota)
}

func TestProviderLoginDefaultTokenInsertFailureRollsBackFlowUserAndIdentity(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.Token{}, &model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Option{})
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.Init())
	flowToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER reject_oauth_default_token
		BEFORE INSERT ON tokens
		BEGIN
			SELECT RAISE(ABORT, 'forced-default-token-failure');
		END
	`).Error)

	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login"}
	_, _, _, err = ConsumeProviderLoginFlow(flowToken, match, "github", &ProviderUser{
		ProviderID: "atomic-token-subject", Username: "atomic-token-user",
	}, "")
	require.Error(t, err)
	assertOAuthFlowStillPending(t, flowToken)
	var userCount, tokenCount int64
	require.NoError(t, db.Model(&model.User{}).Count(&userCount).Error)
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	assert.Zero(t, userCount)
	assert.Zero(t, tokenCount)
	_, claimErr := model.FindExternalIdentityClaimWithTx(db, "github", "atomic-token-subject")
	assert.ErrorIs(t, claimErr, gorm.ErrRecordNotFound)

	require.NoError(t, db.Exec("DROP TRIGGER reject_oauth_default_token").Error)
	_, owner, created, err := ConsumeProviderLoginFlow(flowToken, match, "github", &ProviderUser{
		ProviderID: "atomic-token-subject", Username: "atomic-token-user",
	}, "")
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, db.Model(&model.Token{}).Where("user_id = ?", owner.Id).Count(&tokenCount).Error)
	assert.EqualValues(t, 1, tokenCount)
}

func TestProviderLoginIdentityFailureAfterDefaultTokenRollsBackEverything(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.Token{}, &model.AuthFlow{}, &model.ExternalIdentityClaim{}, &model.Option{})
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.Init())
	flowToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)

	forced := errors.New("forced post-token identity failure")
	tokenInserted := false
	const observeToken = "test:observe_oauth_default_token"
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(observeToken, func(tx *gorm.DB) {
		if tx.Statement.Table == "tokens" {
			tokenInserted = true
		}
	}))
	const rejectIdentity = "test:reject_identity_after_default_token"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(rejectIdentity, func(tx *gorm.DB) {
		if tx.Statement.Table == "external_identity_claims" && tokenInserted {
			tx.AddError(forced)
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(observeToken)
		_ = db.Callback().Create().Remove(rejectIdentity)
	})

	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login"}
	_, _, _, err = ConsumeProviderLoginFlow(flowToken, match, "github", &ProviderUser{
		ProviderID: "post-token-failure-subject", Username: "post-token-failure-user",
	}, "")
	require.ErrorIs(t, err, forced)
	assert.True(t, tokenInserted, "failure must be injected only after the token insert was attempted")
	assertOAuthFlowStillPending(t, flowToken)
	var userCount, tokenCount, claimCount int64
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "post-token-failure-user").Count(&userCount).Error)
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	require.NoError(t, db.Model(&model.ExternalIdentityClaim{}).Count(&claimCount).Error)
	assert.Zero(t, userCount)
	assert.Zero(t, tokenCount)
	assert.Zero(t, claimCount)

	require.NoError(t, db.Callback().Create().Remove(rejectIdentity))
	require.NoError(t, db.Callback().Create().Remove(observeToken))
	_, owner, created, err := ConsumeProviderLoginFlow(flowToken, match, "github", &ProviderUser{
		ProviderID: "post-token-failure-subject", Username: "post-token-failure-user",
	}, "")
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, db.Model(&model.Token{}).Where("user_id = ?", owner.Id).Count(&tokenCount).Error)
	assert.EqualValues(t, 1, tokenCount)
	_, err = model.FindExternalIdentityClaimWithTx(db, "github", "post-token-failure-subject")
	require.NoError(t, err)
}
