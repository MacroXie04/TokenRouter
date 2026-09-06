package store_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	model "github.com/tokenrouter/tokenrouter/internal/store"
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
		{func(p *model.CustomOAuthProvider) { p.ClientSecret = "" }, "client secret is required"},
		{func(p *model.CustomOAuthProvider) { p.ClientId = " client" }, "client ID is invalid"},
		{func(p *model.CustomOAuthProvider) { p.AuthorizationEndpoint = "" }, "authorization endpoint is required"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "" }, "token endpoint is required"},
		{func(p *model.CustomOAuthProvider) { p.UserInfoEndpoint = "" }, "user info endpoint is required"},
		{func(p *model.CustomOAuthProvider) { p.AccessPolicy = "{not json" }, "access_policy must be valid JSON"},
		{func(p *model.CustomOAuthProvider) {
			p.AccessPolicy = `{"logic":"xor","conditions":[{"field":"x","op":"eq","value":1}]}`
		},
			"access_policy is invalid: unsupported logic: xor"},
		{func(p *model.CustomOAuthProvider) { p.Name = strings.Repeat("n", 65) },
			"provider name exceeds safe limits"},
		{func(p *model.CustomOAuthProvider) { p.Slug = strings.Repeat("s", 65) },
			"provider slug exceeds safe limits"},
		{func(p *model.CustomOAuthProvider) { p.ClientId = strings.Repeat("c", 257) },
			"client ID exceeds safe limits"},
		{func(p *model.CustomOAuthProvider) { p.ClientSecret = strings.Repeat("s", 513) },
			"client secret exceeds safe limits"},
		{func(p *model.CustomOAuthProvider) { p.AuthorizationEndpoint = "http://sso.example.com/authorize" },
			"authorization endpoint must use HTTPS except on loopback"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "javascript:alert(1)" },
			"token endpoint must be an absolute HTTP(S) URL"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "https://user:secret@sso.example.com/token" },
			"token endpoint must be an absolute HTTP(S) URL"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "https://sso.example.com/token#secret" },
			"token endpoint must be an absolute HTTP(S) URL"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "https://sso.example.com/token?a=1&a=2" },
			"token endpoint has an unsafe or duplicate query parameter"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "https://sso.example.com/a/../token" },
			"token endpoint has an ambiguous path"},
		{func(p *model.CustomOAuthProvider) { p.TokenEndpoint = "https://sso.example.com:65536/token" },
			"token endpoint has an invalid port"},
		{func(p *model.CustomOAuthProvider) { p.AuthStyle = 3 }, "auth style is invalid"},
		{func(p *model.CustomOAuthProvider) { p.UserIdField = strings.Repeat("f", 129) },
			"user ID field exceeds safe limits"},
		{func(p *model.CustomOAuthProvider) { p.UserIdField = "sub|@pretty" },
			"user ID field must be a simple JSON field path"},
		{func(p *model.CustomOAuthProvider) {
			p.AccessPolicy = `{"conditions":[{"field":"groups.#(admin)","op":"exists"}]}`
		}, "access_policy is invalid: condition[0].field must be a simple JSON field path"},
		{func(p *model.CustomOAuthProvider) { p.Scopes = strings.Repeat("scope ", 33) },
			"scopes exceed safe limits"},
		{func(p *model.CustomOAuthProvider) { p.AccessDeniedMessage = strings.Repeat("m", 513) },
			"access denied message exceeds safe limits"},
		{func(p *model.CustomOAuthProvider) { p.AccessPolicy = strings.Repeat(" ", 64<<10) + "x" },
			"access_policy exceeds safe limits"},
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

func TestAccessPolicyComplexityAndValueBounds(t *testing.T) {
	leaf := model.AccessPolicyDocument{Conditions: []model.AccessPolicyCondition{{
		Field: "membership.plan", Op: "eq", Value: "pro",
	}}}
	deep := leaf
	for range 16 {
		deep = model.AccessPolicyDocument{Groups: []model.AccessPolicyDocument{deep}}
	}
	err := model.ValidateAccessPolicyDocument(&deep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum nesting depth")

	many := model.AccessPolicyDocument{Conditions: make([]model.AccessPolicyCondition, 1025)}
	err = model.ValidateAccessPolicyDocument(&many)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum entry count")

	largeSet := make([]any, 257)
	for index := range largeSet {
		largeSet[index] = index
	}
	policy := model.AccessPolicyDocument{Conditions: []model.AccessPolicyCondition{{
		Field: "teams", Op: "in", Value: largeSet,
	}}}
	err = model.ValidateAccessPolicyDocument(&policy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value exceeds safe limits")

	policy = model.AccessPolicyDocument{Conditions: []model.AccessPolicyCondition{{
		Field: "claim", Op: "eq", Value: strings.Repeat("v", 4097),
	}}}
	err = model.ValidateAccessPolicyDocument(&policy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value exceeds safe limits")

	_, err = model.ParseAccessPolicy(strings.Repeat(" ", 64<<10) + "x")
	assert.EqualError(t, err, "access_policy exceeds safe limits")
}

func TestEnabledAndLoginLookupRejectUnsafeLegacyCustomOAuthProvider(t *testing.T) {
	newCustomOAuthDB(t)
	unsafe := validProvider()
	unsafe.Enabled = true
	unsafe.AuthorizationEndpoint = "http://identity.example.com/authorize"
	require.NoError(t, model.DB.Session(&gorm.Session{SkipHooks: true}).Create(unsafe).Error,
		"the fixture represents a legacy row written before validation existed")

	providers, err := model.GetEnabledCustomOAuthProviders()
	require.Error(t, err)
	assert.Nil(t, providers)
	assert.Contains(t, err.Error(), "must use HTTPS except on loopback")

	provider, err := model.GetCustomOAuthProviderBySlug(unsafe.Slug)
	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "must use HTTPS except on loopback")
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
	err = model.CreateUserOAuthBinding(&model.UserOAuthBinding{
		UserId: 1, ProviderId: p.Id, ProviderUserId: strings.Repeat("x", 257),
	})
	assert.EqualError(t, err, "provider user ID exceeds safe limits")
	err = model.CreateUserOAuthBinding(&model.UserOAuthBinding{
		UserId: 1, ProviderId: p.Id, ProviderUserId: "unsafe\nsubject",
	})
	assert.EqualError(t, err, "provider user ID exceeds safe limits")

	// A binding for user 1; the same identity is then taken for user 2.
	require.NoError(t, model.CreateUserOAuthBinding(&model.UserOAuthBinding{
		UserId: 1, ProviderId: p.Id, ProviderUserId: " ext-1 ",
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
