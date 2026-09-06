package billing

import (
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sync"
	"testing"
)

func waffoCheckoutSpec(userID int) WaffoCheckoutSpec {
	return WaffoCheckoutSpec{
		UserID: userID, Amount: 10, RequestedAmount: 10,
		OrderAmount: "12.34", Currency: "USD", MerchantID: "merchant_123",
		NotifyURL: "https://merchant.example.test/api/waffo/webhook",
		ReturnURL: "https://merchant.example.test/wallet?show_history=true",
		AppName:   "TokenRouter", PayMethodType: "APPLEPAY", PayMethodName: "APPLEPAY",
		RequestedAt: "2026-09-05T01:02:03.000Z", Sandbox: true,
	}
}

func waffoSettlement(order *model.TopUp, acquiringOrderID string) WaffoSettlement {
	return WaffoSettlement{
		PaymentRequestID: order.TradeNo, MerchantOrderID: order.TradeNo,
		AcquiringOrderID: acquiringOrderID, OrderAmount: "12.34", OrderCurrency: "usd",
		MerchantID: "merchant_123", UserID: fmt.Sprintf("%d", order.UserId),
		ProductName: WaffoProductOneTimePayment, Environment: WaffoEnvironmentSandbox,
	}
}

func TestWaffoMoneyFormattingAndBounds(t *testing.T) {
	amount, err := FormatWaffoAmount(12.6, "jpy")
	require.NoError(t, err)
	assert.Equal(t, "13", amount)
	minor, err := WaffoMoneyToMinorUnits(amount, "JPY")
	require.NoError(t, err)
	assert.Equal(t, int64(13), minor)

	amount, err = FormatWaffoAmount(12.6, "usd")
	require.NoError(t, err)
	assert.Equal(t, "12.60", amount)
	minor, err = WaffoMoneyToMinorUnits(amount, "USD")
	require.NoError(t, err)
	assert.Equal(t, int64(1260), minor)

	for _, test := range []struct{ amount, currency string }{
		{"1.1", "JPY"}, {"-1", "USD"}, {"1e3", "USD"}, {"1", "US$"},
		{string(make([]byte, maxWaffoMoneyTextLength+1)), "USD"},
	} {
		_, err := WaffoMoneyToMinorUnits(test.amount, test.currency)
		assert.Error(t, err, "%q %q", test.amount, test.currency)
	}
	_, err = FormatWaffoAmount(0.004, "USD")
	assert.ErrorIs(t, err, ErrTopUpPricingInvalid)
}

func TestWaffoWalletOrderSnapshotBindingRetryAndIdempotentCredit(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "waffo-wallet", 100, "default")
	tradeNo := "WAFFO-1-1700000000000-ABC123"
	order, request, err := CreateBoundWaffoTopUp(waffoCheckoutSpec(user.Id), tradeNo)
	require.NoError(t, err)
	assert.Equal(t, TopUpStatusPending, order.Status)
	assert.Equal(t, PaymentProviderWaffo, order.PaymentProvider)
	assert.Equal(t, PaymentMethodWaffo, order.PaymentMethod)
	assert.Equal(t, int64(10*quotamath.QuotaPerUnit), order.CreditQuota)
	assert.Equal(t, WaffoTopUpCreditQuotaVersion, order.CreditQuotaVersion)
	assert.Equal(t, int64(1234), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)
	assert.Equal(t, WaffoEnvironmentSandbox, order.ProviderMode)
	assert.Equal(t, tradeNo, order.ProviderCreateIdempotencyKey)
	assert.NotEmpty(t, order.CheckoutRequest)
	assert.NotEmpty(t, order.CheckoutFingerprint)
	assert.Equal(t, tradeNo, request.PaymentRequestID)
	assert.Equal(t, "APPLEPAY", request.PaymentInfo.PayMethodType)

	binding := WaffoCreateBinding{PaymentRequestID: tradeNo, MerchantOrderID: tradeNo, AcquiringOrderID: "acq_wallet_123"}
	require.NoError(t, BindWaffoTopUpCheckout(tradeNo, binding))
	require.NoError(t, BindWaffoTopUpCheckout(tradeNo, binding))
	wrongBinding := binding
	wrongBinding.AcquiringOrderID = "acq_wallet_other"
	assert.ErrorIs(t, BindWaffoTopUpCheckout(tradeNo, wrongBinding), ErrWaffoCheckoutBindingMismatch)

	settlement := waffoSettlement(order, binding.AcquiringOrderID)
	mutations := []func(*WaffoSettlement){
		func(value *WaffoSettlement) { value.OrderAmount = "12.33" },
		func(value *WaffoSettlement) { value.OrderCurrency = "EUR" },
		func(value *WaffoSettlement) { value.PaymentRequestID = "WAFFO-forged" },
		func(value *WaffoSettlement) { value.AcquiringOrderID = "acq_forged" },
		func(value *WaffoSettlement) { value.MerchantID = "merchant_forged" },
		func(value *WaffoSettlement) { value.UserID = "999" },
		func(value *WaffoSettlement) { value.ProductName = "SUBSCRIPTION" },
		func(value *WaffoSettlement) { value.Environment = WaffoEnvironmentProduction },
	}
	for _, mutate := range mutations {
		wrong := settlement
		mutate(&wrong)
		assert.Error(t, CompleteBoundWaffoTopUpOrder(wrong))
	}

	injected := errors.New("injected Waffo audit failure")
	const callback = "test:waffo_wallet_audit_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	err = CompleteBoundWaffoTopUpOrder(settlement)
	require.ErrorIs(t, err, injected)
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var stored model.TopUp
	var storedUser model.User
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusPending, stored.Status)
	assert.Equal(t, 100, storedUser.Quota)

	require.NoError(t, CompleteBoundWaffoTopUpOrder(settlement))
	require.NoError(t, CompleteBoundWaffoTopUpOrder(settlement))
	forgedReplay := settlement
	forgedReplay.OrderAmount = "99.99"
	assert.ErrorIs(t, CompleteBoundWaffoTopUpOrder(forgedReplay), ErrWaffoPaymentMismatch)
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)
	assert.Equal(t, 100+10*quotamath.QuotaPerUnit, storedUser.Quota)
	var logs, outbox int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logs).Error)
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).Count(&outbox).Error)
	assert.Equal(t, int64(1), logs)
	assert.Equal(t, int64(1), outbox)
}

func TestWaffoSignedSettlementClaimsAmbiguousCreateAndClosesTerminalOrder(t *testing.T) {
	initSubDB(t)
	paidUser := subUser(t, "waffo-ambiguous", 0, "default")
	paidOrder, _, err := CreateBoundWaffoTopUp(waffoCheckoutSpec(paidUser.Id), "WAFFO-2-1700000000000-ABC123")
	require.NoError(t, err)
	assert.Nil(t, paidOrder.ProviderSessionId)
	settlement := waffoSettlement(paidOrder, "acq_claimed_from_webhook")
	require.NoError(t, CompleteBoundWaffoTopUpOrder(settlement))
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, paidOrder.Id).Error)
	require.NotNil(t, stored.ProviderSessionId)
	assert.Equal(t, "acq_claimed_from_webhook", *stored.ProviderSessionId)
	assert.Equal(t, TopUpStatusSuccess, stored.Status)

	closedUser := subUser(t, "waffo-closed", 50, "default")
	closedOrder, _, err := CreateBoundWaffoTopUp(waffoCheckoutSpec(closedUser.Id), "WAFFO-3-1700000000000-ABC123")
	require.NoError(t, err)
	closed := waffoSettlement(closedOrder, "acq_closed")
	require.NoError(t, CloseBoundWaffoTopUpOrder(closed))
	require.NoError(t, CloseBoundWaffoTopUpOrder(closed))
	stored = model.TopUp{}
	require.NoError(t, model.DB.First(&stored, closedOrder.Id).Error)
	assert.Equal(t, TopUpStatusFailed, stored.Status)
	var userAfter model.User
	require.NoError(t, model.DB.First(&userAfter, closedUser.Id).Error)
	assert.Equal(t, 50, userAfter.Quota)
}

func TestWaffoSettlementFailsClosedOnLegacyOrTamperedSnapshot(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "waffo-tamper", 0, "default")
	legacy, err := CreateTopUpWithTradeNo(user.Id, 10, 12.34, PaymentMethodWaffo, PaymentProviderWaffo, "WAFFO-legacy")
	require.NoError(t, err)
	assert.ErrorIs(t, CompleteBoundWaffoTopUpOrder(waffoSettlement(legacy, "acq_legacy")), ErrWaffoLegacyOrderRequiresReview)

	order, _, err := CreateBoundWaffoTopUp(waffoCheckoutSpec(user.Id), "WAFFO-4-1700000000000-ABC123")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("id = ?", order.Id).
		Update("checkout_request", order.CheckoutRequest+" ").Error)
	assert.ErrorIs(t, CompleteBoundWaffoTopUpOrder(waffoSettlement(order, "acq_tampered")), ErrWaffoCheckoutBindingMismatch)
}

func TestConcurrentWaffoCallbacksCreditOnce(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "waffo-concurrent", 0, "default")
	order, _, err := CreateBoundWaffoTopUp(waffoCheckoutSpec(user.Id), "WAFFO-5-1700000000000-ABC123")
	require.NoError(t, err)
	require.NoError(t, BindWaffoTopUpCheckout(order.TradeNo, WaffoCreateBinding{
		PaymentRequestID: order.TradeNo, MerchantOrderID: order.TradeNo, AcquiringOrderID: "acq_concurrent",
	}))
	settlement := waffoSettlement(order, "acq_concurrent")

	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- CompleteBoundWaffoTopUpOrder(settlement)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	var storedUser model.User
	require.NoError(t, model.DB.First(&storedUser, user.Id).Error)
	assert.Equal(t, 10*quotamath.QuotaPerUnit, storedUser.Quota)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", user.Id).Count(&logs).Error)
	assert.Equal(t, int64(1), logs)
}

func TestCreateBoundWaffoTopUpRejectsInvalidSnapshotInputs(t *testing.T) {
	initSubDB(t)
	user := subUser(t, "waffo-invalid", 0, "default")
	base := waffoCheckoutSpec(user.Id)
	tests := []func(*WaffoCheckoutSpec){
		func(spec *WaffoCheckoutSpec) { spec.Amount = 0 },
		func(spec *WaffoCheckoutSpec) { spec.OrderAmount = "NaN" },
		func(spec *WaffoCheckoutSpec) { spec.OrderAmount = "1000000.00" },
		func(spec *WaffoCheckoutSpec) { spec.Currency = "US$" },
		func(spec *WaffoCheckoutSpec) { spec.NotifyURL = "javascript:alert(1)" },
		func(spec *WaffoCheckoutSpec) { spec.ReturnURL = "https://user@example.test/wallet" },
		func(spec *WaffoCheckoutSpec) { spec.RequestedAt = "not-a-time" },
		func(spec *WaffoCheckoutSpec) { spec.AppName = "" },
	}
	for index, mutate := range tests {
		spec := base
		mutate(&spec)
		_, _, err := CreateBoundWaffoTopUp(spec, fmt.Sprintf("WAFFO-invalid-%d", index))
		assert.Error(t, err)
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	assert.Zero(t, count)
}
