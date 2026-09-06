package settings

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func TestGrokSettingDefaultsValidationAndAtomicPublication(t *testing.T) {
	setupAffinitySettingTest(t)

	defaults := GetGrokSetting()
	assert.True(t, defaults.ViolationDeductionEnabled)
	assert.Equal(t, DefaultGrokViolationDeductionAmount, defaults.ViolationDeductionAmount)
	assert.Equal(t, "0.05", defaults.ViolationDeductionAmountDecimal().String())
	assert.Equal(t, map[string]string{
		GrokViolationDeductionEnabledOption: "true",
		GrokViolationDeductionAmountOption:  "0.05",
	}, GrokOptionDefaults())

	require.NoError(t, UpdateOptions(map[string]string{
		GrokViolationDeductionEnabledOption: "false",
		GrokViolationDeductionAmountOption:  "0.125000",
	}))
	published := GetGrokSetting()
	assert.False(t, published.ViolationDeductionEnabled)
	assert.Equal(t, 0.125, published.ViolationDeductionAmount)
	assert.Equal(t, "0.125", published.ViolationDeductionAmountDecimal().String())

	for _, update := range []map[string]string{
		{GrokViolationDeductionEnabledOption: "1"},
		{GrokViolationDeductionEnabledOption: ""},
		{GrokViolationDeductionAmountOption: ""},
		{GrokViolationDeductionAmountOption: "-0.01"},
		{GrokViolationDeductionAmountOption: "NaN"},
		{GrokViolationDeductionAmountOption: "+Inf"},
		{GrokViolationDeductionAmountOption: "1e100"},
		{GrokViolationDeductionAmountOption: "1e65"},
		{GrokViolationDeductionAmountOption: "1e-65"},
		{GrokViolationDeductionAmountOption: "1e2147483647"},
		{GrokViolationDeductionAmountOption: "1e-2147483648"},
		{GrokViolationDeductionAmountOption: "1e+2147483647"},
		{GrokViolationDeductionAmountOption: strings.Repeat("1", 65)},
		{GrokViolationDeductionAmountOption: "0." + strings.Repeat("0", 64)},
		{GrokViolationDeductionAmountOption: "1e+0000"},
	} {
		require.Error(t, UpdateOptions(update), update)
		assert.Equal(t, published, GetGrokSetting(), "a rejected update must retain the live snapshot")
	}

	require.NoError(t, UpdateOptions(map[string]string{
		GrokViolationDeductionEnabledOption: "true",
		GrokViolationDeductionAmountOption:  "0",
	}))
	zero := GetGrokSetting()
	assert.True(t, zero.ViolationDeductionEnabled)
	assert.Zero(t, zero.ViolationDeductionAmount)
	assert.True(t, zero.ViolationDeductionAmountDecimal().IsZero())
}

func TestGrokSettingAcceptsBoundedDecimalAndScientificSyntax(t *testing.T) {
	for _, raw := range []string{
		"0",
		"+0.05",
		"5e-2",
		"1e-64",
		"0e64",
		"0." + strings.Repeat("0", 62) + "1",
		"4294967294e-6",
	} {
		config, err := buildGrokSetting(map[string]string{GrokViolationDeductionAmountOption: raw})
		require.NoError(t, err, raw)
		assert.False(t, config.ViolationDeductionAmountDecimal().IsNegative(), raw)
		assert.False(t, config.ViolationDeductionAmountDecimal().GreaterThan(maxGrokViolationDeductionAmount), raw)
	}
}

func TestGrokRemoteSyncRejectsMalformedPolicyAndRetainsCache(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		GrokViolationDeductionEnabledOption: "true",
		GrokViolationDeductionAmountOption:  "0.25",
	}))
	before := GetGrokSetting()

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", GrokViolationDeductionAmountOption).
		Update("value", "not-a-number").Error)
	require.Error(t, Sync())

	assert.Equal(t, before, GetGrokSetting())
	assert.Equal(t, "0.25", GetOption(GrokViolationDeductionAmountOption),
		"a malformed database value must not replace the raw or typed live cache")

	// An ordinary update builds from the last validated cache, so it may proceed
	// but cannot publish the malformed database value.
	require.NoError(t, UpdateOption(SystemNameOption, "safe-publication"))
	assert.Equal(t, before, GetGrokSetting())
}
