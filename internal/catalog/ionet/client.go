package ionet

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	minAPIKeyBytes                = 8
	maxAPIKeyBytes                = 4096
	maxUpstreamJSONBytes    int64 = 2 << 20
	maxUpstreamLogBytes     int64 = 4 << 20
	maxUpstreamErrorBytes   int64 = 8 << 10
	maxUpstreamRequestBytes       = MaxRequestBodyBytes
)

var (
	errInvalidCredential = errors.New("io.net credential is invalid")
	errRequestFailed     = errors.New("io.net request failed")
	productionHTTPClient = defaultHTTPClient()
)

// APIError is intentionally sparse: upstream response bodies are untrusted
// and can contain credentials or infrastructure details, so they are never
// included in operator-facing errors.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e != nil && e.Message != "" {
		return e.Message
	}
	return errRequestFailed.Error()
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client is an io.net client. Its fields are deliberately private so
// production callers cannot replace the fixed provider destination.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient httpDoer
}

func NewEnterpriseClient(apiKey string) *Client {
	return newClient(apiKey, DefaultEnterpriseBaseURL, productionHTTPClient)
}

func NewClient(apiKey string) *Client {
	return newClient(apiKey, DefaultBaseURL, productionHTTPClient)
}

func newClient(apiKey, baseURL string, doer httpDoer) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: strings.TrimSpace(apiKey), httpClient: doer}
}

func defaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = httpx.SafeDialContext
	transport.MaxResponseHeaderBytes = 64 << 10
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	} else {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	return &http.Client{
		Transport: transport,
		Timeout:   DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func validAPIKey(value string) bool {
	if len(value) < minAPIKeyBytes || len(value) > maxAPIKeyBytes || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

// ValidateAPIKey validates a credential without sending or returning it.
func ValidateAPIKey(value string) error {
	if !validAPIKey(strings.TrimSpace(value)) {
		return errInvalidCredential
	}
	return nil
}

func (c *Client) makeRequest(ctx context.Context, method, endpoint string, body any, responseLimit int64) ([]byte, error) {
	if c == nil || c.httpClient == nil || ctx == nil || !validAPIKey(c.apiKey) {
		return nil, errInvalidCredential
	}
	if responseLimit < 1 || !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") {
		return nil, errRequestFailed
	}
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errRequestFailed
	}
	requestURL, err := url.Parse(c.baseURL + endpoint)
	if err != nil || requestURL.Scheme != base.Scheme || requestURL.Host != base.Host || !strings.HasPrefix(requestURL.Path, base.Path+"/") {
		return nil, errRequestFailed
	}

	var encoded []byte
	if body != nil {
		encoded, err = jsonutil.Marshal(body)
		if err != nil || int64(len(encoded)) > maxUpstreamRequestBytes {
			return nil, errors.New("io.net request payload is invalid")
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, errRequestFailed
	}
	request.Header.Set("X-API-KEY", c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil || response == nil {
		return nil, errRequestFailed
	}
	if response.Body == nil {
		return nil, errInvalidProviderResponse
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Drain only a small bounded prefix so keep-alive connections can be
		// reused without retaining or reflecting an untrusted provider error.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxUpstreamErrorBytes))
		return nil, &APIError{Code: response.StatusCode, Message: errRequestFailed.Error()}
	}
	data, err := httpx.ReadAllLimited(response.Body, responseLimit)
	if err != nil {
		return nil, errInvalidProviderResponse
	}
	if bytes.Contains(data, []byte(c.apiKey)) {
		return nil, errInvalidProviderResponse
	}
	return data, nil
}

func (c *Client) makeJSONRequest(ctx context.Context, method, endpoint string, body, target any) error {
	data, err := c.makeRequest(ctx, method, endpoint, body, maxUpstreamJSONBytes)
	if err != nil {
		return err
	}
	if containsCredentialJSON(data, c.apiKey) {
		return errInvalidProviderResponse
	}
	return decodeResponse(data, target)
}

func (c *Client) makeDataRequest(ctx context.Context, method, endpoint string, body, target any) error {
	data, err := c.makeRequest(ctx, method, endpoint, body, maxUpstreamJSONBytes)
	if err != nil {
		return err
	}
	if containsCredentialJSON(data, c.apiKey) {
		return errInvalidProviderResponse
	}
	return decodeDataResponse(data, target)
}
