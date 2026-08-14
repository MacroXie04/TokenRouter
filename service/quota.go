package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// ErrInsufficientQuota is returned when pre-consume would overspend.
var ErrInsufficientQuota = errors.New("insufficient quota")

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
	groupRatiosCache        = map[string]float64{}
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
	}
}

// DefaultModelRatioRegistry provides the compatibility ModelRatio option. The
// live billing source remains ModelPrice; ratios are derived from prompt USD
// prices relative to TokenRouter's one-dollar fallback baseline.
func DefaultModelRatioRegistry() map[string]float64 {
	prices := DefaultModelPriceRegistry()
	ratios := make(map[string]float64, len(prices))
	for name, price := range prices {
		ratios[name] = price.Prompt / DefaultModelPromptPriceUSD
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
	if ok && ratio > 0 {
		return ratio
	}
	return 1.0
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

// SetGroupRatios replaces the in-memory group ratio map (startup/reload hook).
func SetGroupRatios(m map[string]float64) {
	copyOfRatios := make(map[string]float64, len(m))
	for group, ratio := range m {
		copyOfRatios[group] = ratio
	}
	pricingCacheMu.Lock()
	groupRatiosCache = copyOfRatios
	pricingCacheMu.Unlock()
}

// ComputeQuota computes quota for a model+group given prompt/completion tokens.
// quota = priceUSD / 1e6 * QuotaPerUnit * groupRatio, with saturation audit.
func ComputeQuota(modelName, group string, promptTokens, completionTokens int) int {
	promptUSD, completionUSD := GetModelPrices(modelName)
	ratio := GroupRatio(group)
	raw := (promptUSD*float64(promptTokens) + completionUSD*float64(completionTokens)) / 1e6 * common.QuotaPerUnit * ratio
	return common.QuotaFromFloat(raw)
}

// ComputeQuotaChecked is ComputeQuota with saturation reporting.
func ComputeQuotaChecked(modelName, group string, promptTokens, completionTokens int) (int, *common.QuotaClamp) {
	promptUSD, completionUSD := GetModelPrices(modelName)
	ratio := GroupRatio(group)
	raw := (promptUSD*float64(promptTokens) + completionUSD*float64(completionTokens)) / 1e6 * common.QuotaPerUnit * ratio
	return common.QuotaFromFloatChecked(raw)
}

// ComputePerCallQuota prices asynchronous provider operations whose configured
// prompt price is USD per call rather than USD per million tokens. The unit
// multiplier must be explicit so duration-based task billing cannot silently
// undercharge, and saturation fails closed instead of billing a clamp.
func ComputePerCallQuota(modelName, group string, units int) (int, error) {
	if units < 1 {
		return 0, errors.New("per-call billing units must be positive")
	}
	priceUSD, _ := GetModelPrices(modelName)
	ratio := GroupRatio(group)
	if math.IsNaN(priceUSD) || math.IsInf(priceUSD, 0) || priceUSD < 0 {
		return 0, fmt.Errorf("invalid per-call price for model %s", modelName)
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
		return 0, fmt.Errorf("invalid group ratio for %s", group)
	}
	raw := decimal.NewFromFloat(priceUSD).
		Mul(decimal.NewFromInt(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(ratio)).
		Mul(decimal.NewFromInt(int64(units)))
	return common.QuotaFromDecimalStrict(raw)
}

// PreConsumeUserQuota atomically deducts quota from the user if sufficient.
func PreConsumeUserQuota(userId int, quota int) error {
	if quota <= 0 {
		return nil
	}
	res := model.DB.Model(&model.User{}).
		Where("id = ? AND quota >= ?", userId, quota).
		UpdateColumn("quota", gormExpr("quota - ?", quota))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInsufficientQuota
	}
	return nil
}

// RefundUserQuota adds quota back to the user (failure rollback / over-deduct).
func RefundUserQuota(userId int, quota int) error {
	if quota <= 0 {
		return nil
	}
	return model.DB.Model(&model.User{}).
		Where("id = ?", userId).
		UpdateColumn("quota", gormExpr("quota + ?", quota)).Error
}

// SettleUserQuota adjusts the user's quota from a pre-consumed reservation to
// the actual usage: refunds the over-reservation, deducts the shortfall, and
// records used_quota + request count exactly once. This is the pre-consume then
// settle accounting pattern; it must never double-charge.
func SettleUserQuota(userId, reserved, actual int) error {
	if actual > reserved {
		// Deduct the shortfall; a concurrent request may have consumed the
		// headroom, in which case the deduction is best-effort (the reservation
		// already guaranteed the estimated amount).
		if err := PreConsumeUserQuota(userId, actual-reserved); err != nil {
			common.SysError("settle: shortfall deduction failed for user " + common.Int2Str(userId))
		}
	} else if actual < reserved {
		if err := RefundUserQuota(userId, reserved-actual); err != nil {
			common.SysError("settle: refund failed for user " + common.Int2Str(userId))
		}
	}
	return RecordUserUsage(userId, actual)
}

// RecordUserUsage adds actual usage to the user's lifetime counters
// (used_quota and request count), regardless of which funding source paid.
func RecordUserUsage(userId, actual int) error {
	if err := model.DB.Model(&model.User{}).Where("id = ?", userId).
		UpdateColumn("used_quota", gormExpr("used_quota + ?", actual)).Error; err != nil {
		return err
	}
	return UpdateUserRequestCount(userId, 1)
}

// PostConsumeTokenQuota increments the token used quota after actual usage.
func PostConsumeTokenQuota(tokenId int, quota int) error {
	return IncreaseTokenUsedQuota(tokenId, quota)
}

// GetUserAutoGroups returns the auto-group candidates a user can attach to a
// token: the configured ratio groups. TokenRouter's group model is flat
// (a single group per user with no group hierarchy), so unlike the reference
// there is no per-group usable-groups filtering — documented as
// KNOWN_DEVIATIONS #12.
func GetUserAutoGroups(userGroup string) []string {
	ratios := getGroupRatios()
	var out []string
	for g := range ratios {
		if g == "" || g == "auto" {
			continue
		}
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}
