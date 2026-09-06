package commerce

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	WaffoSignatureHeader        = "X-SIGNATURE"
	waffoAPIKeyHeader           = "X-API-KEY"
	waffoAPIVersionHeader       = "X-API-VERSION"
	waffoSDKVersionHeader       = "X-SDK-VERSION"
	waffoAPIVersion             = "1.0.0"
	waffoSDKVersion             = "waffo-go/1.3.2"
	waffoCreateTimeout          = 30 * time.Second
	waffoCreateRequestLimit     = 64 << 10
	waffoCreateResponseLimit    = 64 << 10
	waffoCreateHeaderLimit      = 64 << 10
	waffoCreateOrderActionLimit = 8 << 10
)

// WaffoCreateOrderResult is the bounded, credential-free result returned by
// an injectable classic-Waffo checkout client.
type WaffoCreateOrderResult struct {
	PaymentURL       string
	PaymentRequestID string
	MerchantOrderID  string
	AcquiringOrderID string
}

// WaffoOrderClient keeps live provider I/O behind a narrow injectable
// boundary. Tests can replace it without credentials or network access.
type WaffoOrderClient interface {
	CreateOrder(context.Context, setting.WaffoConfig, billingsvc.WaffoCheckoutRequest) (WaffoCreateOrderResult, error)
}

// WaffoSignatureCodec is the independently injectable raw-body signing and
// verification boundary used by both provider responses and webhooks.
type WaffoSignatureCodec interface {
	Sign(payload []byte, privateKey string) (string, error)
	Verify(payload []byte, signature, publicKey string) bool
}

type rsaWaffoSignatureCodec struct{}

func (rsaWaffoSignatureCodec) Sign(payload []byte, privateKey string) (string, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(privateKey)
	if err != nil {
		return "", errors.New("invalid Waffo signing key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok || rsaKey.N.BitLen() < 2048 {
		return "", errors.New("invalid Waffo signing key")
	}
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("Waffo signing failed")
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (rsaWaffoSignatureCodec) Verify(payload []byte, signature, publicKey string) bool {
	if signature == "" || len(signature) > 16<<10 || signature != strings.TrimSpace(signature) {
		return false
	}
	der, err := base64.StdEncoding.Strict().DecodeString(publicKey)
	if err != nil {
		return false
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	rsaKey, ok := parsed.(*rsa.PublicKey)
	if err != nil || !ok || rsaKey.N.BitLen() < 2048 {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(signature)
	if err != nil || len(decoded) != rsaKey.Size() {
		return false
	}
	digest := sha256.Sum256(payload)
	return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, digest[:], decoded) == nil
}

type waffoHTTPOrderClient struct{}

type waffoCreateError struct {
	status             int
	definitelyRejected bool
}

func (e *waffoCreateError) Error() string {
	if e.status > 0 {
		return fmt.Sprintf("Waffo API returned HTTP %d", e.status)
	}
	return "Waffo rejected the checkout request"
}

type waffoCreateResponse struct {
	Code    string `json:"code"`
	Message string `json:"msg,omitempty"`
	Data    struct {
		PaymentRequestID string `json:"paymentRequestId,omitempty"`
		MerchantOrderID  string `json:"merchantOrderId,omitempty"`
		AcquiringOrderID string `json:"acquiringOrderId,omitempty"`
		OrderStatus      string `json:"orderStatus,omitempty"`
		OrderAction      string `json:"orderAction,omitempty"`
	} `json:"data,omitempty"`
}

var waffoDefaultTransport http.RoundTripper = &http.Transport{
	Proxy:                  nil,
	DialContext:            httpx.SafeDialContext,
	ForceAttemptHTTP2:      true,
	MaxIdleConns:           16,
	MaxIdleConnsPerHost:    4,
	IdleConnTimeout:        30 * time.Second,
	TLSHandshakeTimeout:    10 * time.Second,
	ResponseHeaderTimeout:  20 * time.Second,
	MaxResponseHeaderBytes: waffoCreateHeaderLimit,
	ExpectContinueTimeout:  time.Second,
	TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
}

var waffoRuntime = struct {
	sync.RWMutex
	client     WaffoOrderClient
	signatures WaffoSignatureCodec
	transport  http.RoundTripper
}{
	client:     waffoHTTPOrderClient{},
	signatures: rsaWaffoSignatureCodec{},
	transport:  waffoDefaultTransport,
}

func SetWaffoOrderClientForTesting(client WaffoOrderClient) (restore func()) {
	if client == nil {
		client = waffoHTTPOrderClient{}
	}
	waffoRuntime.Lock()
	previous := waffoRuntime.client
	waffoRuntime.client = client
	waffoRuntime.Unlock()
	return func() {
		waffoRuntime.Lock()
		waffoRuntime.client = previous
		waffoRuntime.Unlock()
	}
}

func SetWaffoSignatureCodecForTesting(codec WaffoSignatureCodec) (restore func()) {
	if codec == nil {
		codec = rsaWaffoSignatureCodec{}
	}
	waffoRuntime.Lock()
	previous := waffoRuntime.signatures
	waffoRuntime.signatures = codec
	waffoRuntime.Unlock()
	return func() {
		waffoRuntime.Lock()
		waffoRuntime.signatures = previous
		waffoRuntime.Unlock()
	}
}

func SetWaffoTransportForTesting(transport http.RoundTripper) (restore func()) {
	if transport == nil {
		transport = waffoDefaultTransport
	}
	waffoRuntime.Lock()
	previous := waffoRuntime.transport
	waffoRuntime.transport = transport
	waffoRuntime.Unlock()
	return func() {
		waffoRuntime.Lock()
		waffoRuntime.transport = previous
		waffoRuntime.Unlock()
	}
}

func currentWaffoClient() WaffoOrderClient {
	waffoRuntime.RLock()
	defer waffoRuntime.RUnlock()
	return waffoRuntime.client
}

func currentWaffoSignatureCodec() WaffoSignatureCodec {
	waffoRuntime.RLock()
	defer waffoRuntime.RUnlock()
	return waffoRuntime.signatures
}

func currentWaffoTransport() http.RoundTripper {
	waffoRuntime.RLock()
	defer waffoRuntime.RUnlock()
	return waffoRuntime.transport
}

func (waffoHTTPOrderClient) CreateOrder(ctx context.Context, config setting.WaffoConfig, checkout billingsvc.WaffoCheckoutRequest) (WaffoCreateOrderResult, error) {
	if ctx == nil || config.APIKey == "" || config.PrivateKey == "" || config.PublicKey == "" ||
		(config.APIBaseURL != setting.WaffoProductionBaseURL && config.APIBaseURL != setting.WaffoSandboxBaseURL) {
		return WaffoCreateOrderResult{}, &waffoCreateError{definitelyRejected: true}
	}
	body, err := jsonutil.Marshal(checkout)
	if err != nil || len(body) == 0 || len(body) > waffoCreateRequestLimit {
		return WaffoCreateOrderResult{}, billingsvc.ErrWaffoCheckoutBindingMismatch
	}
	codec := currentWaffoSignatureCodec()
	signature, err := codec.Sign(body, config.PrivateKey)
	if err != nil {
		return WaffoCreateOrderResult{}, &waffoCreateError{definitelyRejected: true}
	}
	endpoint := config.APIBaseURL + "/order/create"
	if !validFixedWaffoEndpoint(endpoint) {
		return WaffoCreateOrderResult{}, &waffoCreateError{definitelyRejected: true}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return WaffoCreateOrderResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(waffoAPIKeyHeader, config.APIKey)
	request.Header.Set(WaffoSignatureHeader, signature)
	request.Header.Set(waffoAPIVersionHeader, waffoAPIVersion)
	request.Header.Set(waffoSDKVersionHeader, waffoSDKVersion)
	client := &http.Client{
		Transport: currentWaffoTransport(), Timeout: waffoCreateTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return WaffoCreateOrderResult{}, err
	}
	defer response.Body.Close()
	responseBody, err := httpx.ReadAllLimited(response.Body, waffoCreateResponseLimit)
	if err != nil {
		return WaffoCreateOrderResult{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		status := response.StatusCode
		definite := status >= 400 && status < 500 && status != 408 && status != 409 && status != 425 && status != 429
		return WaffoCreateOrderResult{}, &waffoCreateError{status: status, definitelyRejected: definite}
	}
	if responseSignature := response.Header.Get(WaffoSignatureHeader); responseSignature != "" &&
		!codec.Verify(responseBody, responseSignature, config.PublicKey) {
		return WaffoCreateOrderResult{}, errors.New("Waffo response signature verification failed")
	}
	var decoded waffoCreateResponse
	if jsonutil.Unmarshal(responseBody, &decoded) != nil || len(decoded.Code) > 64 || len(decoded.Message) > 2048 {
		return WaffoCreateOrderResult{}, errors.New("invalid Waffo response")
	}
	if decoded.Code == "" {
		return WaffoCreateOrderResult{}, errors.New("invalid Waffo response")
	}
	// The pinned SDK classifies E0001 as an unknown create status: the
	// provider may have accepted the signed order even though it could not
	// return a definitive response. Keep the durable local order pending so a
	// later signed webhook can bind and settle it.
	if decoded.Code == "E0001" {
		return WaffoCreateOrderResult{}, &waffoCreateError{}
	}
	if decoded.Code != "0" {
		return WaffoCreateOrderResult{}, &waffoCreateError{definitelyRejected: true}
	}
	if !billingsvc.ValidWaffoCreateIdentifier(decoded.Data.PaymentRequestID) ||
		!billingsvc.ValidWaffoCreateIdentifier(decoded.Data.MerchantOrderID) ||
		!billingsvc.ValidWaffoCreateIdentifier(decoded.Data.AcquiringOrderID) || len(decoded.Data.OrderStatus) > 64 ||
		decoded.Data.PaymentRequestID != checkout.PaymentRequestID || decoded.Data.MerchantOrderID != checkout.MerchantOrderID {
		return WaffoCreateOrderResult{}, billingsvc.ErrWaffoCheckoutBindingMismatch
	}
	paymentURL, err := parseWaffoPaymentURL(decoded.Data.OrderAction)
	if err != nil {
		return WaffoCreateOrderResult{}, err
	}
	return WaffoCreateOrderResult{
		PaymentURL: paymentURL, PaymentRequestID: decoded.Data.PaymentRequestID,
		MerchantOrderID: decoded.Data.MerchantOrderID, AcquiringOrderID: decoded.Data.AcquiringOrderID,
	}, nil
}

func validFixedWaffoEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return raw == setting.WaffoProductionBaseURL+"/order/create" || raw == setting.WaffoSandboxBaseURL+"/order/create"
}

func parseWaffoPaymentURL(action string) (string, error) {
	if action == "" || len(action) > waffoCreateOrderActionLimit || strings.ContainsRune(action, '\x00') {
		return "", billingsvc.ErrWaffoCheckoutBindingMismatch
	}
	if validWaffoPaymentURL(action) {
		return action, nil
	}
	var decoded struct {
		ActionType  string `json:"actionType"`
		WebURL      string `json:"webUrl"`
		DeeplinkURL string `json:"deeplinkUrl"`
	}
	if jsonutil.Unmarshal([]byte(action), &decoded) != nil || len(decoded.ActionType) > 64 {
		return "", billingsvc.ErrWaffoCheckoutBindingMismatch
	}
	candidate := decoded.WebURL
	if decoded.ActionType == "DEEPLINK" && decoded.DeeplinkURL != "" {
		candidate = decoded.DeeplinkURL
	}
	if !validWaffoPaymentURL(candidate) {
		return "", billingsvc.ErrWaffoCheckoutBindingMismatch
	}
	return candidate, nil
}

func validWaffoPaymentURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return false
	}
	for _, char := range raw {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" && parsed.Opaque == ""
}

func waffoCreateDefinitelyRejected(err error) bool {
	var createErr *waffoCreateError
	return errors.As(err, &createErr) && createErr.definitelyRejected
}
