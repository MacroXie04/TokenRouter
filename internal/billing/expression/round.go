package expression

import (
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
)

// CostToQuota converts a computed cost (USD per 1M tokens) into integer quota:
//
//	quota = cost / 1_000_000 * QuotaPerUnit * groupRatio
//
// using the shared half-away-from-zero rounding with int32 saturation. NaN,
// +Inf, and overflow all clamp safely and never produce a negative charge.
func CostToQuota(cost float64, groupRatio float64) int {
	return quotamath.QuotaRound(cost / 1e6 * float64(quotamath.QuotaPerUnit) * groupRatio)
}

// CostToQuotaChecked is CostToQuota with saturation reporting for audit.
func CostToQuotaChecked(cost float64, groupRatio float64) (int, *quotamath.QuotaClamp) {
	return quotamath.QuotaRoundChecked(cost / 1e6 * float64(quotamath.QuotaPerUnit) * groupRatio)
}
