package service

import (
	"errors"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/setting"
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
)

// ErrTopUpNotFound is returned when a trade number does not resolve.
var ErrTopUpNotFound = errors.New("充值订单不存在")

// CreateTopUp creates a wallet recharge order with a random trade number.
func CreateTopUp(userId int, amount int64, money float64, paymentMethod, paymentProvider string) (*model.TopUp, error) {
	return CreateTopUpWithTradeNo(userId, amount, money, paymentMethod, paymentProvider, common.RandomAlphanumeric(32))
}

// CreateTopUpWithTradeNo creates a wallet recharge order with the given
// trade number (the payment gateways carry their own reference formats).
func CreateTopUpWithTradeNo(userId int, amount int64, money float64, paymentMethod, paymentProvider, tradeNo string) (*model.TopUp, error) {
	t := model.TopUp{
		UserId:          userId,
		Amount:          amount,
		Money:           money,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentMethod,
		PaymentProvider: paymentProvider,
		CreateTime:      common.NowTimestamp(),
		Status:          TopUpStatusPending,
	}
	if err := model.DB.Create(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTopupMoney converts a top-up amount into the payable money in the
// configured display type (token amounts are divided by QuotaPerUnit),
// applying the user group ratio, the configured price, and any preset
// discount for the requested amount (reference formula).
func GetTopupMoney(amount int64, group string) float64 {
	dAmount := decimal.NewFromInt(amount)
	if setting.GetQuotaDisplayType() == setting.QuotaDisplayTypeTokens {
		dAmount = dAmount.Div(decimal.NewFromFloat(common.QuotaPerUnit))
	}
	ratio := GroupRatio(group)
	if ratio == 0 {
		ratio = 1
	}
	discount := 1.0
	if ds, ok := setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	payMoney := dAmount.
		Mul(decimal.NewFromFloat(setting.GetTopUpPrice())).
		Mul(decimal.NewFromFloat(ratio)).
		Mul(decimal.NewFromFloat(discount))
	return payMoney.InexactFloat64()
}

// FormatPayMoney renders a payable amount with two decimals (the reference
// money wire format).
func FormatPayMoney(money float64) string {
	return strconv.FormatFloat(money, 'f', 2, 64)
}

// CompleteTopUp idempotently marks an order successful and credits the user's
// quota exactly once (guarded by the pending->success transition).
func CompleteTopUp(userId int, tradeNo string, amount int64) error {
	res := model.DB.Model(&model.TopUp{}).
		Where("trade_no = ? AND user_id = ? AND status = ?", tradeNo, userId, TopUpStatusPending).
		Updates(map[string]any{
			"status":        TopUpStatusSuccess,
			"complete_time": common.NowTimestamp(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Already completed or unknown; treat as idempotent success.
		return nil
	}
	if err := IncreaseUserQuota(userId, int(amount)); err != nil {
		return err
	}
	RecordTopupLog(userId, int(amount), 0, tradeNo)
	return nil
}

// GetTopUpByTradeNo loads a top-up order by trade number.
func GetTopUpByTradeNo(tradeNo string) (*model.TopUp, error) {
	var t model.TopUp
	if err := model.DB.Where("trade_no = ?", tradeNo).First(&t).Error; err != nil {
		return nil, ErrTopUpNotFound
	}
	return &t, nil
}
