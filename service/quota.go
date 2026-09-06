package service

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// ErrInsufficientQuota is returned when pre-consume would overspend.
var ErrInsufficientQuota = errors.New("insufficient quota")

// ErrInvalidQuota rejects negative or out-of-policy accounting inputs before
// they can turn a debit into a credit or overflow a database quota column.
var ErrInvalidQuota = errors.New("invalid quota")

// ErrUserUsageOverflow reports that lifetime usage/request counters cannot be
// incremented without exceeding the platform's persisted quota bounds.
var ErrUserUsageOverflow = errors.New("user usage overflow")

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
	groupRatiosCache        = map[string]float64{GroupDefault: 1}
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
		ratios[name] = price.Prompt * float64(common.QuotaPerUnit) / 1_000_000
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
		m = map[string]float64{GroupDefault: 1}
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

// ComputeQuota computes quota for a model+group given prompt/completion tokens.
// quota = priceUSD / 1e6 * QuotaPerUnit * groupRatio, with saturation audit.
func ComputeQuota(modelName, group string, promptTokens, completionTokens int) int {
	return ComputeQuotaForUser(modelName, "", group, promptTokens, completionTokens)
}

// ComputeQuotaForUser applies any configured user-group override before
// converting the model's USD price to quota.
func ComputeQuotaForUser(modelName, userGroup, usingGroup string, promptTokens, completionTokens int) int {
	promptUSD, completionUSD := GetModelPrices(modelName)
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	raw := (promptUSD*float64(promptTokens) + completionUSD*float64(completionTokens)) / 1e6 * common.QuotaPerUnit * ratio
	return common.QuotaFromFloat(raw)
}

// ComputeQuotaChecked is ComputeQuota with saturation reporting.
func ComputeQuotaChecked(modelName, group string, promptTokens, completionTokens int) (int, *common.QuotaClamp) {
	return ComputeQuotaForUserChecked(modelName, "", group, promptTokens, completionTokens)
}

// ComputeQuotaForUserChecked is ComputeQuotaForUser with saturation reporting.
func ComputeQuotaForUserChecked(modelName, userGroup, usingGroup string, promptTokens, completionTokens int) (int, *common.QuotaClamp) {
	promptUSD, completionUSD := GetModelPrices(modelName)
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	raw := (promptUSD*float64(promptTokens) + completionUSD*float64(completionTokens)) / 1e6 * common.QuotaPerUnit * ratio
	return common.QuotaFromFloatChecked(raw)
}

// ComputePerCallQuota prices asynchronous provider operations whose configured
// prompt price is USD per call rather than USD per million tokens. The unit
// multiplier must be explicit so duration-based task billing cannot silently
// undercharge, and saturation fails closed instead of billing a clamp.
func ComputePerCallQuota(modelName, group string, units int) (int, error) {
	return ComputePerCallQuotaForUser(modelName, "", group, units)
}

// ComputePerCallQuotaForUser applies the same user-group override semantics to
// asynchronous per-call work as ordinary token-priced relays.
func ComputePerCallQuotaForUser(modelName, userGroup, usingGroup string, units int) (int, error) {
	if units < 1 {
		return 0, errors.New("per-call billing units must be positive")
	}
	return ComputePerCallQuotaMultiplierForUser(
		modelName, userGroup, usingGroup, decimal.NewFromInt(int64(units)),
	)
}

// ComputePerCallQuotaMultiplierForUser prices task work with an exact decimal
// multiplier (for example seconds × a resolution factor). Decimal input keeps
// a provider's fractional ratio deterministic and strict quota conversion
// fails closed on overflow.
func ComputePerCallQuotaMultiplierForUser(
	modelName, userGroup, usingGroup string,
	multiplier decimal.Decimal,
) (int, error) {
	if multiplier.LessThanOrEqual(decimal.Zero) {
		return 0, errors.New("per-call billing multiplier must be positive")
	}
	priceUSD, _ := GetModelPrices(modelName)
	ratio, _ := EffectiveGroupRatio(userGroup, usingGroup)
	if math.IsNaN(priceUSD) || math.IsInf(priceUSD, 0) || priceUSD < 0 {
		return 0, fmt.Errorf("invalid per-call price for model %s", modelName)
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return 0, fmt.Errorf("invalid group ratio for %s", usingGroup)
	}
	raw := decimal.NewFromFloat(priceUSD).
		Mul(decimal.NewFromInt(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(ratio)).
		Mul(multiplier)
	return common.QuotaFromDecimalStrict(raw)
}

// PreConsumeUserQuota atomically deducts quota from the user if sufficient.
func PreConsumeUserQuota(userId int, quota int) error {
	if userId <= 0 {
		return ErrUserNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	res := model.DB.Model(&model.User{}).
		Where("id = ? AND quota >= ? AND quota <= ?", userId, quota, common.MaxQuota).
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
	if userId <= 0 {
		return ErrUserNotFound
	}
	if err := validateQuotaAmount(quota); err != nil {
		return err
	}
	if quota == 0 {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "quota").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		newQuota, ok := common.AddQuotaWithinBounds(user.Quota, quota)
		if !ok {
			return ErrUserQuotaOverflow
		}
		result := tx.Model(&model.User{}).Where("id = ? AND quota = ?", userId, user.Quota).
			UpdateColumn("quota", newQuota)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserNotFound
		}
		return nil
	})
}

// SettleUserQuota adjusts the user's quota from a pre-consumed reservation to
// the actual usage: refunds the over-reservation, deducts the shortfall, and
// records used_quota + request count exactly once. This is the pre-consume then
// settle accounting pattern; it must never double-charge.
func SettleUserQuota(userId, reserved, actual int) error {
	if userId <= 0 {
		return ErrUserNotFound
	}
	if err := validateQuotaAmount(reserved); err != nil {
		return fmt.Errorf("reserved quota: %w", err)
	}
	if err := validateQuotaAmount(actual); err != nil {
		return fmt.Errorf("actual quota: %w", err)
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := subscriptionLockForUpdate(tx).
			Select("id", "quota", "used_quota", "request_count").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}

		if !common.QuotaWithinBounds(user.Quota) {
			return ErrUserQuotaOverflow
		}
		newQuota := user.Quota
		switch {
		case reserved > actual:
			var ok bool
			newQuota, ok = common.AddQuotaWithinBounds(user.Quota, reserved-actual)
			if !ok {
				return ErrUserQuotaOverflow
			}
		case actual > reserved:
			debit := actual - reserved
			if user.Quota < debit {
				return ErrInsufficientQuota
			}
			newQuota = user.Quota - debit
		}
		newUsedQuota, usageOK := common.AddQuotaWithinBounds(user.UsedQuota, actual)
		newRequestCount, countOK := common.AddQuotaWithinBounds(user.RequestCount, 1)
		if !usageOK || !countOK {
			return ErrUserUsageOverflow
		}

		updates := map[string]any{
			"quota":         newQuota,
			"used_quota":    newUsedQuota,
			"request_count": newRequestCount,
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND quota = ? AND used_quota = ? AND request_count = ?",
				userId, user.Quota, user.UsedQuota, user.RequestCount).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserNotFound
		}
		return nil
	})
}

// RecordUserUsage adds actual usage to the user's lifetime counters
// (used_quota and request count), regardless of which funding source paid.
func RecordUserUsage(userId, actual int) error {
	if userId <= 0 {
		return ErrUserNotFound
	}
	if err := validateQuotaAmount(actual); err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := subscriptionLockForUpdate(tx).
			Select("id", "used_quota", "request_count").First(&user, userId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUserNotFound
			}
			return err
		}
		newUsedQuota, usageOK := common.AddQuotaWithinBounds(user.UsedQuota, actual)
		newRequestCount, countOK := common.AddQuotaWithinBounds(user.RequestCount, 1)
		if !usageOK || !countOK {
			return ErrUserUsageOverflow
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND used_quota = ? AND request_count = ?", userId, user.UsedQuota, user.RequestCount).
			Updates(map[string]any{
				"used_quota":    newUsedQuota,
				"request_count": newRequestCount,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserNotFound
		}
		return nil
	})
}

func validateQuotaAmount(quota int) error {
	if quota < 0 || int64(quota) > common.MaxQuota {
		return fmt.Errorf("%w: %d", ErrInvalidQuota, quota)
	}
	return nil
}

// PostConsumeTokenQuota increments the token used quota after actual usage.
func PostConsumeTokenQuota(tokenId int, quota int) error {
	return IncreaseTokenUsedQuota(tokenId, quota)
}
