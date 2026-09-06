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

func TestAddQuotaWithinBoundsRejectsMachineAndDomainOverflow(t *testing.T) {
	value, ok := AddQuotaWithinBounds(int(MaxQuota)-10, 10)
	require.True(t, ok)
	assert.Equal(t, int(MaxQuota), value)

	for _, test := range []struct {
		current int
		delta   int
	}{
		{current: int(MaxQuota), delta: 1},
		{current: math.MaxInt - 10, delta: 20},
		{current: -1, delta: 1},
		{current: 1, delta: -1},
	} {
		_, ok := AddQuotaWithinBounds(test.current, test.delta)
		assert.False(t, ok)
	}
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

func TestQuotaFromDecimalStrict(t *testing.T) {
	// In-range values convert exactly.
	q, err := QuotaFromDecimalStrict(decimal.RequireFromString("4995000"))
	require.NoError(t, err)
	assert.Equal(t, 4995000, q)

	// Zero is valid (free plans).
	q, err = QuotaFromDecimalStrict(decimal.Zero)
	require.NoError(t, err)
	assert.Equal(t, 0, q)

	// A value past the int32 boundary must error, never bill the clamp.
	_, err = QuotaFromDecimalStrict(decimal.NewFromInt(MaxQuota + 1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflow")

	_, err = QuotaFromDecimalStrict(decimal.NewFromInt(MinQuota - 1))
	require.Error(t, err)
}

func TestQuotaConversionsHandleExtremeUncheckedInputs(t *testing.T) {
	assert.NotPanics(t, func() {
		assert.Equal(t, int(MaxQuota), QuotaFromFloat(math.Inf(1)))
		assert.Equal(t, int(MinQuota), QuotaRound(math.Inf(-1)))
	})

	huge := decimal.RequireFromString("1e100")
	negativeHuge := decimal.RequireFromString("-1e100")
	assert.Equal(t, int(MaxQuota), QuotaFromDecimal(huge))
	assert.Equal(t, int(MinQuota), QuotaFromDecimal(negativeHuge))
	_, err := QuotaFromDecimalStrict(huge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overflow")
	_, err = QuotaFromDecimalStrict(negativeHuge)
	require.Error(t, err)

	q, err := QuotaFromDecimalStrict(decimal.RequireFromString("2147483647.9"))
	require.NoError(t, err)
	assert.Equal(t, int(MaxQuota), q)
	q, err = QuotaFromDecimalStrict(decimal.RequireFromString("-2147483648.9"))
	require.NoError(t, err)
	assert.Equal(t, int(MinQuota), q)
}
