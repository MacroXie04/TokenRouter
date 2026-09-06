package billing

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"path/filepath"
	"strconv"
	"testing"
)

func setupTopUpPricingTest(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "topup-pricing.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.TopUp{}, &model.AuditLogOutbox{}))
	model.DB = db
	require.NoError(t, setting.Init())
	previousRatios := getGroupRatios()
	previousTopUpRatios := getTopUpGroupRatios()
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	SetTopUpGroupRatios(map[string]float64{"default": 1, "vip": 2})
	t.Cleanup(func() {
		SetGroupRatios(previousRatios)
		SetTopUpGroupRatios(previousTopUpRatios)
	})
}

func TestTopUpPricingPreservesReferenceFormula(t *testing.T) {
	setupTopUpPricingTest(t)
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "2"))
	require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "3"))
	require.NoError(t, setting.UpdateOption(setting.PaymentSettingOption,
		`{"amount_discount":{"10":0.5}}`))

	money, err := GetTopupMoney(10, "vip")
	require.NoError(t, err)
	assert.Equal(t, 20.0, money, "10 * 2 price * 2 group * 0.5 discount")
	stripeMoney, err := GetStripeTopupMoney(10, "vip")
	require.NoError(t, err)
	assert.Equal(t, 30.0, stripeMoney, "10 * 3 price * 2 group * 0.5 discount")

	require.NoError(t, setting.UpdateOption(setting.PaymentSettingOption, ""))
	require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, setting.QuotaDisplayTypeTokens))
	money, err = GetTopupMoney(2*quotamath.QuotaPerUnit, "vip")
	require.NoError(t, err)
	assert.Equal(t, 8.0, money, "token display divides before applying price and group")
}

func TestTopUpPricingRejectsInvalidGroupRatiosAndOverflow(t *testing.T) {
	setupTopUpPricingTest(t)
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "2"))

	for name, ratio := range map[string]float64{
		"zero":     0,
		"negative": -1,
		"NaN":      math.NaN(),
		"infinite": math.Inf(1),
	} {
		t.Run(name, func(t *testing.T) {
			SetTopUpGroupRatios(map[string]float64{"default": ratio})
			_, err := GetTopupMoney(10, "default")
			assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
		})
	}

	SetTopUpGroupRatios(map[string]float64{"default": 1})
	require.NoError(t, setting.UpdateOption(setting.PriceOption,
		strconv.FormatFloat(math.MaxFloat64, 'g', -1, 64)))
	_, err := GetTopupMoney(quotamath.MaxQuota, "default")
	assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
}

func TestTopUpMoneyNormalizationAndExactComparison(t *testing.T) {
	normalized, wire, err := NormalizePayMoney(10)
	require.NoError(t, err)
	assert.Equal(t, 10.0, normalized)
	assert.Equal(t, "10.00", wire)
	assert.True(t, TopUpMoneyMatches(normalized, "10.0"))
	assert.False(t, TopUpMoneyMatches(normalized, "9.99"))
	assert.False(t, TopUpMoneyMatches(normalized, "NaN"))
	assert.False(t, TopUpMoneyMatches(normalized, "1e-2147483648"), "extreme exponents must fail before decimal rescaling")

	_, _, err = NormalizePayMoney(math.Inf(1))
	assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
	normalized, wire, err = NormalizePayMoney(setting.MaxPaymentProviderAmount)
	require.NoError(t, err)
	assert.Equal(t, setting.MaxPaymentProviderAmount, normalized)
	assert.Equal(t, "999999.99", wire)
	_, _, err = NormalizePayMoney(1_000_000)
	assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
}

func TestCreateTopUpRejectsZeroMoney(t *testing.T) {
	setupTopUpPricingTest(t)
	_, err := CreateTopUpWithTradeNo(1, 1, 0, "alipay", PaymentProviderEpay, "zero-money")
	assert.ErrorIs(t, err, ErrTopUpAmountMismatch)
	_, err = CreateTopUpWithTradeNo(1, 1, 1_000_000, "alipay", PaymentProviderEpay, "huge-money")
	assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
}

func TestStripeMoneyToMinorUnitsRejectsAmbiguousOrUnsafeFormats(t *testing.T) {
	for input, expected := range map[string]int64{
		"0":          0,
		"10":         1000,
		"10.0":       1000,
		"10.00":      1000,
		"10.0000000": 1000,
		"0.01":       1,
	} {
		actual, err := StripeMoneyToMinorUnits(input)
		require.NoError(t, err, input)
		assert.Equal(t, expected, actual, input)
	}
	for _, input := range []string{
		"", " 10.00", "+10.00", "-1.00", "1e2", "1.001", ".", "NaN",
		"92233720368547758.08", "99999999999999999999999999999999999999999999999999999999999999999",
	} {
		_, err := StripeMoneyToMinorUnits(input)
		assert.ErrorIs(t, err, ErrStripePaymentMismatch, input)
	}
}

func TestStripeMoneyToMinorUnitsUsesExplicitCurrencyExponent(t *testing.T) {
	tests := []struct {
		money    string
		currency string
		expected int64
	}{
		{money: "12.34", currency: "USD", expected: 1234},
		{money: "123", currency: "JPY", expected: 123},
		{money: "1.234", currency: "KWD", expected: 1234},
	}
	for _, test := range tests {
		actual, err := StripeMoneyToMinorUnitsForCurrency(test.money, test.currency)
		require.NoError(t, err)
		assert.Equal(t, test.expected, actual)
	}
	_, err := StripeMoneyToMinorUnitsForCurrency("1.23", "JPY")
	assert.ErrorIs(t, err, ErrStripePaymentMismatch)
	_, err = StripeMoneyToMinorUnitsForCurrency("1.00", "ZZZ")
	assert.ErrorIs(t, err, ErrStripePaymentMismatch)
}

func TestCompleteStripeTopUpBindsAmountAndCurrencyInsideSettlement(t *testing.T) {
	setupTopUpPricingTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Log{}))
	model.LOG_DB = model.DB
	user := model.User{Username: "stripe-bound-topup", Password: "x", Status: 1, Role: 1, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	order, err := CreateStripeTopUpWithTradeNo(user.Id, 100, 12.345, "usd", "stripe-bound-order")
	require.NoError(t, err)
	assert.Equal(t, 12.35, order.Money)
	assert.Equal(t, int64(1235), order.ProviderAmountMinor)

	wrongAmount := int64(1234)
	require.ErrorIs(t, CompleteStripeTopUp(user.Id, order.TradeNo, order.Amount, &wrongAmount, "usd"), ErrStripePaymentMismatch)
	correctAmount := int64(1235)
	require.ErrorIs(t, CompleteStripeTopUp(user.Id, order.TradeNo, order.Amount, &correctAmount, "eur"), ErrStripePaymentMismatch)

	var stored model.TopUp
	var storedUser model.User
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Zero(t, storedUser.Quota)

	require.NoError(t, CompleteStripeTopUp(user.Id, order.TradeNo, order.Amount, &correctAmount, "USD"))
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.Equal(t, 100*quotamath.QuotaPerUnit, storedUser.Quota)
}

func TestBoundStripeTopUpRequiresExactSessionAndQuarantinesLegacyPending(t *testing.T) {
	setupTopUpPricingTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Log{}))
	model.LOG_DB = model.DB
	user := model.User{Username: "stripe-session-bound-topup", Password: "x", Status: 1, Role: 1, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)

	order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 100, 10, "USD", "ref_bound_wallet")
	require.NoError(t, err)
	require.NoError(t, BindStripeTopUpSession(order.TradeNo, "cs_wallet_expected"))
	amount := int64(1000)
	require.ErrorIs(t, CompleteBoundStripeTopUpOrder(order.TradeNo, "cs_wallet_other", "payment", "wallet", &amount, "usd"), ErrStripeCheckoutBindingMismatch)
	var pending model.TopUp
	require.NoError(t, model.DB.First(&pending, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, pending.Status)
	assert.Equal(t, StripeReconciliationBindingMismatch, pending.ReconciliationState)

	legacy, err := CreateStripeTopUpWithTradeNo(user.Id, 50, 5, "USD", "ref_legacy_wallet_pending")
	require.NoError(t, err)
	legacyAmount := int64(500)
	require.ErrorIs(t, CompleteBoundStripeTopUpOrder(legacy.TradeNo, "cs_legacy", "payment", "wallet", &legacyAmount, "usd"), ErrStripeLegacyOrderRequiresReview)
	pending = model.TopUp{}
	require.NoError(t, model.DB.First(&pending, legacy.Id).Error)
	assert.Equal(t, StripeReconciliationLegacyBinding, pending.ReconciliationState)
	assert.Equal(t, TopUpStatusPending, pending.Status)

	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", legacy.Id).Updates(map[string]any{
		"status": TopUpStatusSuccess,
	}).Error)
	require.NoError(t, CompleteBoundStripeTopUpOrder(legacy.TradeNo, "", "", "", nil, ""),
		"an already-successful legacy order must remain callback-idempotent")
}
