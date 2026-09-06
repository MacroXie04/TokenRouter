package billing

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/store/locking"
	"gorm.io/gorm"
	"math"
	"strings"
	"time"
)

// PaymentMethodStripe is the Stripe payment method for subscription orders
// (PaymentMethodBalance/PaymentProviderBalance live in internal/billing/subscription.go).
const PaymentMethodStripe = "stripe"

const (
	// EpaySubscriptionBindingVersion marks orders whose payable amount and
	// entitlement were captured atomically before the signed checkout params
	// were returned. Legacy rows have no trustworthy callback amount binding.
	EpaySubscriptionBindingVersion = 1
	maxEpayProviderPayloadLength   = 64 << 10
)

// Subscription-order completion errors (reference sentinels).
var (
	ErrSubscriptionOrderNotFound      = errors.New("订阅订单不存在")
	ErrSubscriptionOrderStatusInvalid = errors.New("订阅订单状态无效")
	ErrSubscriptionOrderDataInvalid   = errors.New("订阅订单数据无效")
	ErrPaymentMethodMismatch          = errors.New("支付方式不匹配")
	ErrSubscriptionPurchaseLimit      = errors.New("已达到该套餐购买上限")
	ErrEpayPaymentMismatch            = errors.New("EPay 支付金额或方式不匹配")
)

// ValidatedStripePrice is the exact active one-time Price retrieved from
// Stripe immediately before a local subscription order is reserved.
type ValidatedStripePrice struct {
	ID          string
	AmountMinor int64
	Currency    string
}

// SubscriptionEntitlementSnapshot is the immutable fulfillment contract for
// a paid Stripe order. Mutable plan rows are never consulted for a versioned
// order after Checkout has been created.
type SubscriptionEntitlementSnapshot struct {
	PlanID                  int    `json:"plan_id"`
	Title                   string `json:"title"`
	DurationUnit            string `json:"duration_unit"`
	DurationValue           int    `json:"duration_value"`
	CustomSeconds           int64  `json:"custom_seconds"`
	TotalAmount             int64  `json:"total_amount"`
	QuotaResetPeriod        string `json:"quota_reset_period"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds"`
	UpgradeGroup            string `json:"upgrade_group"`
	DowngradeGroup          string `json:"downgrade_group"`
	AllowWalletOverflow     bool   `json:"allow_wallet_overflow"`
	MaxPurchasePerUser      int    `json:"max_purchase_per_user"`
}

func validSubscriptionOrderMoney(money float64) bool {
	return money >= 0 && money <= 9999 && !math.IsNaN(money) && !math.IsInf(money, 0)
}

// GetSubscriptionPlanById returns one subscription plan by id.
func GetSubscriptionPlanById(id int) (*model.SubscriptionPlan, error) {
	var plan model.SubscriptionPlan
	if err := model.DB.First(&plan, id).Error; err != nil {
		return nil, err
	}
	NormalizeSubscriptionPlanDefaults(&plan)
	return &plan, nil
}

// CountUserSubscriptionsByPlan counts a user's active subscription instances
// for one plan (reference purchase-cap helper).
func CountUserSubscriptionsByPlan(userId int, planId int) (int64, error) {
	if userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid userId or planId")
	}
	return countSubscriptionCapacityUsedTx(model.DB, userId, planId)
}

// CreateBoundEpaySubscriptionOrder reserves purchase capacity and persists the
// exact amount and immutable entitlement that a later signed EPay callback is
// allowed to fulfill. This must happen before checkout params are returned.
func CreateBoundEpaySubscriptionOrder(userId, planId int, paymentMethod, tradeNo string) (*model.SubscriptionOrder, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	paymentMethod = strings.TrimSpace(paymentMethod)
	expectedPrefix := fmt.Sprintf("SUBUSR%dNO", userId)
	if userId <= 0 || planId <= 0 || paymentMethod == "" || len(paymentMethod) > 50 ||
		!strings.HasPrefix(tradeNo, expectedPrefix) || len(tradeNo) > 255 {
		return nil, ErrSubscriptionOrderDataInvalid
	}

	var created model.SubscriptionOrder
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		var plan model.SubscriptionPlan
		if err := tx.Where("id = ?", planId).First(&plan).Error; err != nil {
			return err
		}
		NormalizeSubscriptionPlanDefaults(&plan)
		if !plan.Enabled || plan.MaxPurchasePerUser < 0 {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, ok := boundedSubscriptionQuota(plan.TotalAmount); !ok {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, err := calcPlanEndTime(time.Unix(now, 0), &plan); err != nil {
			return ErrSubscriptionOrderDataInvalid
		}
		currency, err := NormalizeStripeCurrency(plan.Currency)
		if err != nil || currency != "USD" {
			return ErrSubscriptionOrderDataInvalid
		}
		price, err := ParseSubscriptionPlanPrice(plan.PriceAmount)
		if err != nil || price < 0.01 || !validSubscriptionOrderMoney(price) {
			return ErrSubscriptionOrderDataInvalid
		}
		normalizedMoney, wireMoney, err := NormalizePayMoney(price)
		if err != nil || normalizedMoney < 0.01 || !validSubscriptionOrderMoney(normalizedMoney) {
			return ErrSubscriptionOrderDataInvalid
		}
		amountMinor, err := StripeMoneyToMinorUnitsForCurrency(wireMoney, currency)
		if err != nil || amountMinor <= 0 {
			return ErrSubscriptionOrderDataInvalid
		}
		if plan.MaxPurchasePerUser > 0 {
			used, err := countSubscriptionCapacityUsedTx(tx, userId, plan.Id)
			if err != nil {
				return err
			}
			if used >= int64(plan.MaxPurchasePerUser) {
				return ErrSubscriptionPurchaseLimit
			}
		}

		snapshotJSON, err := jsonutil.Marshal(subscriptionEntitlementSnapshotFromPlan(&plan))
		if err != nil {
			return err
		}
		created = model.SubscriptionOrder{
			UserId:                 userId,
			PlanId:                 plan.Id,
			Money:                  normalizedMoney,
			TradeNo:                tradeNo,
			PaymentMethod:          paymentMethod,
			PaymentProvider:        PaymentProviderEpay,
			ProviderAmountMinor:    amountMinor,
			ProviderCurrency:       currency,
			ProviderBindingVersion: EpaySubscriptionBindingVersion,
			ProviderOrderType:      StripeOrderTypeSubscription,
			ProviderMode:           StripeCheckoutModePayment,
			EntitlementSnapshot:    string(snapshotJSON),
			CapacityReserved:       true,
			CreateTime:             now,
			Status:                 TopUpStatusPending,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// CreateBoundStripeSubscriptionOrder reserves purchase capacity and snapshots
// both the validated Stripe Price and local entitlements in one transaction
// under the user's row lock.
func CreateBoundStripeSubscriptionOrder(userId, planId int, validated ValidatedStripePrice, tradeNo string) (*model.SubscriptionOrder, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	validated.ID = strings.TrimSpace(validated.ID)
	validated.Currency = strings.TrimSpace(validated.Currency)
	if userId <= 0 || planId <= 0 || !strings.HasPrefix(tradeNo, "sub_ref_") ||
		len(tradeNo) > 255 || validated.ID == "" || len(validated.ID) > 255 || validated.AmountMinor <= 0 {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	currency, err := NormalizeStripeCurrency(validated.Currency)
	if err != nil || !StripeCurrencySupported(currency) {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	validated.Currency = currency

	var created model.SubscriptionOrder
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var user model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		var plan model.SubscriptionPlan
		if err := tx.Where("id = ?", planId).First(&plan).Error; err != nil {
			return err
		}
		NormalizeSubscriptionPlanDefaults(&plan)
		if !plan.Enabled || strings.TrimSpace(plan.StripePriceId) != validated.ID {
			return ErrSubscriptionOrderDataInvalid
		}
		planCurrency, err := NormalizeStripeCurrency(plan.Currency)
		if err != nil || planCurrency != validated.Currency {
			return ErrStripePaymentMismatch
		}
		minor, err := StripeMoneyToMinorUnitsForCurrency(strings.TrimSpace(plan.PriceAmount), planCurrency)
		if err != nil || minor <= 0 || minor != validated.AmountMinor {
			return ErrStripePaymentMismatch
		}
		price, err := ParseSubscriptionPlanPrice(plan.PriceAmount)
		if err != nil || !validSubscriptionOrderMoney(price) {
			return ErrSubscriptionOrderDataInvalid
		}
		if plan.MaxPurchasePerUser < 0 {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, ok := boundedSubscriptionQuota(plan.TotalAmount); !ok {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, err := calcPlanEndTime(time.Unix(now, 0), &plan); err != nil {
			return ErrSubscriptionOrderDataInvalid
		}
		if plan.MaxPurchasePerUser > 0 {
			used, err := countSubscriptionCapacityUsedTx(tx, userId, plan.Id)
			if err != nil {
				return err
			}
			if used >= int64(plan.MaxPurchasePerUser) {
				return ErrSubscriptionPurchaseLimit
			}
		}

		snapshot := subscriptionEntitlementSnapshotFromPlan(&plan)
		snapshotJSON, err := jsonutil.Marshal(snapshot)
		if err != nil {
			return err
		}
		created = model.SubscriptionOrder{
			UserId:                 userId,
			PlanId:                 plan.Id,
			Money:                  price,
			TradeNo:                tradeNo,
			PaymentMethod:          PaymentMethodStripe,
			PaymentProvider:        PaymentProviderStripe,
			ProviderAmountMinor:    validated.AmountMinor,
			ProviderCurrency:       validated.Currency,
			ProviderBindingVersion: StripeSubscriptionCheckoutBindingVersion,
			ProviderOrderType:      StripeOrderTypeSubscription,
			ProviderMode:           StripeCheckoutModeSubscription,
			ProviderPriceId:        validated.ID,
			EntitlementSnapshot:    string(snapshotJSON),
			CapacityReserved:       true,
			ReconciliationState:    StripeReconciliationCreationUnknown,
			ReconciliationDetail:   "checkout_not_yet_bound",
			CreateTime:             now,
			Status:                 TopUpStatusPending,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func subscriptionEntitlementSnapshotFromPlan(plan *model.SubscriptionPlan) SubscriptionEntitlementSnapshot {
	allowWalletOverflow := true
	if plan != nil && plan.AllowWalletOverflow != nil {
		allowWalletOverflow = *plan.AllowWalletOverflow
	}
	if plan == nil {
		return SubscriptionEntitlementSnapshot{AllowWalletOverflow: allowWalletOverflow}
	}
	return SubscriptionEntitlementSnapshot{
		PlanID:                  plan.Id,
		Title:                   plan.Title,
		DurationUnit:            plan.DurationUnit,
		DurationValue:           plan.DurationValue,
		CustomSeconds:           plan.CustomSeconds,
		TotalAmount:             plan.TotalAmount,
		QuotaResetPeriod:        NormalizeSubscriptionResetPeriod(plan.QuotaResetPeriod),
		QuotaResetCustomSeconds: plan.QuotaResetCustomSeconds,
		UpgradeGroup:            strings.TrimSpace(plan.UpgradeGroup),
		DowngradeGroup:          strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow:     allowWalletOverflow,
		MaxPurchasePerUser:      plan.MaxPurchasePerUser,
	}
}

func planFromSubscriptionSnapshot(raw string, expectedPlanID int) (*model.SubscriptionPlan, error) {
	var snapshot SubscriptionEntitlementSnapshot
	if strings.TrimSpace(raw) == "" || jsonutil.Unmarshal([]byte(raw), &snapshot) != nil ||
		snapshot.PlanID != expectedPlanID || snapshot.PlanID <= 0 || snapshot.MaxPurchasePerUser < 0 {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	allow := snapshot.AllowWalletOverflow
	plan := &model.SubscriptionPlan{
		Id:                      snapshot.PlanID,
		Title:                   snapshot.Title,
		DurationUnit:            snapshot.DurationUnit,
		DurationValue:           snapshot.DurationValue,
		CustomSeconds:           snapshot.CustomSeconds,
		TotalAmount:             snapshot.TotalAmount,
		QuotaResetPeriod:        snapshot.QuotaResetPeriod,
		QuotaResetCustomSeconds: snapshot.QuotaResetCustomSeconds,
		UpgradeGroup:            snapshot.UpgradeGroup,
		DowngradeGroup:          snapshot.DowngradeGroup,
		AllowWalletOverflow:     &allow,
		MaxPurchasePerUser:      snapshot.MaxPurchasePerUser,
	}
	if _, ok := boundedSubscriptionQuota(plan.TotalAmount); !ok {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	if _, err := calcPlanEndTime(time.Unix(0, 0), plan); err != nil {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	return plan, nil
}

// CreateStripeSubscriptionOrder persists the local pending order and its
// immutable Stripe settlement expectation before a Checkout session is
// created. priceText is converted exactly to minor units; sub-cent prices and
// non-decimal formats are rejected.
func CreateStripeSubscriptionOrder(userId, planId int, priceText, currency, tradeNo string) (*model.SubscriptionOrder, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	price, err := ParseSubscriptionPlanPrice(priceText)
	if err != nil || userId <= 0 || planId <= 0 || !validSubscriptionOrderMoney(price) || tradeNo == "" || len(tradeNo) > 255 {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	currency, err = NormalizeStripeCurrency(currency)
	if err != nil {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	minorText := strings.TrimSpace(priceText)
	if minorText == "" {
		minorText = "0"
	}
	minor, err := StripeMoneyToMinorUnitsForCurrency(minorText, currency)
	if err != nil || minor <= 0 {
		return nil, ErrSubscriptionOrderDataInvalid
	}
	var order model.SubscriptionOrder
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		order = model.SubscriptionOrder{
			UserId:              userId,
			PlanId:              planId,
			Money:               price,
			TradeNo:             tradeNo,
			PaymentMethod:       PaymentMethodStripe,
			PaymentProvider:     PaymentProviderStripe,
			ProviderAmountMinor: minor,
			ProviderCurrency:    currency,
			CreateTime:          now,
			Status:              TopUpStatusPending,
		}
		return tx.Create(&order).Error
	}); err != nil {
		return nil, err
	}
	return &order, nil
}

// CompleteSubscriptionOrder fulfills a paid subscription order: it creates
// the user subscription, records the top-up row, and marks the order
// successful (reference transaction: idempotent on success, provider- and
// status-validated, user-row locked so concurrent completions serialize the
// per-user purchase cap).
func CompleteSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string) error {
	return completeSubscriptionOrder(tradeNo, providerPayload, expectedPaymentProvider, actualPaymentMethod, nil, nil, nil, nil, nil)
}

type epaySubscriptionSettlement struct {
	amountMinor   int64
	paymentMethod string
}

// CompleteBoundEpaySubscriptionOrder validates the signed callback's exact
// amount and payment method against the order snapshot in the same transaction
// that creates the entitlement and marks the order successful.
func CompleteBoundEpaySubscriptionOrder(tradeNo, providerPayload, actualPaymentMethod, providerMoney string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	actualPaymentMethod = strings.TrimSpace(actualPaymentMethod)
	if tradeNo == "" || actualPaymentMethod == "" || len(actualPaymentMethod) > 50 ||
		len(providerPayload) > maxEpayProviderPayloadLength {
		return ErrEpayPaymentMismatch
	}
	amountMinor, err := StripeMoneyToMinorUnitsForCurrency(strings.TrimSpace(providerMoney), "USD")
	if err != nil || amountMinor <= 0 {
		return ErrEpayPaymentMismatch
	}
	return completeSubscriptionOrder(tradeNo, providerPayload, PaymentProviderEpay, "", nil, nil,
		&epaySubscriptionSettlement{amountMinor: amountMinor, paymentMethod: actualPaymentMethod}, nil, nil)
}

// CompleteStripeSubscriptionOrder validates the signed Checkout amount and
// currency against the order snapshot in the fulfillment transaction.
func CompleteStripeSubscriptionOrder(tradeNo string, providerPayload string, amountTotal *int64, currency string) error {
	if amountTotal == nil || *amountTotal <= 0 {
		return ErrStripePaymentMismatch
	}
	normalizedCurrency, err := NormalizeStripeCurrency(currency)
	if err != nil {
		return err
	}
	return completeSubscriptionOrder(tradeNo, providerPayload, PaymentProviderStripe, "", &stripeSettlement{
		amountMinor: *amountTotal,
		currency:    normalizedCurrency,
	}, nil, nil, nil, nil)
}

// CompleteBoundStripeSubscriptionOrder fulfills only the exact Checkout
// Session bound to the reserved local order.
func CompleteBoundStripeSubscriptionOrder(tradeNo, providerPayload, sessionID, mode, orderType, priceID, customerID string, amountTotal *int64, currency string) error {
	if amountTotal == nil || *amountTotal <= 0 {
		if legacy, err := legacySuccessfulStripeSubscriptionOrder(tradeNo); err == nil && legacy {
			return nil
		}
		return reconcileBoundStripeSubscriptionError(tradeNo, ErrStripePaymentMismatch)
	}
	normalizedCurrency, err := NormalizeStripeCurrency(currency)
	if err != nil {
		if legacy, lookupErr := legacySuccessfulStripeSubscriptionOrder(tradeNo); lookupErr == nil && legacy {
			return nil
		}
		return reconcileBoundStripeSubscriptionError(tradeNo, err)
	}
	err = completeSubscriptionOrder(tradeNo, providerPayload, PaymentProviderStripe, "",
		&stripeSettlement{amountMinor: *amountTotal, currency: normalizedCurrency, customerID: strings.TrimSpace(customerID)},
		&stripeCheckoutBinding{sessionID: strings.TrimSpace(sessionID), mode: mode, orderType: orderType, priceID: strings.TrimSpace(priceID)}, nil, nil, nil)
	return reconcileBoundStripeSubscriptionError(tradeNo, err)
}

func legacySuccessfulStripeSubscriptionOrder(tradeNo string) (bool, error) {
	var order model.SubscriptionOrder
	if err := model.DB.Select("status", "provider_binding_version").Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
		return false, err
	}
	return order.Status == TopUpStatusSuccess && order.ProviderBindingVersion < StripeSubscriptionCheckoutBindingVersion, nil
}

func completeSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string, stripePayment *stripeSettlement, checkoutBinding *stripeCheckoutBinding, epayPayment *epaySubscriptionSettlement, creemPayment *CreemSettlement, waffoPancakePayment *WaffoPancakeSettlement) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	var logUserId int
	var logPlanTitle string
	var logMoney float64
	var logPaymentMethod string
	auditEventID := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		completeTime, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if order.Status == TopUpStatusSuccess && order.ProviderBindingVersion < StripeSubscriptionCheckoutBindingVersion &&
			epayPayment == nil && creemPayment == nil && waffoPancakePayment == nil &&
			order.PaymentProvider != PaymentProviderCreem && order.PaymentProvider != PaymentProviderWaffoPancake {
			return nil
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.PaymentProvider == PaymentProviderEpay &&
			order.ProviderBindingVersion == EpaySubscriptionBindingVersion && epayPayment == nil {
			return ErrEpayPaymentMismatch
		}
		if order.PaymentProvider == PaymentProviderCreem && creemPayment == nil {
			return ErrCreemPaymentMismatch
		}
		if order.PaymentProvider == PaymentProviderWaffoPancake && waffoPancakePayment == nil {
			return ErrWaffoPancakePaymentMismatch
		}
		if checkoutBinding != nil {
			if err := validateOrClaimSubscriptionCheckoutBindingTx(tx, &order, checkoutBinding); err != nil {
				return err
			}
		}
		if stripePayment != nil {
			expectedCurrency, currencyErr := NormalizeStripeCurrency(order.ProviderCurrency)
			if order.PaymentProvider != PaymentProviderStripe || order.PaymentMethod != PaymentMethodStripe ||
				order.ProviderAmountMinor <= 0 || order.ProviderCurrency == "" || currencyErr != nil ||
				order.ProviderAmountMinor != stripePayment.amountMinor || expectedCurrency != stripePayment.currency {
				return ErrStripePaymentMismatch
			}
		}
		if epayPayment != nil {
			if order.PaymentProvider != PaymentProviderEpay ||
				order.ProviderBindingVersion != EpaySubscriptionBindingVersion ||
				order.ProviderAmountMinor <= 0 || order.ProviderAmountMinor != epayPayment.amountMinor ||
				order.ProviderCurrency != "USD" || order.PaymentMethod != epayPayment.paymentMethod ||
				order.ProviderOrderType != StripeOrderTypeSubscription || order.ProviderMode != StripeCheckoutModePayment ||
				strings.TrimSpace(order.EntitlementSnapshot) == "" ||
				(order.Status == TopUpStatusPending && !order.CapacityReserved) {
				return ErrEpayPaymentMismatch
			}
		}
		if creemPayment != nil {
			if err := validateCreemSubscriptionSettlementTx(tx, &order, creemPayment); err != nil {
				return err
			}
		}
		if waffoPancakePayment != nil {
			if err := validateWaffoPancakeSubscriptionSettlementTx(&order, waffoPancakePayment); err != nil {
				return err
			}
		}
		if order.Status == TopUpStatusSuccess {
			return nil
		}
		if order.Status != TopUpStatusPending {
			return ErrSubscriptionOrderStatusInvalid
		}
		if order.UserId <= 0 || order.PlanId <= 0 || !validSubscriptionOrderMoney(order.Money) ||
			len(order.PaymentMethod) > 50 || len(actualPaymentMethod) > 50 {
			return ErrSubscriptionOrderDataInvalid
		}
		var plan *model.SubscriptionPlan
		var err error
		if checkoutBinding != nil {
			if !order.CapacityReserved || strings.TrimSpace(order.ProviderPriceId) == "" {
				return ErrSubscriptionOrderDataInvalid
			}
			plan, err = planFromSubscriptionSnapshot(order.EntitlementSnapshot, order.PlanId)
		} else if epayPayment != nil || creemPayment != nil || waffoPancakePayment != nil {
			plan, err = planFromSubscriptionSnapshot(order.EntitlementSnapshot, order.PlanId)
		} else {
			plan, err = getSubscriptionPlanByIdTx(tx, order.PlanId)
			if err == nil {
				NormalizeSubscriptionPlanDefaults(plan)
			}
		}
		if err != nil {
			return err
		}
		// Lock the user row: concurrent completions of different orders for
		// the same user must serialize the MaxPurchasePerUser check.
		var userRow model.User
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "stripe_customer").Where("id = ?", order.UserId).First(&userRow).Error; err != nil {
			return err
		}
		if stripePayment != nil {
			if err := bindStripeCustomerTx(tx, &userRow, stripePayment.customerID); err != nil {
				return err
			}
		}
		if _, err := createUserSubscriptionFromPlanTxWithCapacity(tx, order.UserId, plan, "order", checkoutBinding == nil && epayPayment == nil && creemPayment == nil && waffoPancakePayment == nil); err != nil {
			return err
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		updates := map[string]any{
			"status":                          TopUpStatusSuccess,
			"complete_time":                   completeTime,
			"capacity_reserved":               false,
			"reconciliation_state":            "",
			"reconciliation_detail":           "",
			"reconciliation_next_at":          0,
			"reconciliation_lease_owner":      "",
			"reconciliation_lease_expires_at": 0,
		}
		if providerPayload != "" {
			updates["provider_payload"] = providerPayload
		}
		if actualPaymentMethod != "" && order.PaymentMethod != actualPaymentMethod {
			updates["payment_method"] = actualPaymentMethod
		}
		if checkoutBinding != nil && checkoutBinding.leaseOwner != "" {
			updates["provider_session_id"] = checkoutBinding.sessionID
			updates["provider_expires_at"] = checkoutBinding.providerExpiresAt
		}
		transitionQuery := tx.Model(&model.SubscriptionOrder{}).
			Where(`id = ? AND status = ? AND complete_time = ? AND provider_payload = ? AND payment_method = ?`,
				order.Id, TopUpStatusPending, order.CompleteTime, order.ProviderPayload, order.PaymentMethod)
		if checkoutBinding != nil && checkoutBinding.leaseOwner != "" {
			transitionQuery = transitionQuery.Where("reconciliation_lease_owner = ?", checkoutBinding.leaseOwner)
		}
		transition := transitionQuery.
			Updates(updates)
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrSubscriptionOrderStatusInvalid
		}
		order.Status = TopUpStatusSuccess
		order.CompleteTime = completeTime
		if providerPayload != "" {
			order.ProviderPayload = providerPayload
		}
		if actualPaymentMethod != "" {
			order.PaymentMethod = actualPaymentMethod
		}
		logUserId = order.UserId
		logPlanTitle = plan.Title
		logMoney = order.Money
		logPaymentMethod = order.PaymentMethod
		content := fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", logPlanTitle, logMoney, logPaymentMethod)
		var auditErr error
		auditEventID, auditErr = enqueuePaymentSystemLogTx(tx, "subscription", order.TradeNo, logUserId, content, completeTime)
		if auditErr != nil {
			return fmt.Errorf("persist subscription payment audit: %w", auditErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if logUserId > 0 {
		if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
			logging.SysError("subscription payment audit delivery deferred trade_no=" + tradeNo + ": " + err.Error())
		}
	}
	return nil
}

func subscriptionCheckoutBindingMatches(order *model.SubscriptionOrder, binding *stripeCheckoutBinding) bool {
	return order != nil && binding != nil && order.ProviderSessionId != nil &&
		strings.TrimSpace(*order.ProviderSessionId) != "" && *order.ProviderSessionId == binding.sessionID &&
		order.ProviderOrderType == StripeOrderTypeSubscription && binding.orderType == StripeOrderTypeSubscription &&
		order.ProviderMode == StripeCheckoutModeSubscription && binding.mode == StripeCheckoutModeSubscription &&
		order.ProviderPriceId != "" && order.ProviderPriceId == binding.priceID
}

func validateOrClaimSubscriptionCheckoutBindingTx(tx *gorm.DB, order *model.SubscriptionOrder, binding *stripeCheckoutBinding) error {
	if tx == nil || order == nil || binding == nil ||
		order.ProviderBindingVersion < StripeSubscriptionCheckoutBindingVersion {
		return ErrStripeLegacyOrderRequiresReview
	}
	binding.sessionID = strings.TrimSpace(binding.sessionID)
	if !strings.HasPrefix(binding.sessionID, "cs_") ||
		order.ProviderOrderType != StripeOrderTypeSubscription || binding.orderType != StripeOrderTypeSubscription ||
		order.ProviderMode != StripeCheckoutModeSubscription || binding.mode != StripeCheckoutModeSubscription ||
		strings.TrimSpace(order.ProviderPriceId) == "" || order.ProviderPriceId != strings.TrimSpace(binding.priceID) {
		return ErrStripeCheckoutBindingMismatch
	}
	if binding.leaseOwner != "" && order.Status == TopUpStatusPending {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		if order.ReconciliationLeaseOwner != binding.leaseOwner || order.ReconciliationLeaseExpiresAt <= now ||
			order.CheckoutRequest != binding.checkoutRequest || order.CheckoutFingerprint != binding.checkoutFingerprint ||
			(order.ProviderExpiresAt > 0 && order.ProviderExpiresAt != binding.providerExpiresAt) {
			return ErrStripeCheckoutReconciliationLeaseLost
		}
	}
	if order.ProviderSessionId != nil {
		if subscriptionCheckoutBindingMatches(order, binding) {
			return nil
		}
		return ErrStripeCheckoutBindingMismatch
	}
	if order.Status != TopUpStatusPending || !order.CapacityReserved ||
		!stripeReconciliationAllowsSessionClaim(order.ReconciliationState) {
		return ErrStripeCheckoutBindingMismatch
	}
	claimQuery := tx.Model(&model.SubscriptionOrder{}).
		Where(`id = ? AND status = ? AND capacity_reserved = ? AND provider_binding_version >= ?
			AND provider_session_id IS NULL AND provider_order_type = ? AND provider_mode = ? AND provider_price_id = ?
			AND reconciliation_state IN ?`,
			order.Id, TopUpStatusPending, true, StripeSubscriptionCheckoutBindingVersion,
			StripeOrderTypeSubscription, StripeCheckoutModeSubscription, binding.priceID,
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

func markSubscriptionOrderReconciliation(tradeNo, state, detail string) error {
	if model.DB == nil || strings.TrimSpace(tradeNo) == "" {
		return errors.New("subscription database is unavailable")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Select("id", "status", "reconciliation_state", "reconciliation_detail").
			Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if order.Status == TopUpStatusSuccess {
			return ErrSubscriptionOrderStatusInvalid
		}
		if order.ReconciliationState == state && order.ReconciliationDetail == detail {
			return nil
		}
		result := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ?", order.Id, order.Status).
			Updates(map[string]any{"reconciliation_state": state, "reconciliation_detail": detail})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSubscriptionOrderStatusInvalid
		}
		return nil
	})
}

func reconcileBoundStripeSubscriptionError(tradeNo string, err error) error {
	if err == nil || errors.Is(err, ErrSubscriptionOrderNotFound) {
		return err
	}
	if markErr := markSubscriptionOrderReconciliation(tradeNo, reconciliationStateForStripeError(err), err.Error()); markErr != nil {
		return fmt.Errorf("%w: persist reconciliation evidence: %v", err, markErr)
	}
	return err
}

// FlagStripeSubscriptionReconciliation records a non-terminal Checkout
// creation uncertainty while preserving the reserved order for reconciliation.
func FlagStripeSubscriptionReconciliation(tradeNo, state string) error {
	if state != StripeReconciliationBindingMismatch && state != StripeReconciliationCreationUnknown {
		return ErrStripeCheckoutBindingMismatch
	}
	return markSubscriptionOrderReconciliation(tradeNo, state, state)
}

// BindStripeSubscriptionSession records the validated Checkout Session before
// returning its URL. The immutable Price and entitlement snapshots already
// exist on the order at this point.
func BindStripeSubscriptionSession(tradeNo, sessionID string) error {
	return BindStripeSubscriptionSessionWithExpiry(tradeNo, sessionID, 0)
}

// BindStripeSubscriptionSessionWithExpiry persists the provider session state
// and schedules a provider check so a missing webhook cannot hold purchase
// capacity forever. expiresAt may be zero for legacy callers and tests.
func BindStripeSubscriptionSessionWithExpiry(tradeNo, sessionID string, expiresAt int64) error {
	tradeNo = strings.TrimSpace(tradeNo)
	sessionID = strings.TrimSpace(sessionID)
	if !strings.HasPrefix(tradeNo, "sub_ref_") || !strings.HasPrefix(sessionID, "cs_") || expiresAt < 0 {
		return ErrStripeCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if order.ProviderBindingVersion != StripeSubscriptionCheckoutBindingVersion ||
			order.ProviderOrderType != StripeOrderTypeSubscription ||
			order.ProviderMode != StripeCheckoutModeSubscription || !order.CapacityReserved {
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
			return ErrSubscriptionOrderStatusInvalid
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
		result := tx.Model(&model.SubscriptionOrder{}).
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

// upsertSubscriptionTopUpTx records (or completes) the financial top-up row
// backing a subscription order (reference semantics: the order's money is
// the top-up amount; payment-method mismatches are rejected).
func upsertSubscriptionTopUpTx(tx *gorm.DB, order *model.SubscriptionOrder) error {
	if tx == nil || order == nil {
		return errors.New("invalid subscription order")
	}
	if order.UserId <= 0 || !validSubscriptionOrderMoney(order.Money) || len(order.PaymentMethod) > 50 {
		return ErrSubscriptionOrderDataInvalid
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return err
	}
	var topup model.TopUp
	if err := tx.Where("trade_no = ?", order.TradeNo).First(&topup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			topup = model.TopUp{
				UserId:          order.UserId,
				Amount:          0,
				Money:           order.Money,
				TradeNo:         order.TradeNo,
				PaymentMethod:   order.PaymentMethod,
				PaymentProvider: order.PaymentProvider,
				CreateTime:      order.CreateTime,
				CompleteTime:    now,
				Status:          TopUpStatusSuccess,
			}
			return tx.Create(&topup).Error
		}
		return err
	}
	if topup.UserId != order.UserId || topup.Amount != 0 {
		return ErrSubscriptionOrderDataInvalid
	}
	if topup.Status != TopUpStatusPending && topup.Status != TopUpStatusSuccess {
		return ErrSubscriptionOrderStatusInvalid
	}
	paymentMethod := topup.PaymentMethod
	if topup.PaymentMethod == "" {
		paymentMethod = order.PaymentMethod
	} else if topup.PaymentMethod != order.PaymentMethod {
		return ErrPaymentMethodMismatch
	}
	createTime := topup.CreateTime
	if createTime == 0 {
		createTime = order.CreateTime
	}
	if topup.Money == order.Money && topup.PaymentMethod == paymentMethod &&
		topup.CreateTime == createTime && topup.CompleteTime == now && topup.Status == TopUpStatusSuccess {
		return nil
	}
	transition := tx.Model(&model.TopUp{}).
		Where(`id = ? AND user_id = ? AND amount = ? AND money = ? AND payment_method = ?
			AND create_time = ? AND complete_time = ? AND status = ?`,
			topup.Id, topup.UserId, topup.Amount, topup.Money, topup.PaymentMethod,
			topup.CreateTime, topup.CompleteTime, topup.Status).
		Updates(map[string]any{
			"money":          order.Money,
			"payment_method": paymentMethod,
			"create_time":    createTime,
			"complete_time":  now,
			"status":         TopUpStatusSuccess,
		})
	if transition.Error != nil {
		return transition.Error
	}
	if transition.RowsAffected != 1 {
		return ErrSubscriptionOrderStatusInvalid
	}
	return nil
}

// ExpireSubscriptionOrder marks a pending subscription order as expired
// (reference checkout.session.expired handling).
func ExpireSubscriptionOrder(tradeNo string, expectedPaymentProvider string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != TopUpStatusPending {
			return nil
		}
		transition := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status":                          TopUpStatusExpired,
				"complete_time":                   now,
				"capacity_reserved":               false,
				"reconciliation_next_at":          0,
				"reconciliation_lease_owner":      "",
				"reconciliation_lease_expires_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrSubscriptionOrderStatusInvalid
		}
		return nil
	})
}

// UpdateBoundPendingSubscriptionOrderStatus applies a failure/expiry only to
// the exact bound subscription Checkout Session. Successful legacy rows are
// still harmless duplicate callbacks; legacy pending rows are quarantined.
func UpdateBoundPendingSubscriptionOrderStatus(tradeNo, sessionID, mode, orderType, priceID, status string, amountTotal *int64, currency string) error {
	if tradeNo == "" {
		return ErrSubscriptionOrderNotFound
	}
	switch status {
	case TopUpStatusFailed, TopUpStatusCancelled, TopUpStatusExpired:
	default:
		return ErrSubscriptionOrderStatusInvalid
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if order.Status == TopUpStatusSuccess && order.ProviderBindingVersion < StripeSubscriptionCheckoutBindingVersion {
			return nil
		}
		if order.PaymentProvider != PaymentProviderStripe {
			return ErrPaymentMethodMismatch
		}
		normalizedCurrency, currencyErr := NormalizeStripeCurrency(currency)
		if amountTotal == nil || *amountTotal <= 0 || currencyErr != nil ||
			order.ProviderAmountMinor != *amountTotal || order.ProviderCurrency != normalizedCurrency {
			return ErrStripePaymentMismatch
		}
		binding := &stripeCheckoutBinding{sessionID: strings.TrimSpace(sessionID), mode: mode, orderType: orderType, priceID: strings.TrimSpace(priceID)}
		if err := validateOrClaimSubscriptionCheckoutBindingTx(tx, &order, binding); err != nil {
			return err
		}
		if order.Status != TopUpStatusPending {
			return nil
		}
		transition := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status":                          status,
				"complete_time":                   now,
				"capacity_reserved":               false,
				"reconciliation_state":            "",
				"reconciliation_detail":           "",
				"reconciliation_next_at":          0,
				"reconciliation_lease_owner":      "",
				"reconciliation_lease_expires_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrSubscriptionOrderStatusInvalid
		}
		return nil
	})
	if errors.Is(err, ErrStripeLegacyOrderRequiresReview) || errors.Is(err, ErrStripeCheckoutBindingMismatch) || errors.Is(err, ErrStripePaymentMismatch) {
		if markErr := markSubscriptionOrderReconciliation(tradeNo, reconciliationStateForStripeError(err), err.Error()); markErr != nil {
			return fmt.Errorf("%w: persist reconciliation evidence: %v", err, markErr)
		}
	}
	return err
}

// UpdateBoundPendingTopUpStatus is the wallet counterpart of the bound
// subscription terminal transition.
func UpdateBoundPendingTopUpStatus(tradeNo, sessionID, mode, orderType, status string, amountTotal *int64, currency string) error {
	if tradeNo == "" {
		return ErrTopUpNotFound
	}
	switch status {
	case TopUpStatusFailed, TopUpStatusCancelled, TopUpStatusExpired:
	default:
		return ErrTopUpStatusInvalid
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
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
		if order.Status == TopUpStatusSuccess && order.ProviderBindingVersion == 0 {
			return nil
		}
		if order.PaymentProvider != PaymentProviderStripe {
			return ErrPaymentMethodMismatch
		}
		normalizedCurrency, currencyErr := NormalizeStripeCurrency(currency)
		if amountTotal == nil || *amountTotal <= 0 || currencyErr != nil ||
			order.ProviderAmountMinor != *amountTotal || order.ProviderCurrency != normalizedCurrency {
			return ErrStripePaymentMismatch
		}
		binding := &stripeCheckoutBinding{sessionID: strings.TrimSpace(sessionID), mode: mode, orderType: orderType}
		if err := validateOrClaimTopUpCheckoutBindingTx(tx, &order, binding); err != nil {
			return err
		}
		if order.Status != TopUpStatusPending {
			return nil
		}
		transition := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status":                          status,
				"complete_time":                   now,
				"reconciliation_state":            "",
				"reconciliation_detail":           "",
				"reconciliation_next_at":          0,
				"reconciliation_lease_owner":      "",
				"reconciliation_lease_expires_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrTopUpStatusInvalid
		}
		return nil
	})
	if errors.Is(err, ErrStripeLegacyOrderRequiresReview) || errors.Is(err, ErrStripeCheckoutBindingMismatch) || errors.Is(err, ErrStripePaymentMismatch) {
		if markErr := markTopUpReconciliation(tradeNo, reconciliationStateForStripeError(err), err.Error()); markErr != nil {
			return fmt.Errorf("%w: persist reconciliation evidence: %v", err, markErr)
		}
	}
	return err
}

// UpdatePendingSubscriptionOrderStatus applies a non-success terminal state
// only while a subscription order is still pending. It is idempotent for
// delayed or duplicated provider notifications and never overwrites success.
func UpdatePendingSubscriptionOrderStatus(tradeNo, expectedPaymentProvider, status string) error {
	if tradeNo == "" {
		return ErrSubscriptionOrderNotFound
	}
	switch status {
	case TopUpStatusFailed, TopUpStatusCancelled, TopUpStatusExpired:
	default:
		return ErrSubscriptionOrderStatusInvalid
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var order model.SubscriptionOrder
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != TopUpStatusPending {
			return nil
		}
		transition := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status":                          status,
				"complete_time":                   now,
				"capacity_reserved":               false,
				"reconciliation_state":            "",
				"reconciliation_detail":           "",
				"reconciliation_next_at":          0,
				"reconciliation_lease_owner":      "",
				"reconciliation_lease_expires_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrSubscriptionOrderStatusInvalid
		}
		return nil
	})
}

// UpdatePendingTopUpStatus moves a pending top-up order to the given status
// (webhook fallback for legacy top-up orders).
func UpdatePendingTopUpStatus(tradeNo string, expectedProvider string, status string) error {
	if tradeNo == "" {
		return ErrTopUpNotFound
	}
	switch status {
	case TopUpStatusFailed, TopUpStatusCancelled, TopUpStatusExpired:
	default:
		return ErrTopUpStatusInvalid
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var topup model.TopUp
		if err := locking.SubscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&topup).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if expectedProvider != "" && topup.PaymentProvider != expectedProvider {
			return ErrPaymentMethodMismatch
		}
		if topup.Status != TopUpStatusPending {
			return nil
		}
		transition := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ?", topup.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status":                          status,
				"complete_time":                   now,
				"reconciliation_state":            "",
				"reconciliation_detail":           "",
				"reconciliation_next_at":          0,
				"reconciliation_lease_owner":      "",
				"reconciliation_lease_expires_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrTopUpStatusInvalid
		}
		return nil
	})
}

// NewSubscriptionEpayTradeNo builds the reference-compatible subscription
// EPay merchant order number.
func NewSubscriptionEpayTradeNo(userId int, seconds int64, randSuffix string) string {
	return fmt.Sprintf("SUBUSR%dNO%s%d", userId, randSuffix, seconds)
}

// NewSubscriptionStripeTradeNo builds the trade-no for subscription Stripe
// orders (reference format: sub_ref_ + sha1 of the order reference).
func NewSubscriptionStripeTradeNo(userId int, millis int64, randSuffix string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("sub-stripe-ref-%d-%d-%s", userId, millis, randSuffix)))
	return "sub_ref_" + hex.EncodeToString(sum[:])
}
