package common

import (
	"fmt"
	"math"

	"github.com/shopspring/decimal"
)

// Quota accounting columns (user/token/log) are 32-bit integers in the
// database. All float->quota and decimal->quota conversions are centralized
// here so that arithmetic overflow, NaN, and +Inf can never produce a negative
// charge or silently wrap. Bounds are int32 (math.MaxInt32 / math.MinInt32).

const (
	// MaxQuota is the largest representable quota value.
	MaxQuota = int64(math.MaxInt32)
	// MinQuota is the smallest representable quota value.
	MinQuota = int64(math.MinInt32)
)

// QuotaWithinBounds reports whether a persisted or calculated quota value is
// inside the non-negative accounting domain.
func QuotaWithinBounds(value int) bool {
	return value >= 0 && int64(value) <= MaxQuota
}

// AddQuotaWithinBounds adds two non-negative quota values without allowing a
// machine-int overflow or a result outside the persisted accounting domain.
func AddQuotaWithinBounds(current, delta int) (int, bool) {
	if !QuotaWithinBounds(current) || !QuotaWithinBounds(delta) {
		return 0, false
	}
	if int64(current) > MaxQuota-int64(delta) {
		return 0, false
	}
	return current + delta, true
}

// QuotaClamp records that a conversion saturated, for audit purposes.
type QuotaClamp struct {
	// Reason is a short machine-readable cause, e.g. "overflow" or "nan".
	Reason string `json:"reason"`
	// Value is the raw input that triggered the clamp (may be lossy for floats).
	Value string `json:"value,omitempty"`
}

// Error lets a clamp double as the fail-fast error returned by strict
// conversions, so purchase-style paths can refuse to bill a saturated value.
func (c *QuotaClamp) Error() string {
	if c == nil {
		return ""
	}
	if c.Value != "" {
		return fmt.Sprintf("quota conversion clamped (%s): %s", c.Reason, c.Value)
	}
	return fmt.Sprintf("quota conversion clamped (%s)", c.Reason)
}

// clampInt32 saturates a signed 64-bit value into the int32 range.
func clampInt32(v int64) int64 {
	if v > MaxQuota {
		return MaxQuota
	}
	if v < MinQuota {
		return MinQuota
	}
	return v
}

// QuotaFromFloat converts a floating-point product into a quota by truncation
// (toward zero), saturating at int32 bounds. This matches provider billing
// semantics where fractional quota is truncated, never rounded up.
func QuotaFromFloat(f float64) int {
	return int(quotaFromFloat(f, nil))
}

// QuotaFromFloatChecked is like QuotaFromFloat but reports saturation.
func QuotaFromFloatChecked(f float64) (int, *QuotaClamp) {
	var clamp *QuotaClamp
	q := quotaFromFloat(f, &clamp)
	return int(q), clamp
}

func setQuotaClamp(outClamp **QuotaClamp, clamp *QuotaClamp) {
	if outClamp != nil {
		*outClamp = clamp
	}
}

func quotaFromFloat(f float64, outClamp **QuotaClamp) int64 {
	if math.IsNaN(f) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "nan"})
		SysError("quota: NaN input in QuotaFromFloat")
		return 0
	}
	if math.IsInf(f, 1) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "inf"})
		SysError("quota: +Inf input in QuotaFromFloat")
		return MaxQuota
	}
	if math.IsInf(f, -1) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "inf"})
		SysError("quota: -Inf input in QuotaFromFloat")
		return MinQuota
	}
	// Truncate toward zero, then saturate.
	t := int64(f)
	clamped := clampInt32(t)
	if clamped != t {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "overflow"})
		SysError("quota: overflow clamped in QuotaFromFloat")
	}
	return clamped
}

// QuotaRound converts a floating-point product into a quota using half-away-
// from-zero rounding, saturating at int32 bounds.
func QuotaRound(f float64) int {
	return int(quotaRound(f, nil))
}

// QuotaRoundChecked is like QuotaRound but reports saturation.
func QuotaRoundChecked(f float64) (int, *QuotaClamp) {
	var clamp *QuotaClamp
	q := quotaRound(f, &clamp)
	return int(q), clamp
}

func quotaRound(f float64, outClamp **QuotaClamp) int64 {
	if math.IsNaN(f) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "nan"})
		SysError("quota: NaN input in QuotaRound")
		return 0
	}
	if math.IsInf(f, 1) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "inf"})
		SysError("quota: +Inf input in QuotaRound")
		return MaxQuota
	}
	if math.IsInf(f, -1) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "inf"})
		SysError("quota: -Inf input in QuotaRound")
		return MinQuota
	}
	// math.Round rounds half away from zero.
	r := int64(math.Round(f))
	clamped := clampInt32(r)
	if clamped != r {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "overflow"})
		SysError("quota: overflow clamped in QuotaRound")
	}
	return clamped
}

// QuotaFromDecimal converts a shopspring decimal product into a quota by
// truncation toward zero (decimal.IntPart), saturating at int32 bounds.
func QuotaFromDecimal(d decimal.Decimal) int {
	return int(quotaFromDecimal(d, nil))
}

// QuotaFromDecimalChecked is like QuotaFromDecimal but reports saturation.
func QuotaFromDecimalChecked(d decimal.Decimal) (int, *QuotaClamp) {
	var clamp *QuotaClamp
	q := quotaFromDecimal(d, &clamp)
	return int(q), clamp
}

// QuotaFromDecimalStrict converts an in-range decimal quota and rejects a
// value that would otherwise saturate at the int32 boundary. Charging paths
// (subscription purchase, top-up style conversions) must use this instead of
// the clamping variants so an oversized amount fails instead of billing the
// clamp.
func QuotaFromDecimalStrict(d decimal.Decimal) (int, error) {
	q, clamp := QuotaFromDecimalChecked(d)
	if clamp != nil {
		return 0, clamp
	}
	return q, nil
}

func quotaFromDecimal(d decimal.Decimal, outClamp **QuotaClamp) int64 {
	maxExclusive := decimal.NewFromInt(MaxQuota).Add(decimal.NewFromInt(1))
	minInclusive := decimal.NewFromInt(MinQuota).Sub(decimal.NewFromInt(1))
	if d.GreaterThanOrEqual(maxExclusive) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "overflow", Value: d.String()})
		SysError("quota: decimal overflow clamped in QuotaFromDecimal")
		return MaxQuota
	}
	if d.LessThanOrEqual(minInclusive) {
		setQuotaClamp(outClamp, &QuotaClamp{Reason: "overflow", Value: d.String()})
		SysError("quota: decimal underflow clamped in QuotaFromDecimal")
		return MinQuota
	}
	return d.IntPart()
}

// QuotaToFloat converts quota to a float, safe for display.
func QuotaToFloat(q int) float64 {
	return float64(q)
}
