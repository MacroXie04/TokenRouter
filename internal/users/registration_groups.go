package users

import (
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxResolvedGroupIdentifierBytes  = 64
	MaxResolvedGroupDescriptionBytes = 256
)

// ResolvedUsableGroup is one safe, positively priced group after applying the
// current user's special visibility directives. Results are sorted by Name.
type ResolvedUsableGroup struct {
	Name        string
	Description string
}

// GroupRatioLookup returns the current base ratio for a concrete relay group.
// Missing, non-positive, NaN, and infinite ratios always fail closed.
type GroupRatioLookup func(group string) (float64, bool)

// ResolveSpecialUsableGroups applies the reference-compatible visibility
// rules without letting them bypass TokenRouter's positive-ratio invariant.
// The returned slice is deterministic, detached, and duplicate-free.
func ResolveSpecialUsableGroups(
	policy setting.RegistrationGroupPolicy,
	userGroup string,
	base map[string]string,
	ratioLookup GroupRatioLookup,
) []ResolvedUsableGroup {
	candidates := make(map[string]string, len(base)+1)
	for group, description := range base {
		if !ValidResolvedGroupText(group, MaxResolvedGroupIdentifierBytes, false) {
			continue
		}
		if !ValidResolvedGroupText(description, MaxResolvedGroupDescriptionBytes, true) {
			description = ""
		}
		candidates[group] = description
	}
	for _, rule := range policy.SpecialUsableGroupRules(userGroup) {
		switch rule.Action {
		case setting.SpecialUsableGroupRemove:
			delete(candidates, rule.Group)
		case setting.SpecialUsableGroupAdd:
			candidates[rule.Group] = rule.Description
		}
	}
	// Reference behavior always restores the user's own group after applying
	// removals. It is still subject to the same positive-ratio gate below.
	if ValidResolvedGroupText(userGroup, MaxResolvedGroupIdentifierBytes, false) {
		if _, present := candidates[userGroup]; !present {
			candidates[userGroup] = "用户分组"
		}
	}

	groups := make([]ResolvedUsableGroup, 0, len(candidates))
	for group, description := range candidates {
		if group == GroupAuto || ratioLookup == nil {
			continue
		}
		ratio, configured := ratioLookup(group)
		if !configured || ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			continue
		}
		groups = append(groups, ResolvedUsableGroup{Name: group, Description: description})
	}
	sortResolvedUsableGroups(groups)
	return groups
}

func sortResolvedUsableGroups(groups []ResolvedUsableGroup) {
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
}

func ValidResolvedGroupText(value string, maximumBytes int, allowEmpty bool) bool {
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
