package auth

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

// LoginOrBindUser finds an existing user bound to the provider identity, or
// creates a new user and binds the identity (via the user's provider-scoped id
// field for built-in providers, and UserOAuthBinding for custom providers).
func LoginOrBindUser(provider string, pu *ProviderUser) (*model.User, bool, error) {
	return LoginOrBindUserWithAff(provider, pu, "")
}

// LoginOrBindUserWithAff finds an existing user by provider identity or creates
// a new one, applying the optional affiliate code as the inviter.
func LoginOrBindUserWithAff(provider string, pu *ProviderUser, affCode string) (*model.User, bool, error) {
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return nil, false, err
	}
	affCode = strings.TrimSpace(affCode)
	if !validOAuthText(affCode, 32, true) {
		return nil, false, errors.New("affiliate code exceeds safe limits")
	}
	var user *model.User
	var created bool
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		user, created, txErr = loginOrBindUserWithAffTx(tx, provider, normalized, affCode)
		return txErr
	})
	if err == nil {
		return user, created, nil
	}
	// A portable read-back resolves unique-index races without relying on a
	// driver's duplicate-key error type.
	if normalized.CustomProviderId > 0 {
		configured, originErr := model.GetCustomOAuthProviderById(normalized.CustomProviderId)
		if originErr != nil || !configured.Enabled || configured.Slug != provider {
			return nil, false, err
		}
	}
	if winner, lookupErr := findProviderUser(provider, normalized); lookupErr == nil {
		return winner, false, nil
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return nil, false, lookupErr
	}
	return nil, false, err
}

func loginOrBindUserWithAffTx(tx *gorm.DB, provider string, pu *ProviderUser, affCode string) (*model.User, bool, error) {
	if tx == nil || pu == nil {
		return nil, false, errors.New("invalid provider login transaction")
	}
	if err := validateCustomProviderIdentityWithTx(tx, provider, pu); err != nil {
		return nil, false, err
	}
	if user, err := findProviderUserWithTx(tx, provider, pu); err == nil {
		return user, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	// Older GitHub releases stored the login name rather than the stable
	// numeric id. Move the claim and compatibility mirror inside this caller's
	// transaction so the state consume can commit with the migration.
	if provider == model.ExternalIdentityProviderGitHub && pu.LegacyProviderID != "" && pu.LegacyProviderID != pu.ProviderID {
		legacy := &ProviderUser{ProviderID: pu.LegacyProviderID}
		legacyOwner, legacyErr := findProviderUserWithTx(tx, provider, legacy)
		if legacyErr == nil {
			if err := bindProviderToUserWithTx(tx, provider, pu, legacyOwner.Id); err != nil {
				return nil, false, err
			}
			legacyOwner.GitHubId = pu.ProviderID
			return legacyOwner, false, nil
		}
		if !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
			return nil, false, legacyErr
		}
	}

	registrationEnabled, err := registrationEnabledWithTx(tx)
	if err != nil {
		return nil, false, err
	}
	if !registrationEnabled {
		return nil, false, ErrRegistrationDisabled
	}

	inviterId := 0
	if affCode != "" {
		var inviter model.User
		if err := tx.Where("aff_code = ?", affCode).First(&inviter).Error; err == nil {
			inviterId = inviter.Id
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
	}
	baseUsername := pu.Username
	if baseUsername == "" {
		baseUsername = normalizeOAuthProfileText(provider+"_"+pu.ProviderID, maxOAuthUsernameBytes)
	}
	displayName := pu.DisplayName
	if displayName == "" {
		displayName = baseUsername
	}
	username, err := uniqueUsernameWithTx(tx, baseUsername)
	if err != nil {
		return nil, false, err
	}
	registrationPlan, err := PlanRegistrationMutationFromEnvironment(username)
	if err != nil {
		return nil, false, err
	}
	user := &model.User{
		Username: username, Password: "", DisplayName: displayName, Email: pu.Email,
		Role: roles.RoleCommonUser, Status: model.UserStatusEnabled,
		CreatedAt: wallclock.NowTimestamp(), AuthVersion: 1, InviterId: inviterId,
	}
	if pu.CustomProviderId == 0 {
		setProviderField(user, provider, pu.ProviderID)
	}
	if err := InsertPlannedRegistrationUserWithTx(tx, user, registrationPlan); err != nil {
		return nil, false, err
	}
	if pu.CustomProviderId > 0 {
		if err := model.CreateUserOAuthBindingWithTx(tx, &model.UserOAuthBinding{
			UserId: user.Id, ProviderId: pu.CustomProviderId, ProviderUserId: pu.ProviderID,
		}); err != nil {
			return nil, false, err
		}
	} else if err := model.ClaimExternalIdentityWithTx(tx, provider, pu.ProviderID, user.Id); err != nil {
		return nil, false, err
	}
	if err := creditInviterTx(tx, inviterId, user.Id); err != nil {
		return nil, false, err
	}
	return user, true, nil
}

func registrationEnabledWithTx(tx *gorm.DB) (bool, error) {
	gates, err := registrationGateStateWithTx(tx)
	if err != nil {
		return false, err
	}
	return gates.registrationEnabled, nil
}

// BindProviderToUser binds a provider identity to an existing user by setting
// the provider-scoped column (built-ins) or upserting the user_oauth_bindings
// row (custom providers). The identity must not already be bound elsewhere.
func BindProviderToUser(provider string, pu *ProviderUser, userId int) error {
	if userId <= 0 {
		return errors.New("invalid external identity binding")
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		return bindProviderToUserWithTx(tx, provider, normalized, userId)
	})
	if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) || errors.Is(err, model.ErrOAuthBindingTaken) {
		return ErrBindingTaken
	}
	return err
}

// BindProviderToSession revalidates the exact live browser session inside the
// same transaction as the identity mutation. This is used by direct bind
// endpoints (such as WeChat) that do not have a separate one-time flow.
func BindProviderToSession(provider string, pu *ProviderUser, userId int, sessionId string) error {
	if userId <= 0 || !validOAuthText(sessionId, maxAuthFlowSessionBytes, false) || sessionId != strings.TrimSpace(sessionId) {
		return ErrSessionRevoked
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := validateSessionBindingWithTx(tx, userId, sessionId); err != nil {
			return err
		}
		return bindProviderToUserWithTx(tx, provider, normalized, userId)
	})
	if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) || errors.Is(err, model.ErrOAuthBindingTaken) {
		return ErrBindingTaken
	}
	return err
}

func bindProviderToUserWithTx(tx *gorm.DB, provider string, pu *ProviderUser, userId int) error {
	if tx == nil || pu == nil || userId <= 0 {
		return errors.New("invalid external identity binding")
	}
	if err := validateCustomProviderIdentityWithTx(tx, provider, pu); err != nil {
		return err
	}
	if pu.CustomProviderId > 0 {
		if err := model.UpdateUserOAuthBindingWithTx(tx, userId, pu.CustomProviderId, pu.ProviderID); err != nil {
			if errors.Is(err, model.ErrOAuthBindingTaken) {
				return ErrBindingTaken
			}
			return err
		}
		return nil
	}
	column, ok := model.BuiltInExternalIdentityColumn(provider)
	if !ok {
		return errors.New("unsupported external identity provider")
	}
	var user model.User
	query := tx
	if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Select("id", column).First(&user, userId).Error; err != nil {
		return err
	}
	if _, err := model.FindExternalIdentityClaimWithTx(tx, provider, pu.ProviderID); err == nil {
		return ErrBindingTaken
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if provider == model.ExternalIdentityProviderGitHub && pu.LegacyProviderID != "" && pu.LegacyProviderID != pu.ProviderID {
		if legacyClaim, err := model.FindExternalIdentityClaimWithTx(tx, provider, pu.LegacyProviderID); err == nil {
			if legacyClaim.UserId != userId {
				return ErrBindingTaken
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	oldSubject := providerFieldValue(&user, provider)
	if err := model.ReleaseExternalIdentityWithTx(tx, provider, userId); err != nil {
		return err
	}
	if err := model.ClaimExternalIdentityWithTx(tx, provider, pu.ProviderID, userId); err != nil {
		if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) {
			return ErrBindingTaken
		}
		return err
	}
	result := tx.Model(&model.User{}).
		Where("id = ? AND "+column+" = ?", userId, oldSubject).
		Update(column, pu.ProviderID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("concurrent external identity binding update")
	}
	return nil
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

// FindUserByProviderIdentity resolves built-in identity ownership through the
// canonical claim table. A missing/deleted owner is an error, not permission
// to create a replacement account.
func FindUserByProviderIdentity(provider, id string) (*model.User, error) {
	normalized, err := normalizeProviderUser(provider, &ProviderUser{ProviderID: id})
	if err != nil {
		return nil, err
	}
	return model.FindUserByExternalIdentity(provider, normalized.ProviderID)
}

func uniqueUsername(base string) (string, error) {
	return uniqueUsernameWithTx(model.DB, base)
}

func uniqueUsernameWithTx(tx *gorm.DB, base string) (string, error) {
	if tx == nil {
		return "", errors.New("database is nil")
	}
	base = normalizeOAuthProfileText(base, maxOAuthUsernameBytes)
	if base == "" {
		base = "oauth_user"
	}
	var user model.User
	err := tx.Where("username = ?", base).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return base, nil
	}
	if err != nil {
		return "", err
	}
	prefix := normalizeOAuthProfileText(base, maxOAuthUsernameBytes-7)
	for range 5 {
		suffix, err := cryptoutil.SecureRandomAlphanumeric(6)
		if err != nil {
			return "", err
		}
		candidate := prefix + "-" + suffix
		user = model.User{}
		err = tx.Where("username = ?", candidate).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("unable to allocate OAuth username")
}

func findProviderUser(provider string, pu *ProviderUser) (*model.User, error) {
	return findProviderUserWithTx(model.DB, provider, pu)
}

func validateCustomProviderIdentityWithTx(tx *gorm.DB, provider string, pu *ProviderUser) error {
	if pu == nil || pu.CustomProviderId == 0 {
		return nil
	}
	query := tx
	if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var configured model.CustomOAuthProvider
	if err := query.Select("id", "slug", "enabled").First(&configured, pu.CustomProviderId).Error; err != nil {
		return errors.New("custom OAuth identity provider is unavailable")
	}
	if !configured.Enabled || configured.Slug != provider || IsBuiltInOAuthProvider(provider) {
		return errors.New("custom OAuth identity provider is unavailable")
	}
	return nil
}

func findProviderUserWithTx(tx *gorm.DB, provider string, pu *ProviderUser) (*model.User, error) {
	if tx == nil || pu == nil {
		return nil, errors.New("invalid provider identity lookup")
	}
	if pu.CustomProviderId > 0 {
		return model.GetUserByOAuthBindingWithTx(tx, pu.CustomProviderId, pu.ProviderID)
	}
	claim, err := model.FindExternalIdentityClaimWithTx(tx, provider, pu.ProviderID)
	if err != nil {
		return nil, err
	}
	var user model.User
	if err := tx.First(&user, claim.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: user %d", model.ErrExternalIdentityOwnerInvalid, claim.UserId)
		}
		return nil, err
	}
	return &user, nil
}

func validateSessionBindingWithTx(tx *gorm.DB, userId int, sid string) error {
	if tx == nil || userId <= 0 || sid == "" {
		return ErrSessionRevoked
	}
	var session model.UserSession
	if err := tx.Where("user_id = ? AND sid = ?", userId, sid).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	var user model.User
	if err := tx.First(&user, userId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSessionRevoked
		}
		return err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	if !validSessionSnapshot(&session, &user, now) {
		return ErrSessionRevoked
	}
	return nil
}

// ConsumeProviderLoginFlow commits the one-time flow consumption, account
// creation, identity claim, and referral credit in one transaction. Existing
// identities are resolved in that same snapshot.
func ConsumeProviderLoginFlow(token string, match AuthFlowMatch, provider string, pu *ProviderUser, affCode string) (*model.AuthFlow, *model.User, bool, error) {
	if match.Provider != provider || match.UserId != 0 || match.SessionId != "" {
		return nil, nil, false, ErrInvalidFlowToken
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return nil, nil, false, err
	}
	affCode = strings.TrimSpace(affCode)
	if !validOAuthText(affCode, 32, true) {
		return nil, nil, false, errors.New("affiliate code exceeds safe limits")
	}
	var user *model.User
	var created bool
	flow, err := ConsumeAuthFlowExactWithAction(token, match, func(tx *gorm.DB, _ *model.AuthFlow) error {
		var actionErr error
		user, created, actionErr = loginOrBindUserWithAffTx(tx, provider, normalized, affCode)
		return actionErr
	})
	if err != nil {
		return nil, nil, false, err
	}
	return flow, user, created, nil
}

// ConsumeProviderBindFlow commits a one-time session-bound flow and its
// identity replacement together. Ownership conflicts are terminal and still
// consume the flow; transient database failures roll everything back.
func ConsumeProviderBindFlow(token string, match AuthFlowMatch, provider string, pu *ProviderUser) (*model.AuthFlow, error) {
	if match.Provider != provider || match.UserId <= 0 || match.SessionId == "" {
		return nil, ErrInvalidFlowToken
	}
	normalized, err := normalizeProviderUser(provider, pu)
	if err != nil {
		return nil, err
	}
	var bindErr error
	flow, err := ConsumeAuthFlowExactWithAction(token, match, func(tx *gorm.DB, _ *model.AuthFlow) error {
		if err := validateSessionBindingWithTx(tx, match.UserId, match.SessionId); err != nil {
			return err
		}
		bindErr = bindProviderToUserWithTx(tx, provider, normalized, match.UserId)
		if errors.Is(bindErr, ErrBindingTaken) {
			return nil
		}
		return bindErr
	})
	if err != nil {
		return nil, err
	}
	return flow, bindErr
}
