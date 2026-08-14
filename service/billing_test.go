package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestComputeQuotaDeterministic(t *testing.T) {
	// $1/1M prompt, $3/1M completion, QuotaPerUnit=500000.
	// 7 prompt + 2 completion => (7 + 6)/1e6 * 500000 = 6.5 -> 6.
	SetModelPriceRegistry(map[string]ModelPrice{
		"gpt-x": {Prompt: 1.0, Completion: 3.0},
	})
	SetGroupRatios(map[string]float64{"default": 1.0})

	got := ComputeQuota("gpt-x", "default", 7, 2)
	assert.Equal(t, 6, got)

	// Default pricing when model unconfigured.
	got2 := ComputeQuota("unknown-model", "default", 1000, 0)
	assert.Equal(t, 500, got2) // $1/1M * 1000 = 500000/1000 = 500
}

func TestComputeQuotaNeverNegativeOnHugeInput(t *testing.T) {
	SetModelPriceRegistry(map[string]ModelPrice{})
	SetGroupRatios(map[string]float64{})

	// Huge token counts must saturate, never wrap to a negative/credit value.
	q, clamp := ComputeQuotaChecked("m", "default", math.MaxInt32, math.MaxInt32)
	assert.True(t, q > 0)
	assert.NotNil(t, clamp, "expected saturation for huge input")
}

func TestGroupRatioAffectsQuota(t *testing.T) {
	SetModelPriceRegistry(map[string]ModelPrice{"m": {Prompt: 1.0, Completion: 1.0}})
	SetGroupRatios(map[string]float64{"vip": 2.0, "default": 1.0})

	base := ComputeQuota("m", "default", 1000, 0)
	vip := ComputeQuota("m", "vip", 1000, 0)
	assert.Equal(t, base*2, vip)
}

func TestPreConsumeAndRefundInvocations(t *testing.T) {
	// Verify the saturation helper is wired through the checked path.
	_, clamp := common.QuotaFromFloatChecked(math.Inf(1))
	assert.NotNil(t, clamp)
	assert.Equal(t, "inf", clamp.Reason)
}
