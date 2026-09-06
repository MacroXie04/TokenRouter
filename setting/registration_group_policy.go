package setting

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	GroupSpecialUsableGroupOption = "group_ratio_setting.group_special_usable_group"
	DefaultUseAutoGroupOption     = "DefaultUseAutoGroup"
	QuotaForNewUserOption         = "QuotaForNewUser"

	DefaultQuotaForNewUser   = 0
	DefaultRegistrationGroup = "default"

	maxSpecialUsableGroupOptionBytes = 64 * 1024
	maxSpecialUsableUserGroups       = 256
	maxSpecialUsableRulesPerGroup    = 64
	maxSpecialUsableRules            = 4_096
	maxGroupPolicyIdentifierBytes    = 64
	maxGroupPolicyDescriptionBytes   = 256

	defaultGroupSpecialUsableGroupJSON = `{}`
)

// SpecialUsableGroupAction is the normalized operation represented by a
// reference-compatible bare, +:, or -: group directive.
type SpecialUsableGroupAction uint8

const (
	SpecialUsableGroupAdd SpecialUsableGroupAction = iota + 1
	SpecialUsableGroupRemove
)

// SpecialUsableGroupRule is a caller-owned copy of one validated directive.
// Rules are returned in stable target-group order and there can be at most one
// rule for a target within a user group.
type SpecialUsableGroupRule struct {
	Action      SpecialUsableGroupAction
	Group       string
	Description string
}

// RegistrationGroupPolicy is an immutable-at-publication configuration
// snapshot. Its collections remain private and every getter returns a copy.
// Reading this value once keeps quota, token-group defaults, and visibility
// directives coherent throughout one registration plan.
type RegistrationGroupPolicy struct {
	quotaForNewUser             int
	defaultUseAutoGroup         bool
	defaultGroup                string
	registrationEnabled         bool
	passwordRegistrationEnabled bool
	usedLegacyQuota             bool
	specialUsableByGroup        map[string][]SpecialUsableGroupRule
}

var registrationGroupPolicyConfig atomic.Pointer[RegistrationGroupPolicy]

func init() {
	policy, err := buildRegistrationGroupPolicy(nil)
	if err != nil {
		panic(err)
	}
	publishRegistrationGroupPolicy(policy)
}

// GroupSpecialUsableGroupOptionDefault returns the reference-compatible empty
// directive object used when no option has been persisted.
func GroupSpecialUsableGroupOptionDefault() string {
	return defaultGroupSpecialUsableGroupJSON
}

// DefaultUseAutoGroupOptionDefault returns the serialized reference default.
func DefaultUseAutoGroupOptionDefault() string { return "false" }

// QuotaForNewUserOptionDefault returns the canonical, financially safe default.
func QuotaForNewUserOptionDefault() string { return "0" }

// GetRegistrationGroupPolicy returns one detached coherent snapshot.
func GetRegistrationGroupPolicy() RegistrationGroupPolicy {
	policy := registrationGroupPolicyConfig.Load()
	if policy == nil {
		fallback, _ := buildRegistrationGroupPolicy(nil)
		return cloneRegistrationGroupPolicy(fallback)
	}
	return cloneRegistrationGroupPolicy(*policy)
}

// ParseRegistrationGroupPolicyOptions validates an option-map candidate and
// returns a detached snapshot without publishing it. Administrative update
// paths can therefore validate a whole batch before committing any row.
func ParseRegistrationGroupPolicyOptions(options map[string]string) (RegistrationGroupPolicy, error) {
	policy, err := buildRegistrationGroupPolicy(options)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	return cloneRegistrationGroupPolicy(policy), nil
}

// QuotaForNewUser is the exact internal integer quota assigned at creation.
func (p RegistrationGroupPolicy) QuotaForNewUser() int { return p.quotaForNewUser }

// DefaultUseAutoGroup reports whether newly generated default tokens use the
// auto-group inheritance marker.
func (p RegistrationGroupPolicy) DefaultUseAutoGroup() bool { return p.defaultUseAutoGroup }

// DefaultGroup is the bounded group assigned to every newly created account.
func (p RegistrationGroupPolicy) DefaultGroup() string { return p.defaultGroup }

// RegistrationEnabled and PasswordRegistrationEnabled are read from the same
// immutable publication as the rest of the account-creation policy. A caller
// can therefore never authorize password signup using fields from two
// different batched setting updates.
func (p RegistrationGroupPolicy) RegistrationEnabled() bool { return p.registrationEnabled }

func (p RegistrationGroupPolicy) PasswordRegistrationEnabled() bool {
	return p.passwordRegistrationEnabled
}

// UsesLegacyQuotaFallback reports whether InitialQuota supplied the value
// because the canonical QuotaForNewUser option was absent.
func (p RegistrationGroupPolicy) UsesLegacyQuotaFallback() bool { return p.usedLegacyQuota }

// SpecialUsableGroupRules returns a detached, deterministically ordered rule
// list for userGroup.
func (p RegistrationGroupPolicy) SpecialUsableGroupRules(userGroup string) []SpecialUsableGroupRule {
	rules := p.specialUsableByGroup[userGroup]
	return append([]SpecialUsableGroupRule(nil), rules...)
}

func buildRegistrationGroupPolicy(options map[string]string) (RegistrationGroupPolicy, error) {
	quota, usedLegacy, err := parseQuotaForNewUser(options)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	useAuto, err := parseDefaultUseAutoGroup(options)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	special, err := parseGroupSpecialUsableGroups(options)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	defaultGroup, err := parseRegistrationDefaultGroup(options)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	registrationEnabled, err := parseRegistrationPolicyBool(options, RegistrationEnabledOption, true)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	passwordRegistrationEnabled, err := parseRegistrationPolicyBool(options, PasswordRegisterEnabledOption, true)
	if err != nil {
		return RegistrationGroupPolicy{}, err
	}
	return RegistrationGroupPolicy{
		quotaForNewUser:             quota,
		defaultUseAutoGroup:         useAuto,
		defaultGroup:                defaultGroup,
		registrationEnabled:         registrationEnabled,
		passwordRegistrationEnabled: passwordRegistrationEnabled,
		usedLegacyQuota:             usedLegacy,
		specialUsableByGroup:        special,
	}, nil
}

func publishRegistrationGroupPolicy(policy RegistrationGroupPolicy) {
	copyOfPolicy := cloneRegistrationGroupPolicy(policy)
	registrationGroupPolicyConfig.Store(&copyOfPolicy)
}

func cloneRegistrationGroupPolicy(policy RegistrationGroupPolicy) RegistrationGroupPolicy {
	cloned := RegistrationGroupPolicy{
		quotaForNewUser:             policy.quotaForNewUser,
		defaultUseAutoGroup:         policy.defaultUseAutoGroup,
		defaultGroup:                policy.defaultGroup,
		registrationEnabled:         policy.registrationEnabled,
		passwordRegistrationEnabled: policy.passwordRegistrationEnabled,
		usedLegacyQuota:             policy.usedLegacyQuota,
		specialUsableByGroup:        make(map[string][]SpecialUsableGroupRule, len(policy.specialUsableByGroup)),
	}
	for userGroup, rules := range policy.specialUsableByGroup {
		cloned.specialUsableByGroup[userGroup] = append([]SpecialUsableGroupRule(nil), rules...)
	}
	return cloned
}

func parseQuotaForNewUser(options map[string]string) (int, bool, error) {
	if options == nil {
		return DefaultQuotaForNewUser, false, nil
	}
	if raw, present := options[QuotaForNewUserOption]; present {
		quota, err := parseRegistrationQuota(QuotaForNewUserOption, raw)
		return quota, false, err
	}
	if raw, present := options[InitialQuotaOption]; present {
		quota, err := parseRegistrationQuota(InitialQuotaOption, raw)
		return quota, true, err
	}
	return DefaultQuotaForNewUser, false, nil
}

func parseRegistrationQuota(option, raw string) (int, error) {
	if raw == "" {
		return DefaultQuotaForNewUser, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 10 {
		return 0, fmt.Errorf("%s must be an integer between 0 and %d", option, common.MaxQuota)
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%s must be an integer between 0 and %d", option, common.MaxQuota)
		}
	}
	quota, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || quota < 0 || quota > common.MaxQuota {
		return 0, fmt.Errorf("%s must be an integer between 0 and %d", option, common.MaxQuota)
	}
	return int(quota), nil
}

func parseDefaultUseAutoGroup(options map[string]string) (bool, error) {
	if options == nil {
		return false, nil
	}
	raw, present := options[DefaultUseAutoGroupOption]
	if !present || raw == "" {
		return false, nil
	}
	if raw != "true" && raw != "false" {
		return false, fmt.Errorf("%s must be true or false", DefaultUseAutoGroupOption)
	}
	return raw == "true", nil
}

func parseRegistrationPolicyBool(options map[string]string, key string, fallback bool) (bool, error) {
	if options == nil {
		return fallback, nil
	}
	raw, present := options[key]
	if !present || raw == "" {
		return fallback, nil
	}
	switch raw {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", key)
	}
}

func parseRegistrationDefaultGroup(options map[string]string) (string, error) {
	if options == nil {
		return DefaultRegistrationGroup, nil
	}
	raw, present := options[DefaultGroupOption]
	if !present || raw == "" {
		return DefaultRegistrationGroup, nil
	}
	if !validGroupPolicyIdentifier(raw) {
		return "", fmt.Errorf("%s contains an invalid group", DefaultGroupOption)
	}
	return raw, nil
}

func parseGroupSpecialUsableGroups(options map[string]string) (map[string][]SpecialUsableGroupRule, error) {
	raw := defaultGroupSpecialUsableGroupJSON
	if options != nil {
		if configured, present := options[GroupSpecialUsableGroupOption]; present {
			raw = strings.TrimSpace(configured)
			if raw == "" {
				raw = defaultGroupSpecialUsableGroupJSON
			}
		}
	}
	if len(raw) > maxSpecialUsableGroupOptionBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("%s must be bounded valid UTF-8 JSON", GroupSpecialUsableGroupOption)
	}
	if err := common.ValidateJSONNoDuplicateKeys([]byte(raw)); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", GroupSpecialUsableGroupOption, err)
	}
	var decoded map[string]map[string]string
	if err := common.UnmarshalJsonStr(raw, &decoded); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", GroupSpecialUsableGroupOption, err)
	}
	if decoded == nil {
		return nil, fmt.Errorf("%s must be a JSON object", GroupSpecialUsableGroupOption)
	}
	if len(decoded) > maxSpecialUsableUserGroups {
		return nil, fmt.Errorf("%s contains too many user groups", GroupSpecialUsableGroupOption)
	}

	userGroups := make([]string, 0, len(decoded))
	for userGroup := range decoded {
		userGroups = append(userGroups, userGroup)
	}
	sort.Strings(userGroups)

	result := make(map[string][]SpecialUsableGroupRule, len(decoded))
	totalRules := 0
	for _, userGroup := range userGroups {
		if !validGroupPolicyIdentifier(userGroup) {
			return nil, fmt.Errorf("%s contains invalid user group %q", GroupSpecialUsableGroupOption, userGroup)
		}
		rawRules := decoded[userGroup]
		if rawRules == nil {
			return nil, fmt.Errorf("%s entry for %q must be a JSON object", GroupSpecialUsableGroupOption, userGroup)
		}
		if len(rawRules) > maxSpecialUsableRulesPerGroup {
			return nil, fmt.Errorf("%s entry for %q contains too many rules", GroupSpecialUsableGroupOption, userGroup)
		}
		totalRules += len(rawRules)
		if totalRules > maxSpecialUsableRules {
			return nil, fmt.Errorf("%s contains too many rules", GroupSpecialUsableGroupOption)
		}

		rawKeys := make([]string, 0, len(rawRules))
		for rawKey := range rawRules {
			rawKeys = append(rawKeys, rawKey)
		}
		sort.Strings(rawKeys)
		seenTargets := make(map[string]SpecialUsableGroupAction, len(rawRules))
		rules := make([]SpecialUsableGroupRule, 0, len(rawRules))
		for _, rawKey := range rawKeys {
			action, target := parseSpecialUsableGroupKey(rawKey)
			if !validGroupPolicyIdentifier(target) || strings.HasPrefix(target, "+:") || strings.HasPrefix(target, "-:") {
				return nil, fmt.Errorf("%s contains invalid target group %q", GroupSpecialUsableGroupOption, target)
			}
			description := rawRules[rawKey]
			if !validGroupPolicyText(description, maxGroupPolicyDescriptionBytes, true) {
				return nil, fmt.Errorf("%s contains invalid description for %q", GroupSpecialUsableGroupOption, target)
			}
			if previous, duplicate := seenTargets[target]; duplicate {
				return nil, fmt.Errorf(
					"%s contains conflicting or duplicate rules for %q (%d and %d)",
					GroupSpecialUsableGroupOption, target, previous, action,
				)
			}
			seenTargets[target] = action
			rules = append(rules, SpecialUsableGroupRule{Action: action, Group: target, Description: description})
		}
		sort.Slice(rules, func(i, j int) bool { return rules[i].Group < rules[j].Group })
		result[userGroup] = rules
	}
	return result, nil
}

func parseSpecialUsableGroupKey(raw string) (SpecialUsableGroupAction, string) {
	if strings.HasPrefix(raw, "-:") {
		return SpecialUsableGroupRemove, strings.TrimPrefix(raw, "-:")
	}
	if strings.HasPrefix(raw, "+:") {
		return SpecialUsableGroupAdd, strings.TrimPrefix(raw, "+:")
	}
	return SpecialUsableGroupAdd, raw
}

func validGroupPolicyIdentifier(value string) bool {
	return validGroupPolicyText(value, maxGroupPolicyIdentifierBytes, false)
}

func validGroupPolicyText(value string, maximumBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximumBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}
