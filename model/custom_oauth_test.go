package model_test

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func newCustomOAuthDB(t *testing.T) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "custom_oauth.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.CustomOAuthProvider{}, &model.UserOAuthBinding{}))
	model.DB = db
}

func validProvider() *model.CustomOAuthProvider {
	return &model.CustomOAuthProvider{
		Name: "Corp SSO", Slug: "corp-sso",
		ClientId: "cid", ClientSecret: "sec",
		AuthorizationEndpoint: "https://sso.example.com/authorize",
		TokenEndpoint:         "https://sso.example.com/token",
		UserInfoEndpoint:      "https://sso.example.com/userinfo",
	}
}

func TestCustomOAuthProviderValidation(t *testing.T) {
	newCustomOAuthDB(t)

	// Each required field produces its exact reference error string.
	cases := []struct {
		mutate func(*model.CustomOAuthProvider)
		want   string
	}{
		{func(p *model.CustomOAuthProvider) { p.Name = "" }, "provider name is required"},
		{func(p *model.CustomOAuthProvider) { p.Slug = "" }, "provider slug is required"},
		{func(p *model.CustomOAuthProvider) { p.Slug = "bad slug!" },
			"provider slug must contain only lowercase letters, numbers, and hyphens"},
		{func(p *model.CustomOAuthProvider) { p.ClientId = "" }, "client ID is required"},
		{func(p *model.CustomOAuthProvider) { p.AuthorizationEndpoint = "" }, "authorization endpoint is required"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "" }, "token endpoint is required"},
		{func(p *model.CustomOAuthProvider) { p.UserInfoEndpoint = "" }, "user info endpoint is required"},
		{func(p *model.CustomOAuthProvider) { p.AccessPolicy = "{not json" }, "access_policy must be valid JSON"},
		{func(p *model.CustomOAuthProvider) { p.AccessPolicy = `{"logic":"xor","conditions":[{"field":"x","op":"eq","value":1}]}` },
			"access_policy is invalid: unsupported logic: xor"},
	}
	for _, tc := range cases {
		p := validProvider()
		tc.mutate(p)
		err := model.CreateCustomOAuthProvider(p)
		require.Error(t, err)
		assert.Equal(t, tc.want, err.Error())
	}

	// A valid provider persists with lowercased slug and mapping defaults.
	p := validProvider()
	p.Slug = "Corp-SSO"
	require.NoError(t, model.CreateCustomOAuthProvider(p))
	stored, err := model.GetCustomOAuthProviderById(p.Id)
	require.NoError(t, err)
	assert.Equal(t, "corp-sso", stored.Slug)
	assert.Equal(t, "sub", stored.UserIdField)
	assert.Equal(t, "preferred_username", stored.UsernameField)
	assert.Equal(t, "name", stored.DisplayNameField)
	assert.Equal(t, "email", stored.EmailField)
	assert.Equal(t, "openid profile email", stored.Scopes)

	// Slug-taken checks respect the exclusion id.
	assert.True(t, model.IsCustomOAuthSlugTaken("corp-sso", 0))
	assert.False(t, model.IsCustomOAuthSlugTaken("corp-sso", p.Id))
	assert.False(t, model.IsCustomOAuthSlugTaken("other", 0))
}

func TestAccessPolicyDocumentValidation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"unsupported logic", `{"logic":"xor","conditions":[{"field":"a","op":"eq","value":1}]}`,
			"unsupported logic: xor"},
		{"empty tree", `{"logic":"and"}`,
			"policy requires at least one condition or group"},
		{"missing field", `{"conditions":[{"field":"  ","op":"eq","value":1}]}`,
			"condition[0].field is required"},
		{"unsupported op", `{"conditions":[{"field":"a","op":"matches","value":1}]}`,
			"condition[0].op is unsupported: matches"},
		{"in requires array", `{"conditions":[{"field":"a","op":"in","value":"x"}]}`,
			"condition[0].value must be an array for op in"},
		{"group error wrapped", `{"groups":[{"conditions":[{"field":"a","op":"nope","value":1}]}]}`,
			"group[0]: condition[0].op is unsupported: nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := model.ParseAccessPolicy(tc.raw)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
		})
	}

	// Normalization: logic/op lowercased, fields trimmed, empty logic → and.
	policy, err := model.ParseAccessPolicy(`{"logic":"OR","conditions":[{"field":" plan ","op":"EQ","value":"pro"}]}`)
	require.NoError(t, err)
	assert.Equal(t, "or", policy.Logic)
	assert.Equal(t, "plan", policy.Conditions[0].Field)
	assert.Equal(t, "eq", policy.Conditions[0].Op)
	policy, err = model.ParseAccessPolicy(`{"conditions":[{"field":"a","op":"exists"}]}`)
	require.NoError(t, err)
	assert.Equal(t, "and", policy.Logic)
}

func TestUserOAuthBindingLifecycle(t *testing.T) {
	newCustomOAuthDB(t)
	p := validProvider()
	require.NoError(t, model.CreateCustomOAuthProvider(p))

	// Validation strings.
	err := model.CreateUserOAuthBinding(&model.UserOAuthBinding{ProviderId: p.Id, ProviderUserId: "x"})
	assert.EqualError(t, err, "user ID is required")
	err = model.CreateUserOAuthBinding(&model.UserOAuthBinding{UserId: 1, ProviderUserId: "x"})
	assert.EqualError(t, err, "provider ID is required")
	err = model.CreateUserOAuthBinding(&model.UserOAuthBinding{UserId: 1, ProviderId: p.Id})
	assert.EqualError(t, err, "provider user ID is required")

	// A binding for user 1; the same identity is then taken for user 2.
	require.NoError(t, model.CreateUserOAuthBinding(&model.UserOAuthBinding{
		UserId: 1, ProviderId: p.Id, ProviderUserId: "ext-1",
	}))
	err = model.CreateUserOAuthBinding(&model.UserOAuthBinding{
		UserId: 2, ProviderId: p.Id, ProviderUserId: "ext-1",
	})
	assert.ErrorIs(t, err, model.ErrOAuthBindingTaken)

	// Update: rebinding user 1 to a new identity replaces the row; stealing
	// user 1's identity for user 2 fails; a missing row is created.
	require.NoError(t, model.UpdateUserOAuthBinding(1, p.Id, "ext-1b"))
	assert.ErrorIs(t, model.UpdateUserOAuthBinding(2, p.Id, "ext-1b"), model.ErrOAuthBindingTaken)
	require.NoError(t, model.UpdateUserOAuthBinding(2, p.Id, "ext-2"))
	count, err := model.GetBindingCountByProviderId(p.Id)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	// Lookup by identity resolves the bound user (user rows 1/2 must exist).
	require.NoError(t, model.DB.Create(&model.User{Username: "u1", Password: "x", Status: model.UserStatusEnabled, AuthVersion: 1}).Error)
	got, err := model.GetUserByOAuthBinding(p.Id, "ext-1b")
	require.NoError(t, err)
	assert.Equal(t, 1, got.Id)

	// Deleting the provider removes its bindings too.
	require.NoError(t, model.DeleteCustomOAuthProvider(p.Id))
	count, err = model.GetBindingCountByProviderId(p.Id)
	require.NoError(t, err)
	assert.Zero(t, count)
}
