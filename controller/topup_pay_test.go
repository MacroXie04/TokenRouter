package controller_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	epay "github.com/Calcium-Ion/go-epay/epay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

// setupTopupTest builds a root session with the payment options reset and
// compliance confirmed.
func setupTopupTest(t *testing.T) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	handler, do, uid := setupChannelRead(t, constant.RoleRootUser)
	setPaymentCompliance(t, true)
	for _, key := range []string{
		setting.PayMethodsOption, setting.PriceOption, setting.MinTopUpOption,
		setting.PayAddressOption, setting.EpayIdOption, setting.EpayKeyOption,
		setting.StripeMinTopUpOption, setting.StripePriceIdOption, setting.StripeUnitPriceOption,
		setting.QuotaDisplayTypeOption, setting.PaymentSettingOption, setting.CustomCallbackAddressOption,
	} {
		require.NoError(t, setting.UpdateOption(key, ""))
	}
	service.SetGroupRatios(map[string]float64{"default": 1.0, "vip": 2.0})
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

	// Compliance confirmed: default methods, presets, topup link.
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
	assert.NotEmpty(t, data["topup_link"])

	// Stripe configured: the stripe method is appended with its minimum.
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_x")
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
	assert.Equal(t, "alipay", order.PaymentMethod)
	assert.Equal(t, service.PaymentProviderEpay, order.PaymentProvider)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.InDelta(t, 10.0, order.Money, 0.001)

	// Unknown payment method.
	rec = do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"bogus"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "支付方式不存在", body["data"])

	// The gateway notifies success with a fresh signed callback param set
	// (the signature covers every callback field): the order settles and the
	// user quota is credited exactly once.
	notifyParams := epay.GenerateParams(map[string]string{
		"pid":          "pid123",
		"trade_no":     "gateway-trade-1",
		"out_trade_no": tradeNo,
		"type":         "alipay",
		"name":         "TUC10",
		"money":        "10.00",
		"trade_status": epay.StatusTradeSuccess,
	}, "secret-key")
	form := url.Values{}
	for k, v := range notifyParams {
		form.Set(k, v)
	}
	notify := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req)
		return rec2
	}
	rec2 := notify()
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Equal(t, "success", rec2.Body.String())

	var settled model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&settled).Error)
	assert.Equal(t, service.TopUpStatusSuccess, settled.Status)
	assert.Greater(t, settled.CompleteTime, int64(0))
	var u model.User
	require.NoError(t, model.DB.First(&u, uid).Error)
	assert.Greater(t, u.Quota, 1000, "top-up quota credited")

	// A duplicate delivery is idempotent.
	rec3 := notify()
	assert.Equal(t, "success", rec3.Body.String())
	var u2 model.User
	require.NoError(t, model.DB.First(&u2, uid).Error)
	assert.Equal(t, u.Quota, u2.Quota)

	// A tampered signature is rejected.
	form.Set("sign", "bad")
	rec4 := notify()
	assert.Equal(t, "fail", rec4.Body.String())
}

func TestTopUpEpayUnconfigured(t *testing.T) {
	_, do, _ := setupTopupTest(t)
	rec := do(http.MethodPost, "/api/user/pay", `{"amount":10,"payment_method":"alipay"}`)
	body := decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "当前管理员未配置支付信息", body["data"])
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

// stripeMock records the last checkout-session request and returns a canned
// session response.
type stripeMock struct {
	mu     sync.Mutex
	server *httptest.Server
	last   map[string]any
}

func newStripeMock(t *testing.T, response string) *stripeMock {
	t.Helper()
	m := &stripeMock{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if key := r.Form.Get("metadata[user_id]"); key != "" {
			m.last["metadata"] = map[string]any{
				"user_id":  key,
				"trade_no": r.Form.Get("metadata[trade_no]"),
				"quota":    r.Form.Get("metadata[quota]"),
			}
		}
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *stripeMock) lastRequestBody() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
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

	// Without a Stripe API key the session creation fails with the reference
	// error shape.
	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	body = decodeBody(t, rec)
	assert.Equal(t, "error", body["message"])
	assert.Equal(t, "拉起支付失败", body["data"])

	// With a key and a mocked checkout endpoint the session is created, the
	// order recorded, and the pay link returned.
	mock := newStripeMock(t, `{"id":"cs_test","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test","client_reference_id":"ref_x","metadata":{"user_id":"1","trade_no":"ref_x","quota":"10"},"line_items":{"object":"list","data":[{"price":{"id":"price_x"},"quantity":10}],"has_more":false},"mode":"payment","success_url":"http://s","cancel_url":"http://c","payment_status":"unpaid","status":"open"}`)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_tokenrouter")
	require.NoError(t, setting.UpdateOption(setting.StripePriceIdOption, "price_x"))
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(mock.server.URL)}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, nil) })

	rec = do(http.MethodPost, "/api/user/stripe/pay", `{"amount":10,"payment_method":"stripe"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, "success", body["message"])
	payData, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://checkout.stripe.com/c/pay/cs_test", payData["pay_link"])

	var order model.TopUp
	require.NoError(t, model.DB.Where("user_id = ?", uid).First(&order).Error)
	assert.Equal(t, "stripe", order.PaymentMethod)
	assert.Equal(t, service.PaymentProviderStripe, order.PaymentProvider)
	assert.Equal(t, int64(10), order.Amount)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	last := mock.lastRequestBody()
	assert.Equal(t, order.TradeNo, last["client_reference_id"])
	metadata := last["metadata"].(map[string]any)
	assert.Equal(t, "10", metadata["quota"])
	assert.Equal(t, fmt.Sprintf("%d", uid), metadata["user_id"])
	assert.Equal(t, order.TradeNo, metadata["trade_no"])
}
