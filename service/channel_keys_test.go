package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestParseMultiKeys(t *testing.T) {
	assert.Equal(t, []string{"k1", "k2"}, parseMultiKeys(`["k1","k2"]`))
	assert.Equal(t, []string{"k1", "k2"}, parseMultiKeys(`{"keys":["k1","k2"]}`))
	assert.Nil(t, parseMultiKeys(""))
	assert.Nil(t, parseMultiKeys("not-json"))
}

func TestGetChannelKeyRotation(t *testing.T) {
	ch := &model.Channel{Id: 1, Key: "single", Other: `["a","b","c"]`}

	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		seen[GetChannelKey(ch)] = true
	}
	// All three keys are used across calls.
	assert.True(t, seen["a"] && seen["b"] && seen["c"], "rotation must use all keys: %v", seen)

	// A channel with a single key returns it directly.
	single := &model.Channel{Id: 2, Key: "single"}
	assert.Equal(t, "single", GetChannelKey(single))
}
