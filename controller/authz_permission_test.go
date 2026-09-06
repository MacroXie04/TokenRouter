package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// permissionSession bundles an authenticated do-factory for one user.
type permissionSession struct {
	userID int
	do     func(method, path, body string) *httptest.ResponseRecorder
}

// setupPermissionTest builds an isolated router with the permission engine
// initialized, plus sessions for root, an admin (baseline grants only), and
// a common user.
func setupPermissionTest(t *testing.T) (root, admin, plain permissionSession) {
	t.Helper()
	handler, _, rootID := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, service.InitPermissionAuthz())

	makeSession := func(username string, role int, ip string) permissionSession {
		u := model.User{Username: username, Password: "pw", Role: role,
			Status: model.UserStatusEnabled, Quota: 1000, AuthVersion: 1}
		require.NoError(t, model.DB.Create(&u).Error)
		sid, access, refresh, err := service.CompleteLogin(&u, ip, "ua", "test")
		require.NoError(t, err)
		return permissionSession{
			userID: u.Id,
			do: func(method, path, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
				req.AddCookie(&http.Cookie{Name: "refresh_token", Value: sid + "." + refresh})
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			},
		}
	}
	// Rebuild the root session through the same helper so all three share
	// the cookie shape (CompleteLogin above created a fresh user for rootID
	// in setupChannelRead; use a dedicated root user instead).
	_ = rootID
	return makeSession("permroot", constant.RoleRootUser, "127.0.0.1"),
		makeSession("permadmin", constant.RoleAdminUser, "127.0.0.2"),
		makeSession("permplain", constant.RoleCommonUser, "127.0.0.3")
}

func TestPermissionCatalogContract(t *testing.T) {
	root, _, plain := setupPermissionTest(t)

	rec := root.do(http.MethodGet, "/api/authz/catalog", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())

	// Resources: the channel resource with its five actions, without the
	// internal default_roles key.
	resources, ok := data["resources"].([]any)
	require.True(t, ok)
	require.Len(t, resources, 1)
	channelRes, ok := resources[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "channel", channelRes["resource"])
	assert.Equal(t, "Channel Management", channelRes["label_key"])
	actions, ok := channelRes["actions"].([]any)
	require.True(t, ok)
	assert.Len(t, actions, 5)
	wantActions := []string{"read", "operate", "write", "sensitive_write", "secret_view"}
	for i, raw := range actions {
		action, ok := raw.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, wantActions[i], action["action"])
		assert.NotEmpty(t, action["label_key"])
		assert.NotEmpty(t, action["description_key"])
		assert.NotContains(t, action, "default_roles", "internal grants must not leak")
	}

	// Roles: root is a superuser (all grants true); admin holds the
	// read/operate/write baselines only.
	roles, ok := data["roles"].([]any)
	require.True(t, ok)
	require.Len(t, roles, 2)
	byKey := map[string]map[string]any{}
	for _, raw := range roles {
		role, ok := raw.(map[string]any)
		require.True(t, ok)
		byKey[role["key"].(string)] = role
	}
	rootRole := byKey["root"]
	assert.Equal(t, true, rootRole["superuser"])
	rootGrants := rootRole["grants"].(map[string]any)["channel"].(map[string]any)
	for _, action := range wantActions {
		assert.Equal(t, true, rootGrants[action], "root must hold %s", action)
	}
	adminRole := byKey["admin"]
	assert.Equal(t, false, adminRole["superuser"])
	adminGrants := adminRole["grants"].(map[string]any)["channel"].(map[string]any)
	for _, action := range []string{"read", "operate", "write"} {
		assert.Equal(t, true, adminGrants[action], "admin baseline must hold %s", action)
	}
	for _, action := range []string{"sensitive_write", "secret_view"} {
		assert.Equal(t, false, adminGrants[action], "admin baseline must not hold %s", action)
	}

	// Non-admins are rejected by AdminAuth.
	rec = plain.do(http.MethodGet, "/api/authz/catalog", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestPermissionEnforcementOnChannelRoutes(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)
	createSearchChannel(t, "perm-tagged", 1, constant.ChannelStatusEnabled, "default", "gpt-4o", "t1", 5)

	// Baseline read routes are allowed for admins.
	rec := admin.do(http.MethodGet, "/api/channel/search", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	// Exact credential search is a sensitive operation. A baseline channel
	// reader gets no match, while the root's sensitive-write grant retains the
	// operator compatibility workflow. Neither response includes the key.
	rec = admin.do(http.MethodGet, "/api/channel/search?keyword=sk-secret-perm-tagged", "")
	items, _ := searchItems(t, rec)
	assert.Empty(t, items)
	rec = root.do(http.MethodGet, "/api/channel/search?keyword=sk-secret-perm-tagged", "")
	items, _ = searchItems(t, rec)
	require.Len(t, items, 1)
	assert.NotContains(t, rec.Body.String(), "sk-secret-perm-tagged")

	// Sensitive routes are denied with the reference forbidden shape.
	rec = admin.do(http.MethodPost, "/api/channel", `{"name":"ch","type":1,"key":"sk-x","models":"gpt-4o"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "无权限", decodeBody(t, rec)["message"])

	rec = admin.do(http.MethodDelete, "/api/channel/disabled", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = admin.do(http.MethodPost, "/api/channel/batch", `{"ids":[1]}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Write-baseline routes are allowed (tag edit without overrides).
	rec = admin.do(http.MethodPut, "/api/channel/tag", `{"tag":"t1","priority":9}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	// Inline guard: param/header overrides require sensitive_write.
	rec = admin.do(http.MethodPut, "/api/channel/tag", `{"tag":"t1","param_override":"{\"x\":1}"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// Inline guard: multi-key delete actions require sensitive_write.
	mk := createSearchChannel(t, "perm-mk", 1, constant.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&mk).Updates(map[string]any{
		"key":          "sk-a\nsk-b",
		"channel_info": `{"is_multi_key":true,"multi_key_size":2,"multi_key_polling_index":0}`,
	}).Error)
	rec = admin.do(http.MethodPost, "/api/channel/multi_key/manage",
		fmt.Sprintf(`{"channel_id":%d,"action":"delete_key","key_index":0}`, mk.Id))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// The superuser bypasses everything.
	rec = root.do(http.MethodPost, "/api/channel", `{"name":"ch2","type":1,"key":"sk-y","models":"gpt-4o"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	rec = root.do(http.MethodPost, "/api/channel/batch", `{"ids":[1]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestPermissionUpdateChannelSensitiveGuard(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)
	ch := createSearchChannel(t, "upd-target", 1, constant.ChannelStatusEnabled, "default", "gpt-4o", "", 0)
	require.NoError(t, model.DB.Model(&ch).Updates(map[string]any{"base_url": "http://up1", "key": "sk-orig"}).Error)

	// Non-sensitive fields (name) are editable with the write baseline.
	rec := admin.do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"name":"renamed"}`, ch.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	var refreshed model.Channel
	require.NoError(t, model.DB.First(&refreshed, ch.Id).Error)
	assert.Equal(t, "renamed", refreshed.Name)
	assert.Equal(t, "http://up1", refreshed.BaseURL, "untouched fields must survive")

	// The dashboard sends an empty value for its masked key field. It is an
	// unchanged sentinel, not permission to clear the stored credential.
	rec = admin.do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"name":"renamed-again","key":""}`, ch.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&refreshed, ch.Id).Error)
	assert.Equal(t, "renamed-again", refreshed.Name)
	assert.Equal(t, "sk-orig", refreshed.Key)

	// A sensitive field change is denied for a baseline admin.
	rec = admin.do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"base_url":"http://up2"}`, ch.Id))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&refreshed, ch.Id).Error)
	assert.Equal(t, "http://up1", refreshed.BaseURL)

	// Unknown request fields fail closed (treated as sensitive).
	rec = admin.do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"mystery":"x"}`, ch.Id))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// The superuser may change sensitive fields.
	rec = root.do(http.MethodPut, "/api/channel", fmt.Sprintf(`{"id":%d,"base_url":"http://up2"}`, ch.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&refreshed, ch.Id).Error)
	assert.Equal(t, "http://up2", refreshed.BaseURL)

	// Missing id is rejected.
	rec = root.do(http.MethodPut, "/api/channel", `{"name":"x"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAbilityRoutesHonorChannelPermissions(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)

	// Ability records are the channel routing table, so reads and mutations
	// must follow the same fine-grained channel grants as the channel surface.
	rec := root.do(http.MethodPost, "/api/ability",
		`{"group":"default","model":"gpt-4o","channel_id":1}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = root.do(http.MethodPut, "/api/user",
		fmt.Sprintf(`{"id":%d,"admin_permissions":{"channel":{"write":false}}}`, admin.userID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	rec = admin.do(http.MethodGet, "/api/ability", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = admin.do(http.MethodPost, "/api/ability",
		`{"group":"default","model":"gpt-4o-mini","channel_id":1}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "无权限", decodeBody(t, rec)["message"])
	rec = admin.do(http.MethodDelete, "/api/ability",
		`{"group":"default","model":"gpt-4o","channel_id":1}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = root.do(http.MethodDelete, "/api/ability",
		`{"group":"default","model":"gpt-4o","channel_id":1}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestPermissionOverridesViaUpdateUser(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)

	// Baseline admin cannot add channels.
	rec := admin.do(http.MethodPost, "/api/channel", `{"name":"ch3","type":1,"key":"sk-z","models":"gpt-4o"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Only root may update admin permissions.
	rec = admin.do(http.MethodPut, "/api/user",
		fmt.Sprintf(`{"id":%d,"admin_permissions":{"channel":{"sensitive_write":true}}}`, admin.userID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "仅 Root 用户可以更新管理权限", decodeBody(t, rec)["message"])

	// Root grants sensitive_write via the reference admin_permissions map.
	rec = root.do(http.MethodPut, "/api/user",
		fmt.Sprintf(`{"id":%d,"admin_permissions":{"channel":{"sensitive_write":true}}}`, admin.userID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	// The grant is effective immediately (policy reloaded after the tx).
	rec = admin.do(http.MethodPost, "/api/channel", `{"name":"ch4","type":1,"key":"sk-w","models":"gpt-4o"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	// The override is visible on the admin user detail payload.
	rec = root.do(http.MethodGet, "/api/user/"+common.Int2Str(admin.userID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeBody(t, rec)["data"].(map[string]any)
	perms := data["admin_permissions"].(map[string]any)["channel"].(map[string]any)
	assert.Equal(t, true, perms["sensitive_write"])
	assert.Equal(t, true, perms["read"], "baseline grants remain effective")

	// The override persists as a casbin rule.
	var count int64
	model.DB.Model(&model.CasbinRule{}).
		Where("ptype = ? AND v0 = ? AND v1 = ? AND v2 = ? AND v3 = ?",
			"p", service.UserSubject(admin.userID), "channel", "sensitive_write", "allow").
		Count(&count)
	assert.Equal(t, int64(1), count)

	// Revoking the override (explicit false) restores the baseline denial:
	// the entry matches the baseline so no policy row is kept, and the
	// admin is denied again.
	rec = root.do(http.MethodPut, "/api/user",
		fmt.Sprintf(`{"id":%d,"admin_permissions":{"channel":{"sensitive_write":false}}}`, admin.userID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	rec = admin.do(http.MethodPost, "/api/channel", `{"name":"ch5","type":1,"key":"sk-v","models":"gpt-4o"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	model.DB.Model(&model.CasbinRule{}).
		Where("ptype = ? AND v0 = ? AND v1 = ?", "p", service.UserSubject(admin.userID), "channel").
		Count(&count)
	assert.Equal(t, int64(0), count, "baseline-matching entries are omitted, not stored")

	// Revoking a baseline grant (read) stores an explicit deny override and
	// takes precedence over the role baseline.
	rec = root.do(http.MethodPut, "/api/user",
		fmt.Sprintf(`{"id":%d,"admin_permissions":{"channel":{"read":false}}}`, admin.userID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	rec = admin.do(http.MethodGet, "/api/channel/search", "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	model.DB.Model(&model.CasbinRule{}).
		Where("ptype = ? AND v0 = ? AND v1 = ? AND v2 = ? AND v3 = ?",
			"p", service.UserSubject(admin.userID), "channel", "read", "deny").
		Count(&count)
	assert.Equal(t, int64(1), count)
}
