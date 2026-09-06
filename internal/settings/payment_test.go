package settings

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestPayMethodsValidationIsBoundedAtomicAndDefensive(t *testing.T) {
	setupAffinitySettingTest(t)

	defaults := GetPayMethods()
	require.Len(t, defaults, 3)
	defaults[0]["name"] = "mutated"
	assert.NotEqual(t, "mutated", GetPayMethods()[0]["name"])

	const valid = `[{"name":"Card","type":"card","icon":"/card.svg","min_topup":"10"}]`
	require.NoError(t, UpdateOption(PayMethodsOption, valid))
	require.Equal(t, "Card", GetPayMethods()[0]["name"])
	assert.True(t, ContainsPayMethod("card"))
	for _, method := range []string{"", " card", "card other", "card\u202e", strings.Repeat("x", maxPayMethodTypeBytes+1)} {
		assert.False(t, ContainsPayMethod(method), method)
	}

	for _, invalid := range []string{
		`null`,
		`{}`,
		`[{"type":"card"}]`,
		`[{"name":"Card","type":"card"},{"name":"Duplicate","type":"card"}]`,
		`[{"name":"Unsafe\u202eName","type":"card"}]`,
		`[{"name":"Card","type":"card other"}]`,
		strings.Repeat("x", maxPayMethodsJSON+1),
	} {
		require.Error(t, UpdateOption(PayMethodsOption, invalid), invalid)
		assert.Equal(t, valid, GetOption(PayMethodsOption), invalid)
		assert.Equal(t, "Card", GetPayMethods()[0]["name"], invalid)
	}

	// A malformed remote write cannot replace the last coherent in-memory
	// snapshot during a multi-node synchronization.
	require.NoError(t, model.DB.Model(&model.Option{}).Where("key = ?", PayMethodsOption).
		Update("value", `[{"name":"Missing type"}]`).Error)
	require.Error(t, Sync())
	assert.Equal(t, "Card", GetPayMethods()[0]["name"])
}

func TestPaymentNumericOptionsRejectUnsafeValues(t *testing.T) {
	setupAffinitySettingTest(t)

	for _, key := range []string{PriceOption, StripeUnitPriceOption} {
		for _, value := range []string{"0", "-1", "NaN", "+Inf", "1e309", "1000000", " 1", "not-a-number"} {
			require.NoError(t, UpdateOption(key, value))
			var err error
			if key == PriceOption {
				_, err = GetTopUpPriceChecked()
			} else {
				_, err = GetStripeUnitPriceChecked()
			}
			assert.ErrorIs(t, err, ErrInvalidPaymentSetting, "%s=%s", key, value)
		}
	}

	require.NoError(t, UpdateOption(PriceOption, "2.5"))
	price, err := GetTopUpPriceChecked()
	require.NoError(t, err)
	assert.Equal(t, 2.5, price)
	require.NoError(t, UpdateOption(PriceOption, "999999.99"))
	price, err = GetTopUpPriceChecked()
	require.NoError(t, err)
	assert.Equal(t, MaxPaymentProviderAmount, price)
}

func TestPaymentDiscountRejectsMalformedAndNonPositiveValues(t *testing.T) {
	setupAffinitySettingTest(t)

	for _, value := range []string{
		`{"amount_discount":{"10":0}}`,
		`{"amount_discount":{"10":-0.5}}`,
		`{"amount_discount":{"10":1e309}}`,
		`{"amount_discount":{"10":1.01}}`,
		`{"amount_discount":{"0":0.5}}`,
		`{"amount_discount":{"10":0.9,"10":0.8}}`,
		`{"amount_options":[10],"amount_options":[20]}`,
		`not-json`,
	} {
		require.NoError(t, UpdateOption(PaymentSettingOption, value))
		_, err := GetPaymentSettingChecked()
		assert.ErrorIs(t, err, ErrInvalidPaymentSetting, value)
	}

	require.NoError(t, UpdateOption(PaymentSettingOption, `{"amount_discount":{"10":0.5}}`))
	discount, err := GetPaymentDiscount(10)
	require.NoError(t, err)
	assert.Equal(t, 0.5, discount)
	discount, err = GetPaymentDiscount(20)
	require.NoError(t, err)
	assert.Equal(t, 1.0, discount)
}

func TestPaymentSettingBoundsAmountsAndCardinality(t *testing.T) {
	setupAffinitySettingTest(t)

	tooManyAmounts := `{"amount_options":[` + strings.Repeat("1,", maxPaymentAmountOptions) + `2]}`
	for _, value := range []string{
		`{"amount_options":[0]}`,
		`{"amount_options":[10,10]}`,
		tooManyAmounts,
		strings.Repeat("x", maxPaymentSettingJSON+1),
	} {
		require.NoError(t, UpdateOption(PaymentSettingOption, value))
		_, err := GetPaymentSettingChecked()
		assert.ErrorIs(t, err, ErrInvalidPaymentSetting, value)
	}

	require.NoError(t, UpdateOption(PaymentSettingOption,
		`{"amount_options":[10,20],"amount_discount":{"10":0.9}}`))
	configured, err := GetPaymentSettingChecked()
	require.NoError(t, err)
	assert.Equal(t, []int{10, 20}, configured.AmountOptions)
	assert.Equal(t, 0.9, configured.AmountDiscount[10])
}

func TestPaymentMinimumTokenConversionIsOverflowSafe(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(QuotaDisplayTypeOption, QuotaDisplayTypeTokens))

	largestSafe := quotamath.MaxQuota / int64(quotamath.QuotaPerUnit)
	for _, key := range []string{MinTopUpOption, StripeMinTopUpOption} {
		require.NoError(t, UpdateOption(key, strconv.FormatInt(largestSafe, 10)))
		var (
			minimum int64
			err     error
		)
		if key == MinTopUpOption {
			minimum, err = GetMinTopUpChecked()
		} else {
			minimum, err = GetStripeMinTopUpChecked()
		}
		require.NoError(t, err)
		assert.Equal(t, largestSafe*int64(quotamath.QuotaPerUnit), minimum)

		require.NoError(t, UpdateOption(key, strconv.FormatInt(largestSafe+1, 10)))
		if key == MinTopUpOption {
			_, err = GetMinTopUpChecked()
		} else {
			_, err = GetStripeMinTopUpChecked()
		}
		assert.True(t, errors.Is(err, ErrInvalidPaymentSetting))
	}

	require.NoError(t, UpdateOption(MinTopUpOption, strconv.FormatInt(math.MaxInt64, 10)))
	_, err := GetMinTopUpChecked()
	assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
}

func TestPaymentMinimumIsReachableInMoneyDisplayMode(t *testing.T) {
	setupAffinitySettingTest(t)
	for _, key := range []string{MinTopUpOption, StripeMinTopUpOption} {
		require.NoError(t, UpdateOption(key, strconv.FormatInt(MaxTopUpReferenceAmount, 10)))
		assert.NoError(t, ValidateBillingOptionUpdate(key, strconv.FormatInt(MaxTopUpReferenceAmount, 10)))
		require.NoError(t, UpdateOption(key, strconv.FormatInt(MaxTopUpReferenceAmount+1, 10)))
		assert.Error(t, ValidateBillingOptionUpdate(key, strconv.FormatInt(MaxTopUpReferenceAmount+1, 10)))
		if key == MinTopUpOption {
			_, err := GetMinTopUpChecked()
			assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
		} else {
			_, err := GetStripeMinTopUpChecked()
			assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
		}
		require.NoError(t, UpdateOption(key, " 1"))
		if key == MinTopUpOption {
			_, err := GetMinTopUpChecked()
			assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
		} else {
			_, err := GetStripeMinTopUpChecked()
			assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
		}
	}
}

func TestStripeCurrencyChecked(t *testing.T) {
	setupAffinitySettingTest(t)

	currency, err := GetStripeCurrencyChecked()
	require.NoError(t, err)
	assert.Equal(t, "USD", currency)

	require.NoError(t, UpdateOption(StripeCurrencyOption, "eur"))
	currency, err = GetStripeCurrencyChecked()
	require.NoError(t, err)
	assert.Equal(t, "EUR", currency)

	for _, invalid := range []string{"US", "US1", " usd", "\u00a5\u00a5\u00a5"} {
		require.NoError(t, UpdateOption(StripeCurrencyOption, invalid))
		_, err = GetStripeCurrencyChecked()
		assert.ErrorIs(t, err, ErrInvalidPaymentSetting)
	}
}
