package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func creemWalletProduct() CreemProductEconomics {
	return CreemProductEconomics{
		ProductID: "prod_wallet_exact",
		Name:      "Exact wallet credit",
		PriceText: "12.34",
		Currency:  "usd",
		Quota:     12_345,
	}
}

func creemWalletSettlement(order *model.TopUp, checkoutID string) CreemSettlement {
	return CreemSettlement{
		CheckoutID:        checkoutID,
		ProviderOrderID:   "ord_wallet_exact",
		ReferenceID:       order.TradeNo,
		ProductID:         "prod_wallet_exact",
		AmountPaid:        1234,
		Currency:          "usd",
		OrderType:         "onetime",
		MetadataReference: order.TradeNo,
		MetadataQuota:     "12345",
	}
}

func TestCreemMoneyRejectsOversizedNewCharge(t *testing.T) {
	_, _, _, err := creemMoney("1000000.00", "USD")
	assert.ErrorIs(t, err, ErrCreemPaymentMismatch)
	money, minor, currency, err := creemMoney("999999.99", "USD")
	require.NoError(t, err)
	assert.Equal(t, 999999.99, money)
	assert.Equal(t, int64(99_999_999), minor)
	assert.Equal(t, "USD", currency)
}

func TestCreemWalletOrderSnapshotsDirectQuotaAndSettlesExactlyOnce(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "creem-wallet", 100, "default")
	user.Email = "wallet@example.test"
	require.NoError(t, model.DB.Model(user).Update("email", user.Email).Error)
	tradeNo := "ref_creem_wallet_exact"
	order, request, err := CreateBoundCreemTopUpWithTradeNo(user.Id, creemWalletProduct(), tradeNo)
	require.NoError(t, err)
	assert.Equal(t, int64(12_345), order.Amount)
	assert.Equal(t, int64(12_345), order.CreditQuota, "Creem quota is direct and is not multiplied by QuotaPerUnit")
	assert.Equal(t, CreemTopUpCreditQuotaVersion, order.CreditQuotaVersion)
	assert.Equal(t, int64(1234), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)
	assert.Equal(t, "prod_wallet_exact", request.ProductID)
	assert.Equal(t, tradeNo, request.Metadata["reference_id"])
	assert.Equal(t, "12345", request.Metadata["quota"])
	assert.NotEmpty(t, order.CheckoutFingerprint)
	assert.Equal(t, tradeNo, order.ProviderCreateIdempotencyKey)

	// Replaying the same local create is idempotent; changing an immutable
	// economic field under the same merchant id is rejected.
	replayed, replayRequest, err := CreateBoundCreemTopUpWithTradeNo(user.Id, creemWalletProduct(), tradeNo)
	require.NoError(t, err)
	assert.Equal(t, order.Id, replayed.Id)
	assert.Equal(t, request, replayRequest)
	changed := creemWalletProduct()
	changed.Quota++
	_, _, err = CreateBoundCreemTopUpWithTradeNo(user.Id, changed, tradeNo)
	assert.ErrorIs(t, err, ErrCreemCheckoutBindingMismatch)

	require.NoError(t, BindCreemTopUpCheckout(tradeNo, "ch_wallet_exact"))
	require.NoError(t, BindCreemTopUpCheckout(tradeNo, "ch_wallet_exact"))
	assert.ErrorIs(t, BindCreemTopUpCheckout(tradeNo, "ch_wallet_other"), ErrCreemCheckoutBindingMismatch)
	assert.ErrorIs(t, CompleteTopUp(user.Id, tradeNo, order.Amount), ErrCreemPaymentMismatch,
		"the legacy completion helper cannot bypass a versioned Creem binding")

	settlement := creemWalletSettlement(order, "ch_wallet_exact")
	wrong := settlement
	wrong.AmountPaid--
	assert.ErrorIs(t, CompleteBoundCreemTopUpOrder(tradeNo, wrong), ErrCreemPaymentMismatch)
	wrong = settlement
	wrong.ProductID = "prod_forged"
	assert.ErrorIs(t, CompleteBoundCreemTopUpOrder(tradeNo, wrong), ErrCreemPaymentMismatch)
	wrong = settlement
	wrong.CheckoutID = "ch_wallet_forged"
	assert.ErrorIs(t, CompleteBoundCreemTopUpOrder(tradeNo, wrong), ErrCreemCheckoutBindingMismatch)

	injected := errors.New("injected Creem wallet audit failure")
	const callback = "test:creem_wallet_audit_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err = CompleteBoundCreemTopUpOrder(tradeNo, settlement)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var stored model.TopUp
	var storedUser model.User
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, 100, storedUser.Quota)

	require.NoError(t, CompleteBoundCreemTopUpOrder(tradeNo, settlement))
	require.NoError(t, CompleteBoundCreemTopUpOrder(tradeNo, settlement))
	wrong = settlement
	wrong.Currency = "eur"
	assert.ErrorIs(t, CompleteBoundCreemTopUpOrder(tradeNo, wrong), ErrCreemPaymentMismatch,
		"a successful order still rejects a forged replay")
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.Equal(t, 100+12_345, storedUser.Quota)
	var logs, outbox int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logs).Error)
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).Count(&outbox).Error)
	assert.Equal(t, int64(1), logs)
	assert.Equal(t, int64(1), outbox)
}

func TestCreemWalletOverflowRetainsPendingOrderForSafeRetry(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "creem-wallet-overflow", int(common.MaxQuota)-5, "default")
	product := creemWalletProduct()
	product.Quota = 10
	product.PriceText = "1.00"
	tradeNo := "ref_creem_wallet_overflow"
	order, _, err := CreateBoundCreemTopUpWithTradeNo(user.Id, product, tradeNo)
	require.NoError(t, err)
	require.NoError(t, BindCreemTopUpCheckout(tradeNo, "ch_wallet_overflow"))
	settlement := CreemSettlement{
		CheckoutID: "ch_wallet_overflow", ProviderOrderID: "ord_wallet_overflow", ReferenceID: tradeNo,
		ProductID: product.ProductID, AmountPaid: 100, Currency: "USD", OrderType: "onetime",
		MetadataReference: tradeNo, MetadataQuota: "10",
	}
	require.ErrorIs(t, CompleteBoundCreemTopUpOrder(tradeNo, settlement), ErrTopUpQuotaOverflow)
	var stored model.TopUp
	var storedUser model.User
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, int(common.MaxQuota)-5, storedUser.Quota)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", user.Id).Update("quota", 0).Error)
	require.NoError(t, CompleteBoundCreemTopUpOrder(tradeNo, settlement))
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, 10, storedUser.Quota)
}

func TestCreemSignedWebhookClaimsCheckoutAfterAmbiguousCreate(t *testing.T) {
	initSubDB(t)
	walletUser := subUser(t, "creem-ambiguous-wallet", 0, "default")
	walletOrder, _, err := CreateBoundCreemTopUpWithTradeNo(walletUser.Id, creemWalletProduct(), "ref_creem_ambiguous")
	require.NoError(t, err)
	walletSettlement := creemWalletSettlement(walletOrder, "ch_wallet_recovered")

	wrong := walletSettlement
	wrong.AmountPaid--
	require.ErrorIs(t, CompleteBoundCreemTopUpOrder(walletOrder.TradeNo, wrong), ErrCreemPaymentMismatch)
	var storedWallet model.TopUp
	require.NoError(t, model.DB.First(&storedWallet, walletOrder.Id).Error)
	assert.Nil(t, storedWallet.ProviderSessionId, "a mismatched webhook must not claim the checkout")
	require.NoError(t, CompleteBoundCreemTopUpOrder(walletOrder.TradeNo, walletSettlement))
	require.NoError(t, model.DB.First(&storedWallet, walletOrder.Id).Error)
	require.NotNil(t, storedWallet.ProviderSessionId)
	assert.Equal(t, walletSettlement.CheckoutID, *storedWallet.ProviderSessionId)
	assert.Equal(t, TopUpStatusSuccess, storedWallet.Status)

	subscriptionUser := subUser(t, "creem-ambiguous-subscription", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "20.00"
		plan.Currency = "USD"
		plan.CreemProductId = "prod_subscription_exact"
		plan.MaxPurchasePerUser = 1
	})
	subscriptionOrder, _, err := CreateBoundCreemSubscriptionOrder(
		subscriptionUser.Id, plan.Id, "sub_ref_creem_ambiguous")
	require.NoError(t, err)
	subscriptionSettlement := creemSubscriptionSettlement(subscriptionOrder, "ch_subscription_recovered")
	require.NoError(t, CompleteBoundCreemSubscriptionOrder(
		subscriptionOrder.TradeNo, "signed-payload", subscriptionSettlement))
	var storedSubscription model.SubscriptionOrder
	require.NoError(t, model.DB.First(&storedSubscription, subscriptionOrder.Id).Error)
	require.NotNil(t, storedSubscription.ProviderSessionId)
	assert.Equal(t, subscriptionSettlement.CheckoutID, *storedSubscription.ProviderSessionId)
	assert.Equal(t, TopUpStatusSuccess, storedSubscription.Status)
	assert.False(t, storedSubscription.CapacityReserved)
}

func creemSubscriptionSettlement(order *model.SubscriptionOrder, checkoutID string) CreemSettlement {
	return CreemSettlement{
		CheckoutID:        checkoutID,
		ProviderOrderID:   "ord_subscription_exact",
		ReferenceID:       order.TradeNo,
		ProductID:         "prod_subscription_exact",
		AmountPaid:        2000,
		Currency:          "USD",
		OrderType:         "recurring",
		MetadataReference: order.TradeNo,
		MetadataQuota:     "0",
	}
}

func TestCreemSubscriptionSnapshotsEntitlementAndSettlesIdempotently(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "creem-subscription", 0, "default")
	allowOverflow := false
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.Title = "Creem original"
		plan.PriceAmount = "20.00"
		plan.Currency = "USD"
		plan.CreemProductId = "prod_subscription_exact"
		plan.TotalAmount = 2222
		plan.DurationUnit = SubscriptionDurationDay
		plan.DurationValue = 2
		plan.UpgradeGroup = "vip"
		plan.AllowWalletOverflow = &allowOverflow
		plan.MaxPurchasePerUser = 1
	})
	legacy := model.SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: 20, TradeNo: "sub_ref_creem_legacy_unbound",
		PaymentMethod: PaymentMethodCreem, PaymentProvider: PaymentProviderCreem, Status: TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(&legacy).Error)
	assert.ErrorIs(t,
		CompleteSubscriptionOrder(legacy.TradeNo, "unbound", PaymentProviderCreem, ""),
		ErrCreemPaymentMismatch, "legacy Creem orders cannot bypass the bound settlement path")
	tradeNo := "sub_ref_creem_subscription_exact"
	order, request, err := CreateBoundCreemSubscriptionOrder(user.Id, plan.Id, tradeNo)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), order.ProviderAmountMinor)
	assert.Equal(t, "prod_subscription_exact", order.ProviderPriceId)
	assert.True(t, order.CapacityReserved)
	assert.NotEmpty(t, order.EntitlementSnapshot)
	assert.Equal(t, "0", request.Metadata["quota"])
	replayed, _, err := CreateBoundCreemSubscriptionOrder(user.Id, plan.Id, tradeNo)
	require.NoError(t, err)
	assert.Equal(t, order.Id, replayed.Id)
	_, _, err = CreateBoundCreemSubscriptionOrder(user.Id, plan.Id, "sub_ref_creem_other")
	assert.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)
	require.NoError(t, BindCreemSubscriptionCheckout(tradeNo, "ch_subscription_exact"))
	assert.ErrorIs(t, CompleteSubscriptionOrder(tradeNo, "legacy", PaymentProviderCreem, ""), ErrCreemPaymentMismatch)

	settlement := creemSubscriptionSettlement(order, "ch_subscription_exact")
	wrong := settlement
	wrong.AmountPaid = 1999
	assert.ErrorIs(t, CompleteBoundCreemSubscriptionOrder(tradeNo, "wrong", wrong), ErrCreemPaymentMismatch)
	wrong = settlement
	wrong.ProductID = "prod_other"
	assert.ErrorIs(t, CompleteBoundCreemSubscriptionOrder(tradeNo, "wrong", wrong), ErrCreemPaymentMismatch)

	// Fulfillment is driven by the checkout-time entitlement snapshot, not the
	// mutable plan row.
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"title": "mutated", "total_amount": int64(9999), "duration_value": 30,
		"upgrade_group": "pro", "allow_wallet_overflow": true, "creem_product_id": "prod_other",
	}).Error)
	require.NoError(t, CompleteBoundCreemSubscriptionOrder(tradeNo, "signed-payload", settlement))
	require.NoError(t, CompleteBoundCreemSubscriptionOrder(tradeNo, "duplicate-payload", settlement))
	wrong = settlement
	wrong.CheckoutID = "ch_other"
	assert.ErrorIs(t, CompleteBoundCreemSubscriptionOrder(tradeNo, "forged", wrong), ErrCreemCheckoutBindingMismatch)

	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.False(t, stored.CapacityReserved)
	assert.Equal(t, "signed-payload", stored.ProviderPayload)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", user.Id, plan.Id).First(&subscription).Error)
	assert.Equal(t, int64(2222), subscription.AmountTotal)
	assert.Equal(t, "vip", subscription.UpgradeGroup)
	assert.False(t, subscription.AllowWalletOverflow)
	assert.InDelta(t, 2*24*60*60, subscription.EndTime-subscription.StartTime, 2)
	var topup model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&topup).Error)
	assert.Equal(t, PaymentProviderCreem, topup.PaymentProvider)
	assert.Equal(t, PaymentMethodCreem, topup.PaymentMethod)
	assert.Equal(t, 20.0, topup.Money)
}

func TestConcurrentCreemWalletAndSubscriptionCallbacksHaveSingleEffect(t *testing.T) {
	initSubDB(t)
	walletUser := subUser(t, "creem-concurrent-wallet", 0, "default")
	walletOrder, _, err := CreateBoundCreemTopUpWithTradeNo(walletUser.Id, creemWalletProduct(), "ref_creem_concurrent")
	require.NoError(t, err)
	require.NoError(t, BindCreemTopUpCheckout(walletOrder.TradeNo, "ch_wallet_concurrent"))
	walletSettlement := creemWalletSettlement(walletOrder, "ch_wallet_concurrent")

	subUserRow := subUser(t, "creem-concurrent-sub", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.PriceAmount = "20.00"
		plan.Currency = "USD"
		plan.CreemProductId = "prod_subscription_exact"
		plan.MaxPurchasePerUser = 1
	})
	subOrder, _, err := CreateBoundCreemSubscriptionOrder(subUserRow.Id, plan.Id, "sub_ref_creem_concurrent")
	require.NoError(t, err)
	require.NoError(t, BindCreemSubscriptionCheckout(subOrder.TradeNo, "ch_subscription_concurrent"))
	subSettlement := creemSubscriptionSettlement(subOrder, "ch_subscription_concurrent")

	const workers = 4
	start := make(chan struct{})
	results := make(chan error, workers*2)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results <- CompleteBoundCreemTopUpOrder(walletOrder.TradeNo, walletSettlement)
		}()
		go func() {
			defer wg.Done()
			<-start
			results <- CompleteBoundCreemSubscriptionOrder(subOrder.TradeNo, "payload", subSettlement)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	var wallet model.User
	require.NoError(t, model.DB.First(&wallet, walletUser.Id).Error)
	assert.Equal(t, int(creemWalletProduct().Quota), wallet.Quota)
	var subscriptions, subscriptionTopups int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", subUserRow.Id).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", subOrder.TradeNo).Count(&subscriptionTopups).Error)
	assert.Equal(t, int64(1), subscriptions)
	assert.Equal(t, int64(1), subscriptionTopups)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).Count(&logs).Error)
	assert.Equal(t, int64(2), logs, fmt.Sprintf("one wallet and one subscription audit expected, got %d", logs))
}
