// Package ali implements Alibaba Cloud Model Studio (DashScope) relay
// contracts. DashScope exposes OpenAI-compatible text endpoints alongside
// provider-native rerank, image, and Anthropic Messages routes, so it must not
// be treated as a generic OpenAI base URL.
package ali

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	ChannelName                       = "ali"
	defaultBaseURL                    = "https://dashscope.aliyuncs.com"
	aliAnthropicMessagesModelsEnv     = "ALI_ANTHROPIC_MESSAGES_MODELS"
	defaultAliAnthropicMessagesModels = "qwen,deepseek-v4,kimi,glm,minimax-m"
	maxDashScopePluginHeaderBytes     = 8 << 10
	maxAliRerankDocuments             = 10_000
)

var supportedModels = [...]string{
	"qwen-turbo",
	"qwen-plus",
	"qwen-max",
	"qwen-max-longcontext",
	"qwq-32b",
	"qwen3-235b-a22b",
	"text-embedding-v1",
	"gte-rerank-v2",
}

// ModelList returns an owned copy of the reference DashScope model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// SupportsAnthropicMessages reports whether a mapped DashScope model should
// use Alibaba's native Anthropic-compatible endpoint. Patterns are deliberately
// case-insensitive substrings, matching the reference configuration contract.
func SupportsAnthropicMessages(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	if normalized == "" {
		return false
	}
	configured := defaultAliAnthropicMessagesModels
	if value, present := os.LookupEnv(aliAnthropicMessagesModelsEnv); present {
		configured = value
	}
	for _, item := range strings.Split(configured, ",") {
		pattern := strings.ToLower(strings.TrimSpace(item))
		if pattern != "" && strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

// Adaptor implements the explicit DashScope provider boundary.
type Adaptor struct {
	mode      channelcatalog.RelayMode
	format    channelcatalog.RelayFormat
	imageSync bool
	openAI    openai.Adaptor
	auxClient *http.Client
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	a.imageSync = false
	if meta == nil {
		return
	}
	a.mode = meta.Mode
	a.format = meta.Format
	a.imageSync = imageRequestIsSynchronous(meta)
	if a.auxClient == nil {
		a.auxClient = newAuxiliaryClient()
	}
	a.openAI.Init(meta)
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

	path := ""
	if a.relayFormat(meta) == channelcatalog.RelayFormatClaude {
		if SupportsAnthropicMessages(meta.ModelName) {
			path = "/apps/anthropic/v1/messages"
		} else {
			path = "/compatible-mode/v1/chat/completions"
		}
		return relaycommon.JoinURL(base, path), nil
	}

	switch a.mode {
	case channelcatalog.RelayModeChatCompletions:
		path = "/compatible-mode/v1/chat/completions"
	case channelcatalog.RelayModeCompletions:
		path = "/compatible-mode/v1/completions"
	case channelcatalog.RelayModeEmbeddings:
		path = "/compatible-mode/v1/embeddings"
	case channelcatalog.RelayModeRerank:
		path = "/api/v1/services/rerank/text-rerank/text-rerank"
	case channelcatalog.RelayModeResponses:
		path = "/api/v2/apps/protocols/compatible-mode/v1/responses"
	case channelcatalog.RelayModeImagesGenerations:
		if a.imageSync {
			path = "/api/v1/services/aigc/multimodal-generation/generation"
		} else {
			path = "/api/v1/services/aigc/text2image/image-synthesis"
		}
	case channelcatalog.RelayModeImagesEdits:
		modelName := imageRoutingModel(meta)
		switch {
		case isOldWanModel(modelName):
			path = "/api/v1/services/aigc/image2image/image-synthesis"
		case isWanModel(modelName):
			path = "/api/v1/services/aigc/image-generation/generation"
		default:
			path = "/api/v1/services/aigc/multimodal-generation/generation"
		}
	default:
		return "", fmt.Errorf("Ali channel does not support relay mode %d", a.mode)
	}
	return relaycommon.JoinURL(base, path), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil {
		return errors.New("Ali request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	apiKey := strings.TrimSpace(meta.APIKey)
	if apiKey == "" {
		return errors.New("Ali API key is required")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Del("x-api-key")
	req.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("X-DashScope-SSE", "enable")
	} else {
		req.Header.Set("Accept", "application/json")
		req.Header.Del("X-DashScope-SSE")
	}
	if meta.Channel != nil {
		plugin := strings.TrimSpace(meta.Channel.Other)
		if len(plugin) > maxDashScopePluginHeaderBytes {
			return fmt.Errorf("Ali plugin header exceeds %d bytes", maxDashScopePluginHeaderBytes)
		}
		if plugin != "" {
			req.Header.Set("X-DashScope-Plugin", plugin)
		}
	}
	if a.mode == channelcatalog.RelayModeImagesGenerations && !a.imageSync ||
		a.mode == channelcatalog.RelayModeImagesEdits && isWanModel(imageRoutingModel(meta)) {
		req.Header.Set("X-DashScope-Async", "enable")
	} else {
		req.Header.Del("X-DashScope-Async")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if a.relayFormat(meta) == channelcatalog.RelayFormatClaude && SupportsAnthropicMessages(meta.ModelName) {
		return convertNativeClaudeRequest(meta)
	}
	switch a.mode {
	case channelcatalog.RelayModeChatCompletions, channelcatalog.RelayModeCompletions:
		return a.convertTextRequest(meta)
	case channelcatalog.RelayModeEmbeddings, channelcatalog.RelayModeResponses:
		return a.openAI.ConvertRequest(meta)
	case channelcatalog.RelayModeRerank:
		return convertRerankRequest(meta)
	case channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits:
		return convertImageRequest(meta, a.imageSync)
	default:
		return nil, fmt.Errorf("Ali channel does not support relay mode %d", a.mode)
	}
}

func (a *Adaptor) convertTextRequest(meta *relaycommon.Meta) ([]byte, error) {
	body, err := a.openAI.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if err := protocolkit.UnmarshalJSON(body, &request); err != nil {
		return nil, fmt.Errorf("decode Ali text request: %w", err)
	}
	if raw, exists := request["top_p"]; exists {
		value, ok := raw.(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("Ali top_p must be a finite number")
		}
		switch {
		case value >= 1:
			request["top_p"] = 0.99
		case value <= 0:
			request["top_p"] = 0.01
		}
	}
	if !isQwenThinkingBudgetModel(meta.ModelName) {
		delete(request, "thinking_budget")
	}
	if meta.IsStream {
		streamOptions, _ := request["stream_options"].(map[string]any)
		if streamOptions == nil {
			streamOptions = make(map[string]any)
			request["stream_options"] = streamOptions
		}
		streamOptions["include_usage"] = true
	}
	return protocolkit.MarshalJSON(request)
}

func convertNativeClaudeRequest(meta *relaycommon.Meta) ([]byte, error) {
	if len(meta.RawBody) == 0 {
		return nil, errors.New("Ali native Claude request body is empty")
	}
	var request map[string]any
	if err := protocolkit.UnmarshalJSON(meta.RawBody, &request); err != nil || request == nil {
		return nil, errors.New("Ali native Claude request must be a JSON object")
	}
	request["model"] = meta.ModelName
	delete(request, "group")
	return protocolkit.MarshalJSON(request)
}

func convertRerankRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta.Request == nil || meta.Request.Extra == nil {
		return nil, errors.New("Ali rerank request is empty")
	}
	query, ok := meta.Request.Extra["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return nil, errors.New("Ali rerank query is required")
	}
	documents, ok := meta.Request.Extra["documents"].([]any)
	if !ok || len(documents) == 0 {
		return nil, errors.New("Ali rerank documents are required")
	}
	if len(documents) > maxAliRerankDocuments {
		return nil, fmt.Errorf("Ali rerank documents exceed %d entries", maxAliRerankDocuments)
	}
	parameters := make(map[string]any)
	if raw, exists := meta.Request.Extra["top_n"]; exists {
		topN, valid := boundedJSONInteger(raw, 0, maxAliRerankDocuments)
		if !valid {
			return nil, fmt.Errorf("Ali rerank top_n must be an integer between 0 and %d", maxAliRerankDocuments)
		}
		parameters["top_n"] = topN
	}
	returnDocuments := true
	if raw, exists := meta.Request.Extra["return_documents"]; exists {
		value, valid := raw.(bool)
		if !valid {
			return nil, errors.New("Ali rerank return_documents must be a boolean")
		}
		returnDocuments = value
	}
	parameters["return_documents"] = returnDocuments
	return protocolkit.MarshalJSON(map[string]any{
		"model":      meta.ModelName,
		"input":      map[string]any{"query": query, "documents": documents},
		"parameters": parameters,
	})
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil {
		return nil, errors.New("Ali response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, aliHTTPResponseError(resp, meta)
	}
	switch a.mode {
	case channelcatalog.RelayModeRerank:
		return doRerankResponse(c, resp, meta)
	case channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits:
		return a.doImageResponse(c, resp, meta)
	default:
		return a.doCompatibleResponse(c, resp, meta)
	}
}

func (a *Adaptor) doCompatibleResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if meta.IsStream {
		return a.openAI.DoResponse(c, resp, meta)
	}
	limit := relaycommon.MaxUpstreamJSONBodyBytes
	if a.mode == channelcatalog.RelayModeEmbeddings {
		limit = relaycommon.MaxUpstreamLargeJSONBodyBytes
	}
	body, err := relaycommon.ReadUpstreamBody(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("read Ali response: %w", err)
	}
	if providerErr := aliErrorFromBody(meta, body, resp.StatusCode); providerErr != nil {
		return nil, providerErr
	}
	copyOfResponse := *resp
	copyOfResponse.Body = io.NopCloser(bytes.NewReader(body))
	return a.openAI.DoResponse(c, &copyOfResponse, meta)
}

func doRerankResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Ali rerank response: %w", err)
	}
	if providerErr := aliErrorFromBody(meta, body, resp.StatusCode); providerErr != nil {
		return nil, providerErr
	}
	var response struct {
		Output struct {
			Results []any `json:"results"`
		} `json:"output"`
		Usage aliUsage `json:"usage"`
	}
	if err := protocolkit.UnmarshalJSON(body, &response); err != nil {
		return nil, fmt.Errorf("decode Ali rerank response: %w", err)
	}
	if response.Usage.TotalTokens < 0 || response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 {
		return nil, errors.New("Ali rerank response contains negative usage")
	}
	total := response.Usage.TotalTokens
	if total == 0 {
		total = response.Usage.InputTokens + response.Usage.OutputTokens
	}
	usage := &protocolkit.Usage{PromptTokens: total, TotalTokens: total}
	out, err := protocolkit.MarshalJSON(map[string]any{"results": response.Output.Results, "usage": usage})
	if err != nil {
		return nil, fmt.Errorf("encode Ali rerank response: %w", err)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return usage, fmt.Errorf("write Ali rerank response: %w", err)
	}
	return usage, nil
}

type aliUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	ImageCount   int `json:"image_count,omitempty"`
}

func aliErrorFromBody(meta *relaycommon.Meta, body []byte, statusCode int) error {
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil {
		return nil
	}
	code, message := strings.TrimSpace(envelope.Code), strings.TrimSpace(envelope.Message)
	if code == "" && message == "" {
		return nil
	}
	if code == "" {
		code = "ali_error"
	}
	if message == "" {
		message = "DashScope request failed"
	}
	if statusCode < http.StatusBadRequest {
		statusCode = http.StatusBadRequest
	}
	statusCode = mappedStatusCode(meta, statusCode)
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: code, Param: envelope.RequestID, Code: code,
	}, statusCode)
}

func mappedStatusCode(meta *relaycommon.Meta, status int) int {
	if meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.StatusCodeMapping) == "" {
		return status
	}
	var mapping map[string]any
	if protocolkit.UnmarshalJSON([]byte(meta.Channel.StatusCodeMapping), &mapping) != nil {
		return status
	}
	raw, exists := mapping[strconv.Itoa(status)]
	if !exists {
		return status
	}
	mapped, ok := boundedJSONInteger(raw, 100, 599)
	if !ok {
		return status
	}
	return mapped
}

func aliHTTPResponseError(resp *http.Response, meta *relaycommon.Meta) error {
	if resp == nil {
		return errors.New("Ali upstream response is nil")
	}
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamErrorBodyBytes)
	if err != nil {
		return &relaycommon.UpstreamError{
			StatusCode: mappedStatusCode(meta, resp.StatusCode),
			Cause:      fmt.Errorf("read Ali upstream error: %w", err),
		}
	}
	if providerErr := aliErrorFromBody(meta, body, resp.StatusCode); providerErr != nil {
		return providerErr
	}
	return &relaycommon.UpstreamError{
		StatusCode: mappedStatusCode(meta, resp.StatusCode),
		Body:       string(body),
	}
}

func boundedJSONInteger(value any, minimum, maximum int) (int, bool) {
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
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) ||
		number < float64(minimum) || number > float64(maximum) {
		return 0, false
	}
	return int(number), true
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil {
		return errors.New("Ali relay metadata is nil")
	}
	if meta.Request == nil {
		return errors.New("Ali request is nil")
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return errors.New("Ali upstream model is empty")
	}
	format := a.relayFormat(meta)
	supported := false
	switch format {
	case channelcatalog.RelayFormatClaude:
		supported = a.mode == channelcatalog.RelayModeChatCompletions
	case channelcatalog.RelayFormatOpenAI:
		supported = a.mode == channelcatalog.RelayModeChatCompletions || a.mode == channelcatalog.RelayModeCompletions
	case channelcatalog.RelayFormatEmbedding:
		supported = a.mode == channelcatalog.RelayModeEmbeddings
	case channelcatalog.RelayFormatRerank:
		supported = a.mode == channelcatalog.RelayModeRerank
	case channelcatalog.RelayFormatOpenAIResponses:
		supported = a.mode == channelcatalog.RelayModeResponses
	case channelcatalog.RelayFormatOpenAIImage:
		supported = a.mode == channelcatalog.RelayModeImagesGenerations || a.mode == channelcatalog.RelayModeImagesEdits
	default:
		return fmt.Errorf("Ali channel does not support relay format %q", format)
	}
	if !supported {
		return fmt.Errorf("Ali relay format %q does not support mode %d", format, a.mode)
	}
	if meta.IsStream {
		switch a.mode {
		case channelcatalog.RelayModeChatCompletions, channelcatalog.RelayModeCompletions, channelcatalog.RelayModeResponses:
		default:
			return fmt.Errorf("Ali relay mode %d does not support streaming", a.mode)
		}
	}
	return nil
}

func (a *Adaptor) relayFormat(meta *relaycommon.Meta) channelcatalog.RelayFormat {
	if meta != nil && meta.Format != channelcatalog.RelayFormatUnknown {
		return meta.Format
	}
	return a.format
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Ali base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Ali base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Ali base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Ali base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Ali base URL must not contain a query or fragment")
	}
	return nil
}

func isQwenThinkingBudgetModel(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	return strings.HasPrefix(normalized, "qwen") || strings.Contains(normalized, "/qwen") ||
		strings.HasPrefix(normalized, "qwq") || strings.Contains(normalized, "/qwq")
}
