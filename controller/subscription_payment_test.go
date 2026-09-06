package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// setupSubscriptionStripeTest provisions a root session with the
// subscription + payment tables and a Stripe test plan.
func setupSubscriptionStripeTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int, *model.SubscriptionPlan) {
	t.Helper()
	handler, do, uid := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{},
		&model.SubscriptionOrder{}, &model.TopUp{}))
	setPaymentCompliance(t, true)
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://localhost:3000"))
	service.SetGroupRatios(map[string]float64{"default": 1.0, "vip": 2.0})

	plan := model.SubscriptionPlan{
		Title: "Pro", PriceAmount: "40.00", Enabled: true, StripePriceId: "price_sub_x",
		DurationUnit: "month", DurationValue: 1, QuotaResetPeriod: "never",
		TotalAmount: 100000, MaxPurchasePerUser: 2,
	}
	require.NoError(t, model.DB.Create(&plan).Error)
	return handler, do, uid, &plan
}

func installSubscriptionStripeMock(t *testing.T) *stripeMock {
	t.Helper()
	mock := newStripeMock(t, "")
	mock.priceResponse = `{"id":"price_sub_x","object":"price","active":true,"billing_scheme":"per_unit","currency":"usd","type":"one_time","unit_amount":4000,"unit_amount_decimal":"4000"}`
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(mock.server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })
	return mock
}

func TestSubscriptionStripePayContract(t *testing.T) {
	_, do, uid, plan := setupSubscriptionStripeTest(t)

	// Compliance gate: payment compliance not confirmed.
	setPaymentCompliance(t, false)
	rec := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, false, decodeBody(t, rec)["success"])
	setPaymentCompliance(t, true)

	// plan_id missing/invalid.
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", `{"plan_id":0}`)
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "参数错误", body["message"])

	// Disabled plan.
	disabled := model.SubscriptionPlan{Title: "Off", PriceAmount: "1.00", Enabled: false,
		StripePriceId: "price_off", DurationUnit: "month", DurationValue: 1,
		QuotaResetPeriod: "never", TotalAmount: 10}
	require.NoError(t, model.DB.Create(&disabled).Error)
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, disabled.Id))
	assert.Equal(t, "套餐未启用", decodeBody(t, rec)["message"])

	// Plan without a Stripe price id.
	noPrice := model.SubscriptionPlan{Title: "NoPrice", PriceAmount: "1.00", Enabled: true,
		DurationUnit: "month", DurationValue: 1, QuotaResetPeriod: "never", TotalAmount: 10}
	require.NoError(t, model.DB.Create(&noPrice).Error)
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, noPrice.Id))
	assert.Equal(t, "该套餐未配置 StripePriceId", decodeBody(t, rec)["message"])

	// Stripe key and webhook secret gates.
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "Stripe 未配置或密钥无效", decodeBody(t, rec)["message"])
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_abc")
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "Stripe Webhook 未配置", decodeBody(t, rec)["message"])
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	mock := installSubscriptionStripeMock(t)
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://payments.example.test"))
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "Stripe 回调地址配置无效", decodeBody(t, rec)["message"])
	var unsafeOrderCount int64
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", uid).Count(&unsafeOrderCount).Error)
	assert.Zero(t, unsafeOrderCount)
	assert.Zero(t, mock.requestCount(), "invalid defaults must fail before contacting Stripe")
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://localhost:3000"))

	// Purchase cap: two existing subscriptions for this user exhaust the plan.
	require.NoError(t, model.DB.Create(&model.UserSubscription{
		UserId: uid, PlanId: plan.Id, AmountTotal: 100000, Status: "active", Source: "test",
	}).Error)
	require.NoError(t, model.DB.Create(&model.UserSubscription{
		UserId: uid, PlanId: plan.Id, AmountTotal: 100000, Status: "active", Source: "test",
	}).Error)
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "已达到该套餐购买上限", decodeBody(t, rec)["message"])
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", uid, plan.Id).
		Delete(&model.UserSubscription{}).Error)

	// Happy path: checkout session + pending order with the reference shapes.
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://checkout.stripe.com/c/pay/cs_test_1", data["pay_link"])

	form := mock.lastRequestBody()
	assert.Equal(t, "payment", form["mode"])
	assert.Equal(t, "price_sub_x", form["line_items[0][price]"])
	assert.Equal(t, "1", form["line_items[0][quantity]"])
	refID, _ := form["client_reference_id"].(string)
	assert.True(t, strings.HasPrefix(refID, "sub_ref_"), "reference id: %v", refID)
	assert.Empty(t, form["customer_creation"])
	assert.Equal(t, "price_sub_x", form["metadata[price_id]"])

	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&order).Error)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.Equal(t, service.PaymentProviderStripe, order.PaymentProvider)
	assert.Equal(t, service.PaymentMethodStripe, order.PaymentMethod)
	assert.Equal(t, 40.0, order.Money)
	assert.Equal(t, int64(4000), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)
	assert.Equal(t, "price_sub_x", order.ProviderPriceId)
	assert.Equal(t, service.StripeSubscriptionCheckoutBindingVersion, order.ProviderBindingVersion)
	assert.True(t, order.CapacityReserved)
	assert.NotEmpty(t, order.EntitlementSnapshot)
	require.NotNil(t, order.ProviderSessionId)
	assert.Equal(t, "cs_test_1", *order.ProviderSessionId)
	assert.Equal(t, plan.Id, order.PlanId)
	assert.Equal(t, uid, order.UserId)
}

func TestSubscriptionStripeOrderEntropyFailureDoesNotCreateOrder(t *testing.T) {
	_, do, uid, plan := setupSubscriptionStripeTest(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_entropy")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_entropy")
	restore := common.SetSecureRandomReaderForTesting(paymentEntropyFailureReader{})
	t.Cleanup(restore)

	rec := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "error", decodeBody(t, rec)["message"])
	var orderCount, subscriptionCount int64
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", uid).Count(&orderCount).Error)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Count(&subscriptionCount).Error)
	assert.Zero(t, orderCount)
	assert.Zero(t, subscriptionCount)
}

func TestSubscriptionStripeWebhookCompletion(t *testing.T) {
	handler, do, uid, plan := setupSubscriptionStripeTest(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_abc")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	mock := installSubscriptionStripeMock(t)
	rec := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	refID := mock.lastRequestBody()["client_reference_id"].(string)

	sendWebhook := func(eventType string, object string) *httptest.ResponseRecorder {
		payload := []byte(fmt.Sprintf(`{"id":"evt_1","type":%q,"data":{"object":%s}}`, eventType, object))
		req := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Stripe-Signature", signStripe(payload, "whsec_test", currentStripeTimestamp()))
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}

	// Completed event fulfills the order: subscription created, top-up row
	// recorded, order marked success, system log written.
	out := sendWebhook("checkout.session.completed",
		fmt.Sprintf(`{"id":"cs_test_1","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","customer":"cus_1","amount_total":4000,"currency":"eur","metadata":{"order_type":"subscription","trade_no":%q,"price_id":"price_sub_x"}}`, refID, refID))
	require.Equal(t, http.StatusServiceUnavailable, out.Code, out.Body.String())
	var pendingOrder model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&pendingOrder).Error)
	assert.Equal(t, service.TopUpStatusPending, pendingOrder.Status)
	var pendingSubscriptions int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ? AND plan_id = ?", uid, plan.Id).Count(&pendingSubscriptions).Error)
	assert.Zero(t, pendingSubscriptions)

	out = sendWebhook("checkout.session.completed",
		fmt.Sprintf(`{"id":"cs_test_1","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","customer":"cus_1","amount_total":4000,"currency":"usd","metadata":{"order_type":"subscription","trade_no":%q,"price_id":"price_sub_x"}}`, refID, refID))
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())

	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&order).Error)
	assert.Equal(t, service.TopUpStatusSuccess, order.Status)
	assert.NotZero(t, order.CompleteTime)
	assert.Contains(t, order.ProviderPayload, "amount_total")

	var count int64
	model.DB.Model(&model.UserSubscription{}).Where("user_id = ? AND plan_id = ?", uid, plan.Id).Count(&count)
	assert.Equal(t, int64(1), count, "exactly one subscription created")
	var sub model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", uid, plan.Id).First(&sub).Error)
	assert.Equal(t, "order", sub.Source)
	assert.Equal(t, int64(100000), sub.AmountTotal)
	var paidUser model.User
	require.NoError(t, model.DB.First(&paidUser, uid).Error)
	assert.Equal(t, "cus_1", paidUser.StripeCustomer)

	var topup model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&topup).Error)
	assert.Equal(t, service.TopUpStatusSuccess, topup.Status)
	assert.Equal(t, 40.0, topup.Money)
	assert.Equal(t, service.PaymentMethodStripe, topup.PaymentMethod)

	// Duplicate delivery is idempotent.
	out = sendWebhook("checkout.session.completed",
		fmt.Sprintf(`{"id":"cs_test_1","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","customer":"cus_1","amount_total":4000,"currency":"usd","metadata":{"order_type":"subscription","trade_no":%q,"price_id":"price_sub_x"}}`, refID, refID))
	require.Equal(t, http.StatusOK, out.Code)
	model.DB.Model(&model.UserSubscription{}).Where("user_id = ? AND plan_id = ?", uid, plan.Id).Count(&count)
	assert.Equal(t, int64(1), count, "no second subscription on duplicate delivery")

	// Expired event marks a fresh pending order expired.
	rec2 := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	ref2 := mock.lastRequestBody()["client_reference_id"].(string)
	out = sendWebhook("checkout.session.expired",
		fmt.Sprintf(`{"id":"cs_test_2","client_reference_id":%q,"mode":"payment","status":"expired","amount_total":4000,"currency":"usd","metadata":{"order_type":"subscription","trade_no":%q,"price_id":"price_sub_x"}}`, ref2, ref2))
	require.Equal(t, http.StatusOK, out.Code)
	var order2 model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", ref2).First(&order2).Error)
	assert.Equal(t, service.TopUpStatusExpired, order2.Status)

	// Expiring an already-successful order is a no-op.
	out = sendWebhook("checkout.session.expired",
		fmt.Sprintf(`{"id":"cs_test_1","client_reference_id":%q,"mode":"payment","status":"expired","amount_total":4000,"currency":"usd","metadata":{"order_type":"subscription","trade_no":%q,"price_id":"price_sub_x"}}`, refID, refID))
	require.Equal(t, http.StatusOK, out.Code)
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&order).Error)
	assert.Equal(t, service.TopUpStatusSuccess, order.Status)
}

func TestSubscriptionStripePersistsBeforeCheckoutAndMarksProviderFailure(t *testing.T) {
	_, do, uid, plan := setupSubscriptionStripeTest(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_subscription_order_first")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	var calls atomic.Int32
	var sawPending atomic.Bool
	var providerTradeNo atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/prices/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"price_sub_x","object":"price","active":true,"billing_scheme":"per_unit","currency":"usd","type":"one_time","unit_amount":4000,"unit_amount_decimal":"4000"}`))
			return
		}
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tradeNo := r.Form.Get("client_reference_id")
		providerTradeNo.Store(tradeNo)
		var order model.SubscriptionOrder
		if err := model.DB.Where("trade_no = ?", tradeNo).First(&order).Error; err == nil && order.Status == service.TopUpStatusPending {
			sawPending.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"injected checkout failure"}}`))
	}))
	t.Cleanup(server.Close)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

	injected := fmt.Errorf("injected subscription order create failure")
	const callback = "test:fail_stripe_subscription_order_create"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription_orders" {
			tx.AddError(injected)
		}
	}))
	rec := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "创建订单失败", decodeBody(t, rec)["data"])
	assert.Zero(t, calls.Load(), "Checkout must not be opened when the local order cannot be persisted")
	require.NoError(t, model.DB.Callback().Create().Remove(callback))

	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "拉起支付失败", decodeBody(t, rec)["data"])
	assert.Equal(t, int32(1), calls.Load())
	assert.True(t, sawPending.Load(), "provider must observe an already-persisted pending order")
	tradeNo, ok := providerTradeNo.Load().(string)
	require.True(t, ok)
	var failed model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ? AND user_id = ?", tradeNo, uid).First(&failed).Error)
	assert.Equal(t, service.TopUpStatusFailed, failed.Status)
	assert.NotZero(t, failed.CompleteTime)
	assert.False(t, failed.CapacityReserved)
	assert.Empty(t, failed.ReconciliationState, "a definite Stripe rejection is terminal, not ambiguous")
}

func TestSubscriptionStripeRejectsUntrustedPriceBeforeReservingOrder(t *testing.T) {
	tests := []struct {
		name          string
		priceResponse string
	}{
		{
			name:          "inactive",
			priceResponse: `{"id":"price_sub_x","active":false,"billing_scheme":"per_unit","currency":"usd","type":"one_time","unit_amount":4000}`,
		},
		{
			name:          "wrong amount",
			priceResponse: `{"id":"price_sub_x","active":true,"billing_scheme":"per_unit","currency":"usd","type":"one_time","unit_amount":3999}`,
		},
		{
			name:          "recurring price",
			priceResponse: `{"id":"price_sub_x","active":true,"billing_scheme":"per_unit","currency":"usd","recurring":{"interval":"month","interval_count":1,"usage_type":"licensed"},"type":"recurring","unit_amount":4000}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, do, uid, plan := setupSubscriptionStripeTest(t)
			t.Setenv("STRIPE_SECRET_KEY", "sk_test_price_validation")
			t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
			mock := newStripeMock(t, "")
			mock.priceResponse = test.priceResponse
			stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(mock.server.URL)}))
			t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

			rec := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
			assert.Equal(t, "Stripe Price 与套餐配置不一致", decodeBody(t, rec)["data"])
			var orders int64
			require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", uid).Count(&orders).Error)
			assert.Zero(t, orders)
			assert.Nil(t, mock.lastRequestBody(), "Checkout must not run for an invalid Price")
		})
	}
}

func TestSubscriptionStripeAmbiguousCheckoutOutcomeKeepsReservationPending(t *testing.T) {
	_, do, uid, plan := setupSubscriptionStripeTest(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_subscription_ambiguous")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"price_sub_x","active":true,"billing_scheme":"per_unit","currency":"usd","type":"one_time","unit_amount":4000,"unit_amount_decimal":"4000"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"temporary"}}`))
	}))
	t.Cleanup(server.Close)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

	rec := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	assert.Equal(t, "拉起支付失败", decodeBody(t, rec)["data"])
	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("user_id = ?", uid).Order("id desc").First(&order).Error)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.True(t, order.CapacityReserved)
	assert.Zero(t, order.CompleteTime)
	assert.Equal(t, service.StripeReconciliationCreationUnknown, order.ReconciliationState)
}
