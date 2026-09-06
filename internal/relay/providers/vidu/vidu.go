// Package vidu implements the bounded Vidu asynchronous video protocol. It
// owns request conversion, provider authentication, transport, and response
// parsing only; the relay package owns persistence and accounting.
package vidu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DirectBaseURL = "https://api.vidu.cn"

	MaxRequestBodyBytes     int64 = 16 << 20
	MaxResponseBodyBytes    int64 = 1 << 20
	MaxPromptRunes                = 10_000
	MaxImages                     = 16
	MaxImageBytes                 = 8 << 20
	MaxModelBytes                 = 128
	MaxMetadataBytes              = 1 << 20
	MaxProviderTaskIDBytes        = 191
	MaxProviderMessageRunes       = 512
	MaxPayloadBytes               = 4096
	MaxCallbackURLBytes           = 2048
	MaxResolutionBytes            = 64
	MaxMovementBytes              = 64
	MaxDuration                   = 3600
	MaxMultipartParts             = 64

	DefaultModel             = "viduq1"
	DefaultDuration          = 5
	DefaultResolution        = "1080p"
	DefaultMovementAmplitude = "auto"
)

var ModelList = []string{"viduq2", "viduq1", "vidu2.0", "vidu1.5"}

// Action values are the exact task actions persisted by the reference. Their
// provider paths are deliberately resolved separately.
type Action string

const (
	ActionTextGenerate      Action = "textGenerate"
	ActionGenerate          Action = "generate"
	ActionFirstTailGenerate Action = "firstTailGenerate"
	ActionReferenceGenerate Action = "referenceGenerate"
)

func (a Action) Path() (string, error) {
	switch a {
	case ActionTextGenerate:
		return "/ent/v2/text2video", nil
	case ActionGenerate:
		return "/ent/v2/img2video", nil
	case ActionFirstTailGenerate:
		return "/ent/v2/start-end2video", nil
	case ActionReferenceGenerate:
		return "/ent/v2/reference2video", nil
	default:
		return "", errors.New("invalid Vidu action")
	}
}

func ParseAction(value string) (Action, error) {
	action := Action(strings.TrimSpace(value))
	if _, err := action.Path(); err != nil {
		return "", err
	}
	return action, nil
}

type Payload struct {
	Model             string   `json:"model"`
	Images            []string `json:"images"`
	Prompt            string   `json:"prompt,omitempty"`
	Duration          int      `json:"duration,omitempty"`
	Seed              int      `json:"seed,omitempty"`
	Resolution        string   `json:"resolution,omitempty"`
	MovementAmplitude string   `json:"movement_amplitude,omitempty"`
	BGM               bool     `json:"bgm,omitempty"`
	Payload           string   `json:"payload,omitempty"`
	CallbackURL       string   `json:"callback_url,omitempty"`
}

type PreparedRequest struct {
	Body          []byte
	Payload       Payload
	Action        Action
	OriginModel   string
	UpstreamModel string
}

type rawRequest struct {
	Prompt         string          `json:"prompt"`
	Model          string          `json:"model"`
	Mode           string          `json:"mode,omitempty"`
	Image          string          `json:"image,omitempty"`
	Images         []string        `json:"images,omitempty"`
	Size           string          `json:"size,omitempty"`
	Duration       json.RawMessage `json:"duration,omitempty"`
	Seconds        json.RawMessage `json:"seconds,omitempty"`
	InputReference string          `json:"input_reference,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type metadataRequest struct {
	Action            *string         `json:"action,omitempty"`
	Images            *[]string       `json:"images,omitempty"`
	Prompt            *string         `json:"prompt,omitempty"`
	Duration          *int            `json:"duration,omitempty"`
	Seed              *int            `json:"seed,omitempty"`
	Resolution        *string         `json:"resolution,omitempty"`
	MovementAmplitude *string         `json:"movement_amplitude,omitempty"`
	BGM               *bool           `json:"bgm,omitempty"`
	Payload           *string         `json:"payload,omitempty"`
	CallbackURL       *string         `json:"callback_url,omitempty"`
	Model             json.RawMessage `json:"model,omitempty"`
}

// RequestedModel extracts the client-facing model before provider selection.
func RequestedModel(raw []byte, contentType string) (string, error) {
	request, err := decodeRequest(raw, contentType)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(request.Model)
	if !supportedModel(model) {
		return "", fmt.Errorf("unsupported Vidu model %q", model)
	}
	return model, nil
}

// IsModel reports whether a model belongs to the fixed reference Vidu catalog.
func IsModel(model string) bool { return supportedModel(strings.TrimSpace(model)) }

// PrepareSubmit applies the reference defaults and metadata overlay, then
// overwrites the provider model with the selected channel mapping. When
// metadata.action is absent, the pre-overlay image count selects the route,
// matching the reference adapter's validation ordering.
func PrepareSubmit(raw []byte, contentType, originModel, mappedModel string) (*PreparedRequest, error) {
	request, err := decodeRequest(raw, contentType)
	if err != nil {
		return nil, err
	}
	originModel = strings.TrimSpace(originModel)
	if !supportedModel(originModel) || strings.TrimSpace(request.Model) != originModel {
		return nil, errors.New("request model does not match the selected Vidu model")
	}
	mappedModel = strings.TrimSpace(mappedModel)
	if !validText(mappedModel, MaxModelBytes, false) {
		return nil, errors.New("mapped Vidu model is invalid")
	}
	if request.Images == nil && strings.TrimSpace(request.Image) != "" {
		request.Images = []string{request.Image}
	}
	action := actionForImages(len(request.Images))
	metadata, err := decodeMetadata(request.Metadata)
	if err != nil {
		return nil, err
	}
	if metadata.Action != nil {
		action, err = ParseAction(*metadata.Action)
		if err != nil {
			return nil, err
		}
	}
	duration, err := parseOptionalInteger(request.Duration)
	if err != nil {
		return nil, errors.New("duration must be an integer")
	}
	if duration == 0 {
		duration = DefaultDuration
	}
	payload := Payload{
		Model: mappedModel, Images: request.Images, Prompt: request.Prompt,
		Duration: duration, Resolution: defaultString(request.Size, DefaultResolution),
		MovementAmplitude: DefaultMovementAmplitude,
	}
	applyMetadata(&payload, metadata)
	if action == ActionReferenceGenerate && strings.Contains(payload.Model, "viduq2") {
		payload.Model = "viduq2"
	}
	if err := validatePayload(payload); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode Vidu request")
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	return &PreparedRequest{Body: body, Payload: payload, Action: action,
		OriginModel: originModel, UpstreamModel: payload.Model}, nil
}

func actionForImages(count int) Action {
	switch count {
	case 0:
		return ActionTextGenerate
	case 1:
		return ActionGenerate
	case 2:
		return ActionFirstTailGenerate
	default:
		return ActionReferenceGenerate
	}
}

func decodeRequest(raw []byte, contentType string) (*rawRequest, error) {
	if len(raw) == 0 {
		return nil, errors.New("Vidu request body is required")
	}
	if int64(len(raw)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, errors.New("content type must be application/json or multipart/form-data")
	}
	switch mediaType {
	case "application/json":
		if err := rejectDuplicateJSONKeys(raw); err != nil {
			return nil, err
		}
		var request rawRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&request); err != nil {
			return nil, errors.New("invalid Vidu JSON request")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("Vidu request contains trailing JSON")
		}
		return &request, nil
	case "multipart/form-data":
		return decodeMultipart(raw, params["boundary"])
	default:
		return nil, errors.New("content type must be application/json or multipart/form-data")
	}
}

func decodeMultipart(raw []byte, boundary string) (*rawRequest, error) {
	if boundary == "" || len(boundary) > 200 {
		return nil, errors.New("multipart boundary is invalid")
	}
	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	request := &rawRequest{}
	seen := make(map[string]bool)
	parts := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid multipart Vidu request")
		}
		parts++
		if parts > MaxMultipartParts {
			_ = part.Close()
			return nil, errors.New("multipart Vidu request has too many parts")
		}
		name := part.FormName()
		if name == "" || len(name) > 128 || part.FileName() != "" {
			_ = part.Close()
			return nil, errors.New("Vidu multipart fields must be text")
		}
		limit := int64(MaxPayloadBytes)
		if name == "image" || name == "images" || name == "input_reference" {
			limit = MaxImageBytes
		} else if name == "metadata" {
			limit = MaxMetadataBytes
		}
		value, readErr := httpx.ReadAllLimited(part, limit)
		_ = part.Close()
		if readErr != nil {
			return nil, readErr
		}
		if name != "images" && seen[name] {
			return nil, fmt.Errorf("multipart field %s must not be repeated", name)
		}
		seen[name] = true
		switch name {
		case "prompt":
			request.Prompt = string(value)
		case "model":
			request.Model = string(value)
		case "mode":
			request.Mode = string(value)
		case "image":
			request.Image = string(value)
		case "images":
			request.Images = append(request.Images, string(value))
		case "size":
			request.Size = string(value)
		case "duration":
			request.Duration = append(json.RawMessage(nil), strconv.Quote(string(value))...)
		case "seconds":
			request.Seconds = append(json.RawMessage(nil), strconv.Quote(string(value))...)
		case "input_reference":
			request.InputReference = string(value)
		case "metadata":
			request.Metadata = append(json.RawMessage(nil), value...)
		}
	}
	return request, nil
}

func decodeMetadata(raw json.RawMessage) (metadataRequest, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return metadataRequest{}, nil
	}
	if len(raw) > MaxMetadataBytes {
		return metadataRequest{}, errors.New("Vidu metadata is too large")
	}
	data := bytes.TrimSpace(raw)
	if len(data) > 0 && data[0] == '"' {
		var encoded string
		if json.Unmarshal(data, &encoded) != nil || len(encoded) > MaxMetadataBytes {
			return metadataRequest{}, errors.New("invalid Vidu metadata")
		}
		data = []byte(encoded)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return metadataRequest{}, errors.New("invalid Vidu metadata")
	}
	var metadata metadataRequest
	if json.Unmarshal(data, &metadata) != nil {
		return metadataRequest{}, errors.New("invalid Vidu metadata")
	}
	return metadata, nil
}

func applyMetadata(payload *Payload, metadata metadataRequest) {
	if metadata.Images != nil {
		payload.Images = *metadata.Images
	}
	if metadata.Prompt != nil {
		payload.Prompt = *metadata.Prompt
	}
	if metadata.Duration != nil {
		payload.Duration = *metadata.Duration
	}
	if metadata.Seed != nil {
		payload.Seed = *metadata.Seed
	}
	if metadata.Resolution != nil {
		payload.Resolution = *metadata.Resolution
	}
	if metadata.MovementAmplitude != nil {
		payload.MovementAmplitude = *metadata.MovementAmplitude
	}
	if metadata.BGM != nil {
		payload.BGM = *metadata.BGM
	}
	if metadata.Payload != nil {
		payload.Payload = *metadata.Payload
	}
	if metadata.CallbackURL != nil {
		payload.CallbackURL = *metadata.CallbackURL
	}
}

func validatePayload(payload Payload) error {
	if !validText(strings.TrimSpace(payload.Prompt), MaxPromptRunes*4, false) ||
		utf8.RuneCountInString(strings.TrimSpace(payload.Prompt)) > MaxPromptRunes {
		return errors.New("prompt is required and must be valid UTF-8")
	}
	if payload.Duration < 1 || payload.Duration > MaxDuration {
		return fmt.Errorf("duration must be between 1 and %d", MaxDuration)
	}
	if len(payload.Images) > MaxImages {
		return errors.New("too many Vidu images")
	}
	for _, image := range payload.Images {
		if !validText(image, MaxImageBytes, false) {
			return errors.New("Vidu image is invalid or too large")
		}
	}
	if !validText(payload.Model, MaxModelBytes, false) ||
		!validText(payload.Resolution, MaxResolutionBytes, false) ||
		!validText(payload.MovementAmplitude, MaxMovementBytes, false) ||
		!validText(payload.Payload, MaxPayloadBytes, true) ||
		!validText(payload.CallbackURL, MaxCallbackURLBytes, true) {
		return errors.New("Vidu request contains an invalid or oversized field")
	}
	if payload.CallbackURL != "" {
		parsed, err := url.Parse(payload.CallbackURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("Vidu callback_url is invalid")
		}
	}
	return nil
}

func parseOptionalInteger(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, nil
	}
	var integer int
	if json.Unmarshal(raw, &integer) == nil {
		return integer, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, errors.New("not an integer")
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
	return int(parsed), err
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func supportedModel(model string) bool {
	for _, candidate := range ModelList {
		if model == candidate {
			return true
		}
	}
	return false
}

func validText(value string, maxBytes int, emptyOK bool) bool {
	if (!emptyOK && value == "") || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || (unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t') {
			return false
		}
	}
	return true
}

type TaskStatus string

const (
	StatusSubmitted  TaskStatus = "submitted"
	StatusProcessing TaskStatus = "processing"
	StatusSucceeded  TaskStatus = "succeeded"
	StatusFailed     TaskStatus = "failed"
)

type Creation struct {
	ID       string `json:"id,omitempty"`
	URL      string `json:"url,omitempty"`
	CoverURL string `json:"cover_url,omitempty"`
}

type Task struct {
	ProviderTaskID string
	Status         TaskStatus
	ErrorCode      string
	Credits        int
	Payload        string
	Creations      []Creation
	ResultURL      string
	CreatedAt      string
}

type submitResponse struct {
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
}

type fetchResponse struct {
	State     string     `json:"state"`
	ErrCode   string     `json:"err_code"`
	Credits   int        `json:"credits"`
	Payload   string     `json:"payload"`
	Creations []Creation `json:"creations"`
}

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Vidu request failed"
	}
	return e.Err.Error()
}
func (e *RequestError) Unwrap() error { return e.Err }

func SubmitWasDispatched(err error) bool {
	var requestErr *RequestError
	return errors.As(err, &requestErr) && requestErr.Dispatched
}

type Client struct{ HTTPClient *http.Client }

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{Proxy: nil, DialContext: httpx.SafeDialContext,
			ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second},
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
	if len(base) > 4096 {
		return "", errors.New("Vidu base URL is too large")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Vidu base URL is invalid")
	}
	if parsed.Scheme != "https" && !(httpx.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("Vidu base URL must use HTTPS")
	}
	return base, nil
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Task, []byte, error) {
	if prepared == nil || len(prepared.Body) == 0 || !validCredential(apiKey) {
		return nil, nil, &RequestError{Err: errors.New("invalid Vidu submit parameters")}
	}
	path, err := prepared.Action.Path()
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	endpoint, err := endpointURL(baseURL, path)
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(prepared.Body))
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Token "+strings.TrimSpace(apiKey))
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("Vidu submit transport failed: %w", err), Dispatched: true}
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return nil, raw, &RequestError{Err: err, Dispatched: true}
	}
	var parsed submitResponse
	if rejectDuplicateJSONKeys(raw) != nil || json.Unmarshal(raw, &parsed) != nil {
		return nil, raw, &RequestError{Err: errors.New("invalid Vidu submit response"), Dispatched: true}
	}
	status, err := parseStatus(parsed.State)
	if err != nil {
		return nil, raw, &RequestError{Err: err, Dispatched: true}
	}
	// The reference treats an explicit failed state as a definitive rejection
	// before consuming the task id. A successful/queued response still must
	// carry a valid provider identity so it can be recovered without replay.
	if status == StatusFailed {
		return &Task{Status: status, CreatedAt: boundedString(parsed.CreatedAt, 128)}, raw, nil
	}
	if !validProviderTaskID(parsed.TaskID) {
		return nil, raw, &RequestError{Err: errors.New("invalid Vidu submit response"), Dispatched: true}
	}
	return &Task{ProviderTaskID: parsed.TaskID, Status: status, CreatedAt: boundedString(parsed.CreatedAt, 128)}, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, providerTaskID string) (*Task, []byte, error) {
	if !validCredential(apiKey) || !validProviderTaskID(providerTaskID) {
		return nil, nil, errors.New("invalid Vidu fetch parameters")
	}
	endpoint, err := endpointURL(baseURL, "/ent/v2/tasks/"+url.PathEscape(providerTaskID)+"/creations")
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Token "+strings.TrimSpace(apiKey))
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Vidu fetch transport failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return nil, raw, err
	}
	var parsed fetchResponse
	if rejectDuplicateJSONKeys(raw) != nil || json.Unmarshal(raw, &parsed) != nil {
		return nil, raw, errors.New("invalid Vidu fetch response")
	}
	status, err := parseStatus(parsed.State)
	if err != nil {
		return nil, raw, err
	}
	if parsed.Credits < 0 || len(parsed.Creations) > MaxImages || !validText(parsed.Payload, MaxPayloadBytes, true) {
		return nil, raw, errors.New("invalid Vidu fetch response fields")
	}
	creations := make([]Creation, len(parsed.Creations))
	copy(creations, parsed.Creations)
	for i := range creations {
		if !validText(creations[i].ID, 256, true) || !validResultURL(creations[i].URL) || !validResultURL(creations[i].CoverURL) {
			return nil, raw, errors.New("invalid Vidu creation response")
		}
	}
	resultURL := ""
	if len(creations) > 0 {
		resultURL = creations[0].URL
	}
	return &Task{ProviderTaskID: providerTaskID, Status: status,
		ErrorCode: boundedString(parsed.ErrCode, MaxProviderMessageRunes), Credits: parsed.Credits,
		Payload: parsed.Payload, Creations: creations, ResultURL: resultURL}, raw, nil
}

func endpointURL(baseURL, path string) (string, error) {
	base, err := EffectiveBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	return base + path, nil
}

func readResponse(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("Vidu response is nil")
	}
	limit := MaxResponseBodyBytes
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return raw, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	return raw, nil
}

func parseStatus(state string) (TaskStatus, error) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "created", "queueing":
		return StatusSubmitted, nil
	case "processing":
		return StatusProcessing, nil
	case "success":
		return StatusSucceeded, nil
	case "failed":
		return StatusFailed, nil
	default:
		return "", fmt.Errorf("unknown Vidu task state %q", state)
	}
}

func validProviderTaskID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > MaxProviderTaskIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f || character == '/' || character == '\\' || character == '?' || character == '#' {
			return false
		}
	}
	return true
}

func validCredential(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n")
}

func validResultURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 4096 || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func boundedString(value string, max int) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, character := range value {
		if builder.Len() >= max {
			break
		}
		if unicode.IsControl(character) {
			continue
		}
		if builder.Len()+utf8.RuneLen(character) > max {
			break
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func(map[string]struct{}) error
	walk = func(_ map[string]struct{}) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch delimiter := token.(type) {
		case json.Delim:
			switch delimiter {
			case '{':
				seen := map[string]struct{}{}
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return err
					}
					key, ok := keyToken.(string)
					if !ok {
						return errors.New("invalid JSON object key")
					}
					if _, exists := seen[key]; exists {
						return errors.New("duplicate JSON field")
					}
					seen[key] = struct{}{}
					if err := walk(nil); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			case '[':
				for decoder.More() {
					if err := walk(nil); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			}
		}
		return nil
	}
	if err := walk(nil); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
