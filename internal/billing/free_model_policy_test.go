package billing

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
)

func TestGlobalFreeModelPreConsumePolicyIsExactAcrossPricingModes(t *testing.T) {
	db := setupRelayQuotaReservationDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	require.NoError(t, setting.Init())
	previousPrices := ExportedModelPrices()
	previousGroups := ExportedGroupRatios()
	previousSpecial := ExportedGroupGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousGroups)
		SetGroupGroupRatios(previousSpecial)
	})

	SetModelPriceRegistry(map[string]ModelPrice{
		"free-flat":     {},
		"prompt-paid":   {Prompt: 1},
		"complete-paid": {Completion: 1},
		"free-tiered":   {},
		"free-task":     {Completion: 99},
		"paid-task":     {Prompt: 0.1},
	})
	SetGroupRatios(map[string]float64{"default": 1, "vip": 1})
	SetGroupGroupRatios(map[string]map[string]float64{"free-member": {"vip": 0}})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: `{"free-tiered":"tiered_expr"}`,
		setting.ModelBillingExprOption: `{"free-tiered":"0"}`,
	}))

	assert.False(t, ShouldSkipOrdinaryFreeModelPreConsume("free-flat", "", "default"),
		"the default true setting retains the established pre-consume path")
	assert.False(t, ShouldSkipPerCallFreeModelPreConsume("free-task", "", "default"))
	assert.False(t, ShouldSkipExplicitFreeModelPreConsume("free-task"))

	require.NoError(t, setting.UpdateOption(setting.EnableFreeModelPreConsumeOption, "false"))
	assert.True(t, ShouldSkipOrdinaryFreeModelPreConsume("free-flat", "", "default"))
	assert.False(t, ShouldSkipOrdinaryFreeModelPreConsume("prompt-paid", "", "default"))
	assert.False(t, ShouldSkipOrdinaryFreeModelPreConsume("complete-paid", "", "default"))
	assert.False(t, ShouldSkipOrdinaryFreeModelPreConsume("free-tiered", "", "default"),
		"a zero tiered estimate is not itself a free-model policy")
	assert.True(t, ShouldSkipOrdinaryFreeModelPreConsume("free-tiered", "free-member", "vip"))
	assert.True(t, ShouldSkipPerCallFreeModelPreConsume("free-task", "", "default"),
		"legacy per-call pricing uses the prompt price only")
	assert.False(t, ShouldSkipPerCallFreeModelPreConsume("paid-task", "", "default"))
	assert.True(t, ShouldSkipPerCallFreeModelPreConsume("paid-task", "free-member", "vip"))
	assert.True(t, ShouldSkipExplicitFreeModelPreConsume("paid-task"))

	require.NoError(t, setting.UpdateOption(setting.EnableFreeModelPreConsumeOption, "true"))
	assert.False(t, ShouldSkipOrdinaryFreeModelPreConsume("free-flat", "", "default"))
	assert.False(t, ShouldSkipPerCallFreeModelPreConsume("free-task", "", "default"))
	assert.False(t, ShouldSkipExplicitFreeModelPreConsume("free-task"))
}
