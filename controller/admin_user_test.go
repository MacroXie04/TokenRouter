package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func createManagedUser(t *testing.T, username string, role, status int, group string) model.User {
	t.Helper()
	u := model.User{Username: username, Password: "x", Role: role, Status: status,
		Group: group, Quota: 100, AuthVersion: 1, DisplayName: "display-" + username,
		Email: username + "@example.com"}
	require.NoError(t, model.DB.Create(&u).Error)
	return u
}

func TestAdminSearchUsers(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	createManagedUser(t, "alice", constant.RoleCommonUser, model.UserStatusEnabled, "default")
	createManagedUser(t, "bob", constant.RoleAdminUser, model.UserStatusEnabled, "vip")
	createManagedUser(t, "carol", constant.RoleCommonUser, model.UserStatusDisabled, "vip")
	del := createManagedUser(t, "deleted-guy", constant.RoleCommonUser, model.UserStatusEnabled, "default")
	require.NoError(t, model.DB.Delete(&del).Error)

	// Keyword matches username substring.
	rec := do(http.MethodGet, "/api/user/search?keyword=ali", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "alice")
	assert.NotContains(t, rec.Body.String(), "bob")
	// Password and access_token must never leak.
	assert.NotContains(t, rec.Body.String(), `"password"`)
	assert.NotContains(t, rec.Body.String(), `"access_token"`)

	// Numeric keyword matches the id.
	rec = do(http.MethodGet, "/api/user/search?keyword="+common.Int2Str(del.Id), "")
	assert.Contains(t, rec.Body.String(), "deleted-guy")

	// Group filter.
	rec = do(http.MethodGet, "/api/user/search?group=vip", "")
	assert.Contains(t, rec.Body.String(), "bob")
	assert.NotContains(t, rec.Body.String(), "alice")

	// Role filter.
	rec = do(http.MethodGet, "/api/user/search?role=10", "")
	assert.Contains(t, rec.Body.String(), "bob")
	assert.NotContains(t, rec.Body.String(), "carol")

	// Status filter.
	rec = do(http.MethodGet, "/api/user/search?status=2", "")
	assert.Contains(t, rec.Body.String(), "carol")
	assert.NotContains(t, rec.Body.String(), "bob")

	// status=-1 selects only soft-deleted users.
	rec = do(http.MethodGet, "/api/user/search?status=-1", "")
	assert.Contains(t, rec.Body.String(), "deleted-guy")
	assert.NotContains(t, rec.Body.String(), "alice")

	// Sort by username ascending.
	rec = do(http.MethodGet, "/api/user/search?sort_by=username&sort_order=asc", "")
	var body struct {
		Data struct {
			Items []struct {
				Username string `json:"username"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Data.Items)
	assert.Equal(t, "adjuser", body.Data.Items[0].Username, "alphabetically smallest first")
}

func TestAdminManageUser(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	victim := createManagedUser(t, "victim-m", constant.RoleCommonUser, model.UserStatusEnabled, "default")

	// Invalid action.
	rec := do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"dance"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Disable.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"disable"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"success":true`)
	var refreshed model.User
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, model.UserStatusDisabled, refreshed.Status)

	// Enable.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"enable"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, model.UserStatusEnabled, refreshed.Status)

	// Quota add / subtract / override.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"add_quota","mode":"add","value":50}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, 150, refreshed.Quota)

	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"add_quota","mode":"subtract","value":30}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, 120, refreshed.Quota)

	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"add_quota","mode":"override","value":9}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, 9, refreshed.Quota)

	// Zero quota changes are rejected.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"add_quota","mode":"add","value":0}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "配额变更值必须大于0")

	// Promote (root-only; root operator here).
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"promote"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, constant.RoleAdminUser, refreshed.Role)

	// Already admin.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"promote"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "该用户已是管理员")

	// Demote revokes the target's sessions.
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "manage-victim-sid", UserID: victim.Id, Version: 1, UserAuthVersion: 1,
		Status: "active", RefreshHash: "h",
	}).Error)
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"demote"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, constant.RoleCommonUser, refreshed.Role)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "manage-victim-sid").First(&session).Error)
	assert.NotZero(t, session.RevokedAt)

	// Delete (soft).
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(victim.Id)+`,"action":"delete"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var count int64
	model.DB.Model(&model.User{}).Where("id = ?", victim.Id).Count(&count)
	assert.Zero(t, count)
	model.DB.Unscoped().Model(&model.User{}).Where("id = ?", victim.Id).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestAdminManageUserRoleGuards(t *testing.T) {
	// Root cannot be disabled/deleted/demoted.
	_, do, rootId, _ := setupAuthAdjacent(t, constant.RoleRootUser)
	rec := do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(rootId)+`,"action":"disable"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Root 用户")
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(rootId)+`,"action":"delete"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(rootId)+`,"action":"demote"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// A plain admin cannot manage a peer admin or promote anyone.
	_, doAdmin, _, _ := setupAuthAdjacent(t, constant.RoleAdminUser)
	peer := createManagedUser(t, "peer-admin", constant.RoleAdminUser, model.UserStatusEnabled, "default")
	rec = doAdmin(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(peer.Id)+`,"action":"disable"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "无权操作同级或更高级用户")

	commoner := createManagedUser(t, "commoner", constant.RoleCommonUser, model.UserStatusEnabled, "default")
	rec = doAdmin(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(commoner.Id)+`,"action":"promote"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "无权将用户提升为管理员")

	// An admin can manage a lower-role user.
	rec = doAdmin(http.MethodPost, "/api/user/manage", `{"id":`+common.Int2Str(commoner.Id)+`,"action":"disable"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestAdminCreateUserContract(t *testing.T) {
	root, _, _ := setupPermissionTest(t)
	do, rootID := root.do, root.userID
	previousInitialQuota := setting.GetOptionOrDefault(setting.InitialQuotaOption, "500000")
	t.Cleanup(func() { _ = setting.UpdateOption(setting.InitialQuotaOption, previousInitialQuota) })
	require.NoError(t, setting.UpdateOption(setting.InitialQuotaOption, "1234"))

	rec := do(http.MethodPost, "/api/user/", `{
		"username":"  created-admin  ","password":"password8","role":10,
		"quota":999999,"group":"vip","status":2,"email":"ignored@example.com",
		"aff_code":"injected","remark":"ignored",
		"admin_permissions":{"channel":{"sensitive_write":true}}
	}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	assert.NotContains(t, body, "data")

	var created model.User
	require.NoError(t, model.DB.Where("username = ?", "created-admin").First(&created).Error)
	assert.Equal(t, "created-admin", created.DisplayName)
	assert.Equal(t, constant.RoleAdminUser, created.Role)
	assert.Equal(t, model.UserStatusEnabled, created.Status)
	assert.Equal(t, 1234, created.Quota)
	assert.Equal(t, service.GroupDefault, created.Group)
	assert.Empty(t, created.Email)
	assert.Empty(t, created.Remark)
	assert.Len(t, created.AffCode, 4)
	assert.NotEqual(t, "password8", created.Password)
	assert.True(t, common.PasswordVerify("password8", created.Password))
	assert.Equal(t, int64(1), created.AuthVersion)

	var userSettings map[string]any
	require.NoError(t, common.UnmarshalJsonStr(created.Setting, &userSettings))
	sidebarText, ok := userSettings["sidebar_modules"].(string)
	require.True(t, ok)
	var sidebar map[string]any
	require.NoError(t, common.UnmarshalJsonStr(sidebarText, &sidebar))
	adminSidebar := sidebar["admin"].(map[string]any)
	assert.Equal(t, false, adminSidebar["setting"])
	assert.Equal(t, true, service.ExplicitUserOverrides(created.Id)["channel"]["sensitive_write"])

	var audit model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", rootID, service.LogTypeManage).
		Order("id desc").First(&audit).Error)
	assert.Contains(t, audit.Content, "user.create")
	assert.Contains(t, audit.Content, "target_user_id="+common.Int2Str(created.Id))
	var welcomeCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", created.Id, service.LogTypeSystem).Count(&welcomeCount).Error)
	assert.Equal(t, int64(1), welcomeCount)

	// Duplicate usernames are a business failure at HTTP 200.
	rec = do(http.MethodPost, "/api/user/", `{"username":"created-admin","password":"password8","role":1}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
}

func TestAdminCreateUserValidationAndRoleGuards(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)
	doRoot := root.do
	for _, body := range []string{
		`{`,
		`{"username":"   ","password":"password8","role":1}`,
		`{"username":"valid","password":"short","role":1}`,
		`{"username":"` + strings.Repeat("u", 21) + `","password":"password8","role":1}`,
		`{"username":"valid","password":"password8","email":"` + strings.Repeat("e", 51) + `","role":1}`,
	} {
		rec := doRoot(http.MethodPost, "/api/user/", body)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, false, decodeBody(t, rec)["success"])
	}

	// A root cannot create a peer root.
	rec := doRoot(http.MethodPost, "/api/user/", `{"username":"peer-root","password":"password8","role":100}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "无法创建权限大于等于自己的用户", decodeBody(t, rec)["message"])

	// An admin cannot create a peer admin.
	doAdmin := admin.do
	rec = doAdmin(http.MethodPost, "/api/user/", `{"username":"peer-admin-new","password":"password8","role":10}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "无法创建权限大于等于自己的用户", decodeBody(t, rec)["message"])

	// Explicit permission provisioning is root-only and rolls back the user row.
	rec = doAdmin(http.MethodPost, "/api/user/", `{"username":"rolled-back","password":"password8","role":1,"admin_permissions":{"channel":{"read":true}}}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "仅 Root 用户可以更新管理权限", decodeBody(t, rec)["message"])
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "rolled-back").Count(&count).Error)
	assert.Zero(t, count)
}

func TestAdminCreateUserRequiresAdmin(t *testing.T) {
	_, _, plain := setupPermissionTest(t)
	rec := plain.do(http.MethodPost, "/api/user/", `{"username":"denied-user","password":"password8","role":1}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
