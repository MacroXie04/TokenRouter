package setting

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	TopUpLinkOption                   = "TopUpLink"
	TopUpGroupRatioOption             = "TopupGroupRatio"
	USDExchangeRateOption             = "USDExchangeRate"
	DisplayTokenStatEnabledOption     = "DisplayTokenStatEnabled"
	CustomCurrencySymbolOption        = "general_setting.custom_currency_symbol"
	CustomCurrencyExchangeRateOption  = "general_setting.custom_currency_exchange_rate"
	CurrencyDisplayTypeUSD            = "currency"
	CurrencyDisplayTypeCNY            = "cny"
	CurrencyDisplayTypeTokens         = "tokens"
	CurrencyDisplayTypeCustom         = "custom"
	defaultUSDExchangeRate            = 7.3
	defaultCustomCurrencyExchangeRate = 1.0
	defaultCustomCurrencySymbol       = "¤"
	maxCurrencyExchangeRate           = 1_000_000
	maxBillingUnitPrice               = MaxPaymentProviderAmount
	maxCustomCurrencySymbolBytes      = 32
	maxBillingURLBytes                = 2048
	maxBillingCredentialBytes         = 4096
	maxTopUpGroupRatios               = 256
	maxTopUpGroupRatioBytes           = 64 << 10
	maxTopUpGroupRatio                = 1_000_000
	maxBillingGroupNameBytes          = 128
)

// CurrencyDisplaySetting is the server-owned wallet/display conversion
// contract. QuotaPerUnit is intentionally absent: that accounting scale is
// always common.QuotaPerUnit and cannot be supplied by an option.
type CurrencyDisplaySetting struct {
	Type                       string
	Symbol                     string
	USDExchangeRate            float64
	CustomCurrencyExchangeRate float64
}

func defaultCurrencyDisplaySetting() CurrencyDisplaySetting {
	return CurrencyDisplaySetting{
		Type:                       CurrencyDisplayTypeUSD,
		Symbol:                     "$",
		USDExchangeRate:            defaultUSDExchangeRate,
		CustomCurrencyExchangeRate: defaultCustomCurrencyExchangeRate,
	}
}

// BillingOptionDefaults returns bounded reference-compatible defaults that
// make otherwise absent billing controls visible to root operators.
func BillingOptionDefaults() map[string]string {
	return map[string]string{
		TopUpLinkOption:                  "",
		USDExchangeRateOption:            strconv.FormatFloat(defaultUSDExchangeRate, 'f', -1, 64),
		QuotaDisplayTypeOption:           "",
		DisplayTokenStatEnabledOption:    "true",
		CustomCurrencySymbolOption:       defaultCustomCurrencySymbol,
		CustomCurrencyExchangeRateOption: "1",
	}
}

// ParseTopUpGroupRatioOption validates the separate top-up multiplier map.
// Empty storage uses the reference defaults; a present value must be a
// non-empty object with unique keys and bounded, positive finite ratios.
func ParseTopUpGroupRatioOption(raw string) (map[string]float64, error) {
	if raw == "" {
		return map[string]float64{"default": 1, "vip": 1, "svip": 1}, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > maxTopUpGroupRatioBytes {
		return nil, fmt.Errorf("invalid %s value", TopUpGroupRatioOption)
	}
	if err := common.ValidateJSONNoDuplicateKeys([]byte(raw)); err != nil {
		return nil, fmt.Errorf("invalid %s JSON: %w", TopUpGroupRatioOption, err)
	}
	ratios := map[string]float64{}
	if err := common.UnmarshalJsonStr(raw, &ratios); err != nil || ratios == nil {
		return nil, fmt.Errorf("%s must be a JSON object", TopUpGroupRatioOption)
	}
	if len(ratios) == 0 || len(ratios) > maxTopUpGroupRatios {
		return nil, fmt.Errorf("%s must contain between 1 and %d groups", TopUpGroupRatioOption, maxTopUpGroupRatios)
	}
	for group, ratio := range ratios {
		if !validBillingGroupName(group) || ratio <= 0 || ratio > maxTopUpGroupRatio ||
			math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return nil, fmt.Errorf("invalid top-up ratio for group %q", group)
		}
	}
	return ratios, nil
}

// GetCurrencyDisplaySettingChecked returns one coherent validated snapshot.
// Financial compatibility endpoints use this form and fail closed if a row
// was corrupted outside the settings API.
func GetCurrencyDisplaySettingChecked() (CurrencyDisplaySetting, error) {
	return buildCurrencyDisplaySetting(GetOptions(
		QuotaDisplayTypeOption,
		USDExchangeRateOption,
		CustomCurrencySymbolOption,
		CustomCurrencyExchangeRateOption,
	))
}

// GetCurrencyDisplaySetting always returns safe client metadata. Invalid
// externally-written values are replaced component-by-component with bounded
// defaults, while admin writes are rejected by ValidateBillingOptionUpdate.
func GetCurrencyDisplaySetting() CurrencyDisplaySetting {
	options := GetOptions(
		QuotaDisplayTypeOption,
		USDExchangeRateOption,
		CustomCurrencySymbolOption,
		CustomCurrencyExchangeRateOption,
	)
	config := defaultCurrencyDisplaySetting()
	customSymbol := defaultCustomCurrencySymbol
	if displayType, err := parseCurrencyDisplayType(options[QuotaDisplayTypeOption]); err == nil {
		config.Type = displayType
	}
	if rate, err := parseCurrencyRate(options[USDExchangeRateOption], defaultUSDExchangeRate); err == nil {
		config.USDExchangeRate = rate
	}
	if symbol, err := parseCustomCurrencySymbol(options[CustomCurrencySymbolOption]); err == nil {
		customSymbol = symbol
	}
	if rate, err := parseCurrencyRate(options[CustomCurrencyExchangeRateOption], defaultCustomCurrencyExchangeRate); err == nil {
		config.CustomCurrencyExchangeRate = rate
	}
	switch config.Type {
	case CurrencyDisplayTypeCNY:
		config.Symbol = "¥"
	case CurrencyDisplayTypeTokens:
		config.Symbol = "tokens"
	case CurrencyDisplayTypeUSD:
		config.Symbol = "$"
	case CurrencyDisplayTypeCustom:
		config.Symbol = customSymbol
	}
	return config
}

// CurrencyExchangeRate returns the display units corresponding to one USD.
func (config CurrencyDisplaySetting) CurrencyExchangeRate() float64 {
	switch config.Type {
	case CurrencyDisplayTypeCNY:
		return config.USDExchangeRate
	case CurrencyDisplayTypeCustom:
		return config.CustomCurrencyExchangeRate
	default:
		return 1
	}
}

func buildCurrencyDisplaySetting(options map[string]string) (CurrencyDisplaySetting, error) {
	config := defaultCurrencyDisplaySetting()
	var err error
	config.Type, err = parseCurrencyDisplayType(options[QuotaDisplayTypeOption])
	if err != nil {
		return CurrencyDisplaySetting{}, err
	}
	config.USDExchangeRate, err = parseCurrencyRate(options[USDExchangeRateOption], defaultUSDExchangeRate)
	if err != nil {
		return CurrencyDisplaySetting{}, fmt.Errorf("%s: %w", USDExchangeRateOption, err)
	}
	config.CustomCurrencyExchangeRate, err = parseCurrencyRate(options[CustomCurrencyExchangeRateOption], defaultCustomCurrencyExchangeRate)
	if err != nil {
		return CurrencyDisplaySetting{}, fmt.Errorf("%s: %w", CustomCurrencyExchangeRateOption, err)
	}
	config.Symbol, err = parseCustomCurrencySymbol(options[CustomCurrencySymbolOption])
	if err != nil {
		return CurrencyDisplaySetting{}, err
	}
	switch config.Type {
	case CurrencyDisplayTypeUSD:
		config.Symbol = "$"
	case CurrencyDisplayTypeCNY:
		config.Symbol = "¥"
	case CurrencyDisplayTypeTokens:
		config.Symbol = "tokens"
	}
	return config, nil
}

func parseCurrencyDisplayType(raw string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "", "USD", "CURRENCY":
		return CurrencyDisplayTypeUSD, nil
	case "CNY":
		return CurrencyDisplayTypeCNY, nil
	case "TOKENS":
		return CurrencyDisplayTypeTokens, nil
	case "CUSTOM":
		return CurrencyDisplayTypeCustom, nil
	default:
		return "", fmt.Errorf("%s must be USD, CNY, TOKENS, or CUSTOM", QuotaDisplayTypeOption)
	}
}

func parseCurrencyRate(raw string, fallback float64) (float64, error) {
	if raw == "" {
		return fallback, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 64 {
		return 0, fmt.Errorf("exchange rate must be a plain number")
	}
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil || rate <= 0 || rate > maxCurrencyExchangeRate || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, fmt.Errorf("exchange rate must be greater than 0 and at most %d", maxCurrencyExchangeRate)
	}
	return rate, nil
}

func parseCustomCurrencySymbol(raw string) (string, error) {
	if raw == "" {
		return defaultCustomCurrencySymbol, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > maxCustomCurrencySymbolBytes || !validBillingText(raw) {
		return "", fmt.Errorf("%s must be a short printable symbol", CustomCurrencySymbolOption)
	}
	if utf8.RuneCountInString(raw) > 8 {
		return "", fmt.Errorf("%s must contain at most 8 characters", CustomCurrencySymbolOption)
	}
	return raw, nil
}

// GetTopUpLink returns a safe external redemption-code purchase URL. Empty or
// externally-corrupted values are not advertised to wallet clients.
func GetTopUpLink() string {
	raw := GetOption(TopUpLinkOption)
	if raw == "" || !validBillingURL(raw, true) {
		return ""
	}
	return raw
}

// ValidateBillingOptionUpdate applies bounded semantic validation to billing
// keys accepted by the generic root settings endpoint. Unknown keys are left
// to their owning settings family.
func ValidateBillingOptionUpdate(key, value string) error {
	switch key {
	case InitialQuotaOption, PreConsumedQuotaOption, QuotaForInviterOption, QuotaForInviteeOption:
		return validateOptionalBoundedInt(key, value, 0)
	case PriceOption, StripeUnitPriceOption:
		if value == "" {
			return nil
		}
		_, err := parseBoundedPositiveFloat(value, maxBillingUnitPrice)
		if err != nil {
			return fmt.Errorf("%s must be a positive bounded number", key)
		}
		return nil
	case MinTopUpOption, StripeMinTopUpOption:
		return validateOptionalBoundedIntRange(key, value, 1, MaxTopUpReferenceAmount)
	case USDExchangeRateOption, CustomCurrencyExchangeRateOption:
		_, err := parseCurrencyRate(value, map[string]float64{
			USDExchangeRateOption:            defaultUSDExchangeRate,
			CustomCurrencyExchangeRateOption: defaultCustomCurrencyExchangeRate,
		}[key])
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		return nil
	case QuotaDisplayTypeOption:
		_, err := parseCurrencyDisplayType(value)
		return err
	case CustomCurrencySymbolOption:
		_, err := parseCustomCurrencySymbol(value)
		return err
	case DisplayTokenStatEnabledOption, StripePromotionCodesOption:
		return validateOptionalBool(key, value)
	case TopUpLinkOption:
		if value != "" && !validBillingURL(value, true) {
			return fmt.Errorf("%s must be an absolute HTTPS URL (or loopback HTTP URL)", key)
		}
	case PayAddressOption, CustomCallbackAddressOption:
		if value != "" && !validBillingURL(value, false) {
			return fmt.Errorf("%s must be an absolute HTTPS URL (or loopback HTTP URL) without query or fragment", key)
		}
	case EpayIdOption:
		if !validOptionalBillingText(value, 255) {
			return fmt.Errorf("%s is invalid", key)
		}
	case EpayKeyOption:
		if !validOptionalBillingText(value, maxBillingCredentialBytes) {
			return fmt.Errorf("%s is invalid", key)
		}
	case StripePriceIdOption:
		if !validOptionalBillingText(value, 255) {
			return fmt.Errorf("%s is invalid", key)
		}
	case StripeCurrencyOption:
		if value == "" {
			return nil
		}
		if len(value) != 3 || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s must be a three-letter currency code", key)
		}
		for _, character := range value {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') {
				return fmt.Errorf("%s must be a three-letter currency code", key)
			}
		}
	case PayMethodsOption:
		_, err := parsePayMethods(value)
		return err
	case PaymentSettingOption:
		return validatePaymentSettingRaw(value)
	}
	return nil
}

func parseBoundedPositiveFloat(raw string, maximum float64) (float64, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 64 {
		return 0, fmt.Errorf("value must be a plain number")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 || value > maximum || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("value must be greater than 0 and at most %g", maximum)
	}
	return value, nil
}

func validatePaymentSettingRaw(raw string) error {
	if raw == "" {
		return nil
	}
	// Reuse the runtime parser without temporarily publishing the candidate.
	if len(raw) > maxPaymentSettingJSON || !utf8.ValidString(raw) {
		return fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PaymentSettingOption)
	}
	if err := common.ValidateJSONNoDuplicateKeys([]byte(raw)); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidPaymentSetting, PaymentSettingOption, err)
	}
	var stored PaymentSetting
	if err := common.UnmarshalJsonStr(raw, &stored); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PaymentSettingOption)
	}
	if len(stored.AmountOptions) > maxPaymentAmountOptions || len(stored.AmountDiscount) > maxPaymentDiscounts {
		return fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PaymentSettingOption)
	}
	seen := make(map[int]struct{}, len(stored.AmountOptions))
	for _, amount := range stored.AmountOptions {
		if amount <= 0 || int64(amount) > common.MaxQuota {
			return fmt.Errorf("%w: %s amount", ErrInvalidPaymentSetting, PaymentSettingOption)
		}
		if _, duplicate := seen[amount]; duplicate {
			return fmt.Errorf("%w: duplicate %s amount", ErrInvalidPaymentSetting, PaymentSettingOption)
		}
		seen[amount] = struct{}{}
	}
	for amount, discount := range stored.AmountDiscount {
		if amount <= 0 || int64(amount) > common.MaxQuota || !isFinitePositive(discount) || discount > MaxPaymentDiscount {
			return fmt.Errorf("%w: %s discount", ErrInvalidPaymentSetting, PaymentSettingOption)
		}
	}
	return nil
}

func validateOptionalBoundedInt(key, raw string, minimum int64) error {
	return validateOptionalBoundedIntRange(key, raw, minimum, common.MaxQuota)
}

func validateOptionalBoundedIntRange(key, raw string, minimum, maximum int64) error {
	if raw == "" {
		return nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 32 {
		return fmt.Errorf("%s must be an integer", key)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return nil
}

func validateOptionalBool(key, raw string) error {
	if raw == "" {
		return nil
	}
	if raw != "true" && raw != "false" {
		return fmt.Errorf("%s must be true or false", key)
	}
	return nil
}

func validOptionalBillingText(value string, maximum int) bool {
	return value == "" || value == strings.TrimSpace(value) && len(value) <= maximum && validBillingText(value)
}

func validBillingText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func validBillingGroupName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxBillingGroupNameBytes && validBillingText(value)
}

func validBillingURL(raw string, allowQuery bool) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > maxBillingURLBytes ||
		!validBillingText(raw) || strings.Contains(raw, `\`) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.Fragment != "" || (!allowQuery && parsed.RawQuery != "") {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(host)
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || address != nil && address.IsLoopback()
}
