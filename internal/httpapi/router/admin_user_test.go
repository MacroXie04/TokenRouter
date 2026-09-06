package router_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"strings"
	"testing"
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
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	createManagedUser(t, "alice", roles.RoleCommonUser, model.UserStatusEnabled, "default")
	createManagedUser(t, "bob", roles.RoleAdminUser, model.UserStatusEnabled, "vip")
	createManagedUser(t, "carol", roles.RoleCommonUser, model.UserStatusDisabled, "vip")
	del := createManagedUser(t, "deleted-guy", roles.RoleCommonUser, model.UserStatusEnabled, "default")
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
	rec = do(http.MethodGet, "/api/user/search?keyword="+textutil.Int2Str(del.Id), "")
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
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	victim := createManagedUser(t, "victim-m", roles.RoleCommonUser, model.UserStatusEnabled, "default")

	// Invalid action.
	rec := do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"dance"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Disable.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"disable"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"success":true`)
	var refreshed model.User
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, model.UserStatusDisabled, refreshed.Status)

	// Enable.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"enable"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, model.UserStatusEnabled, refreshed.Status)

	// Quota add / subtract / override.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"add_quota","mode":"add","value":50}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, 150, refreshed.Quota)

	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"add_quota","mode":"subtract","value":30}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, 120, refreshed.Quota)

	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"add_quota","mode":"override","value":9}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, 9, refreshed.Quota)

	// Zero quota changes are rejected.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"add_quota","mode":"add","value":0}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "配额变更值必须大于0")

	// Promote (root-only; root operator here).
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"promote"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, roles.RoleAdminUser, refreshed.Role)

	// Already admin.
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"promote"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "该用户已是管理员")

	// Demote revokes the target's sessions.
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "manage-victim-sid", UserID: victim.Id, Version: 1, UserAuthVersion: 1,
		Status: "active", RefreshHash: "h", ExpiresAt: wallclock.NowTimestamp() + 3600,
	}).Error)
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"demote"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	model.DB.First(&refreshed, victim.Id)
	assert.Equal(t, roles.RoleCommonUser, refreshed.Role)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "manage-victim-sid").First(&session).Error)
	assert.NotZero(t, session.RevokedAt)

	// Delete (soft).
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(victim.Id)+`,"action":"delete"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var count int64
	model.DB.Model(&model.User{}).Where("id = ?", victim.Id).Count(&count)
	assert.Zero(t, count)
	model.DB.Unscoped().Model(&model.User{}).Where("id = ?", victim.Id).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestAdminManageUserRoleGuards(t *testing.T) {
	// Root cannot be disabled/deleted/demoted.
	_, do, rootId, _ := setupAuthAdjacent(t, roles.RoleRootUser)
	rec := do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(rootId)+`,"action":"disable"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "无权操作同级或更高级用户")
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(rootId)+`,"action":"delete"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(rootId)+`,"action":"demote"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	var rootBefore model.User
	require.NoError(t, model.DB.First(&rootBefore, rootId).Error)
	rec = do(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(rootId)+`,"action":"add_quota","mode":"add","value":1}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	var rootAfter model.User
	require.NoError(t, model.DB.First(&rootAfter, rootId).Error)
	assert.Equal(t, rootBefore.Quota, rootAfter.Quota, "root-on-root quota mutation must be rejected")

	// A plain admin cannot manage a peer admin or promote anyone.
	_, doAdmin, _, _ := setupAuthAdjacent(t, roles.RoleAdminUser)
	peer := createManagedUser(t, "peer-admin", roles.RoleAdminUser, model.UserStatusEnabled, "default")
	rec = doAdmin(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(peer.Id)+`,"action":"disable"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "无权操作同级或更高级用户")

	commoner := createManagedUser(t, "commoner", roles.RoleCommonUser, model.UserStatusEnabled, "default")
	rec = doAdmin(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(commoner.Id)+`,"action":"promote"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "无权将用户提升为管理员")

	// An admin can manage a lower-role user.
	rec = doAdmin(http.MethodPost, "/api/user/manage", `{"id":`+textutil.Int2Str(commoner.Id)+`,"action":"disable"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestAdminUpdateUserProfileResetsPasswordAndRevokesSessionsAtomically(t *testing.T) {
	root, _, _ := setupPermissionTest(t)
	target := createManagedUser(t, "editable-user", roles.RoleCommonUser, model.UserStatusEnabled, "default")
	oldHash, err := cryptoutil.PasswordHash("old-password")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", target.Id).Updates(map[string]any{
		"password": oldHash,
		"remark":   "clear me",
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSession{
		SID: "admin-edit-session", UserID: target.Id, Version: 1, UserAuthVersion: 1,
		Status: auth.SessionStatusActive, RefreshHash: "admin-edit-refresh", ExpiresAt: wallclock.NowTimestamp() + 3600,
	}).Error)

	body := `{"id":` + textutil.Int2Str(target.Id) + `,"display_name":"  Fresh Name  ","group":"vip","remark":"","password":"replacement8"}`
	rec := root.do(http.MethodPut, "/api/user/", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.NotContains(t, rec.Body.String(), "replacement8")

	var updated model.User
	require.NoError(t, model.DB.First(&updated, target.Id).Error)
	assert.Equal(t, "Fresh Name", updated.DisplayName)
	assert.Equal(t, "vip", updated.Group)
	assert.Empty(t, updated.Remark)
	assert.Equal(t, int64(2), updated.AuthVersion)
	assert.True(t, cryptoutil.PasswordVerify("replacement8", updated.Password))
	assert.False(t, cryptoutil.PasswordVerify("old-password", updated.Password))

	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "admin-edit-session").First(&session).Error)
	assert.Equal(t, auth.SessionStatusRevoked, session.Status)
	assert.Equal(t, "admin_user_update", session.RevokedReason)
	assert.NotZero(t, session.RevokedAt)

	var audit model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", root.userID, billingsvc.LogTypeManage).
		Order("id desc").First(&audit).Error)
	assert.Contains(t, audit.Content, "user.update")
	assert.Contains(t, audit.Content, "password_reset=true")
	assert.NotContains(t, audit.Content, "replacement8")
}

func TestAdminUpdateUserRejectsInvalidPasswordWithoutMutation(t *testing.T) {
	root, _, _ := setupPermissionTest(t)
	target := createManagedUser(t, "invalid-edit-user", roles.RoleCommonUser, model.UserStatusEnabled, "default")
	before := target
	rec := root.do(http.MethodPut, "/api/user/", `{"id":`+textutil.Int2Str(target.Id)+`,"display_name":"Changed","password":"short"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	var after model.User
	require.NoError(t, model.DB.First(&after, target.Id).Error)
	assert.Equal(t, before.DisplayName, after.DisplayName)
	assert.Equal(t, before.Password, after.Password)
	assert.Equal(t, before.AuthVersion, after.AuthVersion)
}

func TestAdminCreateUserContract(t *testing.T) {
	root, _, _ := setupPermissionTest(t)
	do, rootID := root.do, root.userID
	t.Setenv(auth.GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.InitialQuotaOption:        "1234",
		setting.QuotaForNewUserOption:     "2345",
		setting.DefaultUseAutoGroupOption: "true",
		setting.DefaultGroupOption:        "admin-default",
	}))

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
	assert.Equal(t, roles.RoleAdminUser, created.Role)
	assert.Equal(t, model.UserStatusEnabled, created.Status)
	assert.Equal(t, 2345, created.Quota, "canonical registration quota must win over the legacy fallback")
	assert.Equal(t, "admin-default", created.Group)
	assert.Empty(t, created.Email)
	assert.Empty(t, created.Remark)
	assert.Len(t, created.AffCode, 4)
	assert.NotEqual(t, "password8", created.Password)
	assert.True(t, cryptoutil.PasswordVerify("password8", created.Password))
	assert.Equal(t, int64(1), created.AuthVersion)
	var defaultTokens []model.Token
	require.NoError(t, model.DB.Where("user_id = ?", created.Id).Find(&defaultTokens).Error)
	require.Len(t, defaultTokens, 1)
	assert.Equal(t, userssvc.GroupAuto, defaultTokens[0].Group)
	assert.True(t, defaultTokens[0].UnlimitedQuota)

	var userSettings map[string]any
	require.NoError(t, jsonutil.UnmarshalJsonStr(created.Setting, &userSettings))
	sidebarText, ok := userSettings["sidebar_modules"].(string)
	require.True(t, ok)
	var sidebar map[string]any
	require.NoError(t, jsonutil.UnmarshalJsonStr(sidebarText, &sidebar))
	adminSidebar := sidebar["admin"].(map[string]any)
	assert.Equal(t, false, adminSidebar["setting"])
	assert.Equal(t, true, auth.ExplicitUserOverrides(created.Id)["channel"]["sensitive_write"])

	var audit model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", rootID, billingsvc.LogTypeManage).
		Order("id desc").First(&audit).Error)
	assert.Contains(t, audit.Content, "user.create")
	assert.Contains(t, audit.Content, "target_user_id="+textutil.Int2Str(created.Id))
	var welcomeCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("user_id = ? AND type = ?", created.Id, billingsvc.LogTypeSystem).Count(&welcomeCount).Error)
	assert.Equal(t, int64(1), welcomeCount)

	// Duplicate usernames are a business failure at HTTP 200.
	rec = do(http.MethodPost, "/api/user/", `{"username":"created-admin","password":"password8","role":1}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
}

func TestAdminCreateUserValidationAndRoleGuards(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)
	t.Setenv(auth.GenerateDefaultTokenEnvironment, "true")
	require.NoError(t, setting.Init())
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
	require.NoError(t, model.DB.Model(&model.Token{}).Count(&count).Error)
	assert.Zero(t, count, "the default token must roll back with rejected permission provisioning")
}

func TestAdminCreateUserRequiresAdmin(t *testing.T) {
	_, _, plain := setupPermissionTest(t)
	rec := plain.do(http.MethodPost, "/api/user/", `{"username":"denied-user","password":"password8","role":1}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
