package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentMethodWaffoPancake   = "waffo_pancake"

	WaffoPancakeBindingVersion = 1
	WaffoPancakeCreditVersion  = 1
	WaffoPancakeOrderWallet    = "wallet"
	WaffoPancakeOrderSubscribe = "subscription"
	WaffoPancakeCheckoutMode   = "authenticated"
	WaffoPancakeModeTest       = "test"
	WaffoPancakeModeProd       = "prod"
	WaffoPancakeCreateUnknown  = "checkout_creation_uncertain"
	WaffoPancakePending        = "checkout_pending"

	waffoPancakeCheckoutLifetime  = 45 * 60
	maxWaffoPancakeSnapshotLength = 64 << 10
	maxWaffoPancakePayloadLength  = 64 << 10

	defaultWaffoPancakeStoreName   = "tokenrouter-store"
	defaultWaffoPancakeProductName = "tokenrouter-charge-product"
)

var (
	ErrWaffoPancakePaymentMismatch         = errors.New("Waffo Pancake 支付金额、币种或订单不匹配")
	ErrWaffoPancakeCheckoutBindingMismatch = errors.New("Waffo Pancake Checkout 订单绑定不匹配")
	ErrWaffoPancakeLegacyOrderReview       = errors.New("Waffo Pancake 旧订单缺少安全绑定，需要人工核对")
)

// WaffoPancakeSettlement contains only fields authenticated by the verified
// raw webhook body. It is compared with the immutable local order snapshot in
// the same transaction that grants wallet or subscription value.
type WaffoPancakeSettlement struct {
	EventID         string
	ProviderOrderID string
	TradeNo         string
	StoreID         string
	Mode            string
	BuyerIdentity   string
	Currency        string
	Amount          string
	ProductName     string
}

func WaffoPancakeBuyerIdentityFromUserID(userID int) string {
	if userID <= 0 {
		return ""
	}
	return fmt.Sprintf("tokenrouter-user-%d", userID)
}

func GetWaffoPancakeTopupMoney(amount int64, group string, unitPrice float64) (float64, error) {
	return calculateTopUpMoney(amount, group, unitPrice)
}

func FormatWaffoPancakeAmount(money float64) (string, error) {
	_, formatted, err := NormalizePayMoney(money)
	if err != nil {
		return "", ErrWaffoPancakePaymentMismatch
	}
	minor, err := StripeMoneyToMinorUnitsForCurrency(formatted, "USD")
	if err != nil || minor <= 0 {
		return "", ErrWaffoPancakePaymentMismatch
	}
	return formatted, nil
}

// WaffoPancakeSettlementFromEvent creates the settlement value only after the
// event signature has been verified by VerifyConfiguredWaffoPancakeWebhook.
func WaffoPancakeSettlementFromEvent(event *WaffoPancakeWebhookEvent) (WaffoPancakeSettlement, error) {
	if event == nil {
		return WaffoPancakeSettlement{}, ErrWaffoPancakePaymentMismatch
	}
	settlement := WaffoPancakeSettlement{
		EventID: event.ID, ProviderOrderID: event.Data.OrderID,
		TradeNo: event.Data.OrderMerchantExternalID, StoreID: event.StoreID, Mode: event.Mode,
		BuyerIdentity: event.Data.MerchantProvidedBuyerIdentity, Currency: event.Data.Currency,
		Amount: event.Data.Amount, ProductName: event.Data.ProductName,
	}
	if err := normalizeWaffoPancakeSettlement(&settlement); err != nil {
		return WaffoPancakeSettlement{}, err
	}
	return settlement, nil
}

func CreateBoundWaffoPancakeTopUp(userID int, amount, requestedAmount int64, orderAmount string, config setting.WaffoPancakeConfig, tradeNo string) (*model.TopUp, WaffoPancakeCheckoutRequest, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if userID <= 0 || amount <= 0 || requestedAmount <= 0 || !validWaffoPancakeTradeNo(tradeNo, false) ||
		!setting.ValidWaffoPancakeShortID(config.MerchantID, "MER") ||
		!setting.ValidWaffoPancakeShortID(config.ProductID, "PROD") ||
		!setting.ValidWaffoPancakeShortID(config.StoreID, "STO") {
		return nil, WaffoPancakeCheckoutRequest{}, ErrWaffoPancakeCheckoutBindingMismatch
	}
	if _, err := setting.ParseWaffoPancakePrivateKey(config.PrivateKey); err != nil {
		return nil, WaffoPancakeCheckoutRequest{}, ErrWaffoPancakeCheckoutBindingMismatch
	}
	money, normalizedAmount, amountMinor, err := normalizeNewWaffoPancakeOrderMoney(orderAmount)
	if err != nil {
		return nil, WaffoPancakeCheckoutRequest{}, err
	}
	creditQuota, err := topUpCreditQuota(amount)
	if err != nil {
		return nil, WaffoPancakeCheckoutRequest{}, err
	}

	var created model.TopUp
	var request WaffoPancakeCheckoutRequest
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		now, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "email").First(&user, userID).Error; err != nil {
			return err
		}
		request = newWaffoPancakeCheckoutRequest(config, config.ProductID, user.Id, user.Email, tradeNo, normalizedAmount, requestedAmount)
		snapshot, fingerprint, err := encodeWaffoPancakeSnapshot(request)
		if err != nil {
			return err
		}
		created = model.TopUp{
			UserId: user.Id, Amount: amount, CreditQuota: creditQuota, CreditQuotaVersion: WaffoPancakeCreditVersion,
			Money: money, TradeNo: tradeNo, PaymentMethod: PaymentMethodWaffoPancake,
			PaymentProvider: PaymentProviderWaffoPancake, ProviderAmountMinor: amountMinor, ProviderCurrency: "USD",
			ProviderBindingVersion: WaffoPancakeBindingVersion, ProviderOrderType: WaffoPancakeOrderWallet,
			ProviderMode: WaffoPancakeCheckoutMode, ReconciliationState: WaffoPancakeCreateUnknown,
			ReconciliationDetail: "checkout_not_yet_bound", CheckoutRequest: snapshot,
			CheckoutFingerprint: fingerprint, ProviderCreateIdempotencyKey: tradeNo,
			CreateTime: now, Status: TopUpStatusPending,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, WaffoPancakeCheckoutRequest{}, err
	}
	return &created, request, nil
}

func CreateBoundWaffoPancakeSubscriptionOrder(userID, planID int, config setting.WaffoPancakeConfig, tradeNo string) (*model.SubscriptionOrder, WaffoPancakeCheckoutRequest, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if userID <= 0 || planID <= 0 || !validWaffoPancakeTradeNo(tradeNo, true) ||
		!setting.ValidWaffoPancakeShortID(config.MerchantID, "MER") ||
		!setting.ValidWaffoPancakeShortID(config.StoreID, "STO") {
		return nil, WaffoPancakeCheckoutRequest{}, ErrSubscriptionOrderDataInvalid
	}
	if _, err := setting.ParseWaffoPancakePrivateKey(config.PrivateKey); err != nil {
		return nil, WaffoPancakeCheckoutRequest{}, ErrSubscriptionOrderDataInvalid
	}

	var created model.SubscriptionOrder
	var request WaffoPancakeCheckoutRequest
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "email").First(&user, userID).Error; err != nil {
			return err
		}
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planID).Error; err != nil {
			return err
		}
		NormalizeSubscriptionPlanDefaults(&plan)
		productID := strings.TrimSpace(plan.WaffoPancakeProductId)
		if !plan.Enabled || plan.WaffoPancakeProductId != productID ||
			!setting.ValidWaffoPancakeShortID(productID, "PROD") || plan.MaxPurchasePerUser < 0 {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, ok := boundedSubscriptionQuota(plan.TotalAmount); !ok {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, err := calcPlanEndTime(time.Unix(now, 0), &plan); err != nil {
			return ErrSubscriptionOrderDataInvalid
		}
		money, normalizedAmount, amountMinor, err := normalizeWaffoPancakePlanMoney(plan.PriceAmount)
		if err != nil || !validSubscriptionOrderMoney(money) {
			return ErrSubscriptionOrderDataInvalid
		}
		if plan.MaxPurchasePerUser > 0 {
			used, err := countSubscriptionCapacityUsedTx(tx, userID, plan.Id)
			if err != nil {
				return err
			}
			if used >= int64(plan.MaxPurchasePerUser) {
				return ErrSubscriptionPurchaseLimit
			}
		}
		request = newWaffoPancakeCheckoutRequest(config, productID, user.Id, user.Email, tradeNo, normalizedAmount, 0)
		checkoutSnapshot, checkoutFingerprint, err := encodeWaffoPancakeSnapshot(request)
		if err != nil {
			return err
		}
		entitlement, err := common.Marshal(subscriptionEntitlementSnapshotFromPlan(&plan))
		if err != nil {
			return err
		}
		created = model.SubscriptionOrder{
			UserId: user.Id, PlanId: plan.Id, Money: money, TradeNo: tradeNo,
			PaymentMethod: PaymentMethodWaffoPancake, PaymentProvider: PaymentProviderWaffoPancake,
			ProviderAmountMinor: amountMinor, ProviderCurrency: "USD", ProviderBindingVersion: WaffoPancakeBindingVersion,
			ProviderOrderType: WaffoPancakeOrderSubscribe, ProviderMode: WaffoPancakeCheckoutMode,
			ProviderPriceId: productID, EntitlementSnapshot: string(entitlement), CapacityReserved: true,
			ReconciliationState: WaffoPancakeCreateUnknown, ReconciliationDetail: "checkout_not_yet_bound",
			CheckoutRequest: checkoutSnapshot, CheckoutFingerprint: checkoutFingerprint,
			ProviderCreateIdempotencyKey: tradeNo, CreateTime: now, Status: TopUpStatusPending,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, WaffoPancakeCheckoutRequest{}, err
	}
	return &created, request, nil
}

func newWaffoPancakeCheckoutRequest(config setting.WaffoPancakeConfig, productID string, userID int, email, tradeNo, amount string, requestedAmount int64) WaffoPancakeCheckoutRequest {
	email = strings.TrimSpace(email)
	if !validWaffoPancakeText(email, 320, true) {
		email = ""
	}
	return WaffoPancakeCheckoutRequest{
		ProductID: productID, StoreID: config.StoreID, Currency: "USD",
		PriceSnapshot: WaffoPancakePriceSnapshot{Amount: amount, TaxCategory: "saas"},
		BuyerIdentity: WaffoPancakeBuyerIdentityFromUserID(userID), BuyerEmail: email,
		ExpiresInSeconds: waffoPancakeCheckoutLifetime, OrderMerchantExternalID: tradeNo,
		RequestedAmount: requestedAmount,
	}
}

func normalizeWaffoPancakeOrderMoney(raw string) (float64, string, int64, error) {
	if !validWaffoPancakePrice(raw) {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || value.Sign() <= 0 || !value.Equal(value.Round(2)) {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	normalized := value.StringFixed(2)
	minor, err := StripeMoneyToMinorUnitsForCurrency(normalized, "USD")
	if err != nil || minor <= 0 {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	money, err := strconv.ParseFloat(normalized, 64)
	if err != nil || !finitePositiveMoney(money) {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	return money, normalized, minor, nil
}

func normalizeNewWaffoPancakeOrderMoney(raw string) (float64, string, int64, error) {
	money, normalized, minor, err := normalizeWaffoPancakeOrderMoney(raw)
	if err != nil || money > setting.MaxPaymentProviderAmount {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	return money, normalized, minor, nil
}

func normalizeWaffoPancakePlanMoney(raw string) (float64, string, int64, error) {
	price, err := ParseSubscriptionPlanPrice(raw)
	if err != nil || price < 0.01 {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || value.Sign() <= 0 {
		return 0, "", 0, ErrWaffoPancakePaymentMismatch
	}
	return normalizeNewWaffoPancakeOrderMoney(value.Round(2).StringFixed(2))
}

func validWaffoPancakeTradeNo(tradeNo string, subscription bool) bool {
	if !validWaffoPancakeIdentifier(tradeNo) || len(tradeNo) > 128 {
		return false
	}
	prefix := "WAFFO_PANCAKE-"
	if subscription {
		prefix = "WAFFO_PANCAKE_SUB-"
	}
	return strings.HasPrefix(tradeNo, prefix)
}

func encodeWaffoPancakeSnapshot(request WaffoPancakeCheckoutRequest) (string, string, error) {
	if err := validateWaffoPancakeCheckoutRequest(&request); err != nil {
		return "", "", ErrWaffoPancakeCheckoutBindingMismatch
	}
	encoded, err := common.Marshal(request)
	if err != nil || len(encoded) == 0 || len(encoded) > maxWaffoPancakeSnapshotLength {
		return "", "", ErrWaffoPancakeCheckoutBindingMismatch
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:]), nil
}

func parseWaffoPancakeSnapshot(raw, fingerprint string) (WaffoPancakeCheckoutRequest, error) {
	if raw == "" || len(raw) > maxWaffoPancakeSnapshotLength || len(fingerprint) != sha256.Size*2 {
		return WaffoPancakeCheckoutRequest{}, ErrWaffoPancakeCheckoutBindingMismatch
	}
	digest := sha256.Sum256([]byte(raw))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), fingerprint) {
		return WaffoPancakeCheckoutRequest{}, ErrWaffoPancakeCheckoutBindingMismatch
	}
	var request WaffoPancakeCheckoutRequest
	if common.Unmarshal([]byte(raw), &request) != nil || validateWaffoPancakeCheckoutRequest(&request) != nil {
		return WaffoPancakeCheckoutRequest{}, ErrWaffoPancakeCheckoutBindingMismatch
	}
	return request, nil
}

func BindWaffoPancakeTopUpCheckout(tradeNo string, session *WaffoPancakeCheckoutSession) error {
	expiresAt, err := ValidateWaffoPancakeCheckoutSession(session)
	if err != nil {
		return ErrWaffoPancakeCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.TopUp
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if err := validateWaffoPancakeTopUpSnapshot(&order); err != nil {
			return err
		}
		if order.ProviderSessionId != nil {
			if *order.ProviderSessionId == session.SessionID && order.ProviderExpiresAt == expiresAt {
				return nil
			}
			return ErrWaffoPancakeCheckoutBindingMismatch
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND provider_session_id IS NULL", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"provider_session_id": session.SessionID, "provider_expires_at": expiresAt,
				"reconciliation_state": WaffoPancakePending, "reconciliation_detail": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWaffoPancakeCheckoutBindingMismatch
		}
		return nil
	})
}

func BindWaffoPancakeSubscriptionCheckout(tradeNo string, session *WaffoPancakeCheckoutSession) error {
	expiresAt, err := ValidateWaffoPancakeCheckoutSession(session)
	if err != nil {
		return ErrWaffoPancakeCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.SubscriptionOrder
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if err := validateWaffoPancakeSubscriptionSnapshot(&order); err != nil {
			return err
		}
		if order.ProviderSessionId != nil {
			if *order.ProviderSessionId == session.SessionID && order.ProviderExpiresAt == expiresAt {
				return nil
			}
			return ErrWaffoPancakeCheckoutBindingMismatch
		}
		if order.Status != TopUpStatusPending || !order.CapacityReserved {
			return ErrSubscriptionOrderStatusInvalid
		}
		result := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ? AND capacity_reserved = ? AND provider_session_id IS NULL", order.Id, TopUpStatusPending, true).
			Updates(map[string]any{
				"provider_session_id": session.SessionID, "provider_expires_at": expiresAt,
				"reconciliation_state": WaffoPancakePending, "reconciliation_detail": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWaffoPancakeCheckoutBindingMismatch
		}
		return nil
	})
}

func CompleteBoundWaffoPancakeTopUpOrder(settlement WaffoPancakeSettlement) error {
	if err := normalizeWaffoPancakeSettlement(&settlement); err != nil {
		return err
	}
	credited := false
	auditEventID := ""
	var order model.TopUp
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		completeTime, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", settlement.TradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if err := validateWaffoPancakeTopUpSettlementTx(&order, &settlement); err != nil {
			return err
		}
		if order.Status == TopUpStatusSuccess {
			return nil
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "quota").First(&user, order.UserId).Error; err != nil {
			return err
		}
		newQuota, ok := common.AddQuotaWithinBounds(user.Quota, int(order.CreditQuota))
		if !ok {
			return ErrTopUpQuotaOverflow
		}
		transition := tx.Model(&model.TopUp{}).Where("id = ? AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status": TopUpStatusSuccess, "complete_time": completeTime,
				"reconciliation_state": "", "reconciliation_detail": "", "reconciliation_next_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrTopUpStatusInvalid
		}
		credit := tx.Model(&model.User{}).Where("id = ? AND quota = ?", user.Id, user.Quota).UpdateColumn("quota", newQuota)
		if credit.Error != nil {
			return credit.Error
		}
		if credit.RowsAffected != 1 {
			return fmt.Errorf("credit Waffo Pancake top-up user %d: %w", user.Id, gorm.ErrRecordNotFound)
		}
		var auditErr error
		auditEventID, auditErr = enqueueTopupLogTx(tx, user.Id, int(order.CreditQuota), order.TradeNo, completeTime)
		if auditErr != nil {
			return fmt.Errorf("persist Waffo Pancake top-up audit: %w", auditErr)
		}
		credited = true
		return nil
	})
	if err != nil {
		return err
	}
	if credited {
		if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
			common.SysError(fmt.Sprintf("Waffo Pancake audit delivery deferred trade_no=%s user_id=%d: %v", order.TradeNo, order.UserId, err))
		}
	}
	return nil
}

func CompleteBoundWaffoPancakeSubscriptionOrder(tradeNo, providerPayload string, settlement WaffoPancakeSettlement) error {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" || tradeNo != settlement.TradeNo || len(providerPayload) == 0 || len(providerPayload) > maxWaffoPancakePayloadLength {
		return ErrWaffoPancakePaymentMismatch
	}
	if err := normalizeWaffoPancakeSettlement(&settlement); err != nil {
		return err
	}
	return completeSubscriptionOrder(tradeNo, providerPayload, PaymentProviderWaffoPancake, "", nil, nil, nil, nil, &settlement)
}

func normalizeWaffoPancakeSettlement(settlement *WaffoPancakeSettlement) error {
	if settlement == nil || !validWaffoPancakeText(settlement.EventID, 255, false) ||
		!setting.ValidWaffoPancakeShortID(settlement.ProviderOrderID, "ORD") ||
		!validWaffoPancakeTradeNo(settlement.TradeNo, strings.HasPrefix(settlement.TradeNo, "WAFFO_PANCAKE_SUB-")) ||
		!setting.ValidWaffoPancakeShortID(settlement.StoreID, "STO") ||
		(settlement.Mode != WaffoPancakeModeTest && settlement.Mode != WaffoPancakeModeProd) ||
		!validWaffoPancakeText(settlement.BuyerIdentity, 128, false) ||
		!validWaffoPancakeText(settlement.ProductName, 255, false) || settlement.Currency != "USD" {
		return ErrWaffoPancakePaymentMismatch
	}
	if _, _, _, err := normalizeWaffoPancakeOrderMoney(settlement.Amount); err != nil {
		return ErrWaffoPancakePaymentMismatch
	}
	return nil
}

func validateWaffoPancakeTopUpSettlementTx(order *model.TopUp, settlement *WaffoPancakeSettlement) error {
	if err := validateWaffoPancakeTopUpSnapshot(order); err != nil {
		return err
	}
	snapshot, _ := parseWaffoPancakeSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	_, normalized, minor, _ := normalizeWaffoPancakeOrderMoney(settlement.Amount)
	if order.TradeNo != settlement.TradeNo || order.ProviderAmountMinor != minor || order.ProviderCurrency != settlement.Currency ||
		snapshot.OrderMerchantExternalID != settlement.TradeNo || snapshot.PriceSnapshot.Amount != normalized ||
		snapshot.BuyerIdentity != settlement.BuyerIdentity || snapshot.StoreID != settlement.StoreID {
		return ErrWaffoPancakePaymentMismatch
	}
	return nil
}

func validateWaffoPancakeSubscriptionSettlementTx(order *model.SubscriptionOrder, settlement *WaffoPancakeSettlement) error {
	if err := validateWaffoPancakeSubscriptionSnapshot(order); err != nil {
		return err
	}
	snapshot, _ := parseWaffoPancakeSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	_, normalized, minor, _ := normalizeWaffoPancakeOrderMoney(settlement.Amount)
	if order.TradeNo != settlement.TradeNo || order.ProviderAmountMinor != minor || order.ProviderCurrency != settlement.Currency ||
		snapshot.OrderMerchantExternalID != settlement.TradeNo || snapshot.PriceSnapshot.Amount != normalized ||
		snapshot.BuyerIdentity != settlement.BuyerIdentity || snapshot.ProductID != order.ProviderPriceId ||
		snapshot.StoreID != settlement.StoreID ||
		(order.Status == TopUpStatusPending && !order.CapacityReserved) {
		return ErrWaffoPancakePaymentMismatch
	}
	return nil
}

func validateWaffoPancakeTopUpSnapshot(order *model.TopUp) error {
	if order == nil || order.PaymentProvider != PaymentProviderWaffoPancake || order.PaymentMethod != PaymentMethodWaffoPancake ||
		order.ProviderBindingVersion != WaffoPancakeBindingVersion || order.ProviderOrderType != WaffoPancakeOrderWallet ||
		order.ProviderMode != WaffoPancakeCheckoutMode || order.ProviderAmountMinor <= 0 || order.ProviderCurrency != "USD" ||
		order.CreditQuotaVersion != WaffoPancakeCreditVersion || order.CreditQuota <= 0 || order.CreditQuota > common.MaxQuota ||
		order.ProviderCreateIdempotencyKey != order.TradeNo {
		return ErrWaffoPancakeLegacyOrderReview
	}
	expectedCredit, err := topUpCreditQuota(order.Amount)
	if err != nil || expectedCredit != order.CreditQuota {
		return ErrWaffoPancakeLegacyOrderReview
	}
	snapshot, err := parseWaffoPancakeSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	if err != nil {
		return err
	}
	_, normalized, minor, err := normalizeWaffoPancakeOrderMoney(snapshot.PriceSnapshot.Amount)
	if err != nil || minor != order.ProviderAmountMinor || snapshot.Currency != "USD" ||
		snapshot.OrderMerchantExternalID != order.TradeNo || snapshot.BuyerIdentity != WaffoPancakeBuyerIdentityFromUserID(order.UserId) ||
		snapshot.PriceSnapshot.Amount != normalized || !setting.ValidWaffoPancakeShortID(snapshot.ProductID, "PROD") {
		return ErrWaffoPancakeCheckoutBindingMismatch
	}
	return nil
}

func validateWaffoPancakeSubscriptionSnapshot(order *model.SubscriptionOrder) error {
	if order == nil || order.PaymentProvider != PaymentProviderWaffoPancake || order.PaymentMethod != PaymentMethodWaffoPancake ||
		order.ProviderBindingVersion != WaffoPancakeBindingVersion || order.ProviderOrderType != WaffoPancakeOrderSubscribe ||
		order.ProviderMode != WaffoPancakeCheckoutMode || order.ProviderAmountMinor <= 0 || order.ProviderCurrency != "USD" ||
		!setting.ValidWaffoPancakeShortID(order.ProviderPriceId, "PROD") || strings.TrimSpace(order.EntitlementSnapshot) == "" ||
		order.ProviderCreateIdempotencyKey != order.TradeNo {
		return ErrWaffoPancakeLegacyOrderReview
	}
	snapshot, err := parseWaffoPancakeSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	if err != nil {
		return err
	}
	_, normalized, minor, err := normalizeWaffoPancakeOrderMoney(snapshot.PriceSnapshot.Amount)
	if err != nil || minor != order.ProviderAmountMinor || snapshot.Currency != "USD" || snapshot.ProductID != order.ProviderPriceId ||
		snapshot.OrderMerchantExternalID != order.TradeNo || snapshot.BuyerIdentity != WaffoPancakeBuyerIdentityFromUserID(order.UserId) ||
		snapshot.PriceSnapshot.Amount != normalized {
		return ErrWaffoPancakeCheckoutBindingMismatch
	}
	if _, err := planFromSubscriptionSnapshot(order.EntitlementSnapshot, order.PlanId); err != nil {
		return ErrWaffoPancakeCheckoutBindingMismatch
	}
	return nil
}

func transientWaffoPancakeConfig(merchantID, privateKey string) (setting.WaffoPancakeConfig, error) {
	merchantID = strings.TrimSpace(merchantID)
	privateKey = strings.TrimSpace(privateKey)
	config, err := setting.NewWaffoPancakeCredentialConfig(merchantID, privateKey)
	if err != nil || config.MerchantID == "" || config.PrivateKey == "" {
		return setting.WaffoPancakeConfig{}, ErrWaffoPancakeCheckoutBindingMismatch
	}
	return config, nil
}

// SaveWaffoPancakeConfig validates and atomically publishes the operator's
// complete gateway binding. A blank private key preserves the current secret.
func SaveWaffoPancakeConfig(merchantID, privateKey, returnURL, storeID, productID string) error {
	return SaveWaffoPancakeConfigWithPricing(merchantID, privateKey, returnURL, storeID, productID, nil, nil)
}

// SaveWaffoPancakeConfigWithPricing extends the compatibility save contract
// with optional pricing fields used by TokenRouter's administrator workflow.
// When supplied, all seven values commit together; older clients that omit
// them retain the existing pricing configuration.
func SaveWaffoPancakeConfigWithPricing(merchantID, privateKey, returnURL, storeID, productID string, unitPrice, minTopUp *string) error {
	merchantID = strings.TrimSpace(merchantID)
	privateKey = strings.TrimSpace(privateKey)
	returnURL = strings.TrimSpace(returnURL)
	storeID = strings.TrimSpace(storeID)
	productID = strings.TrimSpace(productID)
	if merchantID == "" || storeID == "" || productID == "" {
		return ErrWaffoPancakeCheckoutBindingMismatch
	}
	updates := map[string]string{
		setting.WaffoPancakeMerchantIDOption: merchantID,
		setting.WaffoPancakeReturnURLOption:  returnURL,
		setting.WaffoPancakeStoreIDOption:    storeID,
		setting.WaffoPancakeProductIDOption:  productID,
	}
	if privateKey != "" {
		updates[setting.WaffoPancakePrivateKeyOption] = privateKey
	} else {
		storedMerchantID := strings.TrimSpace(setting.GetOption(setting.WaffoPancakeMerchantIDOption))
		if strings.TrimSpace(setting.GetOption(setting.WaffoPancakePrivateKeyOption)) == "" || merchantID != storedMerchantID {
			return ErrWaffoPancakeCheckoutBindingMismatch
		}
	}
	if unitPrice != nil {
		updates[setting.WaffoPancakeUnitPriceOption] = *unitPrice
	}
	if minTopUp != nil {
		updates[setting.WaffoPancakeMinTopUpOption] = *minTopUp
	}
	if err := setting.UpdateOptions(updates); err != nil {
		return fmt.Errorf("persist Waffo Pancake config: %w", err)
	}
	return nil
}

func CreateWaffoPancakePrimaryPair(ctx context.Context, merchantID, privateKey, returnURL string) (*WaffoPancakePairResult, error) {
	config, err := transientWaffoPancakeConfig(merchantID, privateKey)
	if err != nil {
		return nil, err
	}
	returnURL = strings.TrimSpace(returnURL)
	if returnURL != "" && !setting.ValidWaffoCallbackURL(returnURL) {
		return nil, ErrWaffoPancakeCheckoutBindingMismatch
	}
	gateway := currentWaffoPancakeGateway()
	storeID, err := gateway.CreateStore(ctx, config, defaultWaffoPancakeStoreName)
	if err != nil {
		return nil, err
	}
	result := &WaffoPancakePairResult{StoreID: storeID, StoreName: defaultWaffoPancakeStoreName}
	productID, err := gateway.CreateProduct(ctx, config, storeID, defaultWaffoPancakeProductName, "1.00", returnURL)
	if err != nil {
		result.OrphanStore = true
		return result, err
	}
	result.ProductID = productID
	result.ProductName = defaultWaffoPancakeProductName
	if err := gateway.PublishProduct(ctx, config, productID); err != nil {
		result.OrphanStore = true
		return result, err
	}
	return result, nil
}

func CreateWaffoPancakeProductForPlan(ctx context.Context, merchantID, privateKey, storeID, name, amount, returnURL string) (string, error) {
	config, err := transientWaffoPancakeConfig(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	storeID = strings.TrimSpace(storeID)
	name = strings.TrimSpace(name)
	amount = strings.TrimSpace(amount)
	returnURL = strings.TrimSpace(returnURL)
	if !setting.ValidWaffoPancakeShortID(storeID, "STO") || !validWaffoPancakeText(name, 255, false) ||
		(returnURL != "" && !setting.ValidWaffoCallbackURL(returnURL)) {
		return "", ErrWaffoPancakeCheckoutBindingMismatch
	}
	price, err := ParseSubscriptionPlanPrice(amount)
	if err != nil || price < 0.01 {
		return "", ErrWaffoPancakePaymentMismatch
	}
	priceDecimal, err := decimal.NewFromString(amount)
	if err != nil || priceDecimal.Sign() <= 0 {
		return "", ErrWaffoPancakePaymentMismatch
	}
	normalizedAmount := priceDecimal.String()
	gateway := currentWaffoPancakeGateway()
	productID, err := gateway.CreateProduct(ctx, config, storeID, name, normalizedAmount, returnURL)
	if err != nil {
		return "", err
	}
	if err := gateway.PublishProduct(ctx, config, productID); err != nil {
		return "", err
	}
	return productID, nil
}

func ListWaffoPancakeCatalog(ctx context.Context, merchantID, privateKey string) (*WaffoPancakeCatalog, error) {
	config, err := transientWaffoPancakeConfig(merchantID, privateKey)
	if err != nil {
		return nil, err
	}
	return currentWaffoPancakeGateway().ListCatalog(ctx, config)
}
