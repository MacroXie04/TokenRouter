package billingexpr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, exprStr string, u Usage, req RequestInput) EvalResult {
	t.Helper()
	c, err := CompileFromCache(exprStr)
	require.NoError(t, err)
	params := BuildTokenParams(u, c.usedVars)
	res, err := c.Run(params, req)
	require.NoError(t, err)
	return res
}

func TestFlatPricingExcludesCacheRead(t *testing.T) {
	// GPT format: prompt_tokens=1000 includes 200 cache-read.
	u := Usage{PromptTokens: 1000, CacheReadTokens: 200, CompletionTokens: 500}
	res := run(t, `tier("base", p * 2.5 + c * 15 + cr * 0.25)`, u, RequestInput{})
	// p = 1000-200=800; cost = 800*2.5 + 500*15 + 200*0.25 = 9550.
	assert.InDelta(t, 9550, res.Cost, 0.001)
	assert.Equal(t, "base", res.MatchedTier)
}

func TestNoSubcategoryKeepsTokensInP(t *testing.T) {
	u := Usage{PromptTokens: 1000, CacheReadTokens: 200, CompletionTokens: 500}
	res := run(t, `p * 3 + c * 15`, u, RequestInput{})
	// cr not used -> cache stays in p; cost = 1000*3 + 500*15 = 10500.
	assert.InDelta(t, 10500, res.Cost, 0.001)
}

func TestClaudeNoSubtraction(t *testing.T) {
	// Claude format: input_tokens=1000 is text-only, cache separate.
	u := Usage{PromptTokens: 1000, CacheReadTokens: 200, CompletionTokens: 500, IsClaudeSemantic: true}
	res := run(t, `p * 3 + c * 15 + cr * 0.3`, u, RequestInput{})
	// cost = 1000*3 + 500*15 + 200*0.3 = 10560.
	assert.InDelta(t, 10560, res.Cost, 0.001)
}

func TestTierSelection(t *testing.T) {
	expr := `len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long_context", p * 6 + c * 22.5)`

	res := run(t, expr, Usage{PromptTokens: 100000, CompletionTokens: 100}, RequestInput{})
	assert.Equal(t, "standard", res.MatchedTier)
	assert.InDelta(t, 100000*3+100*15, res.Cost, 0.001)

	res2 := run(t, expr, Usage{PromptTokens: 300000, CompletionTokens: 100}, RequestInput{})
	assert.Equal(t, "long_context", res2.MatchedTier)
	assert.InDelta(t, 300000*6+100*22.5, res2.Cost, 0.001)
}

func TestVersionPrefixStripped(t *testing.T) {
	res := run(t, `v1:tier("base", p * 2 + c * 8)`, Usage{PromptTokens: 100, CompletionTokens: 50}, RequestInput{})
	assert.InDelta(t, 200+400, res.Cost, 0.001)
}

func TestParamFunction(t *testing.T) {
	req := RequestInput{Body: map[string]any{"multiplier": 2.0}}
	res := run(t, `tier("x", p * param("multiplier"))`, Usage{PromptTokens: 100}, req)
	assert.InDelta(t, 200, res.Cost, 0.001)
}

func TestHeaderFunction(t *testing.T) {
	req := RequestInput{Header: map[string]string{"X-Beta": "fast-mode"}}
	// header() returns a string; combine with has() for conditional pricing.
	res := run(t, `has(header("X-Beta"), "fast") ? p * 2 : p`, Usage{PromptTokens: 100}, req)
	assert.InDelta(t, 200, res.Cost, 0.001)
}

func TestInvalidExpression(t *testing.T) {
	_, err := CompileFromCache(`p * + +`)
	assert.Error(t, err)
}

func TestCostToQuota(t *testing.T) {
	// cost=9550 USD/1M -> 9550/1e6 * 500000 = 4775 quota.
	assert.Equal(t, 4775, CostToQuota(9550, 1.0))
	// Group ratio 2 doubles it.
	assert.Equal(t, 9550, CostToQuota(9550, 2.0))
}

func TestCostToQuotaNeverNegativeOnOverflow(t *testing.T) {
	// A huge cost saturates rather than wrapping negative.
	q, clamp := CostToQuotaChecked(1e18, 1.0)
	assert.True(t, q > 0)
	require.NotNil(t, clamp)
}
