package setting

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	// UserUsableGroupsOption is a JSON object whose keys are groups users may
	// select for their tokens. Values are display labels reserved for clients.
	// The literal "auto" is presentation metadata for the automatic-group
	// selector; relay authorization continues to reject it as a concrete group.
	UserUsableGroupsOption = "UserUsableGroups"

	defaultUserUsableGroupsJSON = `{"default":"default","vip":"vip"}`
)

type userUsableGroupsSnapshot struct {
	groups map[string]string
}

// groupRoutingSnapshot is published as one immutable value so readers never
// observe a new user allow-list with an old AutoGroups/limit (or vice versa)
// during an UpdateOptions or multi-node Sync.
type groupRoutingSnapshot struct {
	userGroups userUsableGroupsSnapshot
	autoGroups autoGroupsSnapshot
}

var groupRoutingConfig atomic.Pointer[groupRoutingSnapshot]

func init() {
	config, _ := buildGroupRoutingSetting(nil)
	groupRoutingConfig.Store(&config)
}

// UserUsableGroupsOptionDefault returns the stable serialized default exposed
// by the root settings endpoint when the option has not been persisted yet.
func UserUsableGroupsOptionDefault() string {
	return defaultUserUsableGroupsJSON
}

// GetUserUsableGroups returns a copy so callers cannot mutate the live setting.
func GetUserUsableGroups() map[string]string {
	config := groupRoutingConfig.Load()
	if config == nil {
		fallback, _ := buildGroupRoutingSetting(nil)
		return cloneUserUsableGroups(fallback.userGroups.groups)
	}
	return cloneUserUsableGroups(config.userGroups.groups)
}

// IsUserUsableGroup reports whether a group is present in the configured
// user-selectable set. A user's own group is added by the service layer.
func IsUserUsableGroup(group string) bool {
	config := groupRoutingConfig.Load()
	if config == nil {
		return false
	}
	_, ok := config.userGroups.groups[group]
	return ok
}

func buildGroupRoutingSetting(options map[string]string) (groupRoutingSnapshot, error) {
	userGroups, err := buildUserUsableGroups(options)
	if err != nil {
		return groupRoutingSnapshot{}, err
	}
	autoGroups, err := buildAutoGroups(options)
	if err != nil {
		return groupRoutingSnapshot{}, err
	}
	return groupRoutingSnapshot{userGroups: userGroups, autoGroups: autoGroups}, nil
}

func buildUserUsableGroups(options map[string]string) (userUsableGroupsSnapshot, error) {
	raw := ""
	if options != nil {
		raw = strings.TrimSpace(options[UserUsableGroupsOption])
	}
	if raw == "" {
		raw = defaultUserUsableGroupsJSON
	}

	var groups map[string]string
	if err := common.UnmarshalJsonStr(raw, &groups); err != nil {
		return userUsableGroupsSnapshot{}, fmt.Errorf("invalid %s JSON: %w", UserUsableGroupsOption, err)
	}
	if groups == nil {
		return userUsableGroupsSnapshot{}, fmt.Errorf("%s must be a JSON object", UserUsableGroupsOption)
	}

	validated := make(map[string]string, len(groups))
	for group, label := range groups {
		trimmed := strings.TrimSpace(group)
		if trimmed == "" || trimmed != group {
			return userUsableGroupsSnapshot{}, fmt.Errorf("%s contains invalid group %q", UserUsableGroupsOption, group)
		}
		validated[group] = label
	}
	return userUsableGroupsSnapshot{groups: validated}, nil
}

func cloneUserUsableGroups(groups map[string]string) map[string]string {
	copyOfGroups := make(map[string]string, len(groups))
	for group, label := range groups {
		copyOfGroups[group] = label
	}
	return copyOfGroups
}
