package service

import (
	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/pkg/billingexpr"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Billing mode values.
const (
	BillingModeDefault    = ""            // flat per-token pricing
	BillingModeTieredExpr = "tiered_expr" // expression-based billing
)

// GetBillingMode returns the configured billing mode for a model.
func GetBillingMode(modelName string) string {
	if mode := setting.GetOptionJSONField("ModelBillingMode", modelName); mode != "" {
		return mode
	}
	return BillingModeDefault
}

// GetBillingExpr returns the billing expression for a model (empty if unset).
func GetBillingExpr(modelName string) string {
	return setting.GetOptionJSONField("ModelBillingExpr", modelName)
}

// ComputeBillingQuota computes the settlement quota for a model, dispatching to
// the tiered expression engine when the model is configured for it, and falling
// back to flat per-token pricing otherwise.
func ComputeBillingQuota(modelName, group string, isClaude bool, u *protocolkit.Usage, req billingexpr.RequestInput) (int, *common.QuotaClamp, error) {
	if GetBillingMode(modelName) == BillingModeTieredExpr {
		if exprStr := GetBillingExpr(modelName); exprStr != "" {
			return computeTieredQuota(modelName, group, isClaude, u, req, exprStr)
		}
	}
	prompt, completion := 0, 0
	if u != nil {
		prompt, completion = u.PromptTokens, u.CompletionTokens
	}
	q, clamp := ComputeQuotaChecked(modelName, group, prompt, completion)
	return q, clamp, nil
}

func computeTieredQuota(modelName, group string, isClaude bool, u *protocolkit.Usage, req billingexpr.RequestInput, exprStr string) (int, *common.QuotaClamp, error) {
	bu := usageToBilling(u, isClaude)
	used := billingexpr.UsedVars(exprStr)
	params := billingexpr.BuildTokenParams(bu, used)
	res, err := billingexpr.RunExpr(exprStr, params, req)
	if err != nil {
		common.SysError("tiered billing eval failed for model " + modelName + ": " + err.Error())
		// Fall back to flat pricing rather than failing the request.
		q, clamp := ComputeQuotaChecked(modelName, group, bu.PromptTokens, bu.CompletionTokens)
		return q, clamp, nil
	}
	q, clamp := billingexpr.CostToQuotaChecked(res.Cost, GroupRatio(group))
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
	bu.CacheCreationTokens = u.PromptCacheMissTokens
	if u.PromptCacheCreationTokens > 0 {
		bu.CacheCreationTokens = u.PromptCacheCreationTokens
	}
	if u.PromptTokensDetails != nil {
		if u.PromptTokensDetails.CachedTokens > 0 {
			bu.CacheReadTokens = u.PromptTokensDetails.CachedTokens
		}
		if u.PromptTokensDetails.CachedCreationTokens > 0 {
			bu.CacheCreationTokens = u.PromptTokensDetails.CachedCreationTokens
		}
		bu.ImageInputTokens = u.PromptTokensDetails.ImageTokens
		bu.AudioInputTokens = u.PromptTokensDetails.AudioTokens
	}
	if u.CompletionTokensDetails != nil {
		bu.AudioOutputTokens = u.CompletionTokensDetails.AudioTokens
		bu.ImageOutputTokens = u.CompletionTokensDetails.ImageTokens
	}
	return bu
}
