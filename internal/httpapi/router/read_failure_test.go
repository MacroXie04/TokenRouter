package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckInStatusPropagatesDatabaseFailure(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	rec := do(http.MethodGet, "/api/user/checkin/status", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, "查询签到状态失败", decodeBody(t, rec)["message"])
}

func TestDashboardAndInstanceReadsPropagateDatabaseFailures(t *testing.T) {
	t.Run("dashboard", func(t *testing.T) {
		_, do, _ := setupChannelRead(t, roles.RoleRootUser)
		require.NoError(t, model.DB.Migrator().DropTable(&model.Token{}))
		rec := do(http.MethodGet, "/api/dashboard/stats", "")
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.Equal(t, "查询仪表盘数据失败", decodeBody(t, rec)["message"])
	})

	t.Run("instances", func(t *testing.T) {
		_, do, _ := setupChannelRead(t, roles.RoleRootUser)
		require.NoError(t, model.DB.Migrator().DropTable(&model.SystemInstance{}))
		rec := do(http.MethodGet, "/api/instance", "")
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.Equal(t, "查询实例失败", decodeBody(t, rec)["message"])
	})

	t.Run("channel model catalog", func(t *testing.T) {
		_, do, _ := setupChannelRead(t, roles.RoleRootUser)
		require.NoError(t, model.DB.Migrator().DropTable(&model.Model{}))
		rec := do(http.MethodGet, "/api/channel/models", "")
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.Equal(t, "查询模型失败", decodeBody(t, rec)["message"])
	})

	t.Run("self top-up history", func(t *testing.T) {
		_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
		rec := do(http.MethodGet, "/api/user/topup/self", "")
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.Equal(t, "查询充值记录失败", decodeBody(t, rec)["message"])
	})
}

func TestPublicCatalogReadsPropagateDatabaseFailures(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)

	for _, testCase := range []struct {
		name    string
		path    string
		message string
	}{
		{name: "models", path: "/api/models", message: "查询模型失败"},
		{name: "rankings", path: "/api/rankings", message: "查询排行榜失败"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rec := do(http.MethodGet, testCase.path, "")
			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			assert.Equal(t, testCase.message, decodeBody(t, rec)["message"])
		})
	}
}

func TestPublicCatalogReadsReturnDeterministicResults(t *testing.T) {
	handler, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(&model.Model{}, &model.Ability{}, &model.QuotaData{}))
	require.NoError(t, model.DB.Create(&model.Model{ModelName: "catalog-model", Status: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "catalog-group", Model: "catalog-model", ChannelId: 91, Enabled: true,
	}).Error)
	require.NoError(t, model.DB.Create(&model.QuotaData{
		ModelName: "catalog-model", Count: 3, Quota: 17,
	}).Error)

	models := do(http.MethodGet, "/api/models", "")
	require.Equal(t, http.StatusOK, models.Code, models.Body.String())
	modelData := decodeBody(t, models)["data"].([]any)
	require.Len(t, modelData, 1)
	assert.Equal(t, "catalog-model", modelData[0].(map[string]any)["model_name"])

	for _, path := range []string{"/api/user/groups", "/api/user/self/groups"} {
		groups := do(http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, groups.Code, groups.Body.String())
		groupData, ok := decodeBody(t, groups)["data"].(map[string]any)
		require.True(t, ok, groups.Body.String())
		assert.Equal(t, float64(1), groupData["default"].(map[string]any)["ratio"])
		assert.Equal(t, "default", groupData["default"].(map[string]any)["desc"])
		assert.Equal(t, float64(1), groupData["vip"].(map[string]any)["ratio"])
		assert.NotContains(t, groupData, "catalog-group",
			"an ability row alone must not make a group user-selectable")
	}

	rankings := do(http.MethodGet, "/api/rankings", "")
	require.Equal(t, http.StatusOK, rankings.Code, rankings.Body.String())
	rankingData := decodeBody(t, rankings)["data"].([]any)
	require.Len(t, rankingData, 1)
	assert.Equal(t, "catalog-model", rankingData[0].(map[string]any)["model_name"])
	assert.Equal(t, float64(3), rankingData[0].(map[string]any)["count"])
	assert.Equal(t, float64(17), rankingData[0].(map[string]any)["quota"])

	for _, protectedPath := range []string{"/api/models", "/api/user/self/groups"} {
		req := httptest.NewRequest(http.MethodGet, protectedPath, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, protectedPath)
	}
}

func TestHeaderNavigationModuleAccessContract(t *testing.T) {
	handler, do, _, _ := setupAuthAdjacent(t, roles.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(
		&model.QuotaData{}, &model.PerfMetric{}, &model.Model{}, &model.Vendor{},
	))

	anonymousGet := func(path string, headers ...map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, headerSet := range headers {
			for key, value := range headerSet {
				req.Header.Set(key, value)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	require.NoError(t, setting.UpdateOption(setting.HeaderNavModulesOption, ""))
	for _, publicPath := range []string{"/api/pricing", "/api/rankings", "/api/perf-metrics/summary"} {
		assert.Equal(t, http.StatusOK, anonymousGet(publicPath).Code, publicPath)
	}
	assert.Equal(t, http.StatusUnauthorized, anonymousGet("/api/pricing", map[string]string{
		"Authorization": "Bearer invalid-presented-credential",
	}).Code)

	require.NoError(t, setting.UpdateOption(setting.HeaderNavModulesOption,
		`{"pricing":{"enabled":false,"requireAuth":false}}`))
	assert.Equal(t, http.StatusForbidden, anonymousGet("/api/pricing").Code)
	assert.Equal(t, http.StatusForbidden, do(http.MethodGet, "/api/pricing", "").Code)
	assert.Equal(t, http.StatusUnauthorized, anonymousGet("/api/perf-metrics/summary").Code)
	assert.Equal(t, http.StatusOK, do(http.MethodGet, "/api/perf-metrics/summary", "").Code)

	require.NoError(t, setting.UpdateOption(setting.HeaderNavModulesOption,
		`{"rankings":{"enabled":true,"requireAuth":true}}`))
	assert.Equal(t, http.StatusUnauthorized, anonymousGet("/api/rankings").Code)
	assert.Equal(t, http.StatusOK, do(http.MethodGet, "/api/rankings", "").Code)

	require.NoError(t, setting.UpdateOption(setting.HeaderNavModulesOption, `{malformed`))
	assert.Equal(t, http.StatusOK, anonymousGet("/api/pricing").Code)
}
