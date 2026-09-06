package router_test

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestResetModelRatioContract(t *testing.T) {
	_, do, rootID := setupDashboardSession(t, roles.RoleRootUser)
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	previousSpecialRatios := billingsvc.ExportedGroupGroupRatios()
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
		billingsvc.SetGroupGroupRatios(previousSpecialRatios)
	})

	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"gpt-4o": {Prompt: 99, Completion: 199},
	})
	assert.Equal(t, 49500, billingsvc.ComputeQuota("gpt-4o", "", 1000, 0))

	rec := do(http.MethodPost, "/api/option/rest_model_ratio", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "重置模型倍率成功", body["message"])

	var persistedPrices map[string]billingsvc.ModelPrice
	require.NoError(t, jsonutil.UnmarshalJsonStr(setting.GetOption(setting.ModelPriceOption), &persistedPrices))
	assert.Equal(t, billingsvc.DefaultModelPriceRegistry(), persistedPrices)

	var persistedRatios map[string]float64
	require.NoError(t, jsonutil.UnmarshalJsonStr(setting.GetOption(setting.ModelRatioOption), &persistedRatios))
	assert.Equal(t, billingsvc.DefaultModelRatioRegistry(), persistedRatios)

	assert.Equal(t, 1250, billingsvc.ComputeQuota("gpt-4o", "", 1000, 0), "reset pricing is live without restart")
	assert.Equal(t, billingsvc.DefaultModelPriceRegistry(), billingsvc.ExportedModelPrices())

	var audit model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", rootID, billingsvc.LogTypeManage).
		Order("id desc").First(&audit).Error)
	assert.Equal(t, "option.reset_ratio", audit.Content)
}

func TestPricingOptionsWriteThroughToLiveBilling(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	previousSpecialRatios := billingsvc.ExportedGroupGroupRatios()
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
		billingsvc.SetGroupGroupRatios(previousSpecialRatios)
	})

	requestBody, err := json.Marshal(map[string]any{
		"key":   setting.ModelPriceOption,
		"value": `{"custom-model":{"prompt":4,"completion":12}}`,
	})
	require.NoError(t, err)
	rec := do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, 2000, billingsvc.ComputeQuota("custom-model", "", 1000, 0))

	persisted := setting.GetOption(setting.ModelPriceOption)
	requestBody, err = json.Marshal(map[string]any{
		"key":   setting.ModelPriceOption,
		"value": `{"custom-model":{"prompt":-1,"completion":12}}`,
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(requestBody))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Equal(t, persisted, setting.GetOption(setting.ModelPriceOption), "invalid prices are not persisted")
	assert.Equal(t, 2000, billingsvc.ComputeQuota("custom-model", "", 1000, 0), "invalid prices do not replace the live cache")

	requestBody, err = json.Marshal(map[string]any{
		"key":   setting.GroupRatioOption,
		"value": `{"vip":2.5}`,
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, 5000, billingsvc.ComputeQuota("custom-model", "vip", 1000, 0))

	requestBody, err = json.Marshal(map[string]any{
		"key":   setting.GroupGroupRatioOption,
		"value": `{"default":{"vip":0.5}}`,
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, 1000, billingsvc.ComputeQuotaForUser("custom-model", "default", "vip", 1000, 0))

	persistedSpecial := setting.GetOption(setting.GroupGroupRatioOption)
	requestBody, err = json.Marshal(map[string]any{
		"key":   setting.GroupGroupRatioOption,
		"value": `{"default":{"vip":-1}}`,
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(requestBody))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Equal(t, persistedSpecial, setting.GetOption(setting.GroupGroupRatioOption))
	assert.Equal(t, 1000, billingsvc.ComputeQuotaForUser("custom-model", "default", "vip", 1000, 0))

	// Simulate another node writing options directly to the shared database. A
	// local synchronization refreshes both runtime registries coherently.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.ModelPriceOption).
		Update("value", `{"custom-model":{"prompt":7,"completion":14}}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupRatioOption).
		Update("value", `{"vip":3}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupGroupRatioOption).
		Update("value", `{"default":{"vip":0.25}}`).Error)
	require.NoError(t, billingsvc.SyncRuntimeOptions())
	assert.Equal(t, 10500, billingsvc.ComputeQuota("custom-model", "vip", 1000, 0))
	assert.Equal(t, 875, billingsvc.ComputeQuotaForUser("custom-model", "default", "vip", 1000, 0))

	// Invalid remote settings do not publish either half of a new snapshot.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.ModelPriceOption).
		Update("value", `{"custom-model":{"prompt":8,"completion":16}}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupRatioOption).
		Update("value", `{"vip":0}`).Error)
	require.Error(t, billingsvc.SyncRuntimeOptions())
	assert.Equal(t, 10500, billingsvc.ComputeQuota("custom-model", "vip", 1000, 0))
	assert.Equal(t, 875, billingsvc.ComputeQuotaForUser("custom-model", "default", "vip", 1000, 0))
}

func TestQuotaPerUnitIsAnImmutablePublicOption(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	require.NoError(t, setting.UpdateOption(setting.QuotaPerUnitOption, strconv.Itoa(quotamath.QuotaPerUnit)))

	requestBody, err := json.Marshal(map[string]any{
		"key":   setting.QuotaPerUnitOption,
		"value": quotamath.QuotaPerUnit + 1,
	})
	require.NoError(t, err)
	rec := do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	assert.Equal(t, strconv.Itoa(quotamath.QuotaPerUnit), setting.GetOption(setting.QuotaPerUnitOption))

	requestBody, err = json.Marshal(map[string]any{
		"key":   setting.QuotaPerUnitOption,
		"value": quotamath.QuotaPerUnit,
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])

	rec = do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	response := decodeBody(t, rec)
	options, ok := response["data"].([]any)
	require.True(t, ok)
	found := false
	for _, candidate := range options {
		option, ok := candidate.(map[string]any)
		if !ok || option["key"] != setting.QuotaPerUnitOption {
			continue
		}
		found = true
		assert.Equal(t, strconv.Itoa(quotamath.QuotaPerUnit), option["value"])
	}
	assert.True(t, found, "fixed QuotaPerUnit must remain visible to settings clients")
}

func TestResetModelRatioRoleGuard(t *testing.T) {
	handler, _, _ := setupDashboardSession(t, roles.RoleRootUser)
	req := httptest.NewRequest(http.MethodPost, "/api/option/rest_model_ratio", strings.NewReader(""))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	for _, role := range []int{roles.RoleCommonUser, roles.RoleAdminUser} {
		_, do, _ := setupDashboardSession(t, role)
		assert.Equal(t, http.StatusForbidden, do(http.MethodPost, "/api/option/rest_model_ratio", "").Code)
	}
}
