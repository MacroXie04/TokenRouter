package setting

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageRatioDefaultsFallbacksAndDetachedCopies(t *testing.T) {
	config := defaultUsageRatioSetting()
	assert.Len(t, config.CacheRatio, 73)
	assert.Len(t, config.CreateCacheRatio, 42)
	assert.Len(t, config.ImageRatio, 1)
	assert.Len(t, config.AudioRatio, 5)
	assert.Len(t, config.AudioCompletionRatio, 7)
	assert.Equal(t, 0.5, config.CacheRatio["gpt-4o"])
	assert.Equal(t, 0.25, config.CacheRatio["gpt-4.1"])
	assert.Equal(t, 0.1, config.CacheRatio["claude-opus-4-8-xhigh"])
	assert.Equal(t, 1.25, config.CreateCacheRatio["gpt-5.6-sol"])
	assert.Equal(t, 2.0, config.ImageRatio["gpt-image-1"])
	assert.Equal(t, 16.67, config.AudioRatio["gpt-4o-mini-realtime-preview"])
	assert.Equal(t, 0.0, config.AudioCompletionRatio["tts-1-hd"])
	assert.True(t, config.EnableFreeModelPreConsume)

	old := usageRatioConfig.Load()
	usageRatioConfig.Store(&config)
	t.Cleanup(func() { usageRatioConfig.Store(old) })

	policy := GetUsageRatioPolicy("unconfigured-model")
	assert.Equal(t, 1.0, policy.CacheRatio)
	assert.Equal(t, 1.25, policy.CacheCreationRatio)
	assert.Equal(t, 1.25, policy.CacheCreationFiveMinuteRatio)
	assert.Equal(t, 2.0, policy.CacheCreationOneHourRatio)
	assert.Equal(t, 1.0, policy.ImageRatio)
	assert.Equal(t, 1.0, policy.AudioRatio)
	assert.Equal(t, 1.0, policy.AudioCompletionRatio)

	detached := GetUsageRatioSetting()
	detached.CacheRatio["gpt-4o"] = 99
	detached.AudioRatio["gpt-4o-audio-preview"] = 99
	assert.Equal(t, 0.5, GetUsageRatioPolicy("gpt-4o").CacheRatio)
	assert.Equal(t, 16.0, GetUsageRatioPolicy("gpt-4o-audio-preview").AudioRatio)
}

func TestUsageRatioParsingValidationAndExplicitEmptyMaps(t *testing.T) {
	config, err := buildUsageRatioSetting(map[string]string{
		CacheRatioOption:                `{}`,
		CreateCacheRatioOption:          `{"custom":3}`,
		ImageRatioOption:                `{"custom":4}`,
		AudioRatioOption:                `{"gemini-2.5-pro-thinking-*":5,"gpt-4o-gizmo-*":6}`,
		AudioCompletionRatioOption:      `{"gemini-2.5-pro-thinking-*":7,"gpt-4o-gizmo-*":8}`,
		EnableFreeModelPreConsumeOption: "false",
	})
	require.NoError(t, err)
	assert.Empty(t, config.CacheRatio, "an explicit empty object replaces defaults")
	assert.Equal(t, 3.0, config.CreateCacheRatio["custom"])
	assert.Equal(t, 4.0, config.ImageRatio["custom"])
	assert.False(t, config.EnableFreeModelPreConsume)

	old := usageRatioConfig.Load()
	usageRatioConfig.Store(&config)
	t.Cleanup(func() { usageRatioConfig.Store(old) })
	gemini := GetUsageRatioPolicy("gemini-2.5-pro-thinking-32768")
	assert.Equal(t, 1.0, gemini.CacheRatio, "cache ratios use exact model names")
	assert.Equal(t, 5.0, gemini.AudioRatio)
	assert.Equal(t, 7.0, gemini.AudioCompletionRatio)
	gizmo := GetUsageRatioPolicy("gpt-4o-gizmo-abc")
	assert.Equal(t, 6.0, gizmo.AudioRatio)
	assert.Equal(t, 8.0, gizmo.AudioCompletionRatio)
}

func TestUsageRatioRejectsMalformedAndUnboundedMaps(t *testing.T) {
	invalid := []map[string]string{
		{CacheRatioOption: `{"m":1,"m":2}`},
		{CreateCacheRatioOption: `null`},
		{ImageRatioOption: `[]`},
		{AudioRatioOption: `{"m":-1}`},
		{AudioCompletionRatioOption: `{"m":1000000000001}`},
		{AudioRatioOption: `{" bad ":1}`},
		{EnableFreeModelPreConsumeOption: "yes"},
		{CacheRatioOption: strings.Repeat("x", maxModelPolicyOptionBytes+1)},
	}
	for index, options := range invalid {
		_, err := buildUsageRatioSetting(options)
		assert.Error(t, err, "case %d", index)
	}

	tooMany := make(map[string]float64, maxUsageRatioEntries+1)
	for index := 0; index <= maxUsageRatioEntries; index++ {
		tooMany[fmt.Sprintf("m-%d", index)] = 1
	}
	assert.Error(t, validateUsageRatioMap(CacheRatioOption, tooMany))
}

func TestUsageRatioAtomicSnapshotNeverMixesReloads(t *testing.T) {
	first, err := buildUsageRatioSetting(map[string]string{
		CacheRatioOption:                `{"m":1}`,
		CreateCacheRatioOption:          `{"m":1}`,
		ImageRatioOption:                `{"m":1}`,
		AudioRatioOption:                `{"m":1}`,
		AudioCompletionRatioOption:      `{"m":1}`,
		EnableFreeModelPreConsumeOption: "true",
	})
	require.NoError(t, err)
	second, err := buildUsageRatioSetting(map[string]string{
		CacheRatioOption:                `{"m":2}`,
		CreateCacheRatioOption:          `{"m":2}`,
		ImageRatioOption:                `{"m":2}`,
		AudioRatioOption:                `{"m":2}`,
		AudioCompletionRatioOption:      `{"m":2}`,
		EnableFreeModelPreConsumeOption: "false",
	})
	require.NoError(t, err)

	old := usageRatioConfig.Load()
	usageRatioConfig.Store(&first)
	t.Cleanup(func() { usageRatioConfig.Store(old) })

	var wait sync.WaitGroup
	wait.Add(2)
	errors := make(chan string, 1)
	go func() {
		defer wait.Done()
		for index := 0; index < 10_000; index++ {
			if index%2 == 0 {
				usageRatioConfig.Store(&first)
			} else {
				usageRatioConfig.Store(&second)
			}
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 10_000; index++ {
			policy := GetUsageRatioPolicy("m")
			allFirst := policy.CacheRatio == 1 && policy.CacheCreationRatio == 1 &&
				policy.ImageRatio == 1 && policy.AudioRatio == 1 &&
				policy.AudioCompletionRatio == 1 && policy.EnableFreeModelPreConsume
			allSecond := policy.CacheRatio == 2 && policy.CacheCreationRatio == 2 &&
				policy.ImageRatio == 2 && policy.AudioRatio == 2 &&
				policy.AudioCompletionRatio == 2 && !policy.EnableFreeModelPreConsume
			if !allFirst && !allSecond {
				select {
				case errors <- fmt.Sprintf("mixed policy: %#v", policy):
				default:
				}
				return
			}
		}
	}()
	wait.Wait()
	select {
	case message := <-errors:
		t.Fatal(message)
	default:
	}
}
