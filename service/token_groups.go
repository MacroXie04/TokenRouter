package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

const GroupAuto = "auto"

var (
	ErrTooManyAutoGroups   = errors.New("too many auto groups")
	ErrDuplicateAutoGroup  = errors.New("duplicate auto group")
	ErrInvalidAutoGroup    = errors.New("invalid auto group")
	ErrUnauthorizedGroup   = errors.New("group is not selectable by user")
	ErrNoAuthorizedGroup   = errors.New("no authorized relay group")
	ErrMalformedAutoGroups = errors.New("malformed token auto groups")
)

// RelayGroupPolicy is an immutable per-request authorization decision. Groups
// is ordered and never contains the literal "auto" marker.
type RelayGroupPolicy struct {
	Groups          []string
	Auto            bool
	CrossGroupRetry bool
}

// IsUserSelectableGroup reports whether group can be selected by a user in
// userGroup. The configured selectable set is augmented with the user's own
// group, but every candidate must also have an explicit, finite, positive
// group ratio.
func IsUserSelectableGroup(userGroup, group string) bool {
	if group == "" || group == GroupAuto || group != strings.TrimSpace(group) {
		return false
	}
	if userGroup == "" {
		userGroup = GroupDefault
	}
	if _, allowed := GetUserUsableGroups(userGroup)[group]; !allowed {
		return false
	}

	pricingCacheMu.RLock()
	ratio, configured := groupRatiosCache[group]
	pricingCacheMu.RUnlock()
	return configured && ratio > 0 && !math.IsNaN(ratio) && !math.IsInf(ratio, 0)
}

// GetUserUsableGroups returns the configured group descriptions augmented
// with the user's own group, matching the dashboard group-selection contract.
// The returned map is an isolated copy. The optional "auto" presentation key
// is not a concrete relay group and remains rejected by IsUserSelectableGroup.
func GetUserUsableGroups(userGroup string) map[string]string {
	base := setting.GetUserUsableGroups()
	autoDescription, autoAllowed := base[GroupAuto]
	delete(base, GroupAuto)
	policy := setting.GetRegistrationGroupPolicy()
	for _, rule := range policy.SpecialUsableGroupRules(userGroup) {
		if rule.Group != GroupAuto {
			continue
		}
		if rule.Action == setting.SpecialUsableGroupRemove {
			autoAllowed = false
			autoDescription = ""
		} else {
			autoAllowed = true
			autoDescription = rule.Description
		}
	}
	ratios := getGroupRatios()
	resolved := ResolveSpecialUsableGroups(policy, userGroup, base, func(group string) (float64, bool) {
		ratio, present := ratios[group]
		return ratio, present
	})
	groups := make(map[string]string, len(resolved)+1)
	for _, group := range resolved {
		groups[group.Name] = group.Description
	}
	if autoAllowed && validResolvedGroupText(autoDescription, maxResolvedGroupDescriptionBytes, true) {
		groups[GroupAuto] = autoDescription
	}
	return groups
}

// GetUserAutoGroups returns the sorted set of groups a user may select. Sorting
// makes the API deterministic; submitted auto-group ordering is never changed.
func GetUserAutoGroups(userGroup string) []string {
	if userGroup == "" {
		userGroup = GroupDefault
	}
	candidates := GetUserUsableGroups(userGroup)

	out := make([]string, 0, len(candidates))
	for group := range candidates {
		if IsUserSelectableGroup(userGroup, group) {
			out = append(out, group)
		}
	}
	sort.Strings(out)
	return out
}

// GetUserDefaultAutoGroups returns the currently authorized subset of the
// operator-configured global AutoGroups list in configured order. The full
// list is returned so clients can edit a token even when its per-token runtime
// limit is smaller; the limit is exposed separately and enforced at runtime.
func GetUserDefaultAutoGroups(userGroup string) []string {
	groups := setting.GetAutoGroups()
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		if IsUserSelectableGroup(userGroup, group) {
			out = append(out, group)
		}
	}
	return out
}

// ValidateUserAutoGroups validates a submitted ordered group list without
// rewriting it, so persistence retains the caller's priority order.
func ValidateUserAutoGroups(userGroup string, groups []string, maxCount int) error {
	if maxCount < 1 || len(groups) > maxCount {
		return ErrTooManyAutoGroups
	}
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		trimmed := strings.TrimSpace(group)
		if trimmed == "" || trimmed == GroupAuto || trimmed != group {
			return fmt.Errorf("%w: %q", ErrInvalidAutoGroup, group)
		}
		if _, exists := seen[group]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateAutoGroup, group)
		}
		seen[group] = struct{}{}
		if !IsUserSelectableGroup(userGroup, group) {
			return fmt.Errorf("%w: %q", ErrUnauthorizedGroup, group)
		}
	}
	return nil
}

// ResolveRelayGroupPolicy revalidates a token against current user
// permissions, group ratios, global AutoGroups and the current bounded limit.
// It is intentionally called on every authenticated relay request so revoking
// a group takes effect without rewriting existing tokens.
func ResolveRelayGroupPolicy(userGroup string, token *model.Token) (RelayGroupPolicy, error) {
	if token == nil {
		return RelayGroupPolicy{}, ErrNoAuthorizedGroup
	}
	if userGroup == "" {
		userGroup = GroupDefault
	}

	if token.Group != GroupAuto {
		group := token.Group
		if group == "" {
			group = userGroup
		}
		if !IsUserSelectableGroup(userGroup, group) {
			return RelayGroupPolicy{}, fmt.Errorf("%w: %q", ErrUnauthorizedGroup, group)
		}
		return RelayGroupPolicy{Groups: []string{group}}, nil
	}

	groups, maxGroups := setting.GetAutoGroupConfig()
	if token.AutoGroups != "" {
		parsed, err := token.GetAutoGroupsStrict()
		if err != nil {
			return RelayGroupPolicy{}, fmt.Errorf("%w: %v", ErrMalformedAutoGroups, err)
		}
		groups = parsed
	}

	seen := make(map[string]struct{}, len(groups))
	filtered := make([]string, 0, min(len(groups), maxGroups))
	for _, group := range groups {
		if group == "" || group == GroupAuto || group != strings.TrimSpace(group) {
			return RelayGroupPolicy{}, fmt.Errorf("%w: invalid group %q", ErrMalformedAutoGroups, group)
		}
		if _, duplicate := seen[group]; duplicate {
			return RelayGroupPolicy{}, fmt.Errorf("%w: duplicate group %q", ErrMalformedAutoGroups, group)
		}
		seen[group] = struct{}{}
		if IsUserSelectableGroup(userGroup, group) && len(filtered) < maxGroups {
			filtered = append(filtered, group)
		}
	}
	if len(filtered) == 0 {
		return RelayGroupPolicy{}, ErrNoAuthorizedGroup
	}
	return RelayGroupPolicy{
		Groups: append([]string(nil), filtered...), Auto: true,
		CrossGroupRetry: token.CrossGroupRetry,
	}, nil
}

// GetGroupsModels returns the union of enabled models for authorized groups.
func GetGroupsModels(groups []string) map[string]bool {
	models := make(map[string]bool)
	for _, group := range groups {
		for modelName := range GetGroupModels(group) {
			models[modelName] = true
		}
	}
	return models
}
