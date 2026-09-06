// Package suno implements the bounded Suno task protocol. It deliberately
// contains no database or quota logic; callers own the durable lifecycle.
package suno

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	MaxRequestBodyBytes       int64 = 1 << 20
	MaxResponseBodyBytes      int64 = 2 << 20
	MaxProviderDataBytes            = 1 << 20
	MaxBatchTasks                   = 100
	MaxPromptRunes                  = 10_000
	MaxDescriptionPromptRunes       = 10_000
	MaxTitleRunes                   = 500
	MaxTagsRunes                    = 1_000
	MaxModelVersionBytes            = 128
	MaxTaskIDBytes                  = 191
	MaxBaseURLBytes                 = 4096
	MaxAPIKeyBytes                  = 4096
	MaxProviderMessageRunes         = 512
	MaxProviderCodeBytes            = 64
	MaxContinueAtSeconds            = 3600
	MaxProviderTimestamp      int64 = 32_503_680_000

	DefaultMusicModelVersion = "chirp-v3-0"
)

var ModelList = []string{"suno_music", "suno_lyrics"}

type Action string

const (
	ActionMusic  Action = "MUSIC"
	ActionLyrics Action = "LYRICS"
)

func ModelForAction(action Action) (string, bool) {
	switch action {
	case ActionMusic:
		return "suno_music", true
	case ActionLyrics:
		return "suno_lyrics", true
	default:
		return "", false
	}
}

func ActionForModel(model string) (Action, bool) {
	switch model {
	case "suno_music":
		return ActionMusic, true
	case "suno_lyrics":
		return ActionLyrics, true
	default:
		return "", false
	}
}

func ParseAction(raw string) (Action, error) {
	action := Action(strings.ToUpper(strings.TrimSpace(raw)))
	if _, ok := ModelForAction(action); !ok {
		return "", errors.New("invalid Suno action")
	}
	return action, nil
}

// Request is the exact reference-compatible JSON payload sent upstream.
type Request struct {
	GptDescriptionPrompt string  `json:"gpt_description_prompt,omitempty"`
	Prompt               string  `json:"prompt,omitempty"`
	Mv                   string  `json:"mv,omitempty"`
	Title                string  `json:"title,omitempty"`
	Tags                 string  `json:"tags,omitempty"`
	ContinueAt           float64 `json:"continue_at,omitempty"`
	TaskID               string  `json:"task_id,omitempty"`
	ContinueClipID       string  `json:"continue_clip_id,omitempty"`
	MakeInstrumental     bool    `json:"make_instrumental"`
}

type PreparedRequest struct {
	Action Action
	Model  string
	Body   []byte
	Value  Request
}

// PrepareSubmit strictly decodes, validates, and defaults a Suno submission.
// The route action is authoritative and is mapped to one fixed public model.
func PrepareSubmit(raw []byte, actionText string) (*PreparedRequest, error) {
	if len(raw) == 0 {
		return nil, errors.New("Suno request body is required")
	}
	if int64(len(raw)) > MaxRequestBodyBytes {
		return nil, common.ErrBodyTooLarge
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var request Request
	if err := strictDecode(raw, &request); err != nil {
		return nil, errors.New("invalid Suno JSON request")
	}
	action, err := ParseAction(actionText)
	if err != nil {
		return nil, err
	}
	if action == ActionMusic && request.Mv == "" {
		request.Mv = DefaultMusicModelVersion
	}
	if err := validateRequest(&request, action); err != nil {
		return nil, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, errors.New("encode Suno request")
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return nil, common.ErrBodyTooLarge
	}
	model, _ := ModelForAction(action)
	return &PreparedRequest{Action: action, Model: model, Body: body, Value: request}, nil
}

// WithProviderTaskID returns an isolated copy whose client-facing task_id has
// been replaced with the authenticated provider identifier resolved by the
// durable relay. Callers must never pass an unverified client value here.
func WithProviderTaskID(prepared *PreparedRequest, providerTaskID string) (*PreparedRequest, error) {
	if prepared == nil || prepared.Model == "" {
		return nil, errors.New("invalid prepared Suno request")
	}
	if err := validateIdentifier("provider task_id", providerTaskID, MaxTaskIDBytes, true); err != nil {
		return nil, err
	}
	clone := *prepared
	clone.Value = prepared.Value
	clone.Value.TaskID = providerTaskID
	if err := validateRequest(&clone.Value, clone.Action); err != nil {
		return nil, err
	}
	body, err := json.Marshal(clone.Value)
	if err != nil || int64(len(body)) > MaxRequestBodyBytes {
		return nil, errors.New("encode Suno provider request")
	}
	clone.Body = body
	return &clone, nil
}

func validateRequest(request *Request, action Action) error {
	if request == nil {
		return errors.New("Suno request is nil")
	}
	if err := validateText("prompt", request.Prompt, MaxPromptRunes, action == ActionLyrics); err != nil {
		return err
	}
	if err := validateText("gpt_description_prompt", request.GptDescriptionPrompt, MaxDescriptionPromptRunes, false); err != nil {
		return err
	}
	if err := validateText("title", request.Title, MaxTitleRunes, false); err != nil {
		return err
	}
	if err := validateText("tags", request.Tags, MaxTagsRunes, false); err != nil {
		return err
	}
	if err := validateIdentifier("mv", request.Mv, MaxModelVersionBytes, action == ActionMusic); err != nil {
		return err
	}
	if err := validateIdentifier("task_id", request.TaskID, MaxTaskIDBytes, false); err != nil {
		return err
	}
	if err := validateIdentifier("continue_clip_id", request.ContinueClipID, MaxTaskIDBytes, false); err != nil {
		return err
	}
	if request.ContinueClipID != "" && request.TaskID == "" {
		return errors.New("task_id is required when continue_clip_id is provided")
	}
	if request.TaskID != "" && action != ActionMusic {
		return errors.New("task_id is only supported for MUSIC requests")
	}
	if math.IsNaN(request.ContinueAt) || math.IsInf(request.ContinueAt, 0) || request.ContinueAt < 0 || request.ContinueAt > MaxContinueAtSeconds {
		return fmt.Errorf("continue_at must be finite and between 0 and %d", MaxContinueAtSeconds)
	}
	return nil
}

func validateText(name, value string, maxRunes int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s is invalid or exceeds %d characters", name, maxRunes)
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return fmt.Errorf("%s contains unsupported control characters", name)
		}
	}
	return nil
}

func validateIdentifier(name, value string, maxBytes int, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid or too large", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) || character == '/' || character == '\\' || character == '?' || character == '#' {
			return fmt.Errorf("%s contains unsupported characters", name)
		}
	}
	return nil
}

type Status string

const (
	StatusSubmitted  Status = "submitted"
	StatusQueueing   Status = "queueing"
	StatusProcessing Status = "processing"
	StatusSuccess    Status = "success"
	StatusFailed     Status = "failed"
)

type TaskResult struct {
	ProviderTaskID string
	Action         string
	Status         Status
	FailReason     string
	SubmitTime     int64
	StartTime      int64
	FinishTime     int64
	Data           json.RawMessage
}

type responseEnvelope[T any] struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type providerTask struct {
	TaskID     string          `json:"task_id"`
	Action     string          `json:"action"`
	Status     string          `json:"status"`
	FailReason string          `json:"fail_reason"`
	SubmitTime int64           `json:"submit_time"`
	StartTime  int64           `json:"start_time"`
	FinishTime int64           `json:"finish_time"`
	Data       json.RawMessage `json:"data"`
}

// ProviderError never includes an unparsed response body, credentials, URL,
// or provider identifier in Error().
type ProviderError struct {
	StatusCode int
	Code       string
	Message    string
	Definitive bool
	Cause      error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "Suno provider request failed"
	}
	if e.Cause != nil {
		return fmt.Sprintf("Suno provider returned status %d (%T)", e.StatusCode, e.Cause)
	}
	return fmt.Sprintf("Suno provider returned status %d", e.StatusCode)
}

func (e *ProviderError) Unwrap() error { return e.Cause }

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Suno request failed"
	}
	return e.Err.Error()
}

func (e *RequestError) Unwrap() error { return e.Err }

func SubmitWasDispatched(err error) bool {
	var requestError *RequestError
	return errors.As(err, &requestError) && requestError.Dispatched
}

func IsDefinitiveRejection(err error) bool {
	var providerError *ProviderError
	return errors.As(err, &providerError) && providerError.Definitive
}

type Client struct {
	HTTPClient *http.Client
}

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil, DialContext: common.SafeDialContext, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout: 90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       60 * time.Second,
	}
}

func (client *Client) httpClient() *http.Client {
	if client != nil && client.HTTPClient != nil {
		bounded := *client.HTTPClient
		bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if bounded.Timeout <= 0 || bounded.Timeout > 60*time.Second {
			bounded.Timeout = 60 * time.Second
		}
		return &bounded
	}
	return NewHTTPClient()
}

// ValidateBaseURL accepts one fixed configured HTTPS origin/path and rejects
// credentials, query strings, fragments, control bytes, and path traversal.
func ValidateBaseURL(raw string) (string, error) {
	if hasControl(raw) {
		return "", errors.New("Suno base URL is missing or invalid")
	}
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || len(raw) > MaxBaseURLBytes || !utf8.ValidString(raw) {
		return "", errors.New("Suno base URL is missing or invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Suno base URL must be a valid HTTPS URL")
	}
	for _, segment := range strings.Split(parsed.EscapedPath(), "/") {
		decoded, decodeErr := url.PathUnescape(segment)
		if decodeErr != nil || decoded == "." || decoded == ".." || strings.Contains(decoded, "\\") {
			return "", errors.New("Suno base URL contains an unsafe path")
		}
	}
	return raw, nil
}

func validateAPIKey(key string) error {
	if key == "" || len(key) > MaxAPIKeyBytes || !utf8.ValidString(key) || strings.TrimSpace(key) != key || hasControl(key) {
		return errors.New("invalid Suno API key")
	}
	return nil
}

func (client *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (string, error) {
	if prepared == nil || len(prepared.Body) == 0 || int64(len(prepared.Body)) > MaxRequestBodyBytes {
		return "", &RequestError{Err: errors.New("invalid Suno submit parameters")}
	}
	expectedModel, ok := ModelForAction(prepared.Action)
	if !ok || prepared.Model != expectedModel {
		return "", &RequestError{Err: errors.New("invalid Suno submit action")}
	}
	if err := validateRequest(&prepared.Value, prepared.Action); err != nil {
		return "", &RequestError{Err: errors.New("invalid prepared Suno request")}
	}
	canonicalBody, err := json.Marshal(prepared.Value)
	if err != nil || !bytes.Equal(canonicalBody, prepared.Body) {
		return "", &RequestError{Err: errors.New("invalid prepared Suno request")}
	}
	base, err := ValidateBaseURL(baseURL)
	if err != nil {
		return "", &RequestError{Err: err}
	}
	if err := validateAPIKey(apiKey); err != nil {
		return "", &RequestError{Err: err}
	}
	endpoint := base + "/suno/submit/" + string(prepared.Action)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(prepared.Body))
	if err != nil {
		return "", &RequestError{Err: err}
	}
	setHeaders(request, apiKey)
	response, err := client.httpClient().Do(request)
	if err != nil {
		return "", &RequestError{
			Err:        fmt.Errorf("Suno submit transport failed: %w", relaycommon.SanitizeTransportError(err)),
			Dispatched: true,
		}
	}
	defer response.Body.Close()
	providerID, parseErr := parseSubmitResponse(response, apiKey)
	if parseErr != nil {
		return "", &RequestError{Err: parseErr, Dispatched: true}
	}
	return providerID, nil
}

func (client *Client) Fetch(ctx context.Context, baseURL, apiKey string, ids []string) ([]TaskResult, error) {
	if err := validateProviderIDs(ids); err != nil {
		return nil, err
	}
	base, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if err := validateAPIKey(apiKey); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: ids})
	if err != nil {
		return nil, errors.New("encode Suno fetch request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/suno/fetch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setHeaders(request, apiKey)
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("Suno fetch transport failed: %w", relaycommon.SanitizeTransportError(err))
	}
	defer response.Body.Close()
	return parseFetchResponse(response, ids, apiKey)
}

func setHeaders(request *http.Request, apiKey string) {
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
}

func parseSubmitResponse(response *http.Response, secrets ...string) (string, error) {
	raw, err := readProviderBody(response)
	if err != nil {
		return "", err
	}
	var envelope responseEnvelope[string]
	if err := strictProviderDecode(raw, &envelope); err != nil {
		return "", providerDecodeFailure(response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != "success" {
		return "", providerFailure(response.StatusCode, envelope.Code, envelope.Message,
			response.StatusCode >= 200 && response.StatusCode < 300 || definitiveHTTPStatus(response.StatusCode), secrets...)
	}
	if err := validateIdentifier("provider task_id", envelope.Data, MaxTaskIDBytes, true); err != nil {
		return "", errors.New("Suno submit response is missing a valid data task id")
	}
	return envelope.Data, nil
}

func parseFetchResponse(response *http.Response, expected []string, apiKey string) ([]TaskResult, error) {
	raw, err := readProviderBody(response)
	if err != nil {
		return nil, err
	}
	var envelope responseEnvelope[[]providerTask]
	if err := strictProviderDecode(raw, &envelope); err != nil {
		return nil, providerDecodeFailure(response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != "success" {
		secrets := append([]string{apiKey}, expected...)
		return nil, providerFailure(response.StatusCode, envelope.Code, envelope.Message,
			response.StatusCode >= 200 && response.StatusCode < 300 || definitiveHTTPStatus(response.StatusCode), secrets...)
	}
	if len(envelope.Data) > MaxBatchTasks {
		return nil, errors.New("Suno fetch response contains too many tasks")
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(envelope.Data))
	results := make([]TaskResult, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		if err := validateIdentifier("provider task_id", item.TaskID, MaxTaskIDBytes, true); err != nil {
			return nil, errors.New("Suno fetch response contains an invalid task id")
		}
		if _, ok := wanted[item.TaskID]; !ok {
			return nil, errors.New("Suno fetch response contains an unrequested task")
		}
		if _, duplicate := seen[item.TaskID]; duplicate {
			return nil, errors.New("Suno fetch response contains a duplicate task")
		}
		seen[item.TaskID] = struct{}{}
		status, ok := normalizeStatus(item.Status)
		if !ok {
			return nil, errors.New("Suno fetch response contains an unknown task status")
		}
		if len(item.Action) > 64 || !utf8.ValidString(item.Action) || hasControl(item.Action) {
			return nil, errors.New("Suno fetch response contains an invalid action")
		}
		if item.Action != "" {
			action, actionErr := ParseAction(item.Action)
			if actionErr != nil {
				return nil, errors.New("Suno fetch response contains an invalid action")
			}
			item.Action = string(action)
		}
		if item.SubmitTime < 0 || item.StartTime < 0 || item.FinishTime < 0 ||
			item.SubmitTime > MaxProviderTimestamp || item.StartTime > MaxProviderTimestamp ||
			item.FinishTime > MaxProviderTimestamp {
			return nil, errors.New("Suno fetch response contains an invalid timestamp")
		}
		if item.SubmitTime > 0 && item.StartTime > 0 && item.StartTime < item.SubmitTime ||
			item.SubmitTime > 0 && item.FinishTime > 0 && item.FinishTime < item.SubmitTime ||
			item.StartTime > 0 && item.FinishTime > 0 && item.FinishTime < item.StartTime {
			return nil, errors.New("Suno fetch response contains inconsistent timestamps")
		}
		failReason := sanitizeProviderText(item.FailReason)
		if len(item.Data) == 0 || string(item.Data) == "null" {
			item.Data = json.RawMessage("null")
		} else if len(item.Data) > MaxProviderDataBytes || rejectDuplicateJSONKeys(item.Data) != nil {
			return nil, errors.New("Suno fetch response contains invalid or oversized data")
		}
		results = append(results, TaskResult{
			ProviderTaskID: item.TaskID, Action: item.Action, Status: status,
			FailReason: failReason, SubmitTime: item.SubmitTime, StartTime: item.StartTime,
			FinishTime: item.FinishTime, Data: append(json.RawMessage(nil), item.Data...),
		})
	}
	return results, nil
}

func readProviderBody(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("Suno provider response is nil")
	}
	limit := MaxResponseBodyBytes
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &ProviderError{
			StatusCode: response.StatusCode,
			Code:       "response_too_large",
			Message:    "Suno provider response exceeded the configured limit",
			Definitive: definitiveHTTPStatus(response.StatusCode),
			Cause:      err,
		}
	}
	return raw, nil
}

func providerDecodeFailure(statusCode int, cause error) error {
	return &ProviderError{
		StatusCode: statusCode,
		Code:       "invalid_provider_response",
		Message:    "Suno provider returned an invalid response",
		Definitive: definitiveHTTPStatus(statusCode),
		Cause:      cause,
	}
}

func providerFailure(statusCode int, code, message string, definitive bool, secrets ...string) error {
	if statusCode == 0 {
		statusCode = http.StatusBadGateway
	}
	return &ProviderError{
		StatusCode: statusCode, Code: sanitizeProviderCode(redactProviderSecrets(code, secrets)),
		Message: sanitizeProviderText(redactProviderSecrets(message, secrets)), Definitive: definitive,
	}
}

func redactProviderSecrets(value string, secrets []string) string {
	for _, secret := range secrets {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func definitiveHTTPStatus(statusCode int) bool {
	return statusCode >= 400 && statusCode < 500 &&
		statusCode != http.StatusRequestTimeout && statusCode != http.StatusConflict
}

func normalizeStatus(raw string) (Status, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(StatusSubmitted):
		return StatusSubmitted, true
	case string(StatusQueueing):
		return StatusQueueing, true
	case string(StatusProcessing):
		return StatusProcessing, true
	case string(StatusSuccess):
		return StatusSuccess, true
	case string(StatusFailed):
		return StatusFailed, true
	default:
		return "", false
	}
}

func validateProviderIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > MaxBatchTasks {
		return fmt.Errorf("Suno fetch requires between 1 and %d task ids", MaxBatchTasks)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if err := validateIdentifier("provider task id", id, MaxTaskIDBytes, true); err != nil {
			return err
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("Suno fetch task ids must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func strictDecode(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func strictProviderDecode(raw []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	return strictDecode(raw, destination)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Suno JSON contains trailing data")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid Suno JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return errors.New("invalid Suno JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid Suno JSON object")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("Suno JSON field %q must not be repeated", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return errors.New("invalid Suno JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return errors.New("invalid Suno JSON array")
		}
	default:
		return errors.New("invalid Suno JSON")
	}
	return nil
}

func sanitizeProviderCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxProviderCodeBytes {
		return "provider_error"
	}
	for _, character := range raw {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "provider_error"
		}
	}
	return raw
}

func sanitizeProviderText(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	count := 0
	lastSpace := false
	for _, character := range raw {
		if count >= MaxProviderMessageRunes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 && !lastSpace {
				builder.WriteByte(' ')
				lastSpace = true
				count++
			}
			continue
		}
		builder.WriteRune(character)
		lastSpace = unicode.IsSpace(character)
		count++
	}
	return strings.TrimSpace(builder.String())
}

func hasControl(raw string) bool {
	for _, character := range raw {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
