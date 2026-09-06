package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Payment-related option keys (reference contract). Credentials that the
// reference stores as options remain environment variables in TokenRouter
// (STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET — deviation #8).
const (
	PayMethodsOption            = "PayMethods"
	PriceOption                 = "Price"
	MinTopUpOption              = "MinTopUp"
	PayAddressOption            = "PayAddress"
	EpayIdOption                = "EpayId"
	EpayKeyOption               = "EpayKey"
	CustomCallbackAddressOption = "CustomCallbackAddress"
	StripeMinTopUpOption        = "StripeMinTopUp"
	StripePriceIdOption         = "StripePriceId"
	StripeUnitPriceOption       = "StripeUnitPrice"
	StripeCurrencyOption        = "StripeCurrency"
	StripePromotionCodesOption  = "StripePromotionCodesEnabled"
	CreemAPIKeyOption           = "CreemApiKey"
	CreemProductsOption         = "CreemProducts"
	CreemTestModeOption         = "CreemTestMode"
	CreemWebhookSecretOption    = "CreemWebhookSecret"
	QuotaDisplayTypeOption      = "QuotaDisplayType"
	PaymentSettingOption        = "PaymentSetting"
)

const (
	maxPayMethodsJSON       = 128 << 10
	maxPayMethods           = 64
	maxPayMethodFields      = 16
	maxPayMethodKeyBytes    = 64
	maxPayMethodValueBytes  = 4096
	maxPayMethodNameBytes   = 128
	maxPayMethodTypeBytes   = 64
	maxPaymentSettingJSON   = 128 << 10
	maxPaymentAmountOptions = 100
	maxPaymentDiscounts     = 100

	maxCreemProducts       = 100
	maxCreemProductsJSON   = 128 << 10
	maxCreemProductIDBytes = 255
	maxCreemProductName    = 255
	maxCreemCredential     = 4096
	maxCreemPriceText      = 64
)

// Provider-facing payment bounds are deliberately lower than the storage
// limits. They keep new checkout amounts representable by every supported
// gateway while leaving existing, already-snapshotted orders readable.
const (
	MaxPaymentProviderAmount = 999_999.99
	MaxPaymentDiscount       = 1.0
	MaxTopUpReferenceAmount  = quotamath.MaxQuota / int64(quotamath.QuotaPerUnit)
)

// CreemProduct is one validated wallet product from CreemProducts. PriceText
// preserves the exact base-10 value that is snapshotted into a pending order;
// Price is only the public/display representation.
type CreemProduct struct {
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Quota     int64   `json:"quota"`
	PriceText string  `json:"-"`
}

// CreemConfig is one coherent, validated view of the four hot-reloadable
// Creem options. Secrets are intentionally never serialized by this type.
type CreemConfig struct {
	APIKey        string         `json:"-"`
	Products      []CreemProduct `json:"products"`
	ProductsRaw   string         `json:"-"`
	TestMode      bool           `json:"test_mode"`
	WebhookSecret string         `json:"-"`
}

type creemProductWire struct {
	ProductID string          `json:"productId"`
	Name      string          `json:"name"`
	Price     json.RawMessage `json:"price"`
	Currency  string          `json:"currency"`
	Quota     int64           `json:"quota"`
}

// QuotaDisplayTypeTokens is the display-type value for token-denominated
// top-up amounts.
const QuotaDisplayTypeTokens = "tokens"

// PaymentSetting mirrors the reference payment-setting structure (amount
// presets and per-amount discounts).
type PaymentSetting struct {
	AmountOptions  []int           `json:"amount_options"`
	AmountDiscount map[int]float64 `json:"amount_discount"`
}

// ErrInvalidPaymentSetting is returned when a configured payment value cannot
// be represented safely or would make a charge non-positive. Payment request
// paths use the checked accessors below and fail closed on this error.
var ErrInvalidPaymentSetting = errors.New("invalid payment setting")

const (
	defaultTopUpPrice      = 7.3
	defaultTopUpMinimum    = int64(1)
	defaultStripeUnitPrice = 7.3
	defaultStripeMinimum   = int64(1)
	defaultStripeCurrency  = "USD"
	defaultPaymentDiscount = 1.0
)

// defaultPayMethods mirrors the reference default payment-method catalog.
var defaultPayMethods = []map[string]string{
	{"name": "支付宝", "icon": "SiAlipay", "type": "alipay"},
	{"name": "微信", "icon": "SiWechat", "type": "wxpay"},
	{"name": "自定义1", "icon": "LuCreditCard", "type": "custom1", "min_topup": "50"},
}

// GetPayMethods returns the configured payment-method catalog.
func GetPayMethods() []map[string]string {
	methods, err := parsePayMethods(GetOption(PayMethodsOption))
	if err != nil {
		return []map[string]string{}
	}
	return methods
}

func parsePayMethods(raw string) ([]map[string]string, error) {
	if raw == "" {
		return clonePayMethods(defaultPayMethods), nil
	}
	if len(raw) > maxPayMethodsJSON || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PayMethodsOption)
	}
	var methods []map[string]string
	if err := jsonutil.UnmarshalJsonStr(raw, &methods); err != nil || methods == nil || len(methods) > maxPayMethods {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PayMethodsOption)
	}
	seen := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		if len(method) == 0 || len(method) > maxPayMethodFields {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PayMethodsOption)
		}
		for key, value := range method {
			if !validPayMethodKey(key) || !validPayMethodText(value, maxPayMethodValueBytes) {
				return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PayMethodsOption)
			}
		}
		name := method["name"]
		methodType := method["type"]
		if name == "" || name != strings.TrimSpace(name) || !validPayMethodText(name, maxPayMethodNameBytes) ||
			!validPayMethodType(methodType) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PayMethodsOption)
		}
		if _, duplicate := seen[methodType]; duplicate {
			return nil, fmt.Errorf("%w: duplicate %s type", ErrInvalidPaymentSetting, PayMethodsOption)
		}
		seen[methodType] = struct{}{}
	}
	return clonePayMethods(methods), nil
}

func clonePayMethods(methods []map[string]string) []map[string]string {
	cloned := make([]map[string]string, len(methods))
	for index, method := range methods {
		cloned[index] = make(map[string]string, len(method))
		for key, value := range method {
			cloned[index][key] = value
		}
	}
	return cloned
}

func validPayMethodKey(value string) bool {
	if value == "" || len(value) > maxPayMethodKeyBytes {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validPayMethodType(value string) bool {
	if value == "" || len(value) > maxPayMethodTypeBytes || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validPayMethodText(value string, maximumBytes int) bool {
	if len(value) > maximumBytes || !utf8.ValidString(value) {
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

// ContainsPayMethod reports whether the catalog contains the method type.
func ContainsPayMethod(method string) bool {
	if !validPayMethodType(method) {
		return false
	}
	for _, payMethod := range GetPayMethods() {
		if payMethod["type"] == method {
			return true
		}
	}
	return false
}

// GetTopUpPrice returns the configured price per money unit.
func GetTopUpPrice() float64 {
	price, err := GetTopUpPriceChecked()
	if err != nil {
		return 0
	}
	return price
}

// GetTopUpPriceChecked returns a finite, positive Epay unit price.
func GetTopUpPriceChecked() (float64, error) {
	return positiveFloatOption(PriceOption, defaultTopUpPrice)
}

// GetQuotaDisplayType returns "tokens" when top-up amounts are token-
// denominated, or "" for money-unit amounts.
func GetQuotaDisplayType() string {
	if GetCurrencyDisplaySetting().Type == CurrencyDisplayTypeTokens {
		return QuotaDisplayTypeTokens
	}
	return ""
}

// GetMinTopUp returns the minimum top-up amount in the configured display
// type.
func GetMinTopUp() int64 {
	minimum, err := GetMinTopUpChecked()
	if err != nil {
		// Keep this compatibility accessor bounded and conservative. Payment
		// request paths call the checked form and reject invalid configuration.
		return quotamath.MaxQuota
	}
	return minimum
}

// GetMinTopUpChecked returns a bounded minimum in the configured display
// unit. The multiplication in token-display mode is checked before it occurs.
func GetMinTopUpChecked() (int64, error) {
	return minimumTopUpOption(MinTopUpOption, defaultTopUpMinimum)
}

// GetStripeMinTopUp returns the Stripe minimum top-up amount in the
// configured display type.
func GetStripeMinTopUp() int64 {
	minimum, err := GetStripeMinTopUpChecked()
	if err != nil {
		return quotamath.MaxQuota
	}
	return minimum
}

// GetStripeMinTopUpChecked is the checked Stripe counterpart of
// GetMinTopUpChecked.
func GetStripeMinTopUpChecked() (int64, error) {
	return minimumTopUpOption(StripeMinTopUpOption, defaultStripeMinimum)
}

// GetPaymentSetting returns the amount presets and discounts.
func GetPaymentSetting() PaymentSetting {
	ps, err := GetPaymentSettingChecked()
	if err != nil {
		return defaultPaymentSetting()
	}
	return ps
}

// GetPaymentSettingChecked rejects malformed settings and any non-finite or
// non-positive discount instead of silently changing the price calculation.
func GetPaymentSettingChecked() (PaymentSetting, error) {
	ps := defaultPaymentSetting()
	raw := GetOption(PaymentSettingOption)
	if raw == "" {
		return ps, nil
	}
	if len(raw) > maxPaymentSettingJSON || !utf8.ValidString(raw) {
		return PaymentSetting{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PaymentSettingOption)
	}
	if err := jsonutil.ValidateJSONNoDuplicateKeys([]byte(raw)); err != nil {
		return PaymentSetting{}, fmt.Errorf("%w: %s: %v", ErrInvalidPaymentSetting, PaymentSettingOption, err)
	}
	var stored PaymentSetting
	if err := jsonutil.UnmarshalJsonStr(raw, &stored); err != nil {
		return PaymentSetting{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PaymentSettingOption)
	}
	if len(stored.AmountOptions) > maxPaymentAmountOptions || len(stored.AmountDiscount) > maxPaymentDiscounts {
		return PaymentSetting{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, PaymentSettingOption)
	}
	if stored.AmountOptions != nil {
		ps.AmountOptions = stored.AmountOptions
	}
	if stored.AmountDiscount != nil {
		ps.AmountDiscount = stored.AmountDiscount
	}
	seenAmounts := make(map[int]struct{}, len(ps.AmountOptions))
	for _, amount := range ps.AmountOptions {
		if amount <= 0 || int64(amount) > quotamath.MaxQuota {
			return PaymentSetting{}, fmt.Errorf("%w: %s amount %d", ErrInvalidPaymentSetting, PaymentSettingOption, amount)
		}
		if _, duplicate := seenAmounts[amount]; duplicate {
			return PaymentSetting{}, fmt.Errorf("%w: duplicate %s amount", ErrInvalidPaymentSetting, PaymentSettingOption)
		}
		seenAmounts[amount] = struct{}{}
	}
	for amount, discount := range ps.AmountDiscount {
		if amount <= 0 || int64(amount) > quotamath.MaxQuota || !isFinitePositive(discount) || discount > MaxPaymentDiscount {
			return PaymentSetting{}, fmt.Errorf("%w: %s discount for amount %d", ErrInvalidPaymentSetting, PaymentSettingOption, amount)
		}
	}
	return ps, nil
}

// GetPaymentDiscount returns the validated discount for an exact requested
// amount, defaulting to 1 when that amount has no configured preset.
func GetPaymentDiscount(amount int64) (float64, error) {
	if amount <= 0 || amount > quotamath.MaxQuota {
		return 0, fmt.Errorf("%w: top-up amount", ErrInvalidPaymentSetting)
	}
	ps, err := GetPaymentSettingChecked()
	if err != nil {
		return 0, err
	}
	if discount, ok := ps.AmountDiscount[int(amount)]; ok {
		return discount, nil
	}
	return defaultPaymentDiscount, nil
}

// GetCallbackAddress returns the address payment providers should call back
// (the custom callback address when configured, otherwise the server
// address).
func GetCallbackAddress() string {
	if custom := GetOption(CustomCallbackAddressOption); custom != "" {
		return custom
	}
	return GetOption(ServerAddressOption)
}

// EpayConfigured reports whether the Epay gateway credentials are set.
func EpayConfigured() bool {
	return GetOption(PayAddressOption) != "" && GetOption(EpayIdOption) != "" && GetOption(EpayKeyOption) != ""
}

// StripeConfigured reports whether both request and webhook credentials are
// present and the API secret has a recognized restricted/secret key prefix.
func StripeConfigured() bool {
	secret := env.GetEnv("STRIPE_SECRET_KEY", "")
	validSecret := false
	if secret == strings.TrimSpace(secret) {
		for _, prefix := range []string{"sk_test_", "sk_live_", "rk_test_", "rk_live_"} {
			if strings.HasPrefix(secret, prefix) && len(secret) > len(prefix) {
				validSecret = true
				break
			}
		}
	}
	return validSecret && strings.TrimSpace(env.GetEnv("STRIPE_WEBHOOK_SECRET", "")) != ""
}

// GetCreemConfigChecked returns a coherent configuration snapshot. A caller
// either sees the complete old option set or the complete new one; malformed
// externally-written values disable the provider instead of being defaulted.
func GetCreemConfigChecked() (CreemConfig, error) {
	return buildCreemConfig(GetOptions(
		CreemAPIKeyOption,
		CreemProductsOption,
		CreemTestModeOption,
		CreemWebhookSecretOption,
	))
}

// CreemTopUpConfigured reports whether wallet checkout and signed callbacks
// are both usable. Subscription initiation intentionally shares this gate,
// matching the reference webhook availability contract.
func CreemTopUpConfigured() bool {
	config, err := GetCreemConfigChecked()
	return err == nil && config.APIKey != "" && config.WebhookSecret != "" && len(config.Products) > 0
}

// FindCreemProduct returns a defensive copy of a configured product.
func (c CreemConfig) FindCreemProduct(productID string) (CreemProduct, bool) {
	for _, product := range c.Products {
		if product.ProductID == productID {
			return product, true
		}
	}
	return CreemProduct{}, false
}

func buildCreemConfig(options map[string]string) (CreemConfig, error) {
	config := CreemConfig{ProductsRaw: "[]"}
	apiKey := options[CreemAPIKeyOption]
	webhookSecret := options[CreemWebhookSecretOption]
	if !validCreemCredential(apiKey) || !validCreemCredential(webhookSecret) {
		return CreemConfig{}, fmt.Errorf("%w: Creem credentials", ErrInvalidPaymentSetting)
	}
	config.APIKey = apiKey
	config.WebhookSecret = webhookSecret

	switch options[CreemTestModeOption] {
	case "", "false":
		config.TestMode = false
	case "true":
		config.TestMode = true
	default:
		return CreemConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, CreemTestModeOption)
	}

	storedProducts := options[CreemProductsOption]
	if len(storedProducts) > maxCreemProductsJSON {
		return CreemConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, CreemProductsOption)
	}
	rawProducts := strings.TrimSpace(storedProducts)
	if rawProducts == "" {
		rawProducts = "[]"
	}
	if rawProducts == "null" {
		return CreemConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, CreemProductsOption)
	}
	var wires []creemProductWire
	if err := jsonutil.Unmarshal([]byte(rawProducts), &wires); err != nil || len(wires) > maxCreemProducts {
		return CreemConfig{}, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, CreemProductsOption)
	}
	seen := make(map[string]struct{}, len(wires))
	products := make([]CreemProduct, 0, len(wires))
	for _, wire := range wires {
		productID := wire.ProductID
		name := wire.Name
		currency := wire.Currency
		priceText := strings.TrimSpace(string(wire.Price))
		if productID == "" || productID != strings.TrimSpace(productID) || len(productID) > maxCreemProductIDBytes ||
			strings.ContainsRune(productID, '\x00') || name == "" || name != strings.TrimSpace(name) ||
			len(name) > maxCreemProductName || strings.ContainsRune(name, '\x00') || wire.Quota <= 0 || wire.Quota > quotamath.MaxQuota {
			return CreemConfig{}, fmt.Errorf("%w: %s product", ErrInvalidPaymentSetting, CreemProductsOption)
		}
		if _, duplicate := seen[productID]; duplicate {
			return CreemConfig{}, fmt.Errorf("%w: duplicate Creem product", ErrInvalidPaymentSetting)
		}
		seen[productID] = struct{}{}
		if len(currency) != 3 || currency != strings.TrimSpace(currency) {
			return CreemConfig{}, fmt.Errorf("%w: Creem currency", ErrInvalidPaymentSetting)
		}
		for i := range len(currency) {
			char := currency[i]
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
				return CreemConfig{}, fmt.Errorf("%w: Creem currency", ErrInvalidPaymentSetting)
			}
		}
		if !plainPositiveCreemDecimal(priceText) {
			return CreemConfig{}, fmt.Errorf("%w: Creem price", ErrInvalidPaymentSetting)
		}
		price, err := strconv.ParseFloat(priceText, 64)
		if err != nil || !isFinitePositive(price) || price > MaxPaymentProviderAmount {
			return CreemConfig{}, fmt.Errorf("%w: Creem price", ErrInvalidPaymentSetting)
		}
		products = append(products, CreemProduct{
			ProductID: productID,
			Name:      name,
			Price:     price,
			Currency:  strings.ToUpper(currency),
			Quota:     wire.Quota,
			PriceText: priceText,
		})
	}
	config.Products = products
	config.ProductsRaw = rawProducts
	return config, nil
}

func plainPositiveCreemDecimal(value string) bool {
	if value == "" || len(value) > maxCreemPriceText {
		return false
	}
	digits := 0
	dotSeen := false
	for i := range len(value) {
		switch char := value[i]; {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' && !dotSeen:
			dotSeen = true
		default:
			return false
		}
	}
	return digits > 0
}

func validCreemCredential(value string) bool {
	if value == "" {
		return true
	}
	return value == strings.TrimSpace(value) && len(value) <= maxCreemCredential && !strings.ContainsRune(value, '\x00')
}

func isCreemOptionKey(key string) bool {
	switch key {
	case CreemAPIKeyOption, CreemProductsOption, CreemTestModeOption, CreemWebhookSecretOption:
		return true
	default:
		return false
	}
}

// GetStripeUnitPrice returns the Stripe unit price per top-up unit.
func GetStripeUnitPrice() float64 {
	price, err := GetStripeUnitPriceChecked()
	if err != nil {
		return 0
	}
	return price
}

// GetStripeUnitPriceChecked returns a finite, positive Stripe unit price.
func GetStripeUnitPriceChecked() (float64, error) {
	return positiveFloatOption(StripeUnitPriceOption, defaultStripeUnitPrice)
}

// GetStripeCurrencyChecked returns the three-letter currency captured on new
// Stripe top-up orders. The order snapshot, rather than this mutable option,
// is authoritative when the webhook is later processed.
func GetStripeCurrencyChecked() (string, error) {
	currency := GetOption(StripeCurrencyOption)
	if currency == "" {
		currency = defaultStripeCurrency
	}
	if len(currency) != 3 {
		return "", fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, StripeCurrencyOption)
	}
	for i := range len(currency) {
		char := currency[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
			return "", fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, StripeCurrencyOption)
		}
	}
	return strings.ToUpper(currency), nil
}

// GetStripePromotionCodesEnabled reports whether Stripe promotion codes are
// enabled for checkout sessions.
func GetStripePromotionCodesEnabled() bool {
	raw := GetOption(StripePromotionCodesOption)
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false
	}
	return enabled
}

func defaultPaymentSetting() PaymentSetting {
	return PaymentSetting{
		AmountOptions:  []int{10, 20, 50, 100, 200, 500},
		AmountDiscount: map[int]float64{},
	}
}

func positiveFloatOption(key string, defaultValue float64) (float64, error) {
	raw := GetOption(key)
	if raw == "" {
		return defaultValue, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 64 {
		return 0, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || !isFinitePositive(value) || value > MaxPaymentProviderAmount {
		return 0, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
	}
	return value, nil
}

func minimumTopUpOption(key string, defaultValue int64) (int64, error) {
	raw := GetOption(key)
	minimum := defaultValue
	if raw != "" {
		if raw != strings.TrimSpace(raw) || len(raw) > 32 {
			return 0, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
		}
		minimum = parsed
	}
	if minimum < 1 || minimum > MaxTopUpReferenceAmount {
		return 0, fmt.Errorf("%w: %s", ErrInvalidPaymentSetting, key)
	}
	if GetQuotaDisplayType() != QuotaDisplayTypeTokens {
		return minimum, nil
	}
	return minimum * int64(quotamath.QuotaPerUnit), nil
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
