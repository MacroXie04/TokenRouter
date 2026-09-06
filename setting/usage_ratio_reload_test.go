package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestUsageRatioUpdatePublishesAllFieldsOrNone(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		CacheRatioOption:                `{"m":0.1}`,
		CreateCacheRatioOption:          `{"m":1.5}`,
		ImageRatioOption:                `{"m":2}`,
		AudioRatioOption:                `{"m":3}`,
		AudioCompletionRatioOption:      `{"m":4}`,
		EnableFreeModelPreConsumeOption: "false",
	}))

	want := GetUsageRatioPolicy("m")
	oneHourMultiplier := float64(claudeCacheCreationOneHourMultiplier)
	assert.Equal(t, UsageRatioPolicy{
		CacheRatio: 0.1, CacheCreationRatio: 1.5,
		CacheCreationFiveMinuteRatio: 1.5, CacheCreationOneHourRatio: 1.5 * oneHourMultiplier,
		ImageRatio: 2, AudioRatio: 3, AudioCompletionRatio: 4,
		AudioRatioConfigured: true, AudioCompletionConfigured: true,
		EnableFreeModelPreConsume: false,
	}, want)

	require.Error(t, UpdateOptions(map[string]string{
		CacheRatioOption:           `{"m":9}`,
		AudioCompletionRatioOption: `{"m":-1}`,
	}))
	assert.Equal(t, want, GetUsageRatioPolicy("m"))
	assert.Equal(t, `{"m":0.1}`, GetOption(CacheRatioOption))
}

func TestUsageRatioRemoteSyncRetainsLastValidRawAndTypedSnapshots(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		CacheRatioOption:                `{"m":0.2}`,
		EnableFreeModelPreConsumeOption: "false",
	}))
	want := GetUsageRatioPolicy("m")

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", CacheRatioOption).Update("value", `{"m":1,"m":2}`).Error)
	require.Error(t, Sync())

	assert.Equal(t, want, GetUsageRatioPolicy("m"))
	assert.Equal(t, `{"m":0.2}`, GetOption(CacheRatioOption))
	assert.Equal(t, "false", GetOption(EnableFreeModelPreConsumeOption))
}
