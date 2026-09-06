package billing

import (
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"math"
	"strconv"
	"strings"
	"sync"
)

const (
	// BillingModeReference opts an ordinary relay into the reference gateway's
	// ModelPrice-or-ModelRatio billing contract. Other relay families retain
	// TokenRouter's existing pricing behavior.
	BillingModeReference = "reference"

	referenceDefaultPreConsumedQuota = 500
	referencePricingMaxEntries       = 20_000
	referencePricingMaxOptionBytes   = 1 << 20
	referencePricingMaxModelBytes    = 256
	referencePricingMaxValue         = 1e12
	referenceUnsetModelRatio         = 37.5
)

var ErrReferencePricingConfiguration = errors.New("invalid reference pricing configuration")

type referencePricingRaw struct {
	fixedPrice                string
	modelRatio                string
	completionRatio           string
	completionRatioConfigured bool
}

type referencePricingMaps struct {
	fixedPrice      map[string]float64
	modelRatio      map[string]float64
	completionRatio map[string]float64
}

// FilterModelsByReferencePricing applies the same user preference used by the
// request-time resolver to model catalogs. Normal TokenRouter billing modes
// remain visible; only explicitly reference-priced models without either a
// fixed price or ratio are narrowed when the owner has not opted in.
func FilterModelsByReferencePricing(models map[string]bool, acceptUnset bool) (map[string]bool, error) {
	result := make(map[string]bool, len(models))
	options := setting.GetOptions(
		setting.ModelBillingModeOption,
		setting.PerCallModelPriceOption,
		setting.ModelRatioOption,
		setting.CompletionRatioOption,
	)
	modes, err := parseReferenceBillingModes(options[setting.ModelBillingModeOption])
	if err != nil {
		return nil, err
	}
	hasReferenceModel := false
	for modelName := range models {
		if modes[modelName] == BillingModeReference {
			hasReferenceModel = true
			break
		}
	}
	if !hasReferenceModel {
		for modelName, enabled := range models {
			result[modelName] = enabled
		}
		return result, nil
	}
	maps, err := loadReferencePricingMaps(referencePricingRaw{
		fixedPrice:                options[setting.PerCallModelPriceOption],
		modelRatio:                options[setting.ModelRatioOption],
		completionRatio:           options[setting.CompletionRatioOption],
		completionRatioConfigured: optionConfigured(options, setting.CompletionRatioOption),
	})
	if err != nil {
		return nil, err
	}
	for modelName, enabled := range models {
		if modes[modelName] != BillingModeReference {
			result[modelName] = enabled
			continue
		}
		pricingName := referenceMatchingModelName(modelName)
		_, fixed := maps.fixedPrice[pricingName]
		_, ratio := maps.modelRatio[pricingName]
		if fixed || ratio || acceptUnset {
			result[modelName] = enabled
		}
	}
	return result, nil
}

var referencePricingCache = struct {
	sync.RWMutex
	initialized bool
	raw         referencePricingRaw
	maps        referencePricingMaps
	err         error
}{}

// ReferenceBillingPlan is the immutable price snapshot used by one ordinary
// relay in one selected group. Capturing it before dispatch prevents a hot
// reload from changing an in-flight request's final charge or audit metadata.
type ReferenceBillingPlan struct {
	modelName          string
	groupRatio         float64
	groupRatioSpecial  bool
	useFixedPrice      bool
	fixedPrice         float64
	modelRatio         float64
	completionRatio    float64
	preConsumedMinimum int
	usageRatioPolicy   setting.UsageRatioPolicy
	freeModel          bool
}

// ReferenceAsyncTaskBillingPlan is the JSON-safe immutable pricing snapshot
// for asynchronous providers whose authoritative unit count is not known
// until polling completes. Decimal strings preserve the exact selected
// ratios across process restarts and settings reloads.
type ReferenceAsyncTaskBillingPlan struct {
	Version           int    `json:"version"`
	ModelName         string `json:"model_name"`
	GroupRatio        string `json:"group_ratio"`
	GroupRatioSpecial bool   `json:"group_ratio_special,omitempty"`
	UseFixedPrice     bool   `json:"use_fixed_price"`
	FixedPrice        string `json:"fixed_price,omitempty"`
	ModelRatio        string `json:"model_ratio,omitempty"`
	FreeModel         bool   `json:"free_model,omitempty"`
}

// AsyncTaskBillingPlan returns a durable task-pricing snapshot. It is kept
// separate from the ordinary token pricing methods because the reference
// async contract pre-authorizes half a quota unit for ratio models and then
// settles provider-reported whole units without CompletionRatio or rounding.
func (plan ReferenceBillingPlan) AsyncTaskBillingPlan() (ReferenceAsyncTaskBillingPlan, error) {
	snapshot := ReferenceAsyncTaskBillingPlan{
		Version: 1, ModelName: plan.modelName,
		GroupRatio:        decimal.NewFromFloat(plan.groupRatio).String(),
		GroupRatioSpecial: plan.groupRatioSpecial,
		UseFixedPrice:     plan.useFixedPrice,
		FreeModel:         plan.freeModel,
	}
	if plan.useFixedPrice {
		snapshot.FixedPrice = decimal.NewFromFloat(plan.fixedPrice).String()
	} else {
		snapshot.ModelRatio = decimal.NewFromFloat(plan.modelRatio).String()
	}
	if err := snapshot.Validate(); err != nil {
		return ReferenceAsyncTaskBillingPlan{}, err
	}
	return snapshot, nil
}

// ResolveReferenceAsyncTaskBillingPlan resolves the existing reference-mode
// model and group settings and freezes them for one asynchronous task.
func ResolveReferenceAsyncTaskBillingPlan(modelName, userGroup, usingGroup string) (ReferenceAsyncTaskBillingPlan, bool, error) {
	plan, enabled, err := ResolveOrdinaryReferenceBillingPlan(modelName, userGroup, usingGroup)
	return referenceAsyncTaskBillingPlan(plan, enabled, err)
}

// ResolveReferenceAsyncTaskBillingPlanForUser applies the owner's explicit
// accept-unpriced-model preference when an opted-in reference model has no
// configured fixed price or ratio. The selected fallback is frozen into the
// durable task snapshot before provider dispatch.
func ResolveReferenceAsyncTaskBillingPlanForUser(userID int, modelName, userGroup, usingGroup string) (ReferenceAsyncTaskBillingPlan, bool, error) {
	plan, enabled, err := ResolveOrdinaryReferenceBillingPlanForUser(userID, modelName, userGroup, usingGroup)
	return referenceAsyncTaskBillingPlan(plan, enabled, err)
}

func referenceAsyncTaskBillingPlan(plan ReferenceBillingPlan, enabled bool, err error) (ReferenceAsyncTaskBillingPlan, bool, error) {
	if err != nil || !enabled {
		return ReferenceAsyncTaskBillingPlan{}, enabled, err
	}
	snapshot, err := plan.AsyncTaskBillingPlan()
	return snapshot, true, err
}

// Validate verifies an async pricing snapshot loaded from durable metadata.
func (plan ReferenceAsyncTaskBillingPlan) Validate() error {
	if plan.Version != 1 || !validReferencePricingModel(plan.ModelName) {
		return fmt.Errorf("%w: invalid async task pricing identity", ErrReferencePricingConfiguration)
	}
	group, err := parseReferenceAsyncDecimal("group ratio", plan.GroupRatio)
	if err != nil || group.GreaterThan(decimal.NewFromFloat(referencePricingMaxValue)) {
		return fmt.Errorf("%w: invalid async task group ratio", ErrReferencePricingConfiguration)
	}
	if plan.UseFixedPrice {
		fixed, fixedErr := parseReferenceAsyncDecimal("fixed price", plan.FixedPrice)
		if fixedErr != nil || fixed.GreaterThan(decimal.NewFromFloat(referencePricingMaxValue)) || plan.ModelRatio != "" ||
			(plan.FreeModel && !group.IsZero() && !fixed.IsZero()) {
			return fmt.Errorf("%w: invalid async task fixed price", ErrReferencePricingConfiguration)
		}
		return nil
	}
	ratio, ratioErr := parseReferenceAsyncDecimal("model ratio", plan.ModelRatio)
	if ratioErr != nil || ratio.GreaterThan(decimal.NewFromFloat(referencePricingMaxValue)) || plan.FixedPrice != "" ||
		(plan.FreeModel && !group.IsZero() && !ratio.IsZero()) {
		return fmt.Errorf("%w: invalid async task model ratio", ErrReferencePricingConfiguration)
	}
	return nil
}

// PreConsumeQuota returns the reference task hold. Fixed-price tasks hold the
// complete per-call price. Ratio tasks hold one half of QuotaPerUnit.
func (plan ReferenceAsyncTaskBillingPlan) PreConsumeQuota() (int, error) {
	if err := plan.Validate(); err != nil {
		return 0, err
	}
	group, _ := decimal.NewFromString(plan.GroupRatio)
	if plan.FreeModel {
		return 0, nil
	}
	if plan.UseFixedPrice {
		fixed, _ := decimal.NewFromString(plan.FixedPrice)
		return strictReferenceQuota(fixed.Mul(decimal.NewFromInt(quotamath.QuotaPerUnit)).Mul(group), false)
	}
	ratio, _ := decimal.NewFromString(plan.ModelRatio)
	return strictReferenceQuota(
		ratio.Mul(decimal.NewFromInt(quotamath.QuotaPerUnit)).Div(decimal.NewFromInt(2)).Mul(group),
		false,
	)
}

// SettlementQuota returns the final task charge. A missing/zero provider unit
// count keeps the conservative precharge, matching the reference poller.
// Positive CompletionUnits have already been ceiled by the provider codec.
func (plan ReferenceAsyncTaskBillingPlan) SettlementQuota(completionUnits int) (int, error) {
	if err := plan.Validate(); err != nil {
		return 0, err
	}
	if completionUnits < 0 || int64(completionUnits) > quotamath.MaxQuota {
		return 0, fmt.Errorf("%w: invalid async task completion units", ErrReferencePricingConfiguration)
	}
	if plan.UseFixedPrice || completionUnits == 0 {
		return plan.PreConsumeQuota()
	}
	group, _ := decimal.NewFromString(plan.GroupRatio)
	ratio, _ := decimal.NewFromString(plan.ModelRatio)
	return strictReferenceQuota(
		decimal.NewFromInt(int64(completionUnits)).Mul(ratio).Mul(group),
		false,
	)
}

func (plan ReferenceAsyncTaskBillingPlan) BillingLogFields() map[string]any {
	fields := map[string]any{
		"billing_mode": BillingModeReference,
		"group_ratio":  plan.GroupRatio,
		"use_price":    plan.UseFixedPrice,
	}
	if plan.GroupRatioSpecial {
		fields["user_group_ratio"] = plan.GroupRatio
	}
	if plan.FreeModel {
		fields["free_model"] = true
	}
	if plan.UseFixedPrice {
		fields["model_price"] = plan.FixedPrice
	} else {
		fields["model_ratio"] = plan.ModelRatio
	}
	return fields
}

func parseReferenceAsyncDecimal(name, raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) != raw || !quotamath.IsSafeDecimalLiteral(raw) {
		return decimal.Zero, fmt.Errorf("%w: invalid async task %s", ErrReferencePricingConfiguration, name)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || value.IsNegative() {
		return decimal.Zero, fmt.Errorf("%w: invalid async task %s", ErrReferencePricingConfiguration, name)
	}
	return value, nil
}

// ResolveOrdinaryReferenceBillingPlan returns a plan only when modelName is
// explicitly opted into the reference pricing mode. The boolean distinguishes
// the normal TokenRouter modes from a valid reference plan.
func ResolveOrdinaryReferenceBillingPlan(modelName, userGroup, usingGroup string) (ReferenceBillingPlan, bool, error) {
	return resolveOrdinaryReferenceBillingPlan(modelName, userGroup, usingGroup, nil)
}

// ResolveOrdinaryReferenceBillingPlanForUser is the request-time counterpart
// that honors accept_unset_model_ratio_model. It is intentionally limited to
// models explicitly placed in reference mode; TokenRouter's default billing
// mode retains its established USD-per-million fallback.
func ResolveOrdinaryReferenceBillingPlanForUser(userID int, modelName, userGroup, usingGroup string) (ReferenceBillingPlan, bool, error) {
	return resolveOrdinaryReferenceBillingPlan(modelName, userGroup, usingGroup, func() (bool, error) {
		if userID <= 0 {
			return false, fmt.Errorf("%w: user pricing preference is unavailable", ErrReferencePricingConfiguration)
		}
		settings, err := userssvc.LoadUserSettings(userID)
		if err != nil {
			return false, fmt.Errorf("%w: user pricing preference is unavailable", ErrReferencePricingConfiguration)
		}
		return settings.AcceptUnsetRatioModel, nil
	})
}

func resolveOrdinaryReferenceBillingPlan(modelName, userGroup, usingGroup string, acceptUnset func() (bool, error)) (ReferenceBillingPlan, bool, error) {
	options := setting.GetOptions(
		setting.ModelBillingModeOption,
		setting.PerCallModelPriceOption,
		setting.ModelRatioOption,
		setting.CompletionRatioOption,
		setting.PreConsumedQuotaOption,
		setting.GroupRatioOption,
		setting.GroupGroupRatioOption,
	)
	modes, err := parseReferenceBillingModes(options[setting.ModelBillingModeOption])
	if err != nil {
		return ReferenceBillingPlan{}, false, err
	}
	mode := modes[modelName]
	if mode != BillingModeReference {
		switch mode {
		case BillingModeDefault, BillingModeRatio, BillingModeTieredExpr:
			return ReferenceBillingPlan{}, false, nil
		default:
			return ReferenceBillingPlan{}, false, fmt.Errorf("%w: unsupported billing mode %q for model %q", ErrReferencePricingConfiguration, mode, modelName)
		}
	}

	raw := referencePricingRaw{
		fixedPrice:                options[setting.PerCallModelPriceOption],
		modelRatio:                options[setting.ModelRatioOption],
		completionRatio:           options[setting.CompletionRatioOption],
		completionRatioConfigured: optionConfigured(options, setting.CompletionRatioOption),
	}
	maps, err := loadReferencePricingMaps(raw)
	if err != nil {
		return ReferenceBillingPlan{}, false, err
	}
	groupRatio, special, err := resolveReferenceGroupRatio(options, userGroup, usingGroup)
	if err != nil {
		return ReferenceBillingPlan{}, false, err
	}
	if !validReferencePricingNumber(groupRatio) {
		return ReferenceBillingPlan{}, false, fmt.Errorf("%w: invalid effective group ratio for %q", ErrReferencePricingConfiguration, usingGroup)
	}
	plan := ReferenceBillingPlan{
		modelName:         modelName,
		groupRatio:        groupRatio,
		groupRatioSpecial: special,
		completionRatio:   1,
		usageRatioPolicy:  setting.GetUsageRatioPolicy(modelName),
	}
	pricingModelName := referenceMatchingModelName(modelName)
	if price, found := maps.fixedPrice[pricingModelName]; found {
		plan.useFixedPrice = true
		plan.fixedPrice = price
		plan.freeModel = !plan.usageRatioPolicy.EnableFreeModelPreConsume && (groupRatio == 0 || price == 0)
		return plan, true, nil
	}

	modelRatio, found := maps.modelRatio[pricingModelName]
	if !found {
		accepted := false
		if acceptUnset != nil {
			accepted, err = acceptUnset()
			if err != nil {
				return ReferenceBillingPlan{}, false, err
			}
		}
		if !accepted {
			return ReferenceBillingPlan{}, false, fmt.Errorf("%w: model %q has neither PerCallModelPrice nor ModelRatio", ErrReferencePricingConfiguration, modelName)
		}
		modelRatio = referenceUnsetModelRatio
	}
	plan.modelRatio = modelRatio
	plan.freeModel = !plan.usageRatioPolicy.EnableFreeModelPreConsume && (groupRatio == 0 || modelRatio == 0)
	plan.completionRatio = referenceCompletionRatio(pricingModelName, maps.completionRatio, raw.completionRatioConfigured)
	minimum, err := parseReferencePreConsumedQuota(options)
	if err != nil {
		return ReferenceBillingPlan{}, false, err
	}
	plan.preConsumedMinimum = minimum
	return plan, true, nil
}

func optionConfigured(options map[string]string, key string) bool {
	_, configured := options[key]
	return configured
}

func referenceMatchingModelName(modelName string) string {
	switch {
	case strings.HasPrefix(modelName, "gemini-2.5-flash-lite") && strings.Contains(modelName, "-thinking-"):
		modelName = "gemini-2.5-flash-lite-thinking-*"
	case strings.HasPrefix(modelName, "gemini-2.5-flash") && strings.Contains(modelName, "-thinking-"):
		modelName = "gemini-2.5-flash-thinking-*"
	case strings.HasPrefix(modelName, "gemini-2.5-pro") && strings.Contains(modelName, "-thinking-"):
		modelName = "gemini-2.5-pro-thinking-*"
	}
	if strings.HasPrefix(modelName, "gpt-4-gizmo") {
		return "gpt-4-gizmo-*"
	}
	if strings.HasPrefix(modelName, "gpt-4o-gizmo") {
		return "gpt-4o-gizmo-*"
	}
	return modelName
}

func referenceCompletionRatio(modelName string, configured map[string]float64, optionWasConfigured bool) float64 {
	// Provider-qualified names use their explicit setting before family rules.
	if strings.Contains(modelName, "/") {
		if ratio, found := configured[modelName]; found {
			return ratio
		}
	}
	ratio, locked := referenceCompletionFamilyRatio(modelName)
	if locked {
		return ratio
	}
	if configuredRatio, found := configured[modelName]; found {
		return configuredRatio
	}
	if !optionWasConfigured {
		if defaultRatio, found := map[string]float64{
			"gpt-4-gizmo-*":  2,
			"gpt-4o-gizmo-*": 3,
			"gpt-4-all":      2,
			"gpt-image-1":    8,
		}[modelName]; found {
			return defaultRatio
		}
	}
	return ratio
}

func referenceCompletionFamilyRatio(modelName string) (float64, bool) {
	if strings.HasSuffix(modelName, "-all") || strings.HasSuffix(modelName, "-gizmo-*") {
		return 2, false
	}

	if strings.HasPrefix(modelName, "gpt-") {
		switch {
		case strings.HasPrefix(modelName, "gpt-4o"):
			switch {
			case modelName == "gpt-4o-2024-05-13":
				return 3, true
			case strings.HasPrefix(modelName, "gpt-4o-mini-tts"):
				return 20, false
			default:
				return 4, false
			}
		case strings.HasPrefix(modelName, "gpt-5"):
			if !strings.Contains(modelName, ".") {
				return 8, true
			}
			if strings.HasPrefix(modelName, "gpt-5.4-nano") {
				return 6.25, true
			}
			if strings.HasPrefix(modelName, "gpt-5.4") {
				return 6, true
			}
			return 6, false
		case strings.HasPrefix(modelName, "gpt-4.5-preview"):
			return 2, true
		case strings.HasPrefix(modelName, "gpt-4-turbo"),
			strings.HasSuffix(modelName, "gpt-4-1106"),
			strings.HasSuffix(modelName, "gpt-4-1105"):
			return 3, true
		default:
			return 2, false
		}
	}

	switch {
	case strings.HasPrefix(modelName, "o1"), strings.HasPrefix(modelName, "o3"):
		return 4, true
	case modelName == "chatgpt-4o-latest":
		return 3, true
	case strings.Contains(modelName, "claude-3"),
		strings.Contains(modelName, "claude-sonnet-4"),
		strings.Contains(modelName, "claude-opus-4"),
		strings.Contains(modelName, "claude-haiku-4"):
		return 5, true
	}

	if strings.HasPrefix(modelName, "gpt-3.5") {
		switch {
		case modelName == "gpt-3.5-turbo", strings.HasSuffix(modelName, "0125"):
			return 3, true
		case strings.HasSuffix(modelName, "1106"):
			return 2, true
		default:
			return 4.0 / 3.0, true
		}
	}
	if strings.HasPrefix(modelName, "mistral-") {
		return 3, true
	}
	if strings.HasPrefix(modelName, "gemini-") {
		switch {
		case strings.HasPrefix(modelName, "gemini-1.5"), strings.HasPrefix(modelName, "gemini-2.0"):
			return 4, true
		case strings.HasPrefix(modelName, "gemini-2.5-pro"):
			return 8, false
		case strings.HasPrefix(modelName, "gemini-2.5-flash-preview"):
			if strings.HasSuffix(modelName, "-nothinking") {
				return 4, false
			}
			return 3.5 / 0.15, false
		case strings.HasPrefix(modelName, "gemini-2.5-flash-lite"):
			return 4, false
		case strings.HasPrefix(modelName, "gemini-2.5-flash"), strings.HasPrefix(modelName, "gemini-robotics-er-1.5"):
			return 2.5 / 0.3, false
		case strings.HasPrefix(modelName, "gemini-3-pro-image"):
			return 60, false
		case strings.HasPrefix(modelName, "gemini-3-pro"):
			return 6, false
		default:
			return 4, false
		}
	}
	if strings.HasPrefix(modelName, "command") {
		switch modelName {
		case "command-r":
			return 3, true
		case "command-r-plus":
			return 5, true
		case "command-r-08-2024", "command-r-plus-08-2024":
			return 4, true
		default:
			return 4, false
		}
	}
	if strings.HasPrefix(modelName, "ERNIE-Speed-") || strings.HasPrefix(modelName, "ERNIE-Lite-") ||
		strings.HasPrefix(modelName, "ERNIE-Character") || strings.HasPrefix(modelName, "ERNIE-Functions") {
		return 2, true
	}
	switch modelName {
	case "llama2-70b-4096":
		return 0.8 / 0.64, true
	case "llama3-8b-8192":
		return 2, true
	case "llama3-70b-8192":
		return 0.79 / 0.59, true
	default:
		return 1, false
	}
}

func resolveReferenceGroupRatio(options map[string]string, userGroup, usingGroup string) (float64, bool, error) {
	groupRatios, err := parseGroupRatios(options[setting.GroupRatioOption])
	if err != nil {
		return 0, false, fmt.Errorf("%w: %v", ErrReferencePricingConfiguration, err)
	}
	specialRatios, err := parseGroupGroupRatios(options[setting.GroupGroupRatioOption])
	if err != nil {
		return 0, false, fmt.Errorf("%w: %v", ErrReferencePricingConfiguration, err)
	}
	if userGroup != "" {
		if ratio, configured := specialRatios[userGroup][usingGroup]; configured {
			return ratio, true, nil
		}
	}
	if usingGroup != "" {
		if ratio, configured := groupRatios[usingGroup]; configured {
			return ratio, false, nil
		}
	}
	return 1, false, nil
}

// PreConsumeQuota computes the reference pre-dispatch hold. Fixed-price models
// ignore token estimates. Ratio-priced models use max(prompt, PreConsumedQuota)
// plus an explicitly supplied non-zero completion limit; CompletionRatio is
// deliberately a settlement-only multiplier in the reference contract.
func (plan ReferenceBillingPlan) PreConsumeQuota(promptTokens, maxCompletionTokens int, completionLimitProvided bool) (int, error) {
	if err := validateReferenceTokenCount("prompt tokens", promptTokens); err != nil {
		return 0, err
	}
	if err := validateReferenceTokenCount("maximum completion tokens", maxCompletionTokens); err != nil {
		return 0, err
	}
	if plan.freeModel {
		return 0, nil
	}
	if plan.useFixedPrice {
		return strictReferenceFloatQuota(plan.fixedPrice * float64(quotamath.QuotaPerUnit) * plan.groupRatio)
	}

	preConsumedTokens := promptTokens
	if preConsumedTokens < plan.preConsumedMinimum {
		preConsumedTokens = plan.preConsumedMinimum
	}
	if completionLimitProvided && maxCompletionTokens != 0 {
		if int64(preConsumedTokens) > quotamath.MaxQuota-int64(maxCompletionTokens) {
			return 0, fmt.Errorf("%w: pre-consumed token estimate overflows", ErrReferencePricingConfiguration)
		}
		preConsumedTokens += maxCompletionTokens
	}
	return strictReferenceFloatQuota(float64(preConsumedTokens) * plan.modelRatio * plan.groupRatio)
}

// SettlementQuota applies the authoritative usage correction. The reference
// rounds the final decimal charge, applies CompletionRatio only to completion
// tokens, and charges at least one quota for billable ratio-priced usage when
// the effective model/group ratio is non-zero. Fixed price remains zero when
// an upstream explicitly reports no billable usage.
func (plan ReferenceBillingPlan) SettlementQuota(promptTokens, completionTokens int) (int, *quotamath.QuotaClamp, error) {
	if err := validateReferenceTokenCount("prompt tokens", promptTokens); err != nil {
		return 0, nil, err
	}
	if err := validateReferenceTokenCount("completion tokens", completionTokens); err != nil {
		return 0, nil, err
	}
	if promptTokens == 0 && completionTokens == 0 {
		return 0, nil, nil
	}

	groupRatio := decimal.NewFromFloat(plan.groupRatio)
	var quota decimal.Decimal
	if plan.useFixedPrice {
		quota = decimal.NewFromFloat(plan.fixedPrice).
			Mul(decimal.NewFromInt(quotamath.QuotaPerUnit)).
			Mul(groupRatio)
	} else {
		effectiveRatio := decimal.NewFromFloat(plan.modelRatio).Mul(groupRatio)
		quota = decimal.NewFromInt(int64(promptTokens)).
			Add(decimal.NewFromInt(int64(completionTokens)).Mul(decimal.NewFromFloat(plan.completionRatio))).
			Mul(effectiveRatio)
		if !effectiveRatio.IsZero() && quota.LessThanOrEqual(decimal.Zero) {
			quota = decimal.NewFromInt(1)
		}
	}

	value, err := strictReferenceQuota(quota, true)
	if err != nil {
		var clamp *quotamath.QuotaClamp
		if errors.As(err, &clamp) {
			return 0, clamp, err
		}
		return 0, nil, err
	}
	if !plan.useFixedPrice && plan.modelRatio != 0 && plan.groupRatio != 0 && value == 0 {
		value = 1
	}
	return value, nil, nil
}

// BillingLogFields describes the exact snapshot that produced a reference
// charge. Callers merge it into the ordinary consume log.
func (plan ReferenceBillingPlan) BillingLogFields() map[string]any {
	fields := map[string]any{
		"billing_mode": BillingModeReference,
		"group_ratio":  plan.groupRatio,
		"use_price":    plan.useFixedPrice,
	}
	if plan.groupRatioSpecial {
		fields["user_group_ratio"] = plan.groupRatio
	}
	if plan.freeModel {
		fields["free_model"] = true
	}
	if plan.useFixedPrice {
		fields["model_price"] = plan.fixedPrice
	} else {
		fields["model_ratio"] = plan.modelRatio
		fields["completion_ratio"] = plan.completionRatio
	}
	return fields
}

func strictReferenceQuota(value decimal.Decimal, round bool) (int, error) {
	if round {
		value = value.Round(0)
	}
	quota, err := quotamath.QuotaFromDecimalStrict(value)
	if err != nil {
		return 0, fmt.Errorf("reference pricing quota: %w", err)
	}
	if quota < 0 {
		return 0, fmt.Errorf("%w: reference pricing produced negative quota", ErrReferencePricingConfiguration)
	}
	return quota, nil
}

func strictReferenceFloatQuota(value float64) (int, error) {
	quota, clamp := quotamath.QuotaFromFloatChecked(value)
	if clamp != nil {
		return 0, fmt.Errorf("reference pricing quota: %w", clamp)
	}
	if quota < 0 {
		return 0, fmt.Errorf("%w: reference pricing produced negative quota", ErrReferencePricingConfiguration)
	}
	return quota, nil
}

func validateReferenceTokenCount(name string, value int) error {
	if value < 0 || int64(value) > quotamath.MaxQuota {
		return fmt.Errorf("%w: %s is outside the supported range", ErrReferencePricingConfiguration, name)
	}
	return nil
}

func validReferencePricingNumber(value float64) bool {
	return value >= 0 && value <= referencePricingMaxValue && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validReferencePricingModel(modelName string) bool {
	return modelName != "" && modelName == strings.TrimSpace(modelName) && len(modelName) <= referencePricingMaxModelBytes
}

func parseReferenceBillingModes(raw string) (map[string]string, error) {
	modes := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return modes, nil
	}
	if len(raw) > referencePricingMaxOptionBytes {
		return nil, fmt.Errorf("%w: ModelBillingMode exceeds %d bytes", ErrReferencePricingConfiguration, referencePricingMaxOptionBytes)
	}
	if err := jsonutil.UnmarshalJsonStr(raw, &modes); err != nil || modes == nil {
		return nil, fmt.Errorf("%w: ModelBillingMode must be a JSON object", ErrReferencePricingConfiguration)
	}
	if len(modes) > referencePricingMaxEntries {
		return nil, fmt.Errorf("%w: ModelBillingMode has too many entries", ErrReferencePricingConfiguration)
	}
	for modelName, mode := range modes {
		if !validReferencePricingModel(modelName) || mode != strings.TrimSpace(mode) || len(mode) > 64 {
			return nil, fmt.Errorf("%w: invalid ModelBillingMode entry for %q", ErrReferencePricingConfiguration, modelName)
		}
	}
	return modes, nil
}

func loadReferencePricingMaps(raw referencePricingRaw) (referencePricingMaps, error) {
	referencePricingCache.RLock()
	if referencePricingCache.initialized && referencePricingCache.raw == raw {
		maps, err := referencePricingCache.maps, referencePricingCache.err
		referencePricingCache.RUnlock()
		return maps, err
	}
	referencePricingCache.RUnlock()

	maps, err := parseReferencePricingMaps(raw)
	referencePricingCache.Lock()
	if !referencePricingCache.initialized || referencePricingCache.raw != raw {
		referencePricingCache.initialized = true
		referencePricingCache.raw = raw
		referencePricingCache.maps = maps
		referencePricingCache.err = err
	} else {
		maps, err = referencePricingCache.maps, referencePricingCache.err
	}
	referencePricingCache.Unlock()
	return maps, err
}

func parseReferencePricingMaps(raw referencePricingRaw) (referencePricingMaps, error) {
	fixedPrice, err := parseReferenceNumericMap(setting.PerCallModelPriceOption, raw.fixedPrice)
	if err != nil {
		return referencePricingMaps{}, err
	}
	modelRatio, err := parseReferenceNumericMap(setting.ModelRatioOption, raw.modelRatio)
	if err != nil {
		return referencePricingMaps{}, err
	}
	completionRatio, err := parseReferenceNumericMap(setting.CompletionRatioOption, raw.completionRatio)
	if err != nil {
		return referencePricingMaps{}, err
	}
	return referencePricingMaps{
		fixedPrice: fixedPrice, modelRatio: modelRatio, completionRatio: completionRatio,
	}, nil
}

func parseReferenceNumericMap(optionName, raw string) (map[string]float64, error) {
	values := map[string]float64{}
	if strings.TrimSpace(raw) == "" {
		return values, nil
	}
	if len(raw) > referencePricingMaxOptionBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrReferencePricingConfiguration, optionName, referencePricingMaxOptionBytes)
	}
	if err := jsonutil.UnmarshalJsonStr(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%w: %s must be a numeric JSON object", ErrReferencePricingConfiguration, optionName)
	}
	if len(values) > referencePricingMaxEntries {
		return nil, fmt.Errorf("%w: %s has too many entries", ErrReferencePricingConfiguration, optionName)
	}
	for modelName, value := range values {
		if !validReferencePricingModel(modelName) || !validReferencePricingNumber(value) {
			return nil, fmt.Errorf("%w: invalid %s entry for %q", ErrReferencePricingConfiguration, optionName, modelName)
		}
	}
	return values, nil
}

func parseReferencePreConsumedQuota(options map[string]string) (int, error) {
	raw, configured := options[setting.PreConsumedQuotaOption]
	if !configured {
		return referenceDefaultPreConsumedQuota, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("%w: PreConsumedQuota is empty", ErrReferencePricingConfiguration)
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value < 0 || value > quotamath.MaxQuota {
		return 0, fmt.Errorf("%w: invalid PreConsumedQuota", ErrReferencePricingConfiguration)
	}
	return int(value), nil
}

// validateReferencePricingOptions is part of the runtime reload gate. It only
// activates the compatibility maps when at least one model explicitly selects
// reference mode, leaving dormant legacy compatibility data unable to alter
// TokenRouter's default USD-per-million behavior.
func validateReferencePricingOptions() error {
	options := setting.GetOptions(
		setting.ModelBillingModeOption,
		setting.PerCallModelPriceOption,
		setting.ModelRatioOption,
		setting.CompletionRatioOption,
		setting.PreConsumedQuotaOption,
	)
	modes, err := parseReferenceBillingModes(options[setting.ModelBillingModeOption])
	if err != nil {
		return err
	}
	referenceModels := make([]string, 0)
	for modelName, mode := range modes {
		if mode == BillingModeReference {
			referenceModels = append(referenceModels, modelName)
		}
	}
	if len(referenceModels) == 0 {
		return nil
	}
	maps, err := loadReferencePricingMaps(referencePricingRaw{
		fixedPrice:                options[setting.PerCallModelPriceOption],
		modelRatio:                options[setting.ModelRatioOption],
		completionRatio:           options[setting.CompletionRatioOption],
		completionRatioConfigured: optionConfigured(options, setting.CompletionRatioOption),
	})
	if err != nil {
		return err
	}
	needsPreConsumedQuota := false
	for _, modelName := range referenceModels {
		pricingModelName := referenceMatchingModelName(modelName)
		if _, fixed := maps.fixedPrice[pricingModelName]; fixed {
			continue
		}
		// A missing ratio is a valid request-time state: users who explicitly
		// accept unset ratios receive the reference fallback, while every other
		// request fails closed before provider dispatch.
		needsPreConsumedQuota = true
	}
	if needsPreConsumedQuota {
		_, err = parseReferencePreConsumedQuota(options)
	}
	return err
}
