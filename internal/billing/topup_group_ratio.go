package billing

import (
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"sync"
)

const (
	maxTopUpGroupRatios     = 256
	maxTopUpGroupRatioBytes = 64 << 10
	maxTopUpGroupRatio      = 1_000_000
)

var (
	topUpGroupRatioMu    sync.RWMutex
	topUpGroupRatioCache = defaultTopUpGroupRatios()
)

func defaultTopUpGroupRatios() map[string]float64 {
	return map[string]float64{userssvc.GroupDefault: 1, "vip": 1, "svip": 1}
}

// TopUpGroupRatioOptionDefault is the deterministic reference-compatible
// default exposed to root operators when no database option exists.
func TopUpGroupRatioOptionDefault() string {
	raw, err := jsonutil.Marshal(defaultTopUpGroupRatios())
	if err != nil {
		return `{"default":1,"svip":1,"vip":1}`
	}
	return string(raw)
}

// UpdateTopUpGroupRatioOption validates, persists, and immediately publishes
// top-up-only multipliers. They are deliberately separate from GroupRatio:
// changing request-routing prices must not unexpectedly change wallet charges.
func UpdateTopUpGroupRatioOption(raw string) error {
	ratios, err := parseTopUpGroupRatios(raw)
	if err != nil {
		return err
	}
	if err := setting.UpdateOption(setting.TopUpGroupRatioOption, raw); err != nil {
		return err
	}
	SetTopUpGroupRatios(ratios)
	return nil
}

func parseTopUpGroupRatios(raw string) (map[string]float64, error) {
	return setting.ParseTopUpGroupRatioOption(raw)
}

func getTopUpGroupRatios() map[string]float64 {
	topUpGroupRatioMu.RLock()
	defer topUpGroupRatioMu.RUnlock()
	result := make(map[string]float64, len(topUpGroupRatioCache))
	for group, ratio := range topUpGroupRatioCache {
		result[group] = ratio
	}
	return result
}

// SetTopUpGroupRatios replaces the validated live registry. It is also a
// narrow test hook, mirroring SetGroupRatios.
func SetTopUpGroupRatios(ratios map[string]float64) {
	if len(ratios) == 0 {
		ratios = defaultTopUpGroupRatios()
	}
	copyOfRatios := make(map[string]float64, len(ratios))
	for group, ratio := range ratios {
		copyOfRatios[group] = ratio
	}
	topUpGroupRatioMu.Lock()
	topUpGroupRatioCache = copyOfRatios
	topUpGroupRatioMu.Unlock()
}

func validatedTopUpGroupRatio(group string) (float64, error) {
	ratio := 1.0
	topUpGroupRatioMu.RLock()
	if configured, ok := topUpGroupRatioCache[group]; ok {
		ratio = configured
	}
	topUpGroupRatioMu.RUnlock()
	if !finitePositiveMoney(ratio) || ratio > maxTopUpGroupRatio {
		return 0, ErrTopUpPricingInvalid
	}
	return ratio, nil
}
