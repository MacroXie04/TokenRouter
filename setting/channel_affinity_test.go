package setting

import (
	"strconv"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func setupAffinitySettingTest(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, Init())
}

func TestChannelAffinitySettingValidationAndRemoteSync(t *testing.T) {
	setupAffinitySettingTest(t)
	defaults := GetChannelAffinitySetting()
	assert.True(t, defaults.Enabled)
	assert.Equal(t, DefaultChannelAffinityMaxEntries, defaults.MaxEntries)
	require.Len(t, defaults.Rules, 2)

	invalidRules := `[{"name":"broken","model_regex":["("],"key_sources":[{"type":"gjson","path":"key"}]}]`
	require.Error(t, UpdateOption(ChannelAffinityRulesOption, invalidRules))
	assert.Empty(t, GetOption(ChannelAffinityRulesOption))
	assert.Equal(t, defaults.Rules[0].Name, GetChannelAffinitySetting().Rules[0].Name)

	require.NoError(t, model.DB.Create(&model.Option{Key: ChannelAffinityMaxEntriesOption, Value: "23"}).Error)
	require.NoError(t, Sync())
	assert.Equal(t, 23, GetChannelAffinitySetting().MaxEntries)

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", ChannelAffinityMaxEntriesOption).Update("value", "-1").Error)
	require.Error(t, Sync())
	assert.Equal(t, 23, GetChannelAffinitySetting().MaxEntries, "invalid remote options must not replace the live snapshot")
}

func TestChannelAffinitySettingConcurrentUpdatesRemainCoherent(t *testing.T) {
	setupAffinitySettingTest(t)
	const workers = 40
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			if index%2 == 0 {
				errors <- UpdateOption(ChannelAffinityMaxEntriesOption, strconv.Itoa(100+index))
			} else {
				errors <- UpdateOption(ChannelAffinityEnabledOption, strconv.FormatBool(index%4 == 1))
			}
		}(i)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	config := GetChannelAffinitySetting()
	storedMax, err := strconv.Atoi(GetOption(ChannelAffinityMaxEntriesOption))
	require.NoError(t, err)
	storedEnabled, err := strconv.ParseBool(GetOption(ChannelAffinityEnabledOption))
	require.NoError(t, err)
	assert.Equal(t, storedMax, config.MaxEntries)
	assert.Equal(t, storedEnabled, config.Enabled)
}
