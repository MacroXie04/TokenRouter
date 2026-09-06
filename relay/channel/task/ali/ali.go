// Package ali implements Alibaba DashScope's bounded asynchronous Wan video protocol.
// Durable task state and quota accounting are intentionally owned by the relay layer.
package ali

import (
	"bytes"
	"context"
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
	ChannelName             = "ali-video"
	DirectBaseURL           = "https://dashscope.aliyuncs.com"
	SubmitPath              = "/api/v1/services/aigc/video-generation/video-synthesis"
	FetchPathPrefix         = "/api/v1/tasks/"
	DefaultDuration         = 5
	MaxDuration             = 10
	MinDuration             = 1
	MaxRequestBodyBytes     = 16 << 20
	MaxResponseBodyBytes    = 1 << 20
	MaxPromptBytes          = 32 << 10
	MaxMetadataBytes        = 64 << 10
	MaxMediaReferenceBytes  = 8 << 20
	MaxProviderTaskIDBytes  = 191
	MaxProviderMessageBytes = 8 << 10
	MaxResultURLBytes       = 8 << 10
	maxCredentialBytes      = 16 << 10
	maxBaseURLBytes         = 4 << 10
	maxJSONDepth            = 32
)

var modelCatalog = [...]string{
	"wan2.7-i2v",
	"wan2.7-t2v",
	"wan2.5-i2v-preview",
	"wan2.2-i2v-flash",
	"wan2.2-i2v-plus",
	"wanx2.1-i2v-plus",
	"wanx2.1-i2v-turbo",
}

var supportedSizes = map[string]string{
	"832*480": "480P", "480*832": "480P", "624*624": "480P",
	"1280*720": "720P", "720*1280": "720P", "960*960": "720P",
	"1088*832": "720P", "832*1088": "720P",
	"1920*1080": "1080P", "1080*1920": "1080P", "1440*1440": "1080P",
	"1632*1248": "1080P", "1248*1632": "1080P",
}

// ModelList returns an owned copy of the exact reference model catalog.
func ModelList() []string {
	models := make([]string, len(modelCatalog))
	copy(models, modelCatalog[:])
	return models
}

// IsModel reports whether model is an Alibaba Wan task model.
func IsModel(model string) bool {
	for _, candidate := range modelCatalog {
		if model == candidate {
			return true
		}
	}
	return false
}

// DeclaredModel recognizes the provider family without accepting a malformed
// request. The shared video dispatcher uses it so an Alibaba Wan request with
// an unsupported field cannot silently fall through to another provider.
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

type Media struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type providerInput struct {
	Prompt         string  `json:"prompt,omitempty"`
	ImgURL         string  `json:"img_url,omitempty"`
	FirstFrameURL  string  `json:"first_frame_url,omitempty"`
	LastFrameURL   string  `json:"last_frame_url,omitempty"`
	AudioURL       string  `json:"audio_url,omitempty"`
	Media          []Media `json:"media,omitempty"`
	NegativePrompt string  `json:"negative_prompt,omitempty"`
	Template       string  `json:"template,omitempty"`
}

type providerParameters struct {
	Resolution   string `json:"resolution,omitempty"`
	Size         string `json:"size,omitempty"`
	Duration     int    `json:"duration"`
	PromptExtend *bool  `json:"prompt_extend,omitempty"`
	Watermark    *bool  `json:"watermark,omitempty"`
	Audio        *bool  `json:"audio,omitempty"`
	Seed         *int   `json:"seed,omitempty"`
}

type providerRequest struct {
	Model      string             `json:"model"`
	Input      providerInput      `json:"input"`
	Parameters providerParameters `json:"parameters"`
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
	Model      *string            `json:"model"`
	Input      *metadataInput     `json:"input"`
	Parameters *metadataParameter `json:"parameters"`
}

type metadataInput struct {
	Prompt         *string  `json:"prompt"`
	ImgURL         *string  `json:"img_url"`
	FirstFrameURL  *string  `json:"first_frame_url"`
	LastFrameURL   *string  `json:"last_frame_url"`
	AudioURL       *string  `json:"audio_url"`
	Media          *[]Media `json:"media"`
	NegativePrompt *string  `json:"negative_prompt"`
	Template       *string  `json:"template"`
}

type metadataParameter struct {
	Resolution   *string `json:"resolution"`
	Size         *string `json:"size"`
	Duration     *int    `json:"duration"`
	PromptExtend *bool   `json:"prompt_extend"`
	Watermark    *bool   `json:"watermark"`
	Audio        *bool   `json:"audio"`
	Seed         *int    `json:"seed"`
}

type PreparedRequest struct {
	Body              []byte
	OriginModel       string
	UpstreamModel     string
	Prompt            string
	Duration          int
	Size              string
	Resolution        string
	HasInputReference bool
}

// RequestedModel extracts and validates the client-facing model.
func RequestedModel(raw []byte, contentType string) (string, error) {
	request, _, err := decodeRequest(raw, contentType)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(request.Model)
	if !IsModel(model) {
		return "", fmt.Errorf("unsupported Alibaba Wan model %q", model)
	}
	return model, nil
}

// PrepareSubmit converts an OpenAI-video-shaped request into the provider payload.
func PrepareSubmit(raw []byte, contentType, originModel, mappedModel string) (*PreparedRequest, error) {
	request, fields, err := decodeRequest(raw, contentType)
	if err != nil {
		return nil, err
	}
	originModel = strings.TrimSpace(originModel)
	mappedModel = strings.TrimSpace(mappedModel)
	if strings.TrimSpace(request.Model) != originModel || !IsModel(originModel) || !IsModel(mappedModel) {
		return nil, errors.New("request or mapped Alibaba Wan model is invalid")
	}
	prompt := strings.TrimSpace(request.Prompt)
	if !validText(prompt, MaxPromptBytes, true) {
		return nil, errors.New("Alibaba Wan prompt must be bounded UTF-8 text")
	}
	duration, err := requestDuration(request.Duration, request.Seconds)
	if err != nil {
		return nil, err
	}
	if duration == 0 {
		duration = DefaultDuration
	}
	promptExtend, watermark := true, false
	payload := providerRequest{
		Model:      mappedModel,
		Input:      providerInput{Prompt: prompt, ImgURL: firstTaskImage(request)},
		Parameters: providerParameters{Duration: duration, PromptExtend: &promptExtend, Watermark: &watermark},
	}
	if err := applyRequestedSize(&payload.Parameters, request.Size, mappedModel); err != nil {
		return nil, err
	}
	metadata, err := parseMetadata(request.Metadata)
	if err != nil {
		return nil, err
	}
	if err := applyMetadata(&payload, metadata, mappedModel); err != nil {
		return nil, err
	}
	if err := normalizeWan27Input(&payload, request); err != nil {
		return nil, err
	}
	if err := validateProviderRequest(payload); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, errors.New("Alibaba Wan request body is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode Alibaba Wan request")
	}
	if len(body) > MaxRequestBodyBytes {
		return nil, common.ErrBodyTooLarge
	}
	return &PreparedRequest{
		Body: body, OriginModel: originModel, UpstreamModel: mappedModel,
		Prompt: payload.Input.Prompt, Duration: payload.Parameters.Duration,
		Size: payload.Parameters.Size, Resolution: payload.Parameters.Resolution,
		HasInputReference: hasInputReference(payload.Input),
	}, nil
}

func decodeRequest(raw []byte, contentType string) (rawRequest, map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return rawRequest{}, nil, errors.New("Alibaba Wan request body is required")
	}
	if len(raw) > MaxRequestBodyBytes {
		return rawRequest{}, nil, common.ErrBodyTooLarge
	}
	if strings.TrimSpace(strings.Split(contentType, ";")[0]) != "application/json" {
		return rawRequest{}, nil, errors.New("Alibaba Wan requests must use application/json")
	}
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil || fields == nil {
		return rawRequest{}, nil, errors.New("invalid Alibaba Wan JSON request")
	}
	allowed := map[string]struct{}{
		"model": {}, "prompt": {}, "size": {}, "duration": {}, "seconds": {},
		"input_reference": {}, "image": {}, "images": {}, "metadata": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return rawRequest{}, nil, fmt.Errorf("unsupported Alibaba Wan request field %q", field)
		}
	}
	var request rawRequest
	if strictJSON(raw, &request) != nil {
		return rawRequest{}, nil, errors.New("invalid Alibaba Wan JSON request")
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
	return 0, errors.New("Alibaba Wan duration must be an integer")
}

func presentJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func applyRequestedSize(parameters *providerParameters, requested, model string) error {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		if strings.Contains(model, "t2v") {
			parameters.Size = "1280*720"
		} else {
			switch model {
			case "wan2.5-i2v-preview", "wan2.2-i2v-plus":
				parameters.Resolution = "1080P"
			case "wan2.2-i2v-flash":
				parameters.Resolution = "720P"
			default:
				parameters.Resolution = "720P"
			}
		}
		return nil
	}
	if strings.Contains(requested, "*") {
		if _, ok := supportedSizes[requested]; !ok {
			return fmt.Errorf("unsupported Alibaba Wan size %q", requested)
		}
		parameters.Size = requested
		return nil
	}
	if strings.Contains(model, "t2v") {
		return errors.New("Alibaba Wan text-to-video size must use WIDTH*HEIGHT")
	}
	resolution := strings.ToUpper(requested)
	if !strings.HasSuffix(resolution, "P") {
		resolution += "P"
	}
	if resolution != "480P" && resolution != "720P" && resolution != "1080P" {
		return fmt.Errorf("unsupported Alibaba Wan resolution %q", requested)
	}
	parameters.Resolution = resolution
	return nil
}

func parseMetadata(raw json.RawMessage) (metadataRequest, error) {
	if !presentJSON(raw) {
		return metadataRequest{}, nil
	}
	if len(raw) > MaxMetadataBytes {
		return metadataRequest{}, errors.New("Alibaba Wan metadata is too large")
	}
	data := bytes.TrimSpace(raw)
	if len(data) > 0 && data[0] == '"' {
		var encoded string
		if json.Unmarshal(data, &encoded) != nil || len(encoded) > MaxMetadataBytes {
			return metadataRequest{}, errors.New("invalid Alibaba Wan metadata")
		}
		data = []byte(encoded)
	}
	var fields map[string]json.RawMessage
	if strictJSON(data, &fields) != nil || fields == nil {
		return metadataRequest{}, errors.New("invalid Alibaba Wan metadata")
	}
	for field := range fields {
		if field != "model" && field != "input" && field != "parameters" {
			return metadataRequest{}, fmt.Errorf("unsupported Alibaba Wan metadata field %q", field)
		}
	}
	var metadata metadataRequest
	if strictJSON(data, &metadata) != nil {
		return metadataRequest{}, errors.New("invalid Alibaba Wan metadata")
	}
	if err := validateMetadataFields(fields); err != nil {
		return metadataRequest{}, err
	}
	return metadata, nil
}

func validateMetadataFields(fields map[string]json.RawMessage) error {
	allowedInput := map[string]struct{}{
		"prompt": {}, "img_url": {}, "first_frame_url": {}, "last_frame_url": {},
		"audio_url": {}, "media": {}, "negative_prompt": {}, "template": {},
	}
	allowedParameters := map[string]struct{}{
		"resolution": {}, "size": {}, "duration": {}, "prompt_extend": {},
		"watermark": {}, "audio": {}, "seed": {},
	}
	for name, allowed := range map[string]map[string]struct{}{"input": allowedInput, "parameters": allowedParameters} {
		raw, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var nested map[string]json.RawMessage
		if strictJSON(raw, &nested) != nil || nested == nil {
			return fmt.Errorf("Alibaba Wan metadata %s must be an object", name)
		}
		for field := range nested {
			if _, ok := allowed[field]; !ok {
				return fmt.Errorf("unsupported Alibaba Wan metadata %s field %q", name, field)
			}
		}
	}
	return nil
}

func applyMetadata(payload *providerRequest, metadata metadataRequest, mappedModel string) error {
	if metadata.Model != nil && strings.TrimSpace(*metadata.Model) != mappedModel {
		return errors.New("Alibaba Wan metadata cannot change the mapped model")
	}
	if input := metadata.Input; input != nil {
		setString(&payload.Input.Prompt, input.Prompt)
		setString(&payload.Input.ImgURL, input.ImgURL)
		setString(&payload.Input.FirstFrameURL, input.FirstFrameURL)
		setString(&payload.Input.LastFrameURL, input.LastFrameURL)
		setString(&payload.Input.AudioURL, input.AudioURL)
		setString(&payload.Input.NegativePrompt, input.NegativePrompt)
		setString(&payload.Input.Template, input.Template)
		if input.Media != nil {
			payload.Input.Media = append([]Media(nil), (*input.Media)...)
		}
	}
	if parameters := metadata.Parameters; parameters != nil {
		setString(&payload.Parameters.Resolution, parameters.Resolution)
		setString(&payload.Parameters.Size, parameters.Size)
		if parameters.Duration != nil {
			payload.Parameters.Duration = *parameters.Duration
		}
		if parameters.PromptExtend != nil {
			payload.Parameters.PromptExtend = parameters.PromptExtend
		}
		if parameters.Watermark != nil {
			payload.Parameters.Watermark = parameters.Watermark
		}
		if parameters.Audio != nil {
			payload.Parameters.Audio = parameters.Audio
		}
		if parameters.Seed != nil {
			payload.Parameters.Seed = parameters.Seed
		}
	}
	return nil
}

func setString(destination *string, value *string) {
	if value != nil {
		*destination = strings.TrimSpace(*value)
	}
}

func firstTaskImage(request rawRequest) string {
	if image := strings.TrimSpace(request.Image); image != "" {
		return image
	}
	for _, image := range request.Images {
		if image = strings.TrimSpace(image); image != "" {
			return image
		}
	}
	return strings.TrimSpace(request.InputReference)
}

func secondTaskImage(request rawRequest) string {
	count := 0
	for _, image := range request.Images {
		if image = strings.TrimSpace(image); image != "" {
			count++
			if count == 2 {
				return image
			}
		}
	}
	return ""
}

func normalizeWan27Input(payload *providerRequest, request rawRequest) error {
	if payload.Model != "wan2.7-i2v" {
		return nil
	}
	if len(payload.Input.Media) == 0 {
		first := firstNonEmpty(payload.Input.FirstFrameURL, payload.Input.ImgURL, firstTaskImage(request))
		last := firstNonEmpty(payload.Input.LastFrameURL, secondTaskImage(request))
		if first != "" {
			payload.Input.Media = append(payload.Input.Media, Media{Type: "first_frame", URL: first})
		}
		if last != "" {
			payload.Input.Media = append(payload.Input.Media, Media{Type: "last_frame", URL: last})
		}
		if payload.Input.AudioURL != "" {
			payload.Input.Media = append(payload.Input.Media, Media{Type: "driving_audio", URL: payload.Input.AudioURL})
		}
	}
	if len(payload.Input.Media) == 0 {
		return errors.New("wan2.7-i2v requires image, images, input_reference, or input.media")
	}
	payload.Input.ImgURL = ""
	payload.Input.FirstFrameURL = ""
	payload.Input.LastFrameURL = ""
	payload.Input.AudioURL = ""
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func validateProviderRequest(request providerRequest) error {
	if !IsModel(request.Model) || !validText(request.Input.Prompt, MaxPromptBytes, true) {
		return errors.New("Alibaba Wan request model or prompt is invalid")
	}
	if strings.Contains(request.Model, "t2v") && request.Input.Prompt == "" {
		return errors.New("Alibaba Wan text-to-video prompt is required")
	}
	if request.Parameters.Duration < MinDuration || request.Parameters.Duration > MaxDuration {
		return fmt.Errorf("Alibaba Wan duration must be between %d and %d", MinDuration, MaxDuration)
	}
	if request.Parameters.Size != "" && request.Parameters.Resolution != "" {
		return errors.New("Alibaba Wan size and resolution must not both be set")
	}
	if request.Parameters.Size != "" {
		if _, ok := supportedSizes[request.Parameters.Size]; !ok {
			return errors.New("Alibaba Wan size is unsupported")
		}
	} else if request.Parameters.Resolution != "480P" && request.Parameters.Resolution != "720P" && request.Parameters.Resolution != "1080P" {
		return errors.New("Alibaba Wan resolution is unsupported")
	}
	if request.Parameters.Seed != nil && (*request.Parameters.Seed < 0 || *request.Parameters.Seed > 2147483647) {
		return errors.New("Alibaba Wan seed is out of range")
	}
	for _, value := range []string{request.Input.NegativePrompt, request.Input.Template} {
		if !validText(value, MaxPromptBytes, true) {
			return errors.New("Alibaba Wan input text is invalid")
		}
	}
	for _, value := range []string{request.Input.ImgURL, request.Input.FirstFrameURL, request.Input.LastFrameURL} {
		if value != "" && !validMediaReference(value, true) {
			return errors.New("Alibaba Wan image reference is invalid")
		}
	}
	if request.Input.AudioURL != "" && !validMediaReference(request.Input.AudioURL, false) {
		return errors.New("Alibaba Wan audio reference is invalid")
	}
	if len(request.Input.Media) > 3 {
		return errors.New("Alibaba Wan media contains too many entries")
	}
	seen := make(map[string]struct{}, len(request.Input.Media))
	for _, media := range request.Input.Media {
		if media.Type != "first_frame" && media.Type != "last_frame" && media.Type != "driving_audio" && media.Type != "first_clip" {
			return fmt.Errorf("unsupported Alibaba Wan media type %q", media.Type)
		}
		if _, duplicate := seen[media.Type]; duplicate {
			return fmt.Errorf("duplicate Alibaba Wan media type %q", media.Type)
		}
		seen[media.Type] = struct{}{}
		if !validMediaReference(media.URL, media.Type != "driving_audio" && media.Type != "first_clip") {
			return errors.New("Alibaba Wan media reference is invalid")
		}
	}
	if strings.Contains(request.Model, "i2v") && !hasInputReference(request.Input) {
		return errors.New("Alibaba Wan image-to-video input is required")
	}
	return nil
}

func hasInputReference(input providerInput) bool {
	return input.ImgURL != "" || input.FirstFrameURL != "" || input.LastFrameURL != "" || len(input.Media) > 0
}

// ResolutionMultiplier reproduces the reference's model-specific task pricing
// adjustment. Models without a distinct resolution price use one.
func ResolutionMultiplier(model, resolution string) float64 {
	resolution = strings.ToUpper(strings.TrimSpace(resolution))
	if mapped, ok := supportedSizes[resolution]; ok {
		resolution = mapped
	}
	switch model {
	case "wan2.5-i2v-preview":
		switch resolution {
		case "720P":
			return 2
		case "1080P":
			return 1 / 0.3
		}
	case "wan2.2-i2v-plus":
		if resolution == "1080P" {
			return 0.7 / 0.14
		}
	case "wan2.2-i2v-flash":
		if resolution == "720P" {
			return 2
		}
	}
	return 1
}

func validMediaReference(value string, image bool) bool {
	if !validText(value, MaxMediaReferenceBytes, false) {
		return false
	}
	if strings.HasPrefix(value, "data:") {
		prefixOK := strings.HasPrefix(value, "data:image/")
		if !image {
			prefixOK = prefixOK || strings.HasPrefix(value, "data:audio/")
		}
		return prefixOK && strings.Contains(value, ";base64,")
	}
	return validHTTPURL(value, MaxMediaReferenceBytes)
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
	ResultURL      string
}

type providerOutput struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
	VideoURL   string `json:"video_url"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type providerResponse struct {
	Output    providerOutput `json:"output"`
	RequestID string         `json:"request_id"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
}

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Alibaba Wan request failed"
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
		return "", errors.New("Alibaba Wan base URL is too large")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("Alibaba Wan base URL is invalid")
	}
	if parsed.Scheme != "https" && !(common.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("Alibaba Wan base URL must use HTTPS")
	}
	return base, nil
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Task, []byte, error) {
	if prepared == nil || len(prepared.Body) == 0 || !validCredential(apiKey) {
		return nil, nil, &RequestError{Err: errors.New("invalid Alibaba Wan submit parameters")}
	}
	endpoint, err := endpointURL(baseURL, SubmitPath)
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
		return nil, nil, &RequestError{Err: fmt.Errorf("Alibaba Wan submit transport failed: %w", err), Dispatched: true}
	}
	defer response.Body.Close()
	raw, readErr := readResponseBody(response)
	if readErr != nil {
		return nil, raw, &RequestError{Err: readErr, Dispatched: true}
	}
	var parsed providerResponse
	if strictJSON(raw, &parsed) != nil {
		return nil, raw, &RequestError{Err: errors.New("invalid Alibaba Wan submit response"), Dispatched: true}
	}
	if parsed.Code != "" || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if parsed.Code != "" {
			return &Task{Status: StatusFailed, ErrorCode: boundedCode(parsed.Code), ErrorMessage: boundedMessage(parsed.Message)}, raw, nil
		}
		return nil, raw, &RequestError{Err: &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}, Dispatched: true}
	}
	if !validIdentifier(parsed.Output.TaskID, MaxProviderTaskIDBytes) {
		return nil, raw, &RequestError{Err: errors.New("invalid Alibaba Wan submit task id"), Dispatched: true}
	}
	status, err := parseStatus(parsed.Output.TaskStatus)
	if err != nil {
		return nil, raw, &RequestError{Err: err, Dispatched: true}
	}
	return &Task{ProviderTaskID: parsed.Output.TaskID, Status: status}, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey, providerTaskID string) (*Task, []byte, error) {
	if !validCredential(apiKey) || !validIdentifier(providerTaskID, MaxProviderTaskIDBytes) {
		return nil, nil, errors.New("invalid Alibaba Wan fetch parameters")
	}
	endpoint, err := endpointURL(baseURL, FetchPathPrefix+url.PathEscape(providerTaskID))
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
		return nil, nil, fmt.Errorf("Alibaba Wan fetch transport failed: %w", err)
	}
	defer response.Body.Close()
	raw, readErr := readResponseBody(response)
	if readErr != nil {
		return nil, raw, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, raw, &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(raw)}
	}
	var parsed providerResponse
	if strictJSON(raw, &parsed) != nil {
		return nil, raw, errors.New("invalid Alibaba Wan fetch response")
	}
	if parsed.Output.TaskID != "" && parsed.Output.TaskID != providerTaskID {
		return nil, raw, errors.New("Alibaba Wan fetch task id does not match the requested task")
	}
	if parsed.Code != "" {
		return &Task{ProviderTaskID: providerTaskID, Status: StatusFailed,
			ErrorCode: boundedCode(parsed.Code), ErrorMessage: boundedMessage(parsed.Message)}, raw, nil
	}
	status, err := parseStatus(parsed.Output.TaskStatus)
	if err != nil {
		return nil, raw, err
	}
	task := &Task{ProviderTaskID: providerTaskID, Status: status}
	if status == StatusFailed {
		task.ErrorCode = boundedCode(parsed.Output.Code)
		task.ErrorMessage = boundedMessage(firstNonEmpty(parsed.Message, parsed.Output.Message))
	}
	if status == StatusSucceeded {
		if !validHTTPURL(parsed.Output.VideoURL, MaxResultURLBytes) {
			return nil, raw, errors.New("successful Alibaba Wan response has an invalid video URL")
		}
		task.ResultURL = parsed.Output.VideoURL
	}
	return task, raw, nil
}

func endpointURL(baseURL, path string) (string, error) {
	base, err := EffectiveBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base + path)
	if err != nil {
		return "", errors.New("build Alibaba Wan request URL")
	}
	return parsed.String(), nil
}

func setHeaders(request *http.Request, apiKey string, content bool) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	if content {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-DashScope-Async", "enable")
	}
}

func readResponseBody(response *http.Response) ([]byte, error) {
	if response == nil {
		return nil, errors.New("Alibaba Wan response is nil")
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

func parseStatus(status string) (TaskStatus, error) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PENDING":
		return StatusSubmitted, nil
	case "RUNNING":
		return StatusProcessing, nil
	case "SUCCEEDED":
		return StatusSucceeded, nil
	case "FAILED", "CANCELED", "UNKNOWN":
		return StatusFailed, nil
	default:
		return "", fmt.Errorf("unknown Alibaba Wan task status %q", status)
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
		return "Alibaba Wan provider rejected the request"
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
