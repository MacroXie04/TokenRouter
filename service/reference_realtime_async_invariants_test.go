package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestReferenceRealtimePlanUsesOneCapturedPolicyForHoldSettlementAndLogs(t *testing.T) {
	plan := ReferenceBillingPlan{
		modelName: "gpt-realtime", groupRatio: 0.5, modelRatio: 2, completionRatio: 3,
		preConsumedMinimum: 500,
		usageRatioPolicy: setting.UsageRatioPolicy{
			AudioRatio: 8, AudioCompletionRatio: 2,
			EnableFreeModelPreConsume: true,
		},
	}

	hold, err := plan.PreConsumeQuota(0, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 500, hold, "Realtime reserves the normal reference minimum before the handshake")

	usage := &protocolkit.Usage{
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
		PromptTokensDetails:     &protocolkit.InputTokenDetails{TextTokens: 20, AudioTokens: 80},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{TextTokens: 10, AudioTokens: 40},
	}
	settled, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{ForceAudio: true, Realtime: true})
	require.NoError(t, err)
	assert.Equal(t, 1330, settled.Quota)
	assert.Equal(t, map[string]any{
		"ws": true, "audio_input": 80, "audio_output": 40,
		"text_input": 20, "text_output": 10,
		"audio_ratio": 8.0, "audio_completion_ratio": 2.0,
	}, settled.BillingLogFields())

	// A later settings publication produces a different plan, but must not
	// alter this connection's already-captured reservation or settlement.
	reloaded := plan
	reloaded.preConsumedMinimum = 900
	reloaded.usageRatioPolicy.AudioRatio = 1
	reloaded.usageRatioPolicy.AudioCompletionRatio = 1

	originalAgain, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{ForceAudio: true, Realtime: true})
	require.NoError(t, err)
	assert.Equal(t, settled, originalAgain)

	reloadedHold, err := reloaded.PreConsumeQuota(0, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 900, reloadedHold)
	reloadedSettlement, err := reloaded.SettlementUsageQuota(usage, ReferenceUsageContext{ForceAudio: true, Realtime: true})
	require.NoError(t, err)
	assert.Equal(t, 170, reloadedSettlement.Quota)
	assert.Equal(t, 1.0, reloadedSettlement.BillingLogFields()["audio_ratio"])
}

func TestReferenceRealtimeFixedAndFreePlansKeepTheirEstablishedSemantics(t *testing.T) {
	usage := &protocolkit.Usage{
		PromptTokens: 1, TotalTokens: 1,
		PromptTokensDetails: &protocolkit.InputTokenDetails{AudioTokens: 1},
	}

	fixed := ReferenceBillingPlan{
		modelName: "fixed-realtime", groupRatio: 0.25,
		useFixedPrice: true, fixedPrice: 0.004,
		usageRatioPolicy: setting.UsageRatioPolicy{AudioRatio: 99, AudioCompletionRatio: 99},
	}
	hold, err := fixed.PreConsumeQuota(0, 0, false)
	require.NoError(t, err)
	settled, err := fixed.SettlementUsageQuota(usage, ReferenceUsageContext{ForceAudio: true, Realtime: true})
	require.NoError(t, err)
	assert.Equal(t, 500, hold)
	assert.Equal(t, hold, settled.Quota, "audio ratios do not change per-call prices")
	assert.True(t, settled.BillingLogFields()["ws"].(bool))

	free := ReferenceBillingPlan{
		modelName: "free-realtime", groupRatio: 1, modelRatio: 0,
		preConsumedMinimum: 500, freeModel: true,
		usageRatioPolicy: setting.UsageRatioPolicy{AudioRatio: 8, AudioCompletionRatio: 2},
	}
	hold, err = free.PreConsumeQuota(0, 0, false)
	require.NoError(t, err)
	settled, err = free.SettlementUsageQuota(usage, ReferenceUsageContext{ForceAudio: true, Realtime: true})
	require.NoError(t, err)
	assert.Zero(t, hold)
	assert.Zero(t, settled.Quota)
	assert.True(t, free.FreeModel())
	assert.Equal(t, true, free.BillingLogFields()["free_model"])
}

func TestReferenceAsyncSnapshotIgnoresUnreportedUsageClassesAndSurvivesReload(t *testing.T) {
	requestPlan := ReferenceBillingPlan{
		modelName: "async-video", groupRatio: 0.25, modelRatio: 2,
		usageRatioPolicy: setting.UsageRatioPolicy{
			CacheRatio: 0.1, CacheCreationRatio: 7, ImageRatio: 9,
			AudioRatio: 11, AudioCompletionRatio: 13,
		},
	}
	snapshot, err := requestPlan.AsyncTaskBillingPlan()
	require.NoError(t, err)

	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "cache")
	assert.NotContains(t, string(encoded), "image")
	assert.NotContains(t, string(encoded), "audio")

	// Async providers in this slice report only completion units. Auxiliary
	// token-class ratios therefore must not invent a second charge dimension.
	requestPlan.usageRatioPolicy = setting.UsageRatioPolicy{
		CacheRatio: 100, CacheCreationRatio: 100, ImageRatio: 100,
		AudioRatio: 100, AudioCompletionRatio: 100,
	}
	actual, err := snapshot.SettlementQuota(2)
	require.NoError(t, err)
	assert.Equal(t, 1, actual)
	assert.NotContains(t, snapshot.BillingLogFields(), "cache_ratio")
	assert.NotContains(t, snapshot.BillingLogFields(), "image_ratio")
	assert.NotContains(t, snapshot.BillingLogFields(), "audio_ratio")
}

func TestReferenceAsyncSnapshotBackwardCompatibilityAndFreeMarkerValidation(t *testing.T) {
	var legacy ReferenceAsyncTaskBillingPlan
	require.NoError(t, json.Unmarshal([]byte(`{
		"version":1,"model_name":"async-video","group_ratio":"1",
		"use_fixed_price":false,"model_ratio":"0"
	}`), &legacy))
	require.NoError(t, legacy.Validate())
	assert.False(t, legacy.FreeModel, "version-one records created before the option keep the prior reservation path")
	assert.NotContains(t, legacy.BillingLogFields(), "free_model")

	free := legacy
	free.FreeModel = true
	require.NoError(t, free.Validate())
	assert.Equal(t, true, free.BillingLogFields()["free_model"])

	corrupt := free
	corrupt.ModelRatio = "0.1"
	assert.Error(t, corrupt.Validate(), "a persisted free marker cannot suppress a non-zero effective price")
}
