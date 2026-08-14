package service

import (
	"fmt"
	"math"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

// ResetModelPricingDefaults atomically restores TokenRouter's live price
// registry and the compatibility ModelRatio option, then swaps the runtime
// cache after the database commit.
func ResetModelPricingDefaults() error {
	prices := DefaultModelPriceRegistry()
	ratios := DefaultModelRatioRegistry()
	priceJSON, err := common.Marshal(prices)
	if err != nil {
		return err
	}
	ratioJSON, err := common.Marshal(ratios)
	if err != nil {
		return err
	}
	if err := setting.UpdateOptions(map[string]string{
		setting.ModelPriceOption: string(priceJSON),
		setting.ModelRatioOption: string(ratioJSON),
	}); err != nil {
		return err
	}
	SetModelPriceRegistry(prices)
	return nil
}

// UpdateModelPriceOption validates, persists, and immediately publishes a
// custom USD-per-million registry.
func UpdateModelPriceOption(raw string) error {
	prices, err := parseModelPrices(raw)
	if err != nil {
		return err
	}
	if err := setting.UpdateOption(setting.ModelPriceOption, raw); err != nil {
		return err
	}
	SetModelPriceRegistry(prices)
	return nil
}

// UpdateGroupRatioOption validates, persists, and immediately publishes group
// multipliers so option edits never require a process restart.
func UpdateGroupRatioOption(raw string) error {
	ratios, err := parseGroupRatios(raw)
	if err != nil {
		return err
	}
	if err := setting.UpdateOption(setting.GroupRatioOption, raw); err != nil {
		return err
	}
	SetGroupRatios(ratios)
	return nil
}

// ReloadPricingOptions validates a coherent snapshot of pricing settings before
// replacing either live registry.
func ReloadPricingOptions() error {
	prices, err := parseModelPrices(setting.GetOption(setting.ModelPriceOption))
	if err != nil {
		return err
	}
	ratios, err := parseGroupRatios(setting.GetOption(setting.GroupRatioOption))
	if err != nil {
		return err
	}
	SetModelPriceRegistry(prices)
	SetGroupRatios(ratios)
	return nil
}

// SyncRuntimeOptions refreshes the node-local settings and pricing caches. It
// must run on every node rather than under a cluster-wide lease.
func SyncRuntimeOptions() error {
	if err := setting.Sync(); err != nil {
		return err
	}
	return ReloadPricingOptions()
}

func parseModelPrices(raw string) (map[string]ModelPrice, error) {
	prices := map[string]ModelPrice{}
	if raw == "" {
		return prices, nil
	}
	if err := common.UnmarshalJsonStr(raw, &prices); err != nil {
		return nil, fmt.Errorf("invalid ModelPrice JSON: %w", err)
	}
	for model, price := range prices {
		if model == "" || price.Prompt < 0 || price.Completion < 0 ||
			math.IsNaN(price.Prompt) || math.IsNaN(price.Completion) ||
			math.IsInf(price.Prompt, 0) || math.IsInf(price.Completion, 0) {
			return nil, fmt.Errorf("invalid price for model %q", model)
		}
	}
	return prices, nil
}

func parseGroupRatios(raw string) (map[string]float64, error) {
	ratios := map[string]float64{}
	if raw == "" {
		return ratios, nil
	}
	if err := common.UnmarshalJsonStr(raw, &ratios); err != nil {
		return nil, fmt.Errorf("invalid GroupRatio JSON: %w", err)
	}
	for group, ratio := range ratios {
		if group == "" || ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return nil, fmt.Errorf("invalid ratio for group %q", group)
		}
	}
	return ratios, nil
}
