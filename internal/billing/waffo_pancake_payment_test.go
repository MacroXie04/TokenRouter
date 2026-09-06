package billing

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

const (
	servicePancakeMerchantID = "MER_AbCdEfGhIjKlMnOpQrStUv"
	servicePancakeStoreID    = "STO_AbCdEfGhIjKlMnOpQrStUv"
	servicePancakeProductID  = "PROD_AbCdEfGhIjKlMnOpQrStUv"
	servicePancakeOrderID    = "ORD_AbCdEfGhIjKlMnOpQrStUv"
)

func servicePancakeConfig(t *testing.T) setting.WaffoPancakeConfig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	config, err := setting.NewWaffoPancakeCredentialConfig(
		servicePancakeMerchantID, base64.StdEncoding.EncodeToString(der))
	require.NoError(t, err)
	config.StoreID = servicePancakeStoreID
	config.ProductID = servicePancakeProductID
	return config
}

func servicePancakeSession(id string) *WaffoPancakeCheckoutSession {
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	tokenExpires := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339Nano)
	token := "header.payload.signature"
	return &WaffoPancakeCheckoutSession{
		SessionID: id, CheckoutURL: "https://checkout.waffo.ai/session#token=" + token,
		ExpiresAt: expires, Token: token, TokenExpiresAt: tokenExpires,
	}
}

func servicePancakeSettlement(tradeNo string, userID int, amount string) WaffoPancakeSettlement {
	return WaffoPancakeSettlement{
		EventID: "evt_pancake_123", ProviderOrderID: servicePancakeOrderID,
		TradeNo: tradeNo, StoreID: servicePancakeStoreID, Mode: WaffoPancakeModeTest,
		BuyerIdentity: WaffoPancakeBuyerIdentityFromUserID(userID), Currency: "USD",
		Amount: amount, ProductName: "TokenRouter credits",
	}
}

func TestSaveWaffoPancakeConfigIsAtomicAndBlankKeyCannotChangeMerchant(t *testing.T) {
	initSubDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	previous := setting.GetOptions(
		setting.WaffoPancakeMerchantIDOption, setting.WaffoPancakePrivateKeyOption,
		setting.WaffoPancakeReturnURLOption, setting.WaffoPancakeStoreIDOption,
		setting.WaffoPancakeProductIDOption, setting.WaffoPancakeUnitPriceOption,
		setting.WaffoPancakeMinTopUpOption,
	)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	config := servicePancakeConfig(t)
	unitPrice := "2.5"
	minTopUp := "30"
	require.NoError(t, SaveWaffoPancakeConfigWithPricing(
		config.MerchantID, config.PrivateKey, "https://router.example.test/wallet",
		config.StoreID, config.ProductID, &unitPrice, &minTopUp,
	))

	err := SaveWaffoPancakeConfig(
		"MER_ZyXwVuTsRqPoNmLkJiHgFe", "", "https://router.example.test/other",
		config.StoreID, config.ProductID,
	)
	assert.ErrorIs(t, err, ErrWaffoPancakeCheckoutBindingMismatch)
	assert.Equal(t, config.MerchantID, setting.GetOption(setting.WaffoPancakeMerchantIDOption))
	assert.Equal(t, config.PrivateKey, setting.GetOption(setting.WaffoPancakePrivateKeyOption))

	invalidUnitPrice := "Infinity"
	changedMinimum := "40"
	err = SaveWaffoPancakeConfigWithPricing(
		config.MerchantID, "", "https://router.example.test/must-not-persist",
		config.StoreID, config.ProductID, &invalidUnitPrice, &changedMinimum,
	)
	require.Error(t, err)
	assert.Equal(t, "https://router.example.test/wallet", setting.GetOption(setting.WaffoPancakeReturnURLOption))
	assert.Equal(t, unitPrice, setting.GetOption(setting.WaffoPancakeUnitPriceOption))
	assert.Equal(t, minTopUp, setting.GetOption(setting.WaffoPancakeMinTopUpOption))
}

func TestWaffoPancakeWalletOrderBindsEconomicsAndCreditsOnce(t *testing.T) {
	initSubDB(t)
	config := servicePancakeConfig(t)
	user := subUser(t, "pancake-wallet", 100, "default")
	require.NoError(t, model.DB.Model(user).Update("email", "wallet@example.test").Error)
	tradeNo := "WAFFO_PANCAKE-1-1700000000000-ABC123"
	order, checkout, err := CreateBoundWaffoPancakeTopUp(user.Id, 10, 10, "12.34", config, tradeNo)
	require.NoError(t, err)
	assert.Equal(t, PaymentProviderWaffoPancake, order.PaymentProvider)
	assert.Equal(t, PaymentMethodWaffoPancake, order.PaymentMethod)
	assert.Equal(t, int64(1234), order.ProviderAmountMinor)
	assert.Equal(t, int64(10*quotamath.QuotaPerUnit), order.CreditQuota)
	assert.Equal(t, tradeNo, checkout.OrderMerchantExternalID)
	assert.Equal(t, WaffoPancakeBuyerIdentityFromUserID(user.Id), checkout.BuyerIdentity)
	assert.Equal(t, "wallet@example.test", checkout.BuyerEmail)
	assert.NotEmpty(t, order.CheckoutFingerprint)

	assert.ErrorIs(t, CompleteTopUp(user.Id, tradeNo, order.Amount), ErrWaffoPancakePaymentMismatch,
		"generic completion must not bypass Pancake evidence")
	session := servicePancakeSession("chs_wallet_exact")
	require.NoError(t, BindWaffoPancakeTopUpCheckout(tradeNo, session))
	require.NoError(t, BindWaffoPancakeTopUpCheckout(tradeNo, session))
	assert.ErrorIs(t, BindWaffoPancakeTopUpCheckout(tradeNo, servicePancakeSession("chs_wallet_other")),
		ErrWaffoPancakeCheckoutBindingMismatch)

	settlement := servicePancakeSettlement(tradeNo, user.Id, "12.34")
	for name, mutate := range map[string]func(*WaffoPancakeSettlement){
		"amount":   func(value *WaffoPancakeSettlement) { value.Amount = "12.35" },
		"currency": func(value *WaffoPancakeSettlement) { value.Currency = "EUR" },
		"identity": func(value *WaffoPancakeSettlement) { value.BuyerIdentity = "tokenrouter-user-999" },
		"store":    func(value *WaffoPancakeSettlement) { value.StoreID = "STO_ZbCdEfGhIjKlMnOpQrStUv" },
		"trade":    func(value *WaffoPancakeSettlement) { value.TradeNo = "WAFFO_PANCAKE-9-forged" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := settlement
			mutate(&forged)
			assert.Error(t, CompleteBoundWaffoPancakeTopUpOrder(forged))
		})
	}

	injected := errors.New("injected Pancake wallet audit failure")
	const callback = "test:waffo_pancake_wallet_audit_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err = CompleteBoundWaffoPancakeTopUpOrder(settlement)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var stored model.TopUp
	var storedUser model.User
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, 100, storedUser.Quota)

	require.NoError(t, CompleteBoundWaffoPancakeTopUpOrder(settlement))
	require.NoError(t, CompleteBoundWaffoPancakeTopUpOrder(settlement))
	forgedReplay := settlement
	forgedReplay.Amount = "12.35"
	assert.ErrorIs(t, CompleteBoundWaffoPancakeTopUpOrder(forgedReplay), ErrWaffoPancakePaymentMismatch)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, 100+10*quotamath.QuotaPerUnit, storedUser.Quota)
	var logCount int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestWaffoPancakeNewOrderRejectsOversizedProviderAmount(t *testing.T) {
	initSubDB(t)
	config := servicePancakeConfig(t)
	user := subUser(t, "pancake-oversized", 0, "default")
	_, _, err := CreateBoundWaffoPancakeTopUp(
		user.Id, 1, 1, "1000000.00", config, "WAFFO_PANCAKE-oversized-1700000000000-ABC123")
	assert.ErrorIs(t, err, ErrWaffoPancakePaymentMismatch)

	// Settlement parsing remains backward compatible for an already-created
	// legacy snapshot above today's creation ceiling.
	_, normalized, _, err := normalizeWaffoPancakeOrderMoney("1000000.00")
	require.NoError(t, err)
	assert.Equal(t, "1000000.00", normalized)
}

func TestWaffoPancakeSubscriptionReservesCapacityAndUsesImmutableEntitlement(t *testing.T) {
	initSubDB(t)
	config := servicePancakeConfig(t)
	user := subUser(t, "pancake-subscription", 0, "default")
	plan := &model.SubscriptionPlan{
		Title: "Pancake Pro", PriceAmount: "12.345000", Currency: "USD", Enabled: true,
		DurationUnit: "day", DurationValue: 1, TotalAmount: 777,
		WaffoPancakeProductId: servicePancakeProductID, MaxPurchasePerUser: 1,
	}
	require.NoError(t, CreateSubscriptionPlan(plan))
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-1700000000000-ABC123", user.Id)
	order, checkout, err := CreateBoundWaffoPancakeSubscriptionOrder(user.Id, plan.Id, config, tradeNo)
	require.NoError(t, err)
	assert.True(t, order.CapacityReserved)
	assert.Equal(t, int64(1235), order.ProviderAmountMinor)
	assert.Equal(t, "12.35", checkout.PriceSnapshot.Amount)
	assert.Equal(t, servicePancakeProductID, order.ProviderPriceId)
	_, _, err = CreateBoundWaffoPancakeSubscriptionOrder(user.Id, plan.Id, config,
		fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-1700000000001-DEF456", user.Id))
	assert.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)

	assert.ErrorIs(t, CompleteSubscriptionOrder(tradeNo, "payload", PaymentProviderWaffoPancake, ""),
		ErrWaffoPancakePaymentMismatch)
	session := servicePancakeSession("chs_subscription_exact")
	require.NoError(t, BindWaffoPancakeSubscriptionCheckout(tradeNo, session))
	require.NoError(t, BindWaffoPancakeSubscriptionCheckout(tradeNo, session))
	assert.ErrorIs(t, BindWaffoPancakeSubscriptionCheckout(tradeNo, servicePancakeSession("chs_subscription_other")),
		ErrWaffoPancakeCheckoutBindingMismatch)

	// Paid fulfillment uses the captured entitlement, not this mutable row.
	require.NoError(t, model.DB.Model(plan).Updates(map[string]any{
		"total_amount": 999999, "duration_value": 30, "title": "mutated",
	}).Error)
	settlement := servicePancakeSettlement(tradeNo, user.Id, "12.35")
	injected := errors.New("injected Pancake subscription audit failure")
	const callback = "test:waffo_pancake_subscription_audit_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err = CompleteBoundWaffoPancakeSubscriptionOrder(tradeNo, `{"signed":true}`, settlement)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var subscriptionCount int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", user.Id).Count(&subscriptionCount).Error)
	assert.Zero(t, subscriptionCount)
	var storedOrder model.SubscriptionOrder
	require.NoError(t, model.DB.First(&storedOrder, order.Id).Error)
	assert.Equal(t, TopUpStatusPending, storedOrder.Status)
	assert.True(t, storedOrder.CapacityReserved)

	require.NoError(t, CompleteBoundWaffoPancakeSubscriptionOrder(tradeNo, `{"signed":true}`, settlement))
	require.NoError(t, CompleteBoundWaffoPancakeSubscriptionOrder(tradeNo, `{"signed":true}`, settlement))
	forged := settlement
	forged.Amount = "12.34"
	assert.ErrorIs(t, CompleteBoundWaffoPancakeSubscriptionOrder(tradeNo, `{"signed":true}`, forged),
		ErrWaffoPancakePaymentMismatch)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&subscription).Error)
	assert.Equal(t, int64(777), subscription.AmountTotal)
	assert.LessOrEqual(t, subscription.EndTime-subscription.StartTime, int64(2*24*time.Hour/time.Second))
	require.NoError(t, model.DB.First(&storedOrder, order.Id).Error)
	assert.False(t, storedOrder.CapacityReserved)
	assert.Equal(t, TopUpStatusSuccess, storedOrder.Status)
}

func TestConcurrentWaffoPancakeCallbacksCreditWalletOnce(t *testing.T) {
	initSubDB(t)
	config := servicePancakeConfig(t)
	user := subUser(t, "pancake-concurrent", 0, "default")
	tradeNo := "WAFFO_PANCAKE-3-1700000000000-ABC123"
	_, _, err := CreateBoundWaffoPancakeTopUp(user.Id, 1, 1, "1.00", config, tradeNo)
	require.NoError(t, err)
	settlement := servicePancakeSettlement(tradeNo, user.Id, "1.00")
	start := make(chan struct{})
	results := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- CompleteBoundWaffoPancakeTopUpOrder(settlement)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	var stored model.User
	require.NoError(t, model.DB.First(&stored, user.Id).Error)
	assert.Equal(t, quotamath.QuotaPerUnit, stored.Quota)
}
