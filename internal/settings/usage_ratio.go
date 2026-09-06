package settings

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
)

const (
	CacheRatioOption                     = "CacheRatio"
	CreateCacheRatioOption               = "CreateCacheRatio"
	ImageRatioOption                     = "ImageRatio"
	AudioRatioOption                     = "AudioRatio"
	AudioCompletionRatioOption           = "AudioCompletionRatio"
	EnableFreeModelPreConsumeOption      = "quota_setting.enable_free_model_pre_consume"
	defaultCacheRatio                    = 1.0
	defaultCreateCacheRatio              = 1.25
	defaultImageRatio                    = 1.0
	defaultAudioRatio                    = 1.0
	defaultAudioCompletionRatio          = 1.0
	maxUsageRatioEntries                 = 20_000
	maxUsageRatioValue                   = 1e12
	claudeCacheCreationOneHourMultiplier = 6.0 / 3.75
)

// UsageRatioSetting is the detached representation of the live auxiliary
// usage-pricing policy. The runtime stores an immutable instance and callers
// that need the maps receive clones, preventing accidental mutation.
type UsageRatioSetting struct {
	CacheRatio                map[string]float64
	CreateCacheRatio          map[string]float64
	ImageRatio                map[string]float64
	AudioRatio                map[string]float64
	AudioCompletionRatio      map[string]float64
	EnableFreeModelPreConsume bool
}

// UsageRatioPolicy is one request's scalar policy snapshot. Capturing all
// fields from one atomic load keeps an in-flight request internally coherent
// while administrators hot-reload the underlying maps.
type UsageRatioPolicy struct {
	CacheRatio                   float64
	CacheCreationRatio           float64
	CacheCreationFiveMinuteRatio float64
	CacheCreationOneHourRatio    float64
	ImageRatio                   float64
	AudioRatio                   float64
	AudioCompletionRatio         float64
	AudioRatioConfigured         bool
	AudioCompletionConfigured    bool
	EnableFreeModelPreConsume    bool
}

var usageRatioConfig atomic.Pointer[UsageRatioSetting]

func init() {
	config := defaultUsageRatioSetting()
	usageRatioConfig.Store(&config)
}

func defaultUsageRatioSetting() UsageRatioSetting {
	cache := make(map[string]float64)
	for _, model := range []string{
		"gemini-3-flash-preview", "gemini-3-pro-preview", "gemini-3.1-pro-preview",
		"gpt-5", "gpt-5-2025-08-07", "gpt-5-chat-latest", "gpt-5-mini",
		"gpt-5-mini-2025-08-07", "gpt-5-nano", "gpt-5-nano-2025-08-07",
		"claude-3-sonnet-20240229", "claude-3-opus-20240229", "claude-3-haiku-20240307",
		"claude-3-5-haiku-20241022", "claude-haiku-4-5-20251001",
		"claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20241022",
		"claude-3-7-sonnet-20250219", "claude-3-7-sonnet-20250219-thinking",
		"claude-sonnet-4-20250514", "claude-sonnet-4-20250514-thinking",
		"claude-opus-4-20250514", "claude-opus-4-20250514-thinking",
		"claude-opus-4-1-20250805", "claude-opus-4-1-20250805-thinking",
		"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929-thinking",
		"claude-opus-4-5-20251101", "claude-opus-4-5-20251101-thinking",
		"claude-opus-4-6", "claude-opus-4-6-thinking", "claude-opus-4-6-max",
		"claude-opus-4-6-high", "claude-opus-4-6-medium", "claude-opus-4-6-low",
		"claude-opus-4-7", "claude-opus-4-7-thinking", "claude-opus-4-7-max",
		"claude-opus-4-7-xhigh", "claude-opus-4-7-high", "claude-opus-4-7-medium",
		"claude-opus-4-7-low", "claude-opus-4-8", "claude-opus-4-8-thinking",
		"claude-opus-4-8-max", "claude-opus-4-8-xhigh", "claude-opus-4-8-high",
		"claude-opus-4-8-medium", "claude-opus-4-8-low",
	} {
		cache[model] = 0.1
	}
	for _, model := range []string{
		"gpt-4", "o1", "o1-2024-12-17", "o1-preview-2024-09-12", "o1-preview",
		"o1-mini-2024-09-12", "o1-mini", "o3-mini", "o3-mini-2025-01-31",
		"gpt-4o-2024-11-20", "gpt-4o-2024-08-06", "gpt-4o", "gpt-4o-mini-2024-07-18",
		"gpt-4o-mini", "gpt-4o-realtime-preview", "gpt-4o-mini-realtime-preview",
		"gpt-4.5-preview", "gpt-4.5-preview-2025-02-27",
	} {
		cache[model] = 0.5
	}
	for _, model := range []string{
		"gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "deepseek-chat",
		"deepseek-reasoner", "deepseek-coder",
	} {
		cache[model] = 0.25
	}

	create := make(map[string]float64)
	for _, model := range []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"claude-3-sonnet-20240229", "claude-3-opus-20240229", "claude-3-haiku-20240307",
		"claude-3-5-haiku-20241022", "claude-haiku-4-5-20251001",
		"claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20241022",
		"claude-3-7-sonnet-20250219", "claude-3-7-sonnet-20250219-thinking",
		"claude-sonnet-4-20250514", "claude-sonnet-4-20250514-thinking",
		"claude-opus-4-20250514", "claude-opus-4-20250514-thinking",
		"claude-opus-4-1-20250805", "claude-opus-4-1-20250805-thinking",
		"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929-thinking",
		"claude-opus-4-5-20251101", "claude-opus-4-5-20251101-thinking",
		"claude-opus-4-6", "claude-opus-4-6-thinking", "claude-opus-4-6-max",
		"claude-opus-4-6-high", "claude-opus-4-6-medium", "claude-opus-4-6-low",
		"claude-opus-4-7", "claude-opus-4-7-thinking", "claude-opus-4-7-max",
		"claude-opus-4-7-xhigh", "claude-opus-4-7-high", "claude-opus-4-7-medium",
		"claude-opus-4-7-low", "claude-opus-4-8", "claude-opus-4-8-thinking",
		"claude-opus-4-8-max", "claude-opus-4-8-xhigh", "claude-opus-4-8-high",
		"claude-opus-4-8-medium", "claude-opus-4-8-low",
	} {
		create[model] = defaultCreateCacheRatio
	}

	return UsageRatioSetting{
		CacheRatio:       cache,
		CreateCacheRatio: create,
		ImageRatio:       map[string]float64{"gpt-image-1": 2},
		AudioRatio: map[string]float64{
			"gpt-4o-audio-preview": 16, "gpt-4o-mini-audio-preview": 66.67,
			"gpt-4o-realtime-preview": 8, "gpt-4o-mini-realtime-preview": 16.67,
			"gpt-4o-mini-tts": 25,
		},
		AudioCompletionRatio: map[string]float64{
			"gpt-4o-realtime": 2, "gpt-4o-mini-realtime": 2, "gpt-4o-mini-tts": 1,
			"tts-1": 0, "tts-1-hd": 0, "tts-1-1106": 0, "tts-1-hd-1106": 0,
		},
		EnableFreeModelPreConsume: true,
	}
}

// UsageRatioOptionDefaults returns the complete persisted settings surface.
func UsageRatioOptionDefaults() map[string]string {
	config := defaultUsageRatioSetting()
	return map[string]string{
		CacheRatioOption:                mustPolicyJSON(config.CacheRatio),
		CreateCacheRatioOption:          mustPolicyJSON(config.CreateCacheRatio),
		ImageRatioOption:                mustPolicyJSON(config.ImageRatio),
		AudioRatioOption:                mustPolicyJSON(config.AudioRatio),
		AudioCompletionRatioOption:      mustPolicyJSON(config.AudioCompletionRatio),
		EnableFreeModelPreConsumeOption: "true",
	}
}

func buildUsageRatioSetting(options map[string]string) (UsageRatioSetting, error) {
	config := defaultUsageRatioSetting()
	if options == nil {
		return config, nil
	}
	for _, candidate := range []struct {
		key    string
		target *map[string]float64
	}{
		{CacheRatioOption, &config.CacheRatio},
		{CreateCacheRatioOption, &config.CreateCacheRatio},
		{ImageRatioOption, &config.ImageRatio},
		{AudioRatioOption, &config.AudioRatio},
		{AudioCompletionRatioOption, &config.AudioCompletionRatio},
	} {
		raw := strings.TrimSpace(options[candidate.key])
		if raw == "" {
			continue
		}
		var values map[string]float64
		if err := decodeBoundedPolicyJSON(raw, &values); err != nil {
			return UsageRatioSetting{}, fmt.Errorf("%s: %w", candidate.key, err)
		}
		if err := validateUsageRatioMap(candidate.key, values); err != nil {
			return UsageRatioSetting{}, err
		}
		*candidate.target = values
	}
	var err error
	config.EnableFreeModelPreConsume, err = modelPolicyBool(
		options, EnableFreeModelPreConsumeOption, config.EnableFreeModelPreConsume,
	)
	if err != nil {
		return UsageRatioSetting{}, err
	}
	return config, nil
}

func validateUsageRatioMap(option string, values map[string]float64) error {
	if values == nil || len(values) > maxUsageRatioEntries {
		return fmt.Errorf("%s must be a JSON object with at most %d entries", option, maxUsageRatioEntries)
	}
	for model, ratio := range values {
		if !validModelPolicyIdentifier(model) || ratio < 0 || ratio > maxUsageRatioValue ||
			math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return fmt.Errorf("%s contains an invalid ratio for %q", option, model)
		}
	}
	return nil
}

// GetUsageRatioSetting returns a detached copy of the current policy.
func GetUsageRatioSetting() UsageRatioSetting {
	config := usageRatioConfig.Load()
	if config == nil {
		fallback := defaultUsageRatioSetting()
		return cloneUsageRatioSetting(fallback)
	}
	return cloneUsageRatioSetting(*config)
}

func cloneUsageRatioSetting(config UsageRatioSetting) UsageRatioSetting {
	copyOf := config
	copyOf.CacheRatio = cloneUsageRatioMap(config.CacheRatio)
	copyOf.CreateCacheRatio = cloneUsageRatioMap(config.CreateCacheRatio)
	copyOf.ImageRatio = cloneUsageRatioMap(config.ImageRatio)
	copyOf.AudioRatio = cloneUsageRatioMap(config.AudioRatio)
	copyOf.AudioCompletionRatio = cloneUsageRatioMap(config.AudioCompletionRatio)
	return copyOf
}

func cloneUsageRatioMap(source map[string]float64) map[string]float64 {
	copyOf := make(map[string]float64, len(source))
	for key, value := range source {
		copyOf[key] = value
	}
	return copyOf
}

// GetUsageRatioPolicy resolves all auxiliary ratios from a single immutable
// snapshot. Cache read/write and image ratios use exact model keys. Audio
// ratios follow the reference model-name normalizer for parameterized Gemini
// thinking models and Gizmo aliases.
func GetUsageRatioPolicy(model string) UsageRatioPolicy {
	config := usageRatioConfig.Load()
	if config == nil {
		fallback := defaultUsageRatioSetting()
		config = &fallback
	}
	cache := usageRatioValue(config.CacheRatio, model, defaultCacheRatio)
	cacheCreation := usageRatioValue(config.CreateCacheRatio, model, defaultCreateCacheRatio)
	image := usageRatioValue(config.ImageRatio, model, defaultImageRatio)
	audioModel := usageRatioMatchingModelName(model)
	audio, audioConfigured := config.AudioRatio[audioModel]
	if !audioConfigured {
		audio = defaultAudioRatio
	}
	audioCompletion, audioCompletionConfigured := config.AudioCompletionRatio[audioModel]
	if !audioCompletionConfigured {
		audioCompletion = defaultAudioCompletionRatio
	}
	return UsageRatioPolicy{
		CacheRatio:                   cache,
		CacheCreationRatio:           cacheCreation,
		CacheCreationFiveMinuteRatio: cacheCreation,
		CacheCreationOneHourRatio:    cacheCreation * claudeCacheCreationOneHourMultiplier,
		ImageRatio:                   image,
		AudioRatio:                   audio,
		AudioCompletionRatio:         audioCompletion,
		AudioRatioConfigured:         audioConfigured,
		AudioCompletionConfigured:    audioCompletionConfigured,
		EnableFreeModelPreConsume:    config.EnableFreeModelPreConsume,
	}
}

func usageRatioValue(values map[string]float64, model string, fallback float64) float64 {
	if ratio, found := values[model]; found {
		return ratio
	}
	return fallback
}

func usageRatioMatchingModelName(model string) string {
	switch {
	case strings.HasPrefix(model, "gemini-2.5-flash-lite") && strings.Contains(model, "-thinking-"):
		model = "gemini-2.5-flash-lite-thinking-*"
	case strings.HasPrefix(model, "gemini-2.5-flash") && strings.Contains(model, "-thinking-"):
		model = "gemini-2.5-flash-thinking-*"
	case strings.HasPrefix(model, "gemini-2.5-pro") && strings.Contains(model, "-thinking-"):
		model = "gemini-2.5-pro-thinking-*"
	}
	if strings.HasPrefix(model, "gpt-4-gizmo") {
		return "gpt-4-gizmo-*"
	}
	if strings.HasPrefix(model, "gpt-4o-gizmo") {
		return "gpt-4o-gizmo-*"
	}
	return model
}
