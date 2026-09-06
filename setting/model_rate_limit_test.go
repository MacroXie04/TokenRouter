package setting

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestModelRequestRateLimitDefaultsValidationAndAtomicPublication(t *testing.T) {
	setupAffinitySettingTest(t)

	defaults := GetModelRequestRateLimitSetting()
	assert.False(t, defaults.Enabled)
	assert.Equal(t, 1, defaults.DurationMinutes)
	assert.Zero(t, defaults.TotalLimit)
	assert.Equal(t, 1000, defaults.SuccessLimit)
	assert.Empty(t, defaults.Groups)

	require.NoError(t, UpdateOptions(map[string]string{
		ModelRequestRateLimitEnabledOption:         "true",
		ModelRequestRateLimitDurationMinutesOption: "15",
		ModelRequestRateLimitCountOption:           "250",
		ModelRequestRateLimitSuccessCountOption:    "200",
		ModelRequestRateLimitGroupOption:           `{"vip":[0,2147483647],"default":[100,80]}`,
	}))
	configured := GetModelRequestRateLimitSetting()
	assert.True(t, configured.Enabled)
	assert.Equal(t, 15, configured.DurationMinutes)
	assert.Equal(t, 250, configured.TotalLimit)
	assert.Equal(t, 200, configured.SuccessLimit)
	assert.Equal(t, [2]int{0, 2147483647}, configured.Groups["vip"])
	assert.Equal(t, [2]int{100, 80}, configured.Groups["default"])
	assert.Equal(t, 0, func() int { total, _ := ModelRequestRateLimits("vip"); return total }())
	assert.Equal(t, 200, func() int { _, success := ModelRequestRateLimits("other"); return success }())

	configured.Groups["vip"] = [2]int{1, 1}
	assert.Equal(t, [2]int{0, 2147483647}, GetModelRequestRateLimitSetting().Groups["vip"], "callers must receive a detached map")

	before := GetModelRequestRateLimitSetting()
	invalid := []map[string]string{
		{ModelRequestRateLimitEnabledOption: "yes"},
		{ModelRequestRateLimitDurationMinutesOption: "0"},
		{ModelRequestRateLimitDurationMinutesOption: "43201"},
		{ModelRequestRateLimitCountOption: "-1"},
		{ModelRequestRateLimitSuccessCountOption: "0"},
		{ModelRequestRateLimitSuccessCountOption: "100000001"},
		{ModelRequestRateLimitGroupOption: `[]`},
		{ModelRequestRateLimitGroupOption: `{"vip":[1]}`},
		{ModelRequestRateLimitGroupOption: `{"vip":[1,2,3]}`},
		{ModelRequestRateLimitGroupOption: `{"vip":[1.5,2]}`},
		{ModelRequestRateLimitGroupOption: `{"vip":[-1,2]}`},
		{ModelRequestRateLimitGroupOption: `{"vip":[1,0]}`},
		{ModelRequestRateLimitGroupOption: `{"vip":[1,2147483648]}`},
		{ModelRequestRateLimitGroupOption: `{"vip":[1,2],"vip":[3,4]}`},
		{ModelRequestRateLimitGroupOption: `{" bad ":[1,2]}`},
		{ModelRequestRateLimitGroupOption: strings.Repeat("x", maxModelRequestRateLimitJSONBytes+1)},
	}
	for _, update := range invalid {
		require.Error(t, UpdateOptions(update), update)
		assert.Equal(t, before, GetModelRequestRateLimitSetting(), "a rejected update must not publish partial policy")
	}
}

func TestModelRequestRateLimitRemoteSyncRetainsLastValidSnapshot(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(ModelRequestRateLimitSuccessCountOption, "17"))
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", ModelRequestRateLimitSuccessCountOption).Update("value", "0").Error)
	require.Error(t, Sync())
	assert.Equal(t, 17, GetModelRequestRateLimitSetting().SuccessLimit)
	assert.Equal(t, "17", GetOption(ModelRequestRateLimitSuccessCountOption))
}

func TestModelRequestRateLimitConcurrentSnapshotsRemainCoherent(t *testing.T) {
	setupAffinitySettingTest(t)
	const workers = 32
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			if index%2 == 0 {
				errors <- UpdateOption(ModelRequestRateLimitCountOption, "100")
				return
			}
			errors <- UpdateOption(ModelRequestRateLimitGroupOption, `{"default":[50,40]}`)
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	config := GetModelRequestRateLimitSetting()
	assert.Equal(t, 100, config.TotalLimit)
	assert.Equal(t, [2]int{50, 40}, config.Groups["default"])
}
