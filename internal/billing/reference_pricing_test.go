package billing

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"sync"
	"testing"
)

func setReferencePricingOptions(t *testing.T, modes, fixedPrices, modelRatios, completionRatios string) {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  modes,
		setting.PerCallModelPriceOption: fixedPrices,
		setting.ModelRatioOption:        modelRatios,
		setting.CompletionRatioOption:   completionRatios,
	}))
}

func TestReferencePricingFixedPriceModelRatioAndCompletionRatioSemantics(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, UpdateGroupRatioOption(`{"default":1,"vip":2}`))
	require.NoError(t, UpdateGroupGroupRatioOption(`{"default":{"vip":0.25}}`))
	setReferencePricingOptions(t,
		`{"fixed":"reference","fixed-group":"reference","ratio":"reference","ratio-round":"reference","default-completion":"reference","zero-fixed":"reference","zero-ratio":"reference","zero-completion":"reference"}`,
		`{"fixed":0.0000036,"fixed-group":0.004,"zero-fixed":0}`,
		`{"fixed":99,"ratio":2,"ratio-round":0.0036,"default-completion":2,"zero-fixed":99,"zero-ratio":0,"zero-completion":2}`,
		`{"ratio":3,"zero-completion":0}`,
	)

	fixed, enabled, err := ResolveOrdinaryReferenceBillingPlan("fixed", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	preConsumed, err := fixed.PreConsumeQuota(100, 9_999, true)
	require.NoError(t, err)
	assert.Equal(t, 1, preConsumed, "fixed pre-consume truncates 1.8 quota once")
	settled, clamp, err := fixed.SettlementQuota(1, 1)
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.Equal(t, 2, settled, "fixed settlement rounds 1.8 quota")
	settled, clamp, err = fixed.SettlementQuota(0, 0)
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.Zero(t, settled, "an explicit zero-usage report is not billable")
	assert.Equal(t, map[string]any{
		"billing_mode": BillingModeReference,
		"group_ratio":  1.0,
		"model_price":  0.0000036,
		"use_price":    true,
	}, fixed.BillingLogFields())

	ratio, enabled, err := ResolveOrdinaryReferenceBillingPlan("ratio", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	belowFloor, err := ratio.PreConsumeQuota(100, 10, true)
	require.NoError(t, err)
	assert.Equal(t, 1_020, belowFloor)
	aboveFloor, err := ratio.PreConsumeQuota(600, 10, true)
	require.NoError(t, err)
	assert.Equal(t, 1_220, aboveFloor)
	withoutLimit, err := ratio.PreConsumeQuota(100, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 1_000, withoutLimit)
	settled, clamp, err = ratio.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.Equal(t, 260, settled)

	ratioRound, enabled, err := ResolveOrdinaryReferenceBillingPlan("ratio-round", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	ratioRoundHold, err := ratioRound.PreConsumeQuota(100, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 1, ratioRoundHold, "ratio pre-consume truncates 1.8 quota")
	settled, _, err = ratioRound.SettlementQuota(500, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, settled, "ratio settlement rounds 1.8 quota")

	defaultCompletion, enabled, err := ResolveOrdinaryReferenceBillingPlan("default-completion", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	settled, _, err = defaultCompletion.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Equal(t, 220, settled, "missing CompletionRatio defaults to one")

	zeroFixed, enabled, err := ResolveOrdinaryReferenceBillingPlan("zero-fixed", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	settled, _, err = zeroFixed.SettlementQuota(100, 100)
	require.NoError(t, err)
	assert.Zero(t, settled, "an explicitly configured zero fixed price wins over ModelRatio")

	zeroRatio, enabled, err := ResolveOrdinaryReferenceBillingPlan("zero-ratio", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	settled, _, err = zeroRatio.SettlementQuota(100, 100)
	require.NoError(t, err)
	assert.Zero(t, settled)

	zeroCompletion, enabled, err := ResolveOrdinaryReferenceBillingPlan("zero-completion", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	settled, _, err = zeroCompletion.SettlementQuota(0, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, settled, "non-zero ratio pricing retains the reference one-quota minimum")

	nested, enabled, err := ResolveOrdinaryReferenceBillingPlan("ratio", "default", "vip")
	require.NoError(t, err)
	require.True(t, enabled)
	nestedPreConsume, err := nested.PreConsumeQuota(100, 10, true)
	require.NoError(t, err)
	assert.Equal(t, 255, nestedPreConsume)
	settled, _, err = nested.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Equal(t, 65, settled)
	assert.Equal(t, 0.25, nested.BillingLogFields()["user_group_ratio"])

	fixedNested, enabled, err := ResolveOrdinaryReferenceBillingPlan("fixed-group", "default", "vip")
	require.NoError(t, err)
	require.True(t, enabled)
	fixedNestedPreConsume, err := fixedNested.PreConsumeQuota(100, 10, true)
	require.NoError(t, err)
	assert.Equal(t, 500, fixedNestedPreConsume)
	settled, _, err = fixedNested.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Equal(t, 500, settled)
}

func TestReferencePricingCompletionFamilyDefaultsLocksAndQualifiedOverride(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, UpdateGroupRatioOption(`{"default":1}`))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: `{
			"gpt-4o":"reference",
			"gpt-4o-2024-05-13":"reference",
			"vendor/gpt-4o-2024-05-13":"reference",
			"gpt-image-1":"reference",
			"gpt-4o-gizmo-custom":"reference"
		}`,
		setting.PerCallModelPriceOption: `{}`,
		setting.ModelRatioOption: `{
			"gpt-4o":1,
			"gpt-4o-2024-05-13":1,
			"vendor/gpt-4o-2024-05-13":1,
			"gpt-image-1":1,
			"gpt-4o-gizmo-*":1
		}`,
	}))

	completionCharge := func(modelName string) int {
		plan, enabled, err := ResolveOrdinaryReferenceBillingPlan(modelName, "default", "default")
		require.NoError(t, err)
		require.True(t, enabled)
		quota, _, err := plan.SettlementQuota(0, 1)
		require.NoError(t, err)
		return quota
	}
	assert.Equal(t, 4, completionCharge("gpt-4o"))
	assert.Equal(t, 3, completionCharge("gpt-4o-2024-05-13"))
	assert.Equal(t, 1, completionCharge("vendor/gpt-4o-2024-05-13"))
	assert.Equal(t, 8, completionCharge("gpt-image-1"), "fresh defaults include the reference image ratio")
	assert.Equal(t, 3, completionCharge("gpt-4o-gizmo-custom"), "formatted gizmo names use the default wildcard entry")

	// Publishing CompletionRatio replaces the default map. Unlocked families
	// remain configurable, locked families do not, and a provider-qualified
	// exact name is intentionally checked before family rules.
	require.NoError(t, setting.UpdateOption(setting.CompletionRatioOption, `{
		"gpt-4o":2,
		"gpt-4o-2024-05-13":99,
		"vendor/gpt-4o-2024-05-13":7,
		"gpt-4o-gizmo-*":9
	}`))
	assert.Equal(t, 2, completionCharge("gpt-4o"))
	assert.Equal(t, 3, completionCharge("gpt-4o-2024-05-13"))
	assert.Equal(t, 7, completionCharge("vendor/gpt-4o-2024-05-13"))
	assert.Equal(t, 2, completionCharge("gpt-image-1"), "an explicit map replaces the fresh default entry")
	assert.Equal(t, 9, completionCharge("gpt-4o-gizmo-custom"))
}

func TestReferencePricingValidationDefaultsAndFailClosedOverflow(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, UpdateGroupRatioOption(`{"default":1}`))

	// Malformed dormant compatibility data is rejected at the same atomic
	// publication boundary as active pricing data.
	err := setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: `{}`,
		setting.ModelRatioOption:       `not-json`,
	})
	require.Error(t, err)
	assert.Empty(t, setting.GetOption(setting.ModelBillingModeOption))
	assert.Empty(t, setting.GetOption(setting.ModelRatioOption))
	_, enabled, err := ResolveOrdinaryReferenceBillingPlan("flat", "default", "default")
	require.NoError(t, err)
	assert.False(t, enabled)
	assert.Equal(t, 500, ComputeQuota("flat", "default", 1_000, 0))

	tests := []struct {
		name                string
		modes               string
		fixed               string
		ratio               string
		completion          string
		publishableButUnset bool
	}{
		{name: "missing price", modes: `{"m":"reference"}`, fixed: `{}`, ratio: `{}`, completion: `{}`, publishableButUnset: true},
		{name: "malformed mode", modes: `{`, fixed: `{}`, ratio: `{"m":1}`, completion: `{}`},
		{name: "unsupported selected mode", modes: `{"m":"mystery"}`, fixed: `{}`, ratio: `{}`, completion: `{}`},
		{name: "null ratio map", modes: `{"m":"reference"}`, fixed: `{}`, ratio: `null`, completion: `{}`},
		{name: "negative ratio", modes: `{"m":"reference"}`, fixed: `{}`, ratio: `{"m":-1}`, completion: `{}`},
		{name: "oversized fixed price", modes: `{"m":"reference"}`, fixed: `{"m":10000000000000}`, ratio: `{}`, completion: `{}`},
		{name: "invalid completion ratio", modes: `{"m":"reference"}`, fixed: `{}`, ratio: `{"m":1}`, completion: `{"m":-1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeModes := setting.GetOption(setting.ModelBillingModeOption)
			updateErr := setting.UpdateOptions(map[string]string{
				setting.ModelBillingModeOption:  test.modes,
				setting.PerCallModelPriceOption: test.fixed,
				setting.ModelRatioOption:        test.ratio,
				setting.CompletionRatioOption:   test.completion,
			})
			if test.publishableButUnset {
				require.NoError(t, updateErr)
				_, _, resolveErr := ResolveOrdinaryReferenceBillingPlan("m", "default", "default")
				require.Error(t, resolveErr)
				assert.ErrorIs(t, resolveErr, ErrReferencePricingConfiguration)
				return
			}
			require.Error(t, updateErr)
			assert.Equal(t, beforeModes, setting.GetOption(setting.ModelBillingModeOption))
		})
	}

	setReferencePricingOptions(t, `{"m":"reference"}`, `{}`, `{"m":2}`, `{}`)
	require.NoError(t, setting.UpdateOption(setting.PreConsumedQuotaOption, "0"))
	plan, enabled, err := ResolveOrdinaryReferenceBillingPlan("m", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	quota, err := plan.PreConsumeQuota(100, 10, true)
	require.NoError(t, err)
	assert.Equal(t, 220, quota, "an explicit zero floor is distinct from the missing default")

	require.NoError(t, setting.UpdateOption(setting.PreConsumedQuotaOption, "invalid"))
	_, _, err = ResolveOrdinaryReferenceBillingPlan("m", "default", "default")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReferencePricingConfiguration)

	// Fixed-price mode never consults the token pre-consume floor.
	setReferencePricingOptions(t, `{"huge":"reference"}`, `{"huge":1000000000000}`, `{}`, `{}`)
	huge, enabled, err := ResolveOrdinaryReferenceBillingPlan("huge", "default", "default")
	require.NoError(t, err)
	require.True(t, enabled)
	_, err = huge.PreConsumeQuota(1, 0, false)
	var preConsumeClamp *quotamath.QuotaClamp
	require.ErrorAs(t, err, &preConsumeClamp)
	_, settleClamp, err := huge.SettlementQuota(1, 0)
	require.Error(t, err)
	require.NotNil(t, settleClamp)
	assert.Equal(t, "overflow", settleClamp.Reason)
}

func TestReferencePricingExplicitRatioModeUsesOrdinaryBilling(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, UpdateGroupRatioOption(`{"default":1}`))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption: `{"ordinary":"ratio"}`,
		setting.ModelRatioOption:       `{}`,
	}))
	SetModelPriceRegistry(map[string]ModelPrice{"ordinary": {Prompt: 2, Completion: 6}})

	_, enabled, err := ResolveOrdinaryReferenceBillingPlan("ordinary", "default", "default")
	require.NoError(t, err)
	assert.False(t, enabled)
	assert.Equal(t, 250, ComputeQuota("ordinary", "default", 100, 50))
}

func TestReferencePricingHotReloadKeepsInFlightSnapshotAndPublishesCoherently(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, UpdateGroupRatioOption(`{"default":1,"vip":2}`))
	require.NoError(t, UpdateGroupGroupRatioOption(`{"default":{"vip":0.5}}`))
	setReferencePricingOptions(t,
		`{"m":"reference"}`, `{}`, `{"m":2}`, `{"m":3}`,
	)
	require.NoError(t, ReloadPricingOptions())

	oldPlan, enabled, err := ResolveOrdinaryReferenceBillingPlan("m", "default", "vip")
	require.NoError(t, err)
	require.True(t, enabled)
	oldCharge, _, err := oldPlan.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Equal(t, 130, oldCharge)

	// Simulate a second node changing all jointly relevant rows. Sync publishes
	// one settings snapshot; oldPlan stays immutable while new requests see it.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.ModelRatioOption).Update("value", `{"m":4}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.CompletionRatioOption).Update("value", `{"m":5}`).Error)
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupGroupRatioOption).Update("value", `{"default":{"vip":0.25}}`).Error)
	require.NoError(t, SyncRuntimeOptions())

	oldChargeAfterReload, _, err := oldPlan.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Equal(t, oldCharge, oldChargeAfterReload)
	newPlan, enabled, err := ResolveOrdinaryReferenceBillingPlan("m", "default", "vip")
	require.NoError(t, err)
	require.True(t, enabled)
	newCharge, _, err := newPlan.SettlementQuota(100, 10)
	require.NoError(t, err)
	assert.Equal(t, 150, newCharge)

	// An invalid synchronized snapshot is rejected before existing live group
	// and USD-per-million registries are replaced.
	SetModelPriceRegistry(map[string]ModelPrice{"sentinel": {Prompt: 7, Completion: 9}})
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.ModelRatioOption).Update("value", `{"m":-1}`).Error)
	err = setting.Sync()
	require.Error(t, err)
	assert.Equal(t, 7.0, ExportedModelPrices()["sentinel"].Prompt)
}

func TestReferencePricingUnsetRatioRequiresPerUserOptIn(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	require.NoError(t, UpdateGroupRatioOption(`{"default":1}`))
	setReferencePricingOptions(t, `{"unpriced":"reference"}`, `{}`, `{}`, `{}`)
	require.NoError(t, setting.UpdateOption(setting.PreConsumedQuotaOption, "500"))

	user := model.User{
		Username: "accept-unset-user", Password: "x", DisplayName: "Accept Unset",
		Role: 1, Status: model.UserStatusEnabled, Group: "default", Setting: `{}`,
	}
	require.NoError(t, model.DB.Create(&user).Error)

	_, enabled, err := ResolveOrdinaryReferenceBillingPlan("unpriced", "default", "default")
	require.ErrorIs(t, err, ErrReferencePricingConfiguration)
	assert.False(t, enabled, "the context-free resolver remains fail closed")

	_, enabled, err = ResolveOrdinaryReferenceBillingPlanForUser(
		user.Id, "unpriced", "default", "default",
	)
	require.ErrorIs(t, err, ErrReferencePricingConfiguration)
	assert.False(t, enabled)
	filtered, err := FilterModelsByReferencePricing(map[string]bool{
		"unpriced": true,
		"ordinary": true,
	}, false)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"ordinary": true}, filtered)

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update(
		"setting", `{"accept_unset_model_ratio_model":true}`,
	).Error)
	plan, enabled, err := ResolveOrdinaryReferenceBillingPlanForUser(
		user.Id, "unpriced", "default", "default",
	)
	require.NoError(t, err)
	require.True(t, enabled)
	assert.Equal(t, referenceUnsetModelRatio, plan.BillingLogFields()["model_ratio"])
	filtered, err = FilterModelsByReferencePricing(map[string]bool{
		"unpriced": true,
		"ordinary": true,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"ordinary": true, "unpriced": true}, filtered)
	quota, clamp, err := plan.SettlementQuota(1, 0)
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.Equal(t, 38, quota)

	async, enabled, err := ResolveReferenceAsyncTaskBillingPlanForUser(
		user.Id, "unpriced", "default", "default",
	)
	require.NoError(t, err)
	require.True(t, enabled)
	assert.Equal(t, "37.5", async.ModelRatio)

	// The coherent settings reload accepts the missing map entry because the
	// final decision is intentionally user-specific at request time.
	require.NoError(t, ReloadPricingOptions())
}

func TestReferencePricingConcurrentResolutionUsesOnlyCoherentSnapshots(t *testing.T) {
	initBillingDB(t)
	database, err := model.DB.DB()
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	require.NoError(t, UpdateGroupRatioOption(`{"default":1}`))
	setReferencePricingOptions(t, `{"m":"reference"}`, `{}`, `{"m":2}`, `{"m":3}`)

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 200; iteration++ {
				plan, enabled, resolveErr := ResolveOrdinaryReferenceBillingPlan("m", "default", "default")
				if resolveErr == nil && !enabled {
					resolveErr = errors.New("reference pricing unexpectedly disabled")
				}
				if resolveErr != nil {
					errorsSeen <- resolveErr
					return
				}
				quota, _, settleErr := plan.SettlementQuota(100, 10)
				if settleErr != nil || (quota != 260 && quota != 600) {
					if settleErr == nil {
						settleErr = errors.New("mixed reference pricing snapshot")
					}
					errorsSeen <- settleErr
					return
				}
			}
		}()
	}
	for iteration := 0; iteration < 40; iteration++ {
		if iteration%2 == 0 {
			require.NoError(t, setting.UpdateOptions(map[string]string{
				setting.ModelRatioOption:      `{"m":4}`,
				setting.CompletionRatioOption: `{"m":5}`,
			}))
		} else {
			require.NoError(t, setting.UpdateOptions(map[string]string{
				setting.ModelRatioOption:      `{"m":2}`,
				setting.CompletionRatioOption: `{"m":3}`,
			}))
		}
	}
	wait.Wait()
	close(errorsSeen)
	for concurrentErr := range errorsSeen {
		require.NoError(t, concurrentErr)
	}
}
