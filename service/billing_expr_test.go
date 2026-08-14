package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/pkg/billingexpr"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
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
}

func TestComputeBillingQuotaTiered(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, setting.UpdateOption("ModelBillingMode", `{"m":"tiered_expr"}`))
	require.NoError(t, setting.UpdateOption("ModelBillingExpr", `{"m":"tier(\"base\", p * 2 + c * 8)"}`))

	u := &protocolkit.Usage{PromptTokens: 100, CompletionTokens: 50}
	q, _, err := ComputeBillingQuota("m", "default", false, u, billingexpr.RequestInput{})
	require.NoError(t, err)
	// cost = 100*2 + 50*8 = 600 USD/1M -> 600/1e6 * 500000 = 300 quota.
	assert.Equal(t, 300, q)
}

func TestComputeBillingQuotaTieredWithCache(t *testing.T) {
	initBillingDB(t)
	require.NoError(t, setting.UpdateOption("ModelBillingMode", `{"m":"tiered_expr"}`))
	require.NoError(t, setting.UpdateOption("ModelBillingExpr", `{"m":"p * 3 + c * 15 + cr * 0.25"}`))

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

func TestUsageToBilling(t *testing.T) {
	u := &protocolkit.Usage{
		PromptTokens:         1000,
		CompletionTokens:     500,
		PromptCacheHitTokens: 100,
		PromptCacheMissTokens: 50,
	}
	bu := usageToBilling(u, false)
	assert.Equal(t, 1000, bu.PromptTokens)
	assert.Equal(t, 500, bu.CompletionTokens)
	assert.Equal(t, 100, bu.CacheReadTokens)
	assert.Equal(t, 50, bu.CacheCreationTokens)
	assert.False(t, bu.IsClaudeSemantic)

	buClaude := usageToBilling(u, true)
	assert.True(t, buClaude.IsClaudeSemantic)
}
