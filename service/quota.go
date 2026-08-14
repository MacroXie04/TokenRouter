package service

import (
	"errors"

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
	registry := getModelPriceRegistry()
	if p, ok := registry[modelName]; ok {
		return p.Prompt, p.Completion
	}
	return DefaultModelPromptPriceUSD, DefaultModelCompletionPriceUSD
}

// ModelPrice is a USD price per 1M tokens for a single model.
type ModelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
}

var modelPriceRegistryCache map[string]ModelPrice

func getModelPriceRegistry() map[string]ModelPrice {
	if modelPriceRegistryCache == nil {
		modelPriceRegistryCache = map[string]ModelPrice{}
	}
	return modelPriceRegistryCache
}

// ExportedModelPrices returns a copy of the price registry for API responses.
func ExportedModelPrices() map[string]ModelPrice {
	out := make(map[string]ModelPrice, len(getModelPriceRegistry()))
	for k, v := range getModelPriceRegistry() {
		out[k] = v
	}
	return out
}

// SetModelPriceRegistry replaces the in-memory price registry (used on startup
// and hot-reload; also a test hook).
func SetModelPriceRegistry(m map[string]ModelPrice) {
	modelPriceRegistryCache = m
}

// GroupRatio returns the billing ratio for a group (default 1.0).
func GroupRatio(group string) float64 {
	if group == "" {
		return 1.0
	}
	ratios := getGroupRatios()
	if r, ok := ratios[group]; ok && r > 0 {
		return r
	}
	return 1.0
}

var groupRatiosCache = map[string]float64{}

func getGroupRatios() map[string]float64 {
	return groupRatiosCache
}

// ExportedGroupRatios returns a copy of the group ratio map for API responses.
func ExportedGroupRatios() map[string]float64 {
	out := make(map[string]float64, len(groupRatiosCache))
	for k, v := range groupRatiosCache {
		out[k] = v
	}
	return out
}

// SetGroupRatios replaces the in-memory group ratio map (startup/reload hook).
func SetGroupRatios(m map[string]float64) {
	groupRatiosCache = m
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
