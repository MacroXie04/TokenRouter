package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
)

// AccessPolicyDocument is the parsed form of CustomOAuthProvider.AccessPolicy:
// a boolean tree of conditions evaluated against the provider's raw userinfo
// JSON at login time. Logic is "and"/"or" (empty means "and").
type AccessPolicyDocument struct {
	Logic      string                  `json:"logic"`
	Conditions []AccessPolicyCondition `json:"conditions"`
	Groups     []AccessPolicyDocument  `json:"groups"`
}

// AccessPolicyCondition is a single field/op/value check inside a policy.
type AccessPolicyCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

var supportedAccessPolicyOps = map[string]struct{}{
	"eq": {}, "ne": {}, "gt": {}, "gte": {}, "lt": {}, "lte": {},
	"in": {}, "not_in": {}, "contains": {}, "not_contains": {},
	"exists": {}, "not_exists": {},
}

// ValidateAccessPolicyDocument checks a parsed policy tree and normalizes it
// in place (logic/op lowercased, fields trimmed) so the runtime evaluator can
// switch on canonical ops.
func ValidateAccessPolicyDocument(policy *AccessPolicyDocument) error {
	if policy == nil {
		return errors.New("policy is nil")
	}

	logic := strings.ToLower(strings.TrimSpace(policy.Logic))
	if logic == "" {
		logic = "and"
	}
	if logic != "and" && logic != "or" {
		return fmt.Errorf("unsupported logic: %s", logic)
	}
	policy.Logic = logic

	if len(policy.Conditions) == 0 && len(policy.Groups) == 0 {
		return errors.New("policy requires at least one condition or group")
	}

	for index := range policy.Conditions {
		condition := &policy.Conditions[index]
		condition.Field = strings.TrimSpace(condition.Field)
		if condition.Field == "" {
			return fmt.Errorf("condition[%d].field is required", index)
		}
		condition.Op = strings.ToLower(strings.TrimSpace(condition.Op))
		if _, ok := supportedAccessPolicyOps[condition.Op]; !ok {
			return fmt.Errorf("condition[%d].op is unsupported: %s", index, condition.Op)
		}
		if condition.Op == "in" || condition.Op == "not_in" {
			if _, ok := condition.Value.([]any); !ok {
				return fmt.Errorf("condition[%d].value must be an array for op %s", index, condition.Op)
			}
		}
	}

	for index := range policy.Groups {
		if err := ValidateAccessPolicyDocument(&policy.Groups[index]); err != nil {
			return fmt.Errorf("group[%d]: %w", index, err)
		}
	}

	return nil
}

// ParseAccessPolicy parses and validates an access-policy JSON string.
func ParseAccessPolicy(raw string) (*AccessPolicyDocument, error) {
	var policy AccessPolicyDocument
	if err := common.UnmarshalJsonStr(raw, &policy); err != nil {
		return nil, err
	}
	if err := ValidateAccessPolicyDocument(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// validateCustomOAuthProvider validates a provider config, normalizing the
// slug to lowercase and filling field-mapping/scope defaults in place.
func validateCustomOAuthProvider(provider *CustomOAuthProvider) error {
	if provider.Name == "" {
		return errors.New("provider name is required")
	}
	if provider.Slug == "" {
		return errors.New("provider slug is required")
	}
	slug := strings.ToLower(provider.Slug)
	for _, c := range slug {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return errors.New("provider slug must contain only lowercase letters, numbers, and hyphens")
		}
	}
	provider.Slug = slug

	if provider.ClientId == "" {
		return errors.New("client ID is required")
	}
	if provider.AuthorizationEndpoint == "" {
		return errors.New("authorization endpoint is required")
	}
	if provider.TokenEndpoint == "" {
		return errors.New("token endpoint is required")
	}
	if provider.UserInfoEndpoint == "" {
		return errors.New("user info endpoint is required")
	}

	if provider.UserIdField == "" {
		provider.UserIdField = "sub"
	}
	if provider.UsernameField == "" {
		provider.UsernameField = "preferred_username"
	}
	if provider.DisplayNameField == "" {
		provider.DisplayNameField = "name"
	}
	if provider.EmailField == "" {
		provider.EmailField = "email"
	}
	if provider.Scopes == "" {
		provider.Scopes = "openid profile email"
	}
	if strings.TrimSpace(provider.AccessPolicy) != "" {
		var policy AccessPolicyDocument
		if err := common.UnmarshalJsonStr(provider.AccessPolicy, &policy); err != nil {
			return errors.New("access_policy must be valid JSON")
		}
		if err := ValidateAccessPolicyDocument(&policy); err != nil {
			return fmt.Errorf("access_policy is invalid: %w", err)
		}
	}

	return nil
}

// GetAllCustomOAuthProviders returns all custom OAuth providers.
func GetAllCustomOAuthProviders() ([]*CustomOAuthProvider, error) {
	var providers []*CustomOAuthProvider
	err := DB.Order("id asc").Find(&providers).Error
	return providers, err
}

// GetEnabledCustomOAuthProviders returns all enabled custom OAuth providers.
func GetEnabledCustomOAuthProviders() ([]*CustomOAuthProvider, error) {
	var providers []*CustomOAuthProvider
	err := DB.Where("enabled = ?", true).Order("id asc").Find(&providers).Error
	return providers, err
}

// GetCustomOAuthProviderById returns a custom OAuth provider by id.
func GetCustomOAuthProviderById(id int) (*CustomOAuthProvider, error) {
	var provider CustomOAuthProvider
	if err := DB.First(&provider, id).Error; err != nil {
		return nil, err
	}
	return &provider, nil
}

// GetCustomOAuthProviderBySlug returns a custom OAuth provider by slug.
func GetCustomOAuthProviderBySlug(slug string) (*CustomOAuthProvider, error) {
	var provider CustomOAuthProvider
	if err := DB.Where("slug = ?", slug).First(&provider).Error; err != nil {
		return nil, err
	}
	return &provider, nil
}

// CreateCustomOAuthProvider validates and stores a new provider.
func CreateCustomOAuthProvider(provider *CustomOAuthProvider) error {
	if err := validateCustomOAuthProvider(provider); err != nil {
		return err
	}
	return DB.Create(provider).Error
}

// UpdateCustomOAuthProvider validates and saves an existing provider.
func UpdateCustomOAuthProvider(provider *CustomOAuthProvider) error {
	if err := validateCustomOAuthProvider(provider); err != nil {
		return err
	}
	return DB.Save(provider).Error
}

// DeleteCustomOAuthProvider deletes a provider and its user bindings.
func DeleteCustomOAuthProvider(id int) error {
	if err := DB.Where("provider_id = ?", id).Delete(&UserOAuthBinding{}).Error; err != nil {
		return err
	}
	return DB.Delete(&CustomOAuthProvider{}, id).Error
}

// IsCustomOAuthSlugTaken reports whether a slug is used by another provider.
// DB errors count as taken (fail-closed) so a conflict can never slip through.
func IsCustomOAuthSlugTaken(slug string, excludeId int) bool {
	var count int64
	query := DB.Model(&CustomOAuthProvider{}).Where("slug = ?", slug)
	if excludeId > 0 {
		query = query.Where("id != ?", excludeId)
	}
	if err := query.Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

// GetUserOAuthBindingsByUserId returns all custom-provider bindings of a user.
func GetUserOAuthBindingsByUserId(userId int) ([]*UserOAuthBinding, error) {
	var bindings []*UserOAuthBinding
	err := DB.Where("user_id = ?", userId).Find(&bindings).Error
	return bindings, err
}

// GetUserByOAuthBinding finds the user bound to a provider identity.
func GetUserByOAuthBinding(providerId int, providerUserId string) (*User, error) {
	var binding UserOAuthBinding
	if err := DB.Where("provider_id = ? AND provider_user_id = ?", providerId, providerUserId).First(&binding).Error; err != nil {
		return nil, err
	}
	var user User
	if err := DB.First(&user, binding.UserId).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// IsProviderUserIdTaken reports whether a provider identity is already bound.
func IsProviderUserIdTaken(providerId int, providerUserId string) bool {
	var count int64
	DB.Model(&UserOAuthBinding{}).Where("provider_id = ? AND provider_user_id = ?", providerId, providerUserId).Count(&count)
	return count > 0
}

func validateUserOAuthBinding(binding *UserOAuthBinding) error {
	if binding.UserId == 0 {
		return errors.New("user ID is required")
	}
	if binding.ProviderId == 0 {
		return errors.New("provider ID is required")
	}
	if binding.ProviderUserId == "" {
		return errors.New("provider user ID is required")
	}
	return nil
}

// ErrOAuthBindingTaken is returned when a provider identity is already bound
// to a different user.
var ErrOAuthBindingTaken = errors.New("this OAuth account is already bound to another user")

// CreateUserOAuthBinding stores a new binding after a taken check.
func CreateUserOAuthBinding(binding *UserOAuthBinding) error {
	if err := validateUserOAuthBinding(binding); err != nil {
		return err
	}
	if IsProviderUserIdTaken(binding.ProviderId, binding.ProviderUserId) {
		return ErrOAuthBindingTaken
	}
	binding.CreatedAt = time.Now()
	return DB.Create(binding).Error
}

// CreateUserOAuthBindingWithTx stores a new binding inside a transaction, with
// the taken check performed through the same transaction.
func CreateUserOAuthBindingWithTx(tx *gorm.DB, binding *UserOAuthBinding) error {
	if err := validateUserOAuthBinding(binding); err != nil {
		return err
	}
	var count int64
	tx.Model(&UserOAuthBinding{}).Where("provider_id = ? AND provider_user_id = ?", binding.ProviderId, binding.ProviderUserId).Count(&count)
	if count > 0 {
		return ErrOAuthBindingTaken
	}
	binding.CreatedAt = time.Now()
	return tx.Create(binding).Error
}

// UpdateUserOAuthBinding rebinds a user's provider identity: creates the
// binding when missing, otherwise replaces the provider user id. The identity
// must not belong to another user.
func UpdateUserOAuthBinding(userId, providerId int, newProviderUserId string) error {
	var existing UserOAuthBinding
	err := DB.Where("provider_id = ? AND provider_user_id = ?", providerId, newProviderUserId).First(&existing).Error
	if err == nil && existing.UserId != userId {
		return ErrOAuthBindingTaken
	}

	var binding UserOAuthBinding
	err = DB.Where("user_id = ? AND provider_id = ?", userId, providerId).First(&binding).Error
	if err != nil {
		return CreateUserOAuthBinding(&UserOAuthBinding{
			UserId:         userId,
			ProviderId:     providerId,
			ProviderUserId: newProviderUserId,
		})
	}
	return DB.Model(&binding).Update("provider_user_id", newProviderUserId).Error
}

// DeleteUserOAuthBinding removes a user's binding for one provider.
func DeleteUserOAuthBinding(userId, providerId int) error {
	return DB.Where("user_id = ? AND provider_id = ?", userId, providerId).Delete(&UserOAuthBinding{}).Error
}

// GetBindingCountByProviderId counts the bindings attached to a provider.
func GetBindingCountByProviderId(providerId int) (int64, error) {
	var count int64
	err := DB.Model(&UserOAuthBinding{}).Where("provider_id = ?", providerId).Count(&count).Error
	return count, err
}
