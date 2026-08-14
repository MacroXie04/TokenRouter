package jimeng

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	SubmitAction        = "CVSync2AsyncSubmitTask"
	FetchAction         = "CVSync2AsyncGetResult"
	APIVersion          = "2022-08-31"
	DefaultFrames       = 121
	LongVideoFrames     = 241
	MaxImageBytes       = 4*1024*1024 + 700*1024
	maxResponseBytes    = 1 << 20
	providerSuccessCode = 10000
)

type Request struct {
	ReqKey           string   `json:"req_key"`
	BinaryDataBase64 []string `json:"binary_data_base64,omitempty"`
	ImageURLs        []string `json:"image_urls,omitempty"`
	Prompt           string   `json:"prompt,omitempty"`
	Seed             int64    `json:"seed"`
	AspectRatio      string   `json:"aspect_ratio"`
	Frames           int      `json:"frames,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	RecoveryToken    string   `json:"recovery_token,omitempty"`
}

type SubmitResult struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      struct {
		TaskID string `json:"task_id"`
	} `json:"data"`
}

type TaskResult struct {
	Code int `json:"code"`
	Data struct {
		BinaryDataBase64 []any  `json:"binary_data_base64"`
		ImageURLs        any    `json:"image_urls"`
		ResponseData     string `json:"resp_data"`
		Status           string `json:"status"`
		VideoURL         string `json:"video_url"`
	} `json:"data"`
	Message     string `json:"message"`
	RequestID   string `json:"request_id"`
	Status      int    `json:"status"`
	TimeElapsed string `json:"time_elapsed"`
}

type ProviderError struct {
	Code    int
	Message string
}

func (e *ProviderError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Jimeng provider error %d", e.Code)
	}
	return e.Message
}

type SubmitError struct {
	Err             error
	Dispatched      bool
	MayHaveAccepted bool
}

func (e *SubmitError) Error() string {
	return e.Err.Error()
}

func (e *SubmitError) Unwrap() error {
	return e.Err
}

func SubmitMayHaveBeenAccepted(err error) bool {
	var submitErr *SubmitError
	return errors.As(err, &submitErr) && submitErr.MayHaveAccepted
}

func SubmitWasDispatched(err error) bool {
	var submitErr *SubmitError
	return errors.As(err, &submitErr) && submitErr.Dispatched
}

func submitError(err error, dispatched, mayHaveAccepted bool) error {
	if err == nil {
		return nil
	}
	return &SubmitError{Err: err, Dispatched: dispatched, MayHaveAccepted: mayHaveAccepted}
}

type Client struct {
	HTTPClient *http.Client
	Now        func() time.Time
}

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: common.SafeDialContext,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: common.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false),
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func PrepareSubmitRequest(request Request, mappedModel string) (Request, error) {
	request.ReqKey = strings.TrimSpace(mappedModel)
	if request.ReqKey == "" {
		return Request{}, errors.New("req_key is required")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return Request{}, errors.New("prompt is required")
	}
	if request.Frames == 0 {
		request.Frames = DefaultFrames
	}
	if request.Frames != DefaultFrames && request.Frames != LongVideoFrames {
		return Request{}, fmt.Errorf("frames must be %d or %d", DefaultFrames, LongVideoFrames)
	}
	if len(request.BinaryDataBase64) > 0 && len(request.ImageURLs) > 0 {
		return Request{}, errors.New("binary_data_base64 and image_urls are mutually exclusive")
	}
	for _, encoded := range request.BinaryDataBase64 {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return Request{}, errors.New("binary_data_base64 contains invalid base64")
		}
		if len(decoded) > MaxImageBytes {
			return Request{}, fmt.Errorf("image exceeds the %d byte limit", MaxImageBytes)
		}
	}
	for _, rawURL := range request.ImageURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return Request{}, errors.New("image_urls must contain HTTP or HTTPS URLs")
		}
	}

	imageCount := len(request.BinaryDataBase64) + len(request.ImageURLs)
	if strings.Contains(request.ReqKey, "jimeng_v30") {
		switch {
		case request.ReqKey == "jimeng_v30_pro":
			request.ReqKey = "jimeng_ti2v_v30_pro"
		case imageCount > 1:
			request.ReqKey = strings.TrimSuffix(strings.Replace(request.ReqKey, "jimeng_v30", "jimeng_i2v_first_tail_v30", 1), "p")
		case imageCount == 1:
			request.ReqKey = strings.TrimSuffix(strings.Replace(request.ReqKey, "jimeng_v30", "jimeng_i2v_first_v30", 1), "p")
		default:
			request.ReqKey = strings.Replace(request.ReqKey, "jimeng_v30", "jimeng_t2v_v30", 1)
		}
	}
	request.TaskID = ""
	request.RecoveryToken = ""
	return request, nil
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, payload Request) (*SubmitResult, []byte, error) {
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, nil, submitError(err, false, false)
	}
	request, err := c.newRequest(ctx, baseURL, apiKey, SubmitAction, body)
	if err != nil {
		return nil, nil, submitError(err, false, false)
	}
	raw, requestWritten, responseReceived, err := c.do(request)
	if err != nil {
		mayHaveAccepted := requestWritten || responseReceived
		var upstream *relaycommon.UpstreamError
		if errors.As(err, &upstream) {
			mayHaveAccepted = upstream.StatusCode == http.StatusRequestTimeout ||
				(upstream.StatusCode >= 300 && upstream.StatusCode < 400) || upstream.StatusCode >= 500
		}
		return nil, raw, submitError(err, requestWritten || responseReceived, mayHaveAccepted)
	}
	var result SubmitResult
	if err := common.Unmarshal(raw, &result); err != nil {
		return nil, raw, submitError(fmt.Errorf("decode Jimeng submit response: %w", err), true, true)
	}
	if result.Code != providerSuccessCode {
		return nil, raw, submitError(&ProviderError{Code: result.Code, Message: result.Message}, true, false)
	}
	if strings.TrimSpace(result.Data.TaskID) == "" {
		return nil, raw, submitError(errors.New("Jimeng submit response is missing task_id"), true, true)
	}
	return &result, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, modelName, upstreamTaskID string) (*TaskResult, []byte, error) {
	payload := struct {
		ReqKey string `json:"req_key"`
		TaskID string `json:"task_id"`
	}{ReqKey: modelName, TaskID: upstreamTaskID}
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	request, err := c.newRequest(ctx, baseURL, apiKey, FetchAction, body)
	if err != nil {
		return nil, nil, err
	}
	raw, _, _, err := c.do(request)
	if err != nil {
		return nil, raw, err
	}
	var result TaskResult
	if err := common.Unmarshal(raw, &result); err != nil {
		return nil, raw, fmt.Errorf("decode Jimeng task response: %w", err)
	}
	if result.Code != providerSuccessCode {
		return nil, raw, &ProviderError{Code: result.Code, Message: result.Message}
	}
	return &result, raw, nil
}

func (c *Client) newRequest(ctx context.Context, baseURL, apiKey, action string, body []byte) (*http.Request, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("Jimeng channel base URL is empty")
	}
	gatewayRelay := strings.HasPrefix(apiKey, "sk-")
	endpoint := baseURL + "/"
	if gatewayRelay {
		endpoint = baseURL + "/jimeng/"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid Jimeng channel base URL")
	}
	query := parsed.Query()
	query.Set("Action", action)
	query.Set("Version", APIVersion)
	parsed.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if gatewayRelay {
		request.Header.Set("Authorization", "Bearer "+apiKey)
		return request, nil
	}
	accessKey, secretKey, ok := strings.Cut(apiKey, "|")
	accessKey = strings.TrimSpace(accessKey)
	secretKey = strings.TrimSpace(secretKey)
	if !ok || accessKey == "" || secretKey == "" || strings.Contains(secretKey, "|") {
		return nil, errors.New("invalid Jimeng API key: expected access_key|secret_key")
	}
	now := time.Now().UTC()
	if c != nil && c.Now != nil {
		now = c.Now().UTC()
	}
	signRequest(request, body, accessKey, secretKey, now)
	return request, nil
}

func (c *Client) do(request *http.Request) ([]byte, bool, bool, error) {
	client := c.HTTPClient
	if client == nil {
		client = NewHTTPClient()
	}
	var requestWritten atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
		requestWritten.Store(true)
	}}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, requestWritten.Load(), response != nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, requestWritten.Load(), true, err
	}
	if len(raw) > maxResponseBytes {
		return nil, requestWritten.Load(), true, errors.New("Jimeng response exceeds 1 MiB")
	}
	if response.StatusCode != http.StatusOK {
		return raw, requestWritten.Load(), true, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	return raw, requestWritten.Load(), true, nil
}

func signRequest(request *http.Request, body []byte, accessKey, secretKey string, now time.Time) {
	payloadHash := sha256.Sum256(body)
	hexPayloadHash := hex.EncodeToString(payloadHash[:])
	xDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	request.Host = request.URL.Host
	request.Header.Set("X-Date", xDate)
	request.Header.Set("X-Content-Sha256", hexPayloadHash)

	headers := map[string]string{
		"content-type":     request.Header.Get("Content-Type"),
		"host":             request.URL.Host,
		"x-content-sha256": hexPayloadHash,
		"x-date":           xDate,
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonicalHeaders strings.Builder
	for _, key := range keys {
		canonicalHeaders.WriteString(key)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers[key]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(keys, ";")
	canonicalRequest := strings.Join([]string{
		request.Method,
		request.URL.EscapedPath(),
		request.URL.Query().Encode(),
		canonicalHeaders.String(),
		signedHeaders,
		hexPayloadHash,
	}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := shortDate + "/cn-north-1/cv/request"
	stringToSign := "HMAC-SHA256\n" + xDate + "\n" + credentialScope + "\n" + hex.EncodeToString(canonicalHash[:])
	dateKey := hmacSHA256([]byte(secretKey), []byte(shortDate))
	regionKey := hmacSHA256(dateKey, []byte("cn-north-1"))
	serviceKey := hmacSHA256(regionKey, []byte("cv"))
	signingKey := hmacSHA256(serviceKey, []byte("request"))
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	request.Header.Set("Authorization", "HMAC-SHA256 Credential="+accessKey+"/"+credentialScope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
