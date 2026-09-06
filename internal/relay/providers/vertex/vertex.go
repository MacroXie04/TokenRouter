// Package vertex implements Google Vertex AI publisher-model routing and
// service-account/API-key authentication.
package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	vertexAnthropicVersion = "vertex-2023-10-16"
	maxMessages            = 4096
	maxMessageBytes        = 1 << 20
	maxPromptBytes         = 8 << 20
	maxTools               = 1024
	maxCandidates          = 8
	maxRequestBytes        = 16 << 20
)

type tokenProvider func(context.Context, string, Credentials) (string, error)

// TaskTokenProvider permits deterministic task transports while production
// callers leave it nil and use the bounded service-account OAuth cache.
type TaskTokenProvider func(context.Context, string, Credentials) (string, error)

type Adaptor struct {
	mode          requestMode
	relayMode     channelcatalog.RelayMode
	credentials   *Credentials
	keyType       string
	apiKey        string
	tokenProvider tokenProvider
}

// SetTaskTokenProvider configures a caller-owned adapter instance. Adapters
// are request-scoped, so this does not mutate global authentication state.
func (a *Adaptor) SetTaskTokenProvider(provider TaskTokenProvider) {
	if a == nil || provider == nil {
		return
	}
	a.tokenProvider = tokenProvider(provider)
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = requestModeGemini
	a.relayMode = channelcatalog.RelayModeUnknown
	a.credentials = nil
	a.keyType = ""
	a.apiKey = ""
	if meta != nil {
		a.mode = modeForModel(meta.ModelName)
		a.relayMode = meta.Mode
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validateMeta(meta); err != nil {
		return "", err
	}
	if err := a.loadCredential(meta); err != nil {
		return "", err
	}
	region, err := modelRegion(meta)
	if err != nil {
		return "", err
	}
	projectID := ""
	if a.credentials != nil {
		projectID = a.credentials.ProjectID
	}
	model := strings.TrimSpace(meta.ModelName)
	if a.mode == requestModeGemini {
		model = relaycommon.PrepareGeminiRequest(nil, meta.OriginalModelName, model, false)
	} else if a.mode == requestModeClaude {
		model = relaycommon.PrepareClaudeRequest(nil, meta.OriginalModelName, model, true)
	}
	base := strings.TrimSpace(meta.BaseURL)
	var requestURL string
	switch a.mode {
	case requestModeGemini:
		action := "generateContent"
		if meta.IsStream {
			action = "streamGenerateContent"
		}
		if strings.HasPrefix(strings.ToLower(model), "imagen") {
			action = "predict"
		}
		requestURL, err = publisherModelURL(base, defaultAPIVersion, projectID, region, "google", model, action)
	case requestModeClaude:
		action := "rawPredict"
		if meta.IsStream {
			action = "streamRawPredict"
		}
		requestURL, err = publisherModelURL(base, defaultAPIVersion, projectID, region, "anthropic", vertexClaudeModel(model), action)
	case requestModeOpenSource:
		if a.keyType == vertexKeyTypeAPIKey {
			return "", errors.New("Vertex AI API-key mode does not support open-source MaaS chat")
		}
		requestURL, err = openSourceChatURL(base, projectID, region)
	default:
		err = errors.New("Vertex AI request mode is invalid")
	}
	if err != nil {
		return "", err
	}
	if meta.IsStream && (a.mode == requestModeGemini || a.mode == requestModeClaude) {
		requestURL, err = addSSEQuery(requestURL)
		if err != nil {
			return "", err
		}
	}
	if a.keyType == vertexKeyTypeAPIKey {
		requestURL, err = addAPIKey(requestURL, a.apiKey)
	}
	return requestURL, err
}

// GetTaskRequestURL builds the two Vertex Veo long-running-operation routes.
// Tasks are intentionally service-account-only, matching the reference task
// contract. operationProject is required for polling and is fenced against
// the credential project before any token or provider request is made.
func (a *Adaptor) GetTaskRequestURL(meta *relaycommon.Meta, action, operationProject string) (string, error) {
	if meta == nil || meta.Channel == nil {
		return "", errors.New("Vertex AI task metadata is nil")
	}
	if err := validateModel(meta.ModelName); err != nil {
		return "", err
	}
	if action != "predictLongRunning" && action != "fetchPredictOperation" {
		return "", errors.New("Vertex AI task action is invalid")
	}
	if err := a.loadCredential(meta); err != nil {
		return "", err
	}
	if a.keyType != vertexKeyTypeJSON || a.credentials == nil {
		return "", errors.New("Vertex AI Veo tasks require a service-account credential")
	}
	region, err := modelRegion(meta)
	if err != nil {
		return "", err
	}
	projectID := a.credentials.ProjectID
	if operationProject != "" {
		if err := validateProjectID(operationProject); err != nil {
			return "", err
		}
		if operationProject != projectID {
			return "", errors.New("Vertex AI operation project does not match the service account")
		}
		projectID = operationProject
	}
	return publisherModelURL(strings.TrimSpace(meta.BaseURL), defaultAPIVersion, projectID, region,
		"google", strings.TrimSpace(meta.ModelName), action)
}

// TaskProjectID returns the validated project loaded by GetTaskRequestURL.
func (a *Adaptor) TaskProjectID() string {
	if a == nil || a.credentials == nil || a.keyType != vertexKeyTypeJSON {
		return ""
	}
	return a.credentials.ProjectID
}

// ValidateTaskChannelSettings rejects API-key task configuration before the
// durable lifecycle reserves or exposes any channel credential.
func ValidateTaskChannelSettings(otherSettings string) error {
	keyType, err := vertexKeyType(&relaycommon.Meta{Channel: &model.Channel{OtherSettings: otherSettings}})
	if err != nil {
		return err
	}
	if keyType != vertexKeyTypeJSON {
		return errors.New("Vertex AI Veo tasks require a service-account credential")
	}
	return nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Vertex AI request is nil")
	}
	if err := a.validateMeta(meta); err != nil {
		return err
	}
	if a.keyType == "" {
		if err := a.loadCredential(meta); err != nil {
			return err
		}
	}
	request.Header.Del("Authorization")
	request.Header.Del("x-api-key")
	request.Header.Del("api-key")
	request.Header.Del("x-goog-api-key")
	request.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	if a.mode == requestModeClaude && meta.ClientHeaders != nil {
		beta := strings.TrimSpace(meta.ClientHeaders.Get("anthropic-beta"))
		if beta != "" {
			if len(beta) > 8<<10 || strings.ContainsAny(beta, "\r\n\x00") {
				return errors.New("Vertex AI anthropic-beta header is invalid")
			}
			request.Header.Set("anthropic-beta", beta)
		}
	}
	if a.keyType == vertexKeyTypeAPIKey {
		return nil
	}
	provider := a.tokenProvider
	if provider == nil {
		provider = cachedServiceAccountToken
	}
	ctx := request.Context()
	if meta.Context != nil {
		ctx = meta.Context
	}
	token, err := provider(ctx, meta.APIKey, *a.credentials)
	if err != nil {
		return sanitizeOAuthError(err, meta.APIKey)
	}
	if token == "" || len(token) > maxAccessTokenBytes || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("Vertex AI OAuth token is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("x-goog-user-project", a.credentials.ProjectID)
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validateMeta(meta); err != nil {
		return nil, err
	}
	if len(meta.RawBody) > maxRequestBytes {
		return nil, errors.New("Vertex AI request is too large")
	}
	if len(meta.RawBody) > 0 {
		if err := rejectDuplicateJSONKeys(meta.RawBody); err != nil {
			return nil, fmt.Errorf("Vertex AI request JSON is invalid: %w", err)
		}
	}
	if isNativeClaudeRequest(meta) {
		if a.mode != requestModeClaude {
			return nil, errors.New("Vertex AI native Claude routes require a Claude model")
		}
		var request protocolkit.ClaudeRequest
		if err := strictJSON(meta.RawBody, &request, false); err != nil {
			return nil, errors.New("Vertex AI native Claude request is invalid")
		}
		if err := validateClaudeRequest(&request); err != nil {
			return nil, err
		}
		var envelope map[string]json.RawMessage
		if err := strictJSON(meta.RawBody, &envelope, false); err != nil || envelope == nil {
			return nil, errors.New("Vertex AI native Claude request is invalid")
		}
		delete(envelope, "model")
		version, err := protocolkit.MarshalJSON(vertexAnthropicVersion)
		if err != nil {
			return nil, errors.New("encode Vertex AI Anthropic version")
		}
		envelope["anthropic_version"] = version
		if err := applyNativeClaudeModelVariant(envelope, &request, meta.ModelName); err != nil {
			return nil, err
		}
		return marshalBoundedRequest(envelope)
	}
	if isNativeGeminiRequest(meta) {
		if a.mode != requestModeGemini || strings.HasPrefix(strings.ToLower(meta.ModelName), "imagen") {
			return nil, errors.New("Vertex AI native Gemini routes require a generateContent model")
		}
		var request protocolkit.GeminiChatRequest
		if err := strictJSON(meta.RawBody, &request, false); err != nil {
			return nil, errors.New("Vertex AI native Gemini request is invalid")
		}
		request.Model = ""
		if err := validateGeminiRequest(&request); err != nil {
			return nil, err
		}
		// Preserve native Gemini fields that this binary does not yet model while
		// still removing the client-controlled body model. Routing is exclusively
		// derived from the validated channel mapping and URL path.
		var envelope map[string]json.RawMessage
		if err := strictJSON(meta.RawBody, &envelope, false); err != nil || envelope == nil {
			return nil, errors.New("Vertex AI native Gemini request is invalid")
		}
		delete(envelope, "model")
		return marshalBoundedRequest(envelope)
	}
	if err := validateOpenAIRequest(meta); err != nil {
		return nil, err
	}
	switch a.mode {
	case requestModeGemini:
		if strings.HasPrefix(strings.ToLower(meta.ModelName), "imagen") {
			return nil, errors.New("Vertex AI Imagen is not supported on the chat-completions adapter")
		}
		request := protocolkit.OpenAIRequestToGeminiRequest(meta.Request)
		_ = relaycommon.PrepareGeminiRequest(request, meta.OriginalModelName, meta.ModelName, true)
		request.Model = ""
		if err := validateGeminiRequest(request); err != nil {
			return nil, err
		}
		return marshalBoundedRequest(request)
	case requestModeClaude:
		request := protocolkit.OpenAIRequestToClaudeRequest(meta.Request)
		request.Stream = meta.IsStream
		model := relaycommon.PrepareClaudeRequest(request, meta.OriginalModelName, meta.ModelName,
			meta.Request.MaxTokens != nil || meta.Request.MaxCompletionTokens != nil)
		providerRequest := vertexClaudeRequestFrom(request)
		if err := applyConvertedClaudeModelVariant(providerRequest, model, meta.Request.ReasoningEffort); err != nil {
			return nil, err
		}
		return marshalBoundedRequest(providerRequest)
	case requestModeOpenSource:
		delegate := &openai.Adaptor{ChannelType: channelcatalog.ChannelTypeOpenAI, Format: channelcatalog.RelayFormatOpenAI, Mode: channelcatalog.RelayModeChatCompletions}
		return delegate.ConvertRequest(meta)
	default:
		return nil, errors.New("Vertex AI request mode is invalid")
	}
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Vertex AI response is nil")
	}
	if err := a.validateMeta(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	var usage *protocolkit.Usage
	var err error
	switch a.mode {
	case requestModeGemini:
		if meta.IsStream {
			usage, err = a.geminiStreamResponse(c, response, meta)
		} else {
			usage, err = a.geminiNonStreamResponse(c, response, meta)
		}
	case requestModeClaude:
		if isNativeGeminiRequest(meta) && !isNativeClaudeRequest(meta) {
			return acceptedUsage(meta), errors.New("Vertex AI Claude cannot serve a native Gemini request")
		}
		if meta.IsStream {
			if isNativeClaudeRequest(meta) {
				usage, err = a.claudeNativeStreamResponse(c, response, meta)
			} else {
				usage, err = a.claudeStreamResponse(c, response, meta)
			}
		} else {
			usage, err = a.claudeNonStreamResponse(c, response, meta)
		}
	case requestModeOpenSource:
		delegate := &openai.Adaptor{ChannelType: channelcatalog.ChannelTypeOpenAI, Format: channelcatalog.RelayFormatOpenAI, Mode: channelcatalog.RelayModeChatCompletions}
		usage, err = delegate.DoResponse(c, response, meta)
	default:
		err = errors.New("Vertex AI response mode is invalid")
	}
	if err != nil && usage == nil {
		var upstream *relaycommon.UpstreamError
		if errors.As(err, &upstream) {
			return nil, err
		}
		// A successful HTTP status means work may already have completed. Local
		// decode/write failures therefore carry conservative usage so the relay
		// settles once instead of retrying or refunding accepted work.
		usage = acceptedUsage(meta)
	}
	return usage, err
}

func (a *Adaptor) validateMeta(meta *relaycommon.Meta) error {
	if meta == nil || meta.Channel == nil || meta.Request == nil {
		return errors.New("Vertex AI relay metadata is nil")
	}
	mode := a.relayMode
	if meta.Mode != channelcatalog.RelayModeUnknown {
		mode = meta.Mode
	}
	if mode != channelcatalog.RelayModeChatCompletions && mode != channelcatalog.RelayModeGemini {
		return fmt.Errorf("Vertex AI does not support relay mode %d", mode)
	}
	if err := validateModel(meta.ModelName); err != nil {
		return err
	}
	return nil
}

func (a *Adaptor) loadCredential(meta *relaycommon.Meta) error {
	keyType, err := vertexKeyType(meta)
	if err != nil {
		return err
	}
	a.keyType = keyType
	if keyType == vertexKeyTypeAPIKey {
		a.apiKey, err = validateAPIKey(meta.APIKey)
		a.credentials = nil
		return err
	}
	credentials, err := parseServiceAccount(meta.APIKey)
	if err != nil {
		return err
	}
	a.credentials = &credentials
	a.apiKey = ""
	return nil
}

func modelRegion(meta *relaycommon.Meta) (string, error) {
	raw := ""
	model := ""
	if meta != nil {
		model = meta.OriginalModelName
		if meta.Channel != nil {
			raw = strings.TrimSpace(meta.Channel.Other)
		}
	}
	if raw == "" {
		return defaultRegion, nil
	}
	if strings.HasPrefix(raw, "{") {
		if len(raw) > 64<<10 {
			return "", errors.New("Vertex AI region mapping is too large")
		}
		var regions map[string]string
		if err := strictJSON([]byte(raw), &regions, false); err != nil {
			return "", errors.New("Vertex AI region mapping is invalid")
		}
		region := strings.TrimSpace(regions[model])
		if region == "" {
			region = strings.TrimSpace(regions["default"])
		}
		return normalizeRegion(region)
	}
	return normalizeRegion(raw)
}

func isNativeGeminiRequest(meta *relaycommon.Meta) bool {
	if meta == nil {
		return false
	}
	return meta.Mode == channelcatalog.RelayModeGemini || strings.Contains(meta.RequestPath, "/models/")
}

func isNativeClaudeRequest(meta *relaycommon.Meta) bool {
	if meta == nil {
		return false
	}
	return meta.Format == channelcatalog.RelayFormatClaude || strings.TrimRight(meta.RequestPath, "/") == "/v1/messages"
}

func validateOpenAIRequest(meta *relaycommon.Meta) error {
	request := meta.Request
	if request == nil || len(request.Messages) == 0 || len(request.Messages) > maxMessages {
		return fmt.Errorf("Vertex AI messages must contain between 1 and %d items", maxMessages)
	}
	if len(request.Tools) > maxTools {
		return fmt.Errorf("Vertex AI tools exceed %d items", maxTools)
	}
	total := 0
	for _, message := range request.Messages {
		switch message.Role {
		case "system", "user", "assistant", "tool":
		default:
			return fmt.Errorf("Vertex AI does not support message role %q", message.Role)
		}
		encoded, err := protocolkit.MarshalJSON(message)
		if err != nil || len(encoded) > maxMessageBytes || !utf8.Valid(encoded) {
			return errors.New("Vertex AI message is invalid or too large")
		}
		if total > maxPromptBytes-len(encoded) {
			return fmt.Errorf("Vertex AI prompt exceeds %d bytes", maxPromptBytes)
		}
		total += len(encoded)
	}
	if err := boundedFloat("temperature", request.Temperature, 0, 2); err != nil {
		return err
	}
	if err := boundedFloat("top_p", request.TopP, 0, 1); err != nil {
		return err
	}
	if request.N != nil && (*request.N < 1 || *request.N > maxCandidates) {
		return fmt.Errorf("Vertex AI n must be between 1 and %d", maxCandidates)
	}
	for name, value := range map[string]*int{
		"max_tokens": request.MaxTokens, "max_completion_tokens": request.MaxCompletionTokens,
	} {
		if value != nil && (*value < 1 || int64(*value) > quotamath.MaxQuota) {
			return fmt.Errorf("Vertex AI %s is outside the supported range", name)
		}
	}
	return nil
}

func boundedFloat(name string, value *float64, minimum, maximum float64) error {
	if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < minimum || *value > maximum) {
		return fmt.Errorf("Vertex AI %s must be between %g and %g", name, minimum, maximum)
	}
	return nil
}

func validateGeminiRequest(request *protocolkit.GeminiChatRequest) error {
	if request == nil || len(request.Contents) == 0 || len(request.Contents) > maxMessages {
		return fmt.Errorf("Vertex AI contents must contain between 1 and %d items", maxMessages)
	}
	total := 0
	for _, content := range request.Contents {
		if content.Role != "" && content.Role != "user" && content.Role != "model" && content.Role != "system" {
			return errors.New("Vertex AI content role is invalid")
		}
		if len(content.Parts) == 0 || len(content.Parts) > maxMessages {
			return errors.New("Vertex AI content parts are invalid")
		}
		encoded, err := protocolkit.MarshalJSON(content)
		if err != nil || len(encoded) > maxMessageBytes {
			return errors.New("Vertex AI content is invalid or too large")
		}
		if total > maxPromptBytes-len(encoded) {
			return fmt.Errorf("Vertex AI prompt exceeds %d bytes", maxPromptBytes)
		}
		total += len(encoded)
	}
	if len(request.Tools) > maxTools {
		return fmt.Errorf("Vertex AI tools exceed %d items", maxTools)
	}
	if config := request.GenerationConfig; config != nil {
		if err := boundedFloat("temperature", config.Temperature, 0, 2); err != nil {
			return err
		}
		if err := boundedFloat("topP", config.TopP, 0, 1); err != nil {
			return err
		}
		if config.TopK != nil && (math.IsNaN(*config.TopK) || math.IsInf(*config.TopK, 0) || *config.TopK < 0 || *config.TopK > float64(quotamath.MaxQuota)) {
			return errors.New("Vertex AI topK is outside the supported range")
		}
		if config.MaxOutputTokens != nil && (*config.MaxOutputTokens < 1 || int64(*config.MaxOutputTokens) > quotamath.MaxQuota) {
			return errors.New("Vertex AI maxOutputTokens is outside the supported range")
		}
		if config.CandidateCount != nil && (*config.CandidateCount < 1 || *config.CandidateCount > maxCandidates) {
			return fmt.Errorf("Vertex AI candidateCount must be between 1 and %d", maxCandidates)
		}
	}
	return nil
}

func validateClaudeRequest(request *protocolkit.ClaudeRequest) error {
	if request == nil || len(request.Messages) == 0 || len(request.Messages) > maxMessages {
		return fmt.Errorf("Vertex AI Claude messages must contain between 1 and %d items", maxMessages)
	}
	if request.MaxTokens < 1 || int64(request.MaxTokens) > quotamath.MaxQuota {
		return errors.New("Vertex AI Claude max_tokens is outside the supported range")
	}
	if len(request.Tools) > maxTools || len(request.StopSequences) > maxMessages {
		return errors.New("Vertex AI Claude request contains too many tools or stop sequences")
	}
	total := 0
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return errors.New("Vertex AI Claude message role is invalid")
		}
		encoded, err := protocolkit.MarshalJSON(message)
		if err != nil || len(encoded) > maxMessageBytes || total > maxPromptBytes-len(encoded) {
			return errors.New("Vertex AI Claude message is invalid or too large")
		}
		total += len(encoded)
	}
	if request.System != nil {
		encoded, err := protocolkit.MarshalJSON(request.System)
		if err != nil || len(encoded) > maxMessageBytes || total > maxPromptBytes-len(encoded) {
			return errors.New("Vertex AI Claude system prompt is invalid or too large")
		}
		total += len(encoded)
	}
	if err := boundedFloat("Claude temperature", request.Temperature, 0, 1); err != nil {
		return err
	}
	if err := boundedFloat("Claude top_p", request.TopP, 0, 1); err != nil {
		return err
	}
	if request.TopK != nil && (*request.TopK < 0 || int64(*request.TopK) > quotamath.MaxQuota) {
		return errors.New("Vertex AI Claude top_k is outside the supported range")
	}
	if request.Thinking != nil && (request.Thinking.BudgetTokens < 0 || int64(request.Thinking.BudgetTokens) > quotamath.MaxQuota) {
		return errors.New("Vertex AI Claude thinking budget is outside the supported range")
	}
	return nil
}

func marshalBoundedRequest(value any) ([]byte, error) {
	body, err := protocolkit.MarshalJSON(value)
	if err != nil {
		return nil, errors.New("encode Vertex AI request")
	}
	if len(body) > maxRequestBytes {
		return nil, errors.New("Vertex AI request is too large")
	}
	return body, nil
}

type vertexClaudeRequest struct {
	AnthropicVersion string                      `json:"anthropic_version"`
	Messages         []protocolkit.ClaudeMessage `json:"messages"`
	System           any                         `json:"system,omitempty"`
	MaxTokens        int                         `json:"max_tokens"`
	StopSequences    []string                    `json:"stop_sequences,omitempty"`
	Stream           bool                        `json:"stream,omitempty"`
	Temperature      *float64                    `json:"temperature,omitempty"`
	TopP             *float64                    `json:"top_p,omitempty"`
	TopK             *int                        `json:"top_k,omitempty"`
	Tools            []protocolkit.Tool          `json:"tools,omitempty"`
	ToolChoice       any                         `json:"tool_choice,omitempty"`
	Thinking         *vertexClaudeThinking       `json:"thinking,omitempty"`
	OutputConfig     json.RawMessage             `json:"output_config,omitempty"`
}

// vertexClaudeThinking uses a pointer for budget_tokens because adaptive
// thinking rejects a numeric budget, including zero. The public protocol DTO
// keeps an integer for compatibility, so the provider envelope normalizes it.
type vertexClaudeThinking struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

func vertexClaudeRequestFrom(request *protocolkit.ClaudeRequest) *vertexClaudeRequest {
	return &vertexClaudeRequest{
		AnthropicVersion: vertexAnthropicVersion,
		Messages:         request.Messages,
		System:           request.System,
		MaxTokens:        request.MaxTokens,
		StopSequences:    request.StopSequences,
		Stream:           request.Stream,
		Temperature:      request.Temperature,
		TopP:             request.TopP,
		TopK:             request.TopK,
		Tools:            request.Tools,
		ToolChoice:       request.ToolChoice,
		Thinking:         vertexThinkingFrom(request.Thinking),
		OutputConfig:     append(json.RawMessage(nil), request.OutputConfig...),
	}
}

func vertexThinkingFrom(thinking *protocolkit.Thinking) *vertexClaudeThinking {
	if thinking == nil {
		return nil
	}
	converted := &vertexClaudeThinking{Type: thinking.Type, Display: thinking.Display}
	if thinking.BudgetTokens != 0 {
		budget := thinking.BudgetTokens
		converted.BudgetTokens = &budget
	}
	return converted
}

func applyConvertedClaudeModelVariant(request *vertexClaudeRequest, model, explicitEffort string) error {
	if request == nil {
		return errors.New("Vertex AI Claude request is nil")
	}
	if base, effort, ok := claudeEffortVariant(model); ok {
		request.Thinking = &vertexClaudeThinking{Type: "adaptive"}
		request.OutputConfig = claudeEffortOutputConfig(effort)
		if claudeUsesAdaptiveDisplay(base) {
			request.Thinking.Display = "summarized"
			request.Temperature = nil
			request.TopP = nil
			request.TopK = nil
		} else {
			one := 1.0
			request.Temperature = &one
			request.TopP = nil
		}
	} else if strings.HasSuffix(model, "-thinking") && request.Thinking == nil {
		base := strings.TrimSuffix(model, "-thinking")
		if claudeUsesAdaptiveDisplay(base) {
			request.Thinking = &vertexClaudeThinking{Type: "adaptive", Display: "summarized"}
			request.OutputConfig = claudeEffortOutputConfig("high")
			request.Temperature = nil
			request.TopP = nil
			request.TopK = nil
		} else {
			if request.MaxTokens < 1280 {
				request.MaxTokens = 1280
			}
			budget := request.MaxTokens * 8 / 10
			request.Thinking = &vertexClaudeThinking{Type: "enabled", BudgetTokens: &budget}
			one := 1.0
			request.Temperature = &one
			request.TopP = nil
		}
	}

	// OpenAI reasoning_effort is applied after suffix adaptation in the
	// reference conversion pipeline and therefore intentionally wins.
	var budget int
	switch strings.TrimSpace(explicitEffort) {
	case "low":
		budget = 1280
	case "medium":
		budget = 2048
	case "high":
		budget = 4096
	}
	if budget != 0 {
		request.Thinking = &vertexClaudeThinking{Type: "enabled", BudgetTokens: &budget}
	}
	return nil
}

func applyNativeClaudeModelVariant(envelope map[string]json.RawMessage, request *protocolkit.ClaudeRequest, model string) error {
	if envelope == nil || request == nil {
		return errors.New("Vertex AI native Claude request is nil")
	}
	if base, effort, ok := claudeEffortVariant(model); ok {
		thinking := vertexClaudeThinking{Type: "adaptive"}
		if claudeUsesAdaptiveDisplay(base) {
			thinking.Display = "summarized"
			delete(envelope, "temperature")
			delete(envelope, "top_p")
			delete(envelope, "top_k")
		} else if err := setClaudeEnvelopeField(envelope, "temperature", 1.0); err != nil {
			return err
		}
		if err := setClaudeEnvelopeField(envelope, "thinking", thinking); err != nil {
			return err
		}
		return setClaudeEnvelopeRawField(envelope, "output_config", claudeEffortOutputConfig(effort))
	}
	if !strings.HasSuffix(model, "-thinking") || request.Thinking != nil {
		return nil
	}
	base := strings.TrimSuffix(model, "-thinking")
	if claudeUsesAdaptiveDisplay(base) {
		delete(envelope, "temperature")
		delete(envelope, "top_p")
		delete(envelope, "top_k")
		if err := setClaudeEnvelopeField(envelope, "thinking", vertexClaudeThinking{Type: "adaptive", Display: "summarized"}); err != nil {
			return err
		}
		return setClaudeEnvelopeRawField(envelope, "output_config", claudeEffortOutputConfig("high"))
	}
	maxTokens := request.MaxTokens
	if maxTokens < 1280 {
		maxTokens = 1280
		if err := setClaudeEnvelopeField(envelope, "max_tokens", maxTokens); err != nil {
			return err
		}
	}
	budget := maxTokens * 8 / 10
	if err := setClaudeEnvelopeField(envelope, "thinking", vertexClaudeThinking{Type: "enabled", BudgetTokens: &budget}); err != nil {
		return err
	}
	return setClaudeEnvelopeField(envelope, "temperature", 1.0)
}

func claudeEffortOutputConfig(effort string) json.RawMessage {
	return json.RawMessage(`{"effort":"` + effort + `"}`)
}

func setClaudeEnvelopeField(envelope map[string]json.RawMessage, name string, value any) error {
	raw, err := protocolkit.MarshalJSON(value)
	if err != nil {
		return fmt.Errorf("encode Vertex AI Claude %s: %w", name, err)
	}
	envelope[name] = raw
	return nil
}

func setClaudeEnvelopeRawField(envelope map[string]json.RawMessage, name string, raw json.RawMessage) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return fmt.Errorf("encode Vertex AI Claude %s", name)
	}
	envelope[name] = append(json.RawMessage(nil), raw...)
	return nil
}
