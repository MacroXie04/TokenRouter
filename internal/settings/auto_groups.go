package settings

import (
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"strconv"
	"strings"
)

const (
	DefaultMaxTokenAutoGroups = 5
	// MaxTokenAutoGroupsUpperBound prevents an operator typo or hostile option
	// write from turning each relay request into an unbounded routing scan.
	MaxTokenAutoGroupsUpperBound = 64

	defaultAutoGroupsJSON = `["default"]`
)

type autoGroupsSnapshot struct {
	groups   []string
	maxCount int
}

// AutoGroupsOptionDefault is the stable default exposed to operators.
func AutoGroupsOptionDefault() string { return defaultAutoGroupsJSON }

// MaxTokenAutoGroupsOptionDefault is the stable serialized default exposed to
// operators.
func MaxTokenAutoGroupsOptionDefault() string {
	return strconv.Itoa(DefaultMaxTokenAutoGroups)
}

// GetAutoGroups returns a copy of the ordered global candidate list.
func GetAutoGroups() []string {
	groups, _ := GetAutoGroupConfig()
	return groups
}

// GetMaxTokenAutoGroups returns the validated, bounded live limit.
func GetMaxTokenAutoGroups() int {
	_, maxCount := GetAutoGroupConfig()
	return maxCount
}

// GetAutoGroupConfig returns the ordered defaults and limit from the same
// atomic snapshot.
func GetAutoGroupConfig() ([]string, int) {
	config := groupRoutingConfig.Load()
	if config == nil {
		return nil, DefaultMaxTokenAutoGroups
	}
	return append([]string(nil), config.autoGroups.groups...), config.autoGroups.maxCount
}

func buildAutoGroups(options map[string]string) (autoGroupsSnapshot, error) {
	rawGroups := ""
	rawMax := ""
	if options != nil {
		rawGroups = strings.TrimSpace(options[AutoGroupsOption])
		rawMax = strings.TrimSpace(options[MaxTokenAutoGroupsOption])
	}
	if rawGroups == "" {
		rawGroups = defaultAutoGroupsJSON
	}
	if rawMax == "" {
		rawMax = strconv.Itoa(DefaultMaxTokenAutoGroups)
	}

	maxCount, err := strconv.Atoi(rawMax)
	if err != nil || maxCount < 1 || maxCount > MaxTokenAutoGroupsUpperBound {
		return autoGroupsSnapshot{}, fmt.Errorf("%s must be an integer between 1 and %d", MaxTokenAutoGroupsOption, MaxTokenAutoGroupsUpperBound)
	}

	var groups []string
	if err := jsonutil.UnmarshalJsonStr(rawGroups, &groups); err != nil {
		return autoGroupsSnapshot{}, fmt.Errorf("invalid %s JSON: %w", AutoGroupsOption, err)
	}
	if groups == nil {
		return autoGroupsSnapshot{}, fmt.Errorf("%s must be a JSON array", AutoGroupsOption)
	}
	if len(groups) > MaxTokenAutoGroupsUpperBound {
		return autoGroupsSnapshot{}, fmt.Errorf("%s cannot contain more than %d groups", AutoGroupsOption, MaxTokenAutoGroupsUpperBound)
	}
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if group == "" || group == "auto" || group != strings.TrimSpace(group) {
			return autoGroupsSnapshot{}, fmt.Errorf("%s contains invalid group %q", AutoGroupsOption, group)
		}
		if _, duplicate := seen[group]; duplicate {
			return autoGroupsSnapshot{}, fmt.Errorf("%s contains duplicate group %q", AutoGroupsOption, group)
		}
		seen[group] = struct{}{}
	}
	return autoGroupsSnapshot{groups: append([]string(nil), groups...), maxCount: maxCount}, nil
}
