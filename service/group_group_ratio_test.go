package service

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/pkg/billingexpr"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

func resetSpecialRatioTestState(t *testing.T) {
	t.Helper()
	previousPrices := ExportedModelPrices()
	previousGroups := ExportedGroupRatios()
	previousSpecial := ExportedGroupGroupRatios()
	t.Cleanup(func() {
		SetModelPriceRegistry(previousPrices)
		SetGroupRatios(previousGroups)
		SetGroupGroupRatios(previousSpecial)
	})
}

func TestUserGroupSpecialRatioPrecedenceAcrossBillingModes(t *testing.T) {
	initBillingDB(t)
	resetSpecialRatioTestState(t)
	SetModelPriceRegistry(map[string]ModelPrice{
		"flat": {Prompt: 2, Completion: 4},
		"task": {Prompt: 0.01},
	})
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	SetGroupGroupRatios(map[string]map[string]float64{
		"default": {"vip": 0.5},
		"free":    {"vip": 0},
	})

	ratio, special := EffectiveGroupRatio("default", "vip")
	assert.True(t, special)
	assert.Equal(t, 0.5, ratio)
	ratio, special = EffectiveGroupRatio("other", "vip")
	assert.False(t, special)
	assert.Equal(t, 2.0, ratio)

	assert.Equal(t, 2_000, ComputeQuota("flat", "vip", 1_000, 0))
	assert.Equal(t, 500, ComputeQuotaForUser("flat", "default", "vip", 1_000, 0))
	assert.Equal(t, 0, ComputeQuotaForUser("flat", "free", "vip", 1_000, 0))

	taskQuota, err := ComputePerCallQuotaForUser("task", "default", "vip", 2)
	require.NoError(t, err)
	assert.Equal(t, 5_000, taskQuota)
	freeTaskQuota, err := ComputePerCallQuotaForUser("task", "free", "vip", 1)
	require.NoError(t, err)
	assert.Zero(t, freeTaskQuota)

	require.NoError(t, setting.UpdateOptions(map[string]string{
		"ModelBillingMode": `{"flat":"tiered_expr"}`,
		"ModelBillingExpr": `{"flat":"p * 2 + c * 4"}`,
	}))
	usage := &protocolkit.Usage{PromptTokens: 1_000, CompletionTokens: 100}
	tiered, clamp, err := ComputeBillingQuotaForUser(
		"flat", "default", "vip", false, usage, billingexpr.RequestInput{},
	)
	require.NoError(t, err)
	assert.Nil(t, clamp)
	assert.Equal(t, 600, tiered)
}

func TestGroupGroupRatioRegistryUsesDefensiveCopies(t *testing.T) {
	resetSpecialRatioTestState(t)
	source := map[string]map[string]float64{"default": {"vip": 0.75}}
	SetGroupGroupRatios(source)
	source["default"]["vip"] = 99

	exported := ExportedGroupGroupRatios()
	assert.Equal(t, 0.75, exported["default"]["vip"])
	exported["default"]["vip"] = 42
	ratio, special := EffectiveGroupRatio("default", "vip")
	assert.True(t, special)
	assert.Equal(t, 0.75, ratio)
}

func TestGroupGroupRatioOptionValidationAndReload(t *testing.T) {
	initBillingDB(t)
	resetSpecialRatioTestState(t)
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2})

	require.NoError(t, UpdateGroupGroupRatioOption(`{"default":{"vip":0.75}}`))
	assert.Equal(t, `{"default":{"vip":0.75}}`, setting.GetOption(setting.GroupGroupRatioOption))
	ratio, special := EffectiveGroupRatio("default", "vip")
	assert.True(t, special)
	assert.Equal(t, 0.75, ratio)

	for _, invalid := range []string{
		`null`, `[]`, `{"":{"vip":1}}`, `{" default":{"vip":1}}`,
		`{"default":null}`, `{"default":{"":1}}`, `{"default":{" vip":1}}`,
		`{"default":{"vip":-0.1}}`, `{"default":{"vip":1e999}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			require.Error(t, UpdateGroupGroupRatioOption(invalid))
			assert.Equal(t, `{"default":{"vip":0.75}}`, setting.GetOption(setting.GroupGroupRatioOption))
		})
	}

	tooMany := make(map[string]map[string]float64, maxSpecialRatioUserGroups+1)
	for i := 0; i <= maxSpecialRatioUserGroups; i++ {
		tooMany[fmt.Sprintf("user-%03d", i)] = map[string]float64{"vip": 1}
	}
	raw, err := common.Marshal(tooMany)
	require.NoError(t, err)
	require.ErrorContains(t, UpdateGroupGroupRatioOption(string(raw)), "too many user groups")

	// A valid value written by another node becomes live on synchronization.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.GroupGroupRatioOption).
		Update("value", `{"default":{"vip":0.25}}`).Error)
	require.NoError(t, SyncRuntimeOptions())
	ratio, special = EffectiveGroupRatio("default", "vip")
	assert.True(t, special)
	assert.Equal(t, 0.25, ratio)
}

func TestGroupGroupRatioRegistryConcurrentAccess(t *testing.T) {
	resetSpecialRatioTestState(t)
	SetGroupRatios(map[string]float64{"vip": 2})
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 500; index++ {
				if worker%2 == 0 {
					SetGroupGroupRatios(map[string]map[string]float64{
						"default": {"vip": float64(index) / 100},
					})
					continue
				}
				_, _ = EffectiveGroupRatio("default", "vip")
				_ = ExportedGroupGroupRatios()
			}
		}()
	}
	wait.Wait()
}
