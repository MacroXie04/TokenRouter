package expression

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestSubcategoryNamesInsideStringsDoNotChangeTokenNormalization(t *testing.T) {
	u := Usage{
		PromptTokens: 100, CompletionTokens: 40,
		ImageInputTokens: 25, AudioInputTokens: 15,
		ImageOutputTokens: 10, AudioOutputTokens: 5,
	}
	res := run(t,
		`header("img") == "enabled" || param("audio.ai") == "ao" || has("img_o", "never") ? p * 2 + c * 4 : p * 2 + c * 4`,
		u, RequestInput{})
	assert.Equal(t, float64(100*2+40*4), res.Cost)
	assert.Empty(t, UsedVars(`header("img") == "x" && param("ai") == "y" && has("ao img_o", "z") ? p : c`))
}

func TestSeparatelyPricedBucketsClampInconsistentBaseTotals(t *testing.T) {
	u := Usage{
		PromptTokens: 5, CompletionTokens: 4,
		ImageInputTokens: 7, AudioInputTokens: 11,
		ImageOutputTokens: 13, AudioOutputTokens: 17,
	}
	params := BuildTokenParams(u, UsedVars(`p + c + img * 2 + ai * 3 + img_o * 5 + ao * 7`))
	assert.Zero(t, params.P)
	assert.Zero(t, params.C)
	assert.Equal(t, float64(7), params.Img)
	assert.Equal(t, float64(11), params.Ai)
	assert.Equal(t, float64(13), params.ImgO)
	assert.Equal(t, float64(17), params.Ao)

	res, err := RunExpr(`p + c + img * 2 + ai * 3 + img_o * 5 + ao * 7`, params, RequestInput{})
	require.NoError(t, err)
	assert.Equal(t, float64(7*2+11*3+13*5+17*7), res.Cost)
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

func TestRuntimeInputsAndResultsStayBounded(t *testing.T) {
	_, err := RunExpr(`-1`, TokenParams{}, RequestInput{})
	assert.Error(t, err)
	_, err = RunExpr(`p / 0`, TokenParams{P: 1}, RequestInput{})
	assert.Error(t, err)

	oversized := strings.Repeat("x", maxBillingMatchedTierBytes+1)
	result := run(t, `tier(header("X-Tier"), p)`, Usage{PromptTokens: 7},
		RequestInput{Header: map[string]string{"X-Tier": oversized}})
	assert.Equal(t, float64(7), result.Cost)
	assert.Empty(t, result.MatchedTier)

	assert.Nil(t, lookupPath(map[string]any{"safe": 1}, strings.Repeat("x", maxBillingParamPathBytes+1)))
	assert.Nil(t, lookupPath(map[string]any{"safe": 1}, "safe."))
	assert.Equal(t, 1, lookupPath(map[string]any{"safe": map[string]any{"value": 1}}, "safe.value"))
}

func TestTimeWindowHelpersUseRequestedTimezoneZone(t *testing.T) {
	previousClock := billingClock
	billingClock = func() time.Time {
		return time.Date(2026, time.September, 4, 15, 37, 0, 0, time.UTC)
	}
	t.Cleanup(func() { billingClock = previousClock })

	result := run(t,
		`hour("UTC") * 1000000 + minute("UTC") * 10000 + weekday("UTC") * 100 + month("UTC") + day("UTC")`,
		Usage{}, RequestInput{})
	// 15:37 UTC on 2026-09-04 is Friday (weekday 5), month 9, day 4.
	assert.Equal(t, float64(15_370_513), result.Cost)
}

func TestInvalidExpression(t *testing.T) {
	_, err := CompileFromCache(`p * + +`)
	assert.Error(t, err)
}

func TestExpressionCompilerRejectsOversizedOrPathologicalSources(t *testing.T) {
	_, err := CompileFromCache(strings.Repeat("p", maxBillingExpressionBytes+1))
	assert.Error(t, err)
	_, err = CompileFromCache(string([]byte{'p', 0xff}))
	assert.Error(t, err)

	deep := strings.Repeat("(", maxBillingExpressionNesting+1) + "p" +
		strings.Repeat(")", maxBillingExpressionNesting+1)
	_, err = CompileFromCache(deep)
	assert.Error(t, err)

	wide := strings.Repeat("p+", maxBillingExpressionLexicalUnits/2+1) + "p"
	_, err = CompileFromCache(wide)
	assert.Error(t, err)
}

func TestCompiledExpressionCacheIsBoundedAndConcurrencySafe(t *testing.T) {
	previous := compileCache
	compileCache = newBoundedCompileCache()
	t.Cleanup(func() { compileCache = previous })

	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < maxCompiledExpressionCacheItems+32; index++ {
				_, err := CompileFromCache(fmt.Sprintf("p + %d + %d", worker, index))
				assert.NoError(t, err)
			}
		}()
	}
	wait.Wait()
	assert.LessOrEqual(t, compileCache.size(), maxCompiledExpressionCacheItems)
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
