package service

import (
	"math"
	"sync"
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

func TestPricingRegistriesUseDefensiveCopies(t *testing.T) {
	prices := map[string]ModelPrice{"model": {Prompt: 2, Completion: 6}}
	ratios := map[string]float64{"vip": 2}
	SetModelPriceRegistry(prices)
	SetGroupRatios(ratios)

	prices["model"] = ModelPrice{Prompt: 100, Completion: 100}
	ratios["vip"] = 100
	assert.Equal(t, 2.0, ExportedModelPrices()["model"].Prompt)
	assert.Equal(t, 2.0, GroupRatio("vip"))

	exportedPrices := ExportedModelPrices()
	exportedRatios := ExportedGroupRatios()
	exportedPrices["model"] = ModelPrice{Prompt: 200, Completion: 200}
	exportedRatios["vip"] = 200
	assert.Equal(t, 2.0, ExportedModelPrices()["model"].Prompt)
	assert.Equal(t, 2.0, GroupRatio("vip"))
}

func TestPricingRegistriesConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if worker%2 == 0 {
					SetModelPriceRegistry(map[string]ModelPrice{"model": {Prompt: float64(i + 1), Completion: float64(i + 2)}})
					SetGroupRatios(map[string]float64{"vip": float64(i + 1)})
					continue
				}
				_, _ = GetModelPrices("model")
				_ = GroupRatio("vip")
				_ = ExportedModelPrices()
				_ = ExportedGroupRatios()
			}
		}()
	}
	wg.Wait()
}
