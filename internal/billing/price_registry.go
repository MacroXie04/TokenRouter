package billing

import (
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"math"
	"sync"
)

// DefaultModelPromptPriceUSD and DefaultModelCompletionPriceUSD are the fallback
// USD prices per 1M tokens used when a model has no configured price. They keep
// the gateway usable out of the box; administrators set real prices via the
// model price registry.
const (
	DefaultModelPromptPriceUSD     = 1.0
	DefaultModelCompletionPriceUSD = 3.0
)

// GetModelPrices returns prompt/completion USD prices per 1M tokens for a model.
// Configured prices come from the ModelPrice option (a JSON map); unconfigured
// models fall back to defaults.
func GetModelPrices(modelName string) (promptUSD, completionUSD float64) {
	pricingCacheMu.RLock()
	price, ok := modelPriceRegistryCache[modelName]
	pricingCacheMu.RUnlock()
	if ok {
		return price.Prompt, price.Completion
	}
	return DefaultModelPromptPriceUSD, DefaultModelCompletionPriceUSD
}

// ModelPrice is a USD price per 1M tokens for a single model.
type ModelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
}

var (
	pricingCacheMu          sync.RWMutex
	modelPriceRegistryCache = map[string]ModelPrice{}
	groupRatiosCache        = map[string]float64{userssvc.GroupDefault: 1}
	groupGroupRatiosCache   = map[string]map[string]float64{}
)

func getModelPriceRegistry() map[string]ModelPrice {
	pricingCacheMu.RLock()
	defer pricingCacheMu.RUnlock()
	out := make(map[string]ModelPrice, len(modelPriceRegistryCache))
	for name, price := range modelPriceRegistryCache {
		out[name] = price
	}
	return out
}

// ExportedModelPrices returns a copy of the price registry for API responses.
func ExportedModelPrices() map[string]ModelPrice {
	return getModelPriceRegistry()
}

// SetModelPriceRegistry replaces the in-memory price registry (used on startup
// and hot-reload; also a test hook).
func SetModelPriceRegistry(m map[string]ModelPrice) {
	copyOfRegistry := make(map[string]ModelPrice, len(m))
	for name, price := range m {
		copyOfRegistry[name] = price
	}
	pricingCacheMu.Lock()
	modelPriceRegistryCache = copyOfRegistry
	pricingCacheMu.Unlock()
}

// DefaultModelPriceRegistry returns TokenRouter's built-in USD-per-million
// baseline. Resetting pricing uses this explicit registry rather than a fixed
// success response, so persistence and live billing change together.
func DefaultModelPriceRegistry() map[string]ModelPrice {
	return map[string]ModelPrice{
		"gpt-4o":                {Prompt: 2.5, Completion: 10},
		"gpt-4o-mini":           {Prompt: 0.15, Completion: 0.6},
		"gpt-4.1":               {Prompt: 2, Completion: 8},
		"gpt-4.1-mini":          {Prompt: 0.4, Completion: 1.6},
		"gpt-4.1-nano":          {Prompt: 0.1, Completion: 0.4},
		"o3":                    {Prompt: 2, Completion: 8},
		"o4-mini":               {Prompt: 1.1, Completion: 4.4},
		"claude-sonnet-4":       {Prompt: 3, Completion: 15},
		"claude-opus-4":         {Prompt: 15, Completion: 75},
		"claude-3-5-haiku":      {Prompt: 0.8, Completion: 4},
		"gemini-2.5-pro":        {Prompt: 1.25, Completion: 10},
		"gemini-2.5-flash":      {Prompt: 0.3, Completion: 2.5},
		"gemini-2.5-flash-lite": {Prompt: 0.1, Completion: 0.4},
		// Sora task prices are USD per generated second. Task billing uses
		// ComputePerCallQuotaMultiplierForUser rather than token conversion.
		"sora-2":     {Prompt: 0.3},
		"sora-2-pro": {Prompt: 0.5},
	}
}

// DefaultModelRatioRegistry provides the reference-compatible ModelRatio
// option. One ratio unit is one quota per token, so a USD-per-million prompt
// price converts through QuotaPerUnit. The values become a live billing source
// only for models explicitly placed in reference mode.
func DefaultModelRatioRegistry() map[string]float64 {
	prices := DefaultModelPriceRegistry()
	ratios := make(map[string]float64, len(prices))
	for name, price := range prices {
		ratios[name] = price.Prompt * float64(quotamath.QuotaPerUnit) / 1_000_000
	}
	return ratios
}

// GroupRatio returns the billing ratio for a group (default 1.0).
func GroupRatio(group string) float64 {
	if group == "" {
		return 1.0
	}
	pricingCacheMu.RLock()
	ratio, ok := groupRatiosCache[group]
	pricingCacheMu.RUnlock()
	if ok && ratio > 0 && !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
		return ratio
	}
	return 1.0
}

// EffectiveGroupRatio returns the ratio charged when a user in userGroup is
// routed through usingGroup. An explicit user-group override wins even when it
// is zero (a deliberately free route); otherwise the using group's base ratio
// applies. The boolean reports whether an override was selected.
func EffectiveGroupRatio(userGroup, usingGroup string) (float64, bool) {
	if userGroup != "" {
		pricingCacheMu.RLock()
		byUsingGroup, exists := groupGroupRatiosCache[userGroup]
		ratio, overridden := byUsingGroup[usingGroup]
		pricingCacheMu.RUnlock()
		if exists && overridden && ratio >= 0 && !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
			return ratio, true
		}
	}
	return GroupRatio(usingGroup), false
}

func getGroupRatios() map[string]float64 {
	pricingCacheMu.RLock()
	defer pricingCacheMu.RUnlock()
	out := make(map[string]float64, len(groupRatiosCache))
	for group, ratio := range groupRatiosCache {
		out[group] = ratio
	}
	return out
}

// ExportedGroupRatios returns a copy of the group ratio map for API responses.
func ExportedGroupRatios() map[string]float64 {
	return getGroupRatios()
}

// ExportedGroupGroupRatios returns an isolated copy of the user-group to
// channel-group override registry.
func ExportedGroupGroupRatios() map[string]map[string]float64 {
	pricingCacheMu.RLock()
	defer pricingCacheMu.RUnlock()
	return cloneGroupGroupRatios(groupGroupRatiosCache)
}

// SetGroupRatios replaces the in-memory group ratio map (startup/reload hook).
func SetGroupRatios(m map[string]float64) {
	if len(m) == 0 {
		m = map[string]float64{userssvc.GroupDefault: 1}
	}
	copyOfRatios := make(map[string]float64, len(m))
	for group, ratio := range m {
		copyOfRatios[group] = ratio
	}
	pricingCacheMu.Lock()
	groupRatiosCache = copyOfRatios
	pricingCacheMu.Unlock()
}

// SetGroupGroupRatios replaces the special ratio registry. Callers must pass a
// validated map; a deep copy prevents later mutation of the live snapshot.
func SetGroupGroupRatios(m map[string]map[string]float64) {
	copyOfRatios := cloneGroupGroupRatios(m)
	pricingCacheMu.Lock()
	groupGroupRatiosCache = copyOfRatios
	pricingCacheMu.Unlock()
}

func cloneGroupGroupRatios(source map[string]map[string]float64) map[string]map[string]float64 {
	result := make(map[string]map[string]float64, len(source))
	for userGroup, byUsingGroup := range source {
		copyOfGroup := make(map[string]float64, len(byUsingGroup))
		for usingGroup, ratio := range byUsingGroup {
			copyOfGroup[usingGroup] = ratio
		}
		result[userGroup] = copyOfGroup
	}
	return result
}
