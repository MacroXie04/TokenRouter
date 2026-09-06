package store

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

const (
	maxCustomOAuthNameBytes           = 64
	maxCustomOAuthSlugBytes           = 64
	maxCustomOAuthIconBytes           = 128
	maxCustomOAuthClientIDBytes       = 256
	maxCustomOAuthClientSecretBytes   = 512
	maxCustomOAuthEndpointBytes       = 512
	maxCustomOAuthScopesBytes         = 256
	maxCustomOAuthMappingFieldBytes   = 128
	maxCustomOAuthDeniedMessageBytes  = 512
	maxCustomOAuthPolicyBytes         = 64 << 10
	maxCustomOAuthEndpointQueryPairs  = 16
	maxCustomOAuthQueryKeyBytes       = 128
	maxCustomOAuthQueryValueBytes     = 2048
	maxCustomOAuthProviderUserIDBytes = 256
	maxCustomOAuthScopeCount          = 32
	maxCustomOAuthSingleScopeBytes    = 128
	maxAccessPolicyLogicBytes         = 16
	maxAccessPolicyOperatorBytes      = 32
	maxAccessPolicyDepth              = 16
	maxAccessPolicyEntries            = 1024
	maxAccessPolicyValues             = 4096
	maxAccessPolicyArrayItems         = 256
	maxAccessPolicyObjectItems        = 256
	maxAccessPolicyValueStringBytes   = 4096
)

var customOAuthFieldPathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

// ValidateAccessPolicyDocument checks a parsed policy tree and normalizes it
// in place (logic/op lowercased, fields trimmed) so the runtime evaluator can
// switch on canonical ops.
func ValidateAccessPolicyDocument(policy *AccessPolicyDocument) error {
	entries := 0
	values := 0
	return validateAccessPolicyDocument(policy, 1, &entries, &values)
}

func validateAccessPolicyDocument(
	policy *AccessPolicyDocument,
	depth int,
	entries *int,
	values *int,
) error {
	if policy == nil {
		return errors.New("policy is nil")
	}
	if depth > maxAccessPolicyDepth {
		return errors.New("policy exceeds maximum nesting depth")
	}
	if len(policy.Conditions) > maxAccessPolicyEntries || len(policy.Groups) > maxAccessPolicyEntries ||
		*entries > maxAccessPolicyEntries-len(policy.Conditions)-len(policy.Groups) {
		return errors.New("policy exceeds maximum entry count")
	}
	*entries += len(policy.Conditions) + len(policy.Groups)

	logic := strings.ToLower(strings.TrimSpace(policy.Logic))
	if logic == "" {
		logic = "and"
	}
	if validateBoundedCustomOAuthText(logic, maxAccessPolicyLogicBytes) != nil {
		return errors.New("policy logic exceeds safe limits")
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
		if err := validateBoundedCustomOAuthText(condition.Field, maxCustomOAuthMappingFieldBytes); err != nil {
			return fmt.Errorf("condition[%d].field exceeds safe limits", index)
		}
		if !customOAuthFieldPathPattern.MatchString(condition.Field) {
			return fmt.Errorf("condition[%d].field must be a simple JSON field path", index)
		}
		condition.Op = strings.ToLower(strings.TrimSpace(condition.Op))
		if validateBoundedCustomOAuthText(condition.Op, maxAccessPolicyOperatorBytes) != nil {
			return fmt.Errorf("condition[%d].op exceeds safe limits", index)
		}
		if _, ok := supportedAccessPolicyOps[condition.Op]; !ok {
			return fmt.Errorf("condition[%d].op is unsupported: %s", index, condition.Op)
		}
		if condition.Op == "in" || condition.Op == "not_in" {
			items, ok := condition.Value.([]any)
			if !ok {
				return fmt.Errorf("condition[%d].value must be an array for op %s", index, condition.Op)
			}
			if len(items) > maxAccessPolicyArrayItems {
				return fmt.Errorf("condition[%d].value exceeds safe limits", index)
			}
		}
		if err := validateAccessPolicyValue(condition.Value, 1, values); err != nil {
			return fmt.Errorf("condition[%d].value exceeds safe limits", index)
		}
	}

	for index := range policy.Groups {
		if err := validateAccessPolicyDocument(&policy.Groups[index], depth+1, entries, values); err != nil {
			return fmt.Errorf("group[%d]: %w", index, err)
		}
	}

	return nil
}

// ParseAccessPolicy parses and validates an access-policy JSON string.
func ParseAccessPolicy(raw string) (*AccessPolicyDocument, error) {
	if len(raw) > maxCustomOAuthPolicyBytes || !utf8.ValidString(raw) {
		return nil, errors.New("access_policy exceeds safe limits")
	}
	var policy AccessPolicyDocument
	if err := jsonutil.UnmarshalJsonStr(raw, &policy); err != nil {
		return nil, err
	}
	if err := ValidateAccessPolicyDocument(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func validateAccessPolicyValue(value any, depth int, values *int) error {
	if depth > maxAccessPolicyDepth || *values >= maxAccessPolicyValues {
		return errors.New("value exceeds safe limits")
	}
	*values = *values + 1
	switch typed := value.(type) {
	case nil, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return nil
	case string:
		return validateBoundedCustomOAuthText(typed, maxAccessPolicyValueStringBytes)
	case []any:
		if len(typed) > maxAccessPolicyArrayItems {
			return errors.New("array exceeds safe limits")
		}
		for _, item := range typed {
			if err := validateAccessPolicyValue(item, depth+1, values); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(typed) > maxAccessPolicyObjectItems {
			return errors.New("object exceeds safe limits")
		}
		for key, item := range typed {
			if err := validateBoundedCustomOAuthText(key, maxCustomOAuthMappingFieldBytes); err != nil {
				return err
			}
			if err := validateAccessPolicyValue(item, depth+1, values); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported value type")
	}
}

func validateBoundedCustomOAuthText(value string, maxBytes int) error {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return errors.New("text exceeds safe limits")
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return errors.New("text contains unsafe characters")
		}
	}
	return nil
}

func validateCustomOAuthEndpoint(label string, raw *string, required bool) error {
	*raw = strings.TrimSpace(*raw)
	if *raw == "" {
		if required {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
	if err := validateBoundedCustomOAuthText(*raw, maxCustomOAuthEndpointBytes); err != nil {
		return fmt.Errorf("%s exceeds safe limits", label)
	}
	parsed, err := url.Parse(*raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery ||
		strings.Contains(*raw, `\`) || strings.Contains(parsed.Path, `\`) ||
		validateBoundedCustomOAuthText(parsed.Path, maxCustomOAuthEndpointBytes) != nil {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", label)
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%s has an ambiguous path", label)
		}
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("%s has an invalid port", label)
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return fmt.Errorf("%s has an invalid query", label)
	}
	pairs := 0
	for key, values := range query {
		pairs += len(values)
		if key == "" || len(key) > maxCustomOAuthQueryKeyBytes || len(values) != 1 ||
			validateBoundedCustomOAuthText(key, maxCustomOAuthQueryKeyBytes) != nil ||
			validateBoundedCustomOAuthText(values[0], maxCustomOAuthQueryValueBytes) != nil {
			return fmt.Errorf("%s has an unsafe or duplicate query parameter", label)
		}
	}
	if pairs > maxCustomOAuthEndpointQueryPairs {
		return fmt.Errorf("%s has too many query parameters", label)
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(hostname)
	loopback := hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") ||
		address != nil && address.IsLoopback()
	if !httpx.SSRFDisabled() && (loopback || address != nil && httpx.IsUnsafeIP(address)) {
		return fmt.Errorf("%s targets an unsafe address", label)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", label)
	}
	// Plaintext is a development-only escape hatch. Requiring the same
	// explicit flag that disables the outbound SSRF guard prevents a
	// production process from silently accepting credential-bearing HTTP.
	if httpx.SSRFDisabled() && loopback {
		return nil
	}
	return fmt.Errorf("%s must use HTTPS except on loopback", label)
}

// validateCustomOAuthProvider validates a provider config, normalizing the
// slug to lowercase and filling field-mapping/scope defaults in place.
func validateCustomOAuthProvider(provider *CustomOAuthProvider) error {
	if provider == nil {
		return errors.New("OAuth provider is required")
	}
	provider.Name = strings.TrimSpace(provider.Name)
	if provider.Name == "" {
		return errors.New("provider name is required")
	}
	if err := validateBoundedCustomOAuthText(provider.Name, maxCustomOAuthNameBytes); err != nil {
		return errors.New("provider name exceeds safe limits")
	}
	provider.Slug = strings.TrimSpace(provider.Slug)
	if provider.Slug == "" {
		return errors.New("provider slug is required")
	}
	if len(provider.Slug) > maxCustomOAuthSlugBytes {
		return errors.New("provider slug exceeds safe limits")
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
	if provider.ClientId != strings.TrimSpace(provider.ClientId) {
		return errors.New("client ID is invalid")
	}
	if provider.ClientSecret == "" {
		return errors.New("client secret is required")
	}
	for _, field := range []struct {
		name     string
		value    string
		maxBytes int
	}{
		{"client ID", provider.ClientId, maxCustomOAuthClientIDBytes},
		{"client secret", provider.ClientSecret, maxCustomOAuthClientSecretBytes},
		{"icon", provider.Icon, maxCustomOAuthIconBytes},
		{"scopes", provider.Scopes, maxCustomOAuthScopesBytes},
		{"access denied message", provider.AccessDeniedMessage, maxCustomOAuthDeniedMessageBytes},
	} {
		if err := validateBoundedCustomOAuthText(field.value, field.maxBytes); err != nil {
			return fmt.Errorf("%s exceeds safe limits", field.name)
		}
	}
	if provider.AuthStyle < 0 || provider.AuthStyle > 2 {
		return errors.New("auth style is invalid")
	}
	if err := validateCustomOAuthEndpoint("authorization endpoint", &provider.AuthorizationEndpoint, true); err != nil {
		return err
	}
	if err := validateCustomOAuthEndpoint("token endpoint", &provider.TokenEndpoint, true); err != nil {
		return err
	}
	if err := validateCustomOAuthEndpoint("user info endpoint", &provider.UserInfoEndpoint, true); err != nil {
		return err
	}
	if err := validateCustomOAuthEndpoint("well-known endpoint", &provider.WellKnown, false); err != nil {
		return err
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
	scopes := strings.Fields(provider.Scopes)
	if len(scopes) == 0 || len(scopes) > maxCustomOAuthScopeCount {
		return errors.New("scopes exceed safe limits")
	}
	for _, scope := range scopes {
		if len(scope) > maxCustomOAuthSingleScopeBytes {
			return errors.New("scopes exceed safe limits")
		}
		for _, character := range scope {
			if character < 0x21 || character > 0x7e || character == '"' || character == '\\' {
				return errors.New("scopes exceed safe limits")
			}
		}
	}
	provider.Scopes = strings.Join(scopes, " ")
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"user ID field", &provider.UserIdField},
		{"username field", &provider.UsernameField},
		{"display name field", &provider.DisplayNameField},
		{"email field", &provider.EmailField},
	} {
		*field.value = strings.TrimSpace(*field.value)
		if err := validateBoundedCustomOAuthText(*field.value, maxCustomOAuthMappingFieldBytes); err != nil {
			return fmt.Errorf("%s exceeds safe limits", field.name)
		}
		if !customOAuthFieldPathPattern.MatchString(*field.value) {
			return fmt.Errorf("%s must be a simple JSON field path", field.name)
		}
	}
	if len(provider.AccessPolicy) > maxCustomOAuthPolicyBytes || !utf8.ValidString(provider.AccessPolicy) {
		return errors.New("access_policy exceeds safe limits")
	}
	if strings.TrimSpace(provider.AccessPolicy) != "" {
		var policy AccessPolicyDocument
		if err := jsonutil.UnmarshalJsonStr(provider.AccessPolicy, &policy); err != nil {
			return errors.New("access_policy must be valid JSON")
		}
		if err := ValidateAccessPolicyDocument(&policy); err != nil {
			return fmt.Errorf("access_policy is invalid: %w", err)
		}
	}

	return nil
}

// ValidateCustomOAuthProvider validates and normalizes an operator-supplied
// provider without persisting it. Controllers use this to distinguish safe
// field-validation feedback from database errors that must not be reflected.
func ValidateCustomOAuthProvider(provider *CustomOAuthProvider) error {
	return validateCustomOAuthProvider(provider)
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
	if err := DB.Where("enabled = ?", true).Order("id asc").Find(&providers).Error; err != nil {
		return nil, err
	}
	for _, provider := range providers {
		if err := validateCustomOAuthProvider(provider); err != nil {
			return nil, fmt.Errorf("invalid enabled custom OAuth provider %d: %w", provider.Id, err)
		}
	}
	return providers, nil
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
	if err := validateCustomOAuthProvider(&provider); err != nil {
		return nil, fmt.Errorf("invalid custom OAuth provider %d: %w", provider.Id, err)
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

// ErrCustomOAuthProviderHasBindings prevents an admin deletion from racing a
// login/bind transaction and orphaning an identity claim.
var ErrCustomOAuthProviderHasBindings = errors.New("custom OAuth provider still has user bindings")

// DeleteCustomOAuthProviderIfUnused performs the production admin deletion
// under the same provider-row lock used by identity mutations. The legacy
// DeleteCustomOAuthProvider helper retains its explicit cascading semantics
// for internal callers that intentionally remove bindings first.
func DeleteCustomOAuthProviderIfUnused(id int) error {
	if id <= 0 {
		return errors.New("invalid custom OAuth provider")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		query := tx
		if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var provider CustomOAuthProvider
		if err := query.Select("id").First(&provider, id).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&UserOAuthBinding{}).Where("provider_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrCustomOAuthProviderHasBindings
		}
		result := tx.Delete(&CustomOAuthProvider{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
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
	return GetUserByOAuthBindingWithTx(DB, providerId, providerUserId)
}

// GetUserByOAuthBindingWithTx finds the user bound to a custom-provider
// identity using the caller's transaction.
func GetUserByOAuthBindingWithTx(tx *gorm.DB, providerId int, providerUserId string) (*User, error) {
	if tx == nil || providerId <= 0 {
		return nil, errors.New("invalid OAuth binding lookup")
	}
	var err error
	providerUserId, err = NormalizeCustomOAuthSubject(providerUserId)
	if err != nil {
		return nil, err
	}
	var binding UserOAuthBinding
	if err := tx.Where("provider_id = ? AND provider_user_id = ?", providerId, providerUserId).First(&binding).Error; err != nil {
		return nil, err
	}
	// Some production databases use case- or accent-insensitive collations for
	// VARCHAR. A collation match is not an identity match: subjects are opaque
	// and must compare byte-for-byte before ownership is trusted.
	if binding.ProviderUserId != providerUserId {
		return nil, ErrExternalIdentityOwnerInvalid
	}
	var user User
	if err := tx.First(&user, binding.UserId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: user %d", ErrExternalIdentityOwnerInvalid, binding.UserId)
		}
		return nil, err
	}
	return &user, nil
}

// IsProviderUserIdTaken reports whether a provider identity is already bound.
func IsProviderUserIdTaken(providerId int, providerUserId string) bool {
	providerUserId, err := NormalizeCustomOAuthSubject(providerUserId)
	if err != nil || providerId <= 0 {
		return true
	}
	var count int64
	if err := DB.Model(&UserOAuthBinding{}).Where("provider_id = ? AND provider_user_id = ?", providerId, providerUserId).Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

// NormalizeCustomOAuthSubject canonicalizes a provider subject before it is
// queried or persisted. Subjects are opaque and therefore never truncated:
// doing so could merge two distinct provider identities.
func NormalizeCustomOAuthSubject(subject string) (string, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" || validateBoundedCustomOAuthText(subject, maxCustomOAuthProviderUserIDBytes) != nil {
		return "", errors.New("provider user ID exceeds safe limits")
	}
	return subject, nil
}

func validateUserOAuthBinding(binding *UserOAuthBinding) error {
	if binding.UserId == 0 {
		return errors.New("user ID is required")
	}
	if binding.ProviderId == 0 {
		return errors.New("provider ID is required")
	}
	providerUserId, err := NormalizeCustomOAuthSubject(binding.ProviderUserId)
	if err != nil {
		if strings.TrimSpace(binding.ProviderUserId) == "" {
			return errors.New("provider user ID is required")
		}
		return err
	}
	binding.ProviderUserId = providerUserId
	return nil
}

// ErrOAuthBindingTaken is returned when a provider identity is already bound
// to a different user.
var ErrOAuthBindingTaken = errors.New("this OAuth account is already bound to another user")

// CreateUserOAuthBinding stores a new binding after a taken check.
func CreateUserOAuthBinding(binding *UserOAuthBinding) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return CreateUserOAuthBindingWithTx(tx, binding)
	})
}

// CreateUserOAuthBindingWithTx stores a new binding inside a transaction, with
// the taken check performed through the same transaction.
func CreateUserOAuthBindingWithTx(tx *gorm.DB, binding *UserOAuthBinding) error {
	if tx == nil {
		return errors.New("database is nil")
	}
	if err := validateUserOAuthBinding(binding); err != nil {
		return err
	}
	var count int64
	if err := tx.Model(&UserOAuthBinding{}).Where("provider_id = ? AND provider_user_id = ?", binding.ProviderId, binding.ProviderUserId).Count(&count).Error; err != nil {
		return err
	}
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
	err := DB.Transaction(func(tx *gorm.DB) error {
		return UpdateUserOAuthBindingWithTx(tx, userId, providerId, newProviderUserId)
	})
	if err == nil || errors.Is(err, ErrOAuthBindingTaken) {
		return err
	}
	// Portable post-conflict read-back maps a database-specific unique-index
	// race to the public ownership error without classifying unrelated write
	// failures as "taken".
	var existing UserOAuthBinding
	lookup := DB.Where("provider_id = ? AND provider_user_id = ?", providerId, strings.TrimSpace(newProviderUserId)).First(&existing)
	if lookup.Error == nil {
		if existing.ProviderUserId == strings.TrimSpace(newProviderUserId) && existing.UserId == userId {
			return nil
		}
		return ErrOAuthBindingTaken
	}
	return err
}

// UpdateUserOAuthBindingWithTx atomically creates or replaces one user's
// custom-provider slot while refusing an identity owned by another user.
func UpdateUserOAuthBindingWithTx(tx *gorm.DB, userId, providerId int, newProviderUserId string) error {
	if tx == nil || userId <= 0 || providerId <= 0 {
		return errors.New("invalid OAuth binding")
	}
	var err error
	newProviderUserId, err = NormalizeCustomOAuthSubject(newProviderUserId)
	if err != nil {
		return err
	}
	query := tx
	if dialect := tx.Dialector.Name(); dialect == "mysql" || dialect == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var claimed UserOAuthBinding
	claimErr := query.Where("provider_id = ? AND provider_user_id = ?", providerId, newProviderUserId).First(&claimed).Error
	if claimErr == nil {
		if claimed.ProviderUserId == newProviderUserId && claimed.UserId == userId {
			return nil
		}
		return ErrOAuthBindingTaken
	}
	if !errors.Is(claimErr, gorm.ErrRecordNotFound) {
		return claimErr
	}

	var binding UserOAuthBinding
	bindingErr := query.Where("user_id = ? AND provider_id = ?", userId, providerId).First(&binding).Error
	if errors.Is(bindingErr, gorm.ErrRecordNotFound) {
		return CreateUserOAuthBindingWithTx(tx, &UserOAuthBinding{
			UserId: userId, ProviderId: providerId, ProviderUserId: newProviderUserId,
		})
	}
	if bindingErr != nil {
		return bindingErr
	}
	result := tx.Model(&UserOAuthBinding{}).
		Where("id = ? AND provider_user_id = ?", binding.Id, binding.ProviderUserId).
		Update("provider_user_id", newProviderUserId)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("concurrent OAuth binding update")
	}
	return nil
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
