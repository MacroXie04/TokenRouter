package setting

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	GrokViolationDeductionEnabledOption = "grok.violation_deduction_enabled"
	GrokViolationDeductionAmountOption  = "grok.violation_deduction_amount"

	DefaultGrokViolationDeductionAmount = 0.05
)

var maxGrokViolationDeductionAmount = decimal.NewFromInt(common.MaxQuota).
	Div(decimal.NewFromInt(int64(common.QuotaPerUnit)))

// GrokSetting is one coherent, validated snapshot of the xAI violation-fee
// policy. The decimal amount is retained alongside the reference-compatible
// float field so quota conversion never depends on binary float arithmetic.
type GrokSetting struct {
	ViolationDeductionEnabled bool
	ViolationDeductionAmount  float64
	violationAmountDecimal    decimal.Decimal
}

var grokConfig atomic.Pointer[GrokSetting]

func init() {
	config := defaultGrokSetting()
	grokConfig.Store(&config)
}

func defaultGrokSetting() GrokSetting {
	amount := decimal.RequireFromString("0.05")
	return GrokSetting{
		ViolationDeductionEnabled: true,
		ViolationDeductionAmount:  DefaultGrokViolationDeductionAmount,
		violationAmountDecimal:    amount,
	}
}

// GetGrokSetting returns an immutable copy of the live policy snapshot.
func GetGrokSetting() GrokSetting {
	config := grokConfig.Load()
	if config == nil {
		return defaultGrokSetting()
	}
	return *config
}

// ViolationDeductionAmountDecimal returns the validated exact amount used by
// accounting. The returned decimal is immutable.
func (s GrokSetting) ViolationDeductionAmountDecimal() decimal.Decimal {
	if s.violationAmountDecimal.IsZero() && s.ViolationDeductionAmount != 0 {
		// This compatibility fallback is useful only for callers constructing a
		// policy value directly in tests; published settings always carry the
		// exact parsed decimal above.
		return decimal.NewFromFloat(s.ViolationDeductionAmount)
	}
	return s.violationAmountDecimal
}

// GrokOptionDefaults exposes the reference defaults to root operators.
func GrokOptionDefaults() map[string]string {
	return map[string]string{
		GrokViolationDeductionEnabledOption: "true",
		GrokViolationDeductionAmountOption:  "0.05",
	}
}

func buildGrokSetting(options map[string]string) (GrokSetting, error) {
	config := defaultGrokSetting()
	if options == nil {
		return config, nil
	}
	if raw, present := options[GrokViolationDeductionEnabledOption]; present {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return GrokSetting{}, fmt.Errorf("%s must be a boolean", GrokViolationDeductionEnabledOption)
		}
		if raw != "true" && raw != "false" {
			return GrokSetting{}, fmt.Errorf("%s must be a boolean", GrokViolationDeductionEnabledOption)
		}
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return GrokSetting{}, fmt.Errorf("%s must be a boolean", GrokViolationDeductionEnabledOption)
		}
		config.ViolationDeductionEnabled = enabled
	}
	if raw, present := options[GrokViolationDeductionAmountOption]; present {
		raw = strings.TrimSpace(raw)
		if !common.IsSafeDecimalLiteral(raw) {
			return GrokSetting{}, fmt.Errorf(
				"%s must be a finite number between 0 and %s",
				GrokViolationDeductionAmountOption, maxGrokViolationDeductionAmount.String(),
			)
		}
		asFloat, floatErr := strconv.ParseFloat(raw, 64)
		if raw == "" || floatErr != nil || math.IsNaN(asFloat) || math.IsInf(asFloat, 0) ||
			asFloat < 0 || asFloat > float64(common.MaxQuota)/float64(common.QuotaPerUnit) {
			return GrokSetting{}, fmt.Errorf(
				"%s must be a finite number between 0 and %s",
				GrokViolationDeductionAmountOption, maxGrokViolationDeductionAmount.String(),
			)
		}
		amount, err := decimal.NewFromString(raw)
		if err != nil || amount.IsNegative() || amount.GreaterThan(maxGrokViolationDeductionAmount) {
			return GrokSetting{}, fmt.Errorf(
				"%s must be a finite number between 0 and %s",
				GrokViolationDeductionAmountOption, maxGrokViolationDeductionAmount.String(),
			)
		}
		config.ViolationDeductionAmount = asFloat
		config.violationAmountDecimal = amount
	}
	return config, nil
}
