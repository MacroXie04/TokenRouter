package billing

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"math"
	"strings"
	"testing"
)

func TestTopUpGroupRatioIsSeparateAndHotReloadable(t *testing.T) {
	setupTopUpPricingTest(t)
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "2"))
	SetGroupRatios(map[string]float64{"default": 1, "vip": 99})
	SetTopUpGroupRatios(map[string]float64{"default": 1, "vip": 1.25})

	money, err := GetTopupMoney(10, "vip")
	require.NoError(t, err)
	assert.Equal(t, 25.0, money, "routing GroupRatio must not alter top-up pricing")

	require.NoError(t, UpdateTopUpGroupRatioOption(`{"default":1,"vip":1.5}`))
	money, err = GetTopupMoney(10, "vip")
	require.NoError(t, err)
	assert.Equal(t, 30.0, money)
	assert.Equal(t, `{"default":1,"vip":1.5}`, setting.GetOption(setting.TopUpGroupRatioOption))

	// The generic option writer uses the same parser and cannot bypass live
	// registry validation or publish an ambiguous duplicate-key document.
	require.Error(t, setting.UpdateOption(setting.TopUpGroupRatioOption, `{"vip":1,"vip":2}`))
	assert.Equal(t, `{"default":1,"vip":1.5}`, setting.GetOption(setting.TopUpGroupRatioOption))
	money, err = GetTopupMoney(10, "vip")
	require.NoError(t, err)
	assert.Equal(t, 30.0, money)
}

func TestTopUpGroupRatioRejectsMalformedZeroAndOverflow(t *testing.T) {
	setupTopUpPricingTest(t)
	SetTopUpGroupRatios(map[string]float64{"default": 1, "vip": 1.5})
	for _, raw := range []string{
		`null`, `{}`, `[]`, `{"default":0}`, `{"default":-1}`,
		`{"default":1e100}`, `{" bad":1}`, `{"vip":1,"vip":2}`,
		strings.Repeat("x", maxTopUpGroupRatioBytes+1),
	} {
		require.Error(t, UpdateTopUpGroupRatioOption(raw), raw)
		ratio, err := validatedTopUpGroupRatio("vip")
		require.NoError(t, err)
		assert.Equal(t, 1.5, ratio, raw)
	}

	SetTopUpGroupRatios(map[string]float64{"default": math.Inf(1)})
	_, err := GetTopupMoney(10, "default")
	assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
}

func TestReloadPricingOptionsPublishesTopUpRatiosOnlyAfterValidation(t *testing.T) {
	setupTopUpPricingTest(t)
	const valid = `{"default":1,"vip":1.75}`
	require.NoError(t, UpdateTopUpGroupRatioOption(valid))
	require.NoError(t, ReloadPricingOptions())
	ratio, err := validatedTopUpGroupRatio("vip")
	require.NoError(t, err)
	assert.Equal(t, 1.75, ratio)

	// Simulate a malformed row written by an older node or directly in the
	// database. Sync must reject the candidate before publishing it into the
	// settings snapshot, and the live top-up registry must remain unchanged.
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", setting.TopUpGroupRatioOption).Update("value", `{"default":0}`).Error)
	require.Error(t, SyncRuntimeOptions())
	assert.Equal(t, valid, setting.GetOption(setting.TopUpGroupRatioOption))
	ratio, err = validatedTopUpGroupRatio("vip")
	require.NoError(t, err)
	assert.Equal(t, 1.75, ratio, "a rejected reload must retain the last coherent live registry")

	// An unrelated ordinary option update publishes only from the last valid
	// in-memory candidate; it cannot import the malformed database row.
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "3"))
	assert.Equal(t, valid, setting.GetOption(setting.TopUpGroupRatioOption))
	ratio, err = validatedTopUpGroupRatio("vip")
	require.NoError(t, err)
	assert.Equal(t, 1.75, ratio)

	// Repair the database so subsequent setup in this process can initialize.
	require.NoError(t, UpdateTopUpGroupRatioOption(valid))
}
