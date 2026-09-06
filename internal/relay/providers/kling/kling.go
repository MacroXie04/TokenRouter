// Package kling implements the bounded Kling asynchronous video protocol. It
// contains only request conversion, provider authentication, transport, and
// response parsing so the relay lifecycle can persist and account for tasks
// independently.
package kling

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DirectBaseURL = "https://api.klingai.com"

	MaxRequestBodyBytes  int64 = 16 << 20
	MaxResponseBodyBytes int64 = 1 << 20

	MaxPromptRunes          = 2500
	MaxNegativePromptRunes  = 2500
	MaxAssetBytes           = 8 << 20
	MaxDynamicMasks         = 4
	MaxTrajectoriesPerMask  = 20
	MaxModelBytes           = 128
	MaxModeBytes            = 16
	MaxDurationBytes        = 2
	MaxAspectRatioBytes     = 8
	MaxSizeBytes            = 32
	MaxCallbackURLBytes     = 2048
	MaxExternalTaskIDBytes  = 191
	MaxProviderTaskIDBytes  = 191
	MaxCredentialPartBytes  = 512
	MaxRelayAPIKeyBytes     = 4096
	MaxProviderMessageRunes = 512
	MaxProviderCodeBytes    = 64

	DefaultMode        = "std"
	DefaultDuration    = "5"
	DefaultCfgScale    = 0.5
	DefaultAspectRatio = "1:1"
)

// ModelList is the reference Kling model catalog. Callers should treat it as
// read-only.
var ModelList = []string{"kling-v1", "kling-v1-6", "kling-v2-master"}

type Action string

const (
	ActionTextToVideo  Action = "text2video"
	ActionImageToVideo Action = "image2video"
)

type TrajectoryPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type DynamicMask struct {
	Mask         string            `json:"mask,omitempty"`
	Trajectories []TrajectoryPoint `json:"trajectories,omitempty"`
}

type CameraConfig struct {
	Horizontal float64 `json:"horizontal,omitempty"`
	Vertical   float64 `json:"vertical,omitempty"`
	Pan        float64 `json:"pan,omitempty"`
	Tilt       float64 `json:"tilt,omitempty"`
	Roll       float64 `json:"roll,omitempty"`
	Zoom       float64 `json:"zoom,omitempty"`
}

type CameraControl struct {
	Type   string        `json:"type,omitempty"`
	Config *CameraConfig `json:"config,omitempty"`
}

// Payload is the exact JSON object sent to Kling. Both model fields are set
// from the selected channel mapping after all client and metadata fields have
// been merged.
type Payload struct {
	Prompt         string         `json:"prompt,omitempty"`
	Image          string         `json:"image,omitempty"`
	ImageTail      string         `json:"image_tail,omitempty"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Mode           string         `json:"mode"`
	Duration       string         `json:"duration"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	ModelName      string         `json:"model_name"`
	Model          string         `json:"model"`
	CfgScale       float64        `json:"cfg_scale"`
	StaticMask     string         `json:"static_mask,omitempty"`
	DynamicMasks   []DynamicMask  `json:"dynamic_masks,omitempty"`
	CameraControl  *CameraControl `json:"camera_control,omitempty"`
	CallbackURL    string         `json:"callback_url,omitempty"`
	ExternalTaskID string         `json:"external_task_id,omitempty"`
}

type PreparedRequest struct {
	Body          []byte
	Payload       Payload
	Action        Action
	OriginModel   string
	UpstreamModel string
}

type rawRequest struct {
	Prompt         *string         `json:"prompt,omitempty"`
	Model          *string         `json:"model,omitempty"`
	ModelName      *string         `json:"model_name,omitempty"`
	Mode           *string         `json:"mode,omitempty"`
	Image          *string         `json:"image,omitempty"`
	Images         *[]string       `json:"images,omitempty"`
	ImageTail      *string         `json:"image_tail,omitempty"`
	InputReference *string         `json:"input_reference,omitempty"`
	NegativePrompt *string         `json:"negative_prompt,omitempty"`
	Size           *string         `json:"size,omitempty"`
	Duration       json.RawMessage `json:"duration,omitempty"`
	Seconds        json.RawMessage `json:"seconds,omitempty"`
	AspectRatio    *string         `json:"aspect_ratio,omitempty"`
	CfgScale       *float64        `json:"cfg_scale,omitempty"`
	StaticMask     *string         `json:"static_mask,omitempty"`
	DynamicMasks   *[]DynamicMask  `json:"dynamic_masks,omitempty"`
	CameraControl  *CameraControl  `json:"camera_control,omitempty"`
	CallbackURL    *string         `json:"callback_url,omitempty"`
	ExternalTaskID *string         `json:"external_task_id,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

func RequestedModel(raw []byte) (string, error) {
	request, err := decodeRequest(raw, true)
	if err != nil {
		return "", err
	}
	model, err := clientModel(request)
	if err != nil {
		return "", err
	}
	if !supportedModel(model) {
		return "", fmt.Errorf("unsupported Kling model %q", model)
	}
	return model, nil
}

// PrepareSubmit strictly decodes and validates a request, overlays supported
// metadata fields, applies defaults, and finally overwrites both model fields
// with mappedModel. The request body itself determines text vs image action.
func PrepareSubmit(raw []byte, originModel, mappedModel string) (*PreparedRequest, error) {
	request, err := decodeRequest(raw, true)
	if err != nil {
		return nil, err
	}
	originModel = strings.TrimSpace(originModel)
	if !supportedModel(originModel) {
		return nil, fmt.Errorf("unsupported Kling model %q", originModel)
	}
	requestedModel, err := clientModel(request)
	if err != nil {
		return nil, err
	}
	if requestedModel != originModel {
		return nil, errors.New("request model does not match the selected Kling model")
	}
	if err := validateMappedModel(mappedModel); err != nil {
		return nil, err
	}

	if len(request.Metadata) != 0 && string(request.Metadata) != "null" {
		metadata, metadataErr := decodeMetadata(request.Metadata)
		if metadataErr != nil {
			return nil, metadataErr
		}
		mergeRawRequest(request, metadata)
	}

	payload, action, err := buildPayload(request, mappedModel)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode Kling request")
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return nil, httpx.ErrBodyTooLarge
	}
	return &PreparedRequest{
		Body: body, Payload: payload, Action: action,
		OriginModel: originModel, UpstreamModel: mappedModel,
	}, nil
}

func clientModel(request *rawRequest) (string, error) {
	model, modelName := "", ""
	if request != nil && request.Model != nil {
		model = *request.Model
	}
	if request != nil && request.ModelName != nil {
		modelName = *request.ModelName
	}
	if model != "" && modelName != "" && model != modelName {
		return "", errors.New("model and model_name must not conflict")
	}
	if model == "" {
		model = modelName
	}
	if strings.TrimSpace(model) == "" {
		return "", errors.New("model or model_name is required")
	}
	if len(model) > MaxModelBytes || !utf8.ValidString(model) || hasControl(model) {
		return "", errors.New("Kling model is invalid or too large")
	}
	return model, nil
}

func decodeRequest(raw []byte, allowMetadata bool) (*rawRequest, error) {
	if len(raw) == 0 {
		return nil, errors.New("Kling request body is required")
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
		return nil, errors.New("invalid Kling JSON request")
	}
	if !allowMetadata && len(request.Metadata) != 0 {
		return nil, errors.New("nested Kling metadata is not allowed")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return &request, nil
}

func decodeMetadata(raw json.RawMessage) (*rawRequest, error) {
	value := []byte(raw)
	if len(value) > 0 && value[0] == '"' {
		var encoded string
		if err := json.Unmarshal(value, &encoded); err != nil {
			return nil, errors.New("Kling metadata must be a JSON object")
		}
		value = []byte(encoded)
	}
	metadata, err := decodeRequest(value, false)
	if err != nil {
		return nil, fmt.Errorf("invalid Kling metadata: %w", err)
	}
	return metadata, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Kling request contains trailing JSON")
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
		return errors.New("invalid Kling JSON request")
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
				return errors.New("invalid Kling JSON request")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid Kling JSON object")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("Kling JSON field %q must not be repeated", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return errors.New("invalid Kling JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return errors.New("invalid Kling JSON array")
		}
	default:
		return errors.New("invalid Kling JSON request")
	}
	return nil
}

func mergeRawRequest(base, override *rawRequest) {
	if override.Prompt != nil {
		base.Prompt = override.Prompt
	}
	if override.Model != nil {
		base.Model = override.Model
	}
	if override.ModelName != nil {
		base.ModelName = override.ModelName
	}
	if override.Mode != nil {
		base.Mode = override.Mode
	}
	if override.Image != nil {
		base.Image = override.Image
	}
	if override.Images != nil {
		base.Images = override.Images
	}
	if override.ImageTail != nil {
		base.ImageTail = override.ImageTail
	}
	if override.InputReference != nil {
		base.InputReference = override.InputReference
	}
	if override.NegativePrompt != nil {
		base.NegativePrompt = override.NegativePrompt
	}
	if override.Size != nil {
		base.Size = override.Size
	}
	if len(override.Duration) != 0 {
		base.Duration = override.Duration
	}
	if len(override.Seconds) != 0 {
		base.Seconds = override.Seconds
	}
	if override.AspectRatio != nil {
		base.AspectRatio = override.AspectRatio
	}
	if override.CfgScale != nil {
		base.CfgScale = override.CfgScale
	}
	if override.StaticMask != nil {
		base.StaticMask = override.StaticMask
	}
	if override.DynamicMasks != nil {
		base.DynamicMasks = override.DynamicMasks
	}
	if override.CameraControl != nil {
		base.CameraControl = override.CameraControl
	}
	if override.CallbackURL != nil {
		base.CallbackURL = override.CallbackURL
	}
	if override.ExternalTaskID != nil {
		base.ExternalTaskID = override.ExternalTaskID
	}
}

func buildPayload(request *rawRequest, mappedModel string) (Payload, Action, error) {
	payload := Payload{
		Mode: "std", Duration: "5", AspectRatio: DefaultAspectRatio,
		Model: mappedModel, ModelName: mappedModel, CfgScale: DefaultCfgScale,
	}
	if request.Prompt != nil {
		payload.Prompt = *request.Prompt
	}
	if err := validateText("prompt", payload.Prompt, MaxPromptRunes, true); err != nil {
		return Payload{}, "", err
	}
	if request.NegativePrompt != nil {
		payload.NegativePrompt = *request.NegativePrompt
	}
	if err := validateText("negative_prompt", payload.NegativePrompt, MaxNegativePromptRunes, false); err != nil {
		return Payload{}, "", err
	}
	if request.Mode != nil {
		payload.Mode = *request.Mode
	}
	if len(payload.Mode) > MaxModeBytes || (payload.Mode != "std" && payload.Mode != "pro") {
		return Payload{}, "", errors.New("mode must be std or pro")
	}

	duration, err := requestDuration(request.Duration, request.Seconds)
	if err != nil {
		return Payload{}, "", err
	}
	if duration != "" {
		payload.Duration = duration
	}

	if request.CfgScale != nil {
		payload.CfgScale = *request.CfgScale
	}
	if math.IsNaN(payload.CfgScale) || math.IsInf(payload.CfgScale, 0) || payload.CfgScale < 0 || payload.CfgScale > 1 {
		return Payload{}, "", errors.New("cfg_scale must be finite and between 0 and 1")
	}

	if request.Image != nil {
		payload.Image = *request.Image
	}
	if request.Images != nil {
		if len(*request.Images) > 1 {
			return Payload{}, "", errors.New("images must contain at most one image")
		}
		if len(*request.Images) == 1 {
			image := (*request.Images)[0]
			if payload.Image != "" && payload.Image != image {
				return Payload{}, "", errors.New("image and images contain conflicting values")
			}
			payload.Image = image
		}
	}
	if request.InputReference != nil {
		input := *request.InputReference
		if payload.Image != "" && payload.Image != input {
			return Payload{}, "", errors.New("image and input_reference contain conflicting values")
		}
		payload.Image = input
	}
	if request.ImageTail != nil {
		payload.ImageTail = *request.ImageTail
	}
	if err := validateAsset("image", payload.Image, false); err != nil {
		return Payload{}, "", err
	}
	if err := validateAsset("image_tail", payload.ImageTail, false); err != nil {
		return Payload{}, "", err
	}

	sizeRatio := ""
	if request.Size != nil {
		size := *request.Size
		if len(size) > MaxSizeBytes {
			return Payload{}, "", errors.New("size is too large")
		}
		var ok bool
		sizeRatio, ok = aspectRatioForSize(size)
		if !ok {
			return Payload{}, "", fmt.Errorf("unsupported Kling video size %q", size)
		}
		payload.AspectRatio = sizeRatio
	}
	if request.AspectRatio != nil {
		ratio := *request.AspectRatio
		if len(ratio) > MaxAspectRatioBytes || !validAspectRatio(ratio) {
			return Payload{}, "", errors.New("aspect_ratio must be 1:1, 16:9, or 9:16")
		}
		if sizeRatio != "" && sizeRatio != ratio {
			return Payload{}, "", errors.New("size and aspect_ratio conflict")
		}
		payload.AspectRatio = ratio
	}

	if request.StaticMask != nil {
		payload.StaticMask = *request.StaticMask
	}
	if err := validateAsset("static_mask", payload.StaticMask, false); err != nil {
		return Payload{}, "", err
	}
	if request.DynamicMasks != nil {
		payload.DynamicMasks = append([]DynamicMask(nil), (*request.DynamicMasks)...)
	}
	if len(payload.DynamicMasks) > MaxDynamicMasks {
		return Payload{}, "", fmt.Errorf("dynamic_masks must contain at most %d entries", MaxDynamicMasks)
	}
	for index := range payload.DynamicMasks {
		mask := &payload.DynamicMasks[index]
		if err := validateAsset(fmt.Sprintf("dynamic_masks[%d].mask", index), mask.Mask, true); err != nil {
			return Payload{}, "", err
		}
		if len(mask.Trajectories) > MaxTrajectoriesPerMask {
			return Payload{}, "", fmt.Errorf("dynamic_masks[%d].trajectories must contain at most %d points", index, MaxTrajectoriesPerMask)
		}
		for pointIndex, point := range mask.Trajectories {
			if point.X < 0 || point.X > 10000 || point.Y < 0 || point.Y > 10000 {
				return Payload{}, "", fmt.Errorf("dynamic_masks[%d].trajectories[%d] coordinates must be between 0 and 10000", index, pointIndex)
			}
		}
	}

	if request.CameraControl != nil {
		payload.CameraControl = request.CameraControl
		if err := validateCameraControl(payload.CameraControl); err != nil {
			return Payload{}, "", err
		}
	}
	if request.CallbackURL != nil {
		payload.CallbackURL = *request.CallbackURL
	}
	if payload.CallbackURL != "" {
		if len(payload.CallbackURL) > MaxCallbackURLBytes || !validHTTPSURL(payload.CallbackURL) {
			return Payload{}, "", errors.New("callback_url must be a valid HTTPS URL")
		}
	}
	if request.ExternalTaskID != nil {
		payload.ExternalTaskID = *request.ExternalTaskID
	}
	if err := validateIdentifier("external_task_id", payload.ExternalTaskID, MaxExternalTaskIDBytes, false); err != nil {
		return Payload{}, "", err
	}

	action := ActionTextToVideo
	if payload.Image != "" || payload.ImageTail != "" {
		action = ActionImageToVideo
	}
	return payload, action, nil
}

func requestDuration(duration, seconds json.RawMessage) (string, error) {
	if len(duration) != 0 && string(duration) != "null" && len(seconds) != 0 && string(seconds) != "null" {
		return "", errors.New("duration and seconds must not both be provided")
	}
	raw := duration
	if len(raw) == 0 || string(raw) == "null" {
		raw = seconds
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		var number int
		if numberErr := json.Unmarshal(raw, &number); numberErr != nil {
			return "", errors.New("duration must be 5 or 10")
		}
		value = strconv.Itoa(number)
	}
	if len(value) > MaxDurationBytes || (value != "5" && value != "10") {
		return "", errors.New("duration must be 5 or 10")
	}
	return value, nil
}

func validateMappedModel(model string) error {
	if model == "" || strings.TrimSpace(model) != model || len(model) > MaxModelBytes || !utf8.ValidString(model) || hasControl(model) {
		return errors.New("mapped Kling model is invalid or too large")
	}
	return nil
}

func supportedModel(model string) bool {
	for _, candidate := range ModelList {
		if model == candidate {
			return true
		}
	}
	return false
}

func validateText(name, value string, maxRunes int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes || hasForbiddenTextControl(value) {
		return fmt.Errorf("%s is invalid or exceeds %d characters", name, maxRunes)
	}
	return nil
}

func hasForbiddenTextControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func validateAsset(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if len(value) > MaxAssetBytes || !utf8.ValidString(value) || hasControl(value) {
		return fmt.Errorf("%s is invalid or too large", name)
	}
	if strings.HasPrefix(value, "https://") {
		if !validHTTPSURL(value) {
			return fmt.Errorf("%s must contain a valid HTTPS URL or base64 image", name)
		}
		return nil
	}
	encoded := value
	if strings.HasPrefix(value, "data:") {
		comma := strings.IndexByte(value, ',')
		if comma < 0 || comma > 128 {
			return fmt.Errorf("%s contains an invalid data URL", name)
		}
		header := strings.ToLower(value[:comma])
		if !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
			return fmt.Errorf("%s contains an invalid image data URL", name)
		}
		encoded = value[comma+1:]
	}
	if encoded == "" {
		return fmt.Errorf("%s contains empty base64 data", name)
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return fmt.Errorf("%s must contain a valid HTTPS URL or base64 image", name)
	}
	return nil
}

func validateCameraControl(control *CameraControl) error {
	if control == nil {
		return nil
	}
	if len(control.Type) > 64 || !utf8.ValidString(control.Type) || hasControl(control.Type) {
		return errors.New("camera_control.type is invalid or too large")
	}
	if control.Config == nil {
		return nil
	}
	values := []float64{
		control.Config.Horizontal, control.Config.Vertical, control.Config.Pan,
		control.Config.Tilt, control.Config.Roll, control.Config.Zoom,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < -10 || value > 10 {
			return errors.New("camera_control config values must be finite and between -10 and 10")
		}
	}
	return nil
}

func aspectRatioForSize(size string) (string, bool) {
	switch size {
	case "512x512", "1024x1024":
		return "1:1", true
	case "1280x720", "1920x1080":
		return "16:9", true
	case "720x1280", "1080x1920":
		return "9:16", true
	default:
		return "", false
	}
}

func validAspectRatio(value string) bool {
	return value == "1:1" || value == "16:9" || value == "9:16"
}

func validHTTPSURL(value string) bool {
	if value == "" || hasControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validateIdentifier(name, value string, maxBytes int, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || hasControl(value) {
		return fmt.Errorf("%s is invalid or too large", name)
	}
	for _, character := range value {
		if unicode.IsSpace(character) || character == '/' || character == '\\' || character == '?' || character == '#' {
			return fmt.Errorf("%s contains unsupported characters", name)
		}
	}
	return nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// TaskStatus is the normalized Kling task state. Its values intentionally
// match the provider spellings while preventing unknown states from entering
// a durable lifecycle.
type TaskStatus string

const (
	StatusSubmitted  TaskStatus = "submitted"
	StatusProcessing TaskStatus = "processing"
	StatusSucceeded  TaskStatus = "succeed"
	StatusFailed     TaskStatus = "failed"
)

type Task struct {
	ProviderTaskID  string
	Status          TaskStatus
	StatusMessage   string
	ResultURL       string
	CompletionUnits int
	CreatedAt       int64
	UpdatedAt       int64
}

type providerEnvelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Data      struct {
		TaskID        string `json:"task_id"`
		TaskStatus    string `json:"task_status"`
		TaskStatusMsg string `json:"task_status_msg"`
		TaskResult    struct {
			Videos []struct {
				ID           string `json:"id"`
				URL          string `json:"url"`
				WatermarkURL string `json:"watermark_url"`
				Duration     string `json:"duration"`
			} `json:"videos"`
		} `json:"task_result"`
		CreatedAt          int64  `json:"created_at"`
		UpdatedAt          int64  `json:"updated_at"`
		FinalUnitDeduction string `json:"final_unit_deduction"`
	} `json:"data"`
}

// ProviderError deliberately omits the raw provider body from Error. The
// bounded, normalized Code and Message fields may be used for a client-facing
// response without leaking headers or unparsed upstream content.
type ProviderError struct {
	StatusCode int
	Code       string
	Message    string
	Cause      error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "Kling provider request failed"
	}
	if e.Cause != nil {
		return fmt.Sprintf("Kling provider returned status %d (%T)", e.StatusCode, e.Cause)
	}
	return fmt.Sprintf("Kling provider returned status %d", e.StatusCode)
}

func (e *ProviderError) Unwrap() error { return e.Cause }

type RequestError struct {
	Err        error
	Dispatched bool
}

func (e *RequestError) Error() string {
	if e == nil || e.Err == nil {
		return "Kling request failed"
	}
	return e.Err.Error()
}

func (e *RequestError) Unwrap() error { return e.Err }

func SubmitWasDispatched(err error) bool {
	var requestErr *RequestError
	return errors.As(err, &requestErr) && requestErr.Dispatched
}

// ParseFinalUnitDeduction rounds a non-negative provider unit value upward and
// saturates it at the database quota limit. Empty input means no final unit was
// supplied and is valid.
func ParseFinalUnitDeduction(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	if len(raw) > 64 {
		return 0, false
	}
	if raw[0] == '-' {
		return 0, false
	}
	if raw[0] == '+' {
		raw = raw[1:]
		if raw == "" {
			return 0, false
		}
	}
	mantissa, exponentText := raw, ""
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		if strings.IndexAny(raw[index+1:], "eE") >= 0 {
			return 0, false
		}
		mantissa, exponentText = raw[:index], raw[index+1:]
		if exponentText == "" {
			return 0, false
		}
	}
	exponent := int64(0)
	if exponentText != "" {
		exponentDigits := exponentText
		negativeExponent := false
		if exponentDigits[0] == '+' || exponentDigits[0] == '-' {
			negativeExponent = exponentDigits[0] == '-'
			exponentDigits = exponentDigits[1:]
		}
		if exponentDigits == "" {
			return 0, false
		}
		for _, character := range exponentDigits {
			if character < '0' || character > '9' {
				return 0, false
			}
		}
		parsed, err := strconv.ParseInt(exponentText, 10, 64)
		if err != nil {
			// A non-zero value with an out-of-range positive exponent is
			// necessarily above MaxQuota; a negative one is between 0 and 1.
			if negativeExponent {
				exponent = math.MinInt64
			} else {
				exponent = math.MaxInt64
			}
		} else {
			exponent = parsed
		}
	}

	dot := strings.IndexByte(mantissa, '.')
	if dot >= 0 && strings.IndexByte(mantissa[dot+1:], '.') >= 0 {
		return 0, false
	}
	digits := mantissa
	fractionDigits := int64(0)
	if dot >= 0 {
		digits = mantissa[:dot] + mantissa[dot+1:]
		fractionDigits = int64(len(mantissa) - dot - 1)
	}
	if digits == "" {
		return 0, false
	}
	for _, character := range digits {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	significant := strings.TrimLeft(digits, "0")
	if significant == "" {
		return 0, true
	}

	if exponent == math.MaxInt64 {
		return int(quotamath.MaxQuota), true
	}
	if exponent == math.MinInt64 {
		return 1, true
	}
	if exponent > math.MaxInt64-fractionDigits {
		return int(quotamath.MaxQuota), true
	}
	if exponent < math.MinInt64+fractionDigits {
		return 1, true
	}
	scale := exponent - fractionDigits
	if scale > int64(len(strconv.FormatInt(quotamath.MaxQuota, 10))) {
		return int(quotamath.MaxQuota), true
	}
	if scale < -int64(len(significant)) {
		return 1, true
	}

	integerDigits := int64(len(significant)) + scale
	if integerDigits <= 0 {
		return 1, true
	}
	maxText := strconv.FormatInt(quotamath.MaxQuota, 10)
	if integerDigits > int64(len(maxText)) {
		return int(quotamath.MaxQuota), true
	}
	integerText := significant
	fractionalNonZero := false
	if scale >= 0 {
		integerText += strings.Repeat("0", int(scale))
	} else {
		cut := int(integerDigits)
		integerText = significant[:cut]
		fractionalNonZero = strings.TrimRight(significant[cut:], "0") != ""
	}
	integerValue, err := strconv.ParseInt(integerText, 10, 64)
	if err != nil || integerValue >= quotamath.MaxQuota {
		return int(quotamath.MaxQuota), true
	}
	if fractionalNonZero {
		integerValue++
	}
	return int(integerValue), true
}

// ParseTaskResponse parses a bounded provider response. expectedTaskID is
// empty for submit and required by callers for fetch identity verification.
func ParseTaskResponse(response *http.Response, expectedTaskID string) (*Task, []byte, error) {
	if response == nil {
		return nil, nil, errors.New("Kling response is nil")
	}
	limit := MaxResponseBodyBytes
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	raw, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, nil, &ProviderError{StatusCode: response.StatusCode, Code: "response_too_large", Message: "Kling response exceeded the configured limit", Cause: err}
	}
	var envelope providerEnvelope
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = decodeProviderEnvelope(raw, &envelope)
		return nil, raw, providerFailure(response.StatusCode, envelope.Code, envelope.Message)
	}
	if err := decodeProviderEnvelope(raw, &envelope); err != nil {
		return nil, raw, errors.New("invalid Kling task response")
	}
	if envelope.Code != 0 {
		return nil, raw, providerFailure(response.StatusCode, envelope.Code, envelope.Message)
	}
	taskID := envelope.Data.TaskID
	if err := validateIdentifier("provider task_id", taskID, MaxProviderTaskIDBytes, true); err != nil {
		return nil, raw, errors.New("Kling task response is missing a valid data.task_id")
	}
	if expectedTaskID != "" && taskID != expectedTaskID {
		return nil, raw, errors.New("Kling fetch response task id does not match the requested task")
	}
	status, ok := normalizeStatus(envelope.Data.TaskStatus)
	if !ok {
		return nil, raw, errors.New("unknown Kling task status")
	}
	units, ok := ParseFinalUnitDeduction(envelope.Data.FinalUnitDeduction)
	if !ok {
		return nil, raw, errors.New("Kling task response contains invalid final_unit_deduction")
	}
	resultURL := ""
	if len(envelope.Data.TaskResult.Videos) > 0 {
		resultURL = envelope.Data.TaskResult.Videos[0].URL
		if resultURL != "" && (len(resultURL) > MaxCallbackURLBytes || !validHTTPSURL(resultURL)) {
			return nil, raw, errors.New("Kling task response contains an invalid video URL")
		}
	}
	return &Task{
		ProviderTaskID:  taskID,
		Status:          status,
		StatusMessage:   sanitizeProviderText(envelope.Data.TaskStatusMsg),
		ResultURL:       resultURL,
		CompletionUnits: units,
		CreatedAt:       envelope.Data.CreatedAt,
		UpdatedAt:       envelope.Data.UpdatedAt,
	}, raw, nil
}

func decodeProviderEnvelope(raw []byte, envelope *providerEnvelope) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(envelope); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func normalizeStatus(value string) (TaskStatus, bool) {
	switch value {
	case string(StatusSubmitted):
		return StatusSubmitted, true
	case string(StatusProcessing):
		return StatusProcessing, true
	case string(StatusSucceeded):
		return StatusSucceeded, true
	case string(StatusFailed):
		return StatusFailed, true
	default:
		return "", false
	}
}

func providerFailure(statusCode, providerCode int, message string) error {
	code := strconv.Itoa(providerCode)
	if providerCode == 0 {
		code = "provider_error"
	}
	code = sanitizeProviderCode(code)
	message = sanitizeProviderText(message)
	if message == "" {
		message = "Kling provider rejected the request"
	}
	return &ProviderError{StatusCode: statusCode, Code: code, Message: message}
}

func sanitizeProviderCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxProviderCodeBytes {
		return "provider_error"
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "provider_error"
		}
	}
	return value
}

func sanitizeProviderText(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	count := 0
	lastWasSpace := false
	for _, character := range value {
		if count >= MaxProviderMessageRunes {
			break
		}
		if unicode.IsControl(character) {
			if builder.Len() > 0 && !lastWasSpace {
				builder.WriteByte(' ')
				lastWasSpace = true
			}
			continue
		}
		builder.WriteRune(character)
		lastWasSpace = unicode.IsSpace(character)
		count++
	}
	return strings.TrimSpace(builder.String())
}

// Client uses a no-proxy, no-redirect, SSRF-checked transport with bounded
// headers and total duration.
type Client struct {
	HTTPClient *http.Client
	Now        func() time.Time
}

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

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// EffectiveBaseURL fixes direct Kling traffic to the official origin. Relay
// mode (sk- keys) accepts a configured HTTPS origin and adds /kling per request.
func EffectiveBaseURL(configured, apiKey string) (string, bool, error) {
	relay := isRelayKey(apiKey)
	configured = strings.TrimRight(strings.TrimSpace(configured), "/")
	if !relay {
		if configured != "" && configured != DirectBaseURL {
			return "", false, errors.New("direct Kling channels must use https://api.klingai.com")
		}
		return DirectBaseURL, false, nil
	}
	if configured == "" || len(configured) > 4096 {
		return "", true, errors.New("Kling relay base URL is missing or too large")
	}
	parsed, err := url.Parse(configured)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", true, errors.New("Kling relay base URL must be a valid HTTPS URL")
	}
	if hasControl(configured) {
		return "", true, errors.New("Kling relay base URL is invalid")
	}
	return configured, true, nil
}

// AuthorizationToken returns a raw sk- relay token or a direct HS256 JWT.
func AuthorizationToken(apiKey string, now time.Time) (string, error) {
	if isRelayKey(apiKey) {
		if len(apiKey) <= len("sk-") || len(apiKey) > MaxRelayAPIKeyBytes || strings.TrimSpace(apiKey) != apiKey || hasControl(apiKey) {
			return "", errors.New("invalid Kling relay API key")
		}
		return apiKey, nil
	}
	if strings.Count(apiKey, "|") != 1 {
		return "", errors.New("invalid Kling API key: required format is accessKey|secretKey")
	}
	parts := strings.SplitN(apiKey, "|", 2)
	accessKey, secretKey := parts[0], parts[1]
	if accessKey == "" || secretKey == "" || len(accessKey) > MaxCredentialPartBytes || len(secretKey) > MaxCredentialPartBytes ||
		strings.TrimSpace(accessKey) != accessKey || strings.TrimSpace(secretKey) != secretKey ||
		hasControl(accessKey) || hasControl(secretKey) {
		return "", errors.New("invalid Kling API key: required format is accessKey|secretKey")
	}
	claims := jwt.MapClaims{
		"iss": accessKey,
		"exp": now.Unix() + 1800,
		"nbf": now.Unix() - 5,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["typ"] = "JWT"
	signed, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", errors.New("sign Kling JWT")
	}
	return signed, nil
}

func isRelayKey(apiKey string) bool {
	return strings.HasPrefix(apiKey, "sk-")
}

func (c *Client) Submit(ctx context.Context, baseURL, apiKey string, prepared *PreparedRequest) (*Task, []byte, error) {
	if prepared == nil || len(prepared.Body) == 0 || int64(len(prepared.Body)) > MaxRequestBodyBytes ||
		(prepared.Action != ActionTextToVideo && prepared.Action != ActionImageToVideo) {
		return nil, nil, &RequestError{Err: errors.New("invalid Kling submit parameters")}
	}
	endpoint, err := endpointURL(baseURL, apiKey, prepared.Action, "")
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	token, err := AuthorizationToken(apiKey, c.now())
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(prepared.Body))
	if err != nil {
		return nil, nil, &RequestError{Err: err}
	}
	setHeaders(request, token)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("Kling submit transport failed: %w", err), Dispatched: true}
	}
	defer response.Body.Close()
	task, raw, err := ParseTaskResponse(response, "")
	if err != nil {
		return nil, raw, &RequestError{Err: err, Dispatched: true}
	}
	return task, raw, nil
}

func (c *Client) Fetch(ctx context.Context, baseURL, apiKey string, action Action, providerTaskID string) (*Task, []byte, error) {
	if action != ActionTextToVideo && action != ActionImageToVideo {
		return nil, nil, errors.New("invalid Kling fetch action")
	}
	if err := validateIdentifier("provider task_id", providerTaskID, MaxProviderTaskIDBytes, true); err != nil {
		return nil, nil, err
	}
	endpoint, err := endpointURL(baseURL, apiKey, action, providerTaskID)
	if err != nil {
		return nil, nil, err
	}
	token, err := AuthorizationToken(apiKey, c.now())
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	setHeaders(request, token)
	response, err := c.client().Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("Kling fetch transport failed: %w", err)
	}
	defer response.Body.Close()
	return ParseTaskResponse(response, providerTaskID)
}

func endpointURL(baseURL, apiKey string, action Action, providerTaskID string) (string, error) {
	base, relay, err := EffectiveBaseURL(baseURL, apiKey)
	if err != nil {
		return "", err
	}
	path := "/v1/videos/" + string(action)
	if relay {
		path = "/kling" + path
	}
	if providerTaskID != "" {
		path += "/" + url.PathEscape(providerTaskID)
	}
	return strings.TrimRight(base, "/") + path, nil
}

func setHeaders(request *http.Request, token string) {
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "kling-sdk/1.0")
}
