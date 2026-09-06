package billing

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"testing"
)

func TestReferenceUsageSettlementConsumesNativeClaudeCacheClasses(t *testing.T) {
	plan := referenceUsageTestPlan()
	plan.groupRatio = 1
	plan.modelRatio = 1
	usage := protocolkit.ClaudeUsageToOpenAIUsage(&protocolkit.ClaudeUsage{
		InputTokens: 700, OutputTokens: 100, CacheReadInputTokens: 100,
		CacheCreationInputTokens: 200,
		CacheCreation: &protocolkit.ClaudeCacheCreation{
			Ephemeral5mInputTokens: 150,
			Ephemeral1hInputTokens: 50,
		},
	})

	settlement, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{IsClaude: true})
	require.NoError(t, err)
	assert.Equal(t, 1298, settlement.Quota)
	assert.Equal(t, map[string]any{
		"cache_tokens": 100, "cache_ratio": 0.1,
		"claude": true, "usage_semantic": "anthropic",
		"cache_creation_tokens": 200, "cache_creation_ratio": 1.25,
		"cache_write_tokens":       200,
		"cache_creation_tokens_5m": 150, "cache_creation_ratio_5m": 1.25,
		"cache_creation_tokens_1h": 50, "cache_creation_ratio_1h": 2.0,
	}, settlement.BillingLogFields())
}

func TestReferenceUsageSettlementConsumesNativeGeminiModalities(t *testing.T) {
	plan := referenceUsageTestPlan()
	plan.groupRatio = 1
	plan.modelRatio = 1
	usage := protocolkit.GeminiUsageToOpenAIUsage(&protocolkit.GeminiUsageMetadata{
		PromptTokenCount:        1_000,
		CandidatesTokenCount:    100,
		TotalTokenCount:         1_100,
		CachedContentTokenCount: 100,
		PromptTokensDetails: []protocolkit.GeminiPromptTokensDetails{
			{Modality: "TEXT", TokenCount: 800},
			{Modality: "IMAGE", TokenCount: 100},
			{Modality: "AUDIO", TokenCount: 100},
		},
		CandidatesTokensDetails: []protocolkit.GeminiCandidatesTokensDetails{
			{Modality: "TEXT", TokenCount: 60},
			{Modality: "AUDIO", TokenCount: 40},
		},
	})

	audio, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 2420, audio.Quota)
	assert.Equal(t, true, audio.BillingLogFields()["audio"])
	assert.Equal(t, 100, audio.BillingLogFields()["audio_input"])
	assert.Equal(t, 40, audio.BillingLogFields()["audio_output"])

	plan.usageRatioPolicy.AudioRatioConfigured = false
	plan.usageRatioPolicy.AudioCompletionConfigured = false
	text, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 1310, text.Quota)
	assert.Equal(t, true, text.BillingLogFields()["image"])
	assert.Equal(t, 100, text.BillingLogFields()["image_output"])
	assert.Equal(t, 100, text.BillingLogFields()["cache_tokens"])
}

func TestReferenceUsageSettlementTreatsStreamingUsageAsAuthoritativeOnce(t *testing.T) {
	plan := referenceUsageTestPlan()
	plan.groupRatio = 1
	plan.modelRatio = 1
	usage := &protocolkit.Usage{
		PromptTokens: 100, CompletionTokens: 25, TotalTokens: 125,
		PromptCacheHitTokens: 20,
		PromptTokensDetails:  &protocolkit.InputTokenDetails{CachedTokens: 20},
	}

	first, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	second, err := plan.SettlementUsageQuota(usage, ReferenceUsageContext{})
	require.NoError(t, err)
	assert.Equal(t, 157, first.Quota)
	assert.Equal(t, first, second, "stream transport does not change final usage arithmetic")
}
