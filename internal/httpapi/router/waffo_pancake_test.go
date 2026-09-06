package router_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	controllerPancakeMerchantID = "MER_AbCdEfGhIjKlMnOpQrStUv"
	controllerPancakeStoreID    = "STO_AbCdEfGhIjKlMnOpQrStUv"
	controllerPancakeProductID  = "PROD_AbCdEfGhIjKlMnOpQrStUv"
	controllerPancakePlanID     = "PROD_ZyxWvUtSrQpOnMlKjIhGfE"
	controllerPancakeOrderID    = "ORD_AbCdEfGhIjKlMnOpQrStUv"
)

type controllerPancakeGateway struct {
	mu               sync.Mutex
	checkoutRequests []billingsvc.WaffoPancakeCheckoutRequest
	checkoutErr      error
	storeNames       []string
	productCalls     []map[string]string
	published        []string
	catalog          *billingsvc.WaffoPancakeCatalog
}

func (gateway *controllerPancakeGateway) CreateCheckout(_ context.Context, _ setting.WaffoPancakeConfig, request billingsvc.WaffoPancakeCheckoutRequest) (*billingsvc.WaffoPancakeCheckoutSession, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.checkoutRequests = append(gateway.checkoutRequests, request)
	if gateway.checkoutErr != nil {
		return nil, gateway.checkoutErr
	}
	sessionID := "pancake_wallet_session"
	if strings.HasPrefix(request.OrderMerchantExternalID, "WAFFO_PANCAKE_SUB-") {
		sessionID = "pancake_subscription_session"
	}
	token := "header.payload.signature"
	expiresAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	return &billingsvc.WaffoPancakeCheckoutSession{
		SessionID: sessionID, CheckoutURL: "https://checkout.waffo.ai/session/" + sessionID + "#token=" + token,
		ExpiresAt: expiresAt, Token: token, TokenExpiresAt: expiresAt,
	}, nil
}

func (gateway *controllerPancakeGateway) CreateStore(_ context.Context, _ setting.WaffoPancakeConfig, name string) (string, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.storeNames = append(gateway.storeNames, name)
	return controllerPancakeStoreID, nil
}

func (gateway *controllerPancakeGateway) CreateProduct(_ context.Context, _ setting.WaffoPancakeConfig, storeID, name, amount, returnURL string) (string, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.productCalls = append(gateway.productCalls, map[string]string{
		"store_id": storeID, "name": name, "amount": amount, "return_url": returnURL,
	})
	if name == "tokenrouter-charge-product" {
		return controllerPancakeProductID, nil
	}
	return controllerPancakePlanID, nil
}

func (gateway *controllerPancakeGateway) PublishProduct(_ context.Context, _ setting.WaffoPancakeConfig, productID string) error {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.published = append(gateway.published, productID)
	return nil
}

func (gateway *controllerPancakeGateway) ListCatalog(context.Context, setting.WaffoPancakeConfig) (*billingsvc.WaffoPancakeCatalog, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.catalog == nil {
		return &billingsvc.WaffoPancakeCatalog{}, nil
	}
	copy := *gateway.catalog
	return &copy, nil
}

type controllerPancakeVerifier struct {
	mu                  sync.Mutex
	event               *billingsvc.WaffoPancakeWebhookEvent
	err                 error
	expectedEnvironment string
	payload             string
}

func (verifier *controllerPancakeVerifier) Verify(payload []byte, _ string, expectedEnvironment string) (*billingsvc.WaffoPancakeWebhookEvent, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.expectedEnvironment = expectedEnvironment
	verifier.payload = string(payload)
	if verifier.err != nil {
		return nil, verifier.err
	}
	if verifier.event == nil {
		return nil, errors.New("missing test event")
	}
	copy := *verifier.event
	return &copy, nil
}

func controllerPancakePrivateKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(der)
}

func configureControllerPancake(t *testing.T) string {
	t.Helper()
	privateKey := controllerPancakePrivateKey(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.WaffoPancakeMerchantIDOption: controllerPancakeMerchantID,
		setting.WaffoPancakePrivateKeyOption: privateKey,
		setting.WaffoPancakeReturnURLOption:  "https://merchant.example.test/wallet",
		setting.WaffoPancakeUnitPriceOption:  "1.25",
		setting.WaffoPancakeMinTopUpOption:   "2",
		setting.WaffoPancakeStoreIDOption:    controllerPancakeStoreID,
		setting.WaffoPancakeProductIDOption:  controllerPancakeProductID,
		setting.QuotaDisplayTypeOption:       "currency",
	}))
	return privateKey
}

func controllerPancakeEvent(tradeNo string, userID int, amount string) *billingsvc.WaffoPancakeWebhookEvent {
	return &billingsvc.WaffoPancakeWebhookEvent{
		ID: "event_pancake_123", Timestamp: time.Now().UTC().Format(time.RFC3339Nano), EventType: "order.completed",
		EventID: "delivery_pancake_123", StoreID: controllerPancakeStoreID, StoreName: "Store", Mode: billingsvc.WaffoPancakeModeTest,
		Data: billingsvc.WaffoPancakeWebhookData{
			OrderID: controllerPancakeOrderID, OrderMerchantExternalID: tradeNo, BuyerEmail: "buyer@example.test",
			Currency: "USD", Amount: amount, TaxAmount: "0.00", ProductName: "TokenRouter product",
			MerchantProvidedBuyerIdentity: billingsvc.WaffoPancakeBuyerIdentityFromUserID(userID),
		},
	}
}

func sendControllerPancakeWebhook(handler http.Handler, environment, payload string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/webhook/"+environment, strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Waffo-Signature", "test-signature")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestWaffoPancakeWalletAndSubscriptionHTTPContractEndToEnd(t *testing.T) {
	handler, do, userID, plan := setupSubscriptionStripeTest(t)
	configureControllerPancake(t)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
		"email": "buyer@example.test", "group": "default",
	}).Error)
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		Update("waffo_pancake_product_id", controllerPancakePlanID).Error)

	gateway := &controllerPancakeGateway{}
	verifier := &controllerPancakeVerifier{}
	restoreGateway := billingsvc.SetWaffoPancakeGatewayForTesting(gateway)
	restoreVerifier := billingsvc.SetWaffoPancakeWebhookVerifierForTesting(verifier)
	t.Cleanup(restoreGateway)
	t.Cleanup(restoreVerifier)

	for _, path := range []string{"/api/user/waffo-pancake/amount", "/api/user/waffo-pancake/pay", "/api/subscription/waffo-pancake/pay"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, path)
	}

	topupInfo := do(http.MethodGet, "/api/user/topup/info", "")
	require.Equal(t, http.StatusOK, topupInfo.Code)
	info := decodeBody(t, topupInfo)["data"].(map[string]any)
	assert.Equal(t, true, info["enable_waffo_pancake_topup"])
	assert.Equal(t, float64(2), info["waffo_pancake_min_topup"])
	methods := info["pay_methods"].([]any)
	require.NotEmpty(t, methods)
	assert.Equal(t, billingsvc.PaymentMethodWaffoPancake, methods[len(methods)-1].(map[string]any)["type"])

	amountResponse := do(http.MethodPost, "/api/user/waffo-pancake/amount", `{"amount":10}`)
	require.Equal(t, http.StatusOK, amountResponse.Code, amountResponse.Body.String())
	assert.Equal(t, "12.50", decodeBody(t, amountResponse)["data"])

	walletResponse := do(http.MethodPost, "/api/user/waffo-pancake/pay", `{"amount":10}`)
	require.Equal(t, http.StatusOK, walletResponse.Code, walletResponse.Body.String())
	walletData := decodeBody(t, walletResponse)["data"].(map[string]any)
	walletTradeNo := walletData["order_id"].(string)
	assert.True(t, strings.HasPrefix(walletTradeNo, "WAFFO_PANCAKE-"))
	assert.Equal(t, "pancake_wallet_session", walletData["session_id"])
	assert.Contains(t, walletData["checkout_url"], "#token=")
	assert.NotEmpty(t, walletData["token"])
	var walletOrder model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", walletTradeNo).First(&walletOrder).Error)
	assert.Equal(t, billingsvc.PaymentProviderWaffoPancake, walletOrder.PaymentProvider)
	assert.Equal(t, int64(1250), walletOrder.ProviderAmountMinor)
	assert.Equal(t, int64(5_000_000), walletOrder.CreditQuota)
	require.NotNil(t, walletOrder.ProviderSessionId)
	assert.Equal(t, "pancake_wallet_session", *walletOrder.ProviderSessionId)
	assert.NotEmpty(t, walletOrder.CheckoutRequest)
	assert.NotEmpty(t, walletOrder.CheckoutFingerprint)

	verifier.event = controllerPancakeEvent(walletTradeNo, userID, "12.50")
	misrouted := sendControllerPancakeWebhook(handler, "prod", `{"signed":"wallet"}`)
	assert.Equal(t, http.StatusOK, misrouted.Code)
	var user model.User
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 1000, user.Quota)

	forged := controllerPancakeEvent(walletTradeNo, userID, "12.51")
	verifier.event = forged
	forgedResponse := sendControllerPancakeWebhook(handler, "test", `{"signed":"wallet-forged"}`)
	assert.Equal(t, http.StatusInternalServerError, forgedResponse.Code)
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 1000, user.Quota)

	verifier.event = controllerPancakeEvent(walletTradeNo, userID, "12.50")
	for i := 0; i < 2; i++ {
		response := sendControllerPancakeWebhook(handler, "test", `{"signed":"wallet-valid"}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Equal(t, "OK", response.Body.String())
	}
	require.NoError(t, model.DB.First(&user, userID).Error)
	assert.Equal(t, 5_001_000, user.Quota)

	subscriptionResponse := do(http.MethodPost, "/api/subscription/waffo-pancake/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	require.Equal(t, http.StatusOK, subscriptionResponse.Code, subscriptionResponse.Body.String())
	subscriptionData := decodeBody(t, subscriptionResponse)["data"].(map[string]any)
	subscriptionTradeNo := subscriptionData["order_id"].(string)
	assert.True(t, strings.HasPrefix(subscriptionTradeNo, "WAFFO_PANCAKE_SUB-"))
	assert.Equal(t, "pancake_subscription_session", subscriptionData["session_id"])
	var subscriptionOrder model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", subscriptionTradeNo).First(&subscriptionOrder).Error)
	assert.True(t, subscriptionOrder.CapacityReserved)
	assert.Equal(t, int64(4000), subscriptionOrder.ProviderAmountMinor)
	assert.Equal(t, controllerPancakePlanID, subscriptionOrder.ProviderPriceId)
	require.NotNil(t, subscriptionOrder.ProviderSessionId)

	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"total_amount": int64(1), "duration_value": 12, "waffo_pancake_product_id": controllerPancakeProductID,
	}).Error)
	verifier.event = controllerPancakeEvent(subscriptionTradeNo, userID, "40.00")
	for i := 0; i < 2; i++ {
		response := sendControllerPancakeWebhook(handler, "test", `{"signed":"subscription-valid"}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	var subscription model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ? AND plan_id = ?", userID, plan.Id).First(&subscription).Error)
	assert.Equal(t, int64(100000), subscription.AmountTotal)

	gateway.mu.Lock()
	require.Len(t, gateway.checkoutRequests, 2)
	assert.Equal(t, controllerPancakeProductID, gateway.checkoutRequests[0].ProductID)
	assert.Equal(t, "12.50", gateway.checkoutRequests[0].PriceSnapshot.Amount)
	assert.Equal(t, controllerPancakePlanID, gateway.checkoutRequests[1].ProductID)
	assert.Equal(t, "40.00", gateway.checkoutRequests[1].PriceSnapshot.Amount)
	gateway.mu.Unlock()
}

func TestWaffoPancakeWebhookAvailabilitySignatureAndBodyLimits(t *testing.T) {
	handler, _, _, _ := setupSubscriptionStripeTest(t)
	configureControllerPancake(t)
	verifier := &controllerPancakeVerifier{err: errors.New("invalid test signature")}
	restore := billingsvc.SetWaffoPancakeWebhookVerifierForTesting(verifier)
	t.Cleanup(restore)

	response := sendControllerPancakeWebhook(handler, "unknown", `{}`)
	assert.Equal(t, http.StatusNotFound, response.Code)
	response = sendControllerPancakeWebhook(handler, "test", `{}`)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	oversized := strings.Repeat("x", (64<<10)+1)
	response = sendControllerPancakeWebhook(handler, "test", oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)

	require.NoError(t, setting.UpdateOption(setting.WaffoPancakeProductIDOption, ""))
	response = sendControllerPancakeWebhook(handler, "unknown", `{}`)
	assert.Equal(t, http.StatusForbidden, response.Code, "availability is checked before the path environment")
}

func TestWaffoPancakeCheckoutFailureClassification(t *testing.T) {
	_, do, userID, plan := setupSubscriptionStripeTest(t)
	configureControllerPancake(t)
	require.NoError(t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", plan.Id).
		Update("waffo_pancake_product_id", controllerPancakePlanID).Error)

	gateway := &controllerPancakeGateway{checkoutErr: billingsvc.ErrWaffoPancakeTransport}
	restore := billingsvc.SetWaffoPancakeGatewayForTesting(gateway)
	response := do(http.MethodPost, "/api/user/waffo-pancake/pay", `{"amount":10}`)
	restore()
	assert.Equal(t, "error", decodeBody(t, response)["message"])
	var wallet model.TopUp
	require.NoError(t, model.DB.Where("user_id = ? AND payment_provider = ?", userID, billingsvc.PaymentProviderWaffoPancake).
		Order("id desc").First(&wallet).Error)
	assert.Equal(t, billingsvc.TopUpStatusPending, wallet.Status)
	assert.Equal(t, billingsvc.WaffoPancakeCreateUnknown, wallet.ReconciliationState)
	assert.Nil(t, wallet.ProviderSessionId)

	gateway = &controllerPancakeGateway{checkoutErr: &billingsvc.WaffoPancakeGatewayError{Status: http.StatusBadRequest, DefinitelyRejected: true}}
	restore = billingsvc.SetWaffoPancakeGatewayForTesting(gateway)
	response = do(http.MethodPost, "/api/subscription/waffo-pancake/pay", fmt.Sprintf(`{"plan_id":%d}`, plan.Id))
	restore()
	assert.Equal(t, "error", decodeBody(t, response)["message"])
	var subscription model.SubscriptionOrder
	require.NoError(t, model.DB.Where("user_id = ? AND payment_provider = ?", userID, billingsvc.PaymentProviderWaffoPancake).
		Order("id desc").First(&subscription).Error)
	assert.Equal(t, billingsvc.TopUpStatusFailed, subscription.Status)
	assert.False(t, subscription.CapacityReserved)
}

func TestWaffoPancakeRootAdministrationContract(t *testing.T) {
	handler, do, _, _ := setupSubscriptionStripeTest(t)
	privateKey := configureControllerPancake(t)
	gateway := &controllerPancakeGateway{catalog: &billingsvc.WaffoPancakeCatalog{Stores: []billingsvc.WaffoPancakeCatalogStore{{
		ID: controllerPancakeStoreID, Name: "Store", Status: "active", ProdEnabled: true,
		OnetimeProducts: []billingsvc.WaffoPancakeCatalogProduct{{ID: controllerPancakeProductID, Name: "Wallet", Status: "active"}, {ID: controllerPancakePlanID, Name: "Plan", Status: "active"}},
	}}}}
	restore := billingsvc.SetWaffoPancakeGatewayForTesting(gateway)
	t.Cleanup(restore)

	request := httptest.NewRequest(http.MethodPost, "/api/option/waffo-pancake/save", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, request)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	saveBody, err := json.Marshal(map[string]string{
		"merchant_id": controllerPancakeMerchantID, "private_key": "",
		"return_url": "https://merchant.example.test/new-return", "store_id": controllerPancakeStoreID,
		"product_id": controllerPancakeProductID, "unit_price": "2.5", "min_top_up": "30",
	})
	require.NoError(t, err)
	response := do(http.MethodPost, "/api/option/waffo-pancake/save", string(saveBody))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "success", decodeBody(t, response)["message"])
	assert.Equal(t, privateKey, setting.GetOption(setting.WaffoPancakePrivateKeyOption), "blank save preserves the stored secret")
	assert.Equal(t, "2.5", setting.GetOption(setting.WaffoPancakeUnitPriceOption))
	assert.Equal(t, "30", setting.GetOption(setting.WaffoPancakeMinTopUpOption))

	invalidSaveBody, err := json.Marshal(map[string]string{
		"merchant_id": controllerPancakeMerchantID, "private_key": "",
		"return_url": "https://merchant.example.test/must-not-persist", "store_id": controllerPancakeStoreID,
		"product_id": controllerPancakeProductID, "unit_price": "Infinity", "min_top_up": "40",
	})
	require.NoError(t, err)
	response = do(http.MethodPost, "/api/option/waffo-pancake/save", string(invalidSaveBody))
	assert.Equal(t, "error", decodeBody(t, response)["message"])
	assert.Equal(t, "https://merchant.example.test/new-return", setting.GetOption(setting.WaffoPancakeReturnURLOption))
	assert.Equal(t, "2.5", setting.GetOption(setting.WaffoPancakeUnitPriceOption))
	assert.Equal(t, "30", setting.GetOption(setting.WaffoPancakeMinTopUpOption))

	response = do(http.MethodPost, "/api/option/waffo-pancake/pair", `{}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	pairData := decodeBody(t, response)["data"].(map[string]any)
	assert.Equal(t, controllerPancakeStoreID, pairData["store_id"])
	assert.Equal(t, controllerPancakeProductID, pairData["product_id"])

	response = do(http.MethodGet, "/api/option/waffo-pancake/catalog", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, "success", decodeBody(t, response)["message"])

	response = do(http.MethodPost, "/api/option/waffo-pancake/subscription-product", `{"name":"Plan B","amount":"19.95"}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	productData := decodeBody(t, response)["data"].(map[string]any)
	assert.Equal(t, controllerPancakePlanID, productData["product_id"])
	assert.Equal(t, controllerPancakeStoreID, productData["store_id"])

	response = do(http.MethodGet, "/api/option/waffo-pancake/subscription-product-options", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	options := decodeBody(t, response)["data"].(map[string]any)
	assert.Equal(t, controllerPancakeStoreID, options["store_id"])
	assert.Len(t, options["products"], 2)

	response = do(http.MethodGet, "/api/option/", "")
	assert.NotContains(t, response.Body.String(), privateKey)
	assert.NotContains(t, response.Body.String(), setting.WaffoPancakePrivateKeyOption)

	gateway.mu.Lock()
	assert.Contains(t, gateway.storeNames, "tokenrouter-store")
	assert.Contains(t, gateway.published, controllerPancakeProductID)
	assert.Contains(t, gateway.published, controllerPancakePlanID)
	require.Len(t, gateway.productCalls, 2)
	assert.Equal(t, "19.95", gateway.productCalls[1]["amount"])
	gateway.mu.Unlock()
}
