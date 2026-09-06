// Package midjourney implements the bounded Midjourney Proxy wire protocol.
// Database ownership, quota reservations, and task recovery intentionally live
// in the parent relay package.
package midjourney

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	MaxRequestBodyBytes      int64 = 16 << 20
	MaxResponseBodyBytes     int64 = 4 << 20
	MaxProviderMetadataBytes       = 1 << 20
	MaxBatchTasks                  = 100
	MaxBase64Items                 = 10
	MaxBase64ValueBytes            = 8 << 20
	MaxPromptRunes                 = 10_000
	MaxContentRunes                = 2_000
	MaxStateRunes                  = 2_000
	MaxDescriptionRunes            = 2_000
	MaxFailReasonRunes             = 2_000
	MaxCustomIDBytes               = 2_048
	MaxTaskIDBytes                 = 191
	MaxBaseURLBytes                = 4_096
	MaxAPIKeyBytes                 = 4_096
	MaxURLBytes                    = 8_192
	MaxProviderTimestamp     int64 = 32_503_680_000_000
)

type Action string

const (
	ActionImagine       Action = "IMAGINE"
	ActionDescribe      Action = "DESCRIBE"
	ActionBlend         Action = "BLEND"
	ActionUpscale       Action = "UPSCALE"
	ActionVariation     Action = "VARIATION"
	ActionReroll        Action = "REROLL"
	ActionInpaint       Action = "INPAINT"
	ActionModal         Action = "MODAL"
	ActionZoom          Action = "ZOOM"
	ActionCustomZoom    Action = "CUSTOM_ZOOM"
	ActionShorten       Action = "SHORTEN"
	ActionHighVariation Action = "HIGH_VARIATION"
	ActionLowVariation  Action = "LOW_VARIATION"
	ActionPan           Action = "PAN"
	ActionSwapFace      Action = "SWAP_FACE"
	ActionUpload        Action = "UPLOAD"
	ActionVideo         Action = "VIDEO"
	ActionEdits         Action = "EDITS"
)

var Models = []string{
	"mj_imagine", "mj_describe", "mj_blend", "mj_upscale", "mj_variation",
	"mj_reroll", "mj_inpaint", "mj_modal", "mj_zoom", "mj_custom_zoom",
	"mj_shorten", "mj_high_variation", "mj_low_variation", "mj_pan",
	"swap_face", "mj_upload", "mj_video", "mj_edits",
}

func ModelForAction(action Action) (string, bool) {
	if !validAction(action) {
		return "", false
	}
	if action == ActionSwapFace {
		return "swap_face", true
	}
	return "mj_" + strings.ToLower(string(action)), true
}

// Request is the exact JSON field surface accepted by the reference proxy.
// NotifyHook is decoded for compatibility but deliberately never forwarded.
type Request struct {
	Prompt      string   `json:"prompt,omitempty"`
	CustomID    string   `json:"customId,omitempty"`
	BotType     string   `json:"botType,omitempty"`
	NotifyHook  string   `json:"notifyHook,omitempty"`
	Action      Action   `json:"action,omitempty"`
	Index       int      `json:"index,omitempty"`
	State       string   `json:"state,omitempty"`
	TaskID      string   `json:"taskId,omitempty"`
	Base64Array []string `json:"base64Array,omitempty"`
	Content     string   `json:"content,omitempty"`
	MaskBase64  string   `json:"maskBase64,omitempty"`
}

type SwapFaceRequest struct {
	SourceBase64 string `json:"sourceBase64"`
	TargetBase64 string `json:"targetBase64"`
}

type PreparedRequest struct {
	Operation       string
	Action          Action
	Model           string
	Path            string
	Body            []byte
	Value           Request
	Swap            SwapFaceRequest
	OriginalTaskID  string
	SimpleActionTag string
	wire            map[string]json.RawMessage
}

// PrepareSubmit strictly validates one route-specific request and produces
// the canonical /mj provider path. A mode prefix is never forwarded.
func PrepareSubmit(raw []byte, operation string) (*PreparedRequest, error) {
	if len(raw) == 0 {
		return nil, errors.New("Midjourney request body is required")
	}
	if int64(len(raw)) > MaxRequestBodyBytes {
		return nil, common.ErrBodyTooLarge
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	operation = strings.TrimSpace(operation)
	prepared := &PreparedRequest{Operation: operation, Path: canonicalPath(operation)}
	if prepared.Path == "" {
		return nil, errors.New("unknown Midjourney operation")
	}
	if operation == "swap" {
		if err := strictDecode(raw, &prepared.Swap); err != nil {
			return nil, errors.New("invalid Midjourney swap request")
		}
		if err := validateBase64("sourceBase64", prepared.Swap.SourceBase64, true); err != nil {
			return nil, err
		}
		if err := validateBase64("targetBase64", prepared.Swap.TargetBase64, true); err != nil {
			return nil, err
		}
		prepared.Action = ActionSwapFace
		prepared.Model, _ = ModelForAction(prepared.Action)
		prepared.Body, _ = json.Marshal(prepared.Swap)
		return prepared, nil
	}
	if err := strictDecode(raw, &prepared.Value); err != nil {
		return nil, errors.New("invalid Midjourney request")
	}
	if err := json.Unmarshal(raw, &prepared.wire); err != nil || prepared.wire == nil {
		return nil, errors.New("invalid Midjourney request")
	}
	if err := validateCommonRequest(&prepared.Value); err != nil {
		return nil, err
	}
	// Provider callbacks permit an authenticated provider to make an arbitrary
	// outbound request. The reference disables forwarding by default; this
	// adapter always applies that safe default.
	prepared.Value.NotifyHook = ""
	delete(prepared.wire, "notifyHook")

	switch operation {
	case "imagine":
		prepared.Action = ActionImagine
		if strings.TrimSpace(prepared.Value.Prompt) == "" {
			return nil, errors.New("prompt is required")
		}
	case "describe":
		prepared.Action = ActionDescribe
		if len(prepared.Value.Base64Array) != 1 {
			return nil, errors.New("describe requires one image")
		}
	case "blend":
		prepared.Action = ActionBlend
		if len(prepared.Value.Base64Array) < 2 || len(prepared.Value.Base64Array) > 5 {
			return nil, errors.New("blend requires between two and five images")
		}
	case "edits":
		prepared.Action = ActionEdits
		if len(prepared.Value.Base64Array) == 0 {
			return nil, errors.New("edits requires an image")
		}
	case "shorten":
		prepared.Action = ActionShorten
		if strings.TrimSpace(prepared.Value.Prompt) == "" {
			return nil, errors.New("prompt is required")
		}
	case "upload-discord-images":
		prepared.Action = ActionUpload
		if len(prepared.Value.Base64Array) == 0 {
			return nil, errors.New("upload requires at least one image")
		}
	case "change":
		prepared.Action = normalizeAction(prepared.Value.Action)
		if prepared.Value.TaskID == "" || !taskChangeAction(prepared.Action) || prepared.Value.Index < 1 || prepared.Value.Index > 4 {
			return nil, errors.New("change requires a valid taskId, action, and index")
		}
		prepared.OriginalTaskID = prepared.Value.TaskID
	case "simple-change":
		taskID, action, _, tag, err := parseSimpleChange(prepared.Value.Content)
		if err != nil {
			return nil, err
		}
		prepared.Action = action
		prepared.OriginalTaskID, prepared.SimpleActionTag = taskID, tag
	case "modal":
		prepared.Action = ActionModal
		if prepared.Value.TaskID == "" {
			return nil, errors.New("modal requires taskId")
		}
		prepared.OriginalTaskID = prepared.Value.TaskID
	case "video":
		prepared.Action = ActionVideo
		if prepared.Value.TaskID == "" {
			return nil, errors.New("video requires taskId")
		}
		prepared.OriginalTaskID = prepared.Value.TaskID
	case "action":
		action, _, err := ParsePlusAction(prepared.Value.CustomID)
		if err != nil || prepared.Value.TaskID == "" {
			return nil, errors.New("action requires a valid customId and taskId")
		}
		prepared.Action = action
		prepared.OriginalTaskID = prepared.Value.TaskID
	default:
		return nil, errors.New("unknown Midjourney operation")
	}
	prepared.Model, _ = ModelForAction(prepared.Action)
	body, err := json.Marshal(prepared.wire)
	if err != nil || int64(len(body)) > MaxRequestBodyBytes {
		return nil, common.ErrBodyTooLarge
	}
	prepared.Body = body
	return prepared, nil
}

func canonicalPath(operation string) string {
	switch operation {
	case "action", "shorten", "modal", "imagine", "change", "simple-change", "describe", "blend", "edits", "video":
		return "/mj/submit/" + operation
	case "upload-discord-images":
		return "/mj/submit/upload-discord-images"
	case "swap":
		return "/mj/insight-face/swap"
	default:
		return ""
	}
}

// BindProviderTaskID rewrites only the task reference after an owner-scoped
// local lookup. The caller's public task identifier is never sent upstream.
func BindProviderTaskID(prepared *PreparedRequest, providerTaskID string) error {
	if prepared == nil || prepared.OriginalTaskID == "" {
		return errors.New("Midjourney request is not task-bound")
	}
	if err := validateIdentifier("provider task id", providerTaskID, MaxTaskIDBytes, true); err != nil {
		return err
	}
	if prepared.Operation == "simple-change" {
		prepared.Value.Content = providerTaskID + " " + prepared.SimpleActionTag
		prepared.wire["content"], _ = json.Marshal(prepared.Value.Content)
	} else {
		prepared.Value.TaskID = providerTaskID
		prepared.wire["taskId"], _ = json.Marshal(providerTaskID)
	}
	body, err := json.Marshal(prepared.wire)
	if err != nil || int64(len(body)) > MaxRequestBodyBytes {
		return errors.New("encode Midjourney provider request")
	}
	prepared.Body = body
	return nil
}

func ParsePlusAction(customID string) (Action, int, error) {
	if err := validateIdentifier("customId", customID, MaxCustomIDBytes, true); err != nil {
		return "", 0, err
	}
	parts := strings.Split(customID, "::")
	if len(parts) < 2 || len(parts) > 12 {
		return "", 0, errors.New("invalid customId")
	}
	position := 1
	if parts[position] == "JOB" {
		position++
	}
	if position >= len(parts) || parts[position] == "" {
		return "", 0, errors.New("invalid customId action")
	}
	rawAction := parts[position]
	switch {
	case strings.Contains(rawAction, "upsample"):
		index, err := plusIndex(parts, position+1)
		return ActionUpscale, index, err
	case rawAction == "variation":
		index, err := plusIndex(parts, position+1)
		return ActionVariation, index, err
	case rawAction == "low_variation":
		return ActionLowVariation, 1, nil
	case rawAction == "high_variation":
		return ActionHighVariation, 1, nil
	case strings.Contains(rawAction, "pan"):
		return ActionPan, 1, nil
	case strings.Contains(rawAction, "reroll"):
		return ActionReroll, 1, nil
	case rawAction == "Outpaint":
		return ActionZoom, 1, nil
	case rawAction == "CustomZoom":
		return ActionCustomZoom, 1, nil
	case rawAction == "Inpaint":
		return ActionInpaint, 1, nil
	default:
		return "", 0, errors.New("unknown customId action")
	}
}

func plusIndex(parts []string, position int) (int, error) {
	if position >= len(parts) {
		return 0, errors.New("customId index is missing")
	}
	index, err := strconv.Atoi(parts[position])
	if err != nil || index < 1 || index > 4 {
		return 0, errors.New("customId index is invalid")
	}
	return index, nil
}

func parseSimpleChange(content string) (string, Action, int, string, error) {
	fields := strings.Fields(content)
	if len(fields) != 2 || strings.Join(fields, " ") != content {
		return "", "", 0, "", errors.New("content must be '<taskId> <U1-U4|V1-V4|R>'")
	}
	if err := validateIdentifier("taskId", fields[0], MaxTaskIDBytes, true); err != nil {
		return "", "", 0, "", err
	}
	tag := strings.ToUpper(fields[1])
	if tag == "R" {
		return fields[0], ActionReroll, 1, tag, nil
	}
	if len(tag) != 2 || (tag[0] != 'U' && tag[0] != 'V') || tag[1] < '1' || tag[1] > '4' {
		return "", "", 0, "", errors.New("simple-change action is invalid")
	}
	action := ActionUpscale
	if tag[0] == 'V' {
		action = ActionVariation
	}
	return fields[0], action, int(tag[1] - '0'), tag, nil
}

func validateCommonRequest(request *Request) error {
	if request == nil {
		return errors.New("Midjourney request is nil")
	}
	if err := validateText("prompt", request.Prompt, MaxPromptRunes); err != nil {
		return err
	}
	if err := validateText("state", request.State, MaxStateRunes); err != nil {
		return err
	}
	if err := validateText("content", request.Content, MaxContentRunes); err != nil {
		return err
	}
	if request.NotifyHook != "" {
		if len(request.NotifyHook) > MaxURLBytes || !utf8.ValidString(request.NotifyHook) || hasControl(request.NotifyHook) {
			return errors.New("notifyHook is invalid")
		}
	}
	if err := validateIdentifier("customId", request.CustomID, MaxCustomIDBytes, false); err != nil {
		return err
	}
	if err := validateIdentifier("botType", request.BotType, 128, false); err != nil {
		return err
	}
	if err := validateIdentifier("taskId", request.TaskID, MaxTaskIDBytes, false); err != nil {
		return err
	}
	if request.Index < 0 || request.Index > 4 {
		return errors.New("index is invalid")
	}
	if request.Action != "" && !validAction(normalizeAction(request.Action)) {
		return errors.New("action is invalid")
	}
	if len(request.Base64Array) > MaxBase64Items {
		return errors.New("too many images")
	}
	for _, value := range request.Base64Array {
		if err := validateBase64("base64Array", value, true); err != nil {
			return err
		}
	}
	if err := validateBase64("maskBase64", request.MaskBase64, false); err != nil {
		return err
	}
	return nil
}

func validateBase64(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if !utf8.ValidString(value) || len(value) > MaxBase64ValueBytes || hasControl(value) {
		return fmt.Errorf("%s is invalid or too large", name)
	}
	payload := value
	if strings.HasPrefix(payload, "data:") {
		comma := strings.IndexByte(payload, ',')
		if comma <= 5 || !strings.HasSuffix(payload[:comma], ";base64") {
			return fmt.Errorf("%s data URI is invalid", name)
		}
		payload = payload[comma+1:]
	}
	if payload == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return fmt.Errorf("%s is not valid base64", name)
	}
	return nil
}

func validateText(name, value string, maxRunes int) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s is invalid or too large", name)
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
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value || hasControl(value) {
		return fmt.Errorf("%s is invalid or too large", name)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("%s contains an unsafe path segment", name)
	}
	for _, character := range value {
		if character == '/' || character == '\\' || character == '?' || character == '#' {
			return fmt.Errorf("%s contains unsafe characters", name)
		}
	}
	return nil
}

// ValidateProviderTaskID lets the owner-scoped relay validate an injected or
// restored provider identity at the same boundary used by the HTTP codec.
func ValidateProviderTaskID(value string) error {
	return validateIdentifier("provider task id", value, MaxTaskIDBytes, true)
}

// ValidateResultURL bounds provider-returned media references. The public
// image proxy independently enforces HTTP(S), DNS, and address safety before
// it dereferences one.
func ValidateResultURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > MaxURLBytes || !utf8.ValidString(value) || hasControl(value) {
		return errors.New("provider result URL is invalid or too large")
	}
	return nil
}

func normalizeAction(action Action) Action {
	return Action(strings.ToUpper(strings.TrimSpace(string(action))))
}

func validAction(action Action) bool {
	switch action {
	case ActionImagine, ActionDescribe, ActionBlend, ActionUpscale, ActionVariation, ActionReroll,
		ActionInpaint, ActionModal, ActionZoom, ActionCustomZoom, ActionShorten,
		ActionHighVariation, ActionLowVariation, ActionPan, ActionSwapFace, ActionUpload,
		ActionVideo, ActionEdits:
		return true
	default:
		return false
	}
}

func taskChangeAction(action Action) bool {
	switch action {
	case ActionUpscale, ActionVariation, ActionReroll, ActionInpaint, ActionZoom,
		ActionCustomZoom, ActionHighVariation, ActionLowVariation, ActionPan:
		return true
	default:
		return false
	}
}

type Response struct {
	Code        int             `json:"code"`
	Description string          `json:"description"`
	Properties  json.RawMessage `json:"properties"`
	Result      string          `json:"result"`
}

type UploadResponse struct {
	Code        int      `json:"code"`
	Description string   `json:"description"`
	Result      []string `json:"result"`
}

type VideoURL struct {
	URL string `json:"url"`
}

type Properties struct {
	FinalPrompt   string `json:"finalPrompt,omitempty"`
	FinalZhPrompt string `json:"finalZhPrompt,omitempty"`
}

type TaskResult struct {
	ProviderTaskID string
	Action         string
	CustomID       string
	BotType        string
	Prompt         string
	PromptEn       string
	Description    string
	State          string
	SubmitTime     int64
	StartTime      int64
	FinishTime     int64
	ImageURL       string
	VideoURL       string
	VideoURLs      []VideoURL
	Status         string
	Progress       string
	FailReason     string
	Buttons        json.RawMessage
	MaskBase64     string
	Properties     json.RawMessage
}

type providerTask struct {
	ID          string          `json:"id"`
	Action      string          `json:"action"`
	CustomID    string          `json:"customId"`
	BotType     string          `json:"botType"`
	Prompt      string          `json:"prompt"`
	PromptEn    string          `json:"promptEn"`
	Description string          `json:"description"`
	State       string          `json:"state"`
	SubmitTime  int64           `json:"submitTime"`
	StartTime   int64           `json:"startTime"`
	FinishTime  int64           `json:"finishTime"`
	ImageURL    string          `json:"imageUrl"`
	VideoURL    string          `json:"videoUrl"`
	VideoURLs   []VideoURL      `json:"videoUrls"`
	Status      string          `json:"status"`
	Progress    string          `json:"progress"`
	FailReason  string          `json:"failReason"`
	Buttons     json.RawMessage `json:"buttons"`
	MaskBase64  string          `json:"maskBase64"`
	Properties  json.RawMessage `json:"properties"`
}

// ProviderError is deliberately safe to log. It never includes a URL,
// credential, raw response, task identifier, or provider description.
type ProviderError struct {
	StatusCode int
	Code       int
	Definitive bool
	Cause      error
}

func (err *ProviderError) Error() string {
	if err == nil {
		return "Midjourney provider request failed"
	}
	if err.Cause != nil {
		return fmt.Sprintf("Midjourney provider returned status %d (%T)", err.StatusCode, err.Cause)
	}
	return fmt.Sprintf("Midjourney provider returned status %d with code %d", err.StatusCode, err.Code)
}

func (err *ProviderError) Unwrap() error { return err.Cause }

type RequestError struct {
	Err        error
	Dispatched bool
}

func (err *RequestError) Error() string {
	if err == nil || err.Err == nil {
		return "Midjourney request failed"
	}
	return err.Err.Error()
}

func (err *RequestError) Unwrap() error { return err.Err }

func SubmitWasDispatched(err error) bool {
	var requestError *RequestError
	return errors.As(err, &requestError) && requestError.Dispatched
}

func IsDefinitiveRejection(err error) bool {
	var providerError *ProviderError
	return errors.As(err, &providerError) && providerError.Definitive
}

type Client struct{ HTTPClient *http.Client }

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
		return client.HTTPClient
	}
	return NewHTTPClient()
}

func ValidateBaseURL(raw string) (string, error) {
	if hasControl(raw) {
		return "", errors.New("Midjourney base URL is invalid")
	}
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || len(raw) > MaxBaseURLBytes || !utf8.ValidString(raw) {
		return "", errors.New("Midjourney base URL is missing or invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Midjourney base URL must be HTTPS")
	}
	for _, segment := range strings.Split(parsed.EscapedPath(), "/") {
		decoded, decodeErr := url.PathUnescape(segment)
		if decodeErr != nil || decoded == "." || decoded == ".." || strings.Contains(decoded, "\\") {
			return "", errors.New("Midjourney base URL contains an unsafe path")
		}
	}
	return raw, nil
}

func (client *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Response, error) {
	if prepared == nil || prepared.Path == "" || len(prepared.Body) == 0 || int64(len(prepared.Body)) > MaxRequestBodyBytes || prepared.Action == ActionUpload {
		return nil, &RequestError{Err: errors.New("invalid Midjourney submit parameters")}
	}
	response, err := client.do(ctx, http.MethodPost, baseURL, apiKey, prepared.Path, prepared.Body)
	if err != nil {
		return nil, &RequestError{Err: err, Dispatched: requestDispatched(err)}
	}
	defer response.Body.Close()
	result, err := parseSubmitResponse(response, apiKey)
	if err != nil {
		return nil, &RequestError{Err: err, Dispatched: true}
	}
	return result, nil
}

func (client *Client) Upload(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*UploadResponse, error) {
	if prepared == nil || prepared.Action != ActionUpload || prepared.Path != "/mj/submit/upload-discord-images" || len(prepared.Body) == 0 {
		return nil, &RequestError{Err: errors.New("invalid Midjourney upload parameters")}
	}
	response, err := client.do(ctx, http.MethodPost, baseURL, apiKey, prepared.Path, prepared.Body)
	if err != nil {
		return nil, &RequestError{Err: err, Dispatched: requestDispatched(err)}
	}
	defer response.Body.Close()
	result, err := parseUploadResponse(response, apiKey)
	if err != nil {
		return nil, &RequestError{Err: err, Dispatched: true}
	}
	return result, nil
}

func (client *Client) Fetch(ctx context.Context, baseURL, apiKey string, ids []string) ([]TaskResult, error) {
	if err := validateProviderIDs(ids); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: ids})
	response, err := client.do(ctx, http.MethodPost, baseURL, apiKey, "/mj/task/list-by-condition", body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return parseFetchResponse(response, ids, apiKey)
}

func (client *Client) ImageSeed(ctx context.Context, baseURL, apiKey, providerTaskID string) (*Response, error) {
	if err := validateIdentifier("provider task id", providerTaskID, MaxTaskIDBytes, true); err != nil {
		return nil, err
	}
	response, err := client.do(ctx, http.MethodGet, baseURL, apiKey,
		"/mj/task/"+url.PathEscape(providerTaskID)+"/image-seed", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return parseOrdinaryResponse(response, false, apiKey)
}

func (client *Client) do(ctx context.Context, method, baseURL, apiKey, path string, body []byte) (*http.Response, error) {
	base, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if err := validateAPIKey(apiKey); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("mj-api-secret", apiKey)
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, &transportError{err: err}
	}
	return response, nil
}

type transportError struct{ err error }

func (err *transportError) Error() string { return "Midjourney provider transport failed" }
func (err *transportError) Unwrap() error { return err.err }
func requestDispatched(err error) bool {
	var transport *transportError
	return errors.As(err, &transport)
}

func parseSubmitResponse(response *http.Response, apiKey string) (*Response, error) {
	result, err := parseOrdinaryResponse(response, true, apiKey)
	if err != nil {
		return nil, err
	}
	if err := validateIdentifier("provider result", result.Result, MaxTaskIDBytes, true); err != nil {
		return nil, providerDecodeFailure(response.StatusCode, err)
	}
	return result, nil
}

func parseOrdinaryResponse(response *http.Response, requireAccepted bool, apiKey string) (*Response, error) {
	raw, err := readProviderBody(response)
	if err != nil {
		return nil, err
	}
	if responseContainsSecret(raw, apiKey) {
		return nil, providerDecodeFailure(response.StatusCode, errors.New("provider response contains a credential"))
	}
	var result Response
	if err := strictProviderDecode(raw, &result); err != nil {
		return nil, providerDecodeFailure(response.StatusCode, err)
	}
	if err := validateProviderResponseFields(&result); err != nil {
		return nil, providerDecodeFailure(response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, providerFailure(response.StatusCode, result.Code, response.StatusCode < 500)
	}
	if requireAccepted && result.Code != 1 && result.Code != 21 && result.Code != 22 {
		return nil, providerFailure(response.StatusCode, result.Code, true)
	}
	return &result, nil
}

func parseUploadResponse(response *http.Response, apiKey string) (*UploadResponse, error) {
	raw, err := readProviderBody(response)
	if err != nil {
		return nil, err
	}
	if responseContainsSecret(raw, apiKey) {
		return nil, providerDecodeFailure(response.StatusCode, errors.New("provider response contains a credential"))
	}
	var result UploadResponse
	if err := strictProviderDecode(raw, &result); err != nil {
		return nil, providerDecodeFailure(response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || result.Code != 1 {
		return nil, providerFailure(response.StatusCode, result.Code, response.StatusCode < 500 || response.StatusCode == http.StatusOK)
	}
	if err := validateText("provider description", result.Description, MaxDescriptionRunes); err != nil || len(result.Result) == 0 || len(result.Result) > MaxBase64Items {
		return nil, providerDecodeFailure(response.StatusCode, errors.New("invalid upload response"))
	}
	seen := map[string]struct{}{}
	for _, item := range result.Result {
		if item == "" || len(item) > MaxURLBytes || !utf8.ValidString(item) || hasControl(item) {
			return nil, providerDecodeFailure(response.StatusCode, errors.New("invalid upload result"))
		}
		if _, duplicate := seen[item]; duplicate {
			return nil, providerDecodeFailure(response.StatusCode, errors.New("duplicate upload result"))
		}
		seen[item] = struct{}{}
	}
	return &result, nil
}

func parseFetchResponse(response *http.Response, expected []string, apiKey string) ([]TaskResult, error) {
	raw, err := readProviderBody(response)
	if err != nil {
		return nil, err
	}
	if responseContainsSecret(raw, apiKey) {
		return nil, providerDecodeFailure(response.StatusCode, errors.New("provider response contains a credential"))
	}
	var items []providerTask
	if err := strictProviderDecode(raw, &items); err != nil {
		return nil, providerDecodeFailure(response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, providerFailure(response.StatusCode, 0, response.StatusCode < 500)
	}
	if len(items) > MaxBatchTasks {
		return nil, providerDecodeFailure(response.StatusCode, errors.New("too many tasks"))
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(items))
	results := make([]TaskResult, 0, len(items))
	for _, item := range items {
		if err := validateProviderTask(&item); err != nil {
			return nil, providerDecodeFailure(response.StatusCode, err)
		}
		if _, ok := wanted[item.ID]; !ok {
			return nil, providerDecodeFailure(response.StatusCode, errors.New("unrequested task"))
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, providerDecodeFailure(response.StatusCode, errors.New("duplicate task"))
		}
		seen[item.ID] = struct{}{}
		results = append(results, TaskResult{
			ProviderTaskID: item.ID, Action: item.Action, CustomID: item.CustomID, BotType: item.BotType,
			Prompt: item.Prompt, PromptEn: item.PromptEn, Description: item.Description, State: item.State,
			SubmitTime: item.SubmitTime, StartTime: item.StartTime, FinishTime: item.FinishTime,
			ImageURL: item.ImageURL, VideoURL: item.VideoURL, VideoURLs: append([]VideoURL(nil), item.VideoURLs...),
			Status: normalizeStatus(item.Status), Progress: normalizeProgress(item.Progress), FailReason: sanitizeProviderText(item.FailReason, MaxFailReasonRunes),
			Buttons: cloneRaw(item.Buttons), MaskBase64: item.MaskBase64, Properties: cloneRaw(item.Properties),
		})
	}
	return results, nil
}

func responseContainsSecret(raw []byte, apiKey string) bool {
	return apiKey != "" && bytes.Contains(raw, []byte(apiKey))
}

func validateProviderTask(item *providerTask) error {
	if item == nil || validateIdentifier("provider task id", item.ID, MaxTaskIDBytes, true) != nil {
		return errors.New("invalid provider task id")
	}
	if item.Action != "" && !validAction(normalizeAction(Action(item.Action))) {
		return errors.New("invalid provider task action")
	}
	for name, value := range map[string]string{
		"customId": item.CustomID, "botType": item.BotType,
	} {
		if err := validateIdentifier(name, value, MaxCustomIDBytes, false); err != nil {
			return err
		}
	}
	for name, spec := range map[string]struct {
		value string
		limit int
	}{
		"prompt": {item.Prompt, MaxPromptRunes}, "promptEn": {item.PromptEn, MaxPromptRunes},
		"description": {item.Description, MaxDescriptionRunes}, "state": {item.State, MaxStateRunes},
		"failReason": {item.FailReason, MaxFailReasonRunes},
	} {
		if err := validateText(name, spec.value, spec.limit); err != nil {
			return err
		}
	}
	if item.SubmitTime < 0 || item.StartTime < 0 || item.FinishTime < 0 ||
		item.SubmitTime > MaxProviderTimestamp || item.StartTime > MaxProviderTimestamp || item.FinishTime > MaxProviderTimestamp {
		return errors.New("invalid provider timestamp")
	}
	if !validStatus(item.Status) || !validProgress(item.Progress) {
		return errors.New("invalid provider status or progress")
	}
	for _, candidate := range append([]string{item.ImageURL, item.VideoURL}, videoURLStrings(item.VideoURLs)...) {
		if ValidateResultURL(candidate) != nil {
			return errors.New("invalid provider result URL")
		}
	}
	if len(item.VideoURLs) > MaxBase64Items || validateRawMetadata(item.Buttons) != nil || validateRawMetadata(item.Properties) != nil {
		return errors.New("invalid provider metadata")
	}
	if item.MaskBase64 != "" && validateBase64("maskBase64", item.MaskBase64, false) != nil {
		return errors.New("invalid provider mask")
	}
	return nil
}

func validateProviderResponseFields(result *Response) error {
	if result == nil || result.Code < 0 || result.Code > 1_000_000 {
		return errors.New("invalid provider response code")
	}
	if err := validateText("provider description", result.Description, MaxDescriptionRunes); err != nil {
		return err
	}
	if result.Result != "" {
		if err := validateIdentifier("provider result", result.Result, MaxTaskIDBytes, false); err != nil {
			return err
		}
	}
	return validateRawMetadata(result.Properties)
}

func validateRawMetadata(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	if len(raw) > MaxProviderMetadataBytes || !json.Valid(raw) {
		return errors.New("provider metadata is invalid or too large")
	}
	return rejectDuplicateJSONKeys(raw)
}

func validateProviderIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > MaxBatchTasks {
		return fmt.Errorf("Midjourney fetch requires between 1 and %d ids", MaxBatchTasks)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if err := validateIdentifier("provider task id", id, MaxTaskIDBytes, true); err != nil {
			return err
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("provider task ids must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateAPIKey(key string) error {
	if key == "" || len(key) > MaxAPIKeyBytes || !utf8.ValidString(key) || strings.TrimSpace(key) != key || hasControl(key) {
		return errors.New("invalid Midjourney API key")
	}
	return nil
}

func readProviderBody(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("Midjourney provider response is nil")
	}
	limit := MaxResponseBodyBytes
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &ProviderError{StatusCode: response.StatusCode, Cause: err}
	}
	return raw, nil
}

func providerDecodeFailure(status int, cause error) error {
	return &ProviderError{StatusCode: status, Cause: cause}
}

func providerFailure(status, code int, definitive bool) error {
	if status == 0 {
		status = http.StatusBadGateway
	}
	return &ProviderError{StatusCode: status, Code: code, Definitive: definitive}
}

func validStatus(raw string) bool {
	switch normalizeStatus(raw) {
	case "", "NOT_START", "SUBMITTED", "QUEUED", "IN_PROGRESS", "SUCCESS", "FAILURE":
		return true
	default:
		return false
	}
}

func normalizeStatus(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "RUNNING", "PROCESSING":
		return "IN_PROGRESS"
	case "FAILED", "FAIL":
		return "FAILURE"
	case "SUCCEEDED", "COMPLETED":
		return "SUCCESS"
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

func validProgress(raw string) bool {
	if raw == "" {
		return true
	}
	if !strings.HasSuffix(raw, "%") || len(raw) > 4 {
		return false
	}
	value, err := strconv.Atoi(strings.TrimSuffix(raw, "%"))
	return err == nil && value >= 0 && value <= 100
}

func normalizeProgress(raw string) string {
	if raw == "" {
		return "0%"
	}
	return raw
}

func videoURLStrings(urls []VideoURL) []string {
	values := make([]string, 0, len(urls))
	for _, item := range urls {
		values = append(values, item.URL)
	}
	return values
}

func sanitizeProviderText(raw string, maximum int) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	count := 0
	for _, character := range raw {
		if count >= maximum {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
				count++
			}
			continue
		}
		builder.WriteRune(character)
		count++
	}
	return strings.TrimSpace(builder.String())
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func hasControl(raw string) bool {
	for _, character := range raw {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
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
		return errors.New("Midjourney JSON contains trailing data")
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
		return errors.New("invalid Midjourney JSON")
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
				return errors.New("invalid Midjourney JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid Midjourney JSON object")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("Midjourney JSON field must not be repeated")
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return errors.New("invalid Midjourney JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return errors.New("invalid Midjourney JSON array")
		}
	default:
		return errors.New("invalid Midjourney JSON")
	}
	return nil
}
