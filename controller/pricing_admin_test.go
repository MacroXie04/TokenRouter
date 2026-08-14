package controller_test

import (
	"encoding/json"
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
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestResetModelRatioContract(t *testing.T) {
	_, do, rootID := setupChannelRead(t, constant.RoleRootUser)
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})

	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"gpt-4o": {Prompt: 99, Completion: 199},
	})
	assert.Equal(t, 49500, service.ComputeQuota("gpt-4o", "", 1000, 0))

	rec := do(http.MethodPost, "/api/option/rest_model_ratio", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "重置模型倍率成功", body["message"])

	var persistedPrices map[string]service.ModelPrice
	require.NoError(t, common.UnmarshalJsonStr(setting.GetOption(setting.ModelPriceOption), &persistedPrices))
	assert.Equal(t, service.DefaultModelPriceRegistry(), persistedPrices)

	var persistedRatios map[string]float64
	require.NoError(t, common.UnmarshalJsonStr(setting.GetOption(setting.ModelRatioOption), &persistedRatios))
	assert.Equal(t, service.DefaultModelRatioRegistry(), persistedRatios)

	assert.Equal(t, 1250, service.ComputeQuota("gpt-4o", "", 1000, 0), "reset pricing is live without restart")
	assert.Equal(t, service.DefaultModelPriceRegistry(), service.ExportedModelPrices())

	var audit model.Log
	require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", rootID, service.LogTypeManage).
		Order("id desc").First(&audit).Error)
	assert.Equal(t, "option.reset_ratio", audit.Content)
}

func TestPricingOptionsWriteThroughToLiveBilling(t *testing.T) {
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	previousPrices := service.ExportedModelPrices()
	previousRatios := service.ExportedGroupRatios()
	t.Cleanup(func() {
		service.SetModelPriceRegistry(previousPrices)
		service.SetGroupRatios(previousRatios)
	})

	requestBody, err := json.Marshal(map[string]any{
		"key":   setting.ModelPriceOption,
		"value": `{"custom-model":{"prompt":4,"completion":12}}`,
	})
	require.NoError(t, err)
	rec := do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, 2000, service.ComputeQuota("custom-model", "", 1000, 0))

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
	assert.Equal(t, 2000, service.ComputeQuota("custom-model", "", 1000, 0), "invalid prices do not replace the live cache")

	requestBody, err = json.Marshal(map[string]any{
		"key":   setting.GroupRatioOption,
		"value": `{"vip":2.5}`,
	})
	require.NoError(t, err)
	rec = do(http.MethodPut, "/api/option/", string(requestBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, decodeBody(t, rec)["success"])
	assert.Equal(t, 5000, service.ComputeQuota("custom-model", "vip", 1000, 0))

	// Simulate another node writing options directly to the shared database. A
	// local synchronization refreshes both runtime registries coherently.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.ModelPriceOption).
		Update("value", `{"custom-model":{"prompt":7,"completion":14}}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupRatioOption).
		Update("value", `{"vip":3}`).Error)
	require.NoError(t, service.SyncRuntimeOptions())
	assert.Equal(t, 10500, service.ComputeQuota("custom-model", "vip", 1000, 0))

	// Invalid remote settings do not publish either half of a new snapshot.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.ModelPriceOption).
		Update("value", `{"custom-model":{"prompt":8,"completion":16}}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupRatioOption).
		Update("value", `{"vip":0}`).Error)
	require.Error(t, service.SyncRuntimeOptions())
	assert.Equal(t, 10500, service.ComputeQuota("custom-model", "vip", 1000, 0))
}

func TestResetModelRatioRoleGuard(t *testing.T) {
	handler, _, _ := setupChannelRead(t, constant.RoleRootUser)
	req := httptest.NewRequest(http.MethodPost, "/api/option/rest_model_ratio", strings.NewReader(""))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	for _, role := range []int{constant.RoleCommonUser, constant.RoleAdminUser} {
		_, do, _ := setupChannelRead(t, role)
		assert.Equal(t, http.StatusForbidden, do(http.MethodPost, "/api/option/rest_model_ratio", "").Code)
	}
}
