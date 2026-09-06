package auth

import (
	"fmt"
	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Fine-grained permission layer (reference contract): resources expose
// actions; built-in roles carry baseline grants; per-user overrides win over
// baselines; the root role is a superuser that bypasses the enforcer.
//
// Policies live in the shared casbin_rule table under a dedicated enforcer
// with a sub/obj/act/eft model. Subject names are "user:<id>" and
// "role:<key>"; the legacy role x path x method rules loaded alongside are
// inert in this model because the matcher requires exact subject/object/
// action equality.

const (
	ResourceChannel = "channel"

	ActionRead           = "read"
	ActionOperate        = "operate"
	ActionWrite          = "write"
	ActionSensitiveWrite = "sensitive_write"
	ActionSecretView     = "secret_view"

	builtInRoleRoot  = "root"
	builtInRoleAdmin = "admin"

	effectAllow = "allow"
	effectDeny  = "deny"

	// managedRoleKey is the role whose baseline per-user overrides are
	// expressed relative to.
	managedRoleKey = builtInRoleAdmin
)

// ChannelRead is the read permission on channels.
var ChannelRead = Permission{Resource: ResourceChannel, Action: ActionRead}

// ChannelOperate is the operate permission on channels.
var ChannelOperate = Permission{Resource: ResourceChannel, Action: ActionOperate}

// ChannelWrite is the write permission on channels.
var ChannelWrite = Permission{Resource: ResourceChannel, Action: ActionWrite}

// ChannelSensitiveWrite is the sensitive-write permission on channels.
var ChannelSensitiveWrite = Permission{Resource: ResourceChannel, Action: ActionSensitiveWrite}

// ChannelSecretView is the secret-view permission on channels.
var ChannelSecretView = Permission{Resource: ResourceChannel, Action: ActionSecretView}

// Permission identifies a single action on a resource.
type Permission struct {
	Resource string
	Action   string
}

// PermissionsMap is a resource -> action -> allowed lookup.
type PermissionsMap map[string]map[string]bool

// ActionDefinition describes a single action exposed by a resource.
// DefaultRoles lists the role keys that receive this action as part of their
// baseline grants.
type ActionDefinition struct {
	Action         string   `json:"action"`
	LabelKey       string   `json:"label_key"`
	DescriptionKey string   `json:"description_key"`
	DefaultRoles   []string `json:"-"`
}

// ResourceDefinition describes a resource and the actions it exposes.
type ResourceDefinition struct {
	Resource string             `json:"resource"`
	LabelKey string             `json:"label_key"`
	Actions  []ActionDefinition `json:"actions"`
}

// RoleDescriptor exposes a role together with its baseline grant matrix.
type RoleDescriptor struct {
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	BuiltIn   bool           `json:"built_in"`
	Superuser bool           `json:"superuser"`
	Grants    PermissionsMap `json:"grants"`
}

type roleSpec struct {
	Key         string
	Name        string
	Description string
	BuiltIn     bool
	Superuser   bool
	Sort        int
}

var builtInRoleSpecs = []roleSpec{
	{Key: builtInRoleRoot, Name: "Root", Description: "Built-in root authorization role", BuiltIn: true, Superuser: true, Sort: 0},
	{Key: builtInRoleAdmin, Name: "Admin", Description: "Built-in admin authorization role", BuiltIn: true, Superuser: false, Sort: 10},
}

var permissionRegistry []ResourceDefinition

// RegisterPermissionResource adds a resource definition to the registry.
func RegisterPermissionResource(resource ResourceDefinition) {
	permissionRegistry = append(permissionRegistry, resource)
}

func init() {
	RegisterPermissionResource(ResourceDefinition{
		Resource: ResourceChannel,
		LabelKey: "Channel Management",
		Actions: []ActionDefinition{
			{
				Action:         ActionRead,
				LabelKey:       "Read channels",
				DescriptionKey: "View channel lists and details without secrets.",
				DefaultRoles:   []string{builtInRoleAdmin},
			},
			{
				Action:         ActionOperate,
				LabelKey:       "Operate channels",
				DescriptionKey: "Test channels, refresh balances, and enable/disable individual, batch, or tagged channels.",
				DefaultRoles:   []string{builtInRoleAdmin},
			},
			{
				Action:         ActionWrite,
				LabelKey:       "Edit channel routing",
				DescriptionKey: "Edit non-sensitive settings such as models, groups, and routing rules.",
				DefaultRoles:   []string{builtInRoleAdmin},
			},
			{
				Action:         ActionSensitiveWrite,
				LabelKey:       "Edit sensitive channel settings",
				DescriptionKey: "Create channels or edit keys, base URLs, and overrides.",
			},
			{
				Action:         ActionSecretView,
				LabelKey:       "View channel secrets",
				DescriptionKey: "Reserved for viewing complete channel keys after secure verification.",
			},
		},
	})
}

// PermissionCatalog returns a copy of the registered resource definitions
// (DefaultRoles omitted, matching the reference wire contract).
func PermissionCatalog() []ResourceDefinition {
	result := make([]ResourceDefinition, 0, len(permissionRegistry))
	for _, resource := range permissionRegistry {
		result = append(result, ResourceDefinition{
			Resource: resource.Resource,
			LabelKey: resource.LabelKey,
			Actions:  append([]ActionDefinition(nil), resource.Actions...),
		})
	}
	return result
}

// PermissionRoles returns the role descriptors with their baseline grants.
func PermissionRoles() []RoleDescriptor {
	result := make([]RoleDescriptor, 0, len(builtInRoleSpecs))
	for _, spec := range builtInRoleSpecs {
		result = append(result, RoleDescriptor{
			Key:       spec.Key,
			Name:      spec.Name,
			BuiltIn:   spec.BuiltIn,
			Superuser: spec.Superuser,
			Grants:    permissionRoleGrants(spec),
		})
	}
	return result
}

func permissionRoleGrants(spec roleSpec) PermissionsMap {
	grants := make(PermissionsMap, len(permissionRegistry))
	for _, resource := range permissionRegistry {
		actions := make(map[string]bool, len(resource.Actions))
		for _, action := range resource.Actions {
			actions[action.Action] = spec.Superuser || actionHasPermissionRole(action, spec.Key)
		}
		grants[resource.Resource] = actions
	}
	return grants
}

func actionHasPermissionRole(action ActionDefinition, roleKey string) bool {
	for _, r := range action.DefaultRoles {
		if r == roleKey {
			return true
		}
	}
	return false
}

// PermissionsForRole returns the permissions whose DefaultRoles include
// roleKey.
func PermissionsForRole(roleKey string) []Permission {
	permissions := make([]Permission, 0)
	for _, resource := range permissionRegistry {
		for _, action := range resource.Actions {
			if actionHasPermissionRole(action, roleKey) {
				permissions = append(permissions, Permission{Resource: resource.Resource, Action: action.Action})
			}
		}
	}
	return permissions
}

func isKnownPermission(permission Permission) bool {
	for _, resource := range permissionRegistry {
		if resource.Resource != permission.Resource {
			continue
		}
		for _, action := range resource.Actions {
			if action.Action == permission.Action {
				return true
			}
		}
	}
	return false
}

func catalogPermissionActions(resource string) []ActionDefinition {
	for _, known := range permissionRegistry {
		if known.Resource == resource {
			return known.Actions
		}
	}
	return nil
}

func isSuperuserPermissionRole(roleKey string) bool {
	for _, spec := range builtInRoleSpecs {
		if spec.Key == roleKey {
			return spec.Superuser
		}
	}
	return false
}

// UserSubject is the permission subject string for a single user.
func UserSubject(userID int) string {
	return "user:" + strconv.Itoa(userID)
}

// RoleSubject is the permission subject string for a role.
func RoleSubject(roleKey string) string {
	return "role:" + roleKey
}

// resolvePermissionRoles maps a system role to permission role keys.
func resolvePermissionRoles(systemRole int) []string {
	switch {
	case systemRole >= roles.RoleRootUser:
		return []string{builtInRoleRoot}
	case systemRole >= roles.RoleAdminUser:
		return []string{builtInRoleAdmin}
	default:
		return nil
	}
}

// permissionEnforcer is the dedicated enforcer for fine-grained permissions.
var (
	permissionEnforcerMu sync.RWMutex
	permissionEnforcer   *casbin.SyncedEnforcer
)

const permissionModelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act, eft

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && r.obj == p.obj && r.act == p.act && p.eft == "allow"
`

// InitPermissionAuthz builds the permission enforcer over the shared
// casbin_rule table and (re)seeds the built-in role baseline policies. The
// baseline reset runs before the policy load so the snapshot only ever
// contains fresh role grants.
func InitPermissionAuthz() error {
	if err := seedBuiltInAuthzRoles(model.DB); err != nil {
		return err
	}
	if err := resetBuiltInPermissionRolePolicies(model.DB); err != nil {
		return err
	}
	m, err := casbinmodel.NewModelFromString(permissionModelText)
	if err != nil {
		return err
	}
	e, err := casbin.NewSyncedEnforcer(m, newPermissionGormAdapter(model.DB))
	if err != nil {
		return err
	}
	e.EnableAutoSave(true)
	for _, spec := range builtInRoleSpecs {
		if spec.Superuser {
			continue
		}
		for _, permission := range PermissionsForRole(spec.Key) {
			if _, err := e.AddPolicy(RoleSubject(spec.Key), permission.Resource, permission.Action, effectAllow); err != nil {
				return err
			}
		}
	}
	permissionEnforcerMu.Lock()
	permissionEnforcer = e
	permissionEnforcerMu.Unlock()
	return nil
}

func seedBuiltInAuthzRoles(db *gorm.DB) error {
	for _, spec := range builtInRoleSpecs {
		role := model.AuthzRole{
			Key:         spec.Key,
			Name:        spec.Name,
			Description: spec.Description,
			BuiltIn:     spec.BuiltIn,
			Enabled:     true,
			Sort:        spec.Sort,
		}
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name",
				"description",
				"built_in",
				"enabled",
				"sort",
			}),
		}).Create(&role).Error; err != nil {
			return err
		}
	}
	return nil
}

func resetBuiltInPermissionRolePolicies(db *gorm.DB) error {
	subjects := make([]string, 0, len(builtInRoleSpecs))
	for _, spec := range builtInRoleSpecs {
		subjects = append(subjects, RoleSubject(spec.Key))
	}
	return db.Where("ptype = ? AND v0 IN ?", "p", subjects).Delete(&model.CasbinRule{}).Error
}

// ReloadPermissionPolicy reloads the permission enforcer snapshot from the
// database (used after transactional policy writes and by the multi-node
// sync loop).
func ReloadPermissionPolicy() error {
	permissionEnforcerMu.Lock()
	defer permissionEnforcerMu.Unlock()
	if permissionEnforcer == nil {
		return fmt.Errorf("permission enforcer is not initialized")
	}
	return permissionEnforcer.LoadPolicy()
}

// StartPermissionPolicySync periodically reloads the permission snapshot so
// other nodes in a multi-node deployment honor policy writes (mirrors the
// option-sync cadence).
func StartPermissionPolicySync(frequency int) {
	if frequency <= 0 {
		return
	}
	go func() {
		for {
			time.Sleep(time.Duration(frequency) * time.Second)
			if err := ReloadPermissionPolicy(); err != nil {
				logging.SysError("failed to reload permission policy: " + err.Error())
			}
		}
	}()
}

func currentPermissionEnforcer() *casbin.SyncedEnforcer {
	permissionEnforcerMu.RLock()
	defer permissionEnforcerMu.RUnlock()
	return permissionEnforcer
}

// Can reports whether the subject may perform the permission. A superuser
// role short-circuits to allow. Otherwise a per-user override wins, then the
// subject's role baselines apply. An uninitialized enforcer fails closed.
func Can(userID int, systemRole int, permission Permission) bool {
	roles := resolvePermissionRoles(systemRole)
	if len(roles) == 0 {
		return false
	}
	for _, role := range roles {
		if isSuperuserPermissionRole(role) {
			return true
		}
	}
	if !isKnownPermission(permission) {
		return false
	}
	e := currentPermissionEnforcer()
	if e == nil {
		return false
	}
	if effect, ok := explicitPermissionSubjectEffect(e, UserSubject(userID), permission); ok {
		return effect == effectAllow
	}
	for _, role := range roles {
		if permissionRoleBaselineAllows(e, role, permission) {
			return true
		}
	}
	return false
}

// Capabilities returns the full resource/action matrix the subject is
// allowed.
func Capabilities(userID int, systemRole int) PermissionsMap {
	result := make(PermissionsMap, len(permissionRegistry))
	for _, resource := range permissionRegistry {
		actions := make(map[string]bool, len(resource.Actions))
		for _, action := range resource.Actions {
			actions[action.Action] = Can(userID, systemRole, Permission{
				Resource: resource.Resource,
				Action:   action.Action,
			})
		}
		result[resource.Resource] = actions
	}
	return result
}

// SetUserPermissionsInTx persists per-user permission overrides inside tx.
// Entries that match the managed-role baseline are omitted; entries that
// differ are written as allow/deny policies. The caller must reload the
// enforcer snapshot after the transaction commits.
func SetUserPermissionsInTx(tx *gorm.DB, userID int, permissions PermissionsMap) error {
	e := currentPermissionEnforcer()
	if e == nil {
		return fmt.Errorf("permission enforcer is not initialized")
	}
	for resource, actions := range permissions {
		if !isKnownResourcePermission(resource) {
			continue
		}
		if err := tx.Where("ptype = ? AND v0 = ? AND v1 = ?", "p", UserSubject(userID), resource).
			Delete(&model.CasbinRule{}).Error; err != nil {
			return err
		}
		policies := permissionUserOverridePolicies(e, resource, actions)
		if len(policies) == 0 {
			continue
		}
		rules := make([]model.CasbinRule, 0, len(policies))
		for _, policy := range policies {
			rules = append(rules, model.CasbinRule{Ptype: "p", V0: UserSubject(userID), V1: policy.Resource, V2: policy.Action, V3: policy.Effect})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rules).Error; err != nil {
			return err
		}
	}
	return nil
}

// ClearUserAuthorizationInTx removes every per-user permission policy for
// userID inside tx.
func ClearUserAuthorizationInTx(tx *gorm.DB, userID int) error {
	for _, resource := range permissionRegistry {
		if err := tx.Where("ptype = ? AND v0 = ? AND v1 = ?", "p", UserSubject(userID), resource.Resource).
			Delete(&model.CasbinRule{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func isKnownResourcePermission(resource string) bool {
	for _, known := range permissionRegistry {
		if known.Resource == resource {
			return true
		}
	}
	return false
}

// ExplicitUserOverrides returns only the per-user override entries.
func ExplicitUserOverrides(userID int) PermissionsMap {
	e := currentPermissionEnforcer()
	if e == nil {
		return PermissionsMap{}
	}
	result := PermissionsMap{}
	for _, resource := range permissionRegistry {
		policies, err := e.GetFilteredPolicy(0, UserSubject(userID), resource.Resource)
		if err != nil {
			return PermissionsMap{}
		}
		actions := make(map[string]bool, len(policies))
		for _, policy := range policies {
			if len(policy) >= 3 && isKnownPermission(Permission{Resource: policy[1], Action: policy[2]}) {
				effect := permissionPolicyEffect(policy)
				if effect == effectAllow || effect == effectDeny {
					actions[policy[2]] = effect == effectAllow
				}
			}
		}
		if len(actions) > 0 {
			result[resource.Resource] = actions
		}
	}
	return result
}

type permissionOverridePolicy struct {
	Resource string
	Action   string
	Effect   string
}

// permissionUserOverridePolicies returns the override entries that differ
// from the managed-role baseline; entries matching the baseline are omitted.
func permissionUserOverridePolicies(e *casbin.SyncedEnforcer, resource string, actions map[string]bool) []permissionOverridePolicy {
	overrides := make([]permissionOverridePolicy, 0, len(actions))
	for _, action := range catalogPermissionActions(resource) {
		desired, ok := actions[action.Action]
		if !ok {
			continue
		}
		permission := Permission{Resource: resource, Action: action.Action}
		if desired == permissionRoleBaselineAllows(e, managedRoleKey, permission) {
			continue
		}
		effect := effectDeny
		if desired {
			effect = effectAllow
		}
		overrides = append(overrides, permissionOverridePolicy{
			Resource: resource,
			Action:   action.Action,
			Effect:   effect,
		})
	}
	sort.Slice(overrides, func(i, j int) bool {
		return overrides[i].Action < overrides[j].Action
	})
	return overrides
}

func permissionRoleBaselineAllows(e *casbin.SyncedEnforcer, roleKey string, permission Permission) bool {
	effect, ok := explicitPermissionSubjectEffect(e, RoleSubject(roleKey), permission)
	return ok && effect == effectAllow
}

func explicitPermissionSubjectEffect(e *casbin.SyncedEnforcer, subject string, permission Permission) (string, bool) {
	policies, err := e.GetFilteredPolicy(0, subject, permission.Resource, permission.Action)
	if err != nil {
		return "", false
	}
	hasAllow := false
	for _, policy := range policies {
		switch permissionPolicyEffect(policy) {
		case effectDeny:
			return effectDeny, true
		case effectAllow:
			hasAllow = true
		}
	}
	if hasAllow {
		return effectAllow, true
	}
	return "", false
}

func permissionPolicyEffect(policy []string) string {
	if len(policy) < 4 || policy[3] == "" {
		return effectAllow
	}
	return policy[3]
}
