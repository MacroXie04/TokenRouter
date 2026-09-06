package billing

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"testing"
)

func TestReferenceAsyncTaskBillingPlanFixedPriceIsFinal(t *testing.T) {
	plan := ReferenceAsyncTaskBillingPlan{
		Version: 1, ModelName: "kling-v1", GroupRatio: "0.25",
		UseFixedPrice: true, FixedPrice: "0.004",
	}
	require.NoError(t, plan.Validate())
	hold, err := plan.PreConsumeQuota()
	require.NoError(t, err)
	assert.Equal(t, 500, hold)
	for _, units := range []int{0, 1, 999} {
		actual, settleErr := plan.SettlementQuota(units)
		require.NoError(t, settleErr)
		assert.Equal(t, hold, actual)
	}
}

func TestReferenceAsyncTaskBillingPlanRatioUsesHalfUnitHoldAndWholeFinalUnits(t *testing.T) {
	plan := ReferenceAsyncTaskBillingPlan{
		Version: 1, ModelName: "kling-v2-master", GroupRatio: "0.25",
		GroupRatioSpecial: true, ModelRatio: "2",
	}
	require.NoError(t, plan.Validate())
	hold, err := plan.PreConsumeQuota()
	require.NoError(t, err)
	assert.Equal(t, quotamath.QuotaPerUnit/4, hold)

	actual, err := plan.SettlementQuota(2)
	require.NoError(t, err)
	assert.Equal(t, 1, actual, "provider units are already ceiled; final ratio accounting truncates once")
	missingUnits, err := plan.SettlementQuota(0)
	require.NoError(t, err)
	assert.Equal(t, hold, missingUnits, "missing provider usage keeps the immutable precharge")

	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	var restored ReferenceAsyncTaskBillingPlan
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, plan, restored)
	restoredActual, err := restored.SettlementQuota(2)
	require.NoError(t, err)
	assert.Equal(t, actual, restoredActual)
	assert.Equal(t, "0.25", restored.BillingLogFields()["user_group_ratio"])
}

func TestReferenceAsyncTaskBillingPlanFailsClosed(t *testing.T) {
	tests := []ReferenceAsyncTaskBillingPlan{
		{},
		{Version: 1, ModelName: " kling-v1", GroupRatio: "1", ModelRatio: "1"},
		{Version: 1, ModelName: "kling-v1", GroupRatio: "-1", ModelRatio: "1"},
		{Version: 1, ModelName: "kling-v1", GroupRatio: "1", UseFixedPrice: true, FixedPrice: "1", ModelRatio: "1"},
		{Version: 1, ModelName: "kling-v1", GroupRatio: "1", ModelRatio: "1", FixedPrice: "1"},
		{Version: 1, ModelName: "kling-v1", GroupRatio: "1", ModelRatio: "10000000000000"},
	}
	for _, plan := range tests {
		assert.Error(t, plan.Validate())
	}

	valid := ReferenceAsyncTaskBillingPlan{Version: 1, ModelName: "kling-v1", GroupRatio: "1", ModelRatio: "1"}
	_, err := valid.SettlementQuota(-1)
	assert.Error(t, err)
	_, err = valid.SettlementQuota(int(quotamath.MaxQuota) + 1)
	assert.Error(t, err)
}

func TestReferenceAsyncTaskBillingPlanRejectsHostileExponentsBeforeDecimalComparison(t *testing.T) {
	for _, hostile := range []string{"1e-2147483648", "1e2147483647", "-1E-2147483648", "+1E2147483647"} {
		for _, plan := range []ReferenceAsyncTaskBillingPlan{
			{Version: 1, ModelName: "kling-v1", GroupRatio: hostile, ModelRatio: "1"},
			{Version: 1, ModelName: "kling-v1", GroupRatio: "1", UseFixedPrice: true, FixedPrice: hostile},
			{Version: 1, ModelName: "kling-v1", GroupRatio: "1", ModelRatio: hostile},
		} {
			assert.Error(t, plan.Validate(), "%s must fail before arbitrary-precision comparison", hostile)
		}
	}

	for _, ordinary := range []string{"0", "0.25", ".5", "1.", "+2", "1e2", "1000000000000"} {
		_, err := parseReferenceAsyncDecimal("ratio", ordinary)
		require.NoError(t, err, ordinary)
	}
}
