package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

// setupSubscriptionStripeTest provisions a root session with the
// subscription + payment tables and a Stripe test plan.
func setupSubscriptionStripeTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int, *model.SubscriptionPlan) {
	t.Helper()
	handler, do, uid := setupChannelRead(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionPlan{}, &model.UserSubscription{},
		&model.SubscriptionOrder{}, &model.TopUp{}))
	setPaymentCompliance(t, true)
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
	mock := newStripeMock(t, `{"id":"cs_sub","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_sub"}`)
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
	mock := installSubscriptionStripeMock(t)
	rec = do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://checkout.stripe.com/c/pay/cs_sub", data["pay_link"])

	form := mock.lastRequestBody()
	assert.Equal(t, "subscription", form["mode"])
	assert.Equal(t, "price_sub_x", form["line_items[0][price]"])
	assert.Equal(t, "1", form["line_items[0][quantity]"])
	refID, _ := form["client_reference_id"].(string)
	assert.True(t, strings.HasPrefix(refID, "sub_ref_"), "reference id: %v", refID)
	assert.Equal(t, "always", form["customer_creation"])

	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&order).Error)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.Equal(t, service.PaymentProviderStripe, order.PaymentProvider)
	assert.Equal(t, service.PaymentMethodStripe, order.PaymentMethod)
	assert.Equal(t, 40.0, order.Money)
	assert.Equal(t, plan.Id, order.PlanId)
	assert.Equal(t, uid, order.UserId)
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
		req.Header.Set("Stripe-Signature", signStripe(payload, "whsec_test", "1700000000"))
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}

	// Completed event fulfills the order: subscription created, top-up row
	// recorded, order marked success, system log written.
	out := sendWebhook("checkout.session.completed",
		fmt.Sprintf(`{"client_reference_id":%q,"customer":"cus_1","amount_total":4000,"currency":"usd","metadata":{}}`, refID))
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

	var topup model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&topup).Error)
	assert.Equal(t, service.TopUpStatusSuccess, topup.Status)
	assert.Equal(t, 40.0, topup.Money)
	assert.Equal(t, service.PaymentMethodStripe, topup.PaymentMethod)

	// Duplicate delivery is idempotent.
	out = sendWebhook("checkout.session.completed",
		fmt.Sprintf(`{"client_reference_id":%q,"customer":"cus_1","amount_total":4000,"currency":"usd","metadata":{}}`, refID))
	require.Equal(t, http.StatusOK, out.Code)
	model.DB.Model(&model.UserSubscription{}).Where("user_id = ? AND plan_id = ?", uid, plan.Id).Count(&count)
	assert.Equal(t, int64(1), count, "no second subscription on duplicate delivery")

	// Expired event marks a fresh pending order expired.
	rec2 := do(http.MethodPost, "/api/subscription/stripe/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	ref2 := mock.lastRequestBody()["client_reference_id"].(string)
	out = sendWebhook("checkout.session.expired",
		fmt.Sprintf(`{"client_reference_id":%q,"status":"expired","metadata":{}}`, ref2))
	require.Equal(t, http.StatusOK, out.Code)
	var order2 model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", ref2).First(&order2).Error)
	assert.Equal(t, service.TopUpStatusExpired, order2.Status)

	// Expiring an already-successful order is a no-op.
	out = sendWebhook("checkout.session.expired",
		fmt.Sprintf(`{"client_reference_id":%q,"status":"expired","metadata":{}}`, refID))
	require.Equal(t, http.StatusOK, out.Code)
	require.NoError(t, model.DB.Where("trade_no = ?", refID).First(&order).Error)
	assert.Equal(t, service.TopUpStatusSuccess, order.Status)
}
