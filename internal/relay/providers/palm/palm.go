// Package palm implements the legacy Google PaLM generateMessage contract.
package palm

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ChannelName          = "google palm"
	defaultBaseURL       = "https://generativelanguage.googleapis.com"
	generateMessagePath  = "/v1beta2/models/chat-bison-001:generateMessage"
	maxBaseURLBytes      = 4 << 10
	maxCredentialBytes   = 16 << 10
	maxModelBytes        = 1024
	maxMessages          = 4096
	maxMessageBytes      = 1 << 20
	maxPromptBytes       = 8 << 20
	maxCandidates        = 8
	maxProviderErrorText = 8 << 10
)

var supportedModels = [...]string{"PaLM-2"}

// ModelList returns an owned copy of the reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

type Adaptor struct {
	mode   channelcatalog.RelayMode
	format channelcatalog.RelayFormat
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	return relaycommon.JoinURL(base, generateMessagePath), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("PaLM request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	credential := strings.TrimSpace(meta.APIKey)
	if credential == "" {
		return errors.New("PaLM API key is required")
	}
	if len(credential) > maxCredentialBytes || strings.ContainsAny(credential, "\r\n\x00") {
		return errors.New("PaLM API key is invalid")
	}
	request.Header.Del("Authorization")
	request.Header.Del("api-key")
	request.Header.Set("x-goog-api-key", credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return nil
}

type message struct {
	Author  string `json:"author"`
	Content string `json:"content"`
}

type prompt struct {
	Messages []message `json:"messages"`
}

type generateMessageRequest struct {
	Prompt         prompt   `json:"prompt"`
	Temperature    *float64 `json:"temperature,omitempty"`
	CandidateCount int      `json:"candidateCount,omitempty"`
	TopP           *float64 `json:"topP,omitempty"`
	TopK           *int     `json:"topK,omitempty"`
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if len(meta.Request.Messages) == 0 || len(meta.Request.Messages) > maxMessages {
		return nil, fmt.Errorf("PaLM messages must contain between 1 and %d items", maxMessages)
	}
	if len(meta.Request.Tools) != 0 || meta.Request.ToolChoice != nil || meta.Request.FunctionCall != nil || meta.Request.Functions != nil {
		return nil, errors.New("PaLM generateMessage does not support tools or function calls")
	}
	if meta.Request.ResponseFormat != nil {
		return nil, errors.New("PaLM generateMessage does not support response_format")
	}

	out := generateMessageRequest{Prompt: prompt{Messages: make([]message, 0, len(meta.Request.Messages))}}
	totalBytes := 0
	for _, input := range meta.Request.Messages {
		author := "0"
		switch input.Role {
		case "assistant":
			author = "1"
		case "system", "user":
		default:
			return nil, fmt.Errorf("PaLM does not support message role %q", input.Role)
		}
		content, err := textContent(input.Content)
		if err != nil {
			return nil, err
		}
		if content == "" || len(content) > maxMessageBytes || !utf8.ValidString(content) {
			return nil, errors.New("PaLM message content is empty, oversized, or invalid UTF-8")
		}
		if totalBytes > maxPromptBytes-len(content) {
			return nil, fmt.Errorf("PaLM prompt exceeds %d bytes", maxPromptBytes)
		}
		totalBytes += len(content)
		out.Prompt.Messages = append(out.Prompt.Messages, message{Author: author, Content: content})
	}

	if value := meta.Request.Temperature; value != nil {
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1 {
			return nil, errors.New("PaLM temperature must be between 0 and 1")
		}
		out.Temperature = value
	}
	if value := meta.Request.TopP; value != nil {
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1 {
			return nil, errors.New("PaLM top_p must be between 0 and 1")
		}
		out.TopP = value
	}
	candidateCount := 1
	if meta.Request.N != nil {
		candidateCount = *meta.Request.N
	}
	if candidateCount < 1 || candidateCount > maxCandidates {
		return nil, fmt.Errorf("PaLM n must be between 1 and %d", maxCandidates)
	}
	out.CandidateCount = candidateCount
	if raw, ok := meta.Request.Extra["top_k"]; ok {
		topK, err := boundedInteger(raw, 1, 40)
		if err != nil {
			return nil, fmt.Errorf("PaLM top_k: %w", err)
		}
		out.TopK = &topK
	}

	body, err := protocolkit.MarshalJSON(out)
	if err != nil {
		return nil, fmt.Errorf("encode PaLM request: %w", err)
	}
	if int64(len(body)) > relaycommon.MaxUpstreamJSONBodyBytes {
		return nil, errors.New("PaLM request is too large")
	}
	return body, nil
}

type providerError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

type generateMessageResponse struct {
	Candidates []message     `json:"candidates"`
	Error      providerError `json:"error"`
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("PaLM response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read PaLM response: %w", err)
	}
	var provider generateMessageResponse
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, errors.New("PaLM returned an invalid response")
	}
	if provider.Error.Code != 0 || strings.TrimSpace(provider.Error.Message) != "" {
		return nil, palmProviderError(provider.Error)
	}
	if len(provider.Candidates) == 0 || len(provider.Candidates) > maxCandidates {
		return nil, errors.New("PaLM returned an invalid candidate count")
	}
	for _, candidate := range provider.Candidates {
		if len(candidate.Content) > maxPromptBytes || !utf8.ValidString(candidate.Content) {
			return nil, errors.New("PaLM returned invalid candidate content")
		}
	}
	usage, err := estimatedUsage(meta, provider.Candidates[0].Content)
	if err != nil {
		return usage, err
	}
	if meta.IsStream {
		return writeStreamResponse(c, meta, provider.Candidates[0].Content, usage)
	}
	return writeBlockingResponse(c, response.StatusCode, meta, provider.Candidates, usage)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("PaLM relay metadata is nil")
	}
	mode := a.mode
	if meta.Mode != channelcatalog.RelayModeUnknown {
		mode = meta.Mode
	}
	format := a.format
	if meta.Format != channelcatalog.RelayFormatUnknown {
		format = meta.Format
	}
	if mode != channelcatalog.RelayModeChatCompletions {
		return fmt.Errorf("PaLM channel does not support relay mode %d", mode)
	}
	if format != channelcatalog.RelayFormatOpenAI {
		return fmt.Errorf("PaLM channel does not support relay format %q", format)
	}
	modelName := strings.TrimSpace(meta.ModelName)
	if modelName == "" || len(modelName) > maxModelBytes || strings.ContainsAny(modelName, "\r\n\x00") {
		return errors.New("PaLM mapped model is invalid")
	}
	return nil
}

func validateBaseURL(raw string) error {
	if raw == "" || len(raw) > maxBaseURLBytes {
		return errors.New("PaLM base URL is missing or too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return errors.New("PaLM base URL must be a valid HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("PaLM base URL must not contain credentials, query, or fragment")
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
				if kind != "text" || !ok {
					return "", errors.New("PaLM supports only text message parts")
				}
				builder.WriteString(text)
			case protocolkit.MediaContent:
				if part.Type != protocolkit.ContentTypeText {
					return "", errors.New("PaLM supports only text message parts")
				}
				builder.WriteString(part.Text)
			default:
				return "", errors.New("PaLM supports only text message parts")
			}
		}
		return builder.String(), nil
	default:
		return "", errors.New("PaLM message content must be text")
	}
}

func boundedInteger(value any, minimum, maximum int) (int, error) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, errors.New("must be an integer")
		}
		number = parsed
	default:
		return 0, errors.New("must be an integer")
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < float64(minimum) || number > float64(maximum) {
		return 0, fmt.Errorf("must be an integer between %d and %d", minimum, maximum)
	}
	return int(number), nil
}

func palmProviderError(provider providerError) error {
	message := strings.TrimSpace(provider.Message)
	if message == "" || len(message) > maxProviderErrorText || !utf8.ValidString(message) {
		message = "PaLM returned an error"
	}
	status := strings.TrimSpace(provider.Status)
	if len(status) > 128 || strings.ContainsAny(status, "\r\n\x00") {
		status = "provider_error"
	}
	code := ""
	if provider.Code != 0 {
		code = strconv.Itoa(provider.Code)
	}
	statusCode := http.StatusBadGateway
	if provider.Code >= http.StatusBadRequest && provider.Code <= 599 {
		statusCode = provider.Code
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: status, Code: code,
	}, statusCode)
}

func estimatedUsage(meta *relaycommon.Meta, content string) (*protocolkit.Usage, error) {
	promptTokens := meta.PromptTokens
	if promptTokens == 0 {
		promptTokens = relaycommon.EstimatePromptTokens(meta.Request)
	}
	completionTokens := relaycommon.CountTokens(content)
	total := int64(promptTokens) + int64(completionTokens)
	usage := &protocolkit.Usage{PromptTokens: promptTokens, CompletionTokens: completionTokens}
	if promptTokens < 0 || completionTokens < 0 || total < 0 || total > quotamath.MaxQuota {
		usage.TotalTokens = -1
		return usage, errors.New("PaLM estimated usage is outside the supported range")
	}
	usage.TotalTokens = int(total)
	return usage, nil
}

func responseModel(meta *relaycommon.Meta) string {
	if strings.TrimSpace(meta.OriginalModelName) != "" {
		return meta.OriginalModelName
	}
	return meta.ModelName
}

func writeBlockingResponse(c *gin.Context, status int, meta *relaycommon.Meta, candidates []message, usage *protocolkit.Usage) (*protocolkit.Usage, error) {
	choices := make([]protocolkit.ChatCompletionsChoice, 0, len(candidates))
	for index, candidate := range candidates {
		choices = append(choices, protocolkit.ChatCompletionsChoice{
			Index: index, Message: &protocolkit.ChatResponseMessage{Role: "assistant", Content: candidate.Content}, FinishReason: "stop",
		})
	}
	output := protocolkit.ChatCompletionsResponse{
		Id: "chatcmpl-" + cryptoutil.BestEffortRandomAlphanumeric(24), Object: "chat.completion",
		Created: time.Now().Unix(), Model: responseModel(meta), Choices: choices, Usage: usage,
	}
	body, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return usage, errors.New("encode PaLM client response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(status)
	if _, err := c.Writer.Write(body); err != nil {
		return usage, fmt.Errorf("write PaLM client response: %w", err)
	}
	return usage, nil
}

func writeStreamResponse(c *gin.Context, meta *relaycommon.Meta, content string, usage *protocolkit.Usage) (*protocolkit.Usage, error) {
	finish := "stop"
	chunk := protocolkit.ChatCompletionsStreamResponse{
		Id: "chatcmpl-" + cryptoutil.BestEffortRandomAlphanumeric(24), Object: "chat.completion.chunk",
		Created: time.Now().Unix(), Model: responseModel(meta),
		Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
			Index: 0, Delta: protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: content}, FinishReason: &finish,
		}},
	}
	body, err := protocolkit.MarshalJSON(chunk)
	if err != nil {
		return usage, errors.New("encode PaLM stream response")
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)
	if _, err := c.Writer.WriteString("data: " + string(body) + "\n\ndata: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write PaLM stream response: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}
