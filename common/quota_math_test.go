package common

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuotaFromFloat(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want int
	}{
		{"zero", 0, 0},
		{"positive truncate", 6.5, 6},
		{"negative truncate", -6.5, -6},
		{"large", 123456.0, 123456},
		{"small fraction", 0.9, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := QuotaFromFloat(c.in)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestQuotaFromFloatSaturation(t *testing.T) {
	// Overflow must clamp to MaxQuota, not wrap negative.
	q, clamp := QuotaFromFloatChecked(float64(math.MaxInt32) + 1000)
	assert.Equal(t, int(MaxQuota), q)
	require.NotNil(t, clamp)
	assert.Equal(t, "overflow", clamp.Reason)

	// Underflow clamps to MinQuota.
	q2, clamp2 := QuotaFromFloatChecked(float64(math.MinInt32) - 1000)
	assert.Equal(t, int(MinQuota), q2)
	require.NotNil(t, clamp2)
}

func TestQuotaFromFloatNaNInf(t *testing.T) {
	q, clamp := QuotaFromFloatChecked(math.NaN())
	assert.Equal(t, 0, q)
	require.NotNil(t, clamp)
	assert.Equal(t, "nan", clamp.Reason)

	qInf, clampInf := QuotaFromFloatChecked(math.Inf(1))
	assert.Equal(t, int(MaxQuota), qInf)
	require.NotNil(t, clampInf)

	qNegInf, clampNegInf := QuotaFromFloatChecked(math.Inf(-1))
	assert.Equal(t, int(MinQuota), qNegInf)
	require.NotNil(t, clampNegInf)
}

func TestQuotaRound(t *testing.T) {
	// Half-away-from-zero rounding.
	assert.Equal(t, 7, QuotaRound(6.5))
	assert.Equal(t, -7, QuotaRound(-6.5))
	assert.Equal(t, 6, QuotaRound(6.4))
	assert.Equal(t, 0, QuotaRound(0.4))
}

func TestQuotaFromDecimal(t *testing.T) {
	// Truncating decimal conversion.
	assert.Equal(t, 6, QuotaFromDecimal(decimal.RequireFromString("6.9")))
	assert.Equal(t, 0, QuotaFromDecimal(decimal.RequireFromString("0.9")))
}

func TestQuotaMathNeverNegativeOnOverflow(t *testing.T) {
	// A wrapped negative float input must never produce a positive quota that
	// reads as a credit.
	for _, f := range []float64{float64(math.MinInt32) - 1, -1e18, math.Inf(-1)} {
		q, clamp := QuotaFromFloatChecked(f)
		assert.True(t, q <= 0 || clamp != nil, "input %v produced %d", f, q)
	}
}
