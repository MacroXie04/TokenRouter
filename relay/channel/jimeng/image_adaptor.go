package jimeng

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName                  = "jimeng"
	ImageModel                   = "jimeng_high_aes_general_v21_L"
	imageAction                  = "CVProcess"
	defaultImageBaseURL          = "https://visual.volcengineapi.com"
	maxImageBaseURLBytes         = 4 << 10
	maxImageCredentialBytes      = 16 << 10
	maxImageCredentialPartBytes  = 4 << 10
	maxImageModelBytes           = 256
	maxImagePromptBytes          = 100 << 10
	maxImageInputs               = 10
	maxImageInputTotalBytes      = 15 << 20
	maxImageOutputCount          = 128
	maxImageOutputURLBytes       = 8 << 10
	maxImageOutputBytes          = 32 << 20
	maxImageOutputAggregateBytes = 60 << 20
	maxImageProviderMessageBytes = 8 << 10
	maxImageLogoTextBytes        = 1 << 10
	maxImageRequestBytes         = 16 << 20
)

var imageModels = [...]string{ImageModel}

// ModelList returns an owned copy of the synchronous Jimeng image model
// catalog. The task client in jimeng.go intentionally has its own video model
// mapping and lifecycle.
func ModelList() []string {
	models := make([]string, len(imageModels))
	copy(models, imageModels[:])
	return models
}

// Adaptor implements the reference's synchronous CVProcess image boundary.
// It is deliberately separate from Client, which owns asynchronous video
// submission and polling.
type Adaptor struct {
	mode   constant.RelayMode
	format constant.RelayFormat
	Now    func() time.Time
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
	base, err := validatedImageBaseURL(meta.BaseURL)
	if err != nil {
		return "", err
	}
	credential, err := parseImageCredential(meta.APIKey)
	if err != nil {
		return "", err
	}
	path := "/"
	if credential.gateway {
		path = "/jimeng/"
	}
	endpoint, err := url.Parse(base + path)
	if err != nil {
		return "", errors.New("build Jimeng image request URL")
	}
	query := endpoint.Query()
	query.Set("Action", imageAction)
	query.Set("Version", APIVersion)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Jimeng image request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	expectedURL, err := a.GetRequestURL(meta)
	if err != nil {
		return err
	}
	if request.Method != http.MethodPost || request.URL == nil || request.URL.String() != expectedURL {
		return errors.New("Jimeng image request URL or method is invalid")
	}
	credential, err := parseImageCredential(meta.APIKey)
	if err != nil {
		return err
	}
	request.Header.Del("Authorization")
	request.Header.Del("X-Date")
	request.Header.Del("X-Content-Sha256")
	request.Header.Del("api-key")
	request.Header.Del("x-api-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if credential.gateway {
		request.Header.Set("Authorization", "Bearer "+credential.token)
		return nil
	}
	if request.GetBody == nil {
		return errors.New("Jimeng signed request body cannot be verified")
	}
	bodyReader, err := request.GetBody()
	if err != nil {
		return errors.New("Jimeng signed request body cannot be read")
	}
	defer bodyReader.Close()
	body, err := io.ReadAll(io.LimitReader(bodyReader, maxImageRequestBytes+1))
	if err != nil || len(body) > maxImageRequestBytes {
		return errors.New("Jimeng signed request body is invalid")
	}
	signRequest(request, body, credential.accessKey, credential.secretKey, a.now())
	return nil
}

type imageLogoInfo struct {
	AddLogo         bool    `json:"add_logo,omitempty"`
	Position        int     `json:"position,omitempty"`
	Language        int     `json:"language,omitempty"`
	Opacity         float64 `json:"opacity,omitempty"`
	LogoTextContent string  `json:"logo_text_content,omitempty"`
}

type imageRequestOptions struct {
	Seed             *int64         `json:"seed"`
	Width            *int           `json:"width"`
	Height           *int           `json:"height"`
	UsePreLLM        *bool          `json:"use_pre_llm"`
	UseSR            *bool          `json:"use_sr"`
	ReturnURL        *bool          `json:"return_url"`
	LogoInfo         *imageLogoInfo `json:"logo_info"`
	ImageURLs        []string       `json:"image_urls"`
	BinaryDataBase64 []string       `json:"binary_data_base64"`
}

type imageRequestPayload struct {
	ReqKey           string         `json:"req_key"`
	Prompt           string         `json:"prompt"`
	Seed             *int64         `json:"seed,omitempty"`
	Width            *int           `json:"width,omitempty"`
	Height           *int           `json:"height,omitempty"`
	UsePreLLM        *bool          `json:"use_pre_llm,omitempty"`
	UseSR            *bool          `json:"use_sr,omitempty"`
	ReturnURL        bool           `json:"return_url,omitempty"`
	LogoInfo         *imageLogoInfo `json:"logo_info,omitempty"`
	ImageURLs        []string       `json:"image_urls,omitempty"`
	BinaryDataBase64 []string       `json:"binary_data_base64,omitempty"`
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if _, err := parseImageCredential(meta.APIKey); err != nil {
		return nil, err
	}
	if len(meta.RawBody) > maxImageRequestBytes {
		return nil, fmt.Errorf("Jimeng image request exceeds %d bytes", maxImageRequestBytes)
	}
	if len(bytes.TrimSpace(meta.RawBody)) != 0 {
		var raw map[string]any
		if err := strictImageJSON(meta.RawBody, &raw); err != nil {
			return nil, errors.New("Jimeng image request contains invalid or duplicate JSON fields")
		}
	}
	prompt, ok := meta.Request.Prompt.(string)
	if !ok {
		prompt, _ = meta.Request.Extra["prompt"].(string)
	}
	if strings.TrimSpace(prompt) == "" || len(prompt) > maxImagePromptBytes || !utf8.ValidString(prompt) {
		return nil, errors.New("Jimeng image prompt is required and must be bounded UTF-8 text")
	}
	if meta.Request.N != nil && *meta.Request.N != 1 {
		return nil, errors.New("Jimeng synchronous image generation supports exactly one request result set")
	}
	if err := validateImageExtraKeys(meta.Request.Extra); err != nil {
		return nil, err
	}
	extra, err := protocolkit.MarshalJSON(meta.Request.Extra)
	if err != nil {
		return nil, errors.New("encode Jimeng image options")
	}
	var options imageRequestOptions
	if len(meta.Request.Extra) != 0 {
		if err := json.Unmarshal(extra, &options); err != nil {
			return nil, errors.New("Jimeng image options have invalid types")
		}
	}
	if err := applyImageSize(meta.Request.Extra, &options); err != nil {
		return nil, err
	}
	if err := validateImageOptions(options); err != nil {
		return nil, err
	}
	responseFormat, _ := meta.Request.Extra["response_format"].(string)
	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	returnURL := responseFormat == "" || responseFormat == "url"
	if responseFormat != "" && responseFormat != "url" && responseFormat != "b64_json" {
		return nil, errors.New("Jimeng response_format must be url or b64_json")
	}
	if options.ReturnURL != nil && responseFormat == "" {
		returnURL = *options.ReturnURL
	}
	payload := imageRequestPayload{
		ReqKey: meta.ModelName, Prompt: prompt,
		Seed: options.Seed, Width: options.Width, Height: options.Height,
		UsePreLLM: options.UsePreLLM, UseSR: options.UseSR, ReturnURL: returnURL,
		LogoInfo: options.LogoInfo, ImageURLs: options.ImageURLs, BinaryDataBase64: options.BinaryDataBase64,
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, errors.New("encode Jimeng image request")
	}
	if len(body) > maxImageRequestBytes {
		return nil, fmt.Errorf("Jimeng converted image request exceeds %d bytes", maxImageRequestBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Jimeng image response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return acceptedImageUsage(meta), fmt.Errorf("read Jimeng image response: %w", err)
	}
	var provider imageProviderResponse
	if err := strictImageJSON(body, &provider); err != nil {
		return acceptedImageUsage(meta), errors.New("Jimeng returned an invalid image response")
	}
	if provider.Code != providerSuccessCode {
		if provider.Code == 0 {
			return acceptedImageUsage(meta), errors.New("Jimeng returned an ambiguous image response")
		}
		return nil, imageProviderError(provider.Code, provider.Message)
	}
	output, err := convertImageResponse(provider, a.now())
	if err != nil {
		return acceptedImageUsage(meta), err
	}
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return acceptedImageUsage(meta), errors.New("encode Jimeng client image response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(response.StatusCode)
	if _, err := c.Writer.Write(encoded); err != nil {
		return acceptedImageUsage(meta), fmt.Errorf("write Jimeng client image response: %w", err)
	}
	// The provider reports no token usage. Nil preserves fixed-price billing and
	// the relay's prompt estimate for ratio-priced deployments.
	return nil, nil
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Jimeng image relay metadata is nil")
	}
	mode := meta.Mode
	if mode == constant.RelayModeUnknown {
		mode = a.mode
	}
	format := meta.Format
	if format == constant.RelayFormatUnknown {
		format = a.format
	}
	if mode != constant.RelayModeImagesGenerations {
		return fmt.Errorf("Jimeng synchronous adapter does not support relay mode %d", mode)
	}
	if format != constant.RelayFormatOpenAIImage {
		return fmt.Errorf("Jimeng synchronous adapter does not support relay format %q", format)
	}
	if meta.IsStream || meta.Request.Stream {
		return errors.New("Jimeng synchronous image generation does not support streaming")
	}
	modelName := strings.TrimSpace(meta.ModelName)
	if modelName != ImageModel || len(modelName) > maxImageModelBytes || !utf8.ValidString(modelName) || containsImageControl(modelName) {
		return errors.New("Jimeng synchronous image model is invalid")
	}
	return nil
}

func (a *Adaptor) now() time.Time {
	if a != nil && a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func validatedImageBaseURL(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		base = defaultImageBaseURL
	}
	if len(base) > maxImageBaseURLBytes || strings.ContainsAny(base, "\\\r\n\x00") {
		return "", errors.New("Jimeng image base URL is invalid")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("Jimeng image base URL must be a valid HTTP or HTTPS URL")
	}
	if parsed.Scheme != "https" && !(appcommon.SSRFDisabled() && parsed.Scheme == "http") {
		return "", errors.New("Jimeng image base URL must use HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("Jimeng image base URL must not contain credentials, query, or fragment")
	}
	return base, nil
}

type imageCredential struct {
	gateway   bool
	token     string
	accessKey string
	secretKey string
}

func parseImageCredential(raw string) (imageCredential, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" || credential != raw || len(credential) > maxImageCredentialBytes || containsImageControl(credential) {
		return imageCredential{}, errors.New("Jimeng image credential is invalid")
	}
	if strings.HasPrefix(credential, "sk-") {
		if strings.Contains(credential, "|") || containsImageSpace(credential) {
			return imageCredential{}, errors.New("Jimeng gateway credential is invalid")
		}
		return imageCredential{gateway: true, token: credential}, nil
	}
	parts := strings.Split(credential, "|")
	if len(parts) != 2 {
		return imageCredential{}, errors.New("Jimeng image credential must use access_key|secret_key")
	}
	for _, part := range parts {
		if part == "" || len(part) > maxImageCredentialPartBytes || strings.TrimSpace(part) != part || containsImageControl(part) || containsImageSpace(part) {
			return imageCredential{}, errors.New("Jimeng image credential component is invalid")
		}
	}
	return imageCredential{accessKey: parts[0], secretKey: parts[1]}, nil
}

func containsImageControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func containsImageSpace(value string) bool {
	for _, char := range value {
		if unicode.IsSpace(char) {
			return true
		}
	}
	return false
}

func validateImageExtraKeys(extra map[string]any) error {
	allowed := map[string]struct{}{
		"model": {}, "prompt": {}, "n": {}, "size": {}, "response_format": {}, "quality": {}, "style": {}, "user": {}, "group": {},
		"seed": {}, "width": {}, "height": {}, "use_pre_llm": {}, "use_sr": {}, "return_url": {}, "logo_info": {},
		"image_urls": {}, "binary_data_base64": {},
	}
	for key := range extra {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("Jimeng image generation does not support request field %q", key)
		}
	}
	return nil
}

func applyImageSize(extra map[string]any, options *imageRequestOptions) error {
	raw, exists := extra["size"]
	if !exists || raw == nil {
		return nil
	}
	size, ok := raw.(string)
	if !ok || len(size) > 32 {
		return errors.New("Jimeng image size is invalid")
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return errors.New("Jimeng image size must use WIDTHxHEIGHT")
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil {
		return errors.New("Jimeng image size must use integer dimensions")
	}
	if options.Width != nil && *options.Width != width || options.Height != nil && *options.Height != height {
		return errors.New("Jimeng image size conflicts with width or height")
	}
	options.Width = &width
	options.Height = &height
	return nil
}

func validateImageOptions(options imageRequestOptions) error {
	if (options.Width == nil) != (options.Height == nil) {
		return errors.New("Jimeng image width and height must be supplied together")
	}
	if options.Width != nil && (*options.Width < 256 || *options.Width > 768 || *options.Height < 256 || *options.Height > 768) {
		return errors.New("Jimeng image width and height must be between 256 and 768")
	}
	if options.Seed != nil && (*options.Seed < -1 || *options.Seed > math.MaxInt32) {
		return errors.New("Jimeng image seed is outside the supported range")
	}
	if len(options.ImageURLs)+len(options.BinaryDataBase64) > maxImageInputs {
		return fmt.Errorf("Jimeng image inputs exceed %d items", maxImageInputs)
	}
	for _, rawURL := range options.ImageURLs {
		if err := validateImageOutputURL(rawURL); err != nil {
			return errors.New("Jimeng image_urls contains an invalid URL")
		}
	}
	total := 0
	for _, encoded := range options.BinaryDataBase64 {
		if len(encoded) > base64.StdEncoding.EncodedLen(MaxImageBytes)+4 {
			return errors.New("Jimeng binary image exceeds the size limit")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded) > MaxImageBytes {
			return errors.New("Jimeng binary_data_base64 contains invalid or oversized image data")
		}
		if total > maxImageInputTotalBytes-len(decoded) {
			return errors.New("Jimeng binary image inputs exceed the aggregate size limit")
		}
		total += len(decoded)
	}
	if options.LogoInfo != nil {
		logo := options.LogoInfo
		if logo.Position < 0 || logo.Position > 4 || logo.Language < 0 || logo.Language > 8 ||
			math.IsNaN(logo.Opacity) || math.IsInf(logo.Opacity, 0) || logo.Opacity < 0 || logo.Opacity > 1 ||
			len(logo.LogoTextContent) > maxImageLogoTextBytes || !utf8.ValidString(logo.LogoTextContent) {
			return errors.New("Jimeng logo_info is invalid")
		}
	}
	return nil
}

type imageProviderResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		BinaryDataBase64 []string `json:"binary_data_base64"`
		ImageURLs        []string `json:"image_urls"`
		RephraseResult   string   `json:"rephraser_result"`
		RequestID        string   `json:"request_id"`
	} `json:"data"`
	RequestID   string `json:"request_id"`
	Status      int    `json:"status"`
	TimeElapsed string `json:"time_elapsed"`
}

type openAIImageData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type openAIImageResponse struct {
	Created int64             `json:"created"`
	Data    []openAIImageData `json:"data"`
}

func convertImageResponse(provider imageProviderResponse, now time.Time) (openAIImageResponse, error) {
	count := len(provider.Data.BinaryDataBase64) + len(provider.Data.ImageURLs)
	if count == 0 || count > maxImageOutputCount {
		return openAIImageResponse{}, errors.New("Jimeng returned an invalid image count")
	}
	output := openAIImageResponse{Created: now.Unix(), Data: make([]openAIImageData, 0, count)}
	total := 0
	for _, encoded := range provider.Data.BinaryDataBase64 {
		if len(encoded) > base64.StdEncoding.EncodedLen(maxImageOutputBytes)+4 {
			return openAIImageResponse{}, errors.New("Jimeng returned oversized base64 image data")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded) > maxImageOutputBytes {
			return openAIImageResponse{}, errors.New("Jimeng returned invalid base64 image data")
		}
		if total > maxImageOutputAggregateBytes-len(decoded) {
			return openAIImageResponse{}, errors.New("Jimeng returned oversized aggregate image data")
		}
		total += len(decoded)
		output.Data = append(output.Data, openAIImageData{B64JSON: encoded})
	}
	for _, rawURL := range provider.Data.ImageURLs {
		if err := validateImageOutputURL(rawURL); err != nil {
			return openAIImageResponse{}, errors.New("Jimeng returned an invalid image URL")
		}
		output.Data = append(output.Data, openAIImageData{URL: rawURL})
	}
	return output, nil
}

func validateImageOutputURL(raw string) error {
	if raw == "" || len(raw) > maxImageOutputURLBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\\r\n\x00") {
		return errors.New("image URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("image URL is invalid")
	}
	return nil
}

func imageProviderError(code int, message string) error {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > maxImageProviderMessageBytes || !utf8.ValidString(message) {
		message = "Jimeng rejected the image request"
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: "jimeng_error", Code: strconv.Itoa(code),
	}, http.StatusBadRequest)
}

func acceptedImageUsage(meta *relaycommon.Meta) *protocolkit.Usage {
	prompt := 0
	if meta != nil {
		prompt = meta.PromptTokens
		if prompt <= 0 {
			prompt = relaycommon.EstimatePromptTokens(meta.Request)
		}
	}
	if prompt <= 0 || int64(prompt) > appcommon.MaxQuota {
		prompt = 1
	}
	return &protocolkit.Usage{PromptTokens: prompt, TotalTokens: prompt}
}
