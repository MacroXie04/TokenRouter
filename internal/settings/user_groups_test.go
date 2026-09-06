package settings

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserUsableGroupsDefaultsValidationAndCopies(t *testing.T) {
	setupAffinitySettingTest(t)

	groups := GetUserUsableGroups()
	assert.Equal(t, map[string]string{"default": "default", "vip": "vip"}, groups)
	groups["mutated"] = "outside"
	assert.NotContains(t, GetUserUsableGroups(), "mutated", "callers must not mutate the live snapshot")

	for _, invalid := range []string{
		`["default"]`,
		`{"":"empty"}`,
		`{" vip":"whitespace"}`,
	} {
		require.Error(t, UpdateOption(UserUsableGroupsOption, invalid))
		assert.Empty(t, GetOption(UserUsableGroupsOption))
		assert.Contains(t, GetUserUsableGroups(), "default")
	}

	require.NoError(t, UpdateOption(UserUsableGroupsOption, `{"staff":"Staff"}`))
	assert.Equal(t, map[string]string{"staff": "Staff"}, GetUserUsableGroups())
	require.NoError(t, UpdateOption(UserUsableGroupsOption, `{"auto":"Automatic","staff":"Staff"}`))
	assert.Equal(t, "Automatic", GetUserUsableGroups()["auto"])
}

func TestAutoGroupSettingsValidateAndPublishAtomically(t *testing.T) {
	setupAffinitySettingTest(t)
	assert.Equal(t, []string{"default"}, GetAutoGroups())
	assert.Equal(t, DefaultMaxTokenAutoGroups, GetMaxTokenAutoGroups())

	for _, update := range []map[string]string{
		{AutoGroupsOption: `{"default":true}`},
		{AutoGroupsOption: `["default","default"]`},
		{AutoGroupsOption: `["auto"]`},
		{MaxTokenAutoGroupsOption: "0"},
		{MaxTokenAutoGroupsOption: strconv.Itoa(MaxTokenAutoGroupsUpperBound + 1)},
	} {
		require.Error(t, UpdateOptions(update))
		assert.Equal(t, []string{"default"}, GetAutoGroups())
		assert.Equal(t, DefaultMaxTokenAutoGroups, GetMaxTokenAutoGroups())
	}

	require.NoError(t, UpdateOptions(map[string]string{
		UserUsableGroupsOption:   `{"default":"Default","vip":"VIP"}`,
		AutoGroupsOption:         `["vip","default"]`,
		MaxTokenAutoGroupsOption: "1",
	}))
	groups, maxCount := GetAutoGroupConfig()
	assert.Equal(t, []string{"vip", "default"}, groups)
	assert.Equal(t, 1, maxCount)
}

func TestRoutingSettingsConcurrentPublicationIsCoherent(t *testing.T) {
	setupAffinitySettingTest(t)
	first := map[string]string{
		UserUsableGroupsOption: `{"default":"Default"}`,
		AutoGroupsOption:       `["default"]`, MaxTokenAutoGroupsOption: "1",
	}
	second := map[string]string{
		UserUsableGroupsOption: `{"vip":"VIP"}`,
		AutoGroupsOption:       `["vip","staff"]`, MaxTokenAutoGroupsOption: "2",
	}
	require.NoError(t, UpdateOptions(first))

	const iterations = 40
	var wait sync.WaitGroup
	wait.Add(2)
	errs := make(chan error, iterations)
	go func() {
		defer wait.Done()
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				errs <- UpdateOptions(second)
			} else {
				errs <- UpdateOptions(first)
			}
		}
	}()
	go func() {
		defer wait.Done()
		for i := 0; i < iterations*10; i++ {
			config := groupRoutingConfig.Load()
			require.NotNil(t, config)
			groups := config.autoGroups.groups
			if config.autoGroups.maxCount == 1 {
				assert.Equal(t, []string{"default"}, groups)
				assert.Contains(t, config.userGroups.groups, "default")
			} else {
				assert.Equal(t, []string{"vip", "staff"}, groups)
				assert.Contains(t, config.userGroups.groups, "vip")
			}
		}
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
