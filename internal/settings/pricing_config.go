package settings

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	billingexpr "github.com/tokenrouter/tokenrouter/internal/billing/expression"
)

const (
	maxPricingConfigEntries         = 20_000
	maxPricingConfigNameBytes       = 256
	maxPricingConfigGroupBytes      = 128
	maxPricingConfigExpressionBytes = 16 << 10
	maxPricingConfigValue           = 1e12
	maxPricingSpecialUserGroups     = 256
	maxPricingSpecialGroupsPerUser  = 256
	maxPricingSpecialEntries        = 4_096
)

type pricingModelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
}

// validatePricingConfiguration is the single publication gate for all
// interdependent model and group pricing documents. It deliberately lives in
// setting rather than service so Init, Sync, and UpdateOptions reject an
// invalid database snapshot before either the raw option map or any typed
// runtime policy is made visible.
func validatePricingConfiguration(options map[string]string) error {
	if err := validatePricingModelPriceMap(options[ModelPriceOption]); err != nil {
		return err
	}
	for _, key := range []string{ModelRatioOption, CompletionRatioOption, PerCallModelPriceOption} {
		if err := validatePricingNumberMap(key, options[key], true); err != nil {
			return err
		}
	}
	if err := validatePricingNumberMap(GroupRatioOption, options[GroupRatioOption], false); err != nil {
		return err
	}
	if err := validatePricingSpecialRatioMap(options[GroupGroupRatioOption]); err != nil {
		return err
	}

	modes := map[string]string{}
	if err := decodePricingDocument(ModelBillingModeOption, options[ModelBillingModeOption], &modes); err != nil {
		return err
	}
	expressions := map[string]string{}
	if err := decodePricingDocument(ModelBillingExprOption, options[ModelBillingExprOption], &expressions); err != nil {
		return err
	}
	if modes == nil || expressions == nil {
		return errors.New("billing mode and expression configuration must be JSON objects")
	}
	if len(modes) > maxPricingConfigEntries || len(expressions) > maxPricingConfigEntries {
		return errors.New("billing mode or expression configuration contains too many entries")
	}
	for modelName, mode := range modes {
		if !validPricingConfigName(modelName, maxPricingConfigNameBytes) {
			return fmt.Errorf("%s contains an invalid model name", ModelBillingModeOption)
		}
		switch mode {
		case "", "ratio", "reference", "tiered_expr":
		default:
			return fmt.Errorf("%s contains unsupported mode %q for %q", ModelBillingModeOption, mode, modelName)
		}
		if mode == "tiered_expr" && strings.TrimSpace(expressions[modelName]) == "" {
			return fmt.Errorf("%s requires a non-empty expression for %q", ModelBillingModeOption, modelName)
		}
	}
	for modelName, expression := range expressions {
		if !validPricingConfigName(modelName, maxPricingConfigNameBytes) ||
			len(expression) > maxPricingConfigExpressionBytes || !utf8.ValidString(expression) {
			return fmt.Errorf("%s contains an invalid expression for %q", ModelBillingExprOption, modelName)
		}
		if strings.TrimSpace(expression) == "" {
			if modes[modelName] == "tiered_expr" {
				return fmt.Errorf("%s requires a non-empty expression for %q", ModelBillingModeOption, modelName)
			}
			continue
		}
		for _, character := range expression {
			if character == 0 || unicode.Is(unicode.Bidi_Control, character) {
				return fmt.Errorf("%s contains unsafe characters for %q", ModelBillingExprOption, modelName)
			}
		}
		if _, err := billingexpr.CompileFromCache(expression); err != nil {
			return fmt.Errorf("%s contains an invalid expression for %q: %w", ModelBillingExprOption, modelName, err)
		}
	}
	return nil
}

func decodePricingDocument(option, raw string, destination any) error {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := decodeBoundedPolicyJSON(raw, destination); err != nil {
		return fmt.Errorf("%s: %w", option, err)
	}
	return nil
}

func validatePricingModelPriceMap(raw string) error {
	values := map[string]pricingModelPrice{}
	if err := decodePricingDocument(ModelPriceOption, raw, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("%s must be a JSON object", ModelPriceOption)
	}
	if len(values) > maxPricingConfigEntries {
		return fmt.Errorf("%s contains too many entries", ModelPriceOption)
	}
	for modelName, price := range values {
		if !validPricingConfigName(modelName, maxPricingConfigNameBytes) ||
			!validPricingConfigNumber(price.Prompt, true) || !validPricingConfigNumber(price.Completion, true) {
			return fmt.Errorf("%s contains an invalid price for %q", ModelPriceOption, modelName)
		}
	}
	return nil
}

func validatePricingNumberMap(option, raw string, allowZero bool) error {
	values := map[string]float64{}
	if err := decodePricingDocument(option, raw, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("%s must be a JSON object", option)
	}
	if len(values) > maxPricingConfigEntries {
		return fmt.Errorf("%s contains too many entries", option)
	}
	nameLimit := maxPricingConfigNameBytes
	if option == GroupRatioOption {
		nameLimit = maxPricingConfigGroupBytes
	}
	for name, value := range values {
		if !validPricingConfigName(name, nameLimit) || !validPricingConfigNumber(value, allowZero) {
			return fmt.Errorf("%s contains an invalid value for %q", option, name)
		}
	}
	return nil
}

func validatePricingSpecialRatioMap(raw string) error {
	values := map[string]map[string]float64{}
	if err := decodePricingDocument(GroupGroupRatioOption, raw, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("%s must be a JSON object", GroupGroupRatioOption)
	}
	if len(values) > maxPricingSpecialUserGroups {
		return fmt.Errorf("%s contains too many user groups", GroupGroupRatioOption)
	}
	total := 0
	for userGroup, ratios := range values {
		if !validPricingConfigName(userGroup, maxPricingConfigGroupBytes) || ratios == nil ||
			len(ratios) > maxPricingSpecialGroupsPerUser {
			return fmt.Errorf("%s contains an invalid entry for %q", GroupGroupRatioOption, userGroup)
		}
		total += len(ratios)
		if total > maxPricingSpecialEntries {
			return fmt.Errorf("%s contains too many entries", GroupGroupRatioOption)
		}
		for usingGroup, ratio := range ratios {
			if !validPricingConfigName(usingGroup, maxPricingConfigGroupBytes) || !validPricingConfigNumber(ratio, true) {
				return fmt.Errorf("%s contains an invalid ratio for %q to %q", GroupGroupRatioOption, userGroup, usingGroup)
			}
		}
	}
	return nil
}

func validPricingConfigNumber(value float64, allowZero bool) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maxPricingConfigValue {
		return false
	}
	return allowZero || value > 0
}

func validPricingConfigName(value string, maximumBytes int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Bidi_Control, character) {
			return false
		}
	}
	return true
}
