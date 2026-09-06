package auth

import (
	"errors"
	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

// rbacModel is the Casbin RBAC model: subject = role name, object = resource
// path, action = HTTP method. keyMatch gives path-pattern matching; an action
// of "*" acts as a wildcard.
const rbacModel = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && keyMatch(r.obj, p.obj) && (r.act == p.act || p.act == "*")
`

// defaultPolicies seed the RBAC model on first init: root can access everything,
// admin can access the dashboard API.
var defaultPolicies = [][]string{
	{"root", "*", "*"},
	{"admin", "/api/*", "*"},
}

var casbinEnforcer *casbin.Enforcer

// InitCasbin loads CasbinRule policies into an in-memory enforcer, seeding
// defaults when no policies exist yet.
func InitCasbin() error {
	m, err := casbinmodel.NewModelFromString(rbacModel)
	if err != nil {
		return err
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		return err
	}
	var rules []model.CasbinRule
	if err := model.DB.Where("ptype = ?", "p").Find(&rules).Error; err != nil {
		return err
	}
	if len(rules) == 0 {
		for _, p := range defaultPolicies {
			_, _ = e.AddPolicy(p[0], p[1], p[2])
		}
	}
	for _, r := range rules {
		if r.V0 != "" {
			_, _ = e.AddPolicy(r.V0, r.V1, r.V2)
		}
	}
	casbinEnforcer = e
	return nil
}

// roleName maps a user role int to a casbin subject.
func roleName(role int) string {
	if role >= roles.RoleRootUser {
		return "root"
	}
	if role >= roles.RoleAdminUser {
		return "admin"
	}
	return "user"
}

// Authorize checks whether a user role may perform an action on a resource.
func Authorize(role int, resource, action string) (bool, error) {
	if casbinEnforcer == nil {
		return false, errors.New("authorization engine not initialized")
	}
	return casbinEnforcer.Enforce(roleName(role), resource, action)
}

// AddPolicy adds a Casbin policy for a role (resource/action), persisting it.
func AddPolicy(roleName, resource, action string) (bool, error) {
	if casbinEnforcer == nil {
		return false, errors.New("authorization engine not initialized")
	}
	ok, err := casbinEnforcer.AddPolicy(roleName, resource, action)
	if err != nil || !ok {
		return ok, err
	}
	rule := model.CasbinRule{Ptype: "p", V0: roleName, V1: resource, V2: action}
	if err := model.DB.Create(&rule).Error; err != nil {
		return false, err
	}
	return true, nil
}
