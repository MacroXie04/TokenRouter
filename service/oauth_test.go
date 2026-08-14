package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func initOAuthDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserOAuthBinding{}))
	model.DB = db
	model.LOG_DB = db
}

func newMockOAuthServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
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

func TestBuildAuthorizationURL(t *testing.T) {
	cfg := &OAuthConfig{ClientID: "cid", AuthURL: "https://github.com/login/oauth/authorize", Scopes: []string{"read:user"}}
	u, err := BuildAuthorizationURL(cfg, "state123", "http://localhost/callback")
	require.NoError(t, err)
	assert.Contains(t, u, "client_id=cid")
	assert.Contains(t, u, "state=state123")
	assert.Contains(t, u, "scope=read%3Auser")
	assert.Contains(t, u, "response_type=code")
}
