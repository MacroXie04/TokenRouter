package router_test

import (
	"errors"
	"fmt"
	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type paymentEntropyFailureReader struct{}

func (paymentEntropyFailureReader) Read([]byte) (int, error) {
	return 0, errors.New("injected entropy failure")
}

// setupTopupTest builds a root session with the payment options reset and
// compliance confirmed.
func setupTopupTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	handler, do, uid := setupDashboardSession(t, roles.RoleRootUser)
	setPaymentCompliance(t, true)
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")
	for _, key := range []string{
		setting.PayMethodsOption, setting.PriceOption, setting.MinTopUpOption,
		setting.PayAddressOption, setting.EpayIdOption, setting.EpayKeyOption, setting.ServerAddressOption,
		setting.StripeMinTopUpOption, setting.StripePriceIdOption, setting.StripeUnitPriceOption,
		setting.StripeCurrencyOption, setting.StripePromotionCodesOption,
		setting.CreemAPIKeyOption, setting.CreemProductsOption, setting.CreemTestModeOption, setting.CreemWebhookSecretOption,
		setting.WaffoPancakeMerchantIDOption, setting.WaffoPancakePrivateKeyOption,
		setting.WaffoPancakeReturnURLOption, setting.WaffoPancakeUnitPriceOption,
		setting.WaffoPancakeMinTopUpOption, setting.WaffoPancakeStoreIDOption,
		setting.WaffoPancakeProductIDOption,
		setting.QuotaDisplayTypeOption, setting.USDExchangeRateOption, setting.CustomCurrencySymbolOption,
		setting.CustomCurrencyExchangeRateOption, setting.TopUpLinkOption, setting.TopUpGroupRatioOption,
		setting.PaymentSettingOption, setting.CustomCallbackAddressOption,
	} {
		require.NoError(t, setting.UpdateOption(key, ""))
	}
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://localhost:3000"))
	billingsvc.SetGroupRatios(map[string]float64{"default": 1.0, "vip": 2.0})
	billingsvc.SetTopUpGroupRatios(map[string]float64{"default": 1.0, "vip": 2.0})
	return handler, do, uid
}

func TestTopUpInfoContract(t *testing.T) {
	_, do, _ := setupTopupTest(t)

	// Compliance not confirmed: empty methods, all gates disabled.
	setPaymentCompliance(t, false)
	rec := do(http.MethodGet, "/api/user/topup/info", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, data["payment_compliance_confirmed"])
	assert.Equal(t, false, data["enable_redemption"])
	assert.Equal(t, false, data["enable_online_topup"])
	assert.Equal(t, false, data["enable_stripe_topup"])
	assert.Empty(t, data["pay_methods"])
	assert.Equal(t, float64(1), data["min_topup"])

	// Compliance confirmed: default methods and presets; the optional external
	// redemption-code link is absent until explicitly configured.
	setPaymentCompliance(t, true)
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, true, data["payment_compliance_confirmed"])
	assert.Equal(t, "v1", data["payment_compliance_terms_version"])
	methods := data["pay_methods"].([]any)
	require.Len(t, methods, 3)
	assert.Equal(t, "alipay", methods[0].(map[string]any)["type"])
	assert.Equal(t, "wxpay", methods[1].(map[string]any)["type"])
	assert.Equal(t, "50", methods[2].(map[string]any)["min_topup"])
	options := data["amount_options"].([]any)
	assert.Len(t, options, 6)
	assert.Equal(t, float64(10), options[0])
	assert.Equal(t, false, data["enable_online_topup"], "epay credentials unset")
	assert.Equal(t, "", data["topup_link"])
	assert.Equal(t, "currency", data["quota_display_type"])
	assert.Equal(t, float64(quotamath.QuotaPerUnit), data["quota_per_unit"])
	assert.Equal(t, 7.3, data["usd_exchange_rate"])

	// Wallet presentation metadata follows the server configuration while the
	// internal accounting scale remains the immutable QuotaPerUnit constant.
	require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, " cNy "))
	require.NoError(t, setting.UpdateOption(setting.USDExchangeRateOption, "6.8"))
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "cny", data["quota_display_type"])
	assert.Equal(t, float64(quotamath.QuotaPerUnit), data["quota_per_unit"])
	assert.Equal(t, 6.8, data["usd_exchange_rate"])
	assert.Equal(t, "¥", data["currency_symbol"])
	assert.Equal(t, 6.8, data["currency_exchange_rate"])
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.QuotaDisplayTypeOption:           "CUSTOM",
		setting.CustomCurrencySymbolOption:       "HK$",
		setting.CustomCurrencyExchangeRateOption: "7.8",
	}))
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "custom", data["quota_display_type"])
	assert.Equal(t, "HK$", data["currency_symbol"])
	assert.Equal(t, 7.8, data["currency_exchange_rate"])
	require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, setting.QuotaDisplayTypeTokens))
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "tokens", data["quota_display_type"])
	assert.Equal(t, float64(quotamath.QuotaPerUnit), data["quota_per_unit"])
	assert.Equal(t, 6.8, data["usd_exchange_rate"])
	assert.Equal(t, "tokens", data["currency_symbol"])
	assert.Equal(t, 1.0, data["currency_exchange_rate"])

	// Invalid and extreme display-only metadata is normalized before it can
	// reach client-side conversion arithmetic.
	for _, rate := range []string{"not-a-number", "0", "NaN", "+Inf", "1e100"} {
		require.NoError(t, setting.UpdateOption(setting.USDExchangeRateOption, rate))
		rec = do(http.MethodGet, "/api/user/topup/info", "")
		data = decodeBody(t, rec)["data"].(map[string]any)
		assert.Equal(t, 7.3, data["usd_exchange_rate"], rate)
	}
	require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, ""))
	require.NoError(t, setting.UpdateOption(setting.USDExchangeRateOption, ""))
	require.NoError(t, setting.UpdateOption(setting.TopUpLinkOption, "https://billing.example.test/codes?campaign=wallet"))
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "https://billing.example.test/codes?campaign=wallet", data["topup_link"])
	require.NoError(t, setting.UpdateOption(setting.TopUpLinkOption, "javascript:alert(1)"))
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "", data["topup_link"])

	// Stripe configured: the stripe method is appended with its minimum.
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_x")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, true, data["enable_stripe_topup"])
	methods = data["pay_methods"].([]any)
	require.Len(t, methods, 4)
	last := methods[3].(map[string]any)
	assert.Equal(t, "stripe", last["type"])
	assert.Equal(t, "#635BFF", last["color"])

	// Epay credentials configured: online top-up enabled.
	require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "https://pay.example.com"))
	require.NoError(t, setting.UpdateOption(setting.EpayIdOption, "pid"))
	require.NoError(t, setting.UpdateOption(setting.EpayKeyOption, "key"))
	rec = do(http.MethodGet, "/api/user/topup/info", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, true, data["enable_online_topup"])
}

func TestTopUpInfoDoesNotAdvertiseUnreadyStripe(t *testing.T) {
	_, do, _ := setupTopupTest(t)
	require.NoError(t, setting.UpdateOption(setting.PayMethodsOption,
		`[{"name":"Stripe","type":"stripe"},{"name":"Alipay","type":"alipay"}]`))
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_readiness")

	assertStripeVisibility := func(enabled bool) {
		t.Helper()
		rec := do(http.MethodGet, "/api/user/topup/info", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		data := decodeBody(t, rec)["data"].(map[string]any)
		assert.Equal(t, enabled, data["enable_stripe_topup"])
		visible := false
		for _, raw := range data["pay_methods"].([]any) {
			if raw.(map[string]any)["type"] == "stripe" {
				visible = true
			}
		}
		assert.Equal(t, enabled, visible)
	}

	assertStripeVisibility(false) // missing webhook secret
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_ready")
	t.Setenv("STRIPE_SECRET_KEY", "sk_")
	assertStripeVisibility(false) // a prefix alone is not a valid API key shape
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_readiness")
	require.NoError(t, setting.UpdateOption(setting.StripePromotionCodesOption, "true"))
	assertStripeVisibility(false) // promotions can change the exact charge
	require.NoError(t, setting.UpdateOption(setting.StripePromotionCodesOption, "false"))
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://payments.example.test"))
	assertStripeVisibility(false) // default Checkout returns must not use remote plaintext
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://localhost:3000"))
	assertStripeVisibility(true)
}

func TestTopUpAmountContract(t *testing.T) {
	_, do, uid := setupTopupTest(t)
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "2"))

	// Below the minimum.
	rec := do(http.MethodPost, "/api/user/amount", `{"amount":0}`)
	body := decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "充值数量不能小于 1", body["data"])

	// Plain money display: amount * price.
	rec = do(http.MethodPost, "/api/user/amount", `{"amount":10}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	assert.Equal(t, "20.00", body["data"])

	// Group ratio applies.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", uid).Update("group", "vip").Error)
	rec = do(http.MethodPost, "/api/user/amount", `{"amount":10}`)
	assert.Equal(t, "40.00", decodeBody(t, rec)["data"])

	// Preset discount for the requested amount applies.
	require.NoError(t, setting.UpdateOption(setting.PaymentSettingOption,
		`{"amount_options":[10,20],"amount_discount":{"10":0.5}}`))
	rec = do(http.MethodPost, "/api/user/amount", `{"amount":10}`)
	assert.Equal(t, "20.00", decodeBody(t, rec)["data"], "10*2*2*0.5")
	rec = do(http.MethodPost, "/api/user/amount", `{"amount":20}`)
	assert.Equal(t, "80.00", decodeBody(t, rec)["data"], "no discount for 20")
}

func TestTopUpEpayPayAndNotifyContract(t *testing.T) {
	handler, do, uid := setupTopupTest(t)
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "1"))
	require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "https://pay.example.com"))
	require.NoError(t, setting.UpdateOption(setting.EpayIdOption, "pid123"))
	require.NoError(t, setting.UpdateOption(setting.EpayKeyOption, "secret-key"))

	rec := do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"alipay"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	params, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	assert.Equal(t, "pid123", params["pid"])
	assert.Equal(t, "alipay", params["type"])
	assert.Equal(t, "10.00", params["money"])
	assert.NotEmpty(t, params["sign"])
	payURL, ok := body["url"].(string)
	require.True(t, ok)
	assert.Contains(t, payURL, "pay.example.com/submit.php")
	tradeNo, ok := params["out_trade_no"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, tradeNo)

	var order model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&order).Error)
	assert.Equal(t, uid, order.UserId)
	assert.Equal(t, int64(10), order.Amount)
	assert.Equal(t, int64(10*quotamath.QuotaPerUnit), order.CreditQuota)
	assert.Positive(t, order.CreditQuotaVersion)
	assert.Equal(t, "alipay", order.PaymentMethod)
	assert.Equal(t, billingsvc.PaymentProviderEpay, order.PaymentProvider)
	assert.Equal(t, billingsvc.TopUpStatusPending, order.Status)
	assert.InDelta(t, 10.0, order.Money, 0.001)

	// Unknown payment method.
	rec = do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"bogus"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "支付方式不存在", body["data"])

	signedNotifyForm := func(money string) url.Values {
		notifyParams := epay.GenerateParams(map[string]string{
			"pid":          "pid123",
			"trade_no":     "gateway-trade-1",
			"out_trade_no": tradeNo,
			"type":         "alipay",
			"name":         "TUC10",
			"money":        money,
			"trade_status": epay.StatusTradeSuccess,
		}, "secret-key")
		form := url.Values{}
		for k, v := range notifyParams {
			form.Set(k, v)
		}
		return form
	}
	notify := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req)
		return rec2
	}

	// A valid signature does not authorize a different amount. The order and
	// user balance remain unchanged so a correct callback can be retried.
	var before model.User
	require.NoError(t, model.DB.First(&before, uid).Error)
	mismatch := notify(signedNotifyForm("9.99"))
	assert.Equal(t, "fail", mismatch.Body.String())
	var stillPending model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&stillPending).Error)
	assert.Equal(t, billingsvc.TopUpStatusPending, stillPending.Status)
	var unchanged model.User
	require.NoError(t, model.DB.First(&unchanged, uid).Error)
	assert.Equal(t, before.Quota, unchanged.Quota)

	// Disabling new checkouts must not strand an already-created pending order.
	// The callback remains authenticated with the configured signing key and is
	// still bound to the order's provider, method, and exact amount snapshot.
	setPaymentCompliance(t, false)
	require.NoError(t, setting.UpdateOption(setting.PayMethodsOption, `[]`))

	// The gateway notifies success with a fresh signed callback param set.
	// Numeric equality accepts equivalent decimal spellings, and settlement is
	// idempotent.
	form := signedNotifyForm("10.0")
	rec2 := notify(form)
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Equal(t, "success", rec2.Body.String())

	var settled model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&settled).Error)
	assert.Equal(t, billingsvc.TopUpStatusSuccess, settled.Status)
	assert.Greater(t, settled.CompleteTime, int64(0))
	var u model.User
	require.NoError(t, model.DB.First(&u, uid).Error)
	assert.Equal(t, before.Quota+10*quotamath.QuotaPerUnit, u.Quota, "top-up credits Amount * QuotaPerUnit")

	// A duplicate delivery is idempotent.
	rec3 := notify(form)
	assert.Equal(t, "success", rec3.Body.String())
	var u2 model.User
	require.NoError(t, model.DB.First(&u2, uid).Error)
	assert.Equal(t, u.Quota, u2.Quota)

	// A tampered signature is rejected.
	form.Set("sign", "bad")
	rec4 := notify(form)
	assert.Equal(t, "fail", rec4.Body.String())
}

func TestTokenDisplayEpayAndStripePreserveMoneyToQuotaContract(t *testing.T) {
	handler, do, uid := setupTopupTest(t)
	require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, setting.QuotaDisplayTypeTokens))
	require.NoError(t, setting.UpdateOption(setting.PriceOption, "1"))
	require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "1"))
	require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "https://pay.example.com"))
	require.NoError(t, setting.UpdateOption(setting.EpayIdOption, "pid-token"))
	require.NoError(t, setting.UpdateOption(setting.EpayKeyOption, "key-token"))
	displayAmount := int64(2 * quotamath.QuotaPerUnit)

	rec := do(http.MethodPost, "/api/user/pay",
		fmt.Sprintf(`{"amount":%d,"payment_method":"alipay"}`, displayAmount))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	params := decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "2.00", params["money"])
	tradeNo := params["out_trade_no"].(string)
	var epayOrder model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&epayOrder).Error)
	assert.Equal(t, int64(2), epayOrder.Amount)
	assert.Equal(t, displayAmount, epayOrder.CreditQuota)
	assert.Positive(t, epayOrder.CreditQuotaVersion)

	notifyParams := epay.GenerateParams(map[string]string{
		"pid": "pid-token", "trade_no": "gateway-token", "out_trade_no": tradeNo,
		"type": "alipay", "name": fmt.Sprintf("TUC%d", displayAmount), "money": "2.00",
		"trade_status": epay.StatusTradeSuccess,
	}, "key-token")
	form := url.Values{}
	for key, value := range notifyParams {
		form.Set(key, value)
	}
	notifyReq := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(form.Encode()))
	notifyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	notifyRec := httptest.NewRecorder()
	handler.ServeHTTP(notifyRec, notifyReq)
	assert.Equal(t, "success", notifyRec.Body.String())

	mock := newStripeMock(t, "")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_token_display")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_token_display")
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend,
		&stripe.BackendConfig{URL: stripe.String(mock.server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })
	rec = do(http.MethodPost, "/api/user/stripe/pay",
		fmt.Sprintf(`{"amount":%d,"payment_method":"stripe"}`, displayAmount))
	require.Equal(t, "success", decodeBody(t, rec)["message"], rec.Body.String())
	stripeForm := mock.lastRequestBody()
	assert.Equal(t, "200", stripeForm["line_items[0][price_data][unit_amount]"])
	stripeTrade := stripeForm["client_reference_id"].(string)
	var stripeOrder model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", stripeTrade).First(&stripeOrder).Error)
	assert.Equal(t, int64(2), stripeOrder.Amount)
	assert.Equal(t, displayAmount, stripeOrder.CreditQuota)

	payload := []byte(fmt.Sprintf(`{"id":"evt_token_display","type":"checkout.session.completed","data":{"object":{"id":"cs_test_1","client_reference_id":%q,"mode":"payment","status":"complete","payment_status":"paid","amount_total":200,"currency":"usd","metadata":{"order_type":"wallet","trade_no":%q}}}}`, stripeTrade, stripeTrade))
	webhookReq := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
	webhookReq.Header.Set("Stripe-Signature", signStripe(payload, "whsec_token_display", currentStripeTimestamp()))
	webhookRec := httptest.NewRecorder()
	handler.ServeHTTP(webhookRec, webhookReq)
	require.Equal(t, http.StatusOK, webhookRec.Code, webhookRec.Body.String())
	var user model.User
	require.NoError(t, model.DB.First(&user, uid).Error)
	assert.Equal(t, 1000+int(2*displayAmount), user.Quota)

	rec = do(http.MethodPost, "/api/user/stripe/pay",
		fmt.Sprintf(`{"amount":%d,"payment_method":"stripe"}`, displayAmount+1))
	assert.Equal(t, "充值数量超出安全范围或无法精确兑换", decodeBody(t, rec)["message"])
}

func TestTopUpEpayUnconfigured(t *testing.T) {
	_, do, _ := setupTopupTest(t)
	rec := do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"alipay"}`)
	body := decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "当前管理员未配置支付信息", body["data"])
}

func TestTopUpEpayFailsClosedOnComplianceAndUnsafeConfiguration(t *testing.T) {
	t.Run("compliance", func(t *testing.T) {
		_, do, _ := setupTopupTest(t)
		require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "https://pay.example.test"))
		require.NoError(t, setting.UpdateOption(setting.EpayIdOption, "pid"))
		require.NoError(t, setting.UpdateOption(setting.EpayKeyOption, "key"))
		setPaymentCompliance(t, false)

		rec := do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"alipay"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		var count int64
		require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
		assert.Zero(t, count)
	})

	for _, testCase := range []struct {
		name       string
		payAddress string
		partnerID  string
		key        string
		server     string
		callback   string
	}{
		{name: "plaintext remote gateway", payAddress: "http://pay.example.test", partnerID: "pid", key: "key", server: "https://router.example.test"},
		{name: "gateway credentials in URL", payAddress: "https://user:secret@pay.example.test", partnerID: "pid", key: "key", server: "https://router.example.test"},
		{name: "gateway query", payAddress: "https://pay.example.test?destination=other", partnerID: "pid", key: "key", server: "https://router.example.test"},
		{name: "unsafe partner", payAddress: "https://pay.example.test", partnerID: "pid\nother", key: "key", server: "https://router.example.test"},
		{name: "unsafe callback", payAddress: "https://pay.example.test", partnerID: "pid", key: "key", server: "https://router.example.test", callback: "https://user:secret@callback.example.test"},
		{name: "unsafe return", payAddress: "https://pay.example.test", partnerID: "pid", key: "key", server: "javascript:alert(1)"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, do, _ := setupTopupTest(t)
			require.NoError(t, setting.UpdateOptions(map[string]string{
				setting.PayAddressOption:            testCase.payAddress,
				setting.EpayIdOption:                testCase.partnerID,
				setting.EpayKeyOption:               testCase.key,
				setting.ServerAddressOption:         testCase.server,
				setting.CustomCallbackAddressOption: testCase.callback,
			}))

			rec := do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"alipay"}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "error", decodeBody(t, rec)["message"])
			var count int64
			require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
			assert.Zero(t, count)

			info := do(http.MethodGet, "/api/user/topup/info", "")
			require.Equal(t, http.StatusOK, info.Code, info.Body.String())
			assert.Equal(t, false, decodeBody(t, info)["data"].(map[string]any)["enable_online_topup"])
		})
	}
}

func TestTopUpEpayWebhookRejectsUnboundedOrAmbiguousParameters(t *testing.T) {
	handler, _, _ := setupTopupTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.PayAddressOption: "https://pay.example.test",
		setting.EpayIdOption:     "pid",
		setting.EpayKeyOption:    "key",
	}))

	request := func(method, encoded string) *httptest.ResponseRecorder {
		t.Helper()
		path := "/api/user/epay/notify"
		body := ""
		if method == http.MethodGet {
			path += "?" + encoded
		} else {
			body = encoded
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	many := url.Values{}
	for index := 0; index <= 32; index++ {
		many.Set(fmt.Sprintf("field_%d", index), "value")
	}
	assert.Equal(t, "fail", request(http.MethodPost, many.Encode()).Body.String())
	assert.Equal(t, "fail", request(http.MethodGet, "pid=first&pid=second").Body.String())
	assert.Equal(t, "fail", request(http.MethodPost, "pid="+strings.Repeat("x", 256)).Body.String())
}

func TestPaymentOrderEntropyFailureDoesNotCreateOrder(t *testing.T) {
	for _, test := range []struct {
		name      string
		path      string
		body      string
		configure func(*testing.T)
	}{
		{
			name: "epay", path: "/api/user/pay", body: `{"amount":10,"payment_method":"alipay"}`,
			configure: func(t *testing.T) {
				require.NoError(t, setting.UpdateOption(setting.PriceOption, "1"))
				require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "https://pay.example.com"))
				require.NoError(t, setting.UpdateOption(setting.EpayIdOption, "pid"))
				require.NoError(t, setting.UpdateOption(setting.EpayKeyOption, "key"))
			},
		},
		{
			name: "stripe wallet", path: "/api/user/stripe/pay", body: `{"amount":10,"payment_method":"stripe"}`,
			configure: func(t *testing.T) {
				t.Setenv("STRIPE_SECRET_KEY", "sk_test_entropy")
				t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_entropy")
				require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "1"))
				require.NoError(t, setting.UpdateOption(setting.StripeCurrencyOption, "USD"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, do, _ := setupTopupTest(t)
			test.configure(t)
			restore := cryptoutil.SetSecureRandomReaderForTesting(paymentEntropyFailureReader{})
			t.Cleanup(restore)

			rec := do(http.MethodPost, test.path, test.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "error", decodeBody(t, rec)["message"])
			var count int64
			require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestTopUpStripeAmountContract(t *testing.T) {
	_, do, _ := setupTopupTest(t)
	require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "3"))

	rec := do(http.MethodPost, "/api/user/stripe/amount", `{"amount":5}`)
	body := decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	assert.Equal(t, "15.00", body["data"])

	rec = do(http.MethodPost, "/api/user/stripe/amount", `{"amount":0}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "充值数量不能小于 1", body["data"])
}

func TestTopUpRequestsFailClosedOnUnsafeAmountsAndPricing(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		body      string
		configure func(t *testing.T)
		message   string
	}{
		{
			name: "amount above quota domain",
			path: "/api/user/amount", body: fmt.Sprintf(`{"amount":%d}`, quotamath.MaxQuota+1),
			message: "error",
		},
		{
			name: "epay amount above quota domain",
			path: "/api/user/pay", body: fmt.Sprintf(`{"amount":%d,"payment_method":"alipay"}`, quotamath.MaxQuota+1),
			message: "error",
		},
		{
			name: "stripe amount above quota domain",
			path: "/api/user/stripe/amount", body: fmt.Sprintf(`{"amount":%d}`, quotamath.MaxQuota+1),
			message: "error",
		},
		{
			name: "NaN epay price",
			path: "/api/user/amount", body: `{"amount":10}`,
			configure: func(t *testing.T) { require.NoError(t, setting.UpdateOption(setting.PriceOption, "NaN")) },
			message:   "error",
		},
		{
			name: "infinite epay price",
			path: "/api/user/pay", body: `{"amount":10,"payment_method":"alipay"}`,
			configure: func(t *testing.T) { require.NoError(t, setting.UpdateOption(setting.PriceOption, "+Inf")) },
			message:   "error",
		},
		{
			name: "zero stripe price",
			path: "/api/user/stripe/amount", body: `{"amount":10}`,
			configure: func(t *testing.T) { require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "0")) },
			message:   "error",
		},
		{
			name: "nonpositive discount",
			path: "/api/user/amount", body: `{"amount":10}`,
			configure: func(t *testing.T) {
				require.NoError(t, setting.UpdateOption(setting.PaymentSettingOption,
					`{"amount_discount":{"10":0}}`))
			},
			message: "error",
		},
		{
			name: "overflowing epay calculation",
			path: "/api/user/amount", body: fmt.Sprintf(`{"amount":%d}`, quotamath.MaxQuota),
			configure: func(t *testing.T) {
				require.NoError(t, setting.UpdateOption(setting.PriceOption, "1.7976931348623157e308"))
			},
			message: "error",
		},
		{
			name: "overflowing token minimum",
			path: "/api/user/amount", body: `{"amount":10}`,
			configure: func(t *testing.T) {
				require.NoError(t, setting.UpdateOption(setting.QuotaDisplayTypeOption, setting.QuotaDisplayTypeTokens))
				require.NoError(t, setting.UpdateOption(setting.MinTopUpOption,
					fmt.Sprintf("%d", quotamath.MaxQuota/int64(quotamath.QuotaPerUnit)+1)))
			},
			message: "error",
		},
		{
			name: "invalid group blocks stripe checkout",
			path: "/api/user/stripe/pay", body: `{"amount":10,"payment_method":"stripe"}`,
			configure: func(t *testing.T) {
				billingsvc.SetTopUpGroupRatios(map[string]float64{"default": math.NaN()})
				require.NoError(t, model.DB.Model(&model.User{}).Where("1 = 1").Update("group", "default").Error)
			},
			message: "充值配置无效",
		},
		{
			name: "invalid currency blocks stripe checkout",
			path: "/api/user/stripe/pay", body: `{"amount":10,"payment_method":"stripe"}`,
			configure: func(t *testing.T) {
				require.NoError(t, setting.UpdateOption(setting.StripeCurrencyOption, "US1"))
			},
			message: "充值配置无效",
		},
		{
			name: "promotion codes cannot bypass exact amount binding",
			path: "/api/user/stripe/pay", body: `{"amount":10,"payment_method":"stripe"}`,
			configure: func(t *testing.T) {
				require.NoError(t, setting.UpdateOption(setting.StripePromotionCodesOption, "true"))
			},
			message: "充值配置无效",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, do, _ := setupTopupTest(t)
			if test.configure != nil {
				test.configure(t)
			}
			rec := do(http.MethodPost, test.path, test.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, test.message, decodeBody(t, rec)["message"], rec.Body.String())
			var orders int64
			require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&orders).Error)
			assert.Zero(t, orders)
		})
	}
}

// stripeMock records the last checkout-session request and returns a canned
// session response.
type stripeMock struct {
	mu                   sync.Mutex
	server               *httptest.Server
	last                 map[string]any
	requests             int
	response             string
	priceResponse        string
	subscriptionAmount   string
	subscriptionCurrency string
	sessionSequence      int
}

func newStripeMock(t *testing.T, response string) *stripeMock {
	t.Helper()
	m := &stripeMock{response: response, subscriptionAmount: "4000", subscriptionCurrency: "usd"}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/prices/") {
			m.mu.Lock()
			priceResponse := m.priceResponse
			m.mu.Unlock()
			if priceResponse == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"price not found"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(priceResponse))
			return
		}
		_ = r.ParseForm()
		m.mu.Lock()
		m.last = map[string]any{}
		for key, values := range r.Form {
			if len(values) == 1 {
				m.last[key] = values[0]
			} else {
				m.last[key] = values
			}
		}
		m.last["metadata"] = map[string]any{
			"order_type": r.Form.Get("metadata[order_type]"),
			"user_id":    r.Form.Get("metadata[user_id]"),
			"trade_no":   r.Form.Get("metadata[trade_no]"),
			"quota":      r.Form.Get("metadata[quota]"),
			"price_id":   r.Form.Get("metadata[price_id]"),
		}
		m.sessionSequence++
		sequence := m.sessionSequence
		response := m.response
		subscriptionAmount := m.subscriptionAmount
		subscriptionCurrency := m.subscriptionCurrency
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if response != "" {
			_, _ = w.Write([]byte(response))
			return
		}
		amount := r.Form.Get("line_items[0][price_data][unit_amount]")
		currency := r.Form.Get("line_items[0][price_data][currency]")
		if amount == "" {
			amount = subscriptionAmount
			currency = subscriptionCurrency
		}
		referenceID := r.Form.Get("client_reference_id")
		orderType := r.Form.Get("metadata[order_type]")
		_, _ = fmt.Fprintf(w, `{"id":"cs_test_%d","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test_%d","client_reference_id":%q,"metadata":{"order_type":%q,"trade_no":%q,"price_id":%q},"mode":%q,"amount_total":%s,"currency":%q,"payment_status":"unpaid","status":"open"}`,
			sequence, sequence, referenceID, orderType, referenceID, r.Form.Get("metadata[price_id]"), r.Form.Get("mode"), amount, currency)
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *stripeMock) lastRequestBody() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func (m *stripeMock) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func TestTopUpStripePayContract(t *testing.T) {
	_, do, uid := setupTopupTest(t)

	// Wrong payment method.
	rec := do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"alipay"}`)
	body := decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "不支持的支付渠道", body["data"])

	// Out of range.
	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":0,"payment_method":"stripe"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "充值数量不能小于 1", body["message"])
	assert.Equal(t, float64(10), body["data"])
	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10001,"payment_method":"stripe"}`)
	assert.Equal(t, "充值数量不能大于 10000", decodeBody(t, rec)["message"])

	// Untrusted redirect URL.
	rec = do(http.MethodPost, "/api/user/stripe/pay",
		`{"amount":10,"payment_method":"stripe","success_url":"https://evil.example.com/x"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "支付成功重定向URL不在可信任域名列表中", decodeBody(t, rec)["message"])

	// Without both Stripe credentials the endpoint fails before persisting an
	// order that could never be safely fulfilled.
	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "Stripe 未配置或回调不可用", body["data"])
	var orderCount int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("user_id = ?", uid).Count(&orderCount).Error)
	assert.Zero(t, orderCount)

	// With a key and a mocked checkout endpoint the session is created, the
	// order recorded, and the pay link returned.
	mock := newStripeMock(t, "")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_tokenrouter")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://payments.example.test"))
	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	assert.Equal(t, "Stripe 回调地址配置无效", decodeBody(t, rec)["data"])
	require.NoError(t, model.DB.Model(&model.TopUp{}).Where("user_id = ?", uid).Count(&orderCount).Error)
	assert.Zero(t, orderCount)
	assert.Zero(t, mock.requestCount(), "invalid defaults must fail before contacting Stripe")
	require.NoError(t, setting.UpdateOption(setting.ServerAddressOption, "http://localhost:3000"))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", uid).Update("group", "vip").Error)
	require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "3"))
	require.NoError(t, setting.UpdateOption(setting.PaymentSettingOption, `{"amount_discount":{"10":0.5}}`))
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(mock.server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	payData, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://checkout.stripe.com/c/pay/cs_test_1", payData["pay_link"])

	last := mock.lastRequestBody()
	referenceID, _ := last["client_reference_id"].(string)
	var order model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", referenceID).First(&order).Error)
	assert.Equal(t, "stripe", order.PaymentMethod)
	assert.Equal(t, billingsvc.PaymentProviderStripe, order.PaymentProvider)
	assert.Equal(t, int64(10), order.Amount)
	assert.Equal(t, int64(10*quotamath.QuotaPerUnit), order.CreditQuota)
	assert.Equal(t, billingsvc.TopUpStatusPending, order.Status)
	assert.Equal(t, 30.0, order.Money, "10 * $3 * vip ratio 2 * discount 0.5")
	assert.Equal(t, int64(3000), order.ProviderAmountMinor)
	assert.Equal(t, "USD", order.ProviderCurrency)
	assert.Equal(t, billingsvc.StripeCheckoutBindingVersion, order.ProviderBindingVersion)
	require.NotNil(t, order.ProviderSessionId)
	assert.Equal(t, "cs_test_1", *order.ProviderSessionId)
	assert.Equal(t, order.TradeNo, last["client_reference_id"])
	metadata := last["metadata"].(map[string]any)
	assert.Equal(t, "10", metadata["quota"])
	assert.Equal(t, fmt.Sprintf("%d", uid), metadata["user_id"])
	assert.Equal(t, order.TradeNo, metadata["trade_no"])
	assert.Equal(t, billingsvc.StripeOrderTypeWallet, metadata["order_type"])
	assert.Equal(t, "3000", last["line_items[0][price_data][unit_amount]"])
	assert.Equal(t, "usd", last["line_items[0][price_data][currency]"])
	assert.Equal(t, "1", last["line_items[0][quantity]"])
	assert.Empty(t, last["line_items[0][price]"])
}

func TestTopUpStripePersistsBeforeCheckoutAndMarksProviderFailure(t *testing.T) {
	_, do, uid := setupTopupTest(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_order_first")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	var calls atomic.Int32
	var sawPending atomic.Bool
	var providerTradeNo atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tradeNo := r.Form.Get("client_reference_id")
		providerTradeNo.Store(tradeNo)
		var order model.TopUp
		if err := model.DB.Where("trade_no = ?", tradeNo).First(&order).Error; err == nil && order.Status == billingsvc.TopUpStatusPending {
			sawPending.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"injected checkout failure"}}`))
	}))
	t.Cleanup(server.Close)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

	injected := fmt.Errorf("injected top-up create failure")
	const callback = "test:fail_stripe_topup_order_create"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "top_ups" {
			tx.AddError(injected)
		}
	}))
	rec := do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	assert.Equal(t, "创建订单失败", decodeBody(t, rec)["data"])
	assert.Zero(t, calls.Load(), "Checkout must not be opened when the local order cannot be persisted")
	require.NoError(t, model.DB.Callback().Create().Remove(callback))

	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	assert.Equal(t, "拉起支付失败", decodeBody(t, rec)["data"])
	assert.Equal(t, int32(1), calls.Load())
	assert.True(t, sawPending.Load(), "provider must observe an already-persisted pending order")
	tradeNo, ok := providerTradeNo.Load().(string)
	require.True(t, ok)
	var failed model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ? AND user_id = ?", tradeNo, uid).First(&failed).Error)
	assert.Equal(t, billingsvc.TopUpStatusFailed, failed.Status)
	assert.NotZero(t, failed.CompleteTime)
	assert.Empty(t, failed.ReconciliationState, "a definite Stripe rejection is terminal, not ambiguous")
}

func TestTopUpStripeAmbiguousCheckoutOutcomeStaysPending(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		response       func(url.Values) string
		reconciliation string
	}{
		{
			name:   "provider 5xx",
			status: http.StatusInternalServerError,
			response: func(url.Values) string {
				return `{"error":{"type":"api_error","message":"temporary"}}`
			},
			reconciliation: billingsvc.StripeReconciliationCreationUnknown,
		},
		{
			name:   "mismatched success response",
			status: http.StatusOK,
			response: func(form url.Values) string {
				ref := form.Get("client_reference_id")
				return fmt.Sprintf(`{"id":"cs_mismatch","url":"https://checkout.stripe.com/c/pay/cs_mismatch","client_reference_id":%q,"mode":"payment","amount_total":999,"currency":"usd","metadata":{"order_type":"wallet","trade_no":%q}}`, ref, ref)
			},
			reconciliation: billingsvc.StripeReconciliationBindingMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, do, uid := setupTopupTest(t)
			t.Setenv("STRIPE_SECRET_KEY", "sk_test_ambiguous")
			t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
			require.NoError(t, setting.UpdateOption(setting.StripeUnitPriceOption, "1"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, r.ParseForm())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.response(r.Form)))
			}))
			t.Cleanup(server.Close)
			stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL)}))
			t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

			rec := do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
			assert.Equal(t, "拉起支付失败", decodeBody(t, rec)["data"])
			var order model.TopUp
			require.NoError(t, model.DB.Where("user_id = ?", uid).Order("id desc").First(&order).Error)
			assert.Equal(t, billingsvc.TopUpStatusPending, order.Status)
			assert.Zero(t, order.CompleteTime)
			assert.Equal(t, test.reconciliation, order.ReconciliationState)
		})
	}
}
