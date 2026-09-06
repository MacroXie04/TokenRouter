// Package tencent implements Tencent Hunyuan's native TC3 chat contract and
// the reference TokenHub OpenAI-compatible credential mode.
package tencent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName = "tencent"

	defaultNativeBaseURL              = "https://hunyuan.tencentcloudapi.com"
	tokenHubBaseURL                   = "https://tokenhub.tencentmaas.com"
	tencentAction                     = "ChatCompletions"
	tencentVersion                    = "2023-09-01"
	tencentService                    = "hunyuan"
	maxBaseURLBytes                   = 4 << 10
	maxCredentialBytes                = 16 << 10
	maxCredentialComponentBytes       = 4 << 10
	maxModelBytes                     = 1 << 10
	maxMessages                       = 40
	maxMessageBytes                   = 1 << 20
	maxPromptBytes                    = 8 << 20
	maxResponseChoices                = 8
	maxResponseTextBytes              = 16 << 20
	maxProviderIDBytes                = 4 << 10
	maxProviderErrorBytes             = 8 << 10
	maxStreamBodyBytes          int64 = 64 << 20
	maxStreamEvents                   = 100_000
)

var supportedModels = [...]string{
	"hunyuan-lite",
	"hunyuan-standard",
	"hunyuan-standard-256K",
	"hunyuan-pro",
}

// ModelList returns an owned copy of the exact reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

type Adaptor struct {
	mode     constant.RelayMode
	format   constant.RelayFormat
	native   bool
	delegate openai.Adaptor
	// Now is injectable so TC3 signatures can be verified deterministically.
	Now func() time.Time
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	a.native = false
	if meta == nil {
		return
	}
	a.mode = meta.Mode
	a.format = meta.Format
	a.native = strings.Contains(strings.TrimSpace(meta.APIKey), "|")
	if !a.native {
		normalizeTokenHubBaseURL(meta)
		a.delegate.Init(meta)
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	if !a.isNative(meta) {
		normalizeTokenHubBaseURL(meta)
		if _, err := parseTokenCredential(meta.APIKey); err != nil {
			return "", err
		}
		if _, err := validatedBaseURL(meta.BaseURL, false); err != nil {
			return "", err
		}
		a.delegate.Init(meta)
		return a.delegate.GetRequestURL(meta)
	}
	if _, err := parseNativeCredentials(meta.APIKey); err != nil {
		return "", err
	}
	base, err := validatedBaseURL(meta.BaseURL, true)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + "/", nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Tencent request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	if !a.isNative(meta) {
		normalizeTokenHubBaseURL(meta)
		if _, err := parseTokenCredential(meta.APIKey); err != nil {
			return err
		}
		a.delegate.Init(meta)
		return a.delegate.SetupRequestHeader(request, meta)
	}

	credentials, err := parseNativeCredentials(meta.APIKey)
	if err != nil {
		return err
	}
	expectedURL, err := a.GetRequestURL(meta)
	if err != nil {
		return err
	}
	if request.URL == nil || request.URL.String() != expectedURL || request.Method != http.MethodPost {
		return errors.New("Tencent signed request URL or method is invalid")
	}
	if request.GetBody == nil {
		return errors.New("Tencent signed request body cannot be verified")
	}
	bodyReader, err := request.GetBody()
	if err != nil {
		return errors.New("Tencent signed request body cannot be read")
	}
	defer bodyReader.Close()
	body, err := io.ReadAll(io.LimitReader(bodyReader, relaycommon.MaxUpstreamJSONBodyBytes+1))
	if err != nil || int64(len(body)) > relaycommon.MaxUpstreamJSONBodyBytes {
		return errors.New("Tencent signed request body is invalid")
	}
	timestamp := a.now().Unix()
	authorization, err := tc3Authorization(request.URL, body, credentials.secretID, credentials.secretKey, timestamp)
	if err != nil {
		return err
	}
	request.Header.Del("api-key")
	request.Header.Del("x-api-key")
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", responseContentType(meta.IsStream))
	request.Header.Set("X-TC-Action", tencentAction)
	request.Header.Set("X-TC-Version", tencentVersion)
	request.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	return nil
}

type tencentMessage struct {
	Role    string `json:"Role"`
	Content string `json:"Content"`
}

type tencentChatRequest struct {
	Model       string           `json:"Model"`
	Messages    []tencentMessage `json:"Messages"`
	Stream      *bool            `json:"Stream,omitempty"`
	TopP        *float64         `json:"TopP,omitempty"`
	Temperature *float64         `json:"Temperature,omitempty"`
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if !a.isNative(meta) {
		normalizeTokenHubBaseURL(meta)
		if _, err := parseTokenCredential(meta.APIKey); err != nil {
			return nil, err
		}
		a.delegate.Init(meta)
		return a.delegate.ConvertRequest(meta)
	}
	if _, err := parseNativeCredentials(meta.APIKey); err != nil {
		return nil, err
	}
	if err := validateNativeRequestFields(meta.Request); err != nil {
		return nil, err
	}
	if len(meta.Request.Messages) == 0 || len(meta.Request.Messages) > maxMessages {
		return nil, fmt.Errorf("Tencent messages must contain between 1 and %d items", maxMessages)
	}
	messages := make([]tencentMessage, 0, len(meta.Request.Messages))
	totalBytes := 0
	for index, input := range meta.Request.Messages {
		if err := validateMessageRole(meta.Request.Messages, index); err != nil {
			return nil, err
		}
		content, err := textContent(input.Content)
		if err != nil {
			return nil, err
		}
		if content == "" || len(content) > maxMessageBytes || !utf8.ValidString(content) {
			return nil, errors.New("Tencent message content is empty, oversized, or invalid UTF-8")
		}
		if totalBytes > maxPromptBytes-len(content) {
			return nil, fmt.Errorf("Tencent prompt exceeds %d bytes", maxPromptBytes)
		}
		totalBytes += len(content)
		messages = append(messages, tencentMessage{Role: input.Role, Content: content})
	}
	stream := meta.IsStream
	out := tencentChatRequest{
		Model: meta.ModelName, Messages: messages, Stream: &stream,
		TopP: meta.Request.TopP, Temperature: meta.Request.Temperature,
	}
	body, err := protocolkit.MarshalJSON(out)
	if err != nil {
		return nil, errors.New("encode Tencent request")
	}
	if int64(len(body)) > relaycommon.MaxUpstreamJSONBodyBytes {
		return nil, errors.New("Tencent converted request is too large")
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Tencent response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if !a.isNative(meta) {
		a.delegate.Init(meta)
		usage, err := a.delegate.DoResponse(c, response, meta)
		if err != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			var upstream *relaycommon.UpstreamError
			if errors.As(err, &upstream) && upstream.StatusCode >= 400 && upstream.StatusCode < 500 && !c.Writer.Written() {
				return nil, err
			}
			return acceptedUsage(meta), err
		}
		return usage, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if meta.IsStream {
		return a.streamResponse(c, response, meta)
	}
	return a.nonStreamResponse(c, response, meta)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Tencent relay metadata is nil")
	}
	mode := meta.Mode
	if mode == constant.RelayModeUnknown {
		mode = a.mode
	}
	format := meta.Format
	if format == constant.RelayFormatUnknown {
		format = a.format
	}
	if mode != constant.RelayModeChatCompletions {
		return fmt.Errorf("Tencent channel does not support relay mode %d", mode)
	}
	if format != constant.RelayFormatOpenAI {
		return fmt.Errorf("Tencent channel does not support relay format %q", format)
	}
	if meta.Request.Stream != meta.IsStream {
		return errors.New("Tencent request stream flag does not match relay metadata")
	}
	modelName := strings.TrimSpace(meta.ModelName)
	if modelName == "" || len(modelName) > maxModelBytes || !utf8.ValidString(modelName) || containsControl(modelName) {
		return errors.New("Tencent mapped model is invalid")
	}
	return nil
}

func (a *Adaptor) isNative(meta *relaycommon.Meta) bool {
	if meta != nil {
		return strings.Contains(strings.TrimSpace(meta.APIKey), "|")
	}
	return a.native
}

func (a *Adaptor) now() time.Time {
	if a != nil && a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeTokenHubBaseURL(meta *relaycommon.Meta) {
	if meta == nil {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(meta.BaseURL), "/")
	if base == "" || base == defaultNativeBaseURL {
		meta.BaseURL = tokenHubBaseURL
	}
}

func validatedBaseURL(raw string, native bool) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		if native {
			base = defaultNativeBaseURL
		} else {
			base = tokenHubBaseURL
		}
	}
	if len(base) > maxBaseURLBytes || strings.ContainsAny(base, "\\\r\n\x00") {
		return "", errors.New("Tencent base URL is invalid")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("Tencent base URL must be a valid HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("Tencent base URL must not contain credentials, query, or fragment")
	}
	if native && parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return "", errors.New("Tencent native base URL must not contain a path")
	}
	return strings.TrimRight(base, "/"), nil
}

type nativeCredentials struct {
	appID     int64
	secretID  string
	secretKey string
}

func parseNativeCredentials(raw string) (nativeCredentials, error) {
	credential := strings.TrimSpace(raw)
	credential = strings.TrimPrefix(credential, "Bearer ")
	if credential == "" || len(credential) > maxCredentialBytes || containsControl(credential) {
		return nativeCredentials{}, errors.New("Tencent native credential is invalid")
	}
	parts := strings.Split(credential, "|")
	if len(parts) != 3 {
		return nativeCredentials{}, errors.New("Tencent native credential must contain app ID, secret ID, and secret key")
	}
	for _, part := range parts {
		if part == "" || len(part) > maxCredentialComponentBytes || strings.TrimSpace(part) != part || containsControl(part) {
			return nativeCredentials{}, errors.New("Tencent native credential component is invalid")
		}
	}
	appID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || appID <= 0 {
		return nativeCredentials{}, errors.New("Tencent native app ID is invalid")
	}
	return nativeCredentials{appID: appID, secretID: parts[1], secretKey: parts[2]}, nil
}

func parseTokenCredential(raw string) (string, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" || credential != raw || len(credential) > maxCredentialBytes || strings.Contains(credential, "|") || containsControl(credential) {
		return "", errors.New("Tencent TokenHub credential is invalid")
	}
	for _, char := range credential {
		if unicode.IsSpace(char) {
			return "", errors.New("Tencent TokenHub credential is invalid")
		}
	}
	return credential, nil
}

func containsControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func validateNativeRequestFields(request *protocolkit.GeneralOpenAIRequest) error {
	if request == nil {
		return errors.New("Tencent request is nil")
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0) || *request.Temperature < 0 || *request.Temperature > 2) {
		return errors.New("Tencent temperature must be between 0 and 2")
	}
	if request.TopP != nil && (math.IsNaN(*request.TopP) || math.IsInf(*request.TopP, 0) || *request.TopP < 0 || *request.TopP > 1) {
		return errors.New("Tencent top_p must be between 0 and 1")
	}
	if request.N != nil || request.MaxTokens != nil || request.MaxCompletionTokens != nil ||
		request.FrequencyPenalty != nil || request.PresencePenalty != nil || len(request.LogitBias) != 0 ||
		request.Stop != nil || request.ResponseFormat != nil || len(request.Tools) != 0 || request.ToolChoice != nil ||
		request.FunctionCall != nil || request.Functions != nil || request.User != "" || request.Seed != nil ||
		request.ReasoningEffort != "" || request.Reasoning != nil || request.WebSearchOptions != nil || len(request.Metadata) != 0 {
		return errors.New("Tencent native chat request contains unsupported fields")
	}
	allowed := map[string]struct{}{
		"model": {}, "messages": {}, "stream": {}, "temperature": {}, "top_p": {}, "group": {},
	}
	for key := range request.Extra {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("Tencent native chat does not support request field %q", key)
		}
	}
	return nil
}

func validateMessageRole(messages []protocolkit.Message, index int) error {
	role := messages[index].Role
	if index == 0 && role == "system" {
		if len(messages) == 1 {
			return errors.New("Tencent messages must end with a user message")
		}
		return nil
	}
	if role == "system" {
		return errors.New("Tencent system message must be first")
	}
	conversationIndex := index
	if len(messages) > 0 && messages[0].Role == "system" {
		conversationIndex--
	}
	expected := "user"
	if conversationIndex%2 == 1 {
		expected = "assistant"
	}
	if role != expected {
		return fmt.Errorf("Tencent message %d must use role %q", index, expected)
	}
	if index == len(messages)-1 && role != "user" {
		return errors.New("Tencent messages must end with a user message")
	}
	return nil
}

func textContent(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []any:
		var builder strings.Builder
		for _, item := range typed {
			switch part := item.(type) {
			case map[string]any:
				kind, _ := part["type"].(string)
				text, ok := part["text"].(string)
				if kind != protocolkit.ContentTypeText || !ok {
					return "", errors.New("Tencent native chat supports only text message parts")
				}
				builder.WriteString(text)
			case protocolkit.MediaContent:
				if part.Type != protocolkit.ContentTypeText {
					return "", errors.New("Tencent native chat supports only text message parts")
				}
				builder.WriteString(part.Text)
			default:
				return "", errors.New("Tencent native chat supports only text message parts")
			}
		}
		return builder.String(), nil
	case []protocolkit.MediaContent:
		var builder strings.Builder
		for _, part := range typed {
			if part.Type != protocolkit.ContentTypeText {
				return "", errors.New("Tencent native chat supports only text message parts")
			}
			builder.WriteString(part.Text)
		}
		return builder.String(), nil
	default:
		return "", errors.New("Tencent message content must be text")
	}
}

func responseContentType(stream bool) string {
	if stream {
		return "text/event-stream"
	}
	return "application/json"
}

func tc3Authorization(requestURL *url.URL, body []byte, secretID, secretKey string, timestamp int64) (string, error) {
	if requestURL == nil || requestURL.Host == "" || requestURL.RawQuery != "" {
		return "", errors.New("Tencent signing URL is invalid")
	}
	if secretID == "" || secretKey == "" {
		return "", errors.New("Tencent signing credential is invalid")
	}
	canonicalURI := requestURL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	host := strings.ToLower(requestURL.Host)
	canonicalHeaders := "content-type:application/json\n" +
		"host:" + host + "\n" +
		"x-tc-action:" + strings.ToLower(tencentAction) + "\n"
	signedHeaders := "content-type;host;x-tc-action"
	payloadHash := sha256Hex(body)
	canonicalRequest := "POST\n" + canonicalURI + "\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	credentialScope := date + "/" + tencentService + "/tc3_request"
	stringToSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(timestamp, 10) + "\n" + credentialScope + "\n" + sha256Hex([]byte(canonicalRequest))
	secretDate := hmacSHA256([]byte(date), []byte("TC3"+secretKey))
	secretService := hmacSHA256([]byte(tencentService), secretDate)
	secretSigning := hmacSHA256([]byte("tc3_request"), secretService)
	signature := hex.EncodeToString(hmacSHA256([]byte(stringToSign), secretSigning))
	return "TC3-HMAC-SHA256 Credential=" + secretID + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature, nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(value, key []byte) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write(value)
	return hash.Sum(nil)
}

type tencentError struct {
	Code    json.RawMessage `json:"Code"`
	Message string          `json:"Message"`
}

type tencentUsage struct {
	PromptTokens     int `json:"PromptTokens"`
	CompletionTokens int `json:"CompletionTokens"`
	TotalTokens      int `json:"TotalTokens"`
}

type tencentChoice struct {
	FinishReason string         `json:"FinishReason"`
	Message      tencentMessage `json:"Message"`
	Delta        tencentMessage `json:"Delta"`
}

type tencentChatResponse struct {
	Choices []tencentChoice `json:"Choices"`
	Created int64           `json:"Created"`
	ID      string          `json:"Id"`
	Usage   tencentUsage    `json:"Usage"`
	Error   *tencentError   `json:"Error"`
	Note    string          `json:"Note"`
	ReqID   string          `json:"Req_id"`
}

type tencentResponseEnvelope struct {
	Response *tencentChatResponse `json:"Response"`
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return acceptedUsage(meta), fmt.Errorf("read Tencent response: %w", err)
	}
	var envelope tencentResponseEnvelope
	if err := strictJSON(body, &envelope); err != nil || envelope.Response == nil {
		return acceptedUsage(meta), errors.New("Tencent returned an invalid response")
	}
	provider := envelope.Response
	if isProviderError(provider.Error) {
		return nil, tencentProviderError(provider.Error)
	}
	usage, err := validateTencentResponse(provider, meta, false)
	if err != nil {
		return acceptedUsage(meta), err
	}
	choices := make([]protocolkit.ChatCompletionsChoice, 0, len(provider.Choices))
	for index, choice := range provider.Choices {
		finish := normalizedFinishReason(choice.FinishReason)
		choices = append(choices, protocolkit.ChatCompletionsChoice{
			Index:        index,
			Message:      &protocolkit.ChatResponseMessage{Role: "assistant", Content: choice.Message.Content},
			FinishReason: finish,
		})
	}
	created := provider.Created
	if created == 0 {
		created = a.now().Unix()
	}
	output := protocolkit.ChatCompletionsResponse{
		Id: provider.ID, Object: "chat.completion", Created: created,
		Model: clientModel(meta), Choices: choices, Usage: usage,
	}
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return usage, errors.New("encode Tencent client response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(response.StatusCode)
	if _, err := c.Writer.Write(encoded); err != nil {
		return usage, fmt.Errorf("write Tencent client response: %w", err)
	}
	return usage, nil
}

func validateTencentResponse(provider *tencentChatResponse, meta *relaycommon.Meta, stream bool) (*protocolkit.Usage, error) {
	if provider == nil || len(provider.Choices) == 0 || len(provider.Choices) > maxResponseChoices {
		return nil, errors.New("Tencent returned an invalid choice count")
	}
	if provider.Created < 0 || len(provider.ID) > maxProviderIDBytes || len(provider.ReqID) > maxProviderIDBytes ||
		!utf8.ValidString(provider.ID) || !utf8.ValidString(provider.ReqID) || containsControl(provider.ID) || containsControl(provider.ReqID) {
		return nil, errors.New("Tencent returned invalid response metadata")
	}
	completionText := ""
	for _, choice := range provider.Choices {
		message := choice.Message
		if stream {
			message = choice.Delta
		}
		if len(message.Content) > maxResponseTextBytes || !utf8.ValidString(message.Content) {
			return nil, errors.New("Tencent returned invalid response content")
		}
		if message.Role != "" && message.Role != "assistant" {
			return nil, errors.New("Tencent returned an invalid response role")
		}
		if err := validateFinishReason(choice.FinishReason); err != nil {
			return nil, err
		}
		completionText += message.Content
		if len(completionText) > maxResponseTextBytes {
			return nil, errors.New("Tencent returned oversized response content")
		}
	}
	return normalizedUsage(provider.Usage, meta, completionText)
}

func normalizedUsage(provider tencentUsage, meta *relaycommon.Meta, completionText string) (*protocolkit.Usage, error) {
	values := []int{provider.PromptTokens, provider.CompletionTokens, provider.TotalTokens}
	for _, value := range values {
		if value < 0 || int64(value) > appcommon.MaxQuota {
			return nil, errors.New("Tencent returned usage outside the supported range")
		}
	}
	if provider.PromptTokens == 0 && provider.CompletionTokens == 0 && provider.TotalTokens == 0 {
		return estimatedUsage(meta, completionText), nil
	}
	sum := int64(provider.PromptTokens) + int64(provider.CompletionTokens)
	if sum < 0 || sum > appcommon.MaxQuota {
		return nil, errors.New("Tencent returned inconsistent usage")
	}
	if provider.TotalTokens == 0 {
		provider.TotalTokens = int(sum)
	}
	if int64(provider.TotalTokens) < sum {
		return nil, errors.New("Tencent returned inconsistent usage")
	}
	// Billing consumes the prompt/completion split. If the provider reports a
	// larger authoritative total, conservatively assign the unexplained tokens
	// to completion instead of silently undercharging them.
	if int64(provider.TotalTokens) > sum {
		provider.CompletionTokens += provider.TotalTokens - int(sum)
	}
	return &protocolkit.Usage{
		PromptTokens: provider.PromptTokens, CompletionTokens: provider.CompletionTokens, TotalTokens: provider.TotalTokens,
	}, nil
}

func estimatedUsage(meta *relaycommon.Meta, content string) *protocolkit.Usage {
	promptTokens := 0
	if meta != nil {
		promptTokens = meta.PromptTokens
		if promptTokens <= 0 {
			promptTokens = relaycommon.EstimatePromptTokens(meta.Request)
		}
	}
	if promptTokens < 0 || int64(promptTokens) > appcommon.MaxQuota {
		promptTokens = 1
	}
	completionTokens := relaycommon.CountTokens(content)
	if completionTokens < 0 || int64(completionTokens) > appcommon.MaxQuota {
		completionTokens = 0
	}
	total := int64(promptTokens) + int64(completionTokens)
	if total > appcommon.MaxQuota {
		return &protocolkit.Usage{PromptTokens: max(promptTokens, 1), TotalTokens: max(promptTokens, 1)}
	}
	return &protocolkit.Usage{PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: int(total)}
}

func acceptedUsage(meta *relaycommon.Meta) *protocolkit.Usage {
	usage := estimatedUsage(meta, "")
	if usage.PromptTokens <= 0 {
		usage.PromptTokens = 1
		usage.TotalTokens = 1
	}
	return usage
}

func clientModel(meta *relaycommon.Meta) string {
	if meta == nil {
		return ""
	}
	if model := strings.TrimSpace(meta.OriginalModelName); model != "" {
		return model
	}
	return strings.TrimSpace(meta.ModelName)
}

func validateFinishReason(reason string) error {
	if len(reason) > 128 || !utf8.ValidString(reason) || containsControl(reason) {
		return errors.New("Tencent returned an invalid finish reason")
	}
	return nil
}

func normalizedFinishReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "stop"
	}
	return reason
}

func isProviderError(provider *tencentError) bool {
	if provider == nil {
		return false
	}
	code := strings.TrimSpace(string(provider.Code))
	return code != "" && code != "null" && code != "0" && code != `""` || strings.TrimSpace(provider.Message) != ""
}

func tencentProviderError(provider *tencentError) error {
	message := strings.TrimSpace(provider.Message)
	if message == "" || len(message) > maxProviderErrorBytes || !utf8.ValidString(message) {
		message = "Tencent rejected the request"
	}
	code := strings.Trim(strings.TrimSpace(string(provider.Code)), `"`)
	if code == "" || len(code) > 128 || containsControl(code) {
		code = "tencent_error"
	}
	status := http.StatusBadRequest
	if numeric, err := strconv.Atoi(code); err == nil && numeric >= 400 && numeric <= 499 {
		status = numeric
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: "provider_error", Code: code,
	}, status)
}

type streamState struct {
	c       *gin.Context
	meta    *relaycommon.Meta
	status  int
	started bool
	done    bool
	events  int
	text    strings.Builder
	usage   *protocolkit.Usage
}

func (a *Adaptor) streamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	state := &streamState{c: c, meta: meta, status: response.StatusCode}
	limited := &io.LimitedReader{R: response.Body, N: maxStreamBodyBytes + 1}
	scanner := relaycommon.NewUpstreamSSEScanner(limited)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		state.events++
		if state.events > maxStreamEvents {
			return state.failureUsage(), fmt.Errorf("Tencent stream exceeds %d events", maxStreamEvents)
		}
		if data == "[DONE]" {
			state.done = true
			break
		}
		var provider tencentChatResponse
		if err := strictJSON([]byte(data), &provider); err != nil {
			return state.failureUsage(), errors.New("Tencent returned an invalid stream event")
		}
		if isProviderError(provider.Error) {
			if !state.started {
				return nil, tencentProviderError(provider.Error)
			}
			return state.failureUsage(), tencentProviderError(provider.Error)
		}
		if len(provider.Choices) == 0 || len(provider.Choices) > maxResponseChoices {
			return state.failureUsage(), errors.New("Tencent returned an invalid stream choice count")
		}
		if provider.Created < 0 || len(provider.ID) > maxProviderIDBytes || !utf8.ValidString(provider.ID) || containsControl(provider.ID) {
			return state.failureUsage(), errors.New("Tencent returned invalid stream metadata")
		}
		for index, choice := range provider.Choices {
			if len(choice.Delta.Content) > maxResponseTextBytes || !utf8.ValidString(choice.Delta.Content) {
				return state.failureUsage(), errors.New("Tencent returned invalid stream content")
			}
			if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
				return state.failureUsage(), errors.New("Tencent returned an invalid stream role")
			}
			if err := validateFinishReason(choice.FinishReason); err != nil {
				return state.failureUsage(), err
			}
			if len(choice.Delta.Content) > maxResponseTextBytes-state.text.Len() {
				return state.failureUsage(), fmt.Errorf("Tencent streamed text exceeds %d bytes", maxResponseTextBytes)
			}
			state.text.WriteString(choice.Delta.Content)
			finish := (*string)(nil)
			if strings.TrimSpace(choice.FinishReason) != "" {
				normalized := normalizedFinishReason(choice.FinishReason)
				finish = &normalized
				if normalized == "stop" {
					state.done = true
				}
			}
			role := choice.Delta.Role
			if role == "" && !state.started {
				role = "assistant"
			}
			created := provider.Created
			if created == 0 {
				created = a.now().Unix()
			}
			chunk := protocolkit.ChatCompletionsStreamResponse{
				Id: provider.ID, Object: "chat.completion.chunk", Created: created,
				Model: clientModel(meta),
				Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
					Index:        index,
					Delta:        protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: role, Content: choice.Delta.Content},
					FinishReason: finish,
				}},
			}
			if err := state.writeChunk(chunk); err != nil {
				return state.failureUsage(), err
			}
		}
		if provider.Usage.PromptTokens != 0 || provider.Usage.CompletionTokens != 0 || provider.Usage.TotalTokens != 0 {
			usage, err := normalizedUsage(provider.Usage, meta, state.text.String())
			if err != nil {
				return state.failureUsage(), err
			}
			state.usage = usage
		}
		if state.done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return state.failureUsage(), fmt.Errorf("read Tencent event stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if limited.N == 0 {
		return state.failureUsage(), fmt.Errorf("%w: Tencent stream maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, maxStreamBodyBytes)
	}
	if !state.done {
		return state.failureUsage(), errors.New("Tencent stream ended before a completion event")
	}
	if state.usage == nil {
		state.usage = estimatedUsage(meta, state.text.String())
	}
	state.ensureStarted()
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return state.usage, fmt.Errorf("write Tencent stream terminator: %w", err)
	}
	c.Writer.Flush()
	return state.usage, nil
}

func (state *streamState) writeChunk(chunk protocolkit.ChatCompletionsStreamResponse) error {
	body, err := protocolkit.MarshalJSON(chunk)
	if err != nil {
		return errors.New("encode Tencent stream chunk")
	}
	state.ensureStarted()
	if _, err := state.c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
		return fmt.Errorf("write Tencent stream chunk: %w", err)
	}
	state.c.Writer.Flush()
	return nil
}

func (state *streamState) ensureStarted() {
	if state.started {
		return
	}
	state.c.Header("Content-Type", "text/event-stream")
	state.c.Header("Cache-Control", "no-cache")
	state.c.Header("Connection", "keep-alive")
	state.c.Status(state.status)
	state.started = true
}

func (state *streamState) failureUsage() *protocolkit.Usage {
	if state.usage != nil {
		return state.usage
	}
	if state.text.Len() > 0 {
		return estimatedUsage(state.meta, state.text.String())
	}
	return acceptedUsage(state.meta)
}
