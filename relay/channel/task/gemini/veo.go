// Package gemini implements Google's bounded Gemini Veo asynchronous task protocol.
// Durable task persistence and accounting are owned by the relay layer.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	ChannelName                   = "gemini-veo"
	DirectBaseURL                 = "https://generativelanguage.googleapis.com"
	APIVersion                    = "v1beta"
	DefaultDuration               = 8
	DefaultResolution             = "720p"
	DefaultAspectRatio            = "16:9"
	MaxRequestBodyBytes           = 32 << 20
	MaxResponseBodyBytes          = 64 << 20
	MaxImageBytes                 = 20 << 20
	MaxPromptBytes                = 32 << 10
	MaxMetadataBytes              = 64 << 10
	MaxOperationNameBytes         = 512
	MaxProviderTaskIDBytes        = MaxOperationNameBytes
	MaxProviderMessageBytes       = 8 << 10
	MaxResultURLBytes             = 8 << 10
	MaxModelBytes                 = 256
	MaxContentBodyBytes     int64 = 512 << 20
	maxCredentialBytes            = 16 << 10
	maxBaseURLBytes               = 4 << 10
	maxJSONDepth                  = 32
)

// Action is the reference task action persisted by the durable relay. Veo
// keeps text and image generations distinct even though both use the same
// predictLongRunning endpoint.
type Action string

const (
	ActionGenerate     Action = "generate"
	ActionTextGenerate Action = "textGenerate"
)

func ParseAction(value string) (Action, error) {
	switch Action(value) {
	case ActionGenerate:
		return ActionGenerate, nil
	case ActionTextGenerate:
		return ActionTextGenerate, nil
	default:
		return "", errors.New("invalid Gemini Veo action")
	}
}

var modelCatalog = [...]string{
	"veo-3.0-generate-001",
	"veo-3.0-fast-generate-001",
	"veo-3.1-generate-preview",
	"veo-3.1-fast-generate-preview",
}

// ModelList returns an owned copy of the exact reference Veo model catalog.
func ModelList() []string {
	models := make([]string, len(modelCatalog))
	copy(models, modelCatalog[:])
	return models
}

func IsModel(model string) bool {
	for _, candidate := range modelCatalog {
		if model == candidate {
			return true
		}
	}
	return false
}

// DeclaredModel recognizes Veo requests for shared dispatch while still
// rejecting duplicate keys, trailing data, invalid JSON, and wrong media types.
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
	if rawModel, ok := fields["model"]; !ok || json.Unmarshal(rawModel, &model) != nil {
		return ""
	}
	model = strings.TrimSpace(model)
	if !IsModel(model) {
		return ""
	}
	return model
}

type imageInput struct {
	BytesBase64Encoded string `json:"bytesBase64Encoded"`
	MimeType           string `json:"mimeType"`
}

type instance struct {
	Prompt string      `json:"prompt"`
	Image  *imageInput `json:"image,omitempty"`
}

type parameters struct {
	SampleCount        int    `json:"sampleCount"`
	DurationSeconds    int    `json:"durationSeconds"`
	AspectRatio        string `json:"aspectRatio"`
	Resolution         string `json:"resolution"`
	NegativePrompt     string `json:"negativePrompt,omitempty"`
	PersonGeneration   string `json:"personGeneration,omitempty"`
	StorageURI         string `json:"storageUri,omitempty"`
	CompressionQuality string `json:"compressionQuality,omitempty"`
	ResizeMode         string `json:"resizeMode,omitempty"`
	Seed               *int   `json:"seed,omitempty"`
	GenerateAudio      *bool  `json:"generateAudio,omitempty"`
}

type providerRequest struct {
	Instances  []instance  `json:"instances"`
	Parameters *parameters `json:"parameters"`
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

type metadataParameters struct {
	DurationSeconds    *int    `json:"durationSeconds"`
	AspectRatio        *string `json:"aspectRatio"`
	Resolution         *string `json:"resolution"`
	NegativePrompt     *string `json:"negativePrompt"`
	PersonGeneration   *string `json:"personGeneration"`
	StorageURI         *string `json:"storageUri"`
	CompressionQuality *string `json:"compressionQuality"`
	ResizeMode         *string `json:"resizeMode"`
	Seed               *int    `json:"seed"`
	GenerateAudio      *bool   `json:"generateAudio"`
	SampleCount        *int    `json:"sampleCount"`
}

type PreparedRequest struct {
	Body          []byte
	Action        Action
	OriginModel   string
	UpstreamModel string
	Prompt        string
	Duration      int
	Resolution    string
	AspectRatio   string
	HasImage      bool
}

func RequestedModel(raw []byte, contentType string) (string, error) {
	request, _, err := decodeRequest(raw, contentType)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(request.Model)
	if !IsModel(model) {
		return "", fmt.Errorf("unsupported Gemini Veo model %q", model)
	}
	return model, nil
}

func PrepareSubmit(raw []byte, contentType, originModel, mappedModel string) (*PreparedRequest, error) {
	request, fields, err := decodeRequest(raw, contentType)
	if err != nil {
		return nil, err
	}
	originModel = strings.TrimSpace(originModel)
	mappedModel = strings.TrimSpace(mappedModel)
	if strings.TrimSpace(request.Model) != originModel || !IsModel(originModel) || !IsModel(mappedModel) {
		return nil, errors.New("request or mapped Gemini Veo model is invalid")
	}
	prompt := strings.TrimSpace(request.Prompt)
	if !validText(prompt, MaxPromptBytes, false) {
		return nil, errors.New("Gemini Veo prompt is required and must be bounded UTF-8 text")
	}
	duration, err := requestDuration(request.Duration, request.Seconds)
	if err != nil {
		return nil, err
	}
	if duration == 0 {
		duration = DefaultDuration
	}
	resolution, aspectRatio, err := sizeParameters(request.Size)
	if err != nil {
		return nil, err
	}
	params := &parameters{SampleCount: 1, DurationSeconds: duration, Resolution: resolution, AspectRatio: aspectRatio}
	metadata, err := parseMetadata(request.Metadata)
	if err != nil {
		return nil, err
	}
	applyMetadata(params, metadata)
	params.Resolution = strings.ToLower(strings.TrimSpace(params.Resolution))
	params.AspectRatio = strings.TrimSpace(params.AspectRatio)
	image, err := requestImage(request)
	if err != nil {
		return nil, err
	}
	payload := providerRequest{Instances: []instance{{Prompt: prompt, Image: image}}, Parameters: params}
	if err := validateProviderRequest(payload, mappedModel); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, errors.New("Gemini Veo request body is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode Gemini Veo request")
	}
	if len(body) > MaxRequestBodyBytes {
		return nil, common.ErrBodyTooLarge
	}
	return (&PreparedRequest{
		Body: body, Action: ActionTextGenerate, OriginModel: originModel, UpstreamModel: mappedModel, Prompt: prompt,
		Duration: params.DurationSeconds, Resolution: params.Resolution,
		AspectRatio: params.AspectRatio, HasImage: image != nil,
	}).withBodyDrivenAction(), nil
}

func (request *PreparedRequest) withBodyDrivenAction() *PreparedRequest {
	if request != nil && request.HasImage {
		request.Action = ActionGenerate
	}
	return request
}

func decodeRequest(raw []byte, contentType string) (rawRequest, map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return rawRequest{}, nil, errors.New("Gemini Veo request body is required")
	}
	if len(raw) > MaxRequestBodyBytes {
		return rawRequest{}, nil, common.ErrBodyTooLarge
	}
	if strings.TrimSpace(strings.Split(contentType, ";")[0]) != "application/json" {
		return rawRequest{}, nil, errors.New("Gemini Veo requests must use application/json")
	}
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil || fields == nil {
		return rawRequest{}, nil, errors.New("invalid Gemini Veo JSON request")
	}
	allowed := map[string]struct{}{
		"model": {}, "prompt": {}, "size": {}, "duration": {}, "seconds": {},
		"input_reference": {}, "image": {}, "images": {}, "metadata": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return rawRequest{}, nil, fmt.Errorf("unsupported Gemini Veo request field %q", field)
		}
	}
	var request rawRequest
	if strictJSON(raw, &request) != nil {
		return rawRequest{}, nil, errors.New("invalid Gemini Veo JSON request")
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
	return 0, errors.New("Gemini Veo duration must be an integer")
}

func presentJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func sizeParameters(size string) (string, string, error) {
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "" {
		return DefaultResolution, DefaultAspectRatio, nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "", "", errors.New("Gemini Veo size must use WIDTHxHEIGHT")
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "", "", errors.New("Gemini Veo size must use positive dimensions")
	}
	var resolution string
	switch {
	case max(width, height) == 1280 && min(width, height) == 720:
		resolution = "720p"
	case max(width, height) == 1920 && min(width, height) == 1080:
		resolution = "1080p"
	case max(width, height) == 3840 && min(width, height) == 2160:
		resolution = "4k"
	default:
		return "", "", errors.New("Gemini Veo size is unsupported")
	}
	aspect := "16:9"
	if height > width {
		aspect = "9:16"
	}
	return resolution, aspect, nil
}

func parseMetadata(raw json.RawMessage) (metadataParameters, error) {
	if !presentJSON(raw) {
		return metadataParameters{}, nil
	}
	if len(raw) > MaxMetadataBytes {
		return metadataParameters{}, errors.New("Gemini Veo metadata is too large")
	}
	data := bytes.TrimSpace(raw)
	if len(data) > 0 && data[0] == '"' {
		var encoded string
		if json.Unmarshal(data, &encoded) != nil || len(encoded) > MaxMetadataBytes {
			return metadataParameters{}, errors.New("invalid Gemini Veo metadata")
		}
		data = []byte(encoded)
	}
	var fields map[string]json.RawMessage
	if strictJSON(data, &fields) != nil || fields == nil {
		return metadataParameters{}, errors.New("invalid Gemini Veo metadata")
	}
	allowed := map[string]struct{}{
		"durationSeconds": {}, "aspectRatio": {}, "resolution": {}, "negativePrompt": {},
		"personGeneration": {}, "storageUri": {}, "compressionQuality": {}, "resizeMode": {},
		"seed": {}, "generateAudio": {}, "sampleCount": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return metadataParameters{}, fmt.Errorf("unsupported Gemini Veo metadata field %q", field)
		}
	}
	var metadata metadataParameters
	if strictJSON(data, &metadata) != nil {
		return metadataParameters{}, errors.New("invalid Gemini Veo metadata")
	}
	return metadata, nil
}

func applyMetadata(params *parameters, metadata metadataParameters) {
	if metadata.DurationSeconds != nil {
		params.DurationSeconds = *metadata.DurationSeconds
	}
	setString(&params.AspectRatio, metadata.AspectRatio)
	setString(&params.Resolution, metadata.Resolution)
	setString(&params.NegativePrompt, metadata.NegativePrompt)
	setString(&params.PersonGeneration, metadata.PersonGeneration)
	setString(&params.StorageURI, metadata.StorageURI)
	setString(&params.CompressionQuality, metadata.CompressionQuality)
	setString(&params.ResizeMode, metadata.ResizeMode)
	if metadata.Seed != nil {
		params.Seed = metadata.Seed
	}
	if metadata.GenerateAudio != nil {
		params.GenerateAudio = metadata.GenerateAudio
	}
	// Veo task responses are singular, matching the public video endpoint.
	params.SampleCount = 1
}

func setString(destination *string, value *string) {
	if value != nil {
		*destination = strings.TrimSpace(*value)
	}
}

func requestImage(request rawRequest) (*imageInput, error) {
	values := make([]string, 0, len(request.Images)+2)
	values = append(values, request.Image, request.InputReference)
	values = append(values, request.Images...)
	selected := ""
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			if selected != "" {
				return nil, errors.New("Gemini Veo supports exactly one input image")
			}
			selected = value
		}
	}
	if selected == "" {
		return nil, nil
	}
	return parseImageInput(selected)
}

func parseImageInput(value string) (*imageInput, error) {
	if len(value) > base64.StdEncoding.EncodedLen(MaxImageBytes)+256 {
		return nil, errors.New("Gemini Veo input image is too large")
	}
	mimeType := ""
	encoded := value
	if strings.HasPrefix(value, "data:") {
		comma := strings.IndexByte(value, ',')
		if comma <= len("data:") {
			return nil, errors.New("Gemini Veo input image data URI is invalid")
		}
		header := value[len("data:"):comma]
		parts := strings.Split(header, ";")
		if len(parts) != 2 || !strings.EqualFold(parts[1], "base64") {
			return nil, errors.New("Gemini Veo input image data URI is invalid")
		}
		mediaType, _, err := mime.ParseMediaType(parts[0])
		if err != nil {
			return nil, errors.New("Gemini Veo input image data URI is invalid")
		}
		mimeType = strings.ToLower(mediaType)
		encoded = value[comma+1:]
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > MaxImageBytes {
		return nil, errors.New("Gemini Veo input image must be bounded base64")
	}
	detected := strings.ToLower(http.DetectContentType(decoded))
	if mimeType == "" {
		mimeType = detected
	}
	if !supportedImageMIME(mimeType) || !supportedImageMIME(detected) || mimeType != detected {
		return nil, errors.New("Gemini Veo input image MIME type is invalid")
	}
	return &imageInput{BytesBase64Encoded: base64.StdEncoding.EncodeToString(decoded), MimeType: mimeType}, nil
}

func supportedImageMIME(value string) bool {
	return value == "image/png" || value == "image/jpeg" || value == "image/webp"
}

func validateProviderRequest(request providerRequest, model string) error {
	if !IsModel(model) || len(request.Instances) != 1 || request.Parameters == nil ||
		!validText(request.Instances[0].Prompt, MaxPromptBytes, false) {
		return errors.New("Gemini Veo request is invalid")
	}
	params := request.Parameters
	if params.SampleCount != 1 {
		return errors.New("Gemini Veo sampleCount must be one")
	}
	if params.DurationSeconds != 4 && params.DurationSeconds != 6 && params.DurationSeconds != 8 {
		return errors.New("Gemini Veo duration must be 4, 6, or 8 seconds")
	}
	if params.Resolution != "720p" && params.Resolution != "1080p" && params.Resolution != "4k" {
		return errors.New("Gemini Veo resolution is unsupported")
	}
	if params.Resolution == "4k" && !strings.Contains(model, "3.1") {
		return errors.New("Gemini Veo 3.0 does not support 4k output")
	}
	if params.AspectRatio != "16:9" && params.AspectRatio != "9:16" {
		return errors.New("Gemini Veo aspectRatio is unsupported")
	}
	for _, value := range []string{params.NegativePrompt, params.PersonGeneration, params.StorageURI, params.CompressionQuality, params.ResizeMode} {
		if !validText(value, MaxMetadataBytes, true) {
			return errors.New("Gemini Veo metadata text is invalid")
		}
	}
	if params.StorageURI != "" && !validStorageURI(params.StorageURI) {
		return errors.New("Gemini Veo storageUri is invalid")
	}
	if params.Seed != nil && (*params.Seed < 0 || *params.Seed > 2147483647) {
		return errors.New("Gemini Veo seed is out of range")
	}
	return nil
}

// ValidModelSettings verifies persisted billing/recovery coordinates without
// accepting aliases or provider-invalid combinations.
func ValidModelSettings(model string, duration int, resolution, aspectRatio string) bool {
	request := providerRequest{
		Instances: []instance{{Prompt: "x"}},
		Parameters: &parameters{
			SampleCount: 1, DurationSeconds: duration,
			Resolution:  strings.ToLower(strings.TrimSpace(resolution)),
			AspectRatio: strings.TrimSpace(aspectRatio),
		},
	}
	return validateProviderRequest(request, model) == nil
}

func validStorageURI(value string) bool {
	if !strings.HasPrefix(value, "gs://") || len(value) > MaxResultURLBytes || strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validText(value string, maximum int, emptyOK bool) bool {
	if (!emptyOK && value == "") || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || (unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t') {
			return false
		}
	}
	return true
}

// ResolutionMultiplier matches the reference Veo pricing multipliers.
func ResolutionMultiplier(model, resolution string) float64 {
	if strings.ToLower(strings.TrimSpace(resolution)) != "4k" {
		return 1
	}
	if strings.Contains(model, "3.1-fast-generate") {
		return 2.333333
	}
	if strings.Contains(model, "3.1") {
		return 1.5
	}
	return 1
}

type TaskStatus string

const (
	StatusProcessing TaskStatus = "processing"
	StatusSucceeded  TaskStatus = "succeeded"
	StatusFailed     TaskStatus = "failed"
)

type Task struct {
	ProviderTaskID string
	Status         TaskStatus
	ErrorCode      string
	ErrorMessage   string
	ResultURL      string
}

type providerError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

type videoReference struct {
	URI string `json:"uri"`
}

type generatedVideo struct {
	Video videoReference `json:"video"`
}

type operationResponse struct {
	Name     string         `json:"name"`
	Done     bool           `json:"done"`
	Error    *providerError `json:"error"`
	Response struct {
		GenerateVideoResponse struct {
			GeneratedVideos []generatedVideo `json:"generatedVideos"`
		} `json:"generateVideoResponse"`
	} `json:"response"`
}

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Gemini Veo request failed"
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
		Transport: &http.Transport{Proxy: nil, DialContext: common.SafeDialContext,
			ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       90 * time.Second,
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
		return "", errors.New("Gemini Veo base URL is too large")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("Gemini Veo base URL is invalid")
	}
	if parsed.Scheme != "https" && !(common.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("Gemini Veo base URL must use HTTPS")
	}
	return base, nil
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Task, []byte, error) {
	if prepared == nil || len(prepared.Body) == 0 || !IsModel(prepared.UpstreamModel) || !validCredential(apiKey) {
		return nil, nil, &RequestError{Err: errors.New("invalid Gemini Veo submit parameters")}
	}
	version := setting.GetGeminiAPIVersion(prepared.UpstreamModel)
	endpoint, err := endpointURL(baseURL, "/"+version+"/models/"+prepared.UpstreamModel+":predictLongRunning")
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
		return nil, nil, &RequestError{Err: fmt.Errorf("Gemini Veo submit transport failed: %w", err), Dispatched: true}
	}
	defer response.Body.Close()
	raw, readErr := readResponse(response)
	if readErr != nil {
		return nil, raw, &RequestError{Err: readErr, Dispatched: true}
	}
	var parsed operationResponse
	parseErr := strictJSON(raw, &parsed)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if parseErr == nil && parsed.Error != nil && response.StatusCode >= http.StatusBadRequest &&
			response.StatusCode < http.StatusInternalServerError && response.StatusCode != http.StatusRequestTimeout &&
			response.StatusCode != http.StatusConflict {
			return failedTask(parsed.Error), raw, nil
		}
		return nil, raw, &RequestError{Err: &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}, Dispatched: true}
	}
	if parseErr != nil {
		return nil, raw, &RequestError{Err: errors.New("invalid Gemini Veo submit response"), Dispatched: true}
	}
	if parsed.Error != nil {
		return failedTask(parsed.Error), raw, nil
	}
	if !validOperationName(parsed.Name, prepared.UpstreamModel) {
		return nil, raw, &RequestError{Err: errors.New("invalid Gemini Veo operation name"), Dispatched: true}
	}
	return &Task{ProviderTaskID: parsed.Name, Status: StatusProcessing}, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, operationName string) (*Task, []byte, error) {
	if !validCredential(apiKey) || !validOperationName(operationName, "") {
		return nil, nil, errors.New("invalid Gemini Veo fetch parameters")
	}
	version := setting.GetGeminiAPIVersion("default")
	endpoint, err := endpointURL(baseURL, "/"+version+"/"+operationName)
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
		return nil, nil, fmt.Errorf("Gemini Veo fetch transport failed: %w", err)
	}
	defer response.Body.Close()
	raw, readErr := readResponse(response)
	if readErr != nil {
		return nil, raw, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, raw, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	var parsed operationResponse
	if strictJSON(raw, &parsed) != nil {
		return nil, raw, errors.New("invalid Gemini Veo fetch response")
	}
	if parsed.Error != nil {
		task := failedTask(parsed.Error)
		task.ProviderTaskID = operationName
		return task, raw, nil
	}
	if parsed.Name != "" && parsed.Name != operationName {
		return nil, raw, errors.New("Gemini Veo operation name does not match the requested task")
	}
	if !parsed.Done {
		return &Task{ProviderTaskID: operationName, Status: StatusProcessing}, raw, nil
	}
	videos := parsed.Response.GenerateVideoResponse.GeneratedVideos
	if len(videos) != 1 || !validHTTPURL(videos[0].Video.URI, MaxResultURLBytes) {
		return nil, raw, errors.New("successful Gemini Veo response has an invalid video result")
	}
	return &Task{ProviderTaskID: operationName, Status: StatusSucceeded, ResultURL: videos[0].Video.URI}, raw, nil
}

// Content fetches an authenticated provider-owned video without forwarding credentials cross-origin.
func (c *Client) Content(ctx context.Context, baseURL, apiKey, resultURL string) (*http.Response, error) {
	if !validCredential(apiKey) || !validHTTPURL(resultURL, MaxResultURLBytes) {
		return nil, errors.New("invalid Gemini Veo content parameters")
	}
	base, err := EffectiveBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	baseParsed, _ := url.Parse(base)
	resultParsed, _ := url.Parse(resultURL)
	if resultParsed.Scheme != "https" && !(common.SSRFDisabled() && resultParsed.Scheme == "http") {
		return nil, errors.New("Gemini Veo content URL must use HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "video/*,application/octet-stream")
	if strings.EqualFold(baseParsed.Hostname(), resultParsed.Hostname()) && effectivePort(baseParsed) == effectivePort(resultParsed) {
		request.Header.Set("x-goog-api-key", strings.TrimSpace(apiKey))
	}
	response, err := c.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("Gemini Veo content transport failed: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		raw, _ := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamErrorBodyBytes)
		return nil, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	response.Body = &boundedReadCloser{reader: io.LimitReader(response.Body, MaxContentBodyBytes+1), closer: response.Body, remaining: MaxContentBodyBytes}
	return response, nil
}

type boundedReadCloser struct {
	reader    io.Reader
	closer    io.Closer
	remaining int64
}

func (body *boundedReadCloser) Read(buffer []byte) (int, error) {
	if body.remaining <= 0 {
		var probe [1]byte
		n, err := body.reader.Read(probe[:])
		if n > 0 {
			return 0, common.ErrBodyTooLarge
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

func (body *boundedReadCloser) Close() error { return body.closer.Close() }

func failedTask(providerErr *providerError) *Task {
	if providerErr == nil {
		return &Task{Status: StatusFailed, ErrorCode: "provider_error", ErrorMessage: "Gemini Veo provider rejected the request"}
	}
	code := strings.TrimSpace(providerErr.Status)
	if code == "" {
		code = strconv.Itoa(providerErr.Code)
	}
	return &Task{Status: StatusFailed, ErrorCode: boundedCode(code), ErrorMessage: boundedMessage(providerErr.Message)}
}

func endpointURL(baseURL, path string) (string, error) {
	base, err := EffectiveBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base + path)
	if err != nil {
		return "", errors.New("build Gemini Veo request URL")
	}
	return parsed.String(), nil
}

func setHeaders(request *http.Request, apiKey string, content bool) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-goog-api-key", strings.TrimSpace(apiKey))
	if content {
		request.Header.Set("Content-Type", "application/json")
	}
}

func readResponse(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("Gemini Veo response is nil")
	}
	limit := int64(MaxResponseBodyBytes)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: err}
	}
	return raw, nil
}

func validOperationName(value, expectedModel string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > MaxOperationNameBytes ||
		!utf8.ValidString(value) || strings.Contains(value, "..") || strings.ContainsAny(value, "\\?#&%\r\n\x00") {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) != 4 || parts[0] != "models" || parts[2] != "operations" || parts[1] == "" || parts[3] == "" {
		return false
	}
	if expectedModel != "" && parts[1] != expectedModel {
		return false
	}
	for _, part := range []string{parts[1], parts[3]} {
		for _, character := range part {
			if !(character == '-' || character == '_' || character == '.' || character >= '0' && character <= '9' ||
				character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z') {
				return false
			}
		}
	}
	return true
}

// ValidateOperationName exposes the strict provider-operation fence to the
// durable relay without exposing the operation parser itself.
func ValidateOperationName(value, expectedModel string) error {
	if !validOperationName(value, expectedModel) {
		return errors.New("invalid Gemini Veo operation name")
	}
	return nil
}

func validCredential(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maxCredentialBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func validHTTPURL(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func effectivePort(parsed *url.URL) string {
	if parsed.Port() != "" {
		return parsed.Port()
	}
	if parsed.Scheme == "https" {
		return "443"
	}
	return "80"
}

func boundedCode(value string) string {
	value = strings.TrimSpace(value)
	if !validText(value, 256, true) {
		return "provider_error"
	}
	return value
}

func boundedMessage(value string) string {
	value = strings.TrimSpace(value)
	if !validText(value, MaxProviderMessageBytes, true) {
		return "Gemini Veo provider rejected the request"
	}
	return value
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
