package setting

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestCurrencyDisplaySettingSupportsBoundedCustomCurrency(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		QuotaDisplayTypeOption:           "CUSTOM",
		USDExchangeRateOption:            "7.25",
		CustomCurrencySymbolOption:       "HK$",
		CustomCurrencyExchangeRateOption: "7.8",
	}))

	config, err := GetCurrencyDisplaySettingChecked()
	require.NoError(t, err)
	assert.Equal(t, CurrencyDisplayTypeCustom, config.Type)
	assert.Equal(t, "HK$", config.Symbol)
	assert.Equal(t, 7.25, config.USDExchangeRate)
	assert.Equal(t, 7.8, config.CurrencyExchangeRate())

	require.NoError(t, UpdateOption(QuotaDisplayTypeOption, "cNy"))
	config, err = GetCurrencyDisplaySettingChecked()
	require.NoError(t, err)
	assert.Equal(t, CurrencyDisplayTypeCNY, config.Type)
	assert.Equal(t, "¥", config.Symbol)
	assert.Equal(t, 7.25, config.CurrencyExchangeRate())

	require.NoError(t, UpdateOption(QuotaDisplayTypeOption, "TOKENS"))
	assert.Equal(t, QuotaDisplayTypeTokens, GetQuotaDisplayType())
}

func TestCurrencyDisplaySettingSanitizesCorruptedMetadata(t *testing.T) {
	setupAffinitySettingTest(t)
	for _, raw := range []string{"0", "-1", "NaN", "+Inf", "1000001", strings.Repeat("9", 65)} {
		require.NoError(t, UpdateOptions(map[string]string{
			QuotaDisplayTypeOption: "CNY",
			USDExchangeRateOption:  raw,
		}))
		_, err := GetCurrencyDisplaySettingChecked()
		require.Error(t, err, raw)
		sanitized := GetCurrencyDisplaySetting()
		assert.Equal(t, CurrencyDisplayTypeCNY, sanitized.Type, raw)
		assert.Equal(t, defaultUSDExchangeRate, sanitized.CurrencyExchangeRate(), raw)
	}

	require.NoError(t, UpdateOptions(map[string]string{
		QuotaDisplayTypeOption:           "CUSTOM",
		USDExchangeRateOption:            "7.3",
		CustomCurrencyExchangeRateOption: "0",
		CustomCurrencySymbolOption:       "\u202e$",
	}))
	_, err := GetCurrencyDisplaySettingChecked()
	require.Error(t, err)
	sanitized := GetCurrencyDisplaySetting()
	assert.Equal(t, CurrencyDisplayTypeCustom, sanitized.Type)
	assert.Equal(t, defaultCustomCurrencySymbol, sanitized.Symbol)
	assert.Equal(t, defaultCustomCurrencyExchangeRate, sanitized.CurrencyExchangeRate())
}

func TestValidateBillingOptionUpdateRejectsUnsafeValues(t *testing.T) {
	valid := map[string]string{
		InitialQuotaOption:               strconv.FormatInt(common.MaxQuota, 10),
		QuotaDisplayTypeOption:           "CUSTOM",
		USDExchangeRateOption:            "7.3",
		CustomCurrencySymbolOption:       "€",
		CustomCurrencyExchangeRateOption: "0.91",
		TopUpLinkOption:                  "https://billing.example.test/codes?from=wallet",
		PayAddressOption:                 "https://pay.example.test/base",
		PaymentSettingOption:             `{"amount_options":[10],"amount_discount":{"10":0.9}}`,
		PriceOption:                      "999999.99",
		MinTopUpOption:                   strconv.FormatInt(MaxTopUpReferenceAmount, 10),
		DisplayTokenStatEnabledOption:    "false",
	}
	for key, value := range valid {
		assert.NoError(t, ValidateBillingOptionUpdate(key, value), key)
	}

	invalid := map[string]string{
		InitialQuotaOption:               strconv.FormatInt(common.MaxQuota+1, 10),
		PreConsumedQuotaOption:           "-1",
		QuotaDisplayTypeOption:           "credits",
		USDExchangeRateOption:            strconv.FormatFloat(math.Inf(1), 'g', -1, 64),
		CustomCurrencySymbolOption:       strings.Repeat("x", 9),
		CustomCurrencyExchangeRateOption: "0",
		TopUpLinkOption:                  "javascript:alert(1)",
		PayAddressOption:                 "http://pay.example.test",
		PaymentSettingOption:             `{"amount_options":[10,10]}`,
		PriceOption:                      "1000000",
		MinTopUpOption:                   strconv.FormatInt(MaxTopUpReferenceAmount+1, 10),
		StripeCurrencyOption:             "US1",
		StripePromotionCodesOption:       "yes",
	}
	for key, value := range invalid {
		assert.Error(t, ValidateBillingOptionUpdate(key, value), key)
	}
	assert.Error(t, ValidateBillingOptionUpdate(PaymentSettingOption,
		`{"amount_options":[10],"amount_options":[20]}`))
	assert.Error(t, ValidateBillingOptionUpdate(PaymentSettingOption,
		`{"amount_discount":{"10":0.9,"10":0.8}}`))
	assert.Error(t, ValidateBillingOptionUpdate(PaymentSettingOption,
		`{"amount_discount":{"10":1.01}}`))
}

func TestTopUpLinkOnlyAdvertisesSafeConfiguredURL(t *testing.T) {
	setupAffinitySettingTest(t)
	assert.Empty(t, GetTopUpLink())
	require.NoError(t, UpdateOption(TopUpLinkOption, "https://billing.example.test/codes?campaign=wallet"))
	assert.Equal(t, "https://billing.example.test/codes?campaign=wallet", GetTopUpLink())
	require.NoError(t, UpdateOption(TopUpLinkOption, "javascript:alert(1)"))
	assert.Empty(t, GetTopUpLink())
}
