package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

// OAuthConfig holds the OAuth2 code-flow configuration for a provider.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	Scopes       []string
	Enabled      bool
	Known        bool
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
	ProviderID  string
	Username    string
	Email       string
	DisplayName string
	// CustomProviderId is the CustomOAuthProvider row id when the identity
	// came from a custom provider; 0 for built-ins. It decides whether the
	// binding lives in a users column or in user_oauth_bindings.
	CustomProviderId int
}

// GetOAuthConfig builds the configuration for a named provider from
// environment/settings. Supported: github, discord, oidc, linuxdo, plus
// DB-configured custom providers resolved by slug.
func GetOAuthConfig(provider string) *OAuthConfig {
	prefix := strings.ToUpper(provider)
	cfg := &OAuthConfig{
		ClientID:     common.GetEnv(prefix+"_CLIENT_ID", ""),
		ClientSecret: common.GetEnv(prefix+"_CLIENT_SECRET", ""),
		AuthURL:      common.GetEnv(prefix+"_AUTH_URL", defaultAuthURL(provider)),
		TokenURL:     common.GetEnv(prefix+"_TOKEN_URL", defaultTokenURL(provider)),
		UserInfoURL:  common.GetEnv(prefix+"_USER_INFO_URL", defaultUserInfoURL(provider)),
	}
	// The standard OAuth providers (non-standard Telegram/WeChat flows have
	// their own endpoints and are not state-ceremony providers).
	switch provider {
	case "github", "discord", "oidc", "linuxdo":
		cfg.Known = true
	default:
		if custom := resolveCustomOAuthConfig(provider); custom != nil {
			return custom
		}
	}
	cfg.Scopes = common.GetEnvStrings(prefix + "_SCOPES")
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = defaultScopes(provider)
	}
	cfg.Enabled = cfg.ClientID != "" && cfg.ClientSecret != ""
	return cfg
}

func defaultAuthURL(p string) string {
	switch p {
	case "github":
		return "https://github.com/login/oauth/authorize"
	case "discord":
		return "https://discord.com/oauth2/authorize"
	case "linuxdo":
		return "https://connect.linux.do/oauth2/authorize"
	default: // oidc
		return ""
	}
}

func defaultTokenURL(p string) string {
	switch p {
	case "github":
		return "https://github.com/login/oauth/access_token"
	case "discord":
		return "https://discord.com/api/oauth2/token"
	case "linuxdo":
		return common.GetEnv("LINUX_DO_TOKEN_ENDPOINT", "https://connect.linux.do/oauth2/token")
	default:
		return ""
	}
}

func defaultUserInfoURL(p string) string {
	switch p {
	case "github":
		return "https://api.github.com/user"
	case "discord":
		return "https://discord.com/api/users/@me"
	case "linuxdo":
		return common.GetEnv("LINUX_DO_USER_ENDPOINT", "https://connect.linux.do/api/user")
	default:
		return ""
	}
}

func defaultScopes(p string) []string {
	switch p {
	case "github":
		return []string{"read:user", "user:email"}
	case "discord":
		return []string{"identify", "email"}
	default:
		return []string{"openid", "email", "profile"}
	}
}

// BuildAuthorizationURL returns the provider's authorization URL for a state.
func BuildAuthorizationURL(cfg *OAuthConfig, state, redirectURI string) (string, error) {
	if cfg.AuthURL == "" {
		return "", errors.New("provider authorization endpoint not configured")
	}
	u, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

var oauthHTTPClient = &http.Client{Timeout: 15 * time.Second}

// ExchangeCode exchanges an authorization code for an access token.
func ExchangeCode(cfg *OAuthConfig, code, redirectURI string) (*OAuthToken, error) {
	if cfg.Custom != nil {
		return customExchangeCode(cfg, code, redirectURI)
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequest(http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return nil, errors.New("token exchange returned no access_token")
	}
	return &OAuthToken{AccessToken: tokenResp.AccessToken, TokenType: tokenResp.TokenType}, nil
}

// FetchUserInfo fetches and normalizes the provider identity for an access token.
func FetchUserInfo(cfg *OAuthConfig, provider string, token *OAuthToken) (*ProviderUser, error) {
	if cfg.Custom != nil {
		return customFetchUserInfo(cfg, token)
	}
	req, err := http.NewRequest(http.MethodGet, cfg.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("userinfo failed: %s", string(body))
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	pu := &ProviderUser{
		ProviderID:  firstProviderID(raw),
		Username:    firstString(raw, "preferred_username", "login", "username"),
		Email:       firstString(raw, "email"),
		DisplayName: firstString(raw, "name", "display_name", "login"),
	}
	if pu.ProviderID == "" {
		return nil, errors.New("provider user has no id/sub")
	}
	if pu.Username == "" {
		pu.Username = pu.ProviderID
	}
	if pu.DisplayName == "" {
		pu.DisplayName = pu.Username
	}
	return pu, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// firstProviderID extracts the provider subject, handling string (OIDC "sub")
// and numeric (GitHub/Discord "id") shapes.
func firstProviderID(m map[string]any) string {
	if v, ok := m["sub"].(string); ok && v != "" {
		return v
	}
	if v, ok := m["id"].(string); ok && v != "" {
		return v
	}
	switch v := m["id"].(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

// LoginOrBindUser finds an existing user bound to the provider identity, or
// creates a new user and binds the identity (via the user's provider-scoped id
// field for built-in providers, and UserOAuthBinding for custom providers).
func LoginOrBindUser(provider string, pu *ProviderUser) (*model.User, bool, error) {
	return LoginOrBindUserWithAff(provider, pu, "")
}

// LoginOrBindUserWithAff finds an existing user by provider identity or creates
// a new one, applying the optional affiliate code as the inviter.
func LoginOrBindUserWithAff(provider string, pu *ProviderUser, affCode string) (*model.User, bool, error) {
	if pu.CustomProviderId > 0 {
		if user, err := model.GetUserByOAuthBinding(pu.CustomProviderId, pu.ProviderID); err == nil {
			return user, false, nil
		}
	} else if user := findUserByProviderField(provider, pu.ProviderID); user != nil {
		return user, false, nil
	}

	inviterId := 0
	if affCode != "" {
		if inviter, err := GetUserByAffCode(affCode); err == nil {
			inviterId = inviter.Id
		}
	}
	// Custom identities may carry no username; fall back to a slug-scoped name.
	baseUsername := pu.Username
	if baseUsername == "" {
		baseUsername = provider + "_" + pu.ProviderID
	}
	displayName := pu.DisplayName
	if displayName == "" {
		displayName = baseUsername
	}
	user := &model.User{
		Username:    uniqueUsername(baseUsername),
		Password:    "", // OAuth users authenticate via the provider, not a password
		DisplayName: displayName,
		Email:       pu.Email,
		Role:        constant.RoleCommonUser,
		Status:      model.UserStatusEnabled,
		Group:       setting.GetOptionOrDefault(setting.DefaultGroupOption, GroupDefault),
		Quota:       setting.GetOptionIntOrDefault(setting.InitialQuotaOption, 500000),
		CreatedAt:   common.NowTimestamp(),
		AuthVersion: 1,
		InviterId:   inviterId,
	}
	if pu.CustomProviderId > 0 {
		// User row and binding row commit atomically, mirroring the
		// reference's InsertWithTx + CreateUserOAuthBindingWithTx pairing.
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(user).Error; err != nil {
				return err
			}
			return model.CreateUserOAuthBindingWithTx(tx, &model.UserOAuthBinding{
				UserId:         user.Id,
				ProviderId:     pu.CustomProviderId,
				ProviderUserId: pu.ProviderID,
			})
		})
		if err != nil {
			return nil, false, err
		}
	} else {
		// Built-in providers store the identity in a provider-scoped column.
		setProviderField(user, provider, pu.ProviderID)
		if err := model.DB.Create(user).Error; err != nil {
			return nil, false, err
		}
	}
	CreditInviter(inviterId, user.Id)
	return user, true, nil
}

// BindProviderToUser binds a provider identity to an existing user by setting
// the provider-scoped column (built-ins) or upserting the user_oauth_bindings
// row (custom providers). The identity must not already be bound elsewhere.
func BindProviderToUser(provider string, pu *ProviderUser, userId int) error {
	if pu.CustomProviderId > 0 {
		if err := model.UpdateUserOAuthBinding(userId, pu.CustomProviderId, pu.ProviderID); err != nil {
			if errors.Is(err, model.ErrOAuthBindingTaken) {
				return ErrBindingTaken
			}
			return err
		}
		return nil
	}
	if findUserByProviderField(provider, pu.ProviderID) != nil {
		return ErrBindingTaken
	}
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		return err
	}
	// Only the provider column is written, mirroring the reference's
	// UpdateUserBindColumn (a full snapshot write could clobber concurrent
	// role/status/group changes).
	setProviderField(&user, provider, pu.ProviderID)
	return model.DB.Model(&user).Update(providerFieldColumns[provider], providerFieldValue(&user, provider)).Error
}

func setProviderField(user *model.User, provider, id string) {
	switch provider {
	case "github":
		user.GitHubId = id
	case "discord":
		user.DiscordId = id
	case "oidc":
		user.OidcId = id
	case "linuxdo":
		user.LinuxDOId = id
	case "telegram":
		user.TelegramId = id
	case "wechat":
		user.WeChatId = id
	}
}

// providerFieldColumns maps a provider to its user-column name.
var providerFieldColumns = map[string]string{
	"github": "github_id", "discord": "discord_id", "oidc": "oidc_id", "linuxdo": "linuxdo_id",
	"telegram": "telegram_id", "wechat": "wechat_id",
}

// providerFieldValue returns the provider-scoped identity value of a user.
func providerFieldValue(user *model.User, provider string) string {
	switch provider {
	case "github":
		return user.GitHubId
	case "discord":
		return user.DiscordId
	case "oidc":
		return user.OidcId
	case "linuxdo":
		return user.LinuxDOId
	case "telegram":
		return user.TelegramId
	case "wechat":
		return user.WeChatId
	}
	return ""
}

func findUserByProviderField(provider, id string) *model.User {
	col := providerFieldColumns[provider]
	if col == "" {
		return nil
	}
	var user model.User
	if err := model.DB.Where(col+" = ?", id).First(&user).Error; err != nil {
		return nil
	}
	return &user
}

func uniqueUsername(base string) string {
	if _, err := GetUserByUsername(base); err != nil {
		return base
	}
	return base + "-" + common.RandomAlphanumeric(6)
}

// ErrBindingTaken is returned when a provider identity is already bound to a
// different account (built-in column or custom binding row).
var ErrBindingTaken = errors.New("该账号已绑定其他用户")
