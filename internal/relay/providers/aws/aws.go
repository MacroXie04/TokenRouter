// Package aws implements the Amazon Bedrock InvokeModel boundary used by the
// AWS channel. It deliberately uses the ordinary relay lifecycle: this package
// owns only request conversion, authentication/signing, and bounded provider
// response decoding.
package aws

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/claude"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	AnthropicVersion = "bedrock-2023-05-31"

	MaxRequestBodyBytes  int64 = 16 << 20
	MaxResponseBodyBytes int64 = 16 << 20
	MaxErrorBodyBytes    int64 = 16 << 10
	// Leave enough headroom for the SSE "data: " prefix and delimiters after
	// decoding an AWS event-stream chunk. The shared scanner caps each line at
	// 1 MiB, so accepting a full 1 MiB inner event would fail one layer later.
	MaxEventPayloadBytes = (1 << 20) - 1024
	MaxEventHeadersBytes = 64 << 10
	// A Bedrock event-stream frame wraps the inner JSON as base64 in another
	// JSON object. Bound the encoded wire representation, not only the decoded
	// event, so a valid maximum-sized event is accepted deterministically.
	MaxEventFrameBytes     = ((MaxEventPayloadBytes+2)/3)*4 + MaxEventHeadersBytes + 64
	MaxStreamEvents        = 100_000
	MaxCredentialBytes     = 16 << 10
	MaxCredentialPartBytes = 8 << 10
	MaxRegionBytes         = 64
	MaxModelIDBytes        = 2048
	MaxBaseURLBytes        = 4096
	MaxHeaderValueBytes    = 16 << 10
	MaxBetaValues          = 64
	MaxBetaValueBytes      = 256
	MaxMessages            = 4096
	MaxContentParts        = 4096
	MaxTools               = 1024
	MaxStopSequences       = 256
	MaxStopSequenceBytes   = 4096
	MaxMessageTextBytes    = 8 << 20
	MaxMediaBytes          = 8 << 20
	MaxJSONDepth           = 64
	MaxJSONNodes           = 100_000
	MaxJSONObjectEntries   = 10_000
	MaxJSONArrayEntries    = 10_000
	MaxJSONKeyBytes        = 1024
	MaxJSONTextBytes       = 16 << 20
	MaxJSONNumberMagnitude = 1e18
	MaxOutputTokens        = 1_000_000
	MaxUsageTokens         = int64(quotamath.MaxQuota)
	mediaRequestTimeout    = 30 * time.Second
)

type channelOtherSettings struct {
	AWSKeyType CredentialMode `json:"aws_key_type,omitempty"`
}

type channelSettings struct {
	PassThroughBodyEnabled bool `json:"pass_through_body_enabled,omitempty"`
}

// Adaptor implements the strict Bedrock InvokeModel and
// InvokeModelWithResponseStream wire contracts.
type Adaptor struct {
	mode   channelcatalog.RelayMode
	format channelcatalog.RelayFormat

	// Now is optional and exists to make SigV4 output deterministic in tests.
	Now func() time.Time
	// MediaHTTPClient is optional. It fetches URL-backed Anthropic media without
	// provider credentials; the default is SSRF-safe and refuses redirects.
	MediaHTTPClient *http.Client
	claude          claude.Adaptor
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
	a.claude = claude.Adaptor{}
	a.claude.Init(meta)
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	credential, err := a.validate(meta)
	if err != nil {
		return "", err
	}
	modelName := meta.ModelName
	if !IsNovaModel(modelName) {
		modelName = relaycommon.PrepareClaudeRequest(nil, meta.OriginalModelName, modelName, true)
	}
	modelID := RegionalModelID(modelName, credential.Region)
	if err := validateModelID(modelID); err != nil {
		return "", err
	}
	base, err := bedrockBaseURL(meta.BaseURL, credential.Region)
	if err != nil {
		return "", err
	}
	suffix := "/invoke"
	if meta.IsStream {
		suffix = "/invoke-with-response-stream"
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", errors.New("AWS Bedrock base URL is invalid")
	}
	escapedModel := url.PathEscape(modelID)
	parsed.Path = "/model/" + modelID + suffix
	parsed.RawPath = "/model/" + escapedModel + suffix
	return parsed.String(), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil || request.URL == nil {
		return errors.New("AWS request is nil")
	}
	if request.Method != http.MethodPost {
		return errors.New("AWS Bedrock request method must be POST")
	}
	credential, err := a.validate(meta)
	if err != nil {
		return err
	}
	expected, err := a.GetRequestURL(meta)
	if err != nil {
		return err
	}
	if request.URL.String() != expected {
		return errors.New("AWS request URL does not match the validated Bedrock endpoint")
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Del("Authorization")
	request.Header.Del("x-api-key")
	request.Header.Del("api-key")
	request.Header.Del("x-goog-api-key")
	request.Header.Del("anthropic-version")
	request.Header.Del("anthropic-beta")
	request.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		// InvokeModelWithResponseStream encodes the requested inner payload type
		// in this Bedrock-specific header. The HTTP response itself is an AWS
		// binary event stream, so do not reuse InvokeModel's ordinary Accept
		// header here.
		request.Header.Del("Accept")
		request.Header.Set("X-Amzn-Bedrock-Accept", "application/json")
	} else {
		request.Header.Set("Accept", "application/json")
		request.Header.Del("X-Amzn-Bedrock-Accept")
	}
	body, err := requestBody(request)
	if err != nil {
		return err
	}
	switch credential.Mode {
	case CredentialModeAPIKey:
		request.Header.Set("Authorization", "Bearer "+credential.APIKey)
		request.Header.Del("X-Amz-Date")
		request.Header.Del("X-Amz-Content-Sha256")
	case CredentialModeAKSK:
		now := time.Now()
		if a.Now != nil {
			now = a.Now()
		}
		if err := SignRequestAt(request, body, credential, now); err != nil {
			return err
		}
	default:
		return errors.New("AWS credential mode is invalid")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	credential, err := a.validate(meta)
	if err != nil {
		return nil, err
	}
	_ = credential // validation also protects the pre-network conversion path.
	if len(meta.RawBody) > int(MaxRequestBodyBytes) {
		return nil, fmt.Errorf("AWS request exceeds %d bytes", MaxRequestBodyBytes)
	}
	if len(bytes.TrimSpace(meta.RawBody)) > 0 {
		if err := rejectDuplicateJSONKeys(meta.RawBody); err != nil {
			return nil, fmt.Errorf("AWS request is invalid: %w", err)
		}
	}
	var body []byte
	if IsNovaModel(meta.ModelName) {
		body, err = a.convertNovaRequest(meta)
	} else {
		body, err = a.convertClaudeRequest(meta)
	}
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return nil, fmt.Errorf("AWS converted request exceeds %d bytes", MaxRequestBodyBytes)
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return nil, fmt.Errorf("AWS converted request is invalid: %w", err)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if response == nil || response.Body == nil {
		return nil, errors.New("AWS Bedrock response is nil")
	}
	if meta == nil {
		return nil, errors.New("AWS relay metadata is nil")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, awsHTTPError(response)
	}
	if IsNovaModel(meta.ModelName) {
		return a.novaResponse(c, response, meta)
	}
	if meta.IsStream {
		return a.claudeStreamResponse(c, response, meta)
	}
	return a.claudeResponse(c, response, meta)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) (Credential, error) {
	if meta == nil || meta.Channel == nil {
		return Credential{}, errors.New("AWS relay metadata is nil")
	}
	mode := a.mode
	if mode == channelcatalog.RelayModeUnknown {
		mode = meta.Mode
	}
	if mode != channelcatalog.RelayModeChatCompletions {
		return Credential{}, fmt.Errorf("AWS channel does not support relay mode %d", mode)
	}
	format := a.format
	if format == channelcatalog.RelayFormatUnknown {
		format = meta.Format
	}
	if format != channelcatalog.RelayFormatOpenAI && format != channelcatalog.RelayFormatClaude {
		return Credential{}, fmt.Errorf("AWS channel does not support relay format %q", format)
	}
	if IsNovaModel(meta.ModelName) && format != channelcatalog.RelayFormatOpenAI {
		return Credential{}, errors.New("AWS Nova models do not support native Anthropic Messages input")
	}
	if IsNovaModel(meta.ModelName) && meta.IsStream {
		return Credential{}, errors.New("AWS Nova streaming is not supported by the reference adapter")
	}
	if meta.ModelName != strings.TrimSpace(meta.ModelName) {
		return Credential{}, errors.New("AWS Bedrock model ID is invalid")
	}
	if err := validateModelID(ModelID(meta.ModelName)); err != nil {
		return Credential{}, err
	}
	keyType, err := parseAWSKeyType(meta.Channel.OtherSettings)
	if err != nil {
		return Credential{}, err
	}
	return ParseCredential(meta.APIKey, keyType)
}

func parseAWSKeyType(raw string) (CredentialMode, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	if len(raw) > MaxHeaderValueBytes || rejectDuplicateJSONKeys([]byte(raw)) != nil {
		return "", errors.New("AWS channel settings are invalid")
	}
	var settings channelOtherSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return "", errors.New("AWS channel settings are invalid")
	}
	switch settings.AWSKeyType {
	case "", CredentialModeAKSK, CredentialModeAPIKey:
		return settings.AWSKeyType, nil
	default:
		return "", errors.New("AWS aws_key_type is invalid")
	}
}

func passThroughEnabled(meta *relaycommon.Meta) (bool, error) {
	if meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.Setting) == "" {
		return false, nil
	}
	if len(meta.Channel.Setting) > MaxHeaderValueBytes || rejectDuplicateJSONKeys([]byte(meta.Channel.Setting)) != nil {
		return false, errors.New("AWS channel setting is invalid")
	}
	var settings channelSettings
	if err := json.Unmarshal([]byte(meta.Channel.Setting), &settings); err != nil {
		return false, errors.New("AWS channel setting is invalid")
	}
	return settings.PassThroughBodyEnabled, nil
}

func bedrockBaseURL(configured, region string) (string, error) {
	base := strings.TrimSpace(configured)
	if base == "" {
		base = "https://bedrock-runtime." + region + ".amazonaws.com"
	}
	if len(base) > MaxBaseURLBytes {
		return "", errors.New("AWS Bedrock base URL is too long")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.RawPath != "" && parsed.RawPath != "/") {
		return "", errors.New("AWS Bedrock base URL must be an HTTPS origin")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func validateModelID(model string) error {
	if model == "" || len(model) > MaxModelIDBytes || model != strings.TrimSpace(model) || !utf8.ValidString(model) {
		return errors.New("AWS Bedrock model ID is invalid")
	}
	for _, character := range model {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("-_.:/", character) {
			return errors.New("AWS Bedrock model ID is invalid")
		}
	}
	if strings.HasPrefix(model, "/") || strings.HasSuffix(model, "/") || strings.Contains(model, "//") {
		return errors.New("AWS Bedrock model ID is invalid")
	}
	for _, segment := range strings.Split(model, "/") {
		if segment == "." || segment == ".." {
			return errors.New("AWS Bedrock model ID is invalid")
		}
	}
	return nil
}

func (a *Adaptor) convertNovaRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta.Request == nil {
		return nil, errors.New("AWS Nova request is nil")
	}
	if err := ensureOriginalModel(meta); err != nil {
		return nil, err
	}
	if len(meta.Request.Messages) == 0 || len(meta.Request.Messages) > MaxMessages {
		return nil, fmt.Errorf("AWS Nova messages must contain 1 to %d entries", MaxMessages)
	}
	if len(meta.Request.Tools) > 0 || meta.Request.ToolChoice != nil || meta.Request.FunctionCall != nil || meta.Request.Functions != nil {
		return nil, errors.New("AWS Nova reference codec does not support tools")
	}
	messages := make([]novaMessage, 0, len(meta.Request.Messages))
	for _, message := range meta.Request.Messages {
		role := strings.TrimSpace(message.Role)
		if role != "user" && role != "assistant" && role != "system" {
			return nil, errors.New("AWS Nova message role is invalid")
		}
		text, err := messageText(message.Content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, novaMessage{Role: role, Content: []novaContent{{Text: text}}})
	}
	config, err := novaInference(meta.Request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(novaRequest{SchemaVersion: "messages-v1", Messages: messages, InferenceConfig: config})
}

func messageText(content any) (string, error) {
	if text, ok := content.(string); ok {
		if !utf8.ValidString(text) || len(text) > MaxMessageTextBytes {
			return "", errors.New("AWS Nova message text is outside the supported range")
		}
		return text, nil
	}
	raw, err := json.Marshal(content)
	if err != nil || int64(len(raw)) > MaxRequestBodyBytes {
		return "", errors.New("AWS Nova message content is invalid")
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := strictJSON(raw, &parts, true); err != nil || len(parts) > MaxContentParts {
		return "", errors.New("AWS Nova message content must contain only text parts")
	}
	var output strings.Builder
	for _, part := range parts {
		if part.Type != "text" || !utf8.ValidString(part.Text) || output.Len()+len(part.Text) > MaxMessageTextBytes {
			return "", errors.New("AWS Nova message content must contain only bounded text parts")
		}
		output.WriteString(part.Text)
	}
	return output.String(), nil
}

func novaInference(request *protocolkit.GeneralOpenAIRequest) (*novaInferenceConfig, error) {
	config := &novaInferenceConfig{}
	set := false
	maxTokens := request.MaxTokens
	if request.MaxCompletionTokens != nil {
		maxTokens = request.MaxCompletionTokens
	}
	if maxTokens != nil && *maxTokens != 0 {
		if *maxTokens < 0 || *maxTokens > MaxOutputTokens {
			return nil, errors.New("AWS Nova max_tokens is outside the supported range")
		}
		config.MaxTokens = *maxTokens
		set = true
	}
	if request.Temperature != nil && *request.Temperature != 0 {
		if !finiteRange(*request.Temperature, 0, 1) {
			return nil, errors.New("AWS Nova temperature is outside the supported range")
		}
		config.Temperature = request.Temperature
		set = true
	}
	if request.TopP != nil && *request.TopP != 0 {
		if !finiteRange(*request.TopP, 0, 1) {
			return nil, errors.New("AWS Nova top_p is outside the supported range")
		}
		config.TopP = request.TopP
		set = true
	}
	if rawTopK := request.Extra["top_k"]; rawTopK != nil {
		topK, ok := exactJSONInt(rawTopK)
		if !ok || topK < 0 || topK > 128 {
			return nil, errors.New("AWS Nova top_k is outside the supported range")
		}
		if topK != 0 {
			config.TopK = &topK
			set = true
		}
	}
	stops, err := stopSequences(request.Stop)
	if err != nil {
		return nil, err
	}
	if len(stops) > 0 {
		config.StopSequences = stops
		set = true
	}
	if !set {
		return nil, nil
	}
	return config, nil
}

func finiteRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func exactJSONInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < math.MinInt || typed > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		integer, err := typed.Int64()
		if err != nil || integer < math.MinInt || integer > math.MaxInt {
			return 0, false
		}
		return int(integer), true
	default:
		return 0, false
	}
}

func ensureOriginalModel(meta *relaycommon.Meta) error {
	if len(bytes.TrimSpace(meta.RawBody)) == 0 {
		return nil
	}
	var envelope struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(meta.RawBody, &envelope); err != nil {
		return errors.New("AWS request is invalid")
	}
	expected := strings.TrimSpace(meta.OriginalModelName)
	if expected == "" && meta.Request != nil {
		expected = strings.TrimSpace(meta.Request.Model)
	}
	if expected != "" && strings.TrimSpace(envelope.Model) != expected {
		return errors.New("AWS request model does not match the selected model")
	}
	return nil
}

func stopSequences(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	var result []string
	switch typed := value.(type) {
	case string:
		result = []string{typed}
	case []string:
		result = append([]string(nil), typed...)
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("AWS stop sequences must be strings")
			}
			result = append(result, text)
		}
	default:
		return nil, errors.New("AWS stop sequences must be a string or string array")
	}
	if len(result) > MaxStopSequences {
		return nil, fmt.Errorf("AWS stop sequences exceed %d entries", MaxStopSequences)
	}
	for _, sequence := range result {
		if sequence == "" || !utf8.ValidString(sequence) || len(sequence) > MaxStopSequenceBytes {
			return nil, errors.New("AWS stop sequence is invalid")
		}
	}
	return result, nil
}

type novaRequest struct {
	SchemaVersion   string               `json:"schemaVersion"`
	Messages        []novaMessage        `json:"messages"`
	InferenceConfig *novaInferenceConfig `json:"inferenceConfig,omitempty"`
}

type novaMessage struct {
	Role    string        `json:"role"`
	Content []novaContent `json:"content"`
}

type novaContent struct {
	Text string `json:"text"`
}

type novaInferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	TopK          *int     `json:"topK,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

func mediaHTTPClient() *http.Client {
	return &http.Client{
		Timeout: mediaRequestTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            httpx.SafeDialContext,
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  20 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func normalizeMediaType(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", errors.New("AWS media type is invalid")
	}
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "application/pdf":
		return strings.ToLower(mediaType), nil
	default:
		return "", errors.New("AWS media type is unsupported")
	}
}

func validateBase64Media(data string) error {
	if len(data) > base64.StdEncoding.EncodedLen(MaxMediaBytes)+2 {
		return fmt.Errorf("AWS media exceeds %d decoded bytes", MaxMediaBytes)
	}
	if strings.ContainsAny(data, " \t\r\n") {
		return errors.New("AWS media contains invalid base64")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil || len(decoded) > MaxMediaBytes {
		return errors.New("AWS media contains invalid or oversized base64")
	}
	return nil
}
