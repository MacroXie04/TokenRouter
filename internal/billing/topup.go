package billing

import (
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"math"
	"strconv"
	"strings"
)

// TopUp status constants.
const (
	TopUpStatusPending   = "pending"
	TopUpStatusSuccess   = "success"
	TopUpStatusFailed    = "failed"
	TopUpStatusCancelled = "cancelled"
	TopUpStatusExpired   = "expired"
)

// TopUp payment providers.
const (
	PaymentProviderEpay   = "epay"
	PaymentProviderStripe = "stripe"

	// Provider money values are ordinary decimal strings. Bounding their wire
	// representation also bounds exact-decimal callback comparison work.
	maxTopUpMoneyTextLength = 64
)

var (
	// ErrTopUpNotFound is returned when a trade number does not resolve.
	ErrTopUpNotFound = errors.New("充值订单不存在")
	// ErrTopUpStatusInvalid rejects completion of a terminal non-success order.
	ErrTopUpStatusInvalid = errors.New("充值订单状态无效")
	// ErrTopUpAmountMismatch rejects provider metadata that does not match the
	// authoritative amount captured when the order was created.
	ErrTopUpAmountMismatch = errors.New("充值订单金额不匹配")
	// ErrTopUpQuotaOverflow rejects credits that cannot be represented safely.
	ErrTopUpQuotaOverflow = errors.New("充值额度超出安全范围")
	// ErrTopUpLegacyCreditRequiresReview prevents a pending pre-migration
	// order from being credited under guessed unit semantics.
	ErrTopUpLegacyCreditRequiresReview = errors.New("充值旧订单缺少额度快照，需要人工核对")
	// ErrTopUpPricingInvalid rejects an amount or pricing factor that cannot be
	// represented as a finite, positive provider charge.
	ErrTopUpPricingInvalid = errors.New("充值价格配置无效")
	// ErrStripePaymentMismatch rejects a signed Stripe event whose settled
	// minor-unit amount or currency differs from the immutable order snapshot.
	ErrStripePaymentMismatch = errors.New("Stripe 支付金额或币种不匹配")
	// ErrStripeCheckoutBindingMismatch rejects a signed event that is not for
	// the exact Checkout Session created for the local order.
	ErrStripeCheckoutBindingMismatch = errors.New("Stripe Checkout 订单绑定不匹配")
	// ErrStripeLegacyOrderRequiresReview prevents an unbound legacy pending
	// order from being credited solely from forgeable Checkout metadata.
	ErrStripeLegacyOrderRequiresReview = errors.New("Stripe 旧订单缺少安全绑定，需要人工核对")
)

const (
	StripeCheckoutBindingVersion = 1
	// Subscription binding v2 is a one-time Checkout purchase. Version-one
	// subscription rows used recurring Checkout without renewal fulfillment
	// and pending rows therefore require manual reconciliation.
	StripeSubscriptionCheckoutBindingVersion = 2
	StripeOrderTypeWallet                    = "wallet"
	StripeOrderTypeSubscription              = "subscription"
	StripeCheckoutModePayment                = "payment"
	StripeCheckoutModeSubscription           = "payment"
	StripeReconciliationLegacyBinding        = "legacy_binding_required"
	StripeReconciliationBindingMismatch      = "checkout_binding_mismatch"
	StripeReconciliationCreationUnknown      = "checkout_creation_uncertain"
	StripeReconciliationPaymentMismatch      = "payment_amount_currency_mismatch"
	StripeReconciliationFulfillment          = "paid_fulfillment_failed"
	TopUpReconciliationLegacyCredit          = "legacy_credit_snapshot_required"
)

const topUpCreditQuotaVersion = 1

// StripeMoneyToMinorUnits parses conventional base-10 money into Stripe's
// two-decimal minor-unit representation. It deliberately rejects exponents,
// signs, fractional minor units, and values outside int64 instead of relying
// on floating-point tolerances.
func StripeMoneyToMinorUnits(money string) (int64, error) {
	return StripeMoneyToMinorUnitsForCurrency(money, "USD")
}

// StripeMoneyToMinorUnitsForCurrency converts a decimal major-unit amount
// using an explicitly implemented currency exponent. Unknown currencies are
// rejected instead of being silently treated as two-decimal currencies.
func StripeMoneyToMinorUnitsForCurrency(money, currency string) (int64, error) {
	if money == "" || money != strings.TrimSpace(money) || len(money) > maxTopUpMoneyTextLength {
		return 0, ErrStripePaymentMismatch
	}
	exponent, err := stripeCurrencyExponent(currency)
	if err != nil {
		return 0, err
	}
	digits := 0
	dotSeen := false
	for _, char := range []byte(money) {
		switch {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' && !dotSeen:
			dotSeen = true
		default:
			return 0, ErrStripePaymentMismatch
		}
	}
	if digits == 0 {
		return 0, ErrStripePaymentMismatch
	}
	parsed, err := decimal.NewFromString(money)
	if err != nil || parsed.IsNegative() {
		return 0, ErrStripePaymentMismatch
	}
	scaled := parsed.Shift(exponent)
	if !scaled.Equal(scaled.Truncate(0)) || scaled.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, ErrStripePaymentMismatch
	}
	return scaled.IntPart(), nil
}

func stripeCurrencyExponent(currency string) (int32, error) {
	currency, err := NormalizeStripeCurrency(currency)
	if err != nil {
		return 0, err
	}
	// Stripe's charge minor-unit exponents. Keeping this list explicit makes a
	// newly introduced or misspelled currency fail closed until reviewed.
	zeroDecimal := map[string]struct{}{
		"BIF": {}, "CLP": {}, "DJF": {}, "GNF": {}, "JPY": {}, "KMF": {},
		"KRW": {}, "MGA": {}, "PYG": {}, "RWF": {}, "UGX": {}, "VND": {},
		"VUV": {}, "XAF": {}, "XOF": {}, "XPF": {},
	}
	threeDecimal := map[string]struct{}{
		"BHD": {}, "JOD": {}, "KWD": {}, "OMR": {}, "TND": {},
	}
	twoDecimal := map[string]struct{}{
		"AED": {}, "AFN": {}, "ALL": {}, "AMD": {}, "ANG": {}, "AOA": {},
		"ARS": {}, "AUD": {}, "AWG": {}, "AZN": {}, "BAM": {}, "BBD": {},
		"BDT": {}, "BGN": {}, "BMD": {}, "BND": {}, "BOB": {}, "BRL": {},
		"BSD": {}, "BWP": {}, "BYN": {}, "BZD": {}, "CAD": {}, "CDF": {},
		"CHF": {}, "CNY": {}, "COP": {}, "CRC": {}, "CVE": {}, "CZK": {},
		"DKK": {}, "DOP": {}, "DZD": {}, "EGP": {}, "ETB": {}, "EUR": {},
		"FJD": {}, "FKP": {}, "GBP": {}, "GEL": {}, "GIP": {}, "GMD": {},
		"GTQ": {}, "GYD": {}, "HKD": {}, "HNL": {}, "HRK": {}, "HTG": {},
		"HUF": {}, "IDR": {}, "ILS": {}, "INR": {}, "ISK": {}, "JMD": {},
		"KES": {}, "KGS": {}, "KHR": {}, "KYD": {}, "KZT": {}, "LAK": {},
		"LBP": {}, "LKR": {}, "LRD": {}, "LSL": {}, "MAD": {}, "MDL": {},
		"MKD": {}, "MMK": {}, "MNT": {}, "MOP": {}, "MUR": {}, "MVR": {},
		"MWK": {}, "MXN": {}, "MYR": {}, "MZN": {}, "NAD": {}, "NGN": {},
		"NIO": {}, "NOK": {}, "NPR": {}, "NZD": {}, "PAB": {}, "PEN": {},
		"PGK": {}, "PHP": {}, "PKR": {}, "PLN": {}, "QAR": {}, "RON": {},
		"RSD": {}, "SAR": {}, "SBD": {}, "SCR": {}, "SEK": {}, "SGD": {},
		"SHP": {}, "SLE": {}, "SOS": {}, "SRD": {}, "STD": {}, "SZL": {},
		"THB": {}, "TJS": {}, "TOP": {}, "TRY": {}, "TTD": {}, "TWD": {},
		"TZS": {}, "UAH": {}, "USD": {}, "UYU": {}, "UZS": {}, "WST": {},
		"YER": {}, "ZAR": {}, "ZMW": {},
	}
	if _, ok := zeroDecimal[currency]; ok {
		return 0, nil
	}
	if _, ok := threeDecimal[currency]; ok {
		return 3, nil
	}
	if _, ok := twoDecimal[currency]; ok {
		return 2, nil
	}
	return 0, ErrStripePaymentMismatch
}

// StripeCurrencySupported reports whether this build has an audited minor-
// unit exponent for the supplied currency.
func StripeCurrencySupported(currency string) bool {
	_, err := stripeCurrencyExponent(currency)
	return err == nil
}

// NormalizeStripeCurrency validates and canonicalizes a Stripe currency code.
func NormalizeStripeCurrency(currency string) (string, error) {
	if len(currency) != 3 {
		return "", ErrStripePaymentMismatch
	}
	for i := range len(currency) {
		char := currency[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
			return "", ErrStripePaymentMismatch
		}
	}
	return strings.ToUpper(currency), nil
}

// CreateTopUp creates a wallet recharge order with a random trade number.
func CreateTopUp(userId int, amount int64, money float64, paymentMethod, paymentProvider string) (*model.TopUp, error) {
	tradeNo, err := cryptoutil.SecureRandomAlphanumeric(32)
	if err != nil {
		return nil, err
	}
	return CreateTopUpWithTradeNo(userId, amount, money, paymentMethod, paymentProvider, tradeNo)
}

// CreateTopUpWithTradeNo creates a wallet recharge order with the given
// trade number (the payment gateways carry their own reference formats).
func CreateTopUpWithTradeNo(userId int, amount int64, money float64, paymentMethod, paymentProvider, tradeNo string) (*model.TopUp, error) {
	return createTopUpWithTradeNo(userId, amount, money, paymentMethod, paymentProvider, tradeNo, 0, "", 0, "", "", "", "")
}

// NormalizeTopUpOrderAmount translates the public display amount into the
// reference monetary amount persisted on the order. Token display can only
// represent whole reference units; rejecting a remainder avoids silently
// charging for fewer tokens than the caller requested.
func NormalizeTopUpOrderAmount(displayAmount int64) (int64, error) {
	if displayAmount <= 0 {
		return 0, ErrTopUpAmountMismatch
	}
	amount := displayAmount
	if setting.GetQuotaDisplayType() == setting.QuotaDisplayTypeTokens {
		unit := int64(quotamath.QuotaPerUnit)
		if unit <= 0 || displayAmount%unit != 0 {
			return 0, ErrTopUpAmountMismatch
		}
		amount = displayAmount / unit
	}
	if _, err := topUpCreditQuota(amount); err != nil {
		return 0, err
	}
	return amount, nil
}

func topUpCreditQuota(amount int64) (int64, error) {
	unit := int64(quotamath.QuotaPerUnit)
	if amount <= 0 || unit <= 0 || amount > quotamath.MaxQuota/unit {
		return 0, ErrTopUpQuotaOverflow
	}
	credit := amount * unit
	if credit <= 0 || credit > quotamath.MaxQuota {
		return 0, ErrTopUpQuotaOverflow
	}
	return credit, nil
}

// CreateStripeTopUpWithTradeNo atomically persists a pending Stripe top-up
// together with the exact provider amount and currency expected at webhook
// time. The stored display money and minor-unit expectation are derived from
// one normalized value so they cannot drift.
func CreateStripeTopUpWithTradeNo(userId int, amount int64, money float64, currency, tradeNo string) (*model.TopUp, error) {
	return createStripeTopUpWithTradeNo(userId, amount, money, currency, tradeNo, false)
}

// CreateBoundStripeTopUpWithTradeNo creates a versioned Checkout-bound order
// used by the public Stripe endpoint. The exact price snapshot is later used
// both to create Checkout and to validate the signed completion event.
func CreateBoundStripeTopUpWithTradeNo(userId int, amount int64, money float64, currency, tradeNo string) (*model.TopUp, error) {
	if !strings.HasPrefix(tradeNo, "ref_") || strings.HasPrefix(tradeNo, "sub_ref_") {
		return nil, ErrTopUpAmountMismatch
	}
	return createStripeTopUpWithTradeNo(userId, amount, money, currency, tradeNo, true)
}

func createStripeTopUpWithTradeNo(userId int, amount int64, money float64, currency, tradeNo string, bound bool) (*model.TopUp, error) {
	normalizedMoney, providerMoney, err := NormalizePayMoney(money)
	if err != nil {
		return nil, ErrTopUpPricingInvalid
	}
	currency, err = NormalizeStripeCurrency(currency)
	if err != nil {
		return nil, ErrTopUpPricingInvalid
	}
	minor, err := StripeMoneyToMinorUnitsForCurrency(providerMoney, currency)
	if err != nil || minor <= 0 {
		return nil, ErrTopUpPricingInvalid
	}
	bindingVersion := 0
	orderType := ""
	mode := ""
	reconciliationState := ""
	reconciliationDetail := ""
	if bound {
		bindingVersion = StripeCheckoutBindingVersion
		orderType = StripeOrderTypeWallet
		mode = StripeCheckoutModePayment
		reconciliationState = StripeReconciliationCreationUnknown
		reconciliationDetail = "checkout_not_yet_bound"
	}
	return createTopUpWithTradeNo(userId, amount, normalizedMoney, PaymentMethodStripe, PaymentProviderStripe, tradeNo, minor, currency, bindingVersion, orderType, mode, reconciliationState, reconciliationDetail)
}

func createTopUpWithTradeNo(userId int, amount int64, money float64, paymentMethod, paymentProvider, tradeNo string, providerAmountMinor int64, providerCurrency string, bindingVersion int, orderType, mode, reconciliationState, reconciliationDetail string) (*model.TopUp, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	creditQuota, creditErr := topUpCreditQuota(amount)
	if creditErr != nil {
		return nil, errors.Join(ErrTopUpAmountMismatch, creditErr)
	}
	if userId <= 0 ||
		math.IsNaN(money) || math.IsInf(money, 0) || money <= 0 ||
		tradeNo == "" || len(tradeNo) > 255 || len(paymentMethod) > 50 || len(paymentProvider) > 50 {
		return nil, ErrTopUpAmountMismatch
	}
	if money > setting.MaxPaymentProviderAmount {
		return nil, ErrTopUpPricingInvalid
	}
	var t model.TopUp
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		t = model.TopUp{
			UserId:                 userId,
			Amount:                 amount,
			CreditQuota:            creditQuota,
			CreditQuotaVersion:     topUpCreditQuotaVersion,
			Money:                  money,
			TradeNo:                tradeNo,
			PaymentMethod:          paymentMethod,
			PaymentProvider:        paymentProvider,
			ProviderAmountMinor:    providerAmountMinor,
			ProviderCurrency:       providerCurrency,
			ProviderBindingVersion: bindingVersion,
			ProviderOrderType:      orderType,
			ProviderMode:           mode,
			ReconciliationState:    reconciliationState,
			ReconciliationDetail:   reconciliationDetail,
			CreateTime:             now,
			Status:                 TopUpStatusPending,
		}
		return tx.Create(&t).Error
	}); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTopupMoney converts a top-up amount into the payable money in the
// configured display type (token amounts are divided by QuotaPerUnit),
// applying the user group ratio, the configured price, and any preset
// discount for the requested amount (reference formula).
func GetTopupMoney(amount int64, group string) (float64, error) {
	unitPrice, err := setting.GetTopUpPriceChecked()
	if err != nil {
		return 0, ErrTopUpPricingInvalid
	}
	return calculateTopUpMoney(amount, group, unitPrice)
}

// GetStripeTopupMoney is the checked Stripe counterpart of GetTopupMoney.
func GetStripeTopupMoney(amount int64, group string) (float64, error) {
	unitPrice, err := setting.GetStripeUnitPriceChecked()
	if err != nil {
		return 0, ErrTopUpPricingInvalid
	}
	return calculateTopUpMoney(amount, group, unitPrice)
}

// GetTopUpChargedMoney computes the accounting money captured on a Stripe
// order. Stripe checkout itself charges a configured Price ID times amount;
// this preserves the reference order-audit formula while rejecting unsafe
// group ratios before the provider is called.
func GetTopUpChargedMoney(amount int64, group string) (float64, error) {
	if amount <= 0 || amount > quotamath.MaxQuota {
		return 0, ErrTopUpPricingInvalid
	}
	ratio, err := validatedTopUpGroupRatio(group)
	if err != nil {
		return 0, err
	}
	charged := decimal.NewFromInt(amount).Mul(decimal.NewFromFloat(ratio)).InexactFloat64()
	if !finitePositiveMoney(charged) || charged > setting.MaxPaymentProviderAmount ||
		len(FormatPayMoney(charged)) > maxTopUpMoneyTextLength {
		return 0, ErrTopUpPricingInvalid
	}
	return charged, nil
}

// FormatPayMoney renders a payable amount with two decimals (the reference
// money wire format).
func FormatPayMoney(money float64) string {
	return strconv.FormatFloat(money, 'f', 2, 64)
}

// NormalizePayMoney returns the exact two-decimal amount sent to a provider
// and the corresponding finite float persisted on the order. Keeping both
// representations derived from one string prevents callback/order drift.
func NormalizePayMoney(money float64) (float64, string, error) {
	if !finitePositiveMoney(money) {
		return 0, "", ErrTopUpPricingInvalid
	}
	formatted := FormatPayMoney(money)
	if len(formatted) > maxTopUpMoneyTextLength {
		return 0, "", ErrTopUpPricingInvalid
	}
	normalized, err := strconv.ParseFloat(formatted, 64)
	if err != nil || !finitePositiveMoney(normalized) || normalized > setting.MaxPaymentProviderAmount {
		return 0, "", ErrTopUpPricingInvalid
	}
	return normalized, formatted, nil
}

// TopUpMoneyMatches compares a signed provider amount to the authoritative
// order amount numerically (so "10.0" equals "10.00") without float epsilon.
func TopUpMoneyMatches(orderMoney float64, providerMoney string) bool {
	providerMoney = strings.TrimSpace(providerMoney)
	if !finitePositiveMoney(orderMoney) || providerMoney == "" || len(providerMoney) > maxTopUpMoneyTextLength {
		return false
	}
	actual, err := decimal.NewFromString(providerMoney)
	if err != nil || actual.Sign() <= 0 {
		return false
	}
	expected := decimal.NewFromFloat(orderMoney)
	// Equal rescales both operands. Reject an exponent gap wider than the
	// bounded input could bridge before asking decimal to allocate that scale.
	exponentGap := int64(actual.Exponent()) - int64(expected.Exponent())
	if exponentGap > maxTopUpMoneyTextLength || exponentGap < -maxTopUpMoneyTextLength {
		return false
	}
	return actual.Equal(expected)
}

func calculateTopUpMoney(amount int64, group string, unitPrice float64) (float64, error) {
	if amount <= 0 || amount > quotamath.MaxQuota || !finitePositiveMoney(unitPrice) {
		return 0, ErrTopUpPricingInvalid
	}
	ratio, err := validatedTopUpGroupRatio(group)
	if err != nil {
		return 0, err
	}
	discount, err := setting.GetPaymentDiscount(amount)
	if err != nil || !finitePositiveMoney(discount) {
		return 0, ErrTopUpPricingInvalid
	}
	dAmount := decimal.NewFromInt(amount)
	if setting.GetQuotaDisplayType() == setting.QuotaDisplayTypeTokens {
		dAmount = dAmount.Div(decimal.NewFromInt(quotamath.QuotaPerUnit))
	}
	payMoney := dAmount.
		Mul(decimal.NewFromFloat(unitPrice)).
		Mul(decimal.NewFromFloat(ratio)).
		Mul(decimal.NewFromFloat(discount)).
		InexactFloat64()
	if !finitePositiveMoney(payMoney) {
		return 0, ErrTopUpPricingInvalid
	}
	if _, _, err := NormalizePayMoney(payMoney); err != nil {
		return 0, ErrTopUpPricingInvalid
	}
	return payMoney, nil
}

func finitePositiveMoney(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// CompleteTopUp idempotently marks an order successful and credits the user's
// quota exactly once. The status transition and credit commit in one database
// transaction; provider metadata must match the stored order amount.
func CompleteTopUp(userId int, tradeNo string, amount int64) error {
	err := completeTopUp(userId, tradeNo, amount, nil, nil, nil)
	if errors.Is(err, ErrTopUpLegacyCreditRequiresReview) {
		if markErr := markTopUpReconciliation(tradeNo, TopUpReconciliationLegacyCredit, err.Error()); markErr != nil {
			return fmt.Errorf("%w: persist reconciliation evidence: %v", err, markErr)
		}
	}
	return err
}

type stripeSettlement struct {
	amountMinor int64
	currency    string
	customerID  string
}

type stripeCheckoutBinding struct {
	sessionID string
	mode      string
	orderType string
	priceID   string
	// leaseOwner is set only by the background reconciler. When present, the
	// order transition is fenced to the exact claim that performed the provider
	// lookup so an expired worker cannot fulfill after another worker takes over.
	leaseOwner string
	// The remaining fields are populated only by the reconciler and bind paid
	// fulfillment to the exact immutable request claimed before the provider
	// call. Webhook completions continue to rely on the signed session binding.
	checkoutRequest      string
	checkoutFingerprint  string
	providerExpiresAt    int64
	snapshotUserID       int
	snapshotWalletAmount int64
}

// CompleteStripeTopUp validates Stripe's signed metadata and financial fields
// against the immutable order snapshot in the same transaction that credits
// quota and marks the order successful.
func CompleteStripeTopUp(userId int, tradeNo string, amount int64, amountTotal *int64, currency string) error {
	if amountTotal == nil || *amountTotal <= 0 {
		return ErrStripePaymentMismatch
	}
	normalizedCurrency, err := NormalizeStripeCurrency(currency)
	if err != nil {
		return err
	}
	return completeTopUp(userId, tradeNo, amount, &stripeSettlement{amountMinor: *amountTotal, currency: normalizedCurrency}, nil, nil)
}

// CompleteBoundStripeTopUp settles a wallet order only when the signed event
// matches the exact Checkout Session, mode and order type bound at creation.
func CompleteBoundStripeTopUp(userId int, tradeNo string, amount int64, sessionID, mode, orderType string, amountTotal *int64, currency string) error {
	if amountTotal == nil || *amountTotal <= 0 {
		if legacy, err := legacySuccessfulStripeTopUp(tradeNo); err == nil && legacy {
			return nil
		}
		return reconcileBoundStripeTopUpError(tradeNo, ErrStripePaymentMismatch)
	}
	normalizedCurrency, err := NormalizeStripeCurrency(currency)
	if err != nil {
		if legacy, lookupErr := legacySuccessfulStripeTopUp(tradeNo); lookupErr == nil && legacy {
			return nil
		}
		return reconcileBoundStripeTopUpError(tradeNo, err)
	}
	err = completeTopUp(userId, tradeNo, amount,
		&stripeSettlement{amountMinor: *amountTotal, currency: normalizedCurrency},
		&stripeCheckoutBinding{sessionID: strings.TrimSpace(sessionID), mode: mode, orderType: orderType}, nil)
	return reconcileBoundStripeTopUpError(tradeNo, err)
}

func legacySuccessfulStripeTopUp(tradeNo string) (bool, error) {
	var order model.TopUp
	if err := model.DB.Select("status", "provider_binding_version", "credit_quota_version").Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
		return false, err
	}
	return order.Status == TopUpStatusSuccess &&
		(order.ProviderBindingVersion == 0 || order.CreditQuotaVersion == 0), nil
}

// CompleteBoundStripeTopUpOrder derives credit identity and quota from the
// immutable local order rather than trusting Checkout metadata.
func CompleteBoundStripeTopUpOrder(tradeNo, sessionID, mode, orderType string, amountTotal *int64, currency string) error {
	return CompleteBoundStripeTopUpOrderWithCustomer(tradeNo, sessionID, mode, orderType, "", amountTotal, currency)
}

// CompleteBoundStripeTopUpOrderWithCustomer additionally persists the
// customer identifier carried by the verified Stripe event.
func CompleteBoundStripeTopUpOrderWithCustomer(tradeNo, sessionID, mode, orderType, customerID string, amountTotal *int64, currency string) error {
	if amountTotal == nil || *amountTotal <= 0 {
		if legacy, err := legacySuccessfulStripeTopUp(tradeNo); err == nil && legacy {
			return nil
		}
		return reconcileBoundStripeTopUpError(tradeNo, ErrStripePaymentMismatch)
	}
	var order model.TopUp
	if err := model.DB.Select("user_id", "amount").Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTopUpNotFound
		}
		return err
	}
	normalizedCurrency, err := NormalizeStripeCurrency(currency)
	if err != nil {
		if legacy, lookupErr := legacySuccessfulStripeTopUp(tradeNo); lookupErr == nil && legacy {
			return nil
		}
		return reconcileBoundStripeTopUpError(tradeNo, err)
	}
	err = completeTopUp(order.UserId, tradeNo, order.Amount,
		&stripeSettlement{amountMinor: *amountTotal, currency: normalizedCurrency, customerID: strings.TrimSpace(customerID)},
		&stripeCheckoutBinding{sessionID: strings.TrimSpace(sessionID), mode: mode, orderType: orderType}, nil)
	return reconcileBoundStripeTopUpError(tradeNo, err)
}

func bindStripeCustomerTx(tx *gorm.DB, user *model.User, customerID string) error {
	if tx == nil || user == nil || strings.TrimSpace(customerID) == "" {
		return nil
	}
	customerID = strings.TrimSpace(customerID)
	if !strings.HasPrefix(customerID, "cus_") || len(customerID) > 128 {
		return ErrStripeCheckoutBindingMismatch
	}
	if user.StripeCustomer != "" {
		return nil
	}
	result := tx.Model(&model.User{}).Where("id = ? AND stripe_customer = ''", user.Id).
		Update("stripe_customer", customerID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		// A concurrent verified Checkout may have populated another legitimate
		// customer first. Customer persistence is auxiliary and must never turn
		// an otherwise valid paid order into paid-but-unfulfilled state.
		return nil
	}
	user.StripeCustomer = customerID
	return nil
}

func completeTopUp(userId int, tradeNo string, amount int64, stripePayment *stripeSettlement, checkoutBinding *stripeCheckoutBinding, creemPayment *CreemSettlement) error {
	if userId <= 0 || strings.TrimSpace(tradeNo) == "" || amount <= 0 || amount > quotamath.MaxQuota {
		return ErrTopUpAmountMismatch
	}

	credited := false
	auditEventID := ""
	var order model.TopUp
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		completeTime, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		query := locking.SubscriptionLockForUpdate(tx).
			Where("trade_no = ? AND user_id = ?", tradeNo, userId).
			First(&order)
		if query.Error != nil {
			if errors.Is(query.Error, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return query.Error
		}
		// Legacy successful rows predate binding fields. Duplicate callbacks must
		// remain idempotent, but a legacy pending row cannot be safely credited.
		if order.Status == TopUpStatusSuccess && order.CreditQuotaVersion == 0 && creemPayment == nil {
			return nil
		}
		if order.PaymentProvider == PaymentProviderCreem && creemPayment == nil {
			return ErrCreemPaymentMismatch
		}
		if order.PaymentProvider == PaymentProviderWaffo {
			return ErrWaffoPaymentMismatch
		}
		if order.PaymentProvider == PaymentProviderWaffoPancake {
			return ErrWaffoPancakePaymentMismatch
		}
		if checkoutBinding != nil {
			if err := validateOrClaimTopUpCheckoutBindingTx(tx, &order, checkoutBinding); err != nil {
				return err
			}
		}
		if stripePayment != nil {
			expectedCurrency, currencyErr := NormalizeStripeCurrency(order.ProviderCurrency)
			if order.PaymentProvider != PaymentProviderStripe || order.PaymentMethod != PaymentMethodStripe ||
				order.ProviderAmountMinor <= 0 || currencyErr != nil ||
				order.ProviderAmountMinor != stripePayment.amountMinor || expectedCurrency != stripePayment.currency {
				return ErrStripePaymentMismatch
			}
		}
		if creemPayment != nil {
			if err := validateCreemTopUpSettlementTx(tx, &order, creemPayment); err != nil {
				return err
			}
		}
		if order.Status == TopUpStatusSuccess {
			return nil
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		if order.Amount != amount {
			return ErrTopUpAmountMismatch
		}
		if creemPayment != nil {
			if order.CreditQuotaVersion != CreemTopUpCreditQuotaVersion || order.CreditQuota != order.Amount ||
				order.CreditQuota <= 0 || order.CreditQuota > quotamath.MaxQuota {
				return ErrTopUpQuotaOverflow
			}
		} else {
			expectedCredit, creditErr := topUpCreditQuota(order.Amount)
			if order.CreditQuotaVersion < topUpCreditQuotaVersion {
				return ErrTopUpLegacyCreditRequiresReview
			}
			if creditErr != nil || order.CreditQuota != expectedCredit {
				return ErrTopUpQuotaOverflow
			}
		}

		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "quota", "stripe_customer").First(&user, userId).Error; err != nil {
			return err
		}
		if stripePayment != nil {
			if err := bindStripeCustomerTx(tx, &user, stripePayment.customerID); err != nil {
				return err
			}
		}
		newQuota, ok := quotamath.AddQuotaWithinBounds(user.Quota, int(order.CreditQuota))
		if !ok {
			return ErrTopUpQuotaOverflow
		}

		transitionQuery := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ?", order.Id, TopUpStatusPending)
		if checkoutBinding != nil && checkoutBinding.leaseOwner != "" {
			transitionQuery = transitionQuery.Where("reconciliation_lease_owner = ?", checkoutBinding.leaseOwner)
		}
		transitionUpdates := map[string]any{
			"status":                          TopUpStatusSuccess,
			"complete_time":                   completeTime,
			"reconciliation_state":            "",
			"reconciliation_detail":           "",
			"reconciliation_next_at":          0,
			"reconciliation_lease_owner":      "",
			"reconciliation_lease_expires_at": 0,
		}
		if checkoutBinding != nil && checkoutBinding.leaseOwner != "" {
			transitionUpdates["provider_session_id"] = checkoutBinding.sessionID
			transitionUpdates["provider_expires_at"] = checkoutBinding.providerExpiresAt
		}
		transition := transitionQuery.Updates(transitionUpdates)
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrTopUpStatusInvalid
		}

		credit := tx.Model(&model.User{}).Where("id = ? AND quota = ?", userId, user.Quota).
			UpdateColumn("quota", newQuota)
		if credit.Error != nil {
			return credit.Error
		}
		if credit.RowsAffected != 1 {
			return fmt.Errorf("credit top-up user %d: %w", userId, gorm.ErrRecordNotFound)
		}
		var auditErr error
		auditEventID, auditErr = enqueueTopupLogTx(tx, userId, int(order.CreditQuota), tradeNo, completeTime)
		if auditErr != nil {
			return fmt.Errorf("persist top-up audit: %w", auditErr)
		}
		credited = true
		return nil
	})
	if err != nil {
		return err
	}
	if credited {
		if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
			logging.SysError(fmt.Sprintf("top-up audit delivery deferred trade_no=%s user_id=%d: %v", tradeNo, userId, err))
		}
	}
	return nil
}

func topUpCheckoutBindingMatches(order *model.TopUp, binding *stripeCheckoutBinding) bool {
	return order != nil && binding != nil && order.ProviderSessionId != nil &&
		strings.TrimSpace(*order.ProviderSessionId) != "" &&
		*order.ProviderSessionId == binding.sessionID &&
		order.ProviderOrderType == StripeOrderTypeWallet && binding.orderType == StripeOrderTypeWallet &&
		order.ProviderMode == StripeCheckoutModePayment && binding.mode == StripeCheckoutModePayment
}

// validateOrClaimTopUpCheckoutBindingTx accepts the session bound after a
// validated create response. If Stripe's create outcome was ambiguous, the
// first matching signed webhook may atomically supply the session id. This
// preserves recoverability after timeouts/5xx responses without weakening the
// immutable mode, type, amount, currency, or trade-number checks.
func validateOrClaimTopUpCheckoutBindingTx(tx *gorm.DB, order *model.TopUp, binding *stripeCheckoutBinding) error {
	if tx == nil || order == nil || binding == nil ||
		order.ProviderBindingVersion < StripeCheckoutBindingVersion {
		return ErrStripeLegacyOrderRequiresReview
	}
	binding.sessionID = strings.TrimSpace(binding.sessionID)
	if !strings.HasPrefix(binding.sessionID, "cs_") ||
		order.ProviderOrderType != StripeOrderTypeWallet || binding.orderType != StripeOrderTypeWallet ||
		order.ProviderMode != StripeCheckoutModePayment || binding.mode != StripeCheckoutModePayment {
		return ErrStripeCheckoutBindingMismatch
	}
	if binding.leaseOwner != "" && order.Status == TopUpStatusPending {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if order.ReconciliationLeaseOwner != binding.leaseOwner || order.ReconciliationLeaseExpiresAt <= now ||
			order.CheckoutRequest != binding.checkoutRequest || order.CheckoutFingerprint != binding.checkoutFingerprint ||
			order.UserId != binding.snapshotUserID || order.Amount != binding.snapshotWalletAmount ||
			(order.ProviderExpiresAt > 0 && order.ProviderExpiresAt != binding.providerExpiresAt) {
			return ErrStripeCheckoutReconciliationLeaseLost
		}
	}
	if order.ProviderSessionId != nil {
		if topUpCheckoutBindingMatches(order, binding) {
			return nil
		}
		return ErrStripeCheckoutBindingMismatch
	}
	if order.Status != TopUpStatusPending || !stripeReconciliationAllowsSessionClaim(order.ReconciliationState) {
		return ErrStripeCheckoutBindingMismatch
	}
	claimQuery := tx.Model(&model.TopUp{}).
		Where(`id = ? AND status = ? AND provider_binding_version >= ? AND provider_session_id IS NULL
			AND provider_order_type = ? AND provider_mode = ? AND reconciliation_state IN ?`,
			order.Id, TopUpStatusPending, StripeCheckoutBindingVersion,
			StripeOrderTypeWallet, StripeCheckoutModePayment,
			stripeSessionClaimReconciliationStates())
	if binding.leaseOwner != "" {
		claimQuery = claimQuery.Where("reconciliation_lease_owner = ?", binding.leaseOwner)
	}
	result := claimQuery.
		Update("provider_session_id", binding.sessionID)
	if result.Error != nil {
		return fmt.Errorf("%w: claim session: %v", ErrStripeCheckoutBindingMismatch, result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrStripeCheckoutBindingMismatch
	}
	claimed := binding.sessionID
	order.ProviderSessionId = &claimed
	return nil
}

func stripeSessionClaimReconciliationStates() []string {
	return []string{
		StripeReconciliationCreationUnknown,
		StripeReconciliationBindingMismatch,
		StripeReconciliationPaymentMismatch,
		StripeReconciliationFulfillment,
	}
}

func stripeReconciliationAllowsSessionClaim(state string) bool {
	for _, allowed := range stripeSessionClaimReconciliationStates() {
		if state == allowed {
			return true
		}
	}
	return false
}

func reconciliationStateForStripeError(err error) string {
	switch {
	case errors.Is(err, ErrStripeLegacyOrderRequiresReview):
		return StripeReconciliationLegacyBinding
	case errors.Is(err, ErrStripeCheckoutBindingMismatch):
		return StripeReconciliationBindingMismatch
	case errors.Is(err, ErrStripePaymentMismatch):
		return StripeReconciliationPaymentMismatch
	case errors.Is(err, ErrTopUpLegacyCreditRequiresReview):
		return TopUpReconciliationLegacyCredit
	default:
		return StripeReconciliationFulfillment
	}
}

func reconcileBoundStripeTopUpError(tradeNo string, err error) error {
	if err == nil || errors.Is(err, ErrTopUpNotFound) {
		return err
	}
	if markErr := markTopUpReconciliation(tradeNo, reconciliationStateForStripeError(err), err.Error()); markErr != nil {
		return fmt.Errorf("%w: persist reconciliation evidence: %v", err, markErr)
	}
	return err
}

func markTopUpReconciliation(tradeNo, state, detail string) error {
	if model.DB == nil || strings.TrimSpace(tradeNo) == "" {
		return errors.New("top-up database is unavailable")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.TopUp
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "status", "reconciliation_state", "reconciliation_detail").
			Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if order.Status == TopUpStatusSuccess {
			return ErrTopUpStatusInvalid
		}
		if order.ReconciliationState == state && order.ReconciliationDetail == detail {
			return nil
		}
		result := tx.Model(&model.TopUp{}).Where("id = ? AND status = ?", order.Id, order.Status).
			Updates(map[string]any{"reconciliation_state": state, "reconciliation_detail": detail})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTopUpStatusInvalid
		}
		return nil
	})
}

// FlagStripeTopUpReconciliation records a non-terminal Checkout creation
// uncertainty without exposing provider response contents.
func FlagStripeTopUpReconciliation(tradeNo, state string) error {
	if state != StripeReconciliationBindingMismatch && state != StripeReconciliationCreationUnknown {
		return ErrStripeCheckoutBindingMismatch
	}
	return markTopUpReconciliation(tradeNo, state, state)
}

// BindStripeTopUpSession records the validated Checkout Session before its URL
// is returned to the caller. Repeated binding to the same session is safe.
func BindStripeTopUpSession(tradeNo, sessionID string) error {
	return BindStripeTopUpSessionWithExpiry(tradeNo, sessionID, 0)
}

// BindStripeTopUpSessionWithExpiry stores provider response state separately
// from the immutable request and schedules verification in case the webhook is
// lost. expiresAt may be zero for legacy callers and tests.
func BindStripeTopUpSessionWithExpiry(tradeNo, sessionID string, expiresAt int64) error {
	tradeNo = strings.TrimSpace(tradeNo)
	sessionID = strings.TrimSpace(sessionID)
	if !strings.HasPrefix(tradeNo, "ref_") || strings.HasPrefix(tradeNo, "sub_ref_") ||
		!strings.HasPrefix(sessionID, "cs_") || expiresAt < 0 {
		return ErrStripeCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.TopUp
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if order.ProviderBindingVersion != StripeCheckoutBindingVersion ||
			order.ProviderOrderType != StripeOrderTypeWallet || order.ProviderMode != StripeCheckoutModePayment {
			return ErrStripeCheckoutBindingMismatch
		}
		if order.ProviderSessionId != nil {
			if *order.ProviderSessionId == sessionID &&
				(order.ProviderExpiresAt == 0 || expiresAt == 0 || order.ProviderExpiresAt == expiresAt) {
				return nil
			}
			return ErrStripeCheckoutBindingMismatch
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		nextAt := int64(0)
		state := ""
		if order.CheckoutFingerprint != "" {
			nextAt = now + stripeCheckoutReconciliationBaseDelay
			if expiresAt > 0 && nextAt > expiresAt {
				nextAt = expiresAt
			}
			state = StripeReconciliationCheckoutPending
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND provider_session_id IS NULL AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"provider_session_id":             sessionID,
				"provider_expires_at":             expiresAt,
				"reconciliation_state":            state,
				"reconciliation_detail":           "",
				"reconciliation_next_at":          nextAt,
				"reconciliation_lease_owner":      "",
				"reconciliation_lease_expires_at": 0,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStripeCheckoutBindingMismatch
		}
		return nil
	})
}

// GetTopUpByTradeNo loads a top-up order by trade number.
func GetTopUpByTradeNo(tradeNo string) (*model.TopUp, error) {
	if model.DB == nil {
		return nil, errors.New("top-up database is unavailable")
	}
	var t model.TopUp
	if err := model.DB.Where("trade_no = ?", tradeNo).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTopUpNotFound
		}
		return nil, err
	}
	return &t, nil
}
