// Package replicate implements Replicate's synchronous prediction boundary for
// OpenAI-compatible image generation and image edits.
package replicate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName                = "replicate"
	DefaultModel               = "black-forest-labs/flux-1.1-pro"
	defaultBaseURL             = "https://api.replicate.com"
	maxBaseURLBytes            = 4 << 10
	maxCredentialBytes         = 16 << 10
	maxModelSegmentBytes       = 128
	maxPromptBytes             = 100 << 10
	maxImageCount              = 8
	maxProviderOptionBytes     = 1 << 20
	maxProviderOptionNodes     = 100_000
	maxProviderOptionDepth     = 32
	maxProviderOptionKeyBytes  = 256
	maxProviderOptionTextBytes = 1 << 20
	maxProviderOptionNumber    = 1e12
	maxJSONRequestBytes        = 1 << 20
	maxEditRequestBytes        = 10 << 20
)

var (
	supportedModels     = [...]string{DefaultModel}
	modelSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ModelList returns an owned copy of the reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

type Adaptor struct {
	mode   constant.RelayMode
	format constant.RelayFormat
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base, err := validatedBaseURL(meta.BaseURL)
	if err != nil {
		return "", err
	}
	owner, modelName, err := splitModel(meta.ModelName)
	if err != nil {
		return "", err
	}
	path := "/v1/models/" + url.PathEscape(owner) + "/" + url.PathEscape(modelName) + "/predictions"
	return relaycommon.JoinURL(base, path), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Replicate request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	credential, err := validatedCredential(meta.APIKey)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Prefer", "wait")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Del("x-api-key")
	request.Header.Del("x-goog-api-key")
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	mode := a.relayMode(meta)
	if mode == constant.RelayModeImagesGenerations {
		if !isJSON(meta.RequestContentType) {
			return nil, errors.New("Replicate image generation requires application/json")
		}
		if len(meta.RawBody) > maxJSONRequestBytes {
			return nil, fmt.Errorf("Replicate JSON request exceeds %d bytes", maxJSONRequestBytes)
		}
		if err := rejectDuplicateJSONKeys(meta.RawBody); err != nil && len(bytes.TrimSpace(meta.RawBody)) != 0 {
			return nil, errors.New("Replicate request contains invalid or duplicate JSON fields")
		}
	}
	if mode == constant.RelayModeImagesEdits && !isMultipart(meta.RequestContentType) {
		return nil, errors.New("Replicate image edits require multipart/form-data")
	}
	if mode == constant.RelayModeImagesEdits && len(meta.RawBody) > maxEditRequestBytes {
		return nil, fmt.Errorf("Replicate image edit request exceeds %d bytes", maxEditRequestBytes)
	}

	input, err := buildPredictionInput(meta)
	if err != nil {
		return nil, err
	}
	if err := validatePredictionInput(input, mode); err != nil {
		return nil, err
	}
	preUploadPayload := map[string]any{"input": input}
	if err := validateProviderValue(preUploadPayload, 0, new(int)); err != nil {
		return nil, fmt.Errorf("Replicate prediction input is invalid: %w", err)
	}
	body, err := protocolkit.MarshalJSON(preUploadPayload)
	if err != nil {
		return nil, errors.New("encode Replicate prediction request")
	}
	requestLimit := maxProviderOptionBytes
	if mode == constant.RelayModeImagesEdits {
		// A validated upload URL can add up to maxOutputURLBytes after this
		// deterministic preflight. Reserving headroom prevents a malformed
		// options object from causing an upload and then failing locally.
		requestLimit -= maxOutputURLBytes + 256
	}
	if len(body) > requestLimit {
		return nil, fmt.Errorf("Replicate prediction input exceeds %d bytes", maxProviderOptionBytes)
	}
	if mode == constant.RelayModeImagesGenerations {
		return body, nil
	}

	imageURL, err := uploadMultipartImage(meta)
	if err != nil {
		return nil, err
	}
	input["image_prompt"] = imageURL
	if err := validatePredictionInput(input, mode); err != nil {
		return nil, err
	}
	payload := map[string]any{"input": input}
	if err := validateProviderValue(payload, 0, new(int)); err != nil {
		return nil, fmt.Errorf("Replicate prediction input is invalid: %w", err)
	}
	body, err = protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, errors.New("encode Replicate prediction request")
	}
	if len(body) > maxProviderOptionBytes {
		return nil, fmt.Errorf("Replicate prediction input exceeds %d bytes", maxProviderOptionBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Replicate response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	return convertPredictionResponse(c, response, meta)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Replicate relay metadata is nil")
	}
	mode := a.relayMode(meta)
	if mode != constant.RelayModeImagesGenerations && mode != constant.RelayModeImagesEdits {
		return fmt.Errorf("Replicate channel does not support relay mode %d", mode)
	}
	format := a.format
	if meta.Format != constant.RelayFormatUnknown {
		format = meta.Format
	}
	if format != constant.RelayFormatOpenAIImage {
		return fmt.Errorf("Replicate channel does not support relay format %q", format)
	}
	if meta.IsStream || meta.Request.Stream {
		return errors.New("Replicate image requests do not support streaming")
	}
	_, _, err := splitModel(meta.ModelName)
	return err
}

func (a *Adaptor) relayMode(meta *relaycommon.Meta) constant.RelayMode {
	if meta != nil && meta.Mode != constant.RelayModeUnknown {
		return meta.Mode
	}
	return a.mode
}

func splitModel(raw string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) != 2 {
		return "", "", errors.New("Replicate model must use owner/model form")
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxModelSegmentBytes || !modelSegmentPattern.MatchString(part) {
			return "", "", errors.New("Replicate model contains an invalid segment")
		}
	}
	return parts[0], parts[1], nil
}

func validatedBaseURL(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = defaultBaseURL
	}
	if len(base) > maxBaseURLBytes {
		return "", errors.New("Replicate base URL is too long")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("Replicate base URL must be a valid HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Replicate base URL must not contain credentials, query, or fragment")
	}
	return strings.TrimRight(base, "/"), nil
}

func validatedCredential(raw string) (string, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" {
		return "", errors.New("Replicate API key is required")
	}
	if len(credential) > maxCredentialBytes || strings.ContainsAny(credential, "\r\n\x00") || !utf8.ValidString(credential) {
		return "", errors.New("Replicate API key is invalid")
	}
	return credential, nil
}

func buildPredictionInput(meta *relaycommon.Meta) (map[string]any, error) {
	extra := meta.Request.Extra
	prompt, ok := stringField(extra, "prompt")
	if !ok {
		prompt, _ = meta.Request.Prompt.(string)
	}
	if strings.TrimSpace(prompt) == "" || len(prompt) > maxPromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("Replicate prompt is required and must be bounded UTF-8 text")
	}
	input := map[string]any{"prompt": prompt}

	size, sizePresent, err := optionalStringField(extra, "size")
	if err != nil {
		return nil, err
	}
	if sizePresent && strings.TrimSpace(size) != "" {
		aspect, width, height, err := mapOpenAISizeToFlux(size)
		if err != nil {
			return nil, err
		}
		input["aspect_ratio"] = aspect
		if aspect == "custom" {
			input["width"] = width
			input["height"] = height
		}
	}
	outputFormat, outputFormatPresent, err := optionalStringField(extra, "output_format")
	if err != nil {
		return nil, err
	}
	if outputFormatPresent && strings.TrimSpace(outputFormat) != "" {
		outputFormat = strings.ToLower(strings.TrimSpace(outputFormat))
		switch outputFormat {
		case "png", "jpg", "jpeg", "webp":
			input["output_format"] = outputFormat
		default:
			return nil, errors.New("Replicate output_format is unsupported")
		}
	}
	count := 1
	if meta.Request.N != nil {
		count = *meta.Request.N
	}
	if count < 1 || count > maxImageCount {
		return nil, fmt.Errorf("Replicate n must be between 1 and %d", maxImageCount)
	}
	input["num_outputs"] = count
	quality, qualityPresent, err := optionalStringField(extra, "quality")
	if err != nil {
		return nil, err
	}
	if qualityPresent && strings.TrimSpace(quality) != "" {
		switch strings.ToLower(strings.TrimSpace(quality)) {
		case "hd", "high":
			input["prompt_upsampling"] = true
		case "standard", "medium", "low":
		default:
			return nil, errors.New("Replicate quality is unsupported")
		}
	}
	responseFormat, responseFormatPresent, err := optionalStringField(extra, "response_format")
	if err != nil {
		return nil, err
	}
	if responseFormatPresent {
		switch strings.ToLower(strings.TrimSpace(responseFormat)) {
		case "", "url", "b64_json":
		default:
			return nil, errors.New("Replicate response_format must be url or b64_json")
		}
	}

	for _, field := range []string{"extra_fields", "input"} {
		value, exists := extra[field]
		if !exists || value == nil {
			continue
		}
		options, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Replicate %s must be a JSON object", field)
		}
		if err := mergeProviderOptions(input, options); err != nil {
			return nil, err
		}
	}
	known := map[string]struct{}{
		"model": {}, "prompt": {}, "size": {}, "n": {}, "quality": {}, "response_format": {},
		"output_format": {}, "extra_fields": {}, "input": {}, "stream": {},
	}
	for key, value := range extra {
		if _, skip := known[key]; skip || value == nil {
			continue
		}
		if err := validateProviderKey(key); err != nil {
			return nil, err
		}
		input[key] = value
	}
	finalPrompt, ok := input["prompt"].(string)
	if !ok || strings.TrimSpace(finalPrompt) == "" || len(finalPrompt) > maxPromptBytes || !utf8.ValidString(finalPrompt) {
		return nil, errors.New("Replicate provider input prompt is invalid")
	}
	return input, nil
}

func mergeProviderOptions(destination, options map[string]any) error {
	if len(options) > maxProviderOptionNodes {
		return errors.New("Replicate provider options contain too many fields")
	}
	for key, value := range options {
		if err := validateProviderKey(key); err != nil {
			return err
		}
		destination[key] = value
	}
	return nil
}

func validateProviderKey(key string) error {
	if key == "" || len(key) > maxProviderOptionKeyBytes || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("Replicate provider option name is invalid")
	}
	return nil
}

func validateProviderValue(value any, depth int, nodes *int) error {
	if depth > maxProviderOptionDepth {
		return errors.New("provider options are nested too deeply")
	}
	(*nodes)++
	if *nodes > maxProviderOptionNodes {
		return errors.New("provider options contain too many values")
	}
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return nil
	case string:
		if len(typed) > maxProviderOptionTextBytes || !utf8.ValidString(typed) {
			return errors.New("provider option text is invalid")
		}
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Abs(typed) > maxProviderOptionNumber {
			return errors.New("provider option number is invalid")
		}
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) || math.Abs(float64(typed)) > maxProviderOptionNumber {
			return errors.New("provider option number is invalid")
		}
	case int:
		if int64(typed) > int64(maxProviderOptionNumber) || int64(typed) < -int64(maxProviderOptionNumber) {
			return errors.New("provider option number is invalid")
		}
	case int8, int16, int32:
		return nil
	case int64:
		if typed > int64(maxProviderOptionNumber) || typed < -int64(maxProviderOptionNumber) {
			return errors.New("provider option number is invalid")
		}
	case uint:
		if uint64(typed) > uint64(maxProviderOptionNumber) {
			return errors.New("provider option number is invalid")
		}
	case uint8, uint16, uint32:
		return nil
	case uint64:
		if typed > uint64(maxProviderOptionNumber) {
			return errors.New("provider option number is invalid")
		}
	case []any:
		for _, nested := range typed {
			if err := validateProviderValue(nested, depth+1, nodes); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, nested := range typed {
			if err := validateProviderKey(key); err != nil {
				return err
			}
			if err := validateProviderValue(nested, depth+1, nodes); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("provider option type %T is unsupported", value)
	}
	return nil
}

func validatePredictionInput(input map[string]any, mode constant.RelayMode) error {
	prompt, ok := input["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" || len(prompt) > maxPromptBytes || !utf8.ValidString(prompt) {
		return errors.New("Replicate provider input prompt is invalid")
	}
	count, ok := boundedProviderInteger(input["num_outputs"], 1, maxImageCount)
	if !ok {
		return fmt.Errorf("Replicate num_outputs must be an integer between 1 and %d", maxImageCount)
	}
	input["num_outputs"] = count

	customAspect := false
	if rawAspect, exists := input["aspect_ratio"]; exists {
		aspect, ok := rawAspect.(string)
		if !ok {
			return errors.New("Replicate aspect_ratio is invalid")
		}
		switch aspect {
		case "1:1", "16:9", "9:16", "3:2", "2:3", "4:5", "5:4", "3:4", "4:3":
		case "custom":
			customAspect = true
		default:
			return errors.New("Replicate aspect_ratio is unsupported")
		}
	}
	for _, field := range []string{"width", "height"} {
		rawDimension, exists := input[field]
		if !exists {
			if customAspect {
				return fmt.Errorf("Replicate custom aspect_ratio requires %s", field)
			}
			continue
		}
		dimension, valid := boundedProviderInteger(rawDimension, 256, 1440)
		if !valid || dimension%32 != 0 {
			return fmt.Errorf("Replicate %s must be a multiple of 32 between 256 and 1440", field)
		}
		input[field] = dimension
	}
	if rawFormat, exists := input["output_format"]; exists {
		format, ok := rawFormat.(string)
		if !ok {
			return errors.New("Replicate output_format is invalid")
		}
		format = strings.ToLower(strings.TrimSpace(format))
		switch format {
		case "png", "jpg", "jpeg", "webp":
			input["output_format"] = format
		default:
			return errors.New("Replicate output_format is unsupported")
		}
	}
	if rawUpsampling, exists := input["prompt_upsampling"]; exists {
		if _, ok := rawUpsampling.(bool); !ok {
			return errors.New("Replicate prompt_upsampling must be boolean")
		}
	}
	if rawImage, exists := input["image_prompt"]; exists {
		imageURL, ok := rawImage.(string)
		if !ok || validateRemoteImageURL(imageURL) != nil {
			return errors.New("Replicate image_prompt must be a valid HTTP or HTTPS URL")
		}
		if mode == constant.RelayModeImagesEdits && strings.TrimSpace(imageURL) == "" {
			return errors.New("Replicate image edit requires an uploaded image")
		}
	}
	return nil
}

func boundedProviderInteger(value any, minimum, maximum int) (int, bool) {
	var parsed int64
	switch typed := value.(type) {
	case int:
		parsed = int64(typed)
	case int8:
		parsed = int64(typed)
	case int16:
		parsed = int64(typed)
	case int32:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		parsed = int64(typed)
	case uint8:
		parsed = int64(typed)
	case uint16:
		parsed = int64(typed)
	case uint32:
		parsed = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		parsed = int64(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, false
		}
		parsed = int64(typed)
	case float32:
		value64 := float64(typed)
		if math.IsNaN(value64) || math.IsInf(value64, 0) || math.Trunc(value64) != value64 || value64 > math.MaxInt64 || value64 < math.MinInt64 {
			return 0, false
		}
		parsed = int64(typed)
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		parsed = integer
	default:
		return 0, false
	}
	if parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, false
	}
	return int(parsed), true
}

func stringField(values map[string]any, key string) (string, bool) {
	if values == nil {
		return "", false
	}
	value, exists := values[key]
	if !exists {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func optionalStringField(values map[string]any, key string) (string, bool, error) {
	if values == nil {
		return "", false, nil
	}
	value, exists := values[key]
	if !exists || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("Replicate %s must be a string", key)
	}
	if len(text) > maxProviderOptionTextBytes || !utf8.ValidString(text) {
		return "", true, fmt.Errorf("Replicate %s is invalid", key)
	}
	return text, true, nil
}

func mapOpenAISizeToFlux(raw string) (string, int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), "x")
	if len(parts) != 2 {
		return "", 0, 0, errors.New("Replicate size must use WIDTHxHEIGHT form")
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if widthErr != nil || heightErr != nil || width < 1 || height < 1 || width > 16_384 || height > 16_384 {
		return "", 0, 0, errors.New("Replicate size is invalid")
	}
	switch {
	case width == height:
		return "1:1", 0, 0, nil
	case width == 1792 && height == 1024:
		return "16:9", 0, 0, nil
	case width == 1024 && height == 1792:
		return "9:16", 0, 0, nil
	case width == 1536 && height == 1024:
		return "3:2", 0, 0, nil
	case width == 1024 && height == 1536:
		return "2:3", 0, 0, nil
	}
	divisor := greatestCommonDivisor(width, height)
	ratio := fmt.Sprintf("%d:%d", width/divisor, height/divisor)
	switch ratio {
	case "1:1", "16:9", "9:16", "3:2", "2:3", "4:5", "5:4", "3:4", "4:3":
		return ratio, 0, 0, nil
	default:
		return "custom", normalizedDimension(width), normalizedDimension(height), nil
	}
}

func greatestCommonDivisor(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	if left < 0 {
		return -left
	}
	if left == 0 {
		return 1
	}
	return left
}

func normalizedDimension(value int) int {
	if value < 256 {
		value = 256
	}
	if value > 1440 {
		value = 1440
	}
	remainder := value % 32
	if remainder >= 16 {
		value += 32 - remainder
	} else {
		value -= remainder
	}
	if value < 256 {
		return 256
	}
	if value > 1440 {
		return 1440
	}
	return value
}
