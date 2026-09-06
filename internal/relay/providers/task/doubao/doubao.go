// Package doubao implements the bounded VolcEngine Ark asynchronous video
// protocol. It owns request conversion, provider authentication, transport,
// and response parsing only; relay owns persistence and accounting.
package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ChannelName   = "doubao-video"
	DirectBaseURL = "https://ark.cn-beijing.volces.com"

	MaxRequestBodyBytes     int64 = 16 << 20
	MaxResponseBodyBytes    int64 = 1 << 20
	MaxMetadataBytes              = 1 << 20
	MaxPromptRunes                = 10_000
	MaxModelBytes                 = 256
	MaxContentItems               = 64
	MaxTools                      = 16
	MaxURLBytes                   = 8 << 10
	MaxCallbackURLBytes           = 2 << 10
	MaxProviderTaskIDBytes        = 191
	MaxProviderMessageRunes       = 512
	MaxProviderCodeBytes          = 128
	MaxStringFieldBytes           = 256
	MaxDuration                   = 86_400
	MaxFrames                     = 1_000_000
)

var supportedModels = [...]string{
	"doubao-seedance-1-0-pro-250528",
	"doubao-seedance-1-0-lite-t2v",
	"doubao-seedance-1-0-lite-i2v",
	"doubao-seedance-1-5-pro-251215",
	"doubao-seedance-2-0-260128",
	"doubao-seedance-2-0-fast-260128",
}

// ModelList returns an owned copy of the exact reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// IsModel reports whether model belongs to the exact reference catalog.
func IsModel(model string) bool {
	model = strings.TrimSpace(model)
	for _, candidate := range supportedModels {
		if model == candidate {
			return true
		}
	}
	return false
}

type Action string

const ActionGenerate Action = "generate"

type MediaURL struct {
	URL string `json:"url,omitempty"`
}

type ContentItem struct {
	Type     string    `json:"type,omitempty"`
	Text     string    `json:"text,omitempty"`
	ImageURL *MediaURL `json:"image_url,omitempty"`
	VideoURL *MediaURL `json:"video_url,omitempty"`
	AudioURL *MediaURL `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}

type Tool struct {
	Type string `json:"type,omitempty"`
}

// Payload is the exact request object accepted by Ark's content-generation
// task endpoint.
type Payload struct {
	Model                 string        `json:"model"`
	Content               []ContentItem `json:"content,omitempty"`
	CallbackURL           string        `json:"callback_url,omitempty"`
	ReturnLastFrame       *bool         `json:"return_last_frame,omitempty"`
	ServiceTier           string        `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *int          `json:"execution_expires_after,omitempty"`
	GenerateAudio         *bool         `json:"generate_audio,omitempty"`
	Draft                 *bool         `json:"draft,omitempty"`
	Tools                 []Tool        `json:"tools,omitempty"`
	SafetyIdentifier      string        `json:"safety_identifier,omitempty"`
	Priority              *int          `json:"priority,omitempty"`
	Resolution            string        `json:"resolution,omitempty"`
	Ratio                 string        `json:"ratio,omitempty"`
	Duration              *int          `json:"duration,omitempty"`
	Frames                *int          `json:"frames,omitempty"`
	Seed                  *int          `json:"seed,omitempty"`
	CameraFixed           *bool         `json:"camera_fixed,omitempty"`
	Watermark             *bool         `json:"watermark,omitempty"`
}

type PreparedRequest struct {
	Body          []byte
	Payload       Payload
	Action        Action
	OriginModel   string
	UpstreamModel string
	HasVideoInput bool
	PriceRatio    float64
}

type rawRequest struct {
	Model    string          `json:"model"`
	Prompt   string          `json:"prompt"`
	Images   []string        `json:"images,omitempty"`
	Seconds  json.RawMessage `json:"seconds,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// RequestedModel extracts and validates the client-visible task model before
// provider selection.
func RequestedModel(raw []byte) (string, error) {
	request, err := decodeRawRequest(raw)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(request.Model)
	if !IsModel(model) {
		return "", fmt.Errorf("unsupported Doubao video model %q", model)
	}
	return model, nil
}

// PrepareSubmit applies the reference conversion ordering: top-level images,
// metadata overlay, positive seconds override, removal of metadata text items,
// prompt appended last, and mapped model overwritten last.
func PrepareSubmit(raw []byte, originModel, mappedModel string) (*PreparedRequest, error) {
	request, err := decodeRawRequest(raw)
	if err != nil {
		return nil, err
	}
	originModel = strings.TrimSpace(originModel)
	if !IsModel(originModel) || strings.TrimSpace(request.Model) != originModel {
		return nil, errors.New("request model does not match the selected Doubao video model")
	}
	if !validText(mappedModel, MaxModelBytes, false) {
		return nil, errors.New("mapped Doubao video model is invalid")
	}
	if strings.TrimSpace(request.Prompt) == "" || !utf8.ValidString(request.Prompt) ||
		utf8.RuneCountInString(request.Prompt) > MaxPromptRunes || hasForbiddenTextControl(request.Prompt) {
		return nil, errors.New("Doubao video prompt is required and must be valid UTF-8")
	}
	payload := Payload{Model: originModel}
	for _, image := range request.Images {
		payload.Content = append(payload.Content, ContentItem{Type: "image_url", ImageURL: &MediaURL{URL: image}})
	}
	if err := overlayMetadata(request.Metadata, &payload); err != nil {
		return nil, err
	}
	seconds, present, err := optionalPositiveInteger(request.Seconds)
	if err != nil {
		return nil, errors.New("seconds must be a positive integer")
	}
	if present {
		payload.Duration = &seconds
	}
	content := make([]ContentItem, 0, len(payload.Content)+1)
	for _, item := range payload.Content {
		if item.Type != "text" {
			content = append(content, item)
		}
	}
	content = append(content, ContentItem{Type: "text", Text: request.Prompt})
	payload.Content = content
	payload.Model = mappedModel
	if err := validatePayload(payload); err != nil {
		return nil, err
	}
	hasVideo := false
	for _, item := range payload.Content {
		if item.Type == "video_url" || item.VideoURL != nil {
			hasVideo = true
			break
		}
	}
	priceRatio, _ := VideoInputRatio(originModel, payload.Resolution, hasVideo)
	if priceRatio == 0 {
		priceRatio = 1
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode Doubao video request")
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	return &PreparedRequest{
		Body: body, Payload: payload, Action: ActionGenerate,
		OriginModel: originModel, UpstreamModel: mappedModel,
		HasVideoInput: hasVideo, PriceRatio: priceRatio,
	}, nil
}

func decodeRawRequest(raw []byte) (*rawRequest, error) {
	if len(raw) == 0 {
		return nil, errors.New("Doubao video request body is required")
	}
	if int64(len(raw)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request rawRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, errors.New("invalid Doubao video JSON request")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return &request, nil
}

func overlayMetadata(raw json.RawMessage, payload *Payload) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	if len(raw) > MaxMetadataBytes {
		return errors.New("Doubao video metadata is too large")
	}
	value := bytes.TrimSpace(raw)
	if len(value) > 0 && value[0] == '"' {
		var encoded string
		if json.Unmarshal(value, &encoded) != nil || len(encoded) > MaxMetadataBytes {
			return errors.New("invalid Doubao video metadata")
		}
		value = []byte(encoded)
	}
	if err := rejectDuplicateJSONKeys(value); err != nil {
		return errors.New("invalid Doubao video metadata")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return errors.New("invalid Doubao video metadata")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return errors.New("invalid Doubao video metadata")
	}
	return nil
}

func optionalPositiveInteger(raw json.RawMessage) (int, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false, nil
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0, false, errors.New("invalid integer")
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
		if err != nil {
			return 0, false, err
		}
		value = int(parsed)
	}
	if value <= 0 || value > MaxDuration {
		return 0, false, errors.New("integer outside supported bounds")
	}
	return value, true, nil
}

func validatePayload(payload Payload) error {
	if !validText(payload.Model, MaxModelBytes, false) || len(payload.Content) == 0 || len(payload.Content) > MaxContentItems {
		return errors.New("Doubao video payload is invalid")
	}
	textItems := 0
	for _, item := range payload.Content {
		if !validContent(item) {
			return errors.New("Doubao video content is invalid")
		}
		if item.Type == "text" {
			textItems++
		}
	}
	if textItems != 1 || payload.Content[len(payload.Content)-1].Type != "text" {
		return errors.New("Doubao video prompt content is invalid")
	}
	if payload.CallbackURL != "" && !validHTTPSURL(payload.CallbackURL, MaxCallbackURLBytes) {
		return errors.New("Doubao video callback_url is invalid")
	}
	for _, value := range []string{payload.ServiceTier, payload.SafetyIdentifier, payload.Resolution, payload.Ratio} {
		if !validText(value, MaxStringFieldBytes, true) {
			return errors.New("Doubao video payload contains an invalid string field")
		}
	}
	if len(payload.Tools) > MaxTools {
		return errors.New("Doubao video payload has too many tools")
	}
	for _, tool := range payload.Tools {
		if !validText(tool.Type, MaxStringFieldBytes, false) {
			return errors.New("Doubao video tool type is invalid")
		}
	}
	if payload.ExecutionExpiresAfter != nil && (*payload.ExecutionExpiresAfter <= 0 || *payload.ExecutionExpiresAfter > MaxDuration) {
		return errors.New("Doubao video execution_expires_after is invalid")
	}
	if payload.Duration != nil && (*payload.Duration <= 0 || *payload.Duration > MaxDuration) {
		return errors.New("Doubao video duration is invalid")
	}
	if payload.Frames != nil && (*payload.Frames <= 0 || *payload.Frames > MaxFrames) {
		return errors.New("Doubao video frames is invalid")
	}
	return nil
}

func validContent(item ContentItem) bool {
	if !validText(item.Type, MaxStringFieldBytes, false) || !validText(item.Role, MaxStringFieldBytes, true) {
		return false
	}
	switch item.Type {
	case "text":
		return item.ImageURL == nil && item.VideoURL == nil && item.AudioURL == nil &&
			strings.TrimSpace(item.Text) != "" && utf8.ValidString(item.Text) &&
			utf8.RuneCountInString(item.Text) <= MaxPromptRunes && !hasForbiddenTextControl(item.Text)
	case "image_url":
		return item.Text == "" && validMedia(item.ImageURL) && item.VideoURL == nil && item.AudioURL == nil
	case "video_url":
		return item.Text == "" && validMedia(item.VideoURL) && item.ImageURL == nil && item.AudioURL == nil
	case "audio_url":
		return item.Text == "" && validMedia(item.AudioURL) && item.ImageURL == nil && item.VideoURL == nil
	default:
		return false
	}
}

func validMedia(media *MediaURL) bool {
	return media != nil && validHTTPSURL(media.URL, MaxURLBytes)
}

// VideoInputRatio reproduces the reference's resolution/video-input pricing
// multiplier. The boolean reports whether the model has a price table.
func VideoInputRatio(model, resolution string, hasVideo bool) (float64, bool) {
	type key struct {
		resolution string
		video      bool
	}
	prices := map[string]map[key]float64{
		"doubao-seedance-2-0-260128": {
			{"", false}: 46, {"", true}: 28,
			{"1080p", false}: 51, {"1080p", true}: 31,
			{"4k", false}: 26, {"4k", true}: 16,
		},
		"doubao-seedance-2-0-fast-260128": {
			{"", false}: 37, {"", true}: 22,
		},
	}
	table, ok := prices[model]
	if !ok {
		return 0, false
	}
	base := table[key{"", false}]
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if resolution != "1080p" && resolution != "4k" {
		resolution = ""
	}
	selected, found := table[key{resolution, hasVideo}]
	if !found || base <= 0 {
		return 1, true
	}
	return selected / base, true
}

type TaskStatus string

const (
	StatusSubmitted  TaskStatus = "submitted"
	StatusProcessing TaskStatus = "processing"
	StatusSucceeded  TaskStatus = "succeeded"
	StatusFailed     TaskStatus = "failed"
)

type Task struct {
	ProviderTaskID  string
	Status          TaskStatus
	StatusMessage   string
	ErrorCode       string
	ResultURL       string
	CompletionUnits int
	CreatedAt       int64
	UpdatedAt       int64
}

type submitResponse struct {
	ID string `json:"id"`
}

type fetchResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		ToolUsage        struct {
			WebSearch int `json:"web_search"`
		} `json:"tool_usage"`
	} `json:"usage"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

type providerErrorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type ProviderError struct {
	StatusCode int
	Code       string
	Message    string
	Cause      error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "Doubao video provider request failed"
	}
	if e.Cause != nil {
		return fmt.Sprintf("Doubao video provider returned status %d (%T)", e.StatusCode, e.Cause)
	}
	return fmt.Sprintf("Doubao video provider returned status %d", e.StatusCode)
}
func (e *ProviderError) Unwrap() error { return e.Cause }

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Doubao video request failed"
	}
	return e.Err.Error()
}
func (e *RequestError) Unwrap() error { return e.Err }

func SubmitWasDispatched(err error) bool {
	var requestError *RequestError
	return errors.As(err, &requestError) && requestError.Dispatched
}

type Client struct{ HTTPClient *http.Client }

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil, DialContext: httpx.SafeDialContext, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout: 90 * time.Second,
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

func EffectiveBaseURL(configured string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(configured), "/")
	if base == "" {
		base = DirectBaseURL
	}
	if len(base) > 4096 || hasControl(base) {
		return "", errors.New("Doubao video base URL is invalid or too large")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Doubao video base URL is invalid")
	}
	if parsed.Scheme != "https" && !(httpx.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("Doubao video base URL must use HTTPS")
	}
	return base, nil
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Task, []byte, error) {
	if prepared == nil || prepared.Action != ActionGenerate || len(prepared.Body) == 0 ||
		int64(len(prepared.Body)) > MaxRequestBodyBytes || !validCredential(apiKey) {
		return nil, nil, &RequestError{Err: errors.New("invalid Doubao video submit parameters")}
	}
	endpoint, err := endpointURL(baseURL, "")
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(prepared.Body))
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	setHeaders(request, apiKey)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("Doubao video submit transport failed: %w", err), Dispatched: true}
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return nil, raw, &RequestError{Err: err, Dispatched: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, raw, &RequestError{Err: providerFailure(response.StatusCode, raw), Dispatched: true}
	}
	if rejectDuplicateJSONKeys(raw) != nil {
		return nil, raw, &RequestError{Err: errors.New("invalid Doubao video submit response"), Dispatched: true}
	}
	var parsed submitResponse
	if json.Unmarshal(raw, &parsed) != nil || !validProviderTaskID(parsed.ID) {
		return nil, raw, &RequestError{Err: errors.New("invalid Doubao video submit response"), Dispatched: true}
	}
	return &Task{ProviderTaskID: parsed.ID, Status: StatusSubmitted}, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, providerTaskID string) (*Task, []byte, error) {
	if !validCredential(apiKey) || !validProviderTaskID(providerTaskID) {
		return nil, nil, errors.New("invalid Doubao video fetch parameters")
	}
	endpoint, err := endpointURL(baseURL, providerTaskID)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	setHeaders(request, apiKey)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Doubao video fetch transport failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return nil, raw, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, raw, providerFailure(response.StatusCode, raw)
	}
	if rejectDuplicateJSONKeys(raw) != nil {
		return nil, raw, errors.New("invalid Doubao video fetch response")
	}
	var parsed fetchResponse
	if json.Unmarshal(raw, &parsed) != nil || !validProviderTaskID(parsed.ID) || parsed.ID != providerTaskID {
		return nil, raw, errors.New("invalid Doubao video fetch response")
	}
	status, ok := normalizeStatus(parsed.Status)
	if !ok {
		return nil, raw, errors.New("unknown Doubao video task status")
	}
	if parsed.Usage.TotalTokens < 0 || int64(parsed.Usage.TotalTokens) > quotamath.MaxQuota {
		return nil, raw, errors.New("invalid Doubao video total_tokens")
	}
	if parsed.CreatedAt < 0 || parsed.UpdatedAt < 0 {
		return nil, raw, errors.New("invalid Doubao video timestamps")
	}
	resultURL := strings.TrimSpace(parsed.Content.VideoURL)
	if resultURL != "" && !validHTTPSURL(resultURL, MaxURLBytes) {
		return nil, raw, errors.New("invalid Doubao video result URL")
	}
	if status == StatusSucceeded && resultURL == "" {
		return nil, raw, errors.New("successful Doubao video task has no result URL")
	}
	return &Task{
		ProviderTaskID: parsed.ID, Status: status,
		StatusMessage: sanitizeProviderText(parsed.Error.Message), ErrorCode: sanitizeProviderCode(parsed.Error.Code),
		ResultURL: resultURL, CompletionUnits: parsed.Usage.TotalTokens,
		CreatedAt: parsed.CreatedAt, UpdatedAt: parsed.UpdatedAt,
	}, raw, nil
}

func endpointURL(baseURL, providerTaskID string) (string, error) {
	base, err := EffectiveBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	path := "/api/v3/contents/generations/tasks"
	if providerTaskID != "" {
		if !validProviderTaskID(providerTaskID) {
			return "", errors.New("invalid Doubao video task id")
		}
		path += "/" + url.PathEscape(providerTaskID)
	}
	return strings.TrimRight(base, "/") + path, nil
}

func setHeaders(request *http.Request, apiKey string) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
}

func readResponse(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("Doubao video response is nil")
	}
	limit := MaxResponseBodyBytes
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &ProviderError{StatusCode: response.StatusCode, Code: "response_too_large", Message: "Doubao video response exceeded the configured limit", Cause: err}
	}
	return raw, nil
}

func providerFailure(statusCode int, raw []byte) error {
	envelope := providerErrorEnvelope{}
	_ = json.Unmarshal(raw, &envelope)
	code, message := envelope.Code, envelope.Message
	if code == "" {
		code = envelope.Error.Code
	}
	if message == "" {
		message = envelope.Error.Message
	}
	code = sanitizeProviderCode(code)
	message = sanitizeProviderText(message)
	if message == "" {
		message = "Doubao video provider rejected the request"
	}
	return &ProviderError{StatusCode: statusCode, Code: code, Message: message}
}

func normalizeStatus(status string) (TaskStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "queued":
		return StatusSubmitted, true
	case "processing", "running":
		return StatusProcessing, true
	case "succeeded":
		return StatusSucceeded, true
	case "failed":
		return StatusFailed, true
	default:
		return "", false
	}
}

func validCredential(value string) bool {
	return validText(value, 16<<10, false) && strings.TrimSpace(value) == value
}

// ValidateCredential allows the lifecycle to fail before reserving quota.
func ValidateCredential(value string) error {
	if !validCredential(value) {
		return errors.New("Doubao video credential is invalid")
	}
	return nil
}

func validProviderTaskID(value string) bool {
	if !validText(value, MaxProviderTaskIDBytes, false) {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) || strings.ContainsRune(`/\\?#`, character) {
			return false
		}
	}
	return true
}

func validHTTPSURL(value string, maxBytes int) bool {
	if !validText(value, maxBytes, false) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validText(value string, maxBytes int, emptyOK bool) bool {
	if (!emptyOK && value == "") || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	return !hasControl(value)
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func hasForbiddenTextControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func sanitizeProviderCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxProviderCodeBytes {
		return "provider_error"
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' && character != '-' && character != '.' {
			return "provider_error"
		}
	}
	return value
}

func sanitizeProviderText(value string) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "�")
	var builder strings.Builder
	count := 0
	space := false
	for _, character := range value {
		if count >= MaxProviderMessageRunes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 && !space {
				builder.WriteByte(' ')
				space = true
			}
			continue
		}
		builder.WriteRune(character)
		space = unicode.IsSpace(character)
		count++
	}
	return strings.TrimSpace(builder.String())
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Doubao video request contains trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid Doubao video JSON object")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid Doubao video JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Doubao video request contains trailing JSON")
	}
	return nil
}
