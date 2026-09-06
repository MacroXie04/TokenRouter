package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredGroupsContractAndAuthorization(t *testing.T) {
	handler, do, _ := setupDashboardSession(t, roles.RoleAdminUser)
	previous := billingsvc.ExportedGroupRatios()
	billingsvc.SetGroupRatios(map[string]float64{"vip": 2, "default": 1, "staff": 0.5})
	t.Cleanup(func() { billingsvc.SetGroupRatios(previous) })

	rec := do(http.MethodGet, "/api/group/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	assert.Equal(t, []any{"default", "staff", "vip"}, body["data"])

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/group/", nil))
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	_, commonDo, _ := setupDashboardSession(t, roles.RoleCommonUser)
	assert.Equal(t, http.StatusForbidden, commonDo(http.MethodGet, "/api/group/", "").Code)
}

func TestPrefillGroupCRUDContract(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleAdminUser)

	rec := do(http.MethodPost, "/api/prefill_group/", `{
		"name":" core models ","type":"model","items":["gpt-4o","claude-sonnet"],"description":" primary "
	}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], rec.Body.String())
	created := body["data"].(map[string]any)
	id := int(created["id"].(float64))
	assert.Positive(t, id)
	assert.Equal(t, "core models", created["name"])
	assert.Equal(t, "primary", created["description"])
	assert.Equal(t, []any{"gpt-4o", "claude-sonnet"}, created["items"], "items must be a JSON array, not quoted JSON")
	assert.NotZero(t, created["created_time"])
	assert.Equal(t, created["created_time"], created["updated_time"])

	rec = do(http.MethodGet, "/api/prefill_group/?type=model", "")
	require.Equal(t, http.StatusOK, rec.Code)
	groups := decodeBody(t, rec)["data"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, "core models", groups[0].(map[string]any)["name"])

	rec = do(http.MethodPost, "/api/prefill_group/", `{"name":"core models","type":"tag","items":[]}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Equal(t, "组名称已存在", decodeBody(t, rec)["message"])

	rec = do(http.MethodPut, "/api/prefill_group/", `{
		"id":`+itoaTest(id)+`,"name":"endpoint presets","type":"endpoint",
		"items":"{\"chat\":{\"path\":\"/v1/chat/completions\"}}","description":"routes"
	}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	updatedBody := decodeBody(t, rec)
	require.Equal(t, true, updatedBody["success"], rec.Body.String())
	updated := updatedBody["data"].(map[string]any)
	assert.Equal(t, "endpoint presets", updated["name"])
	assert.Equal(t, `{"chat":{"path":"/v1/chat/completions"}}`, updated["items"], "endpoint JSON text retains the reference string shape")
	assert.GreaterOrEqual(t, updated["updated_time"], updated["created_time"])

	rec = do(http.MethodGet, "/api/prefill_group/?type=model", "")
	assert.Empty(t, decodeBody(t, rec)["data"].([]any))
	rec = do(http.MethodGet, "/api/prefill_group/?type=endpoint", "")
	assert.Len(t, decodeBody(t, rec)["data"].([]any), 1)

	rec = do(http.MethodDelete, "/api/prefill_group/"+itoaTest(id), "")
	require.Equal(t, http.StatusOK, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Nil(t, body["data"])

	rec = do(http.MethodPost, "/api/prefill_group/", `{"name":"endpoint presets","type":"endpoint","items":"{}"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, true, decodeBody(t, rec)["success"], "soft-deleted names must be reusable")
}

func TestPrefillGroupValidationAndRoleGuard(t *testing.T) {
	handler, do, _ := setupDashboardSession(t, roles.RoleAdminUser)

	tests := []struct {
		name    string
		method  string
		path    string
		payload string
		message string
	}{
		{"missing fields", http.MethodPost, "/api/prefill_group/", `{"items":[]}`, "组名称和类型不能为空"},
		{"invalid item shape", http.MethodPost, "/api/prefill_group/", `{"name":"bad","type":"model","items":{"x":1}}`, "组项目必须是字符串数组"},
		{"missing update id", http.MethodPut, "/api/prefill_group/", `{"name":"bad","type":"model","items":[]}`, "缺少组 ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := do(test.method, test.path, test.payload)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			body := decodeBody(t, rec)
			assert.Equal(t, false, body["success"])
			assert.Contains(t, body["message"], test.message)
		})
	}

	rec := do(http.MethodDelete, "/api/prefill_group/not-an-id", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/prefill_group/", nil))
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	_, commonDo, _ := setupDashboardSession(t, roles.RoleCommonUser)
	assert.Equal(t, http.StatusForbidden, commonDo(http.MethodGet, "/api/prefill_group/", "").Code)
}

func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}
