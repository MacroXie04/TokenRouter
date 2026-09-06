package commerce

import (
	"context"
	"crypto/tls"
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
	creemProductionCheckoutURL = "https://api.creem.io/v1/checkouts"
	creemTestCheckoutURL       = "https://test-api.creem.io/v1/checkouts"
	creemCheckoutTimeout       = 30 * time.Second
	creemCheckoutResponseLimit = 64 << 10
)

type creemCheckoutResponse struct {
	CheckoutURL string `json:"checkout_url"`
	ID          string `json:"id"`
}

type creemCheckoutHTTPError struct {
	status int
}

func (e *creemCheckoutHTTPError) Error() string {
	return fmt.Sprintf("Creem API returned HTTP %d", e.status)
}

var creemProductionTransport = &http.Transport{
	Proxy:                  nil,
	DialContext:            httpx.SafeDialContext,
	ForceAttemptHTTP2:      true,
	MaxIdleConns:           16,
	MaxIdleConnsPerHost:    2,
	IdleConnTimeout:        30 * time.Second,
	TLSHandshakeTimeout:    10 * time.Second,
	ResponseHeaderTimeout:  15 * time.Second,
	MaxResponseHeaderBytes: 64 << 10,
	TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
}

var creemTransportRegistry = struct {
	sync.RWMutex
	transport http.RoundTripper
}{transport: creemProductionTransport}

// SetCreemCheckoutTransportForTesting replaces only the Creem checkout
// transport and returns a restoration closure. Production endpoints remain
// fixed; tests can deterministically intercept requests without network use.
func SetCreemCheckoutTransportForTesting(transport http.RoundTripper) (restore func()) {
	if transport == nil {
		transport = creemProductionTransport
	}
	creemTransportRegistry.Lock()
	previous := creemTransportRegistry.transport
	creemTransportRegistry.transport = transport
	creemTransportRegistry.Unlock()
	return func() {
		creemTransportRegistry.Lock()
		creemTransportRegistry.transport = previous
		creemTransportRegistry.Unlock()
	}
}

func currentCreemTransport() http.RoundTripper {
	creemTransportRegistry.RLock()
	defer creemTransportRegistry.RUnlock()
	return creemTransportRegistry.transport
}

func createCreemCheckout(ctx context.Context, config setting.CreemConfig, snapshot billingsvc.CreemCheckoutRequest) (*creemCheckoutResponse, error) {
	if ctx == nil || config.APIKey == "" || config.APIKey != strings.TrimSpace(config.APIKey) {
		return nil, errors.New("Creem API is not configured")
	}
	payload, err := jsonutil.Marshal(snapshot)
	if err != nil || len(payload) == 0 || len(payload) > 64<<10 {
		return nil, billingsvc.ErrCreemCheckoutBindingMismatch
	}
	endpoint := creemProductionCheckoutURL
	if config.TestMode {
		endpoint = creemTestCheckoutURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", config.APIKey)
	client := &http.Client{
		Transport: currentCreemTransport(),
		Timeout:   creemCheckoutTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := httpx.ReadAllLimited(response.Body, creemCheckoutResponseLimit)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &creemCheckoutHTTPError{status: response.StatusCode}
	}
	var result creemCheckoutResponse
	if jsonutil.Unmarshal(body, &result) != nil || !validCreemCheckoutURL(result.CheckoutURL) || !billingsvc.ValidCreemCheckoutID(result.ID) {
		return nil, billingsvc.ErrCreemCheckoutBindingMismatch
	}
	return &result, nil
}

func validCreemCheckoutURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 || strings.ContainsRune(raw, '\x00') {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func creemRequestDefinitelyRejected(err error) bool {
	var responseError *creemCheckoutHTTPError
	if !errors.As(err, &responseError) {
		return false
	}
	status := responseError.status
	return status >= 400 && status < 500 && status != 408 && status != 409 && status != 425 && status != 429
}
