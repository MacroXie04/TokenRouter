package billing

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"math"
	"strings"
)

const (
	maxSpecialRatioUserGroups  = 256
	maxSpecialRatiosPerGroup   = 256
	maxSpecialRatioEntries     = 4096
	maxPricingGroupNameBytes   = 128
	maxSpecialRatioOptionBytes = 1 << 20
)

// ResetModelPricingDefaults atomically restores TokenRouter's live price
// registry and the compatibility ModelRatio option, then swaps the runtime
// cache after the database commit.
func ResetModelPricingDefaults() error {
	prices := DefaultModelPriceRegistry()
	ratios := DefaultModelRatioRegistry()
	priceJSON, err := jsonutil.Marshal(prices)
	if err != nil {
		return err
	}
	ratioJSON, err := jsonutil.Marshal(ratios)
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

// UpdateGroupGroupRatioOption validates, persists, and immediately publishes
// user-group-specific billing overrides.
func UpdateGroupGroupRatioOption(raw string) error {
	ratios, err := parseGroupGroupRatios(raw)
	if err != nil {
		return err
	}
	if err := setting.UpdateOption(setting.GroupGroupRatioOption, raw); err != nil {
		return err
	}
	SetGroupGroupRatios(ratios)
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
	specialRatios, err := parseGroupGroupRatios(setting.GetOption(setting.GroupGroupRatioOption))
	if err != nil {
		return err
	}
	topUpRatios, err := parseTopUpGroupRatios(setting.GetOption(setting.TopUpGroupRatioOption))
	if err != nil {
		return err
	}
	if err := validateReferencePricingOptions(); err != nil {
		return err
	}
	publishPricingOptions(prices, ratios, specialRatios)
	SetTopUpGroupRatios(topUpRatios)
	return nil
}

func publishPricingOptions(prices map[string]ModelPrice, ratios map[string]float64, specialRatios map[string]map[string]float64) {
	priceCopy := make(map[string]ModelPrice, len(prices))
	for name, price := range prices {
		priceCopy[name] = price
	}
	ratioCopy := make(map[string]float64, len(ratios))
	for group, ratio := range ratios {
		ratioCopy[group] = ratio
	}
	specialCopy := cloneGroupGroupRatios(specialRatios)
	pricingCacheMu.Lock()
	modelPriceRegistryCache = priceCopy
	groupRatiosCache = ratioCopy
	groupGroupRatiosCache = specialCopy
	pricingCacheMu.Unlock()
}

// SyncRuntimeOptions refreshes the node-local settings and pricing caches. It
// must run on every node rather than under a cluster-wide lease.
func SyncRuntimeOptions() error {
	return SyncRuntimeOptionsContext(context.Background())
}

// SyncRuntimeOptionsContext applies cancellation to the database-backed
// snapshot load and publishes pricing only after that coherent load succeeds.
func SyncRuntimeOptionsContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runtime-options context is nil")
	}
	if err := setting.SyncContext(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ReloadPricingOptions()
}

func parseModelPrices(raw string) (map[string]ModelPrice, error) {
	prices := map[string]ModelPrice{}
	if raw == "" {
		return prices, nil
	}
	if err := jsonutil.UnmarshalJsonStr(raw, &prices); err != nil {
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
	ratios := map[string]float64{userssvc.GroupDefault: 1}
	if raw == "" {
		return ratios, nil
	}
	if err := jsonutil.UnmarshalJsonStr(raw, &ratios); err != nil {
		return nil, fmt.Errorf("invalid GroupRatio JSON: %w", err)
	}
	for group, ratio := range ratios {
		if group == "" || ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return nil, fmt.Errorf("invalid ratio for group %q", group)
		}
	}
	return ratios, nil
}

func parseGroupGroupRatios(raw string) (map[string]map[string]float64, error) {
	ratios := map[string]map[string]float64{}
	if strings.TrimSpace(raw) == "" {
		return ratios, nil
	}
	if len(raw) > maxSpecialRatioOptionBytes {
		return nil, fmt.Errorf("GroupGroupRatio exceeds %d bytes", maxSpecialRatioOptionBytes)
	}
	if err := jsonutil.UnmarshalJsonStr(raw, &ratios); err != nil {
		return nil, fmt.Errorf("invalid GroupGroupRatio JSON: %w", err)
	}
	if ratios == nil {
		return nil, fmt.Errorf("GroupGroupRatio must be a JSON object")
	}
	if len(ratios) > maxSpecialRatioUserGroups {
		return nil, fmt.Errorf("GroupGroupRatio contains too many user groups")
	}
	total := 0
	for userGroup, byUsingGroup := range ratios {
		if !validPricingGroupName(userGroup) {
			return nil, fmt.Errorf("invalid user group %q in GroupGroupRatio", userGroup)
		}
		if byUsingGroup == nil {
			return nil, fmt.Errorf("GroupGroupRatio entry for %q must be a JSON object", userGroup)
		}
		if len(byUsingGroup) > maxSpecialRatiosPerGroup {
			return nil, fmt.Errorf("GroupGroupRatio entry for %q contains too many groups", userGroup)
		}
		total += len(byUsingGroup)
		if total > maxSpecialRatioEntries {
			return nil, fmt.Errorf("GroupGroupRatio contains too many entries")
		}
		for usingGroup, ratio := range byUsingGroup {
			if !validPricingGroupName(usingGroup) {
				return nil, fmt.Errorf("invalid using group %q in GroupGroupRatio", usingGroup)
			}
			if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
				return nil, fmt.Errorf("invalid special ratio for %q to %q", userGroup, usingGroup)
			}
		}
	}
	return ratios, nil
}

func validPricingGroupName(group string) bool {
	return group != "" && group == strings.TrimSpace(group) && len(group) <= maxPricingGroupNameBytes
}
