package billing

import (
	billingexpr "github.com/tokenrouter/tokenrouter/internal/billing/expression"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

// Billing mode values.
const (
	BillingModeDefault    = ""            // flat per-token pricing
	BillingModeRatio      = "ratio"       // explicit form of ordinary ratio pricing
	BillingModeTieredExpr = "tiered_expr" // expression-based billing
)

// GetBillingMode returns the configured billing mode for a model.
func GetBillingMode(modelName string) string {
	if mode := setting.GetOptionJSONField(setting.ModelBillingModeOption, modelName); mode != "" {
		return mode
	}
	return BillingModeDefault
}

// GetBillingExpr returns the billing expression for a model (empty if unset).
func GetBillingExpr(modelName string) string {
	return setting.GetOptionJSONField(setting.ModelBillingExprOption, modelName)
}

// ComputeBillingQuota computes the settlement quota for a model, dispatching to
// the tiered expression engine when the model is configured for it, and falling
// back to flat per-token pricing otherwise.
func ComputeBillingQuota(modelName, group string, isClaude bool, u *protocolkit.Usage, req billingexpr.RequestInput) (int, *quotamath.QuotaClamp, error) {
	return ComputeBillingQuotaForUser(modelName, "", group, isClaude, u, req)
}

// ComputeBillingQuotaForUser applies a user-group-specific override to both
// flat and expression-based billing while preserving the legacy wrapper for
// callers that have no authenticated user-group context.
func ComputeBillingQuotaForUser(modelName, userGroup, usingGroup string, isClaude bool, u *protocolkit.Usage, req billingexpr.RequestInput) (int, *quotamath.QuotaClamp, error) {
	if GetBillingMode(modelName) == BillingModeTieredExpr {
		if exprStr := GetBillingExpr(modelName); exprStr != "" {
			return computeTieredQuotaForUser(modelName, userGroup, usingGroup, isClaude, u, req, exprStr)
		}
	}
	prompt, completion := 0, 0
	if u != nil {
		prompt, completion = u.PromptTokens, u.CompletionTokens
	}
	q, clamp := ComputeQuotaForUserChecked(modelName, userGroup, usingGroup, prompt, completion)
	return q, clamp, nil
}

func computeTieredQuota(modelName, group string, isClaude bool, u *protocolkit.Usage, req billingexpr.RequestInput, exprStr string) (int, *quotamath.QuotaClamp, error) {
	return computeTieredQuotaForUser(modelName, "", group, isClaude, u, req, exprStr)
}

func computeTieredQuotaForUser(modelName, userGroup, usingGroup string, isClaude bool, u *protocolkit.Usage, req billingexpr.RequestInput, exprStr string) (int, *quotamath.QuotaClamp, error) {
	bu := usageToBilling(u, isClaude)
	used := billingexpr.UsedVars(exprStr)
	params := billingexpr.BuildTokenParams(bu, used)
	res, err := billingexpr.RunExpr(exprStr, params, req)
	if err != nil {
		logging.SysError("tiered billing eval failed for model " + modelName + ": " + err.Error())
		// Fall back to flat pricing rather than failing the request.
		q, clamp := ComputeQuotaForUserChecked(modelName, userGroup, usingGroup, bu.PromptTokens, bu.CompletionTokens)
		return q, clamp, nil
	}
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	q, clamp := billingexpr.CostToQuotaChecked(res.Cost, ratio)
	return q, clamp, nil
}

// usageToBilling converts protocolkit usage into the billingexpr usage shape.
func usageToBilling(u *protocolkit.Usage, isClaude bool) billingexpr.Usage {
	bu := billingexpr.Usage{IsClaudeSemantic: isClaude}
	if u == nil {
		return bu
	}
	bu.PromptTokens = u.PromptTokens
	bu.CompletionTokens = u.CompletionTokens
	bu.CacheReadTokens = u.PromptCacheHitTokens
	cacheCreationTotal := u.PromptCacheMissTokens
	if u.PromptCacheCreationTokens > 0 {
		cacheCreationTotal = u.PromptCacheCreationTokens
	}
	cacheCreation5m := u.PromptCacheCreation5mTokens
	cacheCreation1h := u.PromptCacheCreation1hTokens
	if u.PromptTokensDetails != nil {
		if u.PromptTokensDetails.CachedTokens > 0 {
			bu.CacheReadTokens = u.PromptTokensDetails.CachedTokens
		}
		if u.PromptTokensDetails.CachedCreationTokens > 0 {
			cacheCreationTotal = u.PromptTokensDetails.CachedCreationTokens
		}
		if u.PromptTokensDetails.CacheCreation5mTokens > 0 {
			cacheCreation5m = u.PromptTokensDetails.CacheCreation5mTokens
		}
		if u.PromptTokensDetails.CacheCreation1hTokens > 0 {
			cacheCreation1h = u.PromptTokensDetails.CacheCreation1hTokens
		}
		bu.ImageInputTokens = u.PromptTokensDetails.ImageTokens
		bu.AudioInputTokens = u.PromptTokensDetails.AudioTokens
	}
	// Avoid adding untrusted machine-width counters. Any legacy total not
	// already explained by the TTL buckets belongs to the default 5-minute
	// class, and the resulting bucket cannot exceed cacheCreationTotal.
	if cacheCreationTotal > 0 && cacheCreation5m >= 0 && cacheCreation1h >= 0 &&
		cacheCreation5m <= cacheCreationTotal && cacheCreation1h < cacheCreationTotal-cacheCreation5m {
		// Anthropic classifies cache writes without an explicit TTL as the
		// default five-minute class.
		cacheCreation5m += cacheCreationTotal - cacheCreation5m - cacheCreation1h
	}
	bu.CacheCreationTokens = cacheCreation5m
	bu.CacheCreation1hTokens = cacheCreation1h
	if isClaude {
		// ClaudeUsageToOpenAIUsage exposes OpenAI-compatible prompt_tokens (text
		// plus cache). The expression engine's Claude semantic expects p to be
		// text-only, with cache categories supplied separately.
		bu.PromptTokens = subtractUsageBuckets(bu.PromptTokens,
			bu.CacheReadTokens, cacheCreation5m, cacheCreation1h)
	}
	if u.CompletionTokensDetails != nil {
		bu.AudioOutputTokens = u.CompletionTokensDetails.AudioTokens
		bu.ImageOutputTokens = u.CompletionTokensDetails.ImageTokens
	}
	return bu
}

// subtractUsageBuckets removes non-negative subcategory counts without ever
// summing them first. It therefore cannot overflow, and inconsistent provider
// totals clamp the base bucket at zero instead of turning negative or restoring
// the unreduced aggregate.
func subtractUsageBuckets(total int, buckets ...int) int {
	if total <= 0 {
		return 0
	}
	remaining := total
	for _, bucket := range buckets {
		if bucket <= 0 {
			continue
		}
		if bucket >= remaining {
			return 0
		}
		remaining -= bucket
	}
	return remaining
}
