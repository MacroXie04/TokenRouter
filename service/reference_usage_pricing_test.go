package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

func referenceUsageTestPlan() ReferenceBillingPlan {
	return ReferenceBillingPlan{
		modelName: "usage-model", groupRatio: 0.5, modelRatio: 2, completionRatio: 3,
		usageRatioPolicy: setting.UsageRatioPolicy{
			CacheRatio: 0.1, CacheCreationRatio: 1.25,
			CacheCreationFiveMinuteRatio: 1.25, CacheCreationOneHourRatio: 2,
			ImageRatio: 2, AudioRatio: 8, AudioCompletionRatio: 2,
			AudioRatioConfigured: true, AudioCompletionConfigured: true,
			EnableFreeModelPreConsume: true,
		},
	}
}

func TestReferenceUsageSettlementAppliesTextUsageClasses(t *testing.T) {
	usage := &protocolkit.Usage{
		PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100,
		PromptCacheHitTokens: 100, PromptCacheCreationTokens: 200,
		PromptCacheCreation5mTokens: 150, PromptCacheCreation1hTokens: 50,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			CachedTokens: 100, CachedCreationTokens: 200,
			CacheCreation5mTokens: 150, CacheCreation1hTokens: 50,
			ImageTokens: 50,
		},
	}
	plan := referenceUsageTestPlan()

	normal, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 1310, normal.Quota)
	assert.Nil(t, normal.Clamp)
	assert.Equal(t, map[string]any{
		"cache_tokens":             100,
		"cache_ratio":              0.1,
		"image":                    true,
		"image_ratio":              2.0,
		"image_output":             50,
		"cache_creation_tokens":    200,
		"cache_creation_ratio":     1.25,
		"cache_write_tokens":       200,
		"cache_creation_tokens_5m": 150,
		"cache_creation_ratio_5m":  1.25,
		"cache_creation_tokens_1h": 50,
		"cache_creation_ratio_1h":  2.0,
	}, normal.BillingLogFields())

	claude, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{IsClaude: true})
	require.NoError(t, err)
	assert.Equal(t, 1348, claude.Quota, "one-hour Claude cache writes use the reference 6/3.75 multiplier")
	assert.Equal(t, true, claude.BillingLogFields()["claude"])
	assert.Equal(t, "anthropic", claude.BillingLogFields()["usage_semantic"])

	// The calculator must not canonicalize aliases by mutating provider usage.
	assert.Nil(t, usage.InputTokensDetails)
}

func TestReferenceUsageSettlementAppliesAudioOnlyForApplicablePaths(t *testing.T) {
	usage := &protocolkit.Usage{
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
		PromptTokensDetails:     &protocolkit.InputTokenDetails{TextTokens: 20, AudioTokens: 80},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{TextTokens: 10, AudioTokens: 40},
	}
	plan := referenceUsageTestPlan()
	settlement, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 1330, settlement.Quota)
	assert.Equal(t, map[string]any{
		"audio": true, "audio_input": 80, "audio_output": 40,
		"text_input": 20, "text_output": 10,
		"audio_ratio": 8.0, "audio_completion_ratio": 2.0,
	}, settlement.BillingLogFields())

	plan.usageRatioPolicy.AudioRatio = 1
	plan.usageRatioPolicy.AudioCompletionRatio = 1
	plan.usageRatioPolicy.AudioRatioConfigured = false
	plan.usageRatioPolicy.AudioCompletionConfigured = false
	ordinary, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 250, ordinary.Quota, "ordinary unconfigured models keep text settlement")
	assert.Equal(t, 0.1, ordinary.BillingLogFields()["cache_ratio"])

	realtime, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{ForceAudio: true, Realtime: true})
	require.NoError(t, err)
	assert.Equal(t, 170, realtime.Quota)
	assert.Equal(t, true, realtime.BillingLogFields()["ws"])
	assert.NotContains(t, realtime.BillingLogFields(), "audio")
}

func TestReferenceUsageSettlementAliasesOverlapFixedPriceAndMinimum(t *testing.T) {
	plan := referenceUsageTestPlan()
	aliases := &protocolkit.Usage{
		InputTokens: 100, OutputTokens: 10,
		InputTokensDetails: &protocolkit.InputTokenDetails{
			CachedTokens: 80, CacheWriteTokens: 80, ImageTokens: 80,
		},
	}
	settlement, err := plan.SettlementUsageQuota(aliases, ReferenceUsageContext{})
	require.NoError(t, err)
	// Overlapping subcategories clamp the base to zero instead of subtracting
	// into a negative credit: 80*.1 + 80*1.25 + 80*2 + 10*3.
	assert.Equal(t, 298, settlement.Quota)
	assert.Equal(t, 100, aliases.InputTokens)
	assert.Equal(t, 0, aliases.PromptTokens, "alias normalization is performed on a copy")

	fixed := plan
	fixed.useFixedPrice = true
	fixed.fixedPrice = 0.002
	fixedResult, err := fixed.SettlementUsageQuota(aliases, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 500, fixedResult.Quota)
	zero, err := fixed.SettlementUsageQuota(&protocolkit.Usage{}, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Zero(t, zero.Quota)

	minimum := plan
	minimum.groupRatio = 1
	minimum.modelRatio = 1
	minimum.completionRatio = 0
	minimumResult, err := minimum.SettlementUsageQuota(
		&protocolkit.Usage{CompletionTokens: 1, TotalTokens: 1}, ReferenceUsageContext{},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, minimumResult.Quota)
}

func TestReferenceUsageSettlementFailsClosedOnInvalidOrOverflowingUsage(t *testing.T) {
	plan := referenceUsageTestPlan()
	_, err := plan.SettlementUsageQuota(&protocolkit.Usage{PromptTokens: -1}, ReferenceUsageContext{})
	require.ErrorIs(t, err, ErrReferencePricingConfiguration)

	_, err = plan.SettlementUsageQuota(&protocolkit.Usage{
		PromptTokens:                int(common.MaxQuota),
		PromptCacheCreation5mTokens: int(common.MaxQuota),
		PromptCacheCreation1hTokens: 1,
	}, ReferenceUsageContext{IsClaude: true})
	require.ErrorIs(t, err, ErrReferencePricingConfiguration)

	overflow := plan
	overflow.modelRatio = 1e12
	result, err := overflow.SettlementUsageQuota(
		&protocolkit.Usage{PromptTokens: int(common.MaxQuota), TotalTokens: int(common.MaxQuota)},
		ReferenceUsageContext{},
	)
	require.Error(t, err)
	assert.NotNil(t, result.Clamp)
	assert.Equal(t, "overflow", result.Clamp.Reason)
}

func TestReferenceFreeModelSnapshotControlsOnlyPreConsume(t *testing.T) {
	plan := referenceUsageTestPlan()
	plan.modelRatio = 0
	plan.freeModel = true
	hold, err := plan.PreConsumeQuota(100, 100, true)
	require.NoError(t, err)
	assert.Zero(t, hold)
	assert.True(t, plan.FreeModel())
	assert.Equal(t, true, plan.BillingLogFields()["free_model"])

	snapshot, err := plan.AsyncTaskBillingPlan()
	require.NoError(t, err)
	assert.True(t, snapshot.FreeModel)
	hold, err = snapshot.PreConsumeQuota()
	require.NoError(t, err)
	assert.Zero(t, hold)
	restoredCharge, err := snapshot.SettlementQuota(10)
	require.NoError(t, err)
	assert.Zero(t, restoredCharge)

	corrupt := snapshot
	corrupt.ModelRatio = "1"
	assert.Error(t, corrupt.Validate(), "a durable free marker cannot suppress a non-zero price")
}
