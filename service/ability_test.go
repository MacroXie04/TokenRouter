package service

import (
	"math/rand"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func initTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	model.LOG_DB = db
}

func seedChannels(t *testing.T, n int) []model.Channel {
	t.Helper()
	channels := make([]model.Channel, n)
	for i := 0; i < n; i++ {
		w := uint(1)
		channels[i] = model.Channel{
			Name:   "ch" + itoa(i),
			Type:   int(constant.ChannelTypeOpenAI),
			Key:    "sk-test",
			Status: constant.ChannelStatusEnabled,
			Weight: &w,
		}
		require.NoError(t, model.DB.Create(&channels[i]).Error)
	}
	return channels
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func TestPrioritySelection(t *testing.T) {
	initTestDB(t)
	channels := seedChannels(t, 2)
	prio := func(v int64) *int64 { return &v }

	// Channel 0 priority 10, channel 1 priority 100 -> channel 1 wins.
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[0].Id, Enabled: true, Priority: prio(10), Weight: 100}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[1].Id, Enabled: true, Priority: prio(100), Weight: 1}).Error)
	require.NoError(t, InitAbilityCache())

	got, err := GetRandomSatisfiedChannel("default", "m", nil, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.Equal(t, channels[1].Id, got.Id)
}

func TestWeightedSelectionStatisticallyBounded(t *testing.T) {
	initTestDB(t)
	channels := seedChannels(t, 2)

	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[0].Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[1].Id, Enabled: true, Weight: 3}).Error)
	require.NoError(t, InitAbilityCache())

	counts := map[int]int{}
	const trials = 10000
	r := rand.New(rand.NewSource(42))
	for i := 0; i < trials; i++ {
		ch, err := GetRandomSatisfiedChannel("default", "m", nil, r)
		require.NoError(t, err)
		counts[ch.Id]++
	}
	// Weight 3 channel should be selected ~75% of the time (within 5%).
	ratio := float64(counts[channels[1].Id]) / trials
	assert.InDelta(t, 0.75, ratio, 0.05)
}

func TestIgnoreChannel(t *testing.T) {
	initTestDB(t)
	channels := seedChannels(t, 2)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[0].Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[1].Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, InitAbilityCache())

	got, err := GetRandomSatisfiedChannel("default", "m", map[int]struct{}{channels[0].Id: {}}, nil)
	require.NoError(t, err)
	assert.Equal(t, channels[1].Id, got.Id)
}

func TestNoChannelReturnsError(t *testing.T) {
	initTestDB(t)
	require.NoError(t, InitAbilityCache())
	_, err := GetRandomSatisfiedChannel("default", "missing", nil, nil)
	assert.Error(t, err)
	assert.Equal(t, ErrChannelNotFound, err)
}
