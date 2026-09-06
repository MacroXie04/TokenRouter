package service

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestBoundEpaySubscriptionFulfillmentIsAtomicIdempotentAndImmutable(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "epay-bound-subscription", 0, "default")
	allowOverflow := false
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.Title = "EPay original"
		plan.PriceAmount = "12.3400"
		plan.Currency = "USD"
		plan.TotalAmount = 1234
		plan.DurationUnit = SubscriptionDurationDay
		plan.DurationValue = 2
		plan.UpgradeGroup = "vip"
		plan.AllowWalletOverflow = &allowOverflow
		plan.MaxPurchasePerUser = 1
	})
	tradeNo := NewSubscriptionEpayTradeNo(user.Id, 1_700_000_000, "ABC123")
	order, err := CreateBoundEpaySubscriptionOrder(user.Id, plan.Id, "alipay", tradeNo)
	require.NoError(t, err)
	assert.Equal(t, 12.34, order.Money)
	assert.Equal(t, int64(1234), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)
	assert.Equal(t, EpaySubscriptionBindingVersion, order.ProviderBindingVersion)
	assert.Equal(t, StripeOrderTypeSubscription, order.ProviderOrderType)
	assert.Equal(t, StripeCheckoutModePayment, order.ProviderMode)
	assert.True(t, order.CapacityReserved)
	assert.NotEmpty(t, order.EntitlementSnapshot)

	_, err = CreateBoundEpaySubscriptionOrder(user.Id, plan.Id, "alipay",
		NewSubscriptionEpayTradeNo(user.Id, 1_700_000_001, "DEF456"))
	require.ErrorIs(t, err, ErrSubscriptionPurchaseLimit,
		"a pending EPay checkout must reserve the purchase-cap slot")

	// A valid signature is checked by the controller. The settlement boundary
	// still binds the signed method and amount inside the fulfillment transaction.
	require.ErrorIs(t,
		CompleteBoundEpaySubscriptionOrder(order.TradeNo, "wrong-money", "alipay", "12.33"),
		ErrEpayPaymentMismatch)
	require.ErrorIs(t,
		CompleteBoundEpaySubscriptionOrder(order.TradeNo, "wrong-method", "wxpay", "12.34"),
		ErrEpayPaymentMismatch)
	require.ErrorIs(t,
		CompleteSubscriptionOrder(order.TradeNo, "unbound-bypass", PaymentProviderEpay, "alipay"),
		ErrEpayPaymentMismatch,
		"versioned EPay orders cannot bypass amount binding through the legacy completion helper")
	require.ErrorIs(t,
		CompleteBoundEpaySubscriptionOrder(order.TradeNo, strings.Repeat("x", maxEpayProviderPayloadLength+1), "alipay", "12.34"),
		ErrEpayPaymentMismatch)
	require.ErrorIs(t,
		CompleteBoundEpaySubscriptionOrder(order.TradeNo, "oversized-money", "alipay", strings.Repeat("9", 65)),
		ErrEpayPaymentMismatch)

	var subscriptions, topups int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Zero(t, subscriptions)
	assert.Zero(t, topups)

	// Fulfillment must use the snapshot offered at checkout, not a later plan
	// edit. This also proves the reserved slot is consumed rather than counted twice.
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"title": "mutated", "total_amount": int64(9999), "duration_value": 30,
		"upgrade_group": "pro", "allow_wallet_overflow": true,
	}).Error)

	injected := errors.New("injected EPay audit persistence failure")
	const callback = "test:epay_subscription_audit_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err = CompleteBoundEpaySubscriptionOrder(order.TradeNo, "first-payload", "alipay", "12.340")
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))

	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.True(t, stored.CapacityReserved)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Zero(t, subscriptions, "an audit failure must roll back entitlement creation")
	assert.Zero(t, topups, "an audit failure must roll back the financial mirror row")

	require.NoError(t, CompleteBoundEpaySubscriptionOrder(order.TradeNo, "first-payload", "alipay", "12.34"))
	require.NoError(t, CompleteBoundEpaySubscriptionOrder(order.TradeNo, "duplicate-payload", "alipay", "12.3400"))
	require.ErrorIs(t,
		CompleteBoundEpaySubscriptionOrder(order.TradeNo, "forged-replay", "alipay", "12.35"),
		ErrEpayPaymentMismatch,
		"successful orders must still reject a mismatched replay")

	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.False(t, stored.CapacityReserved)
	assert.Equal(t, "first-payload", stored.ProviderPayload, "duplicates cannot rewrite callback evidence")
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Equal(t, int64(1), subscriptions)
	assert.Equal(t, int64(1), topups)

	var entitlement model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&entitlement).Error)
	assert.Equal(t, int64(1234), entitlement.AmountTotal)
	assert.Equal(t, "vip", entitlement.UpgradeGroup)
	assert.False(t, entitlement.AllowWalletOverflow)
	assert.InDelta(t, 2*24*60*60, entitlement.EndTime-entitlement.StartTime, 2)

	var logs, outbox int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logs).Error)
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).Count(&outbox).Error)
	assert.Equal(t, int64(1), logs)
	assert.Equal(t, int64(1), outbox)
}

func TestConcurrentBoundEpaySubscriptionCallbacksHaveSingleEffect(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "epay-concurrent-callback", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "1.00"
		plan.Currency = "USD"
		plan.MaxPurchasePerUser = 1
	})
	order, err := CreateBoundEpaySubscriptionOrder(user.Id, plan.Id, "alipay",
		NewSubscriptionEpayTradeNo(user.Id, 1_700_000_002, "GHI789"))
	require.NoError(t, err)

	const workers = 4
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- CompleteBoundEpaySubscriptionOrder(order.TradeNo, "payload", "alipay", "1.00")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
		}
	}
	assert.Positive(t, successes)

	var subscriptions, topups, logs int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logs).Error)
	assert.Equal(t, int64(1), subscriptions)
	assert.Equal(t, int64(1), topups)
	assert.Equal(t, int64(1), logs)
}
