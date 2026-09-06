package service

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	waffoPancakeRequestBodyLimit  = 64 << 10
	waffoPancakeResponseBodyLimit = 256 << 10
	waffoPancakeHeaderLimit       = 64 << 10
	waffoPancakeCheckoutTimeout   = 30 * time.Second
	waffoPancakeWebhookTolerance  = 5 * time.Minute
	waffoPancakeMaxTokenLength    = 16 << 10
	waffoPancakeMaxTextLength     = 255
	waffoPancakeMaxCatalogStores  = 100
	waffoPancakeMaxStoreProducts  = 1000

	waffoPancakeIssueTokenPath    = "/v1/actions/auth/issue-session-token"
	waffoPancakeCheckoutPath      = "/v1/actions/checkout/create-session"
	waffoPancakeCreateStorePath   = "/v1/actions/store/create-store"
	waffoPancakeCreateProductPath = "/v1/actions/onetime-product/create-product"
	waffoPancakePublishPath       = "/v1/actions/onetime-product/publish-product"
	waffoPancakeGraphQLPath       = "/v1/graphql"
)

var (
	ErrWaffoPancakeTransport       = errors.New("Waffo Pancake transport request failed")
	ErrWaffoPancakeResponseInvalid = errors.New("Waffo Pancake response is invalid")
)

// WaffoPancakePriceSnapshot is the exact checkout amount sent to the gateway.
type WaffoPancakePriceSnapshot struct {
	Amount      string `json:"amount"`
	TaxCategory string `json:"taxCategory"`
}

// WaffoPancakeCheckoutRequest is both the immutable local snapshot and the
// input to the protocol client. BuyerIdentity and StoreID are intentionally
// part of the snapshot even though the create-session call routes them
// elsewhere or does not send them.
type WaffoPancakeCheckoutRequest struct {
	ProductID               string                    `json:"productId"`
	StoreID                 string                    `json:"storeId,omitempty"`
	Currency                string                    `json:"currency"`
	PriceSnapshot           WaffoPancakePriceSnapshot `json:"priceSnapshot"`
	BuyerIdentity           string                    `json:"buyerIdentity"`
	BuyerEmail              string                    `json:"buyerEmail,omitempty"`
	ExpiresInSeconds        int                       `json:"expiresInSeconds"`
	OrderMerchantExternalID string                    `json:"orderMerchantExternalId"`
	RequestedAmount         int64                     `json:"requestedAmount,omitempty"`
}

// WaffoPancakeCheckoutSession is the credential-free result returned to the
// controller after both the buyer token and checkout session are validated.
type WaffoPancakeCheckoutSession struct {
	SessionID      string `json:"session_id"`
	CheckoutURL    string `json:"checkout_url"`
	ExpiresAt      string `json:"expires_at"`
	Token          string `json:"token"`
	TokenExpiresAt string `json:"token_expires_at"`
}

type WaffoPancakeCatalogProduct struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type WaffoPancakeCatalogStore struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Status          string                       `json:"status"`
	ProdEnabled     bool                         `json:"prodEnabled"`
	OnetimeProducts []WaffoPancakeCatalogProduct `json:"onetimeProducts"`
}

type WaffoPancakeCatalog struct {
	Stores []WaffoPancakeCatalogStore `json:"stores"`
}

type WaffoPancakePairResult struct {
	StoreID     string `json:"store_id"`
	StoreName   string `json:"store_name"`
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	OrphanStore bool   `json:"orphan_store,omitempty"`
}

// WaffoPancakeWebhookEvent is the bounded subset required for settlement.
type WaffoPancakeWebhookEvent struct {
	ID        string                  `json:"id"`
	Timestamp string                  `json:"timestamp"`
	EventType string                  `json:"eventType"`
	EventID   string                  `json:"eventId"`
	StoreID   string                  `json:"storeId"`
	StoreName string                  `json:"storeName"`
	Mode      string                  `json:"mode"`
	Data      WaffoPancakeWebhookData `json:"data"`
}

type WaffoPancakeWebhookData struct {
	OrderID                       string `json:"orderId"`
	OrderMerchantExternalID       string `json:"orderMerchantExternalId"`
	BuyerEmail                    string `json:"buyerEmail"`
	Currency                      string `json:"currency"`
	Amount                        string `json:"amount"`
	TaxAmount                     string `json:"taxAmount"`
	ProductName                   string `json:"productName"`
	MerchantProvidedBuyerIdentity string `json:"merchantProvidedBuyerIdentity"`
}

func (event *WaffoPancakeWebhookEvent) NormalizedEventType() string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.EventType)
}

// WaffoPancakeGateway is the narrow, injectable boundary around Pancake's
// merchant API. Production uses a signed, fixed-host, bounded HTTP client.
type WaffoPancakeGateway interface {
	CreateCheckout(context.Context, setting.WaffoPancakeConfig, WaffoPancakeCheckoutRequest) (*WaffoPancakeCheckoutSession, error)
	CreateStore(context.Context, setting.WaffoPancakeConfig, string) (string, error)
	CreateProduct(context.Context, setting.WaffoPancakeConfig, string, string, string, string) (string, error)
	PublishProduct(context.Context, setting.WaffoPancakeConfig, string) error
	ListCatalog(context.Context, setting.WaffoPancakeConfig) (*WaffoPancakeCatalog, error)
}

type WaffoPancakeWebhookVerifier interface {
	Verify(payload []byte, signatureHeader, expectedEnvironment string) (*WaffoPancakeWebhookEvent, error)
}

type httpWaffoPancakeGateway struct{}
type rsaWaffoPancakeWebhookVerifier struct{}

var defaultWaffoPancakeTransport http.RoundTripper = &http.Transport{
	Proxy:                  nil,
	DialContext:            common.SafeDialContext,
	ForceAttemptHTTP2:      true,
	MaxIdleConns:           16,
	MaxIdleConnsPerHost:    4,
	IdleConnTimeout:        30 * time.Second,
	TLSHandshakeTimeout:    10 * time.Second,
	ResponseHeaderTimeout:  20 * time.Second,
	ExpectContinueTimeout:  time.Second,
	MaxResponseHeaderBytes: waffoPancakeHeaderLimit,
	TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
}

var waffoPancakeRuntime = struct {
	sync.RWMutex
	gateway   WaffoPancakeGateway
	verifier  WaffoPancakeWebhookVerifier
	transport http.RoundTripper
}{
	gateway:   httpWaffoPancakeGateway{},
	verifier:  rsaWaffoPancakeWebhookVerifier{},
	transport: defaultWaffoPancakeTransport,
}

func SetWaffoPancakeGatewayForTesting(gateway WaffoPancakeGateway) (restore func()) {
	if gateway == nil {
		gateway = httpWaffoPancakeGateway{}
	}
	waffoPancakeRuntime.Lock()
	previous := waffoPancakeRuntime.gateway
	waffoPancakeRuntime.gateway = gateway
	waffoPancakeRuntime.Unlock()
	return func() {
		waffoPancakeRuntime.Lock()
		waffoPancakeRuntime.gateway = previous
		waffoPancakeRuntime.Unlock()
	}
}

func SetWaffoPancakeWebhookVerifierForTesting(verifier WaffoPancakeWebhookVerifier) (restore func()) {
	if verifier == nil {
		verifier = rsaWaffoPancakeWebhookVerifier{}
	}
	waffoPancakeRuntime.Lock()
	previous := waffoPancakeRuntime.verifier
	waffoPancakeRuntime.verifier = verifier
	waffoPancakeRuntime.Unlock()
	return func() {
		waffoPancakeRuntime.Lock()
		waffoPancakeRuntime.verifier = previous
		waffoPancakeRuntime.Unlock()
	}
}

func SetWaffoPancakeTransportForTesting(transport http.RoundTripper) (restore func()) {
	if transport == nil {
		transport = defaultWaffoPancakeTransport
	}
	waffoPancakeRuntime.Lock()
	previous := waffoPancakeRuntime.transport
	waffoPancakeRuntime.transport = transport
	waffoPancakeRuntime.Unlock()
	return func() {
		waffoPancakeRuntime.Lock()
		waffoPancakeRuntime.transport = previous
		waffoPancakeRuntime.Unlock()
	}
}

func currentWaffoPancakeGateway() WaffoPancakeGateway {
	waffoPancakeRuntime.RLock()
	defer waffoPancakeRuntime.RUnlock()
	return waffoPancakeRuntime.gateway
}

func currentWaffoPancakeVerifier() WaffoPancakeWebhookVerifier {
	waffoPancakeRuntime.RLock()
	defer waffoPancakeRuntime.RUnlock()
	return waffoPancakeRuntime.verifier
}

func currentWaffoPancakeTransport() http.RoundTripper {
	waffoPancakeRuntime.RLock()
	defer waffoPancakeRuntime.RUnlock()
	return waffoPancakeRuntime.transport
}

func CreateWaffoPancakeCheckoutSession(ctx context.Context, config setting.WaffoPancakeConfig, request WaffoPancakeCheckoutRequest) (*WaffoPancakeCheckoutSession, error) {
	return currentWaffoPancakeGateway().CreateCheckout(ctx, config, request)
}

func VerifyConfiguredWaffoPancakeWebhook(payload []byte, signatureHeader, expectedEnvironment string) (*WaffoPancakeWebhookEvent, error) {
	return currentWaffoPancakeVerifier().Verify(payload, signatureHeader, expectedEnvironment)
}

type WaffoPancakeGatewayError struct {
	Status             int
	DefinitelyRejected bool
}

func (err *WaffoPancakeGatewayError) Error() string {
	if err.Status > 0 {
		return fmt.Sprintf("Waffo Pancake API returned HTTP %d", err.Status)
	}
	return "Waffo Pancake rejected the request"
}

func WaffoPancakeRequestDefinitelyRejected(err error) bool {
	var gatewayErr *WaffoPancakeGatewayError
	return errors.As(err, &gatewayErr) && gatewayErr.DefinitelyRejected
}

type waffoPancakeNotice struct {
	Message string `json:"message"`
}

type waffoPancakeEnvelope struct {
	Data   json.RawMessage      `json:"data"`
	Errors []waffoPancakeNotice `json:"errors,omitempty"`
}

func (httpWaffoPancakeGateway) doAction(ctx context.Context, config setting.WaffoPancakeConfig, path string, body any, result any, idempotencyWindow int, omitIdempotency bool) error {
	if ctx == nil || config.APIBaseURL != setting.WaffoPancakeBaseURL || !validWaffoPancakePath(path) ||
		!setting.ValidWaffoPancakeShortID(config.MerchantID, "MER") {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	privateKey, err := setting.ParseWaffoPancakePrivateKey(config.PrivateKey)
	if err != nil {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	bodyBytes, err := common.Marshal(body)
	if err != nil || len(bodyBytes) == 0 || len(bodyBytes) > waffoPancakeRequestBodyLimit {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	timestampSeconds := time.Now().Unix()
	timestamp := strconv.FormatInt(timestampSeconds, 10)
	signature, err := signWaffoPancakeRequest(path, timestamp, bodyBytes, privateKey)
	if err != nil {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	endpoint := config.APIBaseURL + path
	if !validWaffoPancakeEndpoint(endpoint, path) {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Merchant-Id", config.MerchantID)
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Signature", signature)
	if !omitIdempotency {
		request.Header.Set("X-Idempotency-Key", waffoPancakeIdempotencyKey(config.MerchantID, path, bodyBytes, timestampSeconds, idempotencyWindow))
	}
	client := &http.Client{
		Transport: currentWaffoPancakeTransport(),
		Timeout:   waffoPancakeCheckoutTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return ErrWaffoPancakeTransport
	}
	defer response.Body.Close()
	responseBody, err := common.ReadAllLimited(response.Body, waffoPancakeResponseBodyLimit)
	if err != nil {
		return ErrWaffoPancakeResponseInvalid
	}
	var envelope waffoPancakeEnvelope
	if len(bytes.TrimSpace(responseBody)) == 0 || common.Unmarshal(responseBody, &envelope) != nil {
		if response.StatusCode >= http.StatusBadRequest {
			return newWaffoPancakeHTTPError(response.StatusCode)
		}
		return ErrWaffoPancakeResponseInvalid
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || len(envelope.Errors) > 0 {
		status := response.StatusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		return newWaffoPancakeHTTPError(status)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || common.Unmarshal(envelope.Data, result) != nil {
		return ErrWaffoPancakeResponseInvalid
	}
	return nil
}

func newWaffoPancakeHTTPError(status int) error {
	definite := status >= 400 && status < 500 && status != 408 && status != 409 && status != 425 && status != 429
	return &WaffoPancakeGatewayError{Status: status, DefinitelyRejected: definite}
}

func validWaffoPancakePath(path string) bool {
	switch path {
	case waffoPancakeIssueTokenPath, waffoPancakeCheckoutPath, waffoPancakeCreateStorePath,
		waffoPancakeCreateProductPath, waffoPancakePublishPath, waffoPancakeGraphQLPath:
		return true
	default:
		return false
	}
}

func validWaffoPancakeEndpoint(endpoint, path string) bool {
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "api.waffo.ai" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Path == path &&
		endpoint == setting.WaffoPancakeBaseURL+path
}

func signWaffoPancakeRequest(path, timestamp string, body []byte, privateKey *rsa.PrivateKey) (string, error) {
	if privateKey == nil || !validWaffoPancakePath(path) || timestamp == "" {
		return "", errors.New("invalid Waffo Pancake signing input")
	}
	bodyDigest := sha256.Sum256(body)
	canonical := http.MethodPost + "\n" + path + "\n" + timestamp + "\n" + base64.StdEncoding.EncodeToString(bodyDigest[:])
	digest := sha256.Sum256([]byte(canonical))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func waffoPancakeIdempotencyKey(merchantID, path string, body []byte, timestamp int64, window int) string {
	input := merchantID + ":" + path + ":" + string(body)
	if window > 0 {
		input += ":" + strconv.FormatInt(timestamp/int64(window), 10)
	}
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])
}

func (gateway httpWaffoPancakeGateway) CreateCheckout(ctx context.Context, config setting.WaffoPancakeConfig, checkout WaffoPancakeCheckoutRequest) (*WaffoPancakeCheckoutSession, error) {
	if err := validateWaffoPancakeCheckoutRequest(&checkout); err != nil {
		return nil, err
	}
	tokenRequest := struct {
		ProductID     string `json:"productId"`
		BuyerIdentity string `json:"buyerIdentity"`
	}{ProductID: checkout.ProductID, BuyerIdentity: checkout.BuyerIdentity}
	var tokenResponse struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := gateway.doAction(ctx, config, waffoPancakeIssueTokenPath, tokenRequest, &tokenResponse, 60, false); err != nil {
		return nil, err
	}
	if !validWaffoPancakeToken(tokenResponse.Token) || !validWaffoPancakeTimestamp(tokenResponse.ExpiresAt) {
		return nil, ErrWaffoPancakeResponseInvalid
	}

	type checkoutWire struct {
		ProductID               string                     `json:"productId"`
		Currency                string                     `json:"currency"`
		PriceSnapshot           *WaffoPancakePriceSnapshot `json:"priceSnapshot,omitempty"`
		BuyerEmail              *string                    `json:"buyerEmail,omitempty"`
		ExpiresInSeconds        *int                       `json:"expiresInSeconds,omitempty"`
		OrderMerchantExternalID *string                    `json:"orderMerchantExternalId,omitempty"`
	}
	buyerEmail := optionalWaffoPancakeString(checkout.BuyerEmail)
	expires := checkout.ExpiresInSeconds
	externalID := checkout.OrderMerchantExternalID
	wire := checkoutWire{
		ProductID: checkout.ProductID, Currency: checkout.Currency, PriceSnapshot: &checkout.PriceSnapshot,
		BuyerEmail: buyerEmail, ExpiresInSeconds: &expires, OrderMerchantExternalID: &externalID,
	}
	var sessionResponse struct {
		SessionID   string `json:"sessionId"`
		CheckoutURL string `json:"checkoutUrl"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if err := gateway.doAction(ctx, config, waffoPancakeCheckoutPath, wire, &sessionResponse, 60, false); err != nil {
		return nil, err
	}
	if !validWaffoPancakeIdentifier(sessionResponse.SessionID) || !validWaffoPancakeCheckoutURL(sessionResponse.CheckoutURL) ||
		!validWaffoPancakeTimestamp(sessionResponse.ExpiresAt) {
		return nil, ErrWaffoPancakeResponseInvalid
	}
	return &WaffoPancakeCheckoutSession{
		SessionID: sessionResponse.SessionID, CheckoutURL: sessionResponse.CheckoutURL + "#token=" + tokenResponse.Token,
		ExpiresAt: sessionResponse.ExpiresAt, Token: tokenResponse.Token, TokenExpiresAt: tokenResponse.ExpiresAt,
	}, nil
}

func (gateway httpWaffoPancakeGateway) CreateStore(ctx context.Context, config setting.WaffoPancakeConfig, name string) (string, error) {
	if !validWaffoPancakeText(name, 128, false) {
		return "", &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	var response struct {
		Store struct {
			ID string `json:"id"`
		} `json:"store"`
	}
	if err := gateway.doAction(ctx, config, waffoPancakeCreateStorePath,
		struct {
			Name string `json:"name"`
		}{Name: name}, &response, 0, false); err != nil {
		return "", err
	}
	if !setting.ValidWaffoPancakeShortID(response.Store.ID, "STO") {
		return "", ErrWaffoPancakeResponseInvalid
	}
	return response.Store.ID, nil
}

func (gateway httpWaffoPancakeGateway) CreateProduct(ctx context.Context, config setting.WaffoPancakeConfig, storeID, name, amount, returnURL string) (string, error) {
	if !setting.ValidWaffoPancakeShortID(storeID, "STO") || !validWaffoPancakeText(name, 255, false) ||
		!validWaffoPancakePrice(amount) || (returnURL != "" && !setting.ValidWaffoCallbackURL(returnURL)) {
		return "", &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	type price struct {
		Amount      string `json:"amount"`
		TaxCategory string `json:"taxCategory"`
	}
	type createProductWire struct {
		StoreID    string           `json:"storeId"`
		Name       string           `json:"name"`
		Prices     map[string]price `json:"prices"`
		SuccessURL *string          `json:"successUrl,omitempty"`
	}
	wire := createProductWire{
		StoreID: storeID, Name: name,
		Prices:     map[string]price{"USD": {Amount: amount, TaxCategory: "saas"}},
		SuccessURL: optionalWaffoPancakeString(returnURL),
	}
	var response struct {
		Product struct {
			ID string `json:"id"`
		} `json:"product"`
	}
	if err := gateway.doAction(ctx, config, waffoPancakeCreateProductPath, wire, &response, 0, false); err != nil {
		return "", err
	}
	if !setting.ValidWaffoPancakeShortID(response.Product.ID, "PROD") {
		return "", ErrWaffoPancakeResponseInvalid
	}
	return response.Product.ID, nil
}

func (gateway httpWaffoPancakeGateway) PublishProduct(ctx context.Context, config setting.WaffoPancakeConfig, productID string) error {
	if !setting.ValidWaffoPancakeShortID(productID, "PROD") {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	var response struct {
		Product struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"product"`
	}
	if err := gateway.doAction(ctx, config, waffoPancakePublishPath,
		struct {
			ID string `json:"id"`
		}{ID: productID}, &response, 0, false); err != nil {
		return err
	}
	if response.Product.ID != productID || !strings.EqualFold(strings.TrimSpace(response.Product.Status), "active") {
		return ErrWaffoPancakeResponseInvalid
	}
	return nil
}

func (gateway httpWaffoPancakeGateway) ListCatalog(ctx context.Context, config setting.WaffoPancakeConfig) (*WaffoPancakeCatalog, error) {
	const query = `query {
  stores(limit: 100) {
    id
    name
    status
    prodEnabled
    onetimeProducts {
      id
      name
      status
    }
  }
}`
	wire := struct {
		Query string `json:"query"`
	}{Query: query}
	var response WaffoPancakeCatalog
	if err := gateway.doAction(ctx, config, waffoPancakeGraphQLPath, wire, &response, 0, true); err != nil {
		return nil, err
	}
	if len(response.Stores) > waffoPancakeMaxCatalogStores {
		return nil, ErrWaffoPancakeResponseInvalid
	}
	stores := make([]WaffoPancakeCatalogStore, 0, len(response.Stores))
	for _, store := range response.Stores {
		if !setting.ValidWaffoPancakeShortID(store.ID, "STO") || !validWaffoPancakeText(store.Name, 255, false) ||
			!validWaffoPancakeText(store.Status, 32, false) || len(store.OnetimeProducts) > waffoPancakeMaxStoreProducts {
			return nil, ErrWaffoPancakeResponseInvalid
		}
		active := make([]WaffoPancakeCatalogProduct, 0, len(store.OnetimeProducts))
		for _, product := range store.OnetimeProducts {
			if !setting.ValidWaffoPancakeShortID(product.ID, "PROD") || !validWaffoPancakeText(product.Name, 255, false) ||
				!validWaffoPancakeText(product.Status, 32, false) {
				return nil, ErrWaffoPancakeResponseInvalid
			}
			if strings.EqualFold(product.Status, "active") {
				active = append(active, product)
			}
		}
		store.OnetimeProducts = active
		stores = append(stores, store)
	}
	return &WaffoPancakeCatalog{Stores: stores}, nil
}

func validateWaffoPancakeCheckoutRequest(request *WaffoPancakeCheckoutRequest) error {
	if request == nil || !setting.ValidWaffoPancakeShortID(request.ProductID, "PROD") ||
		!setting.ValidWaffoPancakeShortID(request.StoreID, "STO") ||
		request.Currency != "USD" || !validWaffoPancakePrice(request.PriceSnapshot.Amount) ||
		request.PriceSnapshot.TaxCategory != "saas" || !validWaffoPancakeText(request.BuyerIdentity, 128, false) ||
		!validWaffoPancakeText(request.BuyerEmail, 320, true) || request.ExpiresInSeconds <= 0 ||
		request.ExpiresInSeconds > 24*60*60 || !validWaffoPancakeText(request.OrderMerchantExternalID, 128, false) {
		return &WaffoPancakeGatewayError{DefinitelyRejected: true}
	}
	return nil
}

func validWaffoPancakePrice(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 64 {
		return false
	}
	digits := 0
	dot := false
	for i := range len(value) {
		switch char := value[i]; {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits > 0
}

func validWaffoPancakeText(value string, limit int, emptyOK bool) bool {
	if value == "" {
		return emptyOK
	}
	if value != strings.TrimSpace(value) || len(value) > limit {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validWaffoPancakeIdentifier(value string) bool {
	if !validWaffoPancakeText(value, waffoPancakeMaxTextLength, false) {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.') {
			return false
		}
	}
	return true
}

func validWaffoPancakeToken(value string) bool {
	if value == "" || len(value) > waffoPancakeMaxTokenLength || value != strings.TrimSpace(value) {
		return false
	}
	for i := range len(value) {
		char := value[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.') {
			return false
		}
	}
	return true
}

func validWaffoPancakeTimestamp(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validWaffoPancakeCheckoutURL(raw string) bool {
	if !validWaffoPancakeText(raw, 2048, false) {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.Fragment == "" && parsed.Opaque == ""
}

// ValidateWaffoPancakeCheckoutSession validates the combined provider result
// before any session identifier or URL is published to a caller.
func ValidateWaffoPancakeCheckoutSession(session *WaffoPancakeCheckoutSession) (int64, error) {
	if session == nil || !validWaffoPancakeIdentifier(session.SessionID) || !validWaffoPancakeToken(session.Token) ||
		!validWaffoPancakeTimestamp(session.ExpiresAt) || !validWaffoPancakeTimestamp(session.TokenExpiresAt) ||
		!validWaffoPancakeText(session.CheckoutURL, 2048+waffoPancakeMaxTokenLength, false) {
		return 0, ErrWaffoPancakeResponseInvalid
	}
	parsed, err := url.Parse(session.CheckoutURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Fragment != "token="+session.Token {
		return 0, ErrWaffoPancakeResponseInvalid
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	tokenExpiresAt, tokenErr := time.Parse(time.RFC3339Nano, session.TokenExpiresAt)
	now := time.Now()
	if err != nil || tokenErr != nil || !expiresAt.After(now) || !tokenExpiresAt.After(now) {
		return 0, ErrWaffoPancakeResponseInvalid
	}
	return expiresAt.Unix(), nil
}

func optionalWaffoPancakeString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

// These are Waffo Pancake's published environment verification keys. The
// request path selects exactly one key; process environment variables cannot
// silently replace this trust root.
const waffoPancakeTestWebhookPublicKey = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAxnmRY6yMMA3lVqmAU6ZG
b1sjL/+r/z6E+ZjkXaDAKiqOhk9rpazni0bNsGXwmftTPk9jy2wn+j6JHODD/WH/
SCnSfvKkLIjy4Hk7BuCgB174C0ydan7J+KgXLkOwgCAxxB68t2tezldwo74ZpXgn
F49opzMvQ9prEwIAWOE+kV9iK6gx/AckSMtHIHpUesoPDkldpmFHlB2qpf1vsFTZ
5kD6DmGl+2GIVK01aChy2lk8pLv0yUMu18v44sLkO5M44TkGPJD9qG09wrvVG2wp
OTVCn1n5pP8P+HRLcgzbUB3OlZVfdFurn6EZwtyL4ZD9kdkQ4EZE/9inKcp3c1h4
xwIDAQAB
-----END PUBLIC KEY-----`

const waffoPancakeProdWebhookPublicKey = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAz+xApdTIb4ua+DgZKQ54
iBsD82ybyhGCLRETONW4Jgbb3A8DUM1LqBk6r/CmTOCHqLalTQHNigvP3R5zkDNX
iRJz6gA4MJ/+8K0+mnEE2RISQzN+Qu65TNd6svb+INm/kMaftY4uIXr6y6kchtTJ
dwnQhcKdAL2v7h7IFnkVelQsKxDdb2PqX8xX/qwd01iXvMcpCCaXovUwZsxH2QN5
ZKBTseJivbhUeyJCco4fdUyxOMHe2ybCVhyvim2uxAl1nkvL5L8RCWMCAV55LLo0
9OhmLahz/DYNu13YLVP6dvIT09ZFBYU6Owj1NxdinTynlJCFS9VYwBgmftosSE1U
dwIDAQAB
-----END PUBLIC KEY-----`

func (rsaWaffoPancakeWebhookVerifier) Verify(payload []byte, signatureHeader, expectedEnvironment string) (*WaffoPancakeWebhookEvent, error) {
	if expectedEnvironment != WaffoPancakeModeTest && expectedEnvironment != WaffoPancakeModeProd {
		return nil, errors.New("invalid Waffo Pancake webhook environment")
	}
	prodKey, err := parseWaffoPancakePublicKey(waffoPancakeProdWebhookPublicKey)
	if err != nil {
		return nil, err
	}
	testKey, err := parseWaffoPancakePublicKey(waffoPancakeTestWebhookPublicKey)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	// Match the provider SDK's environment auto-detection: authenticate with
	// either published trust root first, then let the controller compare the
	// signed event mode with the URL environment. This makes a valid event sent
	// to the wrong registered URL acknowledgeable without settling it.
	if event, verifyErr := verifyWaffoPancakeWebhookWithKey(payload, signatureHeader, "", now, prodKey); verifyErr == nil {
		return event, nil
	}
	if event, verifyErr := verifyWaffoPancakeWebhookWithKey(payload, signatureHeader, "", now, testKey); verifyErr == nil {
		return event, nil
	}
	return nil, errors.New("invalid Waffo Pancake webhook signature")
}

func verifyWaffoPancakeWebhookWithKey(payload []byte, signatureHeader, expectedEnvironment string, now time.Time, key *rsa.PublicKey) (*WaffoPancakeWebhookEvent, error) {
	if len(payload) == 0 || len(payload) > waffoPancakeRequestBodyLimit || key == nil || key.N.BitLen() < 2048 ||
		(signatureHeader == "" || len(signatureHeader) > 16<<10 || signatureHeader != strings.TrimSpace(signatureHeader)) {
		return nil, errors.New("invalid Waffo Pancake webhook")
	}
	timestamp, encodedSignature, err := parseWaffoPancakeSignatureHeader(signatureHeader)
	if err != nil {
		return nil, err
	}
	timestampMillis, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || timestampMillis <= 0 {
		return nil, errors.New("invalid Waffo Pancake webhook timestamp")
	}
	delta := now.UnixMilli() - timestampMillis
	if delta < -waffoPancakeWebhookTolerance.Milliseconds() || delta > waffoPancakeWebhookTolerance.Milliseconds() {
		return nil, errors.New("Waffo Pancake webhook timestamp outside tolerance")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(encodedSignature)
	if err != nil || len(signature) != key.Size() {
		return nil, errors.New("invalid Waffo Pancake webhook signature")
	}
	digest := sha256.Sum256([]byte(timestamp + "." + string(payload)))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return nil, errors.New("invalid Waffo Pancake webhook signature")
	}
	var event WaffoPancakeWebhookEvent
	if common.Unmarshal(payload, &event) != nil || !validWaffoPancakeWebhookEnvelope(&event, expectedEnvironment) {
		return nil, errors.New("invalid Waffo Pancake webhook event")
	}
	return &event, nil
}

func parseWaffoPancakeSignatureHeader(header string) (string, string, error) {
	var timestamp string
	var signature string
	seenTimestamp := false
	seenSignature := false
	parts := strings.Split(header, ",")
	if len(parts) > 8 {
		return "", "", errors.New("invalid Waffo Pancake signature header")
	}
	for _, part := range parts {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "t":
			if seenTimestamp {
				return "", "", errors.New("duplicate Waffo Pancake signature timestamp")
			}
			seenTimestamp = true
			timestamp = value
		case "v1":
			if seenSignature {
				return "", "", errors.New("duplicate Waffo Pancake signature")
			}
			seenSignature = true
			signature = value
		}
	}
	if timestamp == "" || signature == "" || len(timestamp) > 20 || len(signature) > 4096 {
		return "", "", errors.New("malformed Waffo Pancake signature header")
	}
	for i := range len(timestamp) {
		if timestamp[i] < '0' || timestamp[i] > '9' {
			return "", "", errors.New("invalid Waffo Pancake signature timestamp")
		}
	}
	return timestamp, signature, nil
}

func validWaffoPancakeWebhookEnvelope(event *WaffoPancakeWebhookEvent, expectedEnvironment string) bool {
	if event == nil || !validWaffoPancakeText(event.ID, 255, false) || !validWaffoPancakeText(event.Timestamp, 64, false) ||
		!validWaffoPancakeText(event.EventType, 128, false) || !validWaffoPancakeText(event.EventID, 255, true) ||
		!setting.ValidWaffoPancakeShortID(event.StoreID, "STO") || !validWaffoPancakeText(event.StoreName, 255, true) {
		return false
	}
	if event.Mode != WaffoPancakeModeTest && event.Mode != WaffoPancakeModeProd {
		return false
	}
	return expectedEnvironment == "" || event.Mode == expectedEnvironment
}

func parseWaffoPancakePublicKey(raw string) (*rsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil || strings.TrimSpace(string(rest)) != "" || len(block.Bytes) == 0 || len(block.Bytes) > 16<<10 {
		return nil, errors.New("invalid Waffo Pancake public key")
	}
	var key *rsa.PublicKey
	var err error
	switch block.Type {
	case "PUBLIC KEY":
		var parsed any
		parsed, err = x509.ParsePKIXPublicKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PublicKey)
			if !ok {
				err = errors.New("Waffo Pancake public key is not RSA")
			}
		}
	case "RSA PUBLIC KEY":
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		err = errors.New("unsupported Waffo Pancake public key")
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, errors.New("invalid Waffo Pancake public key")
	}
	return key, nil
}
