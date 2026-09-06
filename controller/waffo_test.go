package controller_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/controller"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

type waffoRoundTripFunc func(*http.Request) (*http.Response, error)

func (f waffoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type waffoTestKeys struct {
	merchantPrivate string
	merchantPublic  string
	providerPrivate string
	providerPublic  string
}

var (
	waffoKeysOnce sync.Once
	waffoKeys     waffoTestKeys
	waffoKeysErr  error
)

func controllerWaffoKeys(t *testing.T) waffoTestKeys {
	t.Helper()
	waffoKeysOnce.Do(func() {
		waffoKeys.merchantPrivate, waffoKeys.merchantPublic, waffoKeysErr = controllerWaffoKeyPair()
		if waffoKeysErr == nil {
			waffoKeys.providerPrivate, waffoKeys.providerPublic, waffoKeysErr = controllerWaffoKeyPair()
		}
	})
	require.NoError(t, waffoKeysErr)
	return waffoKeys
}

func controllerWaffoKeyPair() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(privateDER), base64.StdEncoding.EncodeToString(publicDER), nil
}

func signWaffoFixture(t *testing.T, payload []byte, privateKey string) string {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(privateKey)
	require.NoError(t, err)
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	require.NoError(t, err)
	key, ok := parsed.(*rsa.PrivateKey)
	require.True(t, ok)
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(signature)
}

func verifyWaffoFixture(t *testing.T, payload []byte, signature, publicKey string) {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(publicKey)
	require.NoError(t, err)
	parsed, err := x509.ParsePKIXPublicKey(der)
	require.NoError(t, err)
	key, ok := parsed.(*rsa.PublicKey)
	require.True(t, ok)
	decoded, err := base64.StdEncoding.DecodeString(signature)
	require.NoError(t, err)
	digest := sha256.Sum256(payload)
	require.NoError(t, rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], decoded))
}

func configureWaffoForController(t *testing.T, keys waffoTestKeys) {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ServerAddressOption:         "https://merchant.example.test",
		setting.CustomCallbackAddressOption: "https://callback.example.test",
		setting.WaffoEnabledOption:          "true",
		setting.WaffoSandboxOption:          "false",
		setting.WaffoAPIKeyOption:           "waffo-api-key",
		setting.WaffoPrivateKeyOption:       keys.merchantPrivate,
		setting.WaffoPublicCertOption:       keys.providerPublic,
		setting.WaffoMerchantIDOption:       "merchant_123",
		setting.WaffoCurrencyOption:         "USD",
		setting.WaffoUnitPriceOption:        "1.25",
		setting.WaffoMinTopUpOption:         "1",
		setting.WaffoPayMethodsOption:       `[{"name":"Card","icon":"/pay-card.png","payMethodType":"CREDITCARD,DEBITCARD","payMethodName":""},{"name":"Apple Pay","icon":"/pay-apple.png","payMethodType":"APPLEPAY","payMethodName":"APPLEPAY"}]`,
	}))
	t.Cleanup(func() {
		_ = setting.UpdateOptions(map[string]string{
			setting.WaffoEnabledOption: "false", setting.WaffoAPIKeyOption: "",
			setting.WaffoPrivateKeyOption: "", setting.WaffoPublicCertOption: "",
			setting.WaffoPayMethodsOption: "",
		})
	})
}

func signedWaffoCreateResponse(t *testing.T, keys waffoTestKeys, request service.WaffoCheckoutRequest, acquiringID, paymentURL string) *http.Response {
	t.Helper()
	action, err := common.Marshal(map[string]string{"actionType": "WEB", "webUrl": paymentURL})
	require.NoError(t, err)
	body, err := common.Marshal(map[string]any{
		"code": "0",
		"data": map[string]any{
			"paymentRequestId": request.PaymentRequestID,
			"merchantOrderId":  request.MerchantOrderID,
			"acquiringOrderId": acquiringID,
			"orderStatus":      "AUTHORIZATION_REQUIRED",
			"orderAction":      string(action),
		},
	})
	require.NoError(t, err)
	header := make(http.Header)
	header.Set(controller.WaffoSignatureHeader, signWaffoFixture(t, body, keys.providerPrivate))
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}
}

func waffoWebhookPayload(t *testing.T, order model.TopUp, status, amount, currency, merchantID, acquiringID string) []byte {
	t.Helper()
	body, err := common.Marshal(map[string]any{
		"eventType": "PAYMENT_NOTIFICATION",
		"result": map[string]any{
			"paymentRequestId": order.TradeNo, "merchantOrderId": order.TradeNo,
			"acquiringOrderId": acquiringID, "orderStatus": status,
			"orderCurrency": currency, "orderAmount": amount,
			"merchantInfo": map[string]any{"merchantId": merchantID},
			"userInfo":     map[string]any{"userId": fmt.Sprintf("%d", order.UserId)},
			"paymentInfo":  map[string]any{"productName": service.WaffoProductOneTimePayment},
		},
	})
	require.NoError(t, err)
	return body
}

func sendWaffoWebhook(handler http.Handler, keys waffoTestKeys, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(controller.WaffoSignatureHeader, signWaffoFixtureForRequest(body, keys.providerPrivate))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func signWaffoFixtureForRequest(payload []byte, privateKey string) string {
	der, _ := base64.StdEncoding.DecodeString(privateKey)
	parsed, _ := x509.ParsePKCS8PrivateKey(der)
	key, _ := parsed.(*rsa.PrivateKey)
	digest := sha256.Sum256(payload)
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return base64.StdEncoding.EncodeToString(signature)
}

func TestWaffoHTTPWireSnapshotAndRetryableSettlementEndToEnd(t *testing.T) {
	handler, do, userID, _ := setupSubscriptionStripeTest(t)
	keys := controllerWaffoKeys(t)
	configureWaffoForController(t, keys)

	var captured service.WaffoCheckoutRequest
	restore := controller.SetWaffoTransportForTesting(waffoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, setting.WaffoProductionBaseURL+"/order/create", request.URL.String())
		require.Equal(t, "application/json", request.Header.Get("Content-Type"))
		require.Equal(t, "waffo-api-key", request.Header.Get("X-API-KEY"))
		require.Equal(t, "1.0.0", request.Header.Get("X-API-VERSION"))
		body, err := common.ReadAllLimited(request.Body, 64<<10)
		require.NoError(t, err)
		verifyWaffoFixture(t, body, request.Header.Get(controller.WaffoSignatureHeader), keys.merchantPublic)
		require.NoError(t, common.Unmarshal(body, &captured))
		return signedWaffoCreateResponse(t, keys, captured, "acq_wallet_exact", "https://checkout.waffo.test/pay/exact"), nil
	}))
	t.Cleanup(restore)

	for _, path := range []string{"/api/user/waffo/amount", "/api/user/waffo/pay"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"amount":10}`))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, path)
	}

	recorder := do(http.MethodPost, "/api/user/waffo/amount", `{"amount":10}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "12.50", decodeBody(t, recorder)["data"])

	recorder = do(http.MethodPost, "/api/user/waffo/pay", `{"amount":10,"pay_method_index":1}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	require.Equal(t, "success", body["message"])
	data := body["data"].(map[string]any)
	assert.Equal(t, "https://checkout.waffo.test/pay/exact", data["payment_url"])
	tradeNo := data["order_id"].(string)
	assert.True(t, strings.HasPrefix(tradeNo, "WAFFO-"))
	assert.Equal(t, tradeNo, captured.PaymentRequestID)
	assert.Equal(t, "12.50", captured.OrderAmount)
	assert.Equal(t, "USD", captured.OrderCurrency)
	assert.Equal(t, "merchant_123", captured.MerchantInfo.MerchantID)
	assert.Equal(t, "APPLEPAY", captured.PaymentInfo.PayMethodType)
	assert.Equal(t, "APPLEPAY", captured.PaymentInfo.PayMethodName)
	assert.Equal(t, "https://callback.example.test/api/waffo/webhook", captured.NotifyURL)
	assert.Equal(t, "https://merchant.example.test/wallet?show_history=true", captured.SuccessRedirectURL)

	var order model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", tradeNo).First(&order).Error)
	assert.Equal(t, service.TopUpStatusPending, order.Status)
	assert.Equal(t, service.PaymentProviderWaffo, order.PaymentProvider)
	assert.Equal(t, int64(1250), order.ProviderAmountMinor)
	require.NotNil(t, order.ProviderSessionId)
	assert.Equal(t, "acq_wallet_exact", *order.ProviderSessionId)
	assert.Equal(t, order.CheckoutRequest, string(mustWaffoJSON(t, captured)))

	// Mutable pricing/catalog values cannot change a pending order's economics.
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.WaffoUnitPriceOption: "9.99", setting.WaffoCurrencyOption: "EUR", setting.WaffoMerchantIDOption: "merchant_new",
	}))
	badAmount := waffoWebhookPayload(t, order, "PAY_SUCCESS", "12.49", "USD", "merchant_123", "acq_wallet_exact")
	recorder = sendWaffoWebhook(handler, keys, badAmount)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"message":"failed"}`, recorder.Body.String())
	verifyWaffoFixture(t, recorder.Body.Bytes(), recorder.Header().Get(controller.WaffoSignatureHeader), keys.merchantPublic)

	valid := waffoWebhookPayload(t, order, "PAY_SUCCESS", "12.50", "USD", "merchant_123", "acq_wallet_exact")
	injected := errors.New("injected Waffo outbox failure")
	const callback = "test:controller_waffo_outbox_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AuditLogOutbox{}).TableName() {
			tx.AddError(injected)
		}
	}))
	recorder = sendWaffoWebhook(handler, keys, valid)
	assert.JSONEq(t, `{"message":"failed"}`, recorder.Body.String(), "storage failure must ask Waffo to retry")
	require.NoError(t, model.DB.Callback().Create().Remove(callback))
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 1000, user.Quota)

	recorder = sendWaffoWebhook(handler, keys, valid)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"message":"success"}`, recorder.Body.String())
	verifyWaffoFixture(t, recorder.Body.Bytes(), recorder.Header().Get(controller.WaffoSignatureHeader), keys.merchantPublic)
	recorder = sendWaffoWebhook(handler, keys, valid)
	assert.JSONEq(t, `{"message":"success"}`, recorder.Body.String())
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 1000+10*common.QuotaPerUnit, user.Quota)
	var logs int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ?", userID).Count(&logs).Error)
	assert.Equal(t, int64(1), logs)
}

func TestWaffoCheckoutFailuresRemainDurableOrFailTerminally(t *testing.T) {
	_, do, _, _ := setupSubscriptionStripeTest(t)
	keys := controllerWaffoKeys(t)
	configureWaffoForController(t, keys)

	mode := "transport"
	restore := controller.SetWaffoTransportForTesting(waffoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := common.ReadAllLimited(request.Body, 64<<10)
		require.NoError(t, err)
		var checkout service.WaffoCheckoutRequest
		require.NoError(t, common.Unmarshal(body, &checkout))
		switch mode {
		case "transport":
			return nil, errors.New("transport failed with waffo-api-key and private material")
		case "rejected":
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"secret detail"}`))}, nil
		case "business-rejected":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"E123","msg":"rejected"}`))}, nil
		case "unknown-status":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"E0001","msg":"unknown"}`))}, nil
		case "invalid-response-signature":
			response := signedWaffoCreateResponse(t, keys, checkout, "acq_bad_signature", "https://checkout.waffo.test/bad-signature")
			response.Header.Set(controller.WaffoSignatureHeader, "invalid")
			return response, nil
		case "unsafe-url":
			return signedWaffoCreateResponse(t, keys, checkout, "acq_unsafe", "javascript:alert(1)"), nil
		case "oversized":
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (64<<10)+1)))}, nil
		default:
			panic("unknown mode")
		}
	}))
	t.Cleanup(restore)

	for _, currentMode := range []string{"transport", "rejected", "business-rejected", "unknown-status", "invalid-response-signature", "unsafe-url", "oversized"} {
		mode = currentMode
		recorder := do(http.MethodPost, "/api/user/waffo/pay", `{"amount":10}`)
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "拉起支付失败", decodeBody(t, recorder)["data"])
		assert.NotContains(t, recorder.Body.String(), "waffo-api-key")
		var order model.TopUp
		require.NoError(t, model.DB.Order("id desc").First(&order).Error)
		if currentMode == "rejected" || currentMode == "business-rejected" {
			assert.Equal(t, service.TopUpStatusFailed, order.Status)
		} else {
			assert.Equal(t, service.TopUpStatusPending, order.Status)
			assert.Equal(t, service.WaffoReconciliationCreate, order.ReconciliationState)
		}
	}

	before := int64(0)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&before).Error)
	recorder := do(http.MethodPost, "/api/user/waffo/pay", `{"amount":10,"pay_method_index":99}`)
	assert.Equal(t, "不支持的支付方式", decodeBody(t, recorder)["data"])
	recorder = do(http.MethodPost, "/api/user/waffo/pay", `{"amount":10,"pay_method_type":"APPLEPAY","pay_method_name":"forged"}`)
	assert.Equal(t, "不支持的支付方式", decodeBody(t, recorder)["data"])
	recorder = do(http.MethodPost, "/api/user/waffo/pay", `{"amount":10,"pay_method_name":"APPLEPAY"}`)
	assert.Equal(t, "不支持的支付方式", decodeBody(t, recorder)["data"])
	after := int64(0)
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&after).Error)
	assert.Equal(t, before, after)

	recorder = do(http.MethodPost, "/api/user/waffo/pay", strings.Repeat(" ", (64<<10)+1))
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

type injectedWaffoSignatureCodec struct {
	verifiedPayload string
	verifiedSig     string
	signedPayload   string
}

func (codec *injectedWaffoSignatureCodec) Sign(payload []byte, _ string) (string, error) {
	codec.signedPayload = string(payload)
	return "injected-response-signature", nil
}

func (codec *injectedWaffoSignatureCodec) Verify(payload []byte, signature, _ string) bool {
	codec.verifiedPayload = string(payload)
	codec.verifiedSig = signature
	return signature == "injected-request-signature"
}

func TestWaffoSignatureBoundaryIsInjectable(t *testing.T) {
	handler, _, _, _ := setupSubscriptionStripeTest(t)
	keys := controllerWaffoKeys(t)
	configureWaffoForController(t, keys)
	codec := &injectedWaffoSignatureCodec{}
	restore := controller.SetWaffoSignatureCodecForTesting(codec)
	t.Cleanup(restore)

	body := `{"eventType":"REFUND_NOTIFICATION"}`
	request := httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", strings.NewReader(body))
	request.Header.Set(controller.WaffoSignatureHeader, "injected-request-signature")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{"message":"success"}`, recorder.Body.String())
	assert.Equal(t, body, codec.verifiedPayload)
	assert.Equal(t, "injected-request-signature", codec.verifiedSig)
	assert.Equal(t, `{"message":"success"}`, codec.signedPayload)
	assert.Equal(t, "injected-response-signature", recorder.Header().Get(controller.WaffoSignatureHeader))
}

func TestWaffoWebhookSignatureBoundsAndTerminalStatuses(t *testing.T) {
	handler, _, userID, _ := setupSubscriptionStripeTest(t)
	keys := controllerWaffoKeys(t)
	configureWaffoForController(t, keys)
	config, err := setting.GetWaffoConfigChecked()
	require.NoError(t, err)
	order, _, err := service.CreateBoundWaffoTopUp(service.WaffoCheckoutSpec{
		UserID: userID, Amount: 2, RequestedAmount: 2, OrderAmount: "2.50", Currency: "USD",
		MerchantID: "merchant_123", NotifyURL: "https://callback.example.test/api/waffo/webhook",
		ReturnURL: "https://merchant.example.test/wallet", AppName: "TokenRouter",
		RequestedAt: "2026-09-05T01:02:03.000Z", Sandbox: config.Sandbox,
	}, "WAFFO-terminal-1700000000000-ABC123")
	require.NoError(t, err)

	validPending := waffoWebhookPayload(t, *order, "PAY_IN_PROGRESS", "2.50", "USD", "merchant_123", "acq_terminal")
	recorder := sendWaffoWebhook(handler, keys, validPending)
	assert.JSONEq(t, `{"message":"success"}`, recorder.Body.String())
	var stored model.TopUp
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, service.TopUpStatusPending, stored.Status)
	assert.Nil(t, stored.ProviderSessionId, "nonterminal notifications do not claim a provider order")

	closed := waffoWebhookPayload(t, *order, "ORDER_CLOSE", "2.50", "USD", "merchant_123", "acq_terminal")
	recorder = sendWaffoWebhook(handler, keys, closed)
	assert.JSONEq(t, `{"message":"success"}`, recorder.Body.String())
	require.NoError(t, model.DB.First(&stored, order.Id).Error)
	assert.Equal(t, service.TopUpStatusFailed, stored.Status)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 1000, user.Quota)

	invalidSignature := httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", strings.NewReader(string(closed)))
	invalidSignature.Header.Set(controller.WaffoSignatureHeader, "invalid")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, invalidSignature)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	oversized := []byte(strings.Repeat("x", (64<<10)+1))
	recorder = sendWaffoWebhook(handler, keys, oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)

	unknown, err := common.Marshal(map[string]any{"eventType": "REFUND_NOTIFICATION"})
	require.NoError(t, err)
	recorder = sendWaffoWebhook(handler, keys, unknown)
	assert.JSONEq(t, `{"message":"success"}`, recorder.Body.String())

	require.NoError(t, setting.UpdateOption(setting.WaffoEnabledOption, "false"))
	recorder = sendWaffoWebhook(handler, keys, closed)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

type injectedWaffoClient struct {
	called   bool
	checkout service.WaffoCheckoutRequest
}

func (client *injectedWaffoClient) CreateOrder(_ context.Context, _ setting.WaffoConfig, checkout service.WaffoCheckoutRequest) (controller.WaffoCreateOrderResult, error) {
	client.called = true
	client.checkout = checkout
	return controller.WaffoCreateOrderResult{
		PaymentURL: "https://checkout.waffo.test/injected", PaymentRequestID: checkout.PaymentRequestID,
		MerchantOrderID: checkout.MerchantOrderID, AcquiringOrderID: "acq_injected",
	}, nil
}

func TestWaffoClientBoundaryAndTopUpInfoAreInjectedAndAdvertised(t *testing.T) {
	_, do, _, _ := setupSubscriptionStripeTest(t)
	keys := controllerWaffoKeys(t)
	configureWaffoForController(t, keys)
	client := &injectedWaffoClient{}
	restore := controller.SetWaffoOrderClientForTesting(client)
	t.Cleanup(restore)

	recorder := do(http.MethodPost, "/api/user/waffo/pay", `{"amount":10,"pay_method_type":"APPLEPAY","pay_method_name":"APPLEPAY"}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "success", decodeBody(t, recorder)["message"])
	assert.True(t, client.called)
	assert.Equal(t, "APPLEPAY", client.checkout.PaymentInfo.PayMethodType)

	recorder = do(http.MethodGet, "/api/user/topup/info", "")
	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeBody(t, recorder)["data"].(map[string]any)
	assert.Equal(t, true, data["enable_waffo_topup"])
	assert.Equal(t, float64(1), data["waffo_min_topup"])
	assert.Len(t, data["waffo_pay_methods"], 2)
	methods := data["pay_methods"].([]any)
	found := false
	for _, raw := range methods {
		method := raw.(map[string]any)
		if method["type"] == service.PaymentMethodWaffo {
			found = true
		}
	}
	assert.True(t, found)
}

func mustWaffoJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	return encoded
}
