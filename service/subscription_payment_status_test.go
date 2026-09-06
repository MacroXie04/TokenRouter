package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestStripeBindingWriteFailureRetainsDurableReconciliationEvidence(t *testing.T) {
	t.Run("wallet", func(t *testing.T) {
		initSubDB(t)
		user := subUser(t, "wallet-binding-write-failure", 0, "default")
		order, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 250, 2.5, "USD", "ref_binding_write_failure")
		require.NoError(t, err)
		assert.Equal(t, StripeReconciliationCreationUnknown, order.ReconciliationState)

		injected := errors.New("injected wallet binding write failure")
		const callback = "test:wallet_binding_write_failure"
		require.NoError(t, model.DB.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "top_ups" {
				tx.AddError(injected)
			}
		}))
		require.ErrorIs(t, BindStripeTopUpSession(order.TradeNo, "cs_wallet_recovered"), injected)
		// Reconciliation writes are no longer fire-and-forget: callers receive
		// the write failure, while the pre-Stripe marker remains durable.
		require.ErrorIs(t, FlagStripeTopUpReconciliation(order.TradeNo, StripeReconciliationBindingMismatch), injected)
		require.NoError(t, model.DB.Callback().Update().Remove(callback))

		var pending model.TopUp
		require.NoError(t, model.DB.First(&pending, order.Id).Error)
		assert.Equal(t, TopUpStatusPending, pending.Status)
		assert.Nil(t, pending.ProviderSessionId)
		assert.Equal(t, StripeReconciliationCreationUnknown, pending.ReconciliationState)

		// A later signed, fully matching Checkout event can atomically claim the
		// ambiguous session and settle the order.
		amount := int64(250)
		require.NoError(t, CompleteBoundStripeTopUpOrder(order.TradeNo, "cs_wallet_recovered",
			StripeCheckoutModePayment, StripeOrderTypeWallet, &amount, "usd"))
		pending = model.TopUp{}
		require.NoError(t, model.DB.First(&pending, order.Id).Error)
		assert.Equal(t, TopUpStatusSuccess, pending.Status)
		require.NotNil(t, pending.ProviderSessionId)
		assert.Equal(t, "cs_wallet_recovered", *pending.ProviderSessionId)
		assert.Empty(t, pending.ReconciliationState)
	})

	t.Run("subscription", func(t *testing.T) {
		initSubDB(t)
		user := subUser(t, "subscription-binding-write-failure", 0, "default")
		plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
			plan.PriceAmount = "3.00"
			plan.Currency = "USD"
			plan.StripePriceId = "price_binding_failure"
			plan.MaxPurchasePerUser = 1
		})
		order, err := CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
			ID: "price_binding_failure", AmountMinor: 300, Currency: "USD",
		}, "sub_ref_binding_write_failure")
		require.NoError(t, err)
		assert.Equal(t, StripeReconciliationCreationUnknown, order.ReconciliationState)

		injected := errors.New("injected subscription binding write failure")
		const callback = "test:subscription_binding_write_failure"
		require.NoError(t, model.DB.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "subscription_orders" {
				tx.AddError(injected)
			}
		}))
		require.ErrorIs(t, BindStripeSubscriptionSession(order.TradeNo, "cs_subscription_recovered"), injected)
		require.ErrorIs(t, FlagStripeSubscriptionReconciliation(order.TradeNo, StripeReconciliationBindingMismatch), injected)
		require.NoError(t, model.DB.Callback().Update().Remove(callback))

		var pending model.SubscriptionOrder
		require.NoError(t, model.DB.First(&pending, order.Id).Error)
		assert.Equal(t, TopUpStatusPending, pending.Status)
		assert.True(t, pending.CapacityReserved)
		assert.Nil(t, pending.ProviderSessionId)
		assert.Equal(t, StripeReconciliationCreationUnknown, pending.ReconciliationState)

		amount := int64(300)
		require.NoError(t, CompleteBoundStripeSubscriptionOrder(order.TradeNo, "payload", "cs_subscription_recovered",
			StripeCheckoutModeSubscription, StripeOrderTypeSubscription, order.ProviderPriceId, "", &amount, "usd"))
		pending = model.SubscriptionOrder{}
		require.NoError(t, model.DB.First(&pending, order.Id).Error)
		assert.Equal(t, TopUpStatusSuccess, pending.Status)
		assert.False(t, pending.CapacityReserved)
		require.NotNil(t, pending.ProviderSessionId)
		assert.Equal(t, "cs_subscription_recovered", *pending.ProviderSessionId)
		assert.Empty(t, pending.ReconciliationState)
	})
}

func TestAmbiguousStripeSessionCanBeClaimedByTerminalWebhook(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "terminal-session-claim", 0, "default")
	wallet, err := CreateBoundStripeTopUpWithTradeNo(user.Id, 100, 1, "USD", "ref_terminal_claim")
	require.NoError(t, err)
	require.NoError(t, UpdateBoundPendingTopUpStatus(wallet.TradeNo, "cs_terminal_wallet",
		StripeCheckoutModePayment, StripeOrderTypeWallet, TopUpStatusExpired,
		&wallet.ProviderAmountMinor, wallet.ProviderCurrency))
	var storedWallet model.TopUp
	require.NoError(t, model.DB.First(&storedWallet, wallet.Id).Error)
	assert.Equal(t, TopUpStatusExpired, storedWallet.Status)
	require.NotNil(t, storedWallet.ProviderSessionId)
	assert.Equal(t, "cs_terminal_wallet", *storedWallet.ProviderSessionId)
	assert.Empty(t, storedWallet.ReconciliationState)

	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "1.00"
		plan.Currency = "USD"
		plan.StripePriceId = "price_terminal_claim"
	})
	subscription, err := CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
		ID: "price_terminal_claim", AmountMinor: 100, Currency: "USD",
	}, "sub_ref_terminal_claim")
	require.NoError(t, err)
	require.NoError(t, UpdateBoundPendingSubscriptionOrderStatus(subscription.TradeNo, "cs_terminal_subscription",
		StripeCheckoutModeSubscription, StripeOrderTypeSubscription, subscription.ProviderPriceId, TopUpStatusFailed,
		&subscription.ProviderAmountMinor, subscription.ProviderCurrency))
	var storedSubscription model.SubscriptionOrder
	require.NoError(t, model.DB.First(&storedSubscription, subscription.Id).Error)
	assert.Equal(t, TopUpStatusFailed, storedSubscription.Status)
	assert.False(t, storedSubscription.CapacityReserved)
	require.NotNil(t, storedSubscription.ProviderSessionId)
	assert.Equal(t, "cs_terminal_subscription", *storedSubscription.ProviderSessionId)
	assert.Empty(t, storedSubscription.ReconciliationState)
}

func TestUpdatePendingTopUpStatusOnlyAllowsTerminalPendingTransition(t *testing.T) {
	initSubDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.TopUp{}))
	user := subUser(t, "topup-status-transition", 0, "default")
	pending, err := CreateTopUp(user.Id, 10, 1, "stripe", PaymentProviderStripe)
	require.NoError(t, err)

	require.ErrorIs(t,
		UpdatePendingTopUpStatus(pending.TradeNo, PaymentProviderStripe, TopUpStatusSuccess),
		ErrTopUpStatusInvalid)
	require.ErrorIs(t,
		UpdatePendingTopUpStatus(pending.TradeNo, PaymentProviderEpay, TopUpStatusExpired),
		ErrPaymentMethodMismatch)
	require.NoError(t, UpdatePendingTopUpStatus(pending.TradeNo, PaymentProviderStripe, TopUpStatusExpired))

	var got model.TopUp
	require.NoError(t, model.DB.First(&got, pending.Id).Error)
	assert.Equal(t, TopUpStatusExpired, got.Status)
	assert.NotZero(t, got.CompleteTime)

	// A delayed failure/expiry notification can never overwrite a successful
	// financial terminal state.
	success, err := CreateTopUp(user.Id, 10, 1, "stripe", PaymentProviderStripe)
	require.NoError(t, err)
	require.NoError(t, CompleteTopUp(user.Id, success.TradeNo, success.Amount))
	require.NoError(t, UpdatePendingTopUpStatus(success.TradeNo, PaymentProviderStripe, TopUpStatusExpired))
	got = model.TopUp{}
	require.NoError(t, model.DB.First(&got, success.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, got.Status)
}

func TestConcurrentStripeSubscriptionReservationsCannotOversubscribeCap(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reservation-race", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "1.00"
		plan.Currency = "USD"
		plan.StripePriceId = "price_race"
		plan.MaxPurchasePerUser = 1
	})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
				ID: "price_race", AmountMinor: 100, Currency: "USD",
			}, fmt.Sprintf("sub_ref_race_%d", index))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes)
	var reservations int64
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).
		Where("user_id = ? AND plan_id = ? AND status = ? AND capacity_reserved = ?",
			user.Id, plan.Id, TopUpStatusPending, true).Count(&reservations).Error)
	assert.Equal(t, int64(1), reservations)
}

func TestUpdatePendingSubscriptionOrderStatusNeverOverwritesSuccess(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "subscription-status-transition", 0, "default")
	plan := seedRawPlan(t, nil)
	order := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 1, TradeNo: "subscription-status-pending",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(&order).Error)
	require.ErrorIs(t,
		UpdatePendingSubscriptionOrderStatus(order.TradeNo, PaymentProviderStripe, TopUpStatusSuccess),
		ErrSubscriptionOrderStatusInvalid)
	require.ErrorIs(t,
		UpdatePendingSubscriptionOrderStatus(order.TradeNo, PaymentProviderEpay, TopUpStatusFailed),
		ErrPaymentMethodMismatch)
	require.NoError(t,
		UpdatePendingSubscriptionOrderStatus(order.TradeNo, PaymentProviderStripe, TopUpStatusFailed))
	var got model.SubscriptionOrder
	require.NoError(t, model.DB.First(&got, order.Id).Error)
	assert.Equal(t, TopUpStatusFailed, got.Status)
	assert.NotZero(t, got.CompleteTime)

	success := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 1, TradeNo: "subscription-status-success",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: TopUpStatusSuccess,
	}
	require.NoError(t, model.DB.Create(&success).Error)
	require.NoError(t,
		UpdatePendingSubscriptionOrderStatus(success.TradeNo, PaymentProviderStripe, TopUpStatusFailed))
	got = model.SubscriptionOrder{}
	require.NoError(t, model.DB.First(&got, success.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, got.Status)
}

func TestCompleteStripeSubscriptionOrderBindsAmountAndCurrency(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-bound-subscription", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "19.990000"
		plan.Currency = "USD"
	})
	order, err := CreateStripeSubscriptionOrder(user.Id, plan.Id, plan.PriceAmount, plan.Currency, "stripe-bound-subscription-order")
	require.NoError(t, err)
	assert.Equal(t, int64(1999), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)

	wrongAmount := int64(1998)
	require.ErrorIs(t, CompleteStripeSubscriptionOrder(order.TradeNo, "payload", &wrongAmount, "usd"), ErrStripePaymentMismatch)
	correctAmount := int64(1999)
	require.ErrorIs(t, CompleteStripeSubscriptionOrder(order.TradeNo, "payload", &correctAmount, "eur"), ErrStripePaymentMismatch)

	var subscriptions, topups int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Zero(t, subscriptions)
	assert.Zero(t, topups)
	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)

	require.NoError(t, CompleteStripeSubscriptionOrder(order.TradeNo, "payload", &correctAmount, "USD"))
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", order.TradeNo).Count(&topups).Error)
	assert.Equal(t, int64(1), subscriptions)
	assert.Equal(t, int64(1), topups)
}

func TestStripeSubscriptionReservationBlocksAllPurchasePathsAndFulfillsSnapshot(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-reserved-subscription", 10_000_000, "default")
	allowOverflow := false
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.Title = "Original"
		plan.PriceAmount = "10.00"
		plan.Currency = "USD"
		plan.StripePriceId = "price_original"
		plan.MaxPurchasePerUser = 1
		plan.TotalAmount = 1234
		plan.DurationUnit = "day"
		plan.DurationValue = 2
		plan.UpgradeGroup = "vip"
		plan.AllowWalletOverflow = &allowOverflow
	})
	order, err := CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
		ID: "price_original", AmountMinor: 1000, Currency: "usd",
	}, "sub_ref_reserved_snapshot")
	require.NoError(t, err)
	assert.True(t, order.CapacityReserved)

	_, err = CreateBoundStripeSubscriptionOrder(user.Id, plan.Id, ValidatedStripePrice{
		ID: "price_original", AmountMinor: 1000, Currency: "USD",
	}, "sub_ref_second_reservation")
	assert.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)
	assert.ErrorIs(t, PurchaseSubscriptionWithBalance(user.Id, plan.Id), ErrSubscriptionPurchaseLimit)
	_, err = AdminBindSubscription(user.Id, plan.Id)
	assert.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)

	// Mutating or even disabling the live plan after payment starts cannot
	// change the entitlement already offered to the customer.
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"title": "Mutated", "total_amount": int64(9999), "duration_value": 30,
		"upgrade_group": "mutated", "allow_wallet_overflow": true, "enabled": false,
	}).Error)
	require.NoError(t, BindStripeSubscriptionSession(order.TradeNo, "cs_subscription_snapshot"))
	amount := int64(1000)
	require.NoError(t, CompleteBoundStripeSubscriptionOrder(order.TradeNo, "payload", "cs_subscription_snapshot",
		StripeCheckoutModeSubscription, StripeOrderTypeSubscription, order.ProviderPriceId, "", &amount, "usd"))

	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", user.Id, plan.Id).First(&sub).Error)
	assert.Equal(t, int64(1234), sub.AmountTotal)
	assert.Equal(t, "vip", sub.UpgradeGroup)
	assert.False(t, sub.AllowWalletOverflow)
	assert.InDelta(t, 2*24*60*60, sub.EndTime-sub.StartTime, 2)
	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.False(t, stored.CapacityReserved, "fulfillment consumes its own reservation")
}

func TestBoundStripeSubscriptionQuarantinesLegacyPendingButAcceptsLegacySuccessDuplicate(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "stripe-legacy-subscription", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.Currency = "USD"
	})
	legacy, err := CreateStripeSubscriptionOrder(user.Id, plan.Id, plan.PriceAmount, plan.Currency, "sub_ref_legacy_pending")
	require.NoError(t, err)
	amount := legacy.ProviderAmountMinor
	require.ErrorIs(t, CompleteBoundStripeSubscriptionOrder(legacy.TradeNo, "payload", "cs_legacy",
		StripeCheckoutModeSubscription, StripeOrderTypeSubscription, legacy.ProviderPriceId, "", &amount, "usd"), ErrStripeLegacyOrderRequiresReview)
	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, legacy.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, StripeReconciliationLegacyBinding, stored.ReconciliationState)

	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("id = ?", legacy.Id).
		Update("status", TopUpStatusSuccess).Error)
	require.NoError(t, CompleteBoundStripeSubscriptionOrder(legacy.TradeNo, "", "", "", "", "", "", nil, ""))
}
