package billingexpr

import (
	"github.com/tokenrouter/tokenrouter/common"
)

// CostToQuota converts a computed cost (USD per 1M tokens) into integer quota:
//
//	quota = cost / 1_000_000 * QuotaPerUnit * groupRatio
//
// using the shared half-away-from-zero rounding with int32 saturation. NaN,
// +Inf, and overflow all clamp safely and never produce a negative charge.
func CostToQuota(cost float64, groupRatio float64) int {
	return common.QuotaRound(cost / 1e6 * float64(common.QuotaPerUnit) * groupRatio)
}

// CostToQuotaChecked is CostToQuota with saturation reporting for audit.
func CostToQuotaChecked(cost float64, groupRatio float64) (int, *common.QuotaClamp) {
	return common.QuotaRoundChecked(cost / 1e6 * float64(common.QuotaPerUnit) * groupRatio)
}
