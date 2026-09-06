package settings

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
)

func TestPricingConfigurationRequiresUsableTieredExpression(t *testing.T) {
	require.NoError(t, validatePricingConfiguration(map[string]string{
		ModelBillingModeOption: `{"tiered":"tiered_expr","plain":"ratio","fixed":"reference"}`,
		ModelBillingExprOption: `{"tiered":"tier(\"base\", p * 2 + c * 8)"}`,
	}))

	for name, options := range map[string]map[string]string{
		"missing": {ModelBillingModeOption: `{"m":"tiered_expr"}`},
		"empty": {
			ModelBillingModeOption: `{"m":"tiered_expr"}`,
			ModelBillingExprOption: `{"m":"   "}`,
		},
		"invalid": {
			ModelBillingModeOption: `{"m":"tiered_expr"}`,
			ModelBillingExprOption: `{"m":"p * + +"}`,
		},
		"unknown mode": {ModelBillingModeOption: `{"m":"silent_fallback"}`},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validatePricingConfiguration(options))
		})
	}
}

func TestPricingConfigurationRejectsDuplicatesAndUnboundedNumbers(t *testing.T) {
	for name, options := range map[string]map[string]string{
		"duplicate model price": {ModelPriceOption: `{"m":{"prompt":1,"prompt":2,"completion":3}}`},
		"duplicate group":       {GroupRatioOption: `{"default":1,"default":2}`},
		"duplicate nested":      {GroupGroupRatioOption: `{"vip":{"default":1,"default":0}}`},
		"huge model price":      {ModelPriceOption: `{"m":{"prompt":1000000000001,"completion":3}}`},
		"huge group ratio":      {GroupRatioOption: `{"default":1000000000001}`},
		"zero base ratio":       {GroupRatioOption: `{"default":0}`},
		"non finite":            {ModelRatioOption: `{"m":1e999}`},
		"null model prices":     {ModelPriceOption: `null`},
		"null model ratios":     {ModelRatioOption: `null`},
		"null group ratios":     {GroupRatioOption: `null`},
		"null special ratios":   {GroupGroupRatioOption: `null`},
		"null billing modes":    {ModelBillingModeOption: `null`},
		"null billing exprs":    {ModelBillingExprOption: `null`},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validatePricingConfiguration(options))
		})
	}

	many := make(map[string]float64, maxPricingConfigEntries+1)
	for i := 0; i <= maxPricingConfigEntries; i++ {
		many[fmt.Sprintf("model-%05d", i)] = 1
	}
	raw, err := jsonutil.Marshal(many)
	require.NoError(t, err)
	require.Less(t, len(raw), MaxOptionValueBytes)
	require.Error(t, validatePricingConfiguration(map[string]string{ModelRatioOption: string(raw)}))
}

func TestPricingReliabilityAndToolPublicationIsAtomic(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		RetryTimesOption: "2",
		ToolPriceOption:  `{"custom":4}`,
	}))
	beforeVersion := CaptureToolPriceSnapshot().Version()
	assert.Equal(t, 2, GetChannelReliabilitySetting().RetryTimes)
	assert.Equal(t, `{"custom":4}`, CaptureToolPriceSnapshot().CanonicalJSON())

	err := UpdateOptions(map[string]string{
		RetryTimesOption:       "8",
		ToolPriceOption:        `{"custom":9}`,
		ModelBillingModeOption: `{"broken":"tiered_expr"}`,
	})
	require.Error(t, err)
	assert.Equal(t, "2", GetOption(RetryTimesOption))
	assert.Equal(t, 2, GetChannelReliabilitySetting().RetryTimes)
	assert.Equal(t, beforeVersion, CaptureToolPriceSnapshot().Version())
	assert.Equal(t, `{"custom":4}`, CaptureToolPriceSnapshot().CanonicalJSON())

	var stored int64
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ? AND value = ?", RetryTimesOption, "8").Count(&stored).Error)
	assert.Zero(t, stored)
}
