package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	epay "github.com/Calcium-Ion/go-epay/epay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupSubscriptionEpayTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int, *model.SubscriptionPlan) {
	t.Helper()
	handler, do, userID, plan := setupSubscriptionStripeTest(t)
	plan.MaxPurchasePerUser = 3
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		Update("max_purchase_per_user", plan.MaxPurchasePerUser).Error)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PayMethodsOption:            `[{"name":"Alipay","type":"alipay"},{"name":"Wechat","type":"wxpay"}]`,
		setting.PayAddressOption:            "https://pay.example.test/base",
		setting.EpayIdOption:                "subscription-pid",
		setting.EpayKeyOption:               "subscription-secret",
		setting.ServerAddressOption:         "https://dashboard.example.test/",
		setting.CustomCallbackAddressOption: "https://callback.example.test/",
	}))
	return handler, do, userID, plan
}

func signedSubscriptionEpayValues(tradeNo, method, money, status, pid string) url.Values {
	params := epay.GenerateParams(map[string]string{
		"pid":          pid,
		"trade_no":     "gateway-" + tradeNo,
		"out_trade_no": tradeNo,
		"type":         method,
		"name":         "SUB:Pro",
		"money":        money,
		"trade_status": status,
	}, "subscription-secret")
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	return values
}

func sendSubscriptionEpayCallback(handler http.Handler, method, path string, values url.Values) *httptest.ResponseRecorder {
	requestPath := path
	var body *strings.Reader
	if method == http.MethodGet {
		requestPath += "?" + values.Encode()
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(values.Encode())
	}
	request := httptest.NewRequest(method, requestPath, body)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestSubscriptionEpayCheckoutNotifyAndReturnContract(t *testing.T) {
	handler, do, userID, plan := setupSubscriptionEpayTest(t)

	// Checkout is authenticated and compliance gated.
	requestBody := fmt.Sprintf(`{"plan_id":%d,"payment_method":"alipay"}`, plan.Id)
	setPaymentCompliance(t, false)
	recorder := do(http.MethodPost, "/api/subscription/epay/pay", requestBody)
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	assert.Equal(t, false, decodeBody(t, recorder)["success"])
	setPaymentCompliance(t, true)
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/api/subscription/epay/pay", strings.NewReader(requestBody)))
	assert.Equal(t, http.StatusUnauthorized, anonymous.Code)

	recorder = do(http.MethodPost, "/api/subscription/epay/pay",
		fmt.Sprintf(`{"plan_id":%d,"payment_method":"bogus"}`, plan.Id))
	assert.Equal(t, "支付方式不存在", decodeBody(t, recorder)["message"])

	// The checkout call is entirely local: the EPay SDK only signs these
	// deterministic parameters and never contacts pay.example.test.
	recorder = do(http.MethodPost, "/api/subscription/epay/pay", requestBody)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	response := decodeBody(t, recorder)
	assert.Equal(t, "success", response["message"])
	params, ok := response["data"].(map[string]any)
	require.True(t, ok, recorder.Body.String())
	assert.Equal(t, "subscription-pid", params["pid"])
	assert.Equal(t, "alipay", params["type"])
	assert.Equal(t, "40.00", params["money"])
	assert.Equal(t, "https://callback.example.test/api/subscription/epay/notify", params["notify_url"])
	assert.Equal(t, "https://callback.example.test/api/subscription/epay/return", params["return_url"])
	assert.Equal(t, "MD5", params["sign_type"])
	assert.NotEmpty(t, params["sign"])
	assert.Equal(t, "https://pay.example.test/base/submit.php", response["url"])
	signedParams := make(map[string]string, len(params))
	for key, value := range params {
		text, ok := value.(string)
		require.True(t, ok, "%s is not a string", key)
		signedParams[key] = text
	}
	providedSignature := signedParams["sign"]
	assert.Equal(t, epay.GenerateParams(signedParams, "subscription-secret")["sign"], providedSignature)
	tradeNo, ok := params["out_trade_no"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(tradeNo, fmt.Sprintf("SUBUSR%dNO", userID)), tradeNo)

	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&order).Error)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.Equal(t, service.PaymentProviderEpay, order.PaymentProvider)
	assert.Equal(t, "alipay", order.PaymentMethod)
	assert.Equal(t, 40.0, order.Money)
	assert.Equal(t, int64(4000), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)
	assert.Equal(t, service.EpaySubscriptionBindingVersion, order.ProviderBindingVersion)
	assert.True(t, order.CapacityReserved)
	assert.NotEmpty(t, order.EntitlementSnapshot)

	assertStillPending := func() {
		t.Helper()
		order = model.SubscriptionOrder{}
		require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&order).Error)
		assert.Equal(t, service.TopUpStatusPending, order.Status)
		var subscriptions, topups int64
		require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&subscriptions).Error)
		require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", tradeNo).Count(&topups).Error)
		assert.Zero(t, subscriptions)
		assert.Zero(t, topups)
	}

	// Even correctly signed callbacks fail closed when any bound field differs.
	for name, values := range map[string]url.Values{
		"amount":   signedSubscriptionEpayValues(tradeNo, "alipay", "39.99", epay.StatusTradeSuccess, "subscription-pid"),
		"method":   signedSubscriptionEpayValues(tradeNo, "wxpay", "40.00", epay.StatusTradeSuccess, "subscription-pid"),
		"merchant": signedSubscriptionEpayValues(tradeNo, "alipay", "40.00", epay.StatusTradeSuccess, "other-pid"),
		"status":   signedSubscriptionEpayValues(tradeNo, "alipay", "40.00", "WAIT_BUYER_PAY", "subscription-pid"),
	} {
		t.Run("reject "+name, func(t *testing.T) {
			out := sendSubscriptionEpayCallback(handler, http.MethodPost, "/api/subscription/epay/notify", values)
			assert.Equal(t, "fail", out.Body.String())
			assertStillPending()
		})
	}
	tampered := signedSubscriptionEpayValues(tradeNo, "alipay", "40.00", epay.StatusTradeSuccess, "subscription-pid")
	tampered.Set("sign", "bad")
	assert.Equal(t, "fail", sendSubscriptionEpayCallback(handler, http.MethodPost, "/api/subscription/epay/notify", tampered).Body.String())
	duplicateField := signedSubscriptionEpayValues(tradeNo, "alipay", "40.00", epay.StatusTradeSuccess, "subscription-pid")
	duplicateField.Add("money", "40.00")
	assert.Equal(t, "fail", sendSubscriptionEpayCallback(handler, http.MethodPost, "/api/subscription/epay/notify", duplicateField).Body.String())
	assertStillPending()

	valid := signedSubscriptionEpayValues(tradeNo, "alipay", "40.0", epay.StatusTradeSuccess, "subscription-pid")
	// Disabling future checkout creation must not strand an already-issued,
	// cryptographically valid and amount-bound order.
	setPaymentCompliance(t, false)
	settled := sendSubscriptionEpayCallback(handler, http.MethodPost, "/api/subscription/epay/notify", valid)
	require.Equal(t, http.StatusOK, settled.Code)
	assert.Equal(t, "success", settled.Body.String())
	setPaymentCompliance(t, true)

	// GET notify uses the same public callback contract and is idempotent.
	assert.Equal(t, "success", sendSubscriptionEpayCallback(handler, http.MethodGet, "/api/subscription/epay/notify", valid).Body.String())
	wrongReplay := signedSubscriptionEpayValues(tradeNo, "alipay", "40.01", epay.StatusTradeSuccess, "subscription-pid")
	assert.Equal(t, "fail", sendSubscriptionEpayCallback(handler, http.MethodGet, "/api/subscription/epay/notify", wrongReplay).Body.String())

	order = model.SubscriptionOrder{}
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&order).Error)
	assert.Equal(t, service.TopUpStatusSuccess, order.Status)
	assert.False(t, order.CapacityReserved)
	assert.NotZero(t, order.CompleteTime)
	assert.Contains(t, order.ProviderPayload, "gateway-")
	var subscriptions, topups, logs int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("trade_no = ?", tradeNo).Count(&topups).Error)
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", userID).Count(&logs).Error)
	assert.Equal(t, int64(1), subscriptions)
	assert.Equal(t, int64(1), topups)
	assert.Equal(t, int64(1), logs)

	// The browser-return endpoints are also public. A signed pending state only
	// redirects; the later POST success atomically fulfills the same order.
	recorder = do(http.MethodPost, "/api/subscription/epay/pay", requestBody)
	require.Equal(t, "success", decodeBody(t, recorder)["message"], recorder.Body.String())
	secondParams := decodeBody(t, recorder)["data"].(map[string]any)
	secondTrade := secondParams["out_trade_no"].(string)
	pendingReturn := signedSubscriptionEpayValues(secondTrade, "alipay", "40.00", "WAIT_BUYER_PAY", "subscription-pid")
	returned := sendSubscriptionEpayCallback(handler, http.MethodGet, "/api/subscription/epay/return", pendingReturn)
	assert.Equal(t, http.StatusFound, returned.Code)
	assert.Equal(t, "https://dashboard.example.test/wallet?pay=pending", returned.Header().Get("Location"))
	var secondOrder model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", secondTrade).First(&secondOrder).Error)
	assert.Equal(t, service.TopUpStatusPending, secondOrder.Status)

	invalidReturn := signedSubscriptionEpayValues(secondTrade, "alipay", "40.00", epay.StatusTradeSuccess, "subscription-pid")
	invalidReturn.Set("sign", "bad")
	returned = sendSubscriptionEpayCallback(handler, http.MethodPost, "/api/subscription/epay/return", invalidReturn)
	assert.Equal(t, http.StatusFound, returned.Code)
	assert.Equal(t, "https://dashboard.example.test/wallet?pay=fail", returned.Header().Get("Location"))

	successReturn := signedSubscriptionEpayValues(secondTrade, "alipay", "40.00", epay.StatusTradeSuccess, "subscription-pid")
	returned = sendSubscriptionEpayCallback(handler, http.MethodPost, "/api/subscription/epay/return", successReturn)
	assert.Equal(t, http.StatusFound, returned.Code)
	assert.Equal(t, "https://dashboard.example.test/wallet?pay=success", returned.Header().Get("Location"))
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("user_id = ?", userID).Count(&subscriptions).Error)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("user_id = ?", userID).Count(&topups).Error)
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", userID).Count(&logs).Error)
	assert.Equal(t, int64(2), subscriptions)
	assert.Equal(t, int64(2), topups)
	assert.Equal(t, int64(2), logs)
}

func TestSubscriptionEpayCheckoutFailsClosedBeforeOrderCreation(t *testing.T) {
	_, do, userID, plan := setupSubscriptionEpayTest(t)
	requestBody := fmt.Sprintf(`{"plan_id":%d,"payment_method":"alipay"}`, plan.Id)

	require.NoError(t, setting.UpdateOption(setting.CustomCallbackAddressOption, "not-an-absolute-url"))
	recorder := do(http.MethodPost, "/api/subscription/epay/pay", requestBody)
	assert.Equal(t, "回调地址配置错误", decodeBody(t, recorder)["message"])
	var orders int64
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", userID).Count(&orders).Error)
	assert.Zero(t, orders)

	require.NoError(t, setting.UpdateOption(setting.CustomCallbackAddressOption, "https://callback.example.test"))
	require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "pay.example.test"))
	recorder = do(http.MethodPost, "/api/subscription/epay/pay", requestBody)
	assert.Equal(t, "当前管理员未配置支付信息", decodeBody(t, recorder)["message"])
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Where("user_id = ?", userID).Count(&orders).Error)
	assert.Zero(t, orders)
}
