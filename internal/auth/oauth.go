package auth

import (
	"errors"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

// OAuthConfig holds the OAuth2 code-flow configuration for a provider.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	DisplayName  string
	// MinimumTrustLevel is enforced for Linux DO user-info responses.
	MinimumTrustLevel int
	Scopes            []string
	Enabled           bool
	Known             bool
	// Custom is set when the provider is a DB-configured custom provider
	// (resolved by slug); nil for the built-in env-configured providers.
	Custom *model.CustomOAuthProvider
}

// OAuthToken is the outcome of the code exchange. TokenType matters for
// custom providers whose userinfo endpoint expects a non-Bearer scheme.
type OAuthToken struct {
	AccessToken string
	TokenType   string
}

// ProviderUser is the normalized identity returned by a provider's userinfo.
type ProviderUser struct {
	ProviderID       string
	LegacyProviderID string
	Username         string
	Email            string
	DisplayName      string
	// CustomProviderId is the CustomOAuthProvider row id when the identity
	// came from a custom provider; 0 for built-ins. It decides whether the
	// binding lives in a users column or in user_oauth_bindings.
	CustomProviderId int
}

// ErrRegistrationDisabled is returned only when an external identity has no
// existing owner and global registration is disabled. The check happens in
// the same database transaction as account creation and identity claiming.
var ErrRegistrationDisabled = errors.New("registration is disabled")

// ErrLinuxDOTrustLevel is returned before identity persistence when the
// provider's asserted trust level is absent or below the configured minimum.
var ErrLinuxDOTrustLevel = errors.New("Linux DO trust level is below the configured minimum")

const (
	maxOAuthClientIDBytes           = 256
	maxOAuthClientSecretBytes       = 4096
	maxOAuthEndpointBytes           = 2048
	maxOAuthAuthorizationURLBytes   = 16 << 10
	maxOAuthRedirectURIBytes        = 2048
	maxOAuthAuthorizationCodeBytes  = 4096
	maxOAuthStateBytes              = 128
	maxOAuthAccessTokenBytes        = 16 << 10
	maxOAuthTokenTypeBytes          = 32
	maxOAuthScopeCount              = 32
	maxOAuthScopeBytes              = 128
	maxOAuthEndpointQueryPairs      = 16
	maxOAuthEndpointQueryKeyBytes   = 128
	maxOAuthEndpointQueryValueBytes = 2048
	maxOAuthBuiltInSubjectBytes     = 64
	maxOAuthCustomSubjectBytes      = 256
	maxOAuthUsernameBytes           = 20
	maxOAuthDisplayNameBytes        = 20
	maxOAuthProviderSlugBytes       = 64
	maxOAuthJSONDepth               = 16
	maxOAuthJSONNodes               = 4096
	maxOAuthJSONObjectItems         = 256
	maxOAuthJSONArrayItems          = 256
	maxOAuthJSONKeyBytes            = 128
	maxOAuthJSONStringBytes         = 16 << 10
	maxOAuthJSONNumberBytes         = 128
)

// ErrBindingTaken is returned when a provider identity is already bound to a
// different account (built-in column or custom binding row).
var ErrBindingTaken = errors.New("该账号已绑定其他用户")
