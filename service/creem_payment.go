package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
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
	PaymentProviderCreem = "creem"
	PaymentMethodCreem   = "creem"

	CreemCheckoutBindingVersion    = 1
	CreemTopUpCreditQuotaVersion   = 2
	CreemOrderTypeWallet           = "wallet"
	CreemOrderTypeSubscription     = "subscription"
	CreemCheckoutMode              = "checkout"
	CreemReconciliationCreate      = "checkout_creation_uncertain"
	CreemReconciliationPending     = "checkout_pending"
	maxCreemCheckoutSnapshotLength = 64 << 10
	maxCreemProviderPayloadLength  = 64 << 10
)

var (
	ErrCreemPaymentMismatch           = errors.New("Creem 支付金额、币种或产品不匹配")
	ErrCreemCheckoutBindingMismatch   = errors.New("Creem Checkout 订单绑定不匹配")
	ErrCreemLegacyOrderRequiresReview = errors.New("Creem 旧订单缺少安全绑定，需要人工核对")
)

// CreemProductEconomics is the validated product definition captured before
// checkout. PriceText is kept decimal so provider minor units never depend on
// binary floating-point rounding.
type CreemProductEconomics struct {
	ProductID string
	Name      string
	PriceText string
	Currency  string
	Quota     int64
}

type CreemCheckoutCustomer struct {
	Email string `json:"email"`
}

// CreemCheckoutRequest is the exact credential-free request body persisted on
// the local order before the provider is contacted.
type CreemCheckoutRequest struct {
	ProductID string                `json:"product_id"`
	RequestID string                `json:"request_id"`
	Customer  CreemCheckoutCustomer `json:"customer"`
	Metadata  map[string]string     `json:"metadata,omitempty"`
}

// CreemSettlement contains only fields authenticated by the raw-body webhook
// signature. The transaction compares every economic and checkout binding to
// the immutable local order before it can create value.
type CreemSettlement struct {
	CheckoutID        string
	ProviderOrderID   string
	ReferenceID       string
	ProductID         string
	AmountPaid        int64
	Currency          string
	OrderType         string
	MetadataReference string
	MetadataQuota     string
}

func creemMoney(priceText, currency string) (float64, int64, string, error) {
	if priceText == "" || priceText != strings.TrimSpace(priceText) {
		return 0, 0, "", ErrCreemPaymentMismatch
	}
	currency, err := NormalizeStripeCurrency(strings.TrimSpace(currency))
	if err != nil || !StripeCurrencySupported(currency) {
		return 0, 0, "", ErrCreemPaymentMismatch
	}
	minor, err := StripeMoneyToMinorUnitsForCurrency(priceText, currency)
	if err != nil || minor <= 0 {
		return 0, 0, "", ErrCreemPaymentMismatch
	}
	parsed, err := decimal.NewFromString(priceText)
	if err != nil || parsed.Sign() <= 0 || parsed.GreaterThan(decimal.NewFromFloat(setting.MaxPaymentProviderAmount)) {
		return 0, 0, "", ErrCreemPaymentMismatch
	}
	money := parsed.InexactFloat64()
	if money <= 0 || math.IsNaN(money) || math.IsInf(money, 0) {
		return 0, 0, "", ErrCreemPaymentMismatch
	}
	return money, minor, currency, nil
}

func creemCheckoutRequest(tradeNo, productID, productName, email, username string, quota int64) (CreemCheckoutRequest, string, string, error) {
	if !validCreemIdentifier(tradeNo) || !validCreemIdentifier(productID) ||
		productName == "" || productName != strings.TrimSpace(productName) || len(productName) > 255 ||
		len(email) > 320 || strings.ContainsRune(email, '\x00') || len(username) > 255 || strings.ContainsRune(username, '\x00') ||
		quota < 0 || quota > common.MaxQuota {
		return CreemCheckoutRequest{}, "", "", ErrCreemCheckoutBindingMismatch
	}
	request := CreemCheckoutRequest{
		ProductID: productID,
		RequestID: tradeNo,
		Customer:  CreemCheckoutCustomer{Email: email},
		Metadata: map[string]string{
			"username":     username,
			"reference_id": tradeNo,
			"product_name": productName,
			"quota":        strconv.FormatInt(quota, 10),
		},
	}
	encoded, err := common.Marshal(request)
	if err != nil || len(encoded) == 0 || len(encoded) > maxCreemCheckoutSnapshotLength {
		return CreemCheckoutRequest{}, "", "", ErrCreemCheckoutBindingMismatch
	}
	sum := sha256.Sum256(encoded)
	return request, string(encoded), hex.EncodeToString(sum[:]), nil
}

func parseCreemCheckoutSnapshot(raw, fingerprint string) (CreemCheckoutRequest, error) {
	if raw == "" || len(raw) > maxCreemCheckoutSnapshotLength || len(fingerprint) != sha256.Size*2 {
		return CreemCheckoutRequest{}, ErrCreemCheckoutBindingMismatch
	}
	sum := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(sum[:]) != fingerprint {
		return CreemCheckoutRequest{}, ErrCreemCheckoutBindingMismatch
	}
	var snapshot CreemCheckoutRequest
	if common.Unmarshal([]byte(raw), &snapshot) != nil {
		return CreemCheckoutRequest{}, ErrCreemCheckoutBindingMismatch
	}
	canonical, err := common.Marshal(snapshot)
	if err != nil || string(canonical) != raw {
		return CreemCheckoutRequest{}, ErrCreemCheckoutBindingMismatch
	}
	return snapshot, nil
}

// CreateBoundCreemTopUpWithTradeNo idempotently creates a direct-quota Creem
// wallet order and its immutable provider request/economic snapshots before
// any network call.
func CreateBoundCreemTopUpWithTradeNo(userID int, product CreemProductEconomics, tradeNo string) (*model.TopUp, CreemCheckoutRequest, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if userID <= 0 || !validCreemIdentifier(tradeNo) || !strings.HasPrefix(tradeNo, "ref_") || strings.HasPrefix(tradeNo, "sub_ref_") ||
		product.Quota <= 0 || product.Quota > common.MaxQuota {
		return nil, CreemCheckoutRequest{}, ErrTopUpAmountMismatch
	}
	money, amountMinor, currency, err := creemMoney(product.PriceText, product.Currency)
	if err != nil {
		return nil, CreemCheckoutRequest{}, err
	}

	var created model.TopUp
	var request CreemCheckoutRequest
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "email", "username").First(&user, userID).Error; err != nil {
			return err
		}
		var checkoutRequest, checkoutFingerprint string
		request, checkoutRequest, checkoutFingerprint, err = creemCheckoutRequest(
			tradeNo, product.ProductID, product.Name, user.Email, user.Username, product.Quota)
		if err != nil {
			return err
		}
		var existing model.TopUp
		lookup := subscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&existing)
		if lookup.Error == nil {
			if creemTopUpCreationMatches(&existing, userID, product.Quota, money, amountMinor, currency,
				checkoutRequest, checkoutFingerprint) {
				created = existing
				return nil
			}
			return ErrCreemCheckoutBindingMismatch
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		created = model.TopUp{
			UserId:                       userID,
			Amount:                       product.Quota,
			CreditQuota:                  product.Quota,
			CreditQuotaVersion:           CreemTopUpCreditQuotaVersion,
			Money:                        money,
			TradeNo:                      tradeNo,
			PaymentMethod:                PaymentMethodCreem,
			PaymentProvider:              PaymentProviderCreem,
			ProviderAmountMinor:          amountMinor,
			ProviderCurrency:             currency,
			ProviderBindingVersion:       CreemCheckoutBindingVersion,
			ProviderOrderType:            CreemOrderTypeWallet,
			ProviderMode:                 CreemCheckoutMode,
			ReconciliationState:          CreemReconciliationCreate,
			ReconciliationDetail:         "checkout_not_yet_bound",
			CheckoutRequest:              checkoutRequest,
			CheckoutFingerprint:          checkoutFingerprint,
			ProviderCreateIdempotencyKey: tradeNo,
			CreateTime:                   now,
			Status:                       TopUpStatusPending,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, CreemCheckoutRequest{}, err
	}
	return &created, request, nil
}

func creemTopUpCreationMatches(order *model.TopUp, userID int, quota int64, money float64, amountMinor int64, currency, request, fingerprint string) bool {
	return order != nil && order.UserId == userID && order.Amount == quota && order.CreditQuota == quota &&
		order.CreditQuotaVersion == CreemTopUpCreditQuotaVersion && order.Money == money &&
		order.PaymentMethod == PaymentMethodCreem && order.PaymentProvider == PaymentProviderCreem &&
		order.ProviderAmountMinor == amountMinor && order.ProviderCurrency == currency &&
		order.ProviderBindingVersion == CreemCheckoutBindingVersion && order.ProviderOrderType == CreemOrderTypeWallet &&
		order.ProviderMode == CreemCheckoutMode && order.CheckoutRequest == request &&
		order.CheckoutFingerprint == fingerprint && order.ProviderCreateIdempotencyKey == order.TradeNo
}

// CreateBoundCreemSubscriptionOrder reserves the purchase-cap slot and stores
// the product, amount, currency, entitlement and exact checkout request in one
// transaction before the Creem API is contacted.
func CreateBoundCreemSubscriptionOrder(userID, planID int, tradeNo string) (*model.SubscriptionOrder, CreemCheckoutRequest, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if userID <= 0 || planID <= 0 || !validCreemIdentifier(tradeNo) || !strings.HasPrefix(tradeNo, "sub_ref_") {
		return nil, CreemCheckoutRequest{}, ErrSubscriptionOrderDataInvalid
	}

	var created model.SubscriptionOrder
	var request CreemCheckoutRequest
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		var user model.User
		if err := subscriptionLockForUpdate(tx).Select("id", "email", "username").First(&user, userID).Error; err != nil {
			return err
		}
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, planID).Error; err != nil {
			return err
		}
		NormalizeSubscriptionPlanDefaults(&plan)
		productID := strings.TrimSpace(plan.CreemProductId)
		if !plan.Enabled || productID != plan.CreemProductId || !validCreemIdentifier(productID) ||
			plan.MaxPurchasePerUser < 0 {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, ok := boundedSubscriptionQuota(plan.TotalAmount); !ok {
			return ErrSubscriptionOrderDataInvalid
		}
		if _, err := calcPlanEndTime(time.Unix(now, 0), &plan); err != nil {
			return ErrSubscriptionOrderDataInvalid
		}
		money, amountMinor, currency, err := creemMoney(strings.TrimSpace(plan.PriceAmount), plan.Currency)
		if err != nil || !validSubscriptionOrderMoney(money) {
			return ErrSubscriptionOrderDataInvalid
		}
		var checkoutRequest, checkoutFingerprint string
		request, checkoutRequest, checkoutFingerprint, err = creemCheckoutRequest(
			tradeNo, productID, plan.Title, user.Email, user.Username, 0)
		if err != nil {
			return err
		}
		snapshotJSON, err := common.Marshal(subscriptionEntitlementSnapshotFromPlan(&plan))
		if err != nil {
			return err
		}
		var existing model.SubscriptionOrder
		lookup := subscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&existing)
		if lookup.Error == nil {
			if creemSubscriptionCreationMatches(&existing, userID, planID, money, amountMinor, currency, productID,
				string(snapshotJSON), checkoutRequest, checkoutFingerprint) {
				created = existing
				return nil
			}
			return ErrCreemCheckoutBindingMismatch
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
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
		created = model.SubscriptionOrder{
			UserId:                       userID,
			PlanId:                       plan.Id,
			Money:                        money,
			TradeNo:                      tradeNo,
			PaymentMethod:                PaymentMethodCreem,
			PaymentProvider:              PaymentProviderCreem,
			ProviderAmountMinor:          amountMinor,
			ProviderCurrency:             currency,
			ProviderBindingVersion:       CreemCheckoutBindingVersion,
			ProviderOrderType:            CreemOrderTypeSubscription,
			ProviderMode:                 CreemCheckoutMode,
			ProviderPriceId:              productID,
			EntitlementSnapshot:          string(snapshotJSON),
			CapacityReserved:             true,
			ReconciliationState:          CreemReconciliationCreate,
			ReconciliationDetail:         "checkout_not_yet_bound",
			CheckoutRequest:              checkoutRequest,
			CheckoutFingerprint:          checkoutFingerprint,
			ProviderCreateIdempotencyKey: tradeNo,
			CreateTime:                   now,
			Status:                       TopUpStatusPending,
		}
		return tx.Create(&created).Error
	})
	if err != nil {
		return nil, CreemCheckoutRequest{}, err
	}
	return &created, request, nil
}

func creemSubscriptionCreationMatches(order *model.SubscriptionOrder, userID, planID int, money float64, amountMinor int64, currency, productID, entitlement, request, fingerprint string) bool {
	return order != nil && order.UserId == userID && order.PlanId == planID && order.Money == money &&
		order.PaymentMethod == PaymentMethodCreem && order.PaymentProvider == PaymentProviderCreem &&
		order.ProviderAmountMinor == amountMinor && order.ProviderCurrency == currency &&
		order.ProviderBindingVersion == CreemCheckoutBindingVersion && order.ProviderOrderType == CreemOrderTypeSubscription &&
		order.ProviderMode == CreemCheckoutMode && order.ProviderPriceId == productID &&
		order.EntitlementSnapshot == entitlement && order.CheckoutRequest == request &&
		order.CheckoutFingerprint == fingerprint && order.ProviderCreateIdempotencyKey == order.TradeNo
}

func validCreemIdentifier(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 255 {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// ValidCreemCheckoutID applies the bounded identifier contract used for both
// checkout creation responses and signed webhook settlement.
func ValidCreemCheckoutID(value string) bool {
	return validCreemIdentifier(value)
}

// BindCreemTopUpCheckout stores the validated provider checkout identifier
// before its URL is returned. A different identifier can never replace it.
func BindCreemTopUpCheckout(tradeNo, checkoutID string) error {
	if !validCreemIdentifier(checkoutID) {
		return ErrCreemCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.TopUp
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if err := validateCreemTopUpSnapshot(&order); err != nil {
			return err
		}
		if order.ProviderSessionId != nil {
			if *order.ProviderSessionId == checkoutID {
				return nil
			}
			return ErrCreemCheckoutBindingMismatch
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND provider_session_id IS NULL", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"provider_session_id":   checkoutID,
				"reconciliation_state":  CreemReconciliationPending,
				"reconciliation_detail": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCreemCheckoutBindingMismatch
		}
		return nil
	})
}

// BindCreemSubscriptionCheckout is the subscription counterpart of
// BindCreemTopUpCheckout and preserves the purchase-cap reservation.
func BindCreemSubscriptionCheckout(tradeNo, checkoutID string) error {
	if !validCreemIdentifier(checkoutID) {
		return ErrCreemCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.SubscriptionOrder
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if err := validateCreemSubscriptionSnapshot(&order); err != nil {
			return err
		}
		if order.ProviderSessionId != nil {
			if *order.ProviderSessionId == checkoutID {
				return nil
			}
			return ErrCreemCheckoutBindingMismatch
		}
		if order.Status != TopUpStatusPending || !order.CapacityReserved {
			return ErrSubscriptionOrderStatusInvalid
		}
		result := tx.Model(&model.SubscriptionOrder{}).
			Where("id = ? AND status = ? AND capacity_reserved = ? AND provider_session_id IS NULL",
				order.Id, TopUpStatusPending, true).
			Updates(map[string]any{
				"provider_session_id":   checkoutID,
				"reconciliation_state":  CreemReconciliationPending,
				"reconciliation_detail": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCreemCheckoutBindingMismatch
		}
		return nil
	})
}

func validateCreemTopUpSnapshot(order *model.TopUp) error {
	if order == nil || order.PaymentProvider != PaymentProviderCreem || order.PaymentMethod != PaymentMethodCreem ||
		order.ProviderBindingVersion != CreemCheckoutBindingVersion || order.ProviderOrderType != CreemOrderTypeWallet ||
		order.ProviderMode != CreemCheckoutMode || order.ProviderAmountMinor <= 0 || order.ProviderCurrency == "" ||
		order.CreditQuotaVersion != CreemTopUpCreditQuotaVersion || order.CreditQuota <= 0 ||
		order.CreditQuota > common.MaxQuota || order.Amount != order.CreditQuota ||
		order.ProviderCreateIdempotencyKey != order.TradeNo {
		return ErrCreemLegacyOrderRequiresReview
	}
	snapshot, err := parseCreemCheckoutSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	if err != nil || snapshot.RequestID != order.TradeNo || snapshot.ProductID == "" ||
		snapshot.Metadata["reference_id"] != order.TradeNo || snapshot.Metadata["quota"] != strconv.FormatInt(order.CreditQuota, 10) {
		return ErrCreemCheckoutBindingMismatch
	}
	return nil
}

func validateCreemSubscriptionSnapshot(order *model.SubscriptionOrder) error {
	if order == nil || order.PaymentProvider != PaymentProviderCreem || order.PaymentMethod != PaymentMethodCreem ||
		order.ProviderBindingVersion != CreemCheckoutBindingVersion || order.ProviderOrderType != CreemOrderTypeSubscription ||
		order.ProviderMode != CreemCheckoutMode || order.ProviderAmountMinor <= 0 || order.ProviderCurrency == "" ||
		strings.TrimSpace(order.ProviderPriceId) == "" || strings.TrimSpace(order.EntitlementSnapshot) == "" ||
		order.ProviderCreateIdempotencyKey != order.TradeNo {
		return ErrCreemLegacyOrderRequiresReview
	}
	if order.Status == TopUpStatusPending && !order.CapacityReserved {
		return ErrCreemCheckoutBindingMismatch
	}
	snapshot, err := parseCreemCheckoutSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	if err != nil || snapshot.RequestID != order.TradeNo || snapshot.ProductID != order.ProviderPriceId ||
		snapshot.Metadata["reference_id"] != order.TradeNo || snapshot.Metadata["quota"] != "0" {
		return ErrCreemCheckoutBindingMismatch
	}
	return nil
}

func normalizeCreemSettlement(settlement *CreemSettlement) error {
	if settlement == nil || !validCreemIdentifier(settlement.CheckoutID) || !validCreemIdentifier(settlement.ProviderOrderID) ||
		!validCreemIdentifier(settlement.ReferenceID) || !validCreemIdentifier(settlement.ProductID) ||
		settlement.AmountPaid <= 0 || settlement.OrderType == "" || settlement.OrderType != strings.TrimSpace(settlement.OrderType) || len(settlement.OrderType) > 32 ||
		settlement.MetadataReference != settlement.ReferenceID || len(settlement.MetadataQuota) > 32 {
		return ErrCreemPaymentMismatch
	}
	currency, err := NormalizeStripeCurrency(strings.TrimSpace(settlement.Currency))
	if err != nil {
		return ErrCreemPaymentMismatch
	}
	settlement.Currency = currency
	return nil
}

func validateCreemTopUpSettlementTx(tx *gorm.DB, order *model.TopUp, settlement *CreemSettlement) error {
	if err := normalizeCreemSettlement(settlement); err != nil {
		return err
	}
	if err := validateCreemTopUpSnapshot(order); err != nil {
		return err
	}
	snapshot, _ := parseCreemCheckoutSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	if order.TradeNo != settlement.ReferenceID || snapshot.ProductID != settlement.ProductID ||
		order.ProviderAmountMinor != settlement.AmountPaid || order.ProviderCurrency != settlement.Currency ||
		settlement.OrderType != "onetime" || settlement.MetadataQuota != strconv.FormatInt(order.CreditQuota, 10) {
		return ErrCreemPaymentMismatch
	}
	return claimOrValidateCreemTopUpCheckoutTx(tx, order, settlement.CheckoutID)
}

func validateCreemSubscriptionSettlementTx(tx *gorm.DB, order *model.SubscriptionOrder, settlement *CreemSettlement) error {
	if err := normalizeCreemSettlement(settlement); err != nil {
		return err
	}
	if err := validateCreemSubscriptionSnapshot(order); err != nil {
		return err
	}
	if order.TradeNo != settlement.ReferenceID || order.ProviderPriceId != settlement.ProductID ||
		order.ProviderAmountMinor != settlement.AmountPaid || order.ProviderCurrency != settlement.Currency ||
		settlement.MetadataQuota != "0" {
		return ErrCreemPaymentMismatch
	}
	return claimOrValidateCreemSubscriptionCheckoutTx(tx, order, settlement.CheckoutID)
}

// A checkout creation timeout is ambiguous: Creem may have accepted the
// immutable request_id even though TokenRouter never received the response.
// In that state only a correctly signed webhook whose full economics already
// match the local snapshot may atomically claim the missing checkout ID.
func claimOrValidateCreemTopUpCheckoutTx(tx *gorm.DB, order *model.TopUp, checkoutID string) error {
	if tx == nil || order == nil || !validCreemIdentifier(checkoutID) {
		return ErrCreemCheckoutBindingMismatch
	}
	if order.ProviderSessionId != nil {
		if *order.ProviderSessionId == checkoutID {
			return nil
		}
		return ErrCreemCheckoutBindingMismatch
	}
	if order.Status != TopUpStatusPending || order.ReconciliationState != CreemReconciliationCreate {
		return ErrCreemCheckoutBindingMismatch
	}
	result := tx.Model(&model.TopUp{}).
		Where("id = ? AND status = ? AND provider_session_id IS NULL AND reconciliation_state = ?",
			order.Id, TopUpStatusPending, CreemReconciliationCreate).
		Updates(map[string]any{
			"provider_session_id":   checkoutID,
			"reconciliation_state":  CreemReconciliationPending,
			"reconciliation_detail": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCreemCheckoutBindingMismatch
	}
	claimed := checkoutID
	order.ProviderSessionId = &claimed
	order.ReconciliationState = CreemReconciliationPending
	order.ReconciliationDetail = ""
	return nil
}

func claimOrValidateCreemSubscriptionCheckoutTx(tx *gorm.DB, order *model.SubscriptionOrder, checkoutID string) error {
	if tx == nil || order == nil || !validCreemIdentifier(checkoutID) {
		return ErrCreemCheckoutBindingMismatch
	}
	if order.ProviderSessionId != nil {
		if *order.ProviderSessionId == checkoutID {
			return nil
		}
		return ErrCreemCheckoutBindingMismatch
	}
	if order.Status != TopUpStatusPending || !order.CapacityReserved || order.ReconciliationState != CreemReconciliationCreate {
		return ErrCreemCheckoutBindingMismatch
	}
	result := tx.Model(&model.SubscriptionOrder{}).
		Where("id = ? AND status = ? AND capacity_reserved = ? AND provider_session_id IS NULL AND reconciliation_state = ?",
			order.Id, TopUpStatusPending, true, CreemReconciliationCreate).
		Updates(map[string]any{
			"provider_session_id":   checkoutID,
			"reconciliation_state":  CreemReconciliationPending,
			"reconciliation_detail": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCreemCheckoutBindingMismatch
	}
	claimed := checkoutID
	order.ProviderSessionId = &claimed
	order.ReconciliationState = CreemReconciliationPending
	order.ReconciliationDetail = ""
	return nil
}

// CompleteBoundCreemTopUpOrder credits the direct configured quota exactly
// once after all signed provider fields match the bound pending order.
func CompleteBoundCreemTopUpOrder(tradeNo string, settlement CreemSettlement) error {
	if err := normalizeCreemSettlement(&settlement); err != nil || strings.TrimSpace(tradeNo) != settlement.ReferenceID {
		return ErrCreemPaymentMismatch
	}
	var order model.TopUp
	if err := model.DB.Select("user_id", "amount").Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTopUpNotFound
		}
		return err
	}
	return completeTopUp(order.UserId, tradeNo, order.Amount, nil, nil, &settlement)
}

// CompleteBoundCreemSubscriptionOrder creates the immutable entitlement and
// financial mirror row atomically with the pending-to-success transition.
func CompleteBoundCreemSubscriptionOrder(tradeNo, providerPayload string, settlement CreemSettlement) error {
	if len(providerPayload) == 0 || len(providerPayload) > maxCreemProviderPayloadLength ||
		strings.TrimSpace(tradeNo) != settlement.ReferenceID {
		return ErrCreemPaymentMismatch
	}
	if err := normalizeCreemSettlement(&settlement); err != nil {
		return err
	}
	return completeSubscriptionOrder(tradeNo, providerPayload, PaymentProviderCreem, "", nil, nil, nil, &settlement, nil)
}
