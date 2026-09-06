package billing

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"strings"
	"testing"
)

func initPricingCatalogTestDB(t *testing.T) {
	t.Helper()
	testutil.InitTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.Option{}))
	require.NoError(t, setting.Init())
}

func TestBuildPricingCatalogFiltersEnrichesAndVersionsDeterministically(t *testing.T) {
	initPricingCatalogTestDB(t)
	previousPrices := ExportedModelPrices()
	previousRatios := ExportedGroupRatios()
	previousSpecialRatios := ExportedGroupGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousRatios)
		SetGroupGroupRatios(previousSpecialRatios)
	})
	SetModelPriceRegistry(map[string]ModelPrice{
		"alpha":  {Prompt: 2, Completion: 8},
		"beta":   {Prompt: 3, Completion: 6},
		"hidden": {Prompt: 1, Completion: 1},
	})
	SetGroupRatios(map[string]float64{"default": 1, "vip": 1.5, "private": 2})
	SetGroupGroupRatios(map[string]map[string]float64{})

	vendor := model.Vendor{Name: "Acme", Description: "catalog vendor", Status: 1}
	require.NoError(t, model.DB.Create(&vendor).Error)
	require.NoError(t, model.DB.Create(&[]model.Model{
		{ModelName: "alpha", Description: "Alpha model", VendorID: vendor.Id, Status: 1},
		{ModelName: "beta", Status: 0},
	}).Error)
	require.NoError(t, model.DB.Model(&model.Model{}).Where("model_name = ?", "beta").Update("status", 0).Error,
		"the fixture explicitly disables beta after applying the reference enabled-by-default schema")
	channels := []model.Channel{
		{Type: int(channelcatalog.ChannelTypeOpenAI), Key: "one", Name: "one", Status: channelcatalog.ChannelStatusEnabled},
		{Type: int(channelcatalog.ChannelTypeAnthropic), Key: "two", Name: "two", Status: channelcatalog.ChannelStatusEnabled},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.Ability{
		{Group: "default", Model: "alpha", ChannelId: channels[0].Id, Enabled: true},
		{Group: "vip", Model: "alpha", ChannelId: channels[1].Id, Enabled: true},
		{Group: "default", Model: "beta", ChannelId: channels[0].Id, Enabled: true},
		{Group: "private", Model: "hidden", ChannelId: channels[0].Id, Enabled: true},
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	catalog, err := BuildPricingCatalog("")
	require.NoError(t, err)
	require.Len(t, catalog.Items, 1, "disabled metadata and non-public groups must be filtered")
	item := catalog.Items[0]
	assert.Equal(t, "alpha", item.ModelName)
	assert.Equal(t, "Alpha model", item.Description)
	assert.Equal(t, vendor.Id, item.VendorID)
	assert.Equal(t, 2.0, item.PromptPrice)
	assert.Equal(t, 8.0, item.CompletionPrice)
	assert.Equal(t, 1.0, item.ModelRatio)
	assert.Equal(t, 4.0, item.CompletionRatio)
	assert.Equal(t, []string{"default", "vip"}, item.EnableGroup)
	assert.Equal(t, []channelcatalog.EndpointType{
		channelcatalog.EndpointTypeOpenAI,
		channelcatalog.EndpointTypeAnthropic,
	}, item.SupportedEndpointTypes)
	require.Len(t, catalog.Vendors, 1)
	assert.Equal(t, "Acme", catalog.Vendors[0].Name)
	assert.Equal(t, map[string]float64{"default": 1, "vip": 1.5}, catalog.GroupRatio)
	assert.NotContains(t, catalog.UsableGroup, "private")
	assert.Equal(t, []string{"default", "vip"}, catalog.AutoGroups)
	assert.Len(t, catalog.Version, 64)

	again, err := BuildPricingCatalog("")
	require.NoError(t, err)
	assert.Equal(t, catalog.Version, again.Version)
	assert.Equal(t, catalog.Items, again.Items)

	catalog.GroupRatio["default"] = 99
	catalog.UsableGroup["default"] = "mutated"
	catalog.SupportedEndpoint["openai"] = channelcatalog.EndpointInfo{Path: "/mutated", Method: "DELETE"}
	third, err := BuildPricingCatalog("")
	require.NoError(t, err)
	assert.Equal(t, 1.0, third.GroupRatio["default"])
	assert.NotEqual(t, "mutated", third.UsableGroup["default"])
	assert.Equal(t, "/v1/chat/completions", third.SupportedEndpoint["openai"].Path)

	SetGroupGroupRatios(map[string]map[string]float64{"default": {"vip": 0.4}})
	signed, err := BuildPricingCatalog("default")
	require.NoError(t, err)
	assert.Equal(t, 0.4, signed.GroupRatio["vip"], "pricing must expose the caller's effective ratio")
	assert.NotEqual(t, third.Version, signed.Version)
}

func TestBuildPricingCatalogRejectsInvalidLivePrices(t *testing.T) {
	initPricingCatalogTestDB(t)
	previousPrices := ExportedModelPrices()
	previousRatios := ExportedGroupRatios()
	previousSpecialRatios := ExportedGroupGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousRatios)
		SetGroupGroupRatios(previousSpecialRatios)
	})
	SetModelPriceRegistry(map[string]ModelPrice{"bad": {Prompt: -1, Completion: 2}})
	SetGroupRatios(map[string]float64{"default": 1})
	SetGroupGroupRatios(map[string]map[string]float64{})
	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), Key: "one", Name: "one",
		Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "bad", ChannelId: channel.Id, Enabled: true,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	catalog, err := BuildPricingCatalog("")
	require.NoError(t, err)
	assert.Empty(t, catalog.Items, "invalid price records must fail closed instead of reaching clients")
}

func TestBuildPricingCatalogDescribesFixedRatioAndTieredBilling(t *testing.T) {
	initPricingCatalogTestDB(t)
	previousPrices := ExportedModelPrices()
	previousRatios := ExportedGroupRatios()
	previousSpecialRatios := ExportedGroupGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousRatios)
		SetGroupGroupRatios(previousSpecialRatios)
	})
	SetModelPriceRegistry(map[string]ModelPrice{
		"fixed":        {Prompt: 7, Completion: 21},
		"ordinary":     {Prompt: 4, Completion: 12},
		"ratio":        {Prompt: 7, Completion: 21},
		"tiered":       {Prompt: 2, Completion: 8},
		"tiered-empty": {Prompt: 3, Completion: 9},
	})
	SetGroupRatios(map[string]float64{"default": 1})
	SetGroupGroupRatios(map[string]map[string]float64{})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"fixed":"reference","ordinary":"ratio","ratio":"reference","tiered":"tiered_expr"}`,
		setting.ModelBillingExprOption:  `{"tiered":"tier(\"base\", p * 2 + c * 8)"}`,
		setting.PerCallModelPriceOption: `{"fixed":0.25}`,
		setting.ModelRatioOption:        `{"ratio":3}`,
		setting.CompletionRatioOption:   `{"ratio":4}`,
	}))

	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), Key: "one", Name: "one",
		Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	for _, modelName := range []string{"fixed", "ordinary", "ratio", "tiered", "tiered-empty"} {
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: modelName, ChannelId: channel.Id, Enabled: true,
		}).Error)
	}
	require.NoError(t, channelssvc.InitAbilityCache())

	catalog, err := BuildPricingCatalog("")
	require.NoError(t, err)
	byName := make(map[string]PricingCatalogItem, len(catalog.Items))
	for _, item := range catalog.Items {
		byName[item.ModelName] = item
	}
	require.Len(t, byName, 5)

	fixed := byName["fixed"]
	assert.Equal(t, 1, fixed.QuotaType)
	assert.Equal(t, 0.25, fixed.ModelPrice)
	assert.Zero(t, fixed.ModelRatio)
	assert.Zero(t, fixed.PromptPrice)
	assert.Zero(t, fixed.CompletionPrice)
	assert.Empty(t, fixed.BillingMode)
	assert.Empty(t, fixed.BillingExpr)

	ordinary := byName["ordinary"]
	assert.Equal(t, 0, ordinary.QuotaType)
	assert.Equal(t, 2.0, ordinary.ModelRatio)
	assert.Equal(t, 4.0, ordinary.PromptPrice)
	assert.Equal(t, 12.0, ordinary.CompletionPrice)
	assert.Equal(t, 3.0, ordinary.CompletionRatio)
	assert.Empty(t, ordinary.BillingMode)
	assert.Empty(t, ordinary.BillingExpr)

	ratio := byName["ratio"]
	assert.Equal(t, 0, ratio.QuotaType)
	assert.Equal(t, 3.0, ratio.ModelRatio)
	assert.Equal(t, 4.0, ratio.CompletionRatio)
	assert.Equal(t, 6.0, ratio.PromptPrice)
	assert.Equal(t, 24.0, ratio.CompletionPrice)
	assert.Empty(t, ratio.BillingMode)
	assert.Empty(t, ratio.BillingExpr)

	tiered := byName["tiered"]
	assert.Equal(t, BillingModeTieredExpr, tiered.BillingMode)
	assert.Equal(t, `tier("base", p * 2 + c * 8)`, tiered.BillingExpr)
	assert.Equal(t, 1.0, tiered.ModelRatio, "the compatibility ratio represents quota per token")

	empty := byName["tiered-empty"]
	assert.Empty(t, empty.BillingMode, "a model without an explicit mode uses flat billing")
	assert.Empty(t, empty.BillingExpr)

	firstVersion := catalog.Version
	require.NoError(t, setting.UpdateOption(setting.ModelBillingExprOption,
		`{"tiered":"tier(\"base\", p * 3 + c * 9)"}`))
	changed, err := BuildPricingCatalog("")
	require.NoError(t, err)
	assert.NotEqual(t, firstVersion, changed.Version)
}

func TestPricingCatalogBillingConfigurationIsRejectedAtPublication(t *testing.T) {
	initPricingCatalogTestDB(t)
	tests := []struct {
		name        string
		modes       string
		expressions string
	}{
		{name: "malformed modes", modes: `not-json`, expressions: `{}`},
		{name: "unsupported selected mode", modes: `{"m":"surprise"}`, expressions: `{}`},
		{name: "malformed selected expression map", modes: `{"m":"tiered_expr"}`, expressions: `not-json`},
		{
			name: "oversized selected expression", modes: `{"m":"tiered_expr"}`,
			expressions: func() string {
				payload, err := jsonutil.Marshal(map[string]string{
					"m": strings.Repeat("x", pricingCatalogMaxBillingExpressionBytes+1),
				})
				require.NoError(t, err)
				return string(payload)
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeModes := setting.GetOption(setting.ModelBillingModeOption)
			beforeExpressions := setting.GetOption(setting.ModelBillingExprOption)
			err := setting.UpdateOptions(map[string]string{
				setting.ModelBillingModeOption: test.modes,
				setting.ModelBillingExprOption: test.expressions,
			})
			require.Error(t, err)
			assert.Equal(t, beforeModes, setting.GetOption(setting.ModelBillingModeOption))
			assert.Equal(t, beforeExpressions, setting.GetOption(setting.ModelBillingExprOption))
		})
	}
}

func TestBuildPricingCatalogRejectsNonFiniteAdjustedPrice(t *testing.T) {
	initPricingCatalogTestDB(t)
	previousPrices := ExportedModelPrices()
	previousRatios := ExportedGroupRatios()
	previousSpecialRatios := ExportedGroupGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousRatios)
		SetGroupGroupRatios(previousSpecialRatios)
	})
	SetModelPriceRegistry(map[string]ModelPrice{
		"overflow": {Prompt: 1e300, Completion: 1e300},
	})
	SetGroupRatios(map[string]float64{"default": 1e20})
	SetGroupGroupRatios(map[string]map[string]float64{})
	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), Key: "one", Name: "one",
		Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "overflow", ChannelId: channel.Id, Enabled: true,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	_, err := BuildPricingCatalog("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid adjusted pricing")
}
