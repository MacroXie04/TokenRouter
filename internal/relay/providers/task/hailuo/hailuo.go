// Package hailuo implements MiniMax's bounded asynchronous video protocol.
// It owns only request conversion, provider authentication, transport, and
// response parsing; durable task/accounting state belongs to the relay layer.
package hailuo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ChannelName             = "hailuo-video"
	DirectBaseURL           = "https://api.minimax.chat"
	SubmitPath              = "/v1/video_generation"
	FetchPath               = "/v1/query/video_generation"
	RetrieveFilePath        = "/v1/files/retrieve"
	DefaultDuration         = 6
	MaxRequestBodyBytes     = 16 << 20
	MaxResponseBodyBytes    = 1 << 20
	MaxPromptBytes          = 32 << 10
	MaxMetadataBytes        = 64 << 10
	MaxImageReferenceBytes  = 8 << 20
	MaxProviderTaskIDBytes  = 191
	MaxProviderFileIDBytes  = 191
	MaxProviderMessageBytes = 8 << 10
	MaxResultURLBytes       = 8 << 10
	MaxModelBytes           = 256
	maxCredentialBytes      = 16 << 10
	maxBaseURLBytes         = 4 << 10
	maxJSONDepth            = 32
	providerSuccessCode     = 0
	Resolution512P          = "512P"
	Resolution720P          = "720P"
	Resolution768P          = "768P"
	Resolution1080P         = "1080P"
)

// Action is the durable reference task action. MiniMax's Hailuo adapter uses
// one submit entry point for text, image, and subject-reference generation.
type Action string

const ActionGenerate Action = "generate"

// ParseAction validates a persisted Hailuo action without accepting aliases.
func ParseAction(value string) (Action, error) {
	if Action(value) != ActionGenerate {
		return "", errors.New("invalid Hailuo action")
	}
	return ActionGenerate, nil
}

var modelCatalog = [...]string{
	"MiniMax-Hailuo-2.3",
	"MiniMax-Hailuo-2.3-Fast",
	"MiniMax-Hailuo-02",
	"T2V-01-Director",
	"T2V-01",
	"I2V-01-Director",
	"I2V-01-live",
	"I2V-01",
	"S2V-01",
}

// ModelList returns an owned copy of the Hailuo task model catalog.
func ModelList() []string {
	models := make([]string, len(modelCatalog))
	copy(models, modelCatalog[:])
	return models
}

// IsModel reports whether model belongs to the exact Hailuo task catalog.
func IsModel(model string) bool {
	for _, candidate := range modelCatalog {
		if model == candidate {
			return true
		}
	}
	return false
}

type modelConfig struct {
	defaultResolution string
	durations         map[int]struct{}
	resolutions       map[string]struct{}
	promptOptimizer   bool
	fastPretreatment  bool
}

func configForModel(model string) (modelConfig, bool) {
	config := modelConfig{defaultResolution: Resolution720P, durations: intSet(6), resolutions: stringSet(Resolution720P), promptOptimizer: true}
	switch model {
	case "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3-Fast":
		config.defaultResolution = Resolution768P
		config.durations = intSet(6, 10)
		config.resolutions = stringSet(Resolution768P, Resolution1080P)
		config.fastPretreatment = true
	case "MiniMax-Hailuo-02":
		config.defaultResolution = Resolution768P
		config.durations = intSet(6, 10)
		config.resolutions = stringSet(Resolution512P, Resolution768P, Resolution1080P)
		config.fastPretreatment = true
	case "T2V-01-Director":
		config.defaultResolution = Resolution768P
		config.resolutions = stringSet(Resolution768P, Resolution1080P)
	case "I2V-01-Director", "I2V-01-live", "I2V-01":
		config.resolutions = stringSet(Resolution720P, Resolution1080P)
	case "T2V-01", "S2V-01":
	default:
		return modelConfig{}, false
	}
	return config, true
}

// ValidModelSettings reports whether duration and resolution are valid for an
// exact Hailuo model. The durable relay uses this to reject corrupted pricing
// and recovery snapshots before decrypting provider credentials.
func ValidModelSettings(model string, duration int, resolution string) bool {
	config, ok := configForModel(model)
	if !ok {
		return false
	}
	_, durationOK := config.durations[duration]
	_, resolutionOK := config.resolutions[resolution]
	return durationOK && resolutionOK
}

func intSet(values ...int) map[int]struct{} {
	result := make(map[int]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

type SubjectReference struct {
	Type  string   `json:"type"`
	Image []string `json:"image"`
}

type providerRequest struct {
	Model            string             `json:"model"`
	Prompt           string             `json:"prompt,omitempty"`
	PromptOptimizer  *bool              `json:"prompt_optimizer,omitempty"`
	FastPretreatment *bool              `json:"fast_pretreatment,omitempty"`
	Duration         int                `json:"duration"`
	Resolution       string             `json:"resolution"`
	CallbackURL      string             `json:"callback_url,omitempty"`
	AIGCWatermark    *bool              `json:"aigc_watermark,omitempty"`
	FirstFrameImage  string             `json:"first_frame_image,omitempty"`
	LastFrameImage   string             `json:"last_frame_image,omitempty"`
	SubjectReference []SubjectReference `json:"subject_reference,omitempty"`
}

type rawRequest struct {
	Model          string          `json:"model"`
	Prompt         string          `json:"prompt"`
	Size           string          `json:"size"`
	Duration       json.RawMessage `json:"duration"`
	Seconds        json.RawMessage `json:"seconds"`
	InputReference string          `json:"input_reference"`
	Image          string          `json:"image"`
	Images         []string        `json:"images"`
	Metadata       json.RawMessage `json:"metadata"`
}

type metadataRequest struct {
	PromptOptimizer  *bool               `json:"prompt_optimizer"`
	FastPretreatment *bool               `json:"fast_pretreatment"`
	Duration         *int                `json:"duration"`
	Resolution       *string             `json:"resolution"`
	CallbackURL      *string             `json:"callback_url"`
	AIGCWatermark    *bool               `json:"aigc_watermark"`
	FirstFrameImage  *string             `json:"first_frame_image"`
	LastFrameImage   *string             `json:"last_frame_image"`
	SubjectReference *[]SubjectReference `json:"subject_reference"`
}

type PreparedRequest struct {
	Body              []byte
	Action            Action
	OriginModel       string
	UpstreamModel     string
	Prompt            string
	Duration          int
	Resolution        string
	HasInputReference bool
}

// DeclaredModel performs bounded, duplicate-safe JSON inspection for relay
// dispatch. Full field and model validation remains PrepareSubmit's job, so a
// malformed request that declares a Hailuo model cannot fall through to a
// different provider family.
func DeclaredModel(raw []byte, contentType string) string {
	if len(raw) == 0 || len(raw) > MaxRequestBodyBytes ||
		strings.TrimSpace(strings.Split(contentType, ";")[0]) != "application/json" {
		return ""
	}
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil || fields == nil {
		return ""
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil {
		return ""
	}
	return strings.TrimSpace(model)
}

// RequestedModel extracts and validates the client-facing task model.
func RequestedModel(raw []byte, contentType string) (string, error) {
	request, _, err := decodeRequest(raw, contentType)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(request.Model)
	if !IsModel(model) {
		return "", fmt.Errorf("unsupported Hailuo model %q", model)
	}
	return model, nil
}

// PrepareSubmit validates the OpenAI-video-shaped request and creates the
// provider-native Hailuo payload using the channel's mapped model.
func PrepareSubmit(raw []byte, contentType, originModel, mappedModel string) (*PreparedRequest, error) {
	request, rawFields, err := decodeRequest(raw, contentType)
	if err != nil {
		return nil, err
	}
	originModel = strings.TrimSpace(originModel)
	mappedModel = strings.TrimSpace(mappedModel)
	if request.Model != originModel || !IsModel(originModel) || !IsModel(mappedModel) {
		return nil, errors.New("request or mapped Hailuo model is invalid")
	}
	config, _ := configForModel(mappedModel)
	prompt := strings.TrimSpace(request.Prompt)
	if !validText(prompt, MaxPromptBytes, false) {
		return nil, errors.New("Hailuo prompt is required and must be bounded UTF-8 text")
	}
	duration, err := requestDuration(request.Duration, request.Seconds)
	if err != nil {
		return nil, err
	}
	if duration == 0 {
		duration = DefaultDuration
	}
	resolution, err := requestResolution(request.Size, config.defaultResolution)
	if err != nil {
		return nil, err
	}
	metadata, err := parseMetadata(request.Metadata)
	if err != nil {
		return nil, err
	}
	payload := providerRequest{Model: mappedModel, Prompt: prompt, Duration: duration, Resolution: resolution}
	images := make([]string, 0, len(request.Images)+2)
	for _, image := range []string{request.InputReference, request.Image} {
		if strings.TrimSpace(image) != "" {
			images = append(images, image)
		}
	}
	images = append(images, request.Images...)
	if len(images) > 2 {
		return nil, errors.New("Hailuo supports at most first and last frame images")
	}
	if len(images) > 0 {
		payload.FirstFrameImage = images[0]
	}
	if len(images) > 1 {
		payload.LastFrameImage = images[1]
	}
	applyMetadata(&payload, metadata)
	if err := validateProviderRequest(payload, config); err != nil {
		return nil, err
	}
	if len(rawFields) == 0 {
		return nil, errors.New("Hailuo request body is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode Hailuo request")
	}
	if len(body) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	return &PreparedRequest{Body: body, Action: ActionGenerate, OriginModel: originModel, UpstreamModel: mappedModel,
		Prompt: payload.Prompt, Duration: payload.Duration, Resolution: payload.Resolution,
		HasInputReference: payload.FirstFrameImage != "" || payload.LastFrameImage != "" || len(payload.SubjectReference) > 0}, nil
}

func decodeRequest(raw []byte, contentType string) (rawRequest, map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return rawRequest{}, nil, errors.New("Hailuo request body is required")
	}
	if len(raw) > MaxRequestBodyBytes {
		return rawRequest{}, nil, httpx.ErrBodyTooLarge
	}
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if mediaType != "application/json" {
		return rawRequest{}, nil, errors.New("Hailuo requests must use application/json")
	}
	var fields map[string]json.RawMessage
	if err := strictJSON(raw, &fields); err != nil || fields == nil {
		return rawRequest{}, nil, errors.New("invalid Hailuo JSON request")
	}
	allowed := map[string]struct{}{
		"model": {}, "prompt": {}, "size": {}, "duration": {}, "seconds": {},
		"input_reference": {}, "image": {}, "images": {}, "metadata": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return rawRequest{}, nil, fmt.Errorf("unsupported Hailuo request field %q", field)
		}
	}
	var request rawRequest
	if err := strictJSON(raw, &request); err != nil {
		return rawRequest{}, nil, errors.New("invalid Hailuo JSON request")
	}
	return request, fields, nil
}

func requestDuration(durationRaw, secondsRaw json.RawMessage) (int, error) {
	if presentJSON(durationRaw) && presentJSON(secondsRaw) {
		return 0, errors.New("duration and seconds must not both be provided")
	}
	raw := durationRaw
	if !presentJSON(raw) {
		raw = secondsRaw
	}
	if !presentJSON(raw) {
		return 0, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil {
		value, err := strconv.ParseInt(string(number), 10, 32)
		if err == nil {
			return int(value), nil
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
		if err == nil {
			return int(value), nil
		}
	}
	return 0, errors.New("Hailuo duration must be an integer")
}

func presentJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func requestResolution(size, fallback string) (string, error) {
	value := strings.ToUpper(strings.TrimSpace(size))
	if value == "" {
		return fallback, nil
	}
	switch value {
	case Resolution512P, Resolution720P, Resolution768P, Resolution1080P:
		return value, nil
	}
	parts := strings.Split(strings.ToLower(value), "x")
	if len(parts) != 2 {
		return "", errors.New("Hailuo size must identify a supported resolution")
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "", errors.New("Hailuo size must identify a supported resolution")
	}
	for _, resolution := range []struct {
		pixels int
		name   string
	}{{1080, Resolution1080P}, {768, Resolution768P}, {720, Resolution720P}, {512, Resolution512P}} {
		if width == resolution.pixels || height == resolution.pixels {
			return resolution.name, nil
		}
	}
	return "", errors.New("Hailuo size must identify a supported resolution")
}

func parseMetadata(raw json.RawMessage) (metadataRequest, error) {
	if !presentJSON(raw) {
		return metadataRequest{}, nil
	}
	if len(raw) > MaxMetadataBytes {
		return metadataRequest{}, errors.New("Hailuo metadata is too large")
	}
	data := bytes.TrimSpace(raw)
	if len(data) > 0 && data[0] == '"' {
		var encoded string
		if json.Unmarshal(data, &encoded) != nil || len(encoded) > MaxMetadataBytes {
			return metadataRequest{}, errors.New("invalid Hailuo metadata")
		}
		data = []byte(encoded)
	}
	var metadata metadataRequest
	if err := strictJSON(data, &metadata); err != nil {
		return metadataRequest{}, errors.New("invalid Hailuo metadata")
	}
	var fields map[string]json.RawMessage
	if err := strictJSON(data, &fields); err != nil || fields == nil {
		return metadataRequest{}, errors.New("invalid Hailuo metadata")
	}
	allowed := map[string]struct{}{
		"prompt_optimizer": {}, "fast_pretreatment": {}, "duration": {}, "resolution": {},
		"callback_url": {}, "aigc_watermark": {}, "first_frame_image": {},
		"last_frame_image": {}, "subject_reference": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return metadataRequest{}, fmt.Errorf("unsupported Hailuo metadata field %q", field)
		}
	}
	return metadata, nil
}

func applyMetadata(payload *providerRequest, metadata metadataRequest) {
	if metadata.PromptOptimizer != nil {
		payload.PromptOptimizer = metadata.PromptOptimizer
	}
	if metadata.FastPretreatment != nil {
		payload.FastPretreatment = metadata.FastPretreatment
	}
	if metadata.Duration != nil {
		payload.Duration = *metadata.Duration
	}
	if metadata.Resolution != nil {
		payload.Resolution = strings.ToUpper(strings.TrimSpace(*metadata.Resolution))
	}
	if metadata.CallbackURL != nil {
		payload.CallbackURL = strings.TrimSpace(*metadata.CallbackURL)
	}
	if metadata.AIGCWatermark != nil {
		payload.AIGCWatermark = metadata.AIGCWatermark
	}
	if metadata.FirstFrameImage != nil {
		payload.FirstFrameImage = strings.TrimSpace(*metadata.FirstFrameImage)
	}
	if metadata.LastFrameImage != nil {
		payload.LastFrameImage = strings.TrimSpace(*metadata.LastFrameImage)
	}
	if metadata.SubjectReference != nil {
		payload.SubjectReference = append([]SubjectReference(nil), (*metadata.SubjectReference)...)
	}
}

func validateProviderRequest(request providerRequest, config modelConfig) error {
	if !IsModel(request.Model) || !validText(request.Prompt, MaxPromptBytes, false) {
		return errors.New("Hailuo request model or prompt is invalid")
	}
	if _, ok := config.durations[request.Duration]; !ok {
		return fmt.Errorf("duration %d is not supported by Hailuo model %s", request.Duration, request.Model)
	}
	if _, ok := config.resolutions[request.Resolution]; !ok {
		return fmt.Errorf("resolution %s is not supported by Hailuo model %s", request.Resolution, request.Model)
	}
	if request.FastPretreatment != nil && !config.fastPretreatment {
		return fmt.Errorf("fast_pretreatment is not supported by Hailuo model %s", request.Model)
	}
	if request.PromptOptimizer != nil && !config.promptOptimizer {
		return fmt.Errorf("prompt_optimizer is not supported by Hailuo model %s", request.Model)
	}
	for _, image := range []string{request.FirstFrameImage, request.LastFrameImage} {
		if image != "" && !validImageReference(image) {
			return errors.New("Hailuo frame image is invalid or too large")
		}
	}
	if request.CallbackURL != "" && !validHTTPURL(request.CallbackURL, MaxResultURLBytes) {
		return errors.New("Hailuo callback_url is invalid")
	}
	if len(request.SubjectReference) > 1 {
		return errors.New("Hailuo supports at most one subject reference")
	}
	for _, subject := range request.SubjectReference {
		if subject.Type != "character" || len(subject.Image) != 1 || !validImageReference(subject.Image[0]) {
			return errors.New("Hailuo subject_reference is invalid")
		}
	}
	return nil
}

func validImageReference(value string) bool {
	if !validText(value, MaxImageReferenceBytes, false) {
		return false
	}
	if strings.HasPrefix(value, "data:image/") {
		return strings.Contains(value, ";base64,")
	}
	return validHTTPURL(value, MaxImageReferenceBytes)
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

type Task struct {
	ProviderTaskID string
	Status         TaskStatus
	ErrorCode      string
	ErrorMessage   string
	FileID         string
	ResultURL      string
	VideoWidth     int
	VideoHeight    int
}

type baseResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

type submitResponse struct {
	TaskID   string       `json:"task_id"`
	BaseResp baseResponse `json:"base_resp"`
}

type fetchResponse struct {
	TaskID      string       `json:"task_id"`
	Status      string       `json:"status"`
	FileID      string       `json:"file_id"`
	VideoWidth  int          `json:"video_width"`
	VideoHeight int          `json:"video_height"`
	BaseResp    baseResponse `json:"base_resp"`
}

type retrieveResponse struct {
	File struct {
		DownloadURL string `json:"download_url"`
	} `json:"file"`
	BaseResp baseResponse `json:"base_resp"`
}

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Hailuo request failed"
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
	if len(base) > maxBaseURLBytes {
		return "", errors.New("Hailuo base URL is too large")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("Hailuo base URL is invalid")
	}
	if parsed.Scheme != "https" && !(httpx.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("Hailuo base URL must use HTTPS")
	}
	return base, nil
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Task, []byte, error) {
	if prepared == nil || len(prepared.Body) == 0 || !validCredential(apiKey) {
		return nil, nil, &RequestError{Err: errors.New("invalid Hailuo submit parameters")}
	}
	endpoint, err := endpointURL(baseURL, SubmitPath, nil)
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(prepared.Body))
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	setHeaders(request, apiKey, true)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("Hailuo submit transport failed: %w", err), Dispatched: true}
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return nil, raw, &RequestError{Err: err, Dispatched: true}
	}
	var parsed submitResponse
	if strictJSON(raw, &parsed) != nil {
		return nil, raw, &RequestError{Err: errors.New("invalid Hailuo submit response"), Dispatched: true}
	}
	if parsed.BaseResp.StatusCode != providerSuccessCode {
		return &Task{Status: StatusFailed, ErrorCode: strconv.Itoa(parsed.BaseResp.StatusCode),
			ErrorMessage: boundedMessage(parsed.BaseResp.StatusMsg)}, raw, nil
	}
	if !validIdentifier(parsed.TaskID, MaxProviderTaskIDBytes) {
		return nil, raw, &RequestError{Err: errors.New("invalid Hailuo submit task id"), Dispatched: true}
	}
	return &Task{ProviderTaskID: parsed.TaskID, Status: StatusSubmitted}, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, providerTaskID string) (*Task, []byte, error) {
	if !validCredential(apiKey) || !validIdentifier(providerTaskID, MaxProviderTaskIDBytes) {
		return nil, nil, errors.New("invalid Hailuo fetch parameters")
	}
	query := url.Values{"task_id": []string{providerTaskID}}
	endpoint, err := endpointURL(baseURL, FetchPath, query)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	setHeaders(request, apiKey, false)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Hailuo fetch transport failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return nil, raw, err
	}
	var parsed fetchResponse
	if strictJSON(raw, &parsed) != nil {
		return nil, raw, errors.New("invalid Hailuo fetch response")
	}
	if parsed.TaskID != "" && parsed.TaskID != providerTaskID {
		return nil, raw, errors.New("Hailuo fetch task id does not match the requested task")
	}
	if parsed.BaseResp.StatusCode != providerSuccessCode {
		return &Task{ProviderTaskID: providerTaskID, Status: StatusFailed,
			ErrorCode: strconv.Itoa(parsed.BaseResp.StatusCode), ErrorMessage: boundedMessage(parsed.BaseResp.StatusMsg)}, raw, nil
	}
	status, err := parseStatus(parsed.Status)
	if err != nil {
		return nil, raw, err
	}
	task := &Task{ProviderTaskID: providerTaskID, Status: status,
		FileID: parsed.FileID, VideoWidth: parsed.VideoWidth, VideoHeight: parsed.VideoHeight}
	if parsed.VideoWidth < 0 || parsed.VideoHeight < 0 {
		return nil, raw, errors.New("invalid Hailuo video dimensions")
	}
	if status != StatusSucceeded {
		return task, raw, nil
	}
	if !validIdentifier(parsed.FileID, MaxProviderFileIDBytes) {
		return nil, raw, errors.New("successful Hailuo response has an invalid file id")
	}
	resultURL, err := c.retrieveFile(ctx, baseURL, apiKey, parsed.FileID)
	if err != nil {
		return nil, raw, err
	}
	task.ResultURL = resultURL
	return task, raw, nil
}

func (c *Client) retrieveFile(ctx context.Context, baseURL, apiKey, fileID string) (string, error) {
	query := url.Values{"file_id": []string{fileID}}
	endpoint, err := endpointURL(baseURL, RetrieveFilePath, query)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	setHeaders(request, apiKey, false)
	response, err := c.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("Hailuo file retrieval transport failed: %w", err)
	}
	defer response.Body.Close()
	raw, err := readResponse(response)
	if err != nil {
		return "", err
	}
	var parsed retrieveResponse
	if strictJSON(raw, &parsed) != nil || parsed.BaseResp.StatusCode != providerSuccessCode ||
		!validHTTPURL(parsed.File.DownloadURL, MaxResultURLBytes) {
		return "", errors.New("invalid Hailuo file retrieval response")
	}
	return parsed.File.DownloadURL, nil
}

func endpointURL(baseURL, path string, query url.Values) (string, error) {
	base, err := EffectiveBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base + path)
	if err != nil {
		return "", errors.New("build Hailuo request URL")
	}
	if len(query) > 0 {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func setHeaders(request *http.Request, apiKey string, content bool) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	if content {
		request.Header.Set("Content-Type", "application/json")
	}
}

func readResponse(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("Hailuo response is nil")
	}
	limit := int64(MaxResponseBodyBytes)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: err}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return raw, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	return raw, nil
}

func parseStatus(status string) (TaskStatus, error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "preparing", "queueing":
		return StatusSubmitted, nil
	case "processing":
		return StatusProcessing, nil
	case "success":
		return StatusSucceeded, nil
	case "fail", "failed":
		return StatusFailed, nil
	default:
		return "", fmt.Errorf("unknown Hailuo task status %q", status)
	}
}

func validCredential(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maxCredentialBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func validIdentifier(value string, maximum int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f || strings.ContainsRune("/\\?#&", character) {
			return false
		}
	}
	return true
}

func validHTTPURL(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func boundedMessage(value string) string {
	value = strings.TrimSpace(value)
	if !validText(value, MaxProviderMessageBytes, true) {
		return "Hailuo provider rejected the request"
	}
	// Provider text is untrusted and may echo credentials or request content.
	// Preserve only the separately bounded numeric status code at higher layers.
	return "Hailuo provider rejected the request"
}

func strictJSON(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("JSON body is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSON(decoder, 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return errors.New("JSON body has trailing data")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON body has trailing data")
	}
	return nil
}

func walkJSON(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("JSON nesting is too deep")
	}
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
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
