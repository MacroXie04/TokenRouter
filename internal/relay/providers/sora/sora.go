// Package sora implements the bounded OpenAI-compatible asynchronous video
// protocol used by Sora channels. It is intentionally standalone: request
// conversion, direct SSRF-safe transport, and response parsing do not depend on
// an external provider SDK.
package sora

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxRequestBodyBytes    int64 = 16 << 20
	MaxResponseBodyBytes   int64 = 1 << 20
	MaxContentBodyBytes    int64 = 512 << 20
	maxPromptBytes               = 32 << 10
	maxParts                     = 64
	maxFieldNameBytes            = 128
	maxFilenameBytes             = 255
	maxModelBytes                = 128
	maxProviderTaskIDBytes       = 191
	DefaultSeconds               = 4
	MaxSeconds                   = 3600
	DefaultSize                  = "720x1280"
)

var ModelList = []string{"sora-2", "sora-2-pro"}

// RequestedModel extracts the client-facing model before channel selection.
// Full prompt, duration, size, and multipart validation is performed later by
// PrepareSubmit with the selected channel's model mapping.
func RequestedModel(raw []byte, contentType string) (string, error) {
	if int64(len(raw)) > MaxRequestBodyBytes {
		return "", httpx.ErrBodyTooLarge
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", errors.New("content type must be application/json or multipart/form-data")
	}
	var model string
	switch mediaType {
	case "application/json":
		var body map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&body); err != nil || body == nil {
			return "", errors.New("invalid JSON video request")
		}
		model, _ = body["model"].(string)
	case "multipart/form-data":
		boundary := params["boundary"]
		if boundary == "" || len(boundary) > 200 {
			return "", errors.New("multipart boundary is invalid")
		}
		reader := multipart.NewReader(bytes.NewReader(raw), boundary)
		parts := 0
		for {
			part, nextErr := reader.NextPart()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				return "", errors.New("invalid multipart video request")
			}
			parts++
			if parts > maxParts {
				_ = part.Close()
				return "", errors.New("multipart video request has too many parts")
			}
			if part.FormName() == "model" && part.FileName() == "" {
				value, readErr := httpx.ReadAllLimited(part, maxModelBytes)
				_ = part.Close()
				if readErr != nil {
					return "", errors.New("video model is too large")
				}
				model = string(value)
				break
			}
			_ = part.Close()
		}
	default:
		return "", errors.New("content type must be application/json or multipart/form-data")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("model is required")
	}
	if len(model) > maxModelBytes || !utf8.ValidString(model) {
		return "", errors.New("model is invalid or too large")
	}
	return model, nil
}

// PreparedRequest is the validated provider payload plus immutable billing
// inputs derived from it.
type PreparedRequest struct {
	Body              []byte
	ContentType       string
	Model             string
	Prompt            string
	Seconds           int
	Size              string
	HasInputReference bool
}

type ResponseError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// Response is the OpenAI video task representation returned by submit/fetch.
type Response struct {
	ID                 string         `json:"id"`
	TaskID             string         `json:"task_id,omitempty"`
	Object             string         `json:"object"`
	Model              string         `json:"model"`
	Status             string         `json:"status"`
	Progress           int            `json:"progress"`
	CreatedAt          int64          `json:"created_at"`
	CompletedAt        int64          `json:"completed_at,omitempty"`
	ExpiresAt          int64          `json:"expires_at,omitempty"`
	Seconds            string         `json:"seconds,omitempty"`
	Size               string         `json:"size,omitempty"`
	RemixedFromVideoID string         `json:"remixed_from_video_id,omitempty"`
	Error              *ResponseError `json:"error,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// RequestError records whether a submit may have reached the provider. A
// caller must never automatically replay an error marked Dispatched.
type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Sora request failed"
	}
	return e.Err.Error()
}

func (e *RequestError) Unwrap() error { return e.Err }

func SubmitWasDispatched(err error) bool {
	var requestErr *RequestError
	return errors.As(err, &requestErr) && requestErr.Dispatched
}

// PrepareSubmit validates JSON or multipart input and replaces the client
// model with the selected channel's mapped model. Remix requests inherit their
// model, seconds, and size from the origin task when fields are omitted.
func PrepareSubmit(
	raw []byte,
	contentType, originModel, mappedModel string,
	remix bool,
	inheritedSeconds int,
	inheritedSize string,
) (*PreparedRequest, error) {
	if int64(len(raw)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	originModel = strings.TrimSpace(originModel)
	mappedModel = strings.TrimSpace(mappedModel)
	if !supportedModel(originModel) {
		return nil, fmt.Errorf("unsupported video model %q", originModel)
	}
	if mappedModel == "" || len(mappedModel) > maxModelBytes || !utf8.ValidString(mappedModel) {
		return nil, errors.New("mapped video model is invalid")
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, errors.New("content type must be application/json or multipart/form-data")
	}
	switch mediaType {
	case "application/json":
		return prepareJSON(raw, mediaType, originModel, mappedModel, remix, inheritedSeconds, inheritedSize)
	case "multipart/form-data":
		return prepareMultipart(raw, params["boundary"], originModel, mappedModel, remix, inheritedSeconds, inheritedSize)
	default:
		return nil, errors.New("content type must be application/json or multipart/form-data")
	}
}

func supportedModel(model string) bool {
	for _, candidate := range ModelList {
		if model == candidate {
			return true
		}
	}
	return false
}

func prepareJSON(
	raw []byte,
	contentType, originModel, mappedModel string,
	remix bool,
	inheritedSeconds int,
	inheritedSize string,
) (*PreparedRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return nil, errors.New("invalid JSON video request")
	}
	if body == nil {
		return nil, errors.New("video request must be a JSON object")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("video request contains trailing JSON")
	}
	prompt, _ := body["prompt"].(string)
	seconds, size, err := validatedInputs(body, prompt, originModel, remix, inheritedSeconds, inheritedSize)
	if err != nil {
		return nil, err
	}
	body["model"] = mappedModel
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("encode video request")
	}
	if int64(len(encoded)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	return &PreparedRequest{
		Body: encoded, ContentType: contentType, Model: originModel,
		Prompt: strings.TrimSpace(prompt), Seconds: seconds, Size: size,
		HasInputReference: jsonHasInputReference(body),
	}, nil
}

func prepareMultipart(
	raw []byte,
	boundary, originModel, mappedModel string,
	remix bool,
	inheritedSeconds int,
	inheritedSize string,
) (*PreparedRequest, error) {
	if boundary == "" || len(boundary) > 200 {
		return nil, errors.New("multipart boundary is invalid")
	}
	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	if err := writer.WriteField("model", mappedModel); err != nil {
		return nil, errors.New("encode multipart model")
	}
	fields := make(map[string]any)
	hasInput := false
	parts := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid multipart video request")
		}
		parts++
		if parts > maxParts {
			_ = part.Close()
			return nil, errors.New("multipart video request has too many parts")
		}
		name := part.FormName()
		filename := filepath.Base(part.FileName())
		if name == "" || len(name) > maxFieldNameBytes || !utf8.ValidString(name) ||
			len(filename) > maxFilenameBytes || !utf8.ValidString(filename) {
			_ = part.Close()
			return nil, errors.New("multipart field name is invalid")
		}
		data, readErr := httpx.ReadAllLimited(part, MaxRequestBodyBytes)
		_ = part.Close()
		if readErr != nil {
			return nil, readErr
		}
		if name == "model" {
			continue
		}
		if filename == "." {
			filename = ""
		}
		if filename == "" {
			value := string(data)
			if len(value) > maxPromptBytes && name == "prompt" {
				return nil, errors.New("prompt is too large")
			}
			if _, exists := fields[name]; exists && isVideoScalarField(name) {
				return nil, fmt.Errorf("multipart field %s must not be repeated", name)
			}
			if _, exists := fields[name]; !exists {
				fields[name] = value
			}
			if err := writer.WriteField(name, value); err != nil {
				return nil, errors.New("encode multipart field")
			}
		} else {
			if isVideoScalarField(name) {
				return nil, fmt.Errorf("multipart field %s must be text", name)
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
				"name": name, "filename": filename,
			}))
			contentType := strings.TrimSpace(part.Header.Get("Content-Type"))
			if contentType == "" || len(contentType) > 255 || strings.ContainsAny(contentType, "\r\n") {
				contentType = http.DetectContentType(data)
			}
			header.Set("Content-Type", contentType)
			destination, createErr := writer.CreatePart(header)
			if createErr != nil {
				return nil, errors.New("encode multipart file")
			}
			if _, writeErr := destination.Write(data); writeErr != nil {
				return nil, errors.New("encode multipart file")
			}
		}
		if isInputField(name) && len(data) > 0 {
			hasInput = true
		}
	}
	prompt, _ := fields["prompt"].(string)
	seconds, size, err := validatedInputs(fields, prompt, originModel, remix, inheritedSeconds, inheritedSize)
	if err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, errors.New("finish multipart video request")
	}
	if int64(output.Len()) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	return &PreparedRequest{
		Body: output.Bytes(), ContentType: writer.FormDataContentType(), Model: originModel,
		Prompt: strings.TrimSpace(prompt), Seconds: seconds, Size: size, HasInputReference: hasInput,
	}, nil
}

func validatedInputs(
	body map[string]any,
	prompt, model string,
	remix bool,
	inheritedSeconds int,
	inheritedSize string,
) (int, string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return 0, "", errors.New("prompt is required")
	}
	if len(prompt) > maxPromptBytes || !utf8.ValidString(prompt) {
		return 0, "", errors.New("prompt is invalid or too large")
	}
	seconds := DefaultSeconds
	if remix && inheritedSeconds > 0 {
		seconds = inheritedSeconds
	}
	rawSeconds, hasSeconds := body["seconds"]
	rawDuration, hasDuration := body["duration"]
	if hasSeconds && hasDuration {
		return 0, "", errors.New("seconds and duration must not both be provided")
	}
	if hasSeconds {
		raw := rawSeconds
		parsed, err := parsePositiveInteger(raw)
		if err != nil {
			return 0, "", errors.New("seconds must be a positive integer")
		}
		seconds = parsed
	} else if hasDuration {
		raw := rawDuration
		parsed, err := parsePositiveInteger(raw)
		if err != nil {
			return 0, "", errors.New("duration must be a positive integer")
		}
		seconds = parsed
	}
	if seconds < 1 || seconds > MaxSeconds {
		return 0, "", fmt.Errorf("video duration must be between 1 and %d seconds", MaxSeconds)
	}
	size := DefaultSize
	if remix && inheritedSize != "" {
		size = inheritedSize
	}
	if raw, exists := body["size"]; exists {
		value, ok := raw.(string)
		if !ok {
			return 0, "", errors.New("size must be a string")
		}
		size = strings.TrimSpace(value)
	}
	if !validSize(model, size) {
		return 0, "", fmt.Errorf("size %q is not supported by model %s", size, model)
	}
	return seconds, size, nil
}

func parsePositiveInteger(value any) (int, error) {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = strings.TrimSpace(typed)
	case json.Number:
		raw = string(typed)
	case float64:
		raw = strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return typed, nil
	default:
		return 0, errors.New("not an integer")
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || parsed < 1 {
		return 0, errors.New("not a positive integer")
	}
	return int(parsed), nil
}

func validSize(model, size string) bool {
	switch size {
	case "720x1280", "1280x720":
		return true
	case "1792x1024", "1024x1792":
		return model == "sora-2-pro"
	default:
		return false
	}
}

func jsonHasInputReference(body map[string]any) bool {
	for _, key := range []string{"input_reference", "image", "images"} {
		if value, exists := body[key]; exists && value != nil {
			switch typed := value.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return true
				}
			case []any:
				if len(typed) > 0 {
					return true
				}
			default:
				return true
			}
		}
	}
	return false
}

func isInputField(name string) bool {
	return name == "input_reference" || name == "image" || name == "images"
}

func isVideoScalarField(name string) bool {
	switch name {
	case "prompt", "seconds", "duration", "size":
		return true
	default:
		return false
	}
}

// Client uses direct, no-redirect, SSRF-checked transport with bounded
// response headers and total request duration.
type Client struct {
	HTTPClient *http.Client
}

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           httpx.SafeDialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       60 * time.Second,
	}
}

func (c *Client) client() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return NewHTTPClient()
}

func EffectiveBaseURL(configured string, channelType int) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(configured), "/")
	if base == "" && channelType >= 0 && channelType < len(channelcatalog.ChannelBaseURLs) {
		base = strings.TrimRight(channelcatalog.ChannelBaseURLs[channelType], "/")
	}
	if base == "" || len(base) > 4096 {
		return "", errors.New("video channel base URL is missing or too large")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("video channel base URL is invalid")
	}
	if parsed.Scheme != "https" && !(httpx.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("video channel base URL must use HTTPS")
	}
	return base, nil
}

func (c *Client) Submit(
	ctx context.Context,
	baseURL, apiKey, publicTaskID, upstreamOriginID string,
	request *PreparedRequest,
	remix bool,
) (*Response, []byte, error) {
	if request == nil || len(request.Body) == 0 || strings.TrimSpace(apiKey) == "" || !validPublicTaskID(publicTaskID) {
		return nil, nil, &RequestError{Err: errors.New("invalid Sora submit parameters")}
	}
	path := "/v1/videos"
	if remix {
		if !validProviderTaskID(upstreamOriginID) {
			return nil, nil, &RequestError{Err: errors.New("invalid origin provider task id")}
		}
		path = "/v1/videos/" + url.PathEscape(upstreamOriginID) + "/remix"
	}
	endpoint, err := endpointURL(baseURL, path)
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(request.Body))
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	httpRequest.Header.Set("Authorization", "Bearer "+apiKey)
	httpRequest.Header.Set("Content-Type", request.ContentType)
	httpRequest.Header.Set("Idempotency-Key", publicTaskID)
	response, err := c.client().Do(httpRequest)
	if err != nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("Sora submit transport failed: %w", relaycommon.SanitizeTransportError(err)), Dispatched: true}
	}
	defer response.Body.Close()
	parsed, raw, parseErr := parseTaskResponse(response)
	if parseErr != nil {
		return nil, raw, &RequestError{Err: parseErr, Dispatched: true}
	}
	return parsed, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, providerTaskID string) (*Response, []byte, error) {
	if strings.TrimSpace(apiKey) == "" || !validProviderTaskID(providerTaskID) {
		return nil, nil, errors.New("invalid Sora fetch parameters")
	}
	endpoint, err := endpointURL(baseURL, "/v1/videos/"+url.PathEscape(providerTaskID))
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Sora fetch transport failed: %w", relaycommon.SanitizeTransportError(err))
	}
	defer response.Body.Close()
	parsed, raw, err := parseTaskResponse(response)
	if err != nil {
		return nil, raw, err
	}
	if parsed.ID != providerTaskID {
		return nil, raw, errors.New("Sora fetch response id does not match the requested task")
	}
	return parsed, raw, nil
}

func (c *Client) Content(ctx context.Context, baseURL, apiKey, providerTaskID string) (*http.Response, error) {
	if strings.TrimSpace(apiKey) == "" || !validProviderTaskID(providerTaskID) {
		return nil, errors.New("invalid Sora content parameters")
	}
	endpoint, err := endpointURL(baseURL, "/v1/videos/"+url.PathEscape(providerTaskID)+"/content")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("Sora content transport failed: %w", relaycommon.SanitizeTransportError(err))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if response.ContentLength > MaxContentBodyBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("Sora content response exceeds %d bytes", MaxContentBodyBytes)
	}
	response.Body = &boundedContentReadCloser{
		reader: response.Body, closer: response.Body, remaining: MaxContentBodyBytes,
	}
	return response, nil
}

// boundedContentReadCloser keeps chunked or falsely length-declared provider
// content from turning the authenticated proxy into an unbounded stream.
// It probes one byte past the limit so an exact-limit response remains valid.
type boundedContentReadCloser struct {
	reader    io.Reader
	closer    io.Closer
	remaining int64
}

func (body *boundedContentReadCloser) Read(buffer []byte) (int, error) {
	if body.remaining <= 0 {
		var probe [1]byte
		n, err := body.reader.Read(probe[:])
		if n > 0 {
			return 0, httpx.ErrBodyTooLarge
		}
		if err != nil {
			return 0, err
		}
		return 0, io.ErrNoProgress
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	n, err := body.reader.Read(buffer)
	body.remaining -= int64(n)
	return n, err
}

func (body *boundedContentReadCloser) Close() error { return body.closer.Close() }

func endpointURL(baseURL, path string) (string, error) {
	base, err := EffectiveBaseURL(baseURL, int(channelcatalog.ChannelTypeSora))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + path, nil
}

func parseTaskResponse(response *http.Response) (*Response, []byte, error) {
	if response == nil {
		return nil, nil, errors.New("Sora response is nil")
	}
	limit := MaxResponseBodyBytes
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, nil, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, raw, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	var parsed Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, raw, errors.New("invalid Sora task response")
	}
	providerID := strings.TrimSpace(parsed.ID)
	legacyProviderID := strings.TrimSpace(parsed.TaskID)
	if providerID != "" && legacyProviderID != "" && providerID != legacyProviderID {
		return nil, raw, errors.New("Sora task response contains conflicting ids")
	}
	if providerID == "" {
		providerID = legacyProviderID
	}
	if !validProviderTaskID(providerID) {
		return nil, raw, errors.New("Sora task response is missing a valid id")
	}
	parsed.ID = providerID
	return &parsed, raw, nil
}

func validPublicTaskID(value string) bool {
	if !strings.HasPrefix(value, "task_") || len(value) != len("task_")+32 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "task_") {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validProviderTaskID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxProviderTaskIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}
