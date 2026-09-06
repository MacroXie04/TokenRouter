package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	PaymentProviderWaffo = "waffo"
	PaymentMethodWaffo   = "waffo"

	WaffoCheckoutBindingVersion  = 1
	WaffoTopUpCreditQuotaVersion = 1
	WaffoOrderTypeWallet         = "wallet"
	WaffoProductOneTimePayment   = "ONE_TIME_PAYMENT"
	WaffoEnvironmentProduction   = "PRODUCTION"
	WaffoEnvironmentSandbox      = "SANDBOX"
	WaffoReconciliationCreate    = "checkout_creation_uncertain"
	WaffoReconciliationPending   = "checkout_pending"

	maxWaffoCheckoutSnapshotLength = 64 << 10
	maxWaffoIdentifierLength       = 255
	maxWaffoMoneyTextLength        = 64
)

var (
	ErrWaffoPaymentMismatch           = errors.New("Waffo 支付金额、币种或订单不匹配")
	ErrWaffoCheckoutBindingMismatch   = errors.New("Waffo Checkout 订单绑定不匹配")
	ErrWaffoLegacyOrderRequiresReview = errors.New("Waffo 旧订单缺少安全绑定，需要人工核对")
)

type WaffoMerchantInfo struct {
	MerchantID string `json:"merchantId,omitempty"`
}

type WaffoUserInfo struct {
	UserID       string `json:"userId,omitempty"`
	UserEmail    string `json:"userEmail,omitempty"`
	UserTerminal string `json:"userTerminal,omitempty"`
}

type WaffoPaymentInfo struct {
	ProductName   string `json:"productName,omitempty"`
	PayMethodType string `json:"payMethodType,omitempty"`
	PayMethodName string `json:"payMethodName,omitempty"`
}

type WaffoGoodsInfo struct {
	GoodsName string `json:"goodsName,omitempty"`
	AppName   string `json:"appName,omitempty"`
}

// WaffoCheckoutRequest is the exact credential-free JSON document persisted
// before a network request and signed by the checkout client.
type WaffoCheckoutRequest struct {
	PaymentRequestID   string            `json:"paymentRequestId"`
	MerchantOrderID    string            `json:"merchantOrderId"`
	OrderCurrency      string            `json:"orderCurrency"`
	OrderAmount        string            `json:"orderAmount"`
	OrderDescription   string            `json:"orderDescription"`
	OrderRequestedAt   string            `json:"orderRequestedAt,omitempty"`
	NotifyURL          string            `json:"notifyUrl"`
	MerchantInfo       WaffoMerchantInfo `json:"merchantInfo,omitempty"`
	UserInfo           WaffoUserInfo     `json:"userInfo"`
	PaymentInfo        WaffoPaymentInfo  `json:"paymentInfo"`
	GoodsInfo          WaffoGoodsInfo    `json:"goodsInfo,omitempty"`
	SuccessRedirectURL string            `json:"successRedirectUrl,omitempty"`
	FailedRedirectURL  string            `json:"failedRedirectUrl,omitempty"`
}

// WaffoCheckoutSpec contains validated caller-independent values used to
// create the immutable local order and its provider request snapshot.
type WaffoCheckoutSpec struct {
	UserID          int
	Amount          int64
	RequestedAmount int64
	OrderAmount     string
	Currency        string
	MerchantID      string
	NotifyURL       string
	ReturnURL       string
	AppName         string
	PayMethodType   string
	PayMethodName   string
	RequestedAt     string
	Sandbox         bool
}

// WaffoCreateBinding is returned by the authenticated provider create call.
type WaffoCreateBinding struct {
	PaymentRequestID string
	MerchantOrderID  string
	AcquiringOrderID string
}

// WaffoSettlement contains only fields authenticated by the raw webhook
// signature. Every field is compared with the immutable local snapshot before
// either a terminal failure or a credit can be committed.
type WaffoSettlement struct {
	PaymentRequestID string
	MerchantOrderID  string
	AcquiringOrderID string
	OrderAmount      string
	OrderCurrency    string
	MerchantID       string
	UserID           string
	ProductName      string
	Environment      string
}

func GetWaffoTopupMoney(amount int64, group string, unitPrice float64) (float64, error) {
	return calculateTopUpMoney(amount, group, unitPrice)
}

// FormatWaffoAmount applies the reference gateway's zero-decimal currency
// set; every other syntactically valid ISO-style currency uses two decimals.
func FormatWaffoAmount(amount float64, currency string) (string, error) {
	currency, err := NormalizeWaffoCurrency(currency)
	if err != nil || !finitePositiveMoney(amount) {
		return "", ErrTopUpPricingInvalid
	}
	precision := 2
	if waffoZeroDecimalCurrency(currency) {
		precision = 0
	}
	formatted := strconv.FormatFloat(amount, 'f', precision, 64)
	parsed, err := decimal.NewFromString(formatted)
	if err != nil || parsed.GreaterThan(decimal.NewFromFloat(setting.MaxPaymentProviderAmount)) {
		return "", ErrTopUpPricingInvalid
	}
	minor, err := WaffoMoneyToMinorUnits(formatted, currency)
	if err != nil || minor <= 0 {
		return "", ErrTopUpPricingInvalid
	}
	return formatted, nil
}

func NormalizeWaffoCurrency(currency string) (string, error) {
	if currency == "" || currency != strings.TrimSpace(currency) || len(currency) != 3 {
		return "", ErrWaffoPaymentMismatch
	}
	for i := range len(currency) {
		char := currency[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
			return "", ErrWaffoPaymentMismatch
		}
	}
	return strings.ToUpper(currency), nil
}

func WaffoMoneyToMinorUnits(value, currency string) (int64, error) {
	if !plainWaffoMoney(value) {
		return 0, ErrWaffoPaymentMismatch
	}
	currency, err := NormalizeWaffoCurrency(currency)
	if err != nil {
		return 0, err
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil || parsed.Sign() <= 0 {
		return 0, ErrWaffoPaymentMismatch
	}
	exponent := int32(2)
	if waffoZeroDecimalCurrency(currency) {
		exponent = 0
	}
	scaled := parsed.Shift(exponent)
	if !scaled.Equal(scaled.Truncate(0)) || scaled.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, ErrWaffoPaymentMismatch
	}
	return scaled.IntPart(), nil
}

func waffoZeroDecimalCurrency(currency string) bool {
	switch currency {
	case "IDR", "JPY", "KRW", "VND":
		return true
	default:
		return false
	}
}

func plainWaffoMoney(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxWaffoMoneyTextLength {
		return false
	}
	digits := 0
	dot := false
	for i := range len(value) {
		switch char := value[i]; {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits > 0
}

func CreateBoundWaffoTopUp(spec WaffoCheckoutSpec, tradeNo string) (*model.TopUp, WaffoCheckoutRequest, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	currency, err := NormalizeWaffoCurrency(spec.Currency)
	if err != nil || spec.UserID <= 0 || spec.Amount <= 0 || spec.RequestedAmount <= 0 ||
		!validWaffoIdentifier(tradeNo) || !strings.HasPrefix(tradeNo, "WAFFO-") ||
		!validWaffoSnapshotText(spec.MerchantID, maxWaffoIdentifierLength, true) ||
		!setting.ValidWaffoCallbackURL(spec.NotifyURL) || !setting.ValidWaffoCallbackURL(spec.ReturnURL) ||
		!validWaffoSnapshotText(spec.AppName, 255, false) ||
		!validWaffoSnapshotText(spec.PayMethodType, 128, true) ||
		!validWaffoSnapshotText(spec.PayMethodName, 128, true) {
		return nil, WaffoCheckoutRequest{}, ErrWaffoCheckoutBindingMismatch
	}
	requestedAt, err := time.Parse("2006-01-02T15:04:05.000Z", spec.RequestedAt)
	if err != nil || requestedAt.Location() != time.UTC {
		return nil, WaffoCheckoutRequest{}, ErrWaffoCheckoutBindingMismatch
	}
	providerMinor, err := WaffoMoneyToMinorUnits(spec.OrderAmount, currency)
	if err != nil || providerMinor <= 0 {
		return nil, WaffoCheckoutRequest{}, ErrWaffoPaymentMismatch
	}
	moneyDecimal, err := decimal.NewFromString(spec.OrderAmount)
	if err != nil || moneyDecimal.Sign() <= 0 || moneyDecimal.GreaterThan(decimal.NewFromFloat(setting.MaxPaymentProviderAmount)) {
		return nil, WaffoCheckoutRequest{}, ErrWaffoPaymentMismatch
	}
	money := moneyDecimal.InexactFloat64()
	if !finitePositiveMoney(money) {
		return nil, WaffoCheckoutRequest{}, ErrWaffoPaymentMismatch
	}
	creditQuota, err := topUpCreditQuota(spec.Amount)
	if err != nil {
		return nil, WaffoCheckoutRequest{}, err
	}
	description := fmt.Sprintf("Recharge %d credits", spec.RequestedAmount)
	request := WaffoCheckoutRequest{
		PaymentRequestID: tradeNo,
		MerchantOrderID:  tradeNo,
		OrderCurrency:    currency,
		OrderAmount:      spec.OrderAmount,
		OrderDescription: description,
		OrderRequestedAt: spec.RequestedAt,
		NotifyURL:        spec.NotifyURL,
		MerchantInfo:     WaffoMerchantInfo{MerchantID: spec.MerchantID},
		UserInfo: WaffoUserInfo{
			UserID: strconv.Itoa(spec.UserID), UserEmail: fmt.Sprintf("%d@examples.com", spec.UserID), UserTerminal: "WEB",
		},
		PaymentInfo: WaffoPaymentInfo{
			ProductName: WaffoProductOneTimePayment, PayMethodType: spec.PayMethodType, PayMethodName: spec.PayMethodName,
		},
		GoodsInfo:          WaffoGoodsInfo{GoodsName: description, AppName: spec.AppName},
		SuccessRedirectURL: spec.ReturnURL,
		FailedRedirectURL:  spec.ReturnURL,
	}
	snapshot, fingerprint, err := encodeWaffoSnapshot(request)
	if err != nil {
		return nil, WaffoCheckoutRequest{}, err
	}
	environment := WaffoEnvironmentProduction
	if spec.Sandbox {
		environment = WaffoEnvironmentSandbox
	}
	var order model.TopUp
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		now, clockErr := model.DatabaseUnixTimestamp(tx)
		if clockErr != nil {
			return clockErr
		}
		order = model.TopUp{
			UserId: spec.UserID, Amount: spec.Amount,
			CreditQuota: creditQuota, CreditQuotaVersion: WaffoTopUpCreditQuotaVersion,
			Money: money, TradeNo: tradeNo, PaymentMethod: PaymentMethodWaffo, PaymentProvider: PaymentProviderWaffo,
			ProviderAmountMinor: providerMinor, ProviderCurrency: currency,
			ProviderBindingVersion: WaffoCheckoutBindingVersion, ProviderOrderType: WaffoOrderTypeWallet,
			ProviderMode: environment, ReconciliationState: WaffoReconciliationCreate,
			ReconciliationDetail: "checkout_not_yet_bound", CheckoutRequest: snapshot,
			CheckoutFingerprint: fingerprint, ProviderCreateIdempotencyKey: tradeNo,
			CreateTime: now, Status: TopUpStatusPending,
		}
		return tx.Create(&order).Error
	})
	if err != nil {
		return nil, WaffoCheckoutRequest{}, err
	}
	return &order, request, nil
}

func BindWaffoTopUpCheckout(tradeNo string, binding WaffoCreateBinding) error {
	if !validWaffoIdentifier(binding.PaymentRequestID) || !validWaffoIdentifier(binding.MerchantOrderID) ||
		!validWaffoIdentifier(binding.AcquiringOrderID) || binding.PaymentRequestID != tradeNo || binding.MerchantOrderID != tradeNo {
		return ErrWaffoCheckoutBindingMismatch
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.TopUp
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if err := validateWaffoTopUpSnapshot(&order); err != nil {
			return err
		}
		if order.ProviderSessionId != nil {
			if *order.ProviderSessionId == binding.AcquiringOrderID {
				return nil
			}
			return ErrWaffoCheckoutBindingMismatch
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		result := tx.Model(&model.TopUp{}).
			Where("id = ? AND status = ? AND provider_session_id IS NULL", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"provider_session_id": binding.AcquiringOrderID, "reconciliation_state": WaffoReconciliationPending,
				"reconciliation_detail": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWaffoCheckoutBindingMismatch
		}
		return nil
	})
}

func CompleteBoundWaffoTopUpOrder(settlement WaffoSettlement) error {
	return transitionBoundWaffoTopUp(settlement, true)
}

func CloseBoundWaffoTopUpOrder(settlement WaffoSettlement) error {
	return transitionBoundWaffoTopUp(settlement, false)
}

func transitionBoundWaffoTopUp(settlement WaffoSettlement, paid bool) error {
	if err := normalizeWaffoSettlement(&settlement); err != nil {
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
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", settlement.MerchantOrderID).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if err := validateWaffoSettlementTx(tx, &order, &settlement); err != nil {
			return err
		}
		if paid {
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
				return fmt.Errorf("credit Waffo top-up user %d: %w", user.Id, gorm.ErrRecordNotFound)
			}
			var auditErr error
			auditEventID, auditErr = enqueueTopupLogTx(tx, user.Id, int(order.CreditQuota), order.TradeNo, completeTime)
			if auditErr != nil {
				return fmt.Errorf("persist Waffo top-up audit: %w", auditErr)
			}
			credited = true
			return nil
		}

		if order.Status == TopUpStatusFailed || order.Status == TopUpStatusSuccess {
			return nil
		}
		if order.Status != TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		transition := tx.Model(&model.TopUp{}).Where("id = ? AND status = ?", order.Id, TopUpStatusPending).
			Updates(map[string]any{
				"status": TopUpStatusFailed, "complete_time": completeTime,
				"reconciliation_state": "", "reconciliation_detail": "", "reconciliation_next_at": 0,
			})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return ErrTopUpStatusInvalid
		}
		return nil
	})
	if err != nil {
		return err
	}
	if credited {
		if err := DeliverAuditLogOutboxEvent(auditEventID); err != nil {
			common.SysError(fmt.Sprintf("Waffo audit delivery deferred trade_no=%s user_id=%d: %v", order.TradeNo, order.UserId, err))
		}
	}
	return nil
}

func normalizeWaffoSettlement(settlement *WaffoSettlement) error {
	if settlement == nil || !validWaffoIdentifier(settlement.PaymentRequestID) ||
		!validWaffoIdentifier(settlement.MerchantOrderID) || !validWaffoIdentifier(settlement.AcquiringOrderID) ||
		settlement.PaymentRequestID != settlement.MerchantOrderID ||
		!validWaffoSnapshotText(settlement.MerchantID, maxWaffoIdentifierLength, true) ||
		!validWaffoSnapshotText(settlement.UserID, maxWaffoIdentifierLength, false) ||
		settlement.ProductName != WaffoProductOneTimePayment ||
		(settlement.Environment != WaffoEnvironmentProduction && settlement.Environment != WaffoEnvironmentSandbox) {
		return ErrWaffoPaymentMismatch
	}
	currency, err := NormalizeWaffoCurrency(settlement.OrderCurrency)
	if err != nil {
		return ErrWaffoPaymentMismatch
	}
	settlement.OrderCurrency = currency
	if _, err := WaffoMoneyToMinorUnits(settlement.OrderAmount, currency); err != nil {
		return ErrWaffoPaymentMismatch
	}
	return nil
}

func validateWaffoSettlementTx(tx *gorm.DB, order *model.TopUp, settlement *WaffoSettlement) error {
	if err := validateWaffoTopUpSnapshot(order); err != nil {
		return err
	}
	snapshot, _ := parseWaffoSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	minor, _ := WaffoMoneyToMinorUnits(settlement.OrderAmount, settlement.OrderCurrency)
	if order.TradeNo != settlement.MerchantOrderID || snapshot.PaymentRequestID != settlement.PaymentRequestID ||
		order.ProviderAmountMinor != minor || order.ProviderCurrency != settlement.OrderCurrency ||
		order.ProviderMode != settlement.Environment || snapshot.MerchantInfo.MerchantID != settlement.MerchantID ||
		snapshot.UserInfo.UserID != settlement.UserID || snapshot.PaymentInfo.ProductName != settlement.ProductName {
		return ErrWaffoPaymentMismatch
	}
	return claimOrValidateWaffoCheckoutTx(tx, order, settlement.AcquiringOrderID)
}

func validateWaffoTopUpSnapshot(order *model.TopUp) error {
	if order == nil || order.PaymentProvider != PaymentProviderWaffo || order.PaymentMethod != PaymentMethodWaffo ||
		order.ProviderBindingVersion != WaffoCheckoutBindingVersion || order.ProviderOrderType != WaffoOrderTypeWallet ||
		(order.ProviderMode != WaffoEnvironmentProduction && order.ProviderMode != WaffoEnvironmentSandbox) ||
		order.ProviderAmountMinor <= 0 || order.ProviderCurrency == "" ||
		order.CreditQuotaVersion != WaffoTopUpCreditQuotaVersion || order.CreditQuota <= 0 || order.CreditQuota > common.MaxQuota ||
		order.ProviderCreateIdempotencyKey != order.TradeNo {
		return ErrWaffoLegacyOrderRequiresReview
	}
	expectedCredit, err := topUpCreditQuota(order.Amount)
	if err != nil || expectedCredit != order.CreditQuota {
		return ErrWaffoLegacyOrderRequiresReview
	}
	snapshot, err := parseWaffoSnapshot(order.CheckoutRequest, order.CheckoutFingerprint)
	if err != nil {
		return err
	}
	minor, err := WaffoMoneyToMinorUnits(snapshot.OrderAmount, snapshot.OrderCurrency)
	if err != nil || snapshot.PaymentRequestID != order.TradeNo || snapshot.MerchantOrderID != order.TradeNo ||
		snapshot.UserInfo.UserID != strconv.Itoa(order.UserId) || snapshot.PaymentInfo.ProductName != WaffoProductOneTimePayment ||
		minor != order.ProviderAmountMinor || snapshot.OrderCurrency != order.ProviderCurrency ||
		!setting.ValidWaffoCallbackURL(snapshot.NotifyURL) || !setting.ValidWaffoCallbackURL(snapshot.SuccessRedirectURL) ||
		snapshot.SuccessRedirectURL != snapshot.FailedRedirectURL {
		return ErrWaffoCheckoutBindingMismatch
	}
	return nil
}

func claimOrValidateWaffoCheckoutTx(tx *gorm.DB, order *model.TopUp, acquiringOrderID string) error {
	if tx == nil || order == nil || !validWaffoIdentifier(acquiringOrderID) {
		return ErrWaffoCheckoutBindingMismatch
	}
	if order.ProviderSessionId != nil {
		if *order.ProviderSessionId == acquiringOrderID {
			return nil
		}
		return ErrWaffoCheckoutBindingMismatch
	}
	if order.Status != TopUpStatusPending || order.ReconciliationState != WaffoReconciliationCreate {
		return ErrWaffoCheckoutBindingMismatch
	}
	result := tx.Model(&model.TopUp{}).
		Where("id = ? AND status = ? AND provider_session_id IS NULL AND reconciliation_state = ?",
			order.Id, TopUpStatusPending, WaffoReconciliationCreate).
		Updates(map[string]any{
			"provider_session_id": acquiringOrderID, "reconciliation_state": WaffoReconciliationPending,
			"reconciliation_detail": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWaffoCheckoutBindingMismatch
	}
	claimed := acquiringOrderID
	order.ProviderSessionId = &claimed
	order.ReconciliationState = WaffoReconciliationPending
	order.ReconciliationDetail = ""
	return nil
}

func encodeWaffoSnapshot(request WaffoCheckoutRequest) (string, string, error) {
	encoded, err := common.Marshal(request)
	if err != nil || len(encoded) == 0 || len(encoded) > maxWaffoCheckoutSnapshotLength {
		return "", "", ErrWaffoCheckoutBindingMismatch
	}
	sum := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(sum[:]), nil
}

func parseWaffoSnapshot(raw, fingerprint string) (WaffoCheckoutRequest, error) {
	if raw == "" || len(raw) > maxWaffoCheckoutSnapshotLength || len(fingerprint) != sha256.Size*2 {
		return WaffoCheckoutRequest{}, ErrWaffoCheckoutBindingMismatch
	}
	sum := sha256.Sum256([]byte(raw))
	if !strings.EqualFold(hex.EncodeToString(sum[:]), fingerprint) {
		return WaffoCheckoutRequest{}, ErrWaffoCheckoutBindingMismatch
	}
	var snapshot WaffoCheckoutRequest
	if err := common.Unmarshal([]byte(raw), &snapshot); err != nil {
		return WaffoCheckoutRequest{}, ErrWaffoCheckoutBindingMismatch
	}
	return snapshot, nil
}

func validWaffoIdentifier(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxWaffoIdentifierLength || strings.ContainsRune(value, '\x00') {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

// ValidWaffoCreateIdentifier exposes the provider identifier grammar to the
// transport boundary without exposing any database operation.
func ValidWaffoCreateIdentifier(value string) bool {
	return validWaffoIdentifier(value)
}

func validWaffoSnapshotText(value string, limit int, emptyOK bool) bool {
	if value == "" {
		return emptyOK
	}
	if value != strings.TrimSpace(value) || len(value) > limit {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}
