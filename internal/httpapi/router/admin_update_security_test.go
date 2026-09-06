package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"testing"
)

func TestGenericAdminUserUpdateCannotEscalateRoles(t *testing.T) {
	root, admin, _ := setupPermissionTest(t)
	target := createManagedUser(t, "role-escalation-target", roles.RoleCommonUser, model.UserStatusEnabled, "default")

	// An admin cannot rewrite a lower-role account into a root account.
	rec := admin.do(http.MethodPut, "/api/user", `{"id":`+textutil.Int2Str(target.Id)+`,"role":100}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var unchanged model.User
	require.NoError(t, model.DB.First(&unchanged, target.Id).Error)
	assert.Equal(t, roles.RoleCommonUser, unchanged.Role)

	// An admin cannot mutate itself or a peer through the generic editor.
	rec = admin.do(http.MethodPut, "/api/user", `{"id":`+textutil.Int2Str(admin.userID)+`,"quota":0}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// Even root uses the explicit, audited role workflow.
	rec = root.do(http.MethodPut, "/api/user", `{"id":`+textutil.Int2Str(target.Id)+`,"role":10}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.First(&unchanged, target.Id).Error)
	assert.Equal(t, roles.RoleCommonUser, unchanged.Role)
}

func TestGenericAdminUserUpdateRejectsCorruptQuotaAndStatus(t *testing.T) {
	_, admin, _ := setupPermissionTest(t)
	target := createManagedUser(t, "invalid-update-target", roles.RoleCommonUser, model.UserStatusEnabled, "default")
	originalQuota := target.Quota

	for _, body := range []string{
		`{"id":` + textutil.Int2Str(target.Id) + `,"quota":-1}`,
		`{"id":` + textutil.Int2Str(target.Id) + `,"quota":2147483648}`,
		`{"id":` + textutil.Int2Str(target.Id) + `,"status":999}`,
	} {
		rec := admin.do(http.MethodPut, "/api/user", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	var unchanged model.User
	require.NoError(t, model.DB.First(&unchanged, target.Id).Error)
	assert.Equal(t, originalQuota, unchanged.Quota)
	assert.Equal(t, model.UserStatusEnabled, unchanged.Status)
}
