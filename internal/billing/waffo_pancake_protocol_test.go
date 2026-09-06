package billing

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	protocolPancakeMerchant = "MER_AbCdEfGhIjKlMnOpQrStUv"
	protocolPancakeStore    = "STO_AbCdEfGhIjKlMnOpQrStUv"
	protocolPancakeProduct  = "PROD_AbCdEfGhIjKlMnOpQrStUv"
	protocolPancakeOrder    = "ORD_AbCdEfGhIjKlMnOpQrStUv"
)

type waffoPancakeRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn waffoPancakeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func protocolPancakeConfig(t *testing.T) (setting.WaffoPancakeConfig, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	raw := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	config, err := setting.NewWaffoPancakeCredentialConfig(protocolPancakeMerchant, raw)
	require.NoError(t, err)
	config.StoreID = protocolPancakeStore
	config.ProductID = protocolPancakeProduct
	config.ReturnURL = "https://merchant.example.test/wallet"
	return config, privateKey
}

func protocolPancakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func verifyProtocolPancakeRequest(t *testing.T, request *http.Request, privateKey *rsa.PrivateKey, wantIdempotency bool) []byte {
	t.Helper()
	require.Equal(t, http.MethodPost, request.Method)
	require.Equal(t, "https", request.URL.Scheme)
	require.Equal(t, "api.waffo.ai", request.URL.Host)
	require.Empty(t, request.URL.RawQuery)
	require.Empty(t, request.URL.Fragment)
	require.Equal(t, protocolPancakeMerchant, request.Header.Get("X-Merchant-Id"))
	require.Equal(t, "application/json", request.Header.Get("Content-Type"))
	if wantIdempotency {
		require.Len(t, request.Header.Get("X-Idempotency-Key"), sha256.Size*2)
	} else {
		require.Empty(t, request.Header.Get("X-Idempotency-Key"))
	}
	body, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	timestamp := request.Header.Get("X-Timestamp")
	require.NotEmpty(t, timestamp)
	bodyHash := sha256.Sum256(body)
	canonical := http.MethodPost + "\n" + request.URL.Path + "\n" + timestamp + "\n" + base64.StdEncoding.EncodeToString(bodyHash[:])
	digest := sha256.Sum256([]byte(canonical))
	signature, err := base64.StdEncoding.Strict().DecodeString(request.Header.Get("X-Signature"))
	require.NoError(t, err)
	require.NoError(t, rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], signature))
	return body
}

func TestWaffoPancakeProtocolAuthenticatedCheckoutWireAndSignature(t *testing.T) {
	config, privateKey := protocolPancakeConfig(t)
	expiresAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)
	var paths []string
	restore := SetWaffoPancakeTransportForTesting(waffoPancakeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := verifyProtocolPancakeRequest(t, request, privateKey, true)
		paths = append(paths, request.URL.Path)
		switch request.URL.Path {
		case waffoPancakeIssueTokenPath:
			var wire map[string]any
			require.NoError(t, json.Unmarshal(body, &wire))
			require.Equal(t, map[string]any{
				"productId": protocolPancakeProduct, "buyerIdentity": "tokenrouter-user-7",
			}, wire)
			return protocolPancakeResponse(http.StatusOK, `{"data":{"token":"buyer.token_123","expiresAt":"`+expiresAt+`"}}`), nil
		case waffoPancakeCheckoutPath:
			var wire map[string]any
			require.NoError(t, json.Unmarshal(body, &wire))
			require.Equal(t, protocolPancakeProduct, wire["productId"])
			require.Equal(t, "USD", wire["currency"])
			require.Equal(t, "buyer@example.test", wire["buyerEmail"])
			require.Equal(t, "WAFFO_PANCAKE-7-1234567890-AbCd12", wire["orderMerchantExternalId"])
			require.Equal(t, float64(2700), wire["expiresInSeconds"])
			require.Equal(t, map[string]any{"amount": "12.34", "taxCategory": "saas"}, wire["priceSnapshot"])
			assert.NotContains(t, wire, "buyerIdentity")
			assert.NotContains(t, wire, "storeId")
			assert.NotContains(t, wire, "requestedAmount")
			return protocolPancakeResponse(http.StatusOK, `{"data":{"sessionId":"session_123","checkoutUrl":"https://checkout.waffo.ai/s/session_123","expiresAt":"`+expiresAt+`"}}`), nil
		default:
			t.Fatalf("unexpected Pancake path %q", request.URL.Path)
			return nil, nil
		}
	}))
	t.Cleanup(restore)

	session, err := (httpWaffoPancakeGateway{}).CreateCheckout(context.Background(), config, WaffoPancakeCheckoutRequest{
		ProductID: protocolPancakeProduct, StoreID: protocolPancakeStore, Currency: "USD",
		PriceSnapshot: WaffoPancakePriceSnapshot{Amount: "12.34", TaxCategory: "saas"},
		BuyerIdentity: "tokenrouter-user-7", BuyerEmail: "buyer@example.test", ExpiresInSeconds: 2700,
		OrderMerchantExternalID: "WAFFO_PANCAKE-7-1234567890-AbCd12", RequestedAmount: 1234,
	})
	require.NoError(t, err)
	require.Equal(t, []string{waffoPancakeIssueTokenPath, waffoPancakeCheckoutPath}, paths)
	assert.Equal(t, "session_123", session.SessionID)
	assert.Equal(t, "buyer.token_123", session.Token)
	assert.Equal(t, "https://checkout.waffo.ai/s/session_123#token=buyer.token_123", session.CheckoutURL)
}

func TestWaffoPancakeProtocolAdminWireAndCatalogFiltering(t *testing.T) {
	config, privateKey := protocolPancakeConfig(t)
	restore := SetWaffoPancakeTransportForTesting(waffoPancakeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		wantIdempotency := request.URL.Path != waffoPancakeGraphQLPath
		body := verifyProtocolPancakeRequest(t, request, privateKey, wantIdempotency)
		switch request.URL.Path {
		case waffoPancakeCreateStorePath:
			require.JSONEq(t, `{"name":"tokenrouter-store"}`, string(body))
			return protocolPancakeResponse(http.StatusOK, `{"data":{"store":{"id":"`+protocolPancakeStore+`"}}}`), nil
		case waffoPancakeCreateProductPath:
			var wire map[string]any
			require.NoError(t, json.Unmarshal(body, &wire))
			require.Equal(t, protocolPancakeStore, wire["storeId"])
			require.Equal(t, "Plan A", wire["name"])
			require.Equal(t, "https://merchant.example.test/wallet", wire["successUrl"])
			require.Equal(t, map[string]any{"USD": map[string]any{"amount": "8.50", "taxCategory": "saas"}}, wire["prices"])
			return protocolPancakeResponse(http.StatusOK, `{"data":{"product":{"id":"`+protocolPancakeProduct+`"}}}`), nil
		case waffoPancakePublishPath:
			require.JSONEq(t, `{"id":"`+protocolPancakeProduct+`"}`, string(body))
			return protocolPancakeResponse(http.StatusOK, `{"data":{"product":{"id":"`+protocolPancakeProduct+`","status":"active"}}}`), nil
		case waffoPancakeGraphQLPath:
			assert.Contains(t, string(body), "onetimeProducts")
			return protocolPancakeResponse(http.StatusOK, `{"data":{"stores":[{"id":"`+protocolPancakeStore+`","name":"Store","status":"active","prodEnabled":true,"onetimeProducts":[{"id":"`+protocolPancakeProduct+`","name":"Active","status":"active"},{"id":"PROD_ZyxWvUtSrQpOnMlKjIhGfE","name":"Inactive","status":"inactive"}]}]}}`), nil
		default:
			t.Fatalf("unexpected Pancake path %q", request.URL.Path)
			return nil, nil
		}
	}))
	t.Cleanup(restore)

	gateway := httpWaffoPancakeGateway{}
	storeID, err := gateway.CreateStore(context.Background(), config, "tokenrouter-store")
	require.NoError(t, err)
	assert.Equal(t, protocolPancakeStore, storeID)
	productID, err := gateway.CreateProduct(context.Background(), config, storeID, "Plan A", "8.50", config.ReturnURL)
	require.NoError(t, err)
	assert.Equal(t, protocolPancakeProduct, productID)
	require.NoError(t, gateway.PublishProduct(context.Background(), config, productID))
	catalog, err := gateway.ListCatalog(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, catalog.Stores, 1)
	require.Len(t, catalog.Stores[0].OnetimeProducts, 1)
	assert.Equal(t, "Active", catalog.Stores[0].OnetimeProducts[0].Name)
}

func TestWaffoPancakeProtocolBoundsAndGenericErrors(t *testing.T) {
	config, _ := protocolPancakeConfig(t)
	t.Run("transport detail is hidden", func(t *testing.T) {
		restore := SetWaffoPancakeTransportForTesting(waffoPancakeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed private-key-material")
		}))
		defer restore()
		_, err := (httpWaffoPancakeGateway{}).CreateStore(context.Background(), config, "Store")
		assert.ErrorIs(t, err, ErrWaffoPancakeTransport)
		assert.NotContains(t, err.Error(), "private-key-material")
	})
	t.Run("oversized response", func(t *testing.T) {
		restore := SetWaffoPancakeTransportForTesting(waffoPancakeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return protocolPancakeResponse(http.StatusOK, strings.Repeat("x", waffoPancakeResponseBodyLimit+1)), nil
		}))
		defer restore()
		_, err := (httpWaffoPancakeGateway{}).CreateStore(context.Background(), config, "Store")
		assert.ErrorIs(t, err, ErrWaffoPancakeResponseInvalid)
	})
	t.Run("definite four hundred rejection", func(t *testing.T) {
		restore := SetWaffoPancakeTransportForTesting(waffoPancakeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return protocolPancakeResponse(http.StatusBadRequest, `{"errors":[{"message":"secret upstream detail"}]}`), nil
		}))
		defer restore()
		_, err := (httpWaffoPancakeGateway{}).CreateStore(context.Background(), config, "Store")
		assert.True(t, WaffoPancakeRequestDefinitelyRejected(err))
		assert.NotContains(t, err.Error(), "secret upstream detail")
	})
}

func signProtocolPancakeWebhook(t *testing.T, privateKey *rsa.PrivateKey, timestamp string, payload []byte) string {
	t.Helper()
	digest := sha256.Sum256(append([]byte(timestamp+"."), payload...))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return "t=" + timestamp + ",v1=" + base64.StdEncoding.EncodeToString(signature)
}

func TestWaffoPancakeWebhookSignatureReplayAndModeValidation(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.UnixMilli(1_800_000_000_000)
	event := WaffoPancakeWebhookEvent{
		ID: "event_123", Timestamp: now.UTC().Format(time.RFC3339Nano), EventType: "order.completed",
		EventID: "delivery_123", StoreID: protocolPancakeStore, StoreName: "Store", Mode: WaffoPancakeModeTest,
		Data: WaffoPancakeWebhookData{
			OrderID: protocolPancakeOrder, OrderMerchantExternalID: "WAFFO_PANCAKE-7-1234567890-AbCd12",
			Currency: "USD", Amount: "12.34", ProductName: "Wallet", MerchantProvidedBuyerIdentity: "tokenrouter-user-7",
		},
	}
	payload, err := json.Marshal(event)
	require.NoError(t, err)
	timestamp := string(bytes.TrimSpace([]byte(time.UnixMilli(now.UnixMilli()).Format("150405"))))
	// The signature timestamp is Unix milliseconds; keep its string separate
	// from the event's informational RFC3339 timestamp.
	timestamp = "1800000000000"
	header := signProtocolPancakeWebhook(t, privateKey, timestamp, payload)

	verified, err := verifyWaffoPancakeWebhookWithKey(payload, header, WaffoPancakeModeTest, now, &privateKey.PublicKey)
	require.NoError(t, err)
	assert.Equal(t, event.ID, verified.ID)
	_, err = verifyWaffoPancakeWebhookWithKey(payload, header, WaffoPancakeModeProd, now, &privateKey.PublicKey)
	assert.Error(t, err)
	_, err = verifyWaffoPancakeWebhookWithKey(payload, header, "", now, &privateKey.PublicKey)
	assert.NoError(t, err)

	tampered := bytes.Replace(payload, []byte(`"12.34"`), []byte(`"99.99"`), 1)
	_, err = verifyWaffoPancakeWebhookWithKey(tampered, header, WaffoPancakeModeTest, now, &privateKey.PublicKey)
	assert.Error(t, err)
	_, err = verifyWaffoPancakeWebhookWithKey(payload, header, WaffoPancakeModeTest, now.Add(waffoPancakeWebhookTolerance+time.Millisecond), &privateKey.PublicKey)
	assert.Error(t, err)
	_, err = verifyWaffoPancakeWebhookWithKey(payload, header+",t="+timestamp, WaffoPancakeModeTest, now, &privateKey.PublicKey)
	assert.Error(t, err)
}
