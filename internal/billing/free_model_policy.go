package billing

import (
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
)

// ShouldSkipOrdinaryFreeModelPreConsume resolves the global free-model hold
// policy for TokenRouter's non-reference ordinary pricing modes. A tiered
// expression is free only when the effective group ratio is zero; a zero
// estimate produced by the expression or by rounding is not sufficient.
func ShouldSkipOrdinaryFreeModelPreConsume(modelName, userGroup, usingGroup string) bool {
	policy := setting.GetUsageRatioPolicy(modelName)
	if policy.EnableFreeModelPreConsume {
		return false
	}
	groupRatio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	if groupRatio == 0 {
		return true
	}
	if GetBillingMode(modelName) == BillingModeTieredExpr {
		return false
	}
	promptPrice, completionPrice := GetModelPrices(modelName)
	return promptPrice == 0 && completionPrice == 0
}

// ShouldSkipPerCallFreeModelPreConsume resolves the same global policy for
// legacy per-call task pricing (for example OpenAI/Sora video). Only the
// configured prompt price is used by that pricing contract.
func ShouldSkipPerCallFreeModelPreConsume(modelName, userGroup, usingGroup string) bool {
	policy := setting.GetUsageRatioPolicy(modelName)
	if policy.EnableFreeModelPreConsume {
		return false
	}
	groupRatio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	if groupRatio == 0 {
		return true
	}
	price, _ := GetModelPrices(modelName)
	return price == 0
}

// ShouldSkipExplicitFreeModelPreConsume covers adapters whose protocol makes a
// specific operation free independently of the model's configured base price.
// The global switch alone decides whether that zero-price operation bypasses
// the otherwise deliberate minimum funding hold.
func ShouldSkipExplicitFreeModelPreConsume(modelName string) bool {
	return !setting.GetUsageRatioPolicy(modelName).EnableFreeModelPreConsume
}
