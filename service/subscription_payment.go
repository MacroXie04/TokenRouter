package service

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// PaymentMethodStripe is the Stripe payment method for subscription orders
// (PaymentMethodBalance/PaymentProviderBalance live in service/subscription.go).
const PaymentMethodStripe = "stripe"

// Subscription-order completion errors (reference sentinels).
var (
	ErrSubscriptionOrderNotFound      = errors.New("订阅订单不存在")
	ErrSubscriptionOrderStatusInvalid = errors.New("订阅订单状态无效")
	ErrPaymentMethodMismatch          = errors.New("支付方式不匹配")
)

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
	var count int64
	if err := model.DB.Model(&model.UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CompleteSubscriptionOrder fulfills a paid subscription order: it creates
// the user subscription, records the top-up row, and marks the order
// successful (reference transaction: idempotent on success, provider- and
// status-validated, user-row locked so concurrent completions serialize the
// per-user purchase cap).
func CompleteSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	var logUserId int
	var logPlanTitle string
	var logMoney float64
	var logPaymentMethod string
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.SubscriptionOrder
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status == TopUpStatusSuccess {
			return nil
		}
		if order.Status != TopUpStatusPending {
			return ErrSubscriptionOrderStatusInvalid
		}
		plan, err := GetSubscriptionPlanById(order.PlanId)
		if err != nil {
			return err
		}
		// Lock the user row: concurrent completions of different orders for
		// the same user must serialize the MaxPurchasePerUser check.
		var userRow model.User
		if err := subscriptionLockForUpdate(tx).Select("id").Where("id = ?", order.UserId).First(&userRow).Error; err != nil {
			return err
		}
		if _, err := createUserSubscriptionFromPlanTx(tx, order.UserId, plan, "order"); err != nil {
			return err
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		order.Status = TopUpStatusSuccess
		order.CompleteTime = common.NowTimestamp()
		if providerPayload != "" {
			order.ProviderPayload = providerPayload
		}
		if actualPaymentMethod != "" && order.PaymentMethod != actualPaymentMethod {
			order.PaymentMethod = actualPaymentMethod
		}
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		logUserId = order.UserId
		logPlanTitle = plan.Title
		logMoney = order.Money
		logPaymentMethod = order.PaymentMethod
		return nil
	})
	if err != nil {
		return err
	}
	if logUserId > 0 {
		RecordSystemLog(logUserId, LogTypeTopup,
			fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", logPlanTitle, logMoney, logPaymentMethod))
	}
	return nil
}

// upsertSubscriptionTopUpTx records (or completes) the financial top-up row
// backing a subscription order (reference semantics: the order's money is
// the top-up amount; payment-method mismatches are rejected).
func upsertSubscriptionTopUpTx(tx *gorm.DB, order *model.SubscriptionOrder) error {
	if tx == nil || order == nil {
		return errors.New("invalid subscription order")
	}
	now := common.NowTimestamp()
	var topup model.TopUp
	if err := tx.Where("trade_no = ?", order.TradeNo).First(&topup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			topup = model.TopUp{
				UserId:        order.UserId,
				Amount:        0,
				Money:         order.Money,
				TradeNo:       order.TradeNo,
				PaymentMethod: order.PaymentMethod,
				CreateTime:    order.CreateTime,
				CompleteTime:  now,
				Status:        TopUpStatusSuccess,
			}
			return tx.Create(&topup).Error
		}
		return err
	}
	topup.Money = order.Money
	if topup.PaymentMethod == "" {
		topup.PaymentMethod = order.PaymentMethod
	} else if topup.PaymentMethod != order.PaymentMethod {
		return ErrPaymentMethodMismatch
	}
	if topup.CreateTime == 0 {
		topup.CreateTime = order.CreateTime
	}
	topup.CompleteTime = now
	topup.Status = TopUpStatusSuccess
	return tx.Save(&topup).Error
}

// ExpireSubscriptionOrder marks a pending subscription order as expired
// (reference checkout.session.expired handling).
func ExpireSubscriptionOrder(tradeNo string, expectedPaymentProvider string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.SubscriptionOrder
		if err := subscriptionLockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			return ErrSubscriptionOrderNotFound
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != TopUpStatusPending {
			return nil
		}
		order.Status = TopUpStatusExpired
		order.CompleteTime = common.NowTimestamp()
		return tx.Save(&order).Error
	})
}

// UpdatePendingTopUpStatus moves a pending top-up order to the given status
// (webhook fallback for legacy top-up orders).
func UpdatePendingTopUpStatus(tradeNo string, expectedProvider string, status string) error {
	var topup model.TopUp
	if err := model.DB.Where("trade_no = ?", tradeNo).First(&topup).Error; err != nil {
		return ErrTopUpNotFound
	}
	if expectedProvider != "" && topup.PaymentProvider != expectedProvider {
		return ErrPaymentMethodMismatch
	}
	if topup.Status != TopUpStatusPending {
		return nil
	}
	return model.DB.Model(&model.TopUp{}).Where("id = ?", topup.Id).Update("status", status).Error
}

// NewSubscriptionStripeTradeNo builds the trade-no for subscription Stripe
// orders (reference format: sub_ref_ + sha1 of the order reference).
func NewSubscriptionStripeTradeNo(userId int, millis int64, randSuffix string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("sub-stripe-ref-%d-%d-%s", userId, millis, randSuffix)))
	return "sub_ref_" + hex.EncodeToString(sum[:])
}
