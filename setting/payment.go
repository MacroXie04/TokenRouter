package setting

import (
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
)

// Payment-related option keys (reference contract). Credentials that the
// reference stores as options remain environment variables in TokenRouter
// (STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET — deviation #8).
const (
	PayMethodsOption             = "PayMethods"
	PriceOption                  = "Price"
	MinTopUpOption               = "MinTopUp"
	PayAddressOption             = "PayAddress"
	EpayIdOption                 = "EpayId"
	EpayKeyOption                = "EpayKey"
	CustomCallbackAddressOption  = "CustomCallbackAddress"
	StripeMinTopUpOption         = "StripeMinTopUp"
	StripePriceIdOption          = "StripePriceId"
	StripeUnitPriceOption        = "StripeUnitPrice"
	StripePromotionCodesOption   = "StripePromotionCodesEnabled"
	QuotaDisplayTypeOption       = "QuotaDisplayType"
	PaymentSettingOption         = "PaymentSetting"
)

// QuotaDisplayTypeTokens is the display-type value for token-denominated
// top-up amounts.
const QuotaDisplayTypeTokens = "tokens"

// PaymentSetting mirrors the reference payment-setting structure (amount
// presets and per-amount discounts).
type PaymentSetting struct {
	AmountOptions  []int           `json:"amount_options"`
	AmountDiscount map[int]float64 `json:"amount_discount"`
}

// defaultPayMethods mirrors the reference default payment-method catalog.
var defaultPayMethods = []map[string]string{
	{"name": "支付宝", "icon": "SiAlipay", "type": "alipay"},
	{"name": "微信", "icon": "SiWechat", "type": "wxpay"},
	{"name": "自定义1", "icon": "LuCreditCard", "type": "custom1", "min_topup": "50"},
}

// GetPayMethods returns the configured payment-method catalog.
func GetPayMethods() []map[string]string {
	raw := GetOption(PayMethodsOption)
	if raw == "" {
		return defaultPayMethods
	}
	var methods []map[string]string
	if err := common.UnmarshalJsonStr(raw, &methods); err != nil {
		return defaultPayMethods
	}
	return methods
}

// ContainsPayMethod reports whether the catalog contains the method type.
func ContainsPayMethod(method string) bool {
	for _, payMethod := range GetPayMethods() {
		if payMethod["type"] == method {
			return true
		}
	}
	return false
}

// GetTopUpPrice returns the configured price per money unit.
func GetTopUpPrice() float64 {
	return GetOptionFloatOrDefault(PriceOption, 7.3)
}

// GetQuotaDisplayType returns "tokens" when top-up amounts are token-
// denominated, or "" for money-unit amounts.
func GetQuotaDisplayType() string {
	if GetOption(QuotaDisplayTypeOption) == QuotaDisplayTypeTokens {
		return QuotaDisplayTypeTokens
	}
	return ""
}

// GetMinTopUp returns the minimum top-up amount in the configured display
// type.
func GetMinTopUp() int64 {
	minTopup := int64(GetOptionIntOrDefault(MinTopUpOption, 1))
	if GetQuotaDisplayType() == QuotaDisplayTypeTokens {
		minTopup = int64(common.QuotaFromDecimal(decimal.NewFromInt(minTopup).Mul(decimal.NewFromFloat(common.QuotaPerUnit))))
	}
	return minTopup
}

// GetStripeMinTopUp returns the Stripe minimum top-up amount in the
// configured display type.
func GetStripeMinTopUp() int64 {
	minTopup := int64(GetOptionIntOrDefault(StripeMinTopUpOption, 1))
	if GetQuotaDisplayType() == QuotaDisplayTypeTokens {
		minTopup = minTopup * int64(common.QuotaPerUnit)
	}
	return minTopup
}

// GetPaymentSetting returns the amount presets and discounts.
func GetPaymentSetting() PaymentSetting {
	ps := PaymentSetting{AmountOptions: []int{10, 20, 50, 100, 200, 500}, AmountDiscount: map[int]float64{}}
	raw := GetOption(PaymentSettingOption)
	if raw == "" {
		return ps
	}
	var stored PaymentSetting
	if err := common.UnmarshalJsonStr(raw, &stored); err != nil {
		return ps
	}
	if stored.AmountOptions != nil {
		ps.AmountOptions = stored.AmountOptions
	}
	if stored.AmountDiscount != nil {
		ps.AmountDiscount = stored.AmountDiscount
	}
	return ps
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

// StripeConfigured reports whether a Stripe API secret is present.
func StripeConfigured() bool {
	return common.GetEnv("STRIPE_SECRET_KEY", "") != ""
}

// GetStripeUnitPrice returns the Stripe unit price per top-up unit.
func GetStripeUnitPrice() float64 {
	return GetOptionFloatOrDefault(StripeUnitPriceOption, 7.3)
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
