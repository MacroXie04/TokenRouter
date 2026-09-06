package billing

import (
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"math"
)

// ComputeQuota computes quota for a model+group given prompt/completion tokens.
// quota = priceUSD / 1e6 * QuotaPerUnit * groupRatio, with saturation audit.
func ComputeQuota(modelName, group string, promptTokens, completionTokens int) int {
	return ComputeQuotaForUser(modelName, "", group, promptTokens, completionTokens)
}

// ComputeQuotaForUser applies any configured user-group override before
// converting the model's USD price to quota.
func ComputeQuotaForUser(modelName, userGroup, usingGroup string, promptTokens, completionTokens int) int {
	promptUSD, completionUSD := GetModelPrices(modelName)
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	raw := (promptUSD*float64(promptTokens) + completionUSD*float64(completionTokens)) / 1e6 * quotamath.QuotaPerUnit * ratio
	return quotamath.QuotaFromFloat(raw)
}

// ComputeQuotaChecked is ComputeQuota with saturation reporting.
func ComputeQuotaChecked(modelName, group string, promptTokens, completionTokens int) (int, *quotamath.QuotaClamp) {
	return ComputeQuotaForUserChecked(modelName, "", group, promptTokens, completionTokens)
}

// ComputeQuotaForUserChecked is ComputeQuotaForUser with saturation reporting.
func ComputeQuotaForUserChecked(modelName, userGroup, usingGroup string, promptTokens, completionTokens int) (int, *quotamath.QuotaClamp) {
	promptUSD, completionUSD := GetModelPrices(modelName)
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	raw := (promptUSD*float64(promptTokens) + completionUSD*float64(completionTokens)) / 1e6 * quotamath.QuotaPerUnit * ratio
	return quotamath.QuotaFromFloatChecked(raw)
}

// ComputePerCallQuota prices asynchronous provider operations whose configured
// prompt price is USD per call rather than USD per million tokens. The unit
// multiplier must be explicit so duration-based task billing cannot silently
// undercharge, and saturation fails closed instead of billing a clamp.
func ComputePerCallQuota(modelName, group string, units int) (int, error) {
	return ComputePerCallQuotaForUser(modelName, "", group, units)
}

// ComputePerCallQuotaForUser applies the same user-group override semantics to
// asynchronous per-call work as ordinary token-priced relays.
func ComputePerCallQuotaForUser(modelName, userGroup, usingGroup string, units int) (int, error) {
	if units < 1 {
		return 0, errors.New("per-call billing units must be positive")
	}
	return ComputePerCallQuotaMultiplierForUser(
		modelName, userGroup, usingGroup, decimal.NewFromInt(int64(units)),
	)
}

// ComputePerCallQuotaMultiplierForUser prices task work with an exact decimal
// multiplier (for example seconds × a resolution factor). Decimal input keeps
// a provider's fractional ratio deterministic and strict quota conversion
// fails closed on overflow.
func ComputePerCallQuotaMultiplierForUser(
	modelName, userGroup, usingGroup string,
	multiplier decimal.Decimal,
) (int, error) {
	if multiplier.LessThanOrEqual(decimal.Zero) {
		return 0, errors.New("per-call billing multiplier must be positive")
	}
	priceUSD, _ := GetModelPrices(modelName)
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	if math.IsNaN(priceUSD) || math.IsInf(priceUSD, 0) || priceUSD < 0 {
		return 0, fmt.Errorf("invalid per-call price for model %s", modelName)
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return 0, fmt.Errorf("invalid group ratio for %s", usingGroup)
	}
	raw := decimal.NewFromFloat(priceUSD).
		Mul(decimal.NewFromInt(quotamath.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(ratio)).
		Mul(multiplier)
	return quotamath.QuotaFromDecimalStrict(raw)
}
