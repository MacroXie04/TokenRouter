package billing

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingexpr "github.com/tokenrouter/tokenrouter/internal/billing/expression"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"gorm.io/gorm"
	"testing"
)

func initBillingDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init()) // reset option cache
	SetModelPriceRegistry(map[string]ModelPrice{})
	SetGroupRatios(map[string]float64{})
	SetGroupGroupRatios(map[string]map[string]float64{})
}

func TestComputeBillingQuotaTiered(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"m":"tiered_expr"}`,
		"ModelBillingExpr": `{"m":"tier(\"base\", p * 2 + c * 8)"}`,
	}))

	u := &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 50}
	q, _, err := ComputeBillingQuota("m", "default", false, u, billingexpr.RequestInput{})
	require.NoError(t, err)
	// cost = 100*2 + 50*8 = 600 USD/1M -> 600/1e6 * 500000 = 300 quota.
	assert.Equal(t, 300, q)
}

func TestComputeBillingQuotaTieredWithCache(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"m":"tiered_expr"}`,
		"ModelBillingExpr": `{"m":"p * 3 + c * 15 + cr * 0.25"}`,
	}))

	// GPT-format: prompt=1000 includes 200 cache-read.
	u := &protocolkit.Usage{
		PromptTokens:         1000,
		CompletionTokens:     500,
		PromptCacheHitTokens: 200,
	}
	q, _, err := ComputeBillingQuota("m", "default", false, u, billingexpr.RequestInput{})
	require.NoError(t, err)
	// p=800 (cache excluded); cost = 800*3 + 500*15 + 200*0.25 = 9950 -> 4975.
	assert.Equal(t, 4975, q)
}

func TestComputeBillingQuotaFlatFallback(t *testing.T) {
	initBillingDB(t)
	u := &protocolkit.Usage{PromptTokens: 1000, CompletionTokens: 0}
	q, _, err := ComputeBillingQuota("unconfigured", "default", false, u, billingexpr.RequestInput{})
	require.NoError(t, err)
	// Flat $1/1M prompt: 1000 tokens -> 500 quota.
	assert.Equal(t, 500, q)
}

func TestComputeBillingQuotaRejectsInvalidTieredExpressionAtomically(t *testing.T) {
	initBillingDB(t)
	SetModelPriceRegistry(map[string]ModelPrice{"broken-tier": {Prompt: 2, Completion: 8}})
	err := setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"broken-tier":"tiered_expr"}`,
		"ModelBillingExpr": `{"broken-tier":"p * + +"}`,
	})
	require.Error(t, err)
	assert.Empty(t, setting.GetOption("ModelBillingMode"))
	assert.Empty(t, setting.GetOption("ModelBillingExpr"))

	usage := &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 50}
	quota, clamp, err := ComputeBillingQuota("broken-tier", "default", false, usage, billingexpr.RequestInput{})
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.Equal(t, ComputeQuota("broken-tier", "default", 100, 50), quota)
	assert.Equal(t, 300, quota)
}

func TestUsageToBilling(t *testing.T) {
	u := &protocolkit.Usage{
		PromptTokens:                1000,
		CompletionTokens:            500,
		PromptCacheHitTokens:        100,
		PromptCacheMissTokens:       50,
		PromptCacheCreation5mTokens: 20,
		PromptCacheCreation1hTokens: 10,
		PromptTokensDetails:         &protocolkit.InputTokenDetails{ImageTokens: 12, AudioTokens: 13},
		CompletionTokensDetails:     &protocolkit.OutputTokenDetails{ImageTokens: 14, AudioTokens: 15},
	}
	bu := usageToBilling(u, false)
	assert.Equal(t, 1000, bu.PromptTokens)
	assert.Equal(t, 500, bu.CompletionTokens)
	assert.Equal(t, 100, bu.CacheReadTokens)
	assert.Equal(t, 40, bu.CacheCreationTokens)
	assert.Equal(t, 10, bu.CacheCreation1hTokens)
	assert.Equal(t, 12, bu.ImageInputTokens)
	assert.Equal(t, 13, bu.AudioInputTokens)
	assert.Equal(t, 14, bu.ImageOutputTokens)
	assert.Equal(t, 15, bu.AudioOutputTokens)
	assert.False(t, bu.IsClaudeSemantic)

	buClaude := usageToBilling(u, true)
	assert.True(t, buClaude.IsClaudeSemantic)
	assert.Equal(t, 850, buClaude.PromptTokens, "Claude p excludes cache categories from OpenAI-compatible prompt totals")
}

func TestUsageToBillingClampsInconsistentClaudeCacheTotalsWithoutOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	bu := usageToBilling(&protocolkit.Usage{
		PromptTokens: maxInt,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			CachedTokens:          maxInt,
			CacheCreation5mTokens: maxInt,
			CacheCreation1hTokens: maxInt,
		},
	}, true)
	assert.Zero(t, bu.PromptTokens)
	assert.Equal(t, maxInt, bu.CacheReadTokens)
	assert.Equal(t, maxInt, bu.CacheCreationTokens)
	assert.Equal(t, maxInt, bu.CacheCreation1hTokens)
}

func TestComputeBillingQuotaTieredClaudeCacheTTLBreakdown(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"claude-tiered":"tiered_expr"}`,
		"ModelBillingExpr": `{"claude-tiered":"p * 2 + c * 8 + cr * 0.2 + cc * 2.5 + cc1h * 4"}`,
	}))

	u := protocolkit.ClaudeUsageToOpenAIUsage(&protocolkit.ClaudeUsage{
		InputTokens: 100, CacheReadInputTokens: 20, CacheCreationInputTokens: 30,
		CacheCreation: &protocolkit.ClaudeCacheCreation{
			Ephemeral5mInputTokens: 10, Ephemeral1hInputTokens: 20,
		},
		OutputTokens: 5,
	})
	quota, _, err := ComputeBillingQuota("claude-tiered", "default", true, u, billingexpr.RequestInput{})
	require.NoError(t, err)
	// Cost = 100*2 + 5*8 + 20*0.2 + 10*2.5 + 20*4 = 349;
	// one quota is $2 per million-token price-unit at QuotaPerUnit=500000.
	assert.Equal(t, 175, quota)
}
