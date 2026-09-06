package controller_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestRankingsPeriodSnapshotAndLegacyCompatibility(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.AutoMigrate(&model.QuotaData{}, &model.Model{}, &model.Vendor{}))
	vendor := model.Vendor{Name: "Acme", Icon: "acme", Status: 1}
	require.NoError(t, model.DB.Create(&vendor).Error)
	require.NoError(t, model.DB.Create(&model.Model{
		ModelName: "ranked-model", VendorID: vendor.Id, Status: 1,
		NameRule: model.ModelNameRuleExact,
	}).Error)
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID: 1, Username: "ranking-user", ModelName: "ranked-model",
		CreatedAt: time.Now().Add(-time.Hour).Unix(), TokenUsed: 1200,
		Count: 42, Quota: 900,
	}).Error)

	legacy := do(http.MethodGet, "/api/rankings", "")
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	legacyData, ok := decodeBody(t, legacy)["data"].([]any)
	require.True(t, ok, legacy.Body.String())
	require.Len(t, legacyData, 1)
	assert.Equal(t, float64(42), legacyData[0].(map[string]any)["count"])
	assert.Equal(t, float64(900), legacyData[0].(map[string]any)["quota"])

	period := do(http.MethodGet, "/api/rankings?period=today", "")
	require.Equal(t, http.StatusOK, period.Code, period.Body.String())
	snapshot, ok := decodeBody(t, period)["data"].(map[string]any)
	require.True(t, ok, period.Body.String())
	models, ok := snapshot["models"].([]any)
	require.True(t, ok, period.Body.String())
	require.Len(t, models, 1)
	assert.Equal(t, "ranked-model", models[0].(map[string]any)["model_name"])
	assert.Equal(t, float64(1200), models[0].(map[string]any)["total_tokens"])
	assert.IsType(t, []any{}, snapshot["vendors"])
	assert.IsType(t, map[string]any{}, snapshot["models_history"])
	assert.IsType(t, map[string]any{}, snapshot["vendor_share_history"])
}

func TestRankingsRejectsUnsupportedPeriodBeforeQuerying(t *testing.T) {
	_, do, _, _ := setupAuthAdjacent(t, constant.RoleCommonUser)
	recorder := do(http.MethodGet, "/api/rankings?period=forever", "")
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	assert.Equal(t, "排行榜周期无效", decodeBody(t, recorder)["message"])
}
