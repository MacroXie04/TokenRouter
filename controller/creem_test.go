package controller_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/controller"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type creemRoundTripFunc func(*http.Request) (*http.Response, error)

func (f creemRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func signCreem(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func configureCreemForController(t *testing.T) {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.CreemAPIKeyOption:        "creem_test_key",
		setting.CreemWebhookSecretOption: "creem_webhook_secret",
		setting.CreemTestModeOption:      "true",
		setting.CreemProductsOption:      `[{"productId":"prod_wallet","name":"Wallet pack","price":12.34,"currency":"USD","quota":12345}]`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.CreemAPIKeyOption:        "",
			setting.CreemWebhookSecretOption: "",
			setting.CreemTestModeOption:      "false",
			setting.CreemProductsOption:      "[]",
		})
	})
}

func sendCreemWebhook(handler http.Handler, payload, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/creem/webhook", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(controller.CreemSignatureHeader, signCreem([]byte(payload), secret))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func creemPaidEvent(eventID, checkoutID, orderID, tradeNo, productID string, amount int64, currency, orderType, quota string) string {
	return fmt.Sprintf(`{"id":%q,"eventType":"checkout.completed","created_at":1700000000,"object":{"id":%q,"request_id":%q,"order":{"id":%q,"product":%q,"amount_paid":%d,"currency":%q,"status":"paid","type":%q},"product":{"id":%q},"metadata":{"reference_id":%q,"quota":%q}}}`,
		eventID, checkoutID, tradeNo, orderID, productID, amount, currency, orderType, productID, tradeNo, quota)
}

func TestCreemWalletAndSubscriptionHTTPContractEndToEnd(t *testing.T) {
	handler, do, userID, plan := setupSubscriptionStripeTest(t)
	configureCreemForController(t)
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		Update("creem_product_id", "prod_subscription").Error)

	var mu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	restore := controller.SetCreemCheckoutTransportForTesting(creemRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "https://test-api.creem.io/v1/checkouts", request.URL.String())
		require.Equal(t, "application/json", request.Header.Get("Content-Type"))
		require.Equal(t, "creem_test_key", request.Header.Get("x-api-key"))
		body, err := common.ReadAllLimited(request.Body, 64<<10)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, common.Unmarshal(body, &decoded))
		mu.Lock()
		requests = append(requests, decoded)
		mu.Unlock()
		checkoutID := "ch_wallet"
		if decoded["product_id"] == "prod_subscription" {
			checkoutID = "ch_subscription"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
				`{"checkout_url":"https://checkout.creem.io/pay/%s","id":%q}`, checkoutID, checkoutID))),
		}, nil
	}))
	t.Cleanup(restore)

	// Both payment creation routes are session protected. The webhook remains
	// anonymous and authenticates the raw body instead.
	for _, path := range []string{"/api/user/creem/pay", "/api/subscription/creem/pay"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, path)
	}

	recorder := do(http.MethodPost, "/api/user/creem/pay", `{"product_id":"prod_wallet","payment_method":"creem"}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	require.Equal(t, "success", body["message"])
	walletData := body["data"].(map[string]any)
	assert.Equal(t, "https://checkout.creem.io/pay/ch_wallet", walletData["checkout_url"])
	walletTrade := walletData["order_id"].(string)
	assert.True(t, strings.HasPrefix(walletTrade, "ref_"))
	var walletOrder model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", walletTrade).First(&walletOrder).Error)
	assert.Equal(t, service.TopUpStatusPending, walletOrder.Status)
	assert.Equal(t, int64(12_345), walletOrder.CreditQuota)
	assert.Equal(t, int64(1234), walletOrder.ProviderAmountMinor)
	assert.Equal(t, service.PaymentProviderCreem, walletOrder.PaymentProvider)
	require.NotNil(t, walletOrder.ProviderSessionId)
	assert.Equal(t, "ch_wallet", *walletOrder.ProviderSessionId)
	assert.NotEmpty(t, walletOrder.CheckoutRequest)
	assert.NotEmpty(t, walletOrder.CheckoutFingerprint)

	wrongTypeWallet := creemPaidEvent("evt_wallet_recurring", "ch_wallet", "ord_wallet", walletTrade,
		"prod_wallet", 1234, "USD", "recurring", "12345")
	recorder = sendCreemWebhook(handler, wrongTypeWallet, "creem_webhook_secret")
	assert.Equal(t, http.StatusOK, recorder.Code, "the reference ignores non-onetime wallet events")

	wrongWallet := creemPaidEvent("evt_wallet_bad", "ch_wallet", "ord_wallet", walletTrade,
		"prod_wallet", 1233, "USD", "onetime", "12345")
	recorder = sendCreemWebhook(handler, wrongWallet, "creem_webhook_secret")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	var walletUser model.User
	require.NoError(t, model.DB.First(&walletUser, userID).Error)
	assert.Equal(t, 1000, walletUser.Quota)

	validWallet := creemPaidEvent("evt_wallet", "ch_wallet", "ord_wallet", walletTrade,
		"prod_wallet", 1234, "usd", "onetime", "12345")
	recorder = sendCreemWebhook(handler, validWallet, "creem_webhook_secret")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	recorder = sendCreemWebhook(handler, validWallet, "creem_webhook_secret")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, model.DB.First(&walletUser, userID).Error)
	assert.Equal(t, 1000+12_345, walletUser.Quota)

	// Subscription creation uses the plan's provider product while sharing the
	// same fixed endpoint and captures a purchase-cap/entitlement snapshot.
	recorder = do(http.MethodPost, "/api/subscription/creem/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body = decodeBody(t, recorder)
	require.Equal(t, "success", body["message"])
	subscriptionData := body["data"].(map[string]any)
	assert.Equal(t, "https://checkout.creem.io/pay/ch_subscription", subscriptionData["checkout_url"])
	subscriptionTrade := subscriptionData["order_id"].(string)
	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", subscriptionTrade).First(&order).Error)
	assert.True(t, order.CapacityReserved)
	assert.Equal(t, int64(4000), order.ProviderAmountMinor)
	assert.Equal(t, "prod_subscription", order.ProviderPriceId)
	require.NotNil(t, order.ProviderSessionId)
	assert.Equal(t, "ch_subscription", *order.ProviderSessionId)

	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"total_amount": int64(1), "duration_value": 12, "creem_product_id": "prod_mutated",
	}).Error)
	validSubscription := creemPaidEvent("evt_subscription", "ch_subscription", "ord_subscription", subscriptionTrade,
		"prod_subscription", 4000, "USD", "recurring", "0")
	recorder = sendCreemWebhook(handler, validSubscription, "creem_webhook_secret")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	recorder = sendCreemWebhook(handler, validSubscription, "creem_webhook_secret")
	require.Equal(t, http.StatusOK, recorder.Code)
	var subscription model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", userID, plan.Id).First(&subscription).Error)
	assert.Equal(t, int64(100000), subscription.AmountTotal, "checkout-time entitlement wins over plan mutation")
	var subscriptionTopups int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", subscriptionTrade).Count(&subscriptionTopups).Error)
	assert.Equal(t, int64(1), subscriptionTopups)

	mu.Lock()
	require.Len(t, requests, 2)
	walletRequest := requests[0]
	subscriptionRequest := requests[1]
	mu.Unlock()
	assert.Equal(t, "prod_wallet", walletRequest["product_id"])
	assert.Equal(t, walletTrade, walletRequest["request_id"])
	assert.Equal(t, "prod_subscription", subscriptionRequest["product_id"])
	assert.Equal(t, subscriptionTrade, subscriptionRequest["request_id"])
}

func TestCreemWebhookSignatureAvailabilityAndBodyLimits(t *testing.T) {
	handler, _, _, _ := setupSubscriptionStripeTest(t)
	setPaymentCompliance(t, true)
	payload := `{"id":"evt_ignore","eventType":"customer.created"}`

	// A test-mode flag never bypasses the signing secret or the full provider
	// availability gate.
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.CreemAPIKeyOption: "creem_key", setting.CreemWebhookSecretOption: "",
		setting.CreemTestModeOption: "true", setting.CreemProductsOption: `[{"productId":"prod","name":"P","price":1,"currency":"USD","quota":1}]`,
	}))
	recorder := sendCreemWebhook(handler, payload, "")
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	require.NoError(t, setting.UpdateOption(setting.CreemWebhookSecretOption, "creem_secret"))

	req := httptest.NewRequest(http.MethodPost, "/api/creem/webhook", strings.NewReader(payload))
	req.Header.Set(controller.CreemSignatureHeader, strings.Repeat("0", 64))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)

	recorder = sendCreemWebhook(handler, payload, "creem_secret")
	assert.Equal(t, http.StatusOK, recorder.Code, "signed unrelated events are acknowledged")
	recorder = sendCreemWebhook(handler, "", "creem_secret")
	assert.Equal(t, http.StatusBadRequest, recorder.Code, "a valid empty-body HMAC reaches JSON validation")
	recorder = sendCreemWebhook(handler, `{`, "creem_secret")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "1")
	oversized := strings.Repeat("x", 1025)
	req = httptest.NewRequest(http.MethodPost, "/api/creem/webhook", strings.NewReader(oversized))
	req.Header.Set(controller.CreemSignatureHeader, signCreem([]byte(oversized), "creem_secret"))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)

	valid := []byte(`{"id":"evt","eventType":"checkout.completed"}`)
	assert.True(t, controller.VerifyCreemSignature(valid, signCreem(valid, "creem_secret"), "creem_secret"))
	assert.False(t, controller.VerifyCreemSignature(valid, strings.ToUpper(signCreem(valid, "creem_secret")), "creem_secret"))
	assert.False(t, controller.VerifyCreemSignature(valid, signCreem(valid, "wrong"), "creem_secret"))
}

func TestCreemCheckoutFailureClassificationPreservesAmbiguousOrdersAndReleasesDefiniteRejections(t *testing.T) {
	handler, do, userID, plan := setupSubscriptionStripeTest(t)
	configureCreemForController(t)
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		Update("creem_product_id", "prod_subscription").Error)

	// Transport failure is ambiguous: the request_id may already have created
	// checkout state, so the complete immutable local order remains pending.
	restore := controller.SetCreemCheckoutTransportForTesting(creemRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("injected timeout")
	}))
	recorder := do(http.MethodPost, "/api/user/creem/pay", `{"product_id":"prod_wallet","payment_method":"creem"}`)
	restore()
	assert.Equal(t, "error", decodeBody(t, recorder)["message"])
	var wallet model.TopUp
	require.NoError(t, model.DB.Where("user_id = ? AND payment_provider = ?", userID, service.PaymentProviderCreem).
		Order("id desc").First(&wallet).Error)
	assert.Equal(t, service.TopUpStatusPending, wallet.Status)
	assert.Equal(t, service.CreemReconciliationCreate, wallet.ReconciliationState)
	assert.NotEmpty(t, wallet.CheckoutRequest)
	assert.Nil(t, wallet.ProviderSessionId)
	recoveredWallet := creemPaidEvent("evt_wallet_recovered", "ch_wallet_recovered", "ord_wallet_recovered", wallet.TradeNo,
		"prod_wallet", 1234, "USD", "onetime", "12345")
	recorder = sendCreemWebhook(handler, recoveredWallet, "creem_webhook_secret")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, model.DB.First(&wallet, wallet.Id).Error)
	require.NotNil(t, wallet.ProviderSessionId)
	assert.Equal(t, "ch_wallet_recovered", *wallet.ProviderSessionId)
	assert.Equal(t, service.TopUpStatusSuccess, wallet.Status)

	// A definitive provider 400 cannot have produced a usable checkout. The
	// subscription reservation is released instead of permanently consuming
	// the plan's purchase cap.
	restore = controller.SetCreemCheckoutTransportForTesting(creemRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"bad product"}`))}, nil
	}))
	recorder = do(http.MethodPost, "/api/subscription/creem/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	restore()
	assert.Equal(t, "error", decodeBody(t, recorder)["message"])
	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("user_id = ? AND payment_provider = ?", userID, service.PaymentProviderCreem).
		Order("id desc").First(&order).Error)
	assert.Equal(t, service.TopUpStatusFailed, order.Status)
	assert.False(t, order.CapacityReserved)
	assert.NotEmpty(t, order.CheckoutRequest)

	// A malformed 2xx may conceal an already-created checkout, so it remains
	// pending rather than being falsely marked failed.
	restore = controller.SetCreemCheckoutTransportForTesting(creemRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"checkout_url":"https://checkout.creem.io/pay/missing-id"}`))}, nil
	}))
	recorder = do(http.MethodPost, "/api/subscription/creem/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	restore()
	assert.Equal(t, "error", decodeBody(t, recorder)["message"])
	order = model.SubscriptionOrder{}
	require.NoError(t, model.DB.Where("user_id = ? AND payment_provider = ?", userID, service.PaymentProviderCreem).
		Order("id desc").First(&order).Error)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.True(t, order.CapacityReserved)
	assert.Nil(t, order.ProviderSessionId)
	recoveredSubscription := creemPaidEvent("evt_subscription_recovered", "ch_subscription_recovered", "ord_subscription_recovered", order.TradeNo,
		"prod_subscription", 4000, "USD", "recurring", "0")
	recorder = sendCreemWebhook(handler, recoveredSubscription, "creem_webhook_secret")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, model.DB.First(&order, order.Id).Error)
	require.NotNil(t, order.ProviderSessionId)
	assert.Equal(t, "ch_subscription_recovered", *order.ProviderSessionId)
	assert.Equal(t, service.TopUpStatusSuccess, order.Status)
	assert.False(t, order.CapacityReserved)
}

func TestCreemTopUpInfoAndInvalidRequestContracts(t *testing.T) {
	_, do, _ := setupTopupTest(t)
	configureCreemForController(t)
	topUpInfoPath := "/api/user/topup/" + "info"
	recorder := do(http.MethodGet, topUpInfoPath, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeBody(t, recorder)["data"].(map[string]any)
	assert.Equal(t, true, data["enable_creem_topup"])
	assert.Contains(t, data["creem_products"], "prod_wallet")

	for _, request := range []string{
		`{`,
		`{"product_id":"","payment_method":"creem"}`,
		`{"product_id":"prod_wallet","payment_method":"stripe"}`,
		`{"product_id":"missing","payment_method":"creem"}`,
	} {
		recorder = do(http.MethodPost, "/api/user/creem/pay", request)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.NotEqual(t, "success", decodeBody(t, recorder)["message"])
	}

	setPaymentCompliance(t, false)
	recorder = do(http.MethodGet, topUpInfoPath, "")
	data = decodeBody(t, recorder)["data"].(map[string]any)
	assert.Equal(t, false, data["enable_creem_topup"])
	recorder = do(http.MethodPost, "/api/user/creem/pay", `{"product_id":"prod_wallet","payment_method":"creem"}`)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
