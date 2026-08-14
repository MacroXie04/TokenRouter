package service

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestChannelAffinity(t *testing.T) {
	initTestDB(t)
	channels := seedChannels(t, 2)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[0].Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channels[1].Id, Enabled: true, Weight: 1}).Error)
	require.NoError(t, InitAbilityCache())

	// Pin to channel B.
	SetAffinityChannel(991, "m", channels[1].Id)
	got, err := GetSatisfiedChannelWithAffinity("default", "m", 991, nil, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.Equal(t, channels[1].Id, got.Id, "affinity must prefer the pinned channel")

	// A different user (no affinity) falls back to normal selection.
	got2, err := GetSatisfiedChannelWithAffinity("default", "m", 992, nil, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.NotEqual(t, 0, got2.Id)

	// Disabling the pinned channel forces a fallback.
	require.NoError(t, model.DB.Model(&channels[1]).Update("status", constant.ChannelStatusManuallyDisabled).Error)
	got3, err := GetSatisfiedChannelWithAffinity("default", "m", 991, nil, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.Equal(t, channels[0].Id, got3.Id, "disabled affinity channel must fall back")
}
