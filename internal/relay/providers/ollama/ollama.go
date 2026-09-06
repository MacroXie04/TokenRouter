// Package ollama implements the native Ollama wire protocol while exposing
// TokenRouter's OpenAI-compatible relay surface.
package ollama

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL      = "http://localhost:11434"
	maxStreamBodyBytes  = int64(64 << 20)
	maxInlineImageBytes = int64(16 << 20)
)

// Adaptor converts the supported OpenAI relay modes to Ollama's native
// /api/chat, /api/generate, and /api/embed contracts.
type Adaptor struct {
	mode channelcatalog.RelayMode
}

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	if meta == nil {
		a.mode = channelcatalog.RelayModeUnknown
		return
	}
	a.mode = meta.Mode
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", errors.New("Ollama relay metadata is nil")
	}
	base := strings.TrimRight(strings.TrimSpace(meta.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	var path string
	switch a.mode {
	case channelcatalog.RelayModeChatCompletions:
		path = "/api/chat"
	case channelcatalog.RelayModeCompletions:
		path = "/api/generate"
	case channelcatalog.RelayModeEmbeddings:
		path = "/api/embed"
	default:
		return "", fmt.Errorf("Ollama channel does not support relay mode %d", a.mode)
	}
	return relaycommon.JoinURL(base, path), nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Ollama base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Ollama base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Ollama base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Ollama base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Ollama base URL must not contain a query or fragment")
	}
	return nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Ollama request metadata is nil")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(meta.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Del("Authorization")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta == nil || meta.Request == nil {
		return nil, errors.New("Ollama request is nil")
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return nil, errors.New("Ollama upstream model is empty")
	}
	var (
		request any
		err     error
	)
	switch a.mode {
	case channelcatalog.RelayModeChatCompletions:
		request, err = convertChatRequest(meta)
	case channelcatalog.RelayModeCompletions:
		request, err = convertGenerateRequest(meta)
	case channelcatalog.RelayModeEmbeddings:
		request, err = convertEmbeddingRequest(meta)
	default:
		return nil, fmt.Errorf("Ollama channel does not support relay mode %d", a.mode)
	}
	if err != nil {
		return nil, err
	}
	body, err := protocolkit.MarshalJSON(request)
	if err != nil {
		return nil, fmt.Errorf("encode Ollama request: %w", err)
	}
	return body, nil
}

type chatRequest struct {
	Model     string         `json:"model"`
	Messages  []chatMessage  `json:"messages"`
	Tools     []tool         `json:"tools,omitempty"`
	Format    any            `json:"format,omitempty"`
	Stream    bool           `json:"stream"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive any            `json:"keep_alive,omitempty"`
	Think     any            `json:"think,omitempty"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Images     []string   `json:"images,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Thinking   string     `json:"thinking,omitempty"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type toolCall struct {
	ID       string           `json:"id,omitempty"`
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type generateRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt,omitempty"`
	Suffix    string         `json:"suffix,omitempty"`
	Images    []string       `json:"images,omitempty"`
	Format    any            `json:"format,omitempty"`
	Stream    bool           `json:"stream"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive any            `json:"keep_alive,omitempty"`
	Think     any            `json:"think,omitempty"`
}

type embeddingRequest struct {
	Model      string         `json:"model"`
	Input      any            `json:"input"`
	Options    map[string]any `json:"options,omitempty"`
	Dimensions int            `json:"dimensions,omitempty"`
}

func convertChatRequest(meta *relaycommon.Meta) (*chatRequest, error) {
	req := meta.Request
	options, err := requestOptions(req, true)
	if err != nil {
		return nil, err
	}
	format, err := responseFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	think, err := requestThink(req)
	if err != nil {
		return nil, err
	}
	messages, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := convertTools(req.Tools)
	if err != nil {
		return nil, err
	}
	return &chatRequest{
		Model: meta.ModelName, Messages: messages, Tools: tools, Format: format,
		Stream: meta.IsStream, Options: options, KeepAlive: copiedExtra(req, "keep_alive"), Think: think,
	}, nil
}

func convertGenerateRequest(meta *relaycommon.Meta) (*generateRequest, error) {
	req := meta.Request
	prompt, err := flattenPrompt(req.Prompt)
	if err != nil {
		return nil, err
	}
	suffix, err := optionalString(req.Suffix, "suffix")
	if err != nil {
		return nil, err
	}
	options, err := requestOptions(req, true)
	if err != nil {
		return nil, err
	}
	format, err := responseFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	think, err := requestThink(req)
	if err != nil {
		return nil, err
	}
	images, err := extraImages(req.Extra["images"])
	if err != nil {
		return nil, err
	}
	return &generateRequest{
		Model: meta.ModelName, Prompt: prompt, Suffix: suffix, Images: images,
		Format: format, Stream: meta.IsStream, Options: options,
		KeepAlive: copiedExtra(req, "keep_alive"), Think: think,
	}, nil
}

func convertEmbeddingRequest(meta *relaycommon.Meta) (*embeddingRequest, error) {
	req := meta.Request
	input, ok := req.Extra["input"]
	if !ok || input == nil {
		return nil, errors.New("Ollama embedding input is empty")
	}
	normalized, err := normalizeEmbeddingInput(input)
	if err != nil {
		return nil, err
	}
	options, err := requestOptions(req, false)
	if err != nil {
		return nil, err
	}
	dimensions, err := optionalNonNegativeInt(req.Extra["dimensions"], "dimensions")
	if err != nil {
		return nil, err
	}
	if dimensions > 0 {
		options["dimensions"] = dimensions
	}
	return &embeddingRequest{Model: meta.ModelName, Input: normalized, Options: options, Dimensions: dimensions}, nil
}

func convertMessages(messages []protocolkit.Message) ([]chatMessage, error) {
	converted := make([]chatMessage, 0, len(messages))
	toolNames := make(map[string]string)
	for index, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			return nil, fmt.Errorf("Ollama message %d has no role", index)
		}
		text, images, err := messageContent(message.Content)
		if err != nil {
			return nil, fmt.Errorf("convert Ollama message %d: %w", index, err)
		}
		out := chatMessage{
			Role: role, Content: text, Images: images, ToolCallID: strings.TrimSpace(message.ToolCallId),
			Thinking: firstNonEmpty(message.ReasoningContent, message.Reasoning),
		}
		if role == "tool" {
			out.ToolName = strings.TrimSpace(message.Name)
			if out.ToolName == "" {
				out.ToolName = toolNames[out.ToolCallID]
			}
		}
		for callIndex, call := range message.ToolCalls {
			if call.Function == nil || strings.TrimSpace(call.Function.Name) == "" {
				return nil, fmt.Errorf("message %d tool call %d has no function name", index, callIndex)
			}
			arguments := any(map[string]any{})
			if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
				if err := protocolkit.UnmarshalJSON([]byte(raw), &arguments); err != nil {
					return nil, fmt.Errorf("message %d tool call %d has invalid arguments", index, callIndex)
				}
			}
			id := strings.TrimSpace(call.Id)
			name := strings.TrimSpace(call.Function.Name)
			out.ToolCalls = append(out.ToolCalls, toolCall{ID: id, Function: toolCallFunction{Name: name, Arguments: arguments}})
			if id != "" {
				toolNames[id] = name
			}
		}
		converted = append(converted, out)
	}
	return converted, nil
}

func messageContent(content any) (string, []string, error) {
	switch value := content.(type) {
	case nil:
		return "", nil, nil
	case string:
		return value, nil, nil
	case []any:
		var text strings.Builder
		var images []string
		for partIndex, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok {
				return "", nil, fmt.Errorf("content part %d is not an object", partIndex)
			}
			switch kind, _ := part["type"].(string); kind {
			case protocolkit.ContentTypeText:
				fragment, ok := part["text"].(string)
				if !ok {
					return "", nil, fmt.Errorf("text part %d has no text", partIndex)
				}
				text.WriteString(fragment)
			case protocolkit.ContentTypeImageURL:
				image, ok := part["image_url"].(map[string]any)
				if !ok {
					return "", nil, fmt.Errorf("image part %d has no image_url object", partIndex)
				}
				rawURL, _ := image["url"].(string)
				encoded, err := inlineImage(rawURL)
				if err != nil {
					return "", nil, fmt.Errorf("image part %d: %w", partIndex, err)
				}
				images = append(images, encoded)
			default:
				return "", nil, fmt.Errorf("unsupported content part type %q", kind)
			}
		}
		return text.String(), images, nil
	default:
		return "", nil, errors.New("message content must be a string or content-part array")
	}
}

func inlineImage(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:image/") {
		return "", errors.New("Ollama images must use an inline data:image URL")
	}
	comma := strings.IndexByte(raw, ',')
	if comma < 0 || !strings.Contains(strings.ToLower(raw[:comma]), ";base64") {
		return "", errors.New("Ollama image data URL must be base64 encoded")
	}
	encoded := raw[comma+1:]
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > maxInlineImageBytes {
		return "", fmt.Errorf("Ollama inline image exceeds %d bytes", maxInlineImageBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("Ollama image contains invalid base64")
	}
	if len(decoded) == 0 {
		return "", errors.New("Ollama image is empty")
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}

func extraImages(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("Ollama images must be an array")
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		raw, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("Ollama image %d must be a string", index)
		}
		encoded, err := inlineImage(raw)
		if err != nil {
			return nil, fmt.Errorf("Ollama image %d: %w", index, err)
		}
		result = append(result, encoded)
	}
	return result, nil
}

func convertTools(tools []protocolkit.ToolCallRequest) ([]tool, error) {
	converted := make([]tool, 0, len(tools))
	for index, input := range tools {
		if input.Type != "" && input.Type != "function" {
			return nil, fmt.Errorf("Ollama tool %d has unsupported type %q", index, input.Type)
		}
		if input.Function == nil || strings.TrimSpace(input.Function.Name) == "" {
			return nil, fmt.Errorf("Ollama tool %d has no function name", index)
		}
		converted = append(converted, tool{Type: "function", Function: toolFunction{
			Name: strings.TrimSpace(input.Function.Name), Description: input.Function.Description,
			Parameters: input.Function.Parameters,
		}})
	}
	return converted, nil
}

func requestOptions(req *protocolkit.GeneralOpenAIRequest, includeMaxTokens bool) (map[string]any, error) {
	options := make(map[string]any)
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		options["top_p"] = *req.TopP
	}
	if req.FrequencyPenalty != nil {
		options["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		options["presence_penalty"] = *req.PresencePenalty
	}
	if req.Seed != nil {
		options["seed"] = *req.Seed
	}
	if topK, exists := req.Extra["top_k"]; exists {
		integer, err := optionalNonNegativeInt(topK, "top_k")
		if err != nil {
			return nil, err
		}
		options["top_k"] = integer
	}
	if includeMaxTokens {
		var maximum *int
		if req.MaxTokens != nil {
			maximum = req.MaxTokens
		} else if req.MaxCompletionTokens != nil {
			maximum = req.MaxCompletionTokens
		}
		if maximum != nil {
			if *maximum < 0 {
				return nil, errors.New("Ollama max tokens must not be negative")
			}
			options["num_predict"] = *maximum
		}
	}
	if req.Stop != nil {
		stop, err := normalizeStop(req.Stop)
		if err != nil {
			return nil, err
		}
		options["stop"] = stop
	}
	return options, nil
}

func normalizeStop(value any) ([]string, error) {
	switch stop := value.(type) {
	case string:
		return []string{stop}, nil
	case []string:
		return append([]string(nil), stop...), nil
	case []any:
		result := make([]string, 0, len(stop))
		for index, item := range stop {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("Ollama stop item %d must be a string", index)
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, errors.New("Ollama stop must be a string or string array")
	}
}

func requestThink(req *protocolkit.GeneralOpenAIRequest) (any, error) {
	if value, exists := req.Extra["think"]; exists && value != nil {
		switch typed := value.(type) {
		case bool:
			return typed, nil
		case string:
			if validThinkingEffort(typed) {
				return typed, nil
			}
		}
		return nil, errors.New("Ollama think must be a boolean or supported reasoning effort")
	}
	effort := strings.TrimSpace(req.ReasoningEffort)
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		effort = strings.TrimSpace(req.Reasoning.Effort)
	}
	if effort == "" {
		return nil, nil
	}
	if effort == "none" {
		return false, nil
	}
	if !validThinkingEffort(effort) {
		return nil, fmt.Errorf("unsupported Ollama reasoning effort %q", effort)
	}
	return effort, nil
}

func validThinkingEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "max":
		return true
	default:
		return false
	}
}

func responseFormat(format *protocolkit.ResponseFormat) (any, error) {
	if format == nil {
		return nil, nil
	}
	switch format.Type {
	case "json", "json_object":
		return "json", nil
	case "json_schema":
		if format.JsonSchema == nil || len(format.JsonSchema.Schema) == 0 {
			return nil, errors.New("Ollama json_schema response format has no schema")
		}
		return format.JsonSchema.Schema, nil
	case "", "text":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported Ollama response format %q", format.Type)
	}
}

func flattenPrompt(value any) (string, error) {
	switch prompt := value.(type) {
	case nil:
		return "", errors.New("Ollama completion prompt is empty")
	case string:
		return prompt, nil
	case []any:
		var builder strings.Builder
		for index, item := range prompt {
			text, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("Ollama prompt item %d must be a string", index)
			}
			builder.WriteString(text)
		}
		return builder.String(), nil
	case []string:
		return strings.Join(prompt, ""), nil
	default:
		return "", errors.New("Ollama completion prompt must be a string or string array")
	}
}

func optionalString(value any, name string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("Ollama %s must be a string", name)
	}
	return text, nil
}

func normalizeEmbeddingInput(value any) (any, error) {
	switch input := value.(type) {
	case string:
		return input, nil
	case []string:
		if len(input) == 0 {
			return nil, errors.New("Ollama embedding input is empty")
		}
		if len(input) == 1 {
			return input[0], nil
		}
		return append([]string(nil), input...), nil
	case []any:
		if len(input) == 0 {
			return nil, errors.New("Ollama embedding input is empty")
		}
		stringsOnly := make([]string, 0, len(input))
		for index, item := range input {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("Ollama embedding input %d must be a string", index)
			}
			stringsOnly = append(stringsOnly, text)
		}
		if len(stringsOnly) == 1 {
			return stringsOnly[0], nil
		}
		return stringsOnly, nil
	default:
		return nil, errors.New("Ollama embedding input must be a string or string array")
	}
}

func optionalNonNegativeInt(value any, name string) (int, error) {
	if value == nil {
		return 0, nil
	}
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int64:
		number = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed > float64(math.MaxInt64) {
			return 0, fmt.Errorf("Ollama %s must be an integer", name)
		}
		number = int64(typed)
	default:
		return 0, fmt.Errorf("Ollama %s must be an integer", name)
	}
	if number < 0 || uint64(number) > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("Ollama %s is outside the supported range", name)
	}
	return int(number), nil
}

func copiedExtra(req *protocolkit.GeneralOpenAIRequest, key string) any {
	if req == nil || req.Extra == nil {
		return nil
	}
	return req.Extra[key]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type responseChunk struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Message   *struct {
		Role      string             `json:"role"`
		Content   string             `json:"content"`
		Thinking  any                `json:"thinking"`
		ToolCalls []responseToolCall `json:"tool_calls"`
	} `json:"message"`
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error,omitempty"`
}

type responseToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

type embeddingResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	Error           string      `json:"error,omitempty"`
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || resp.Body == nil || meta == nil {
		return nil, errors.New("Ollama response metadata is nil")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if a.mode == channelcatalog.RelayModeEmbeddings {
		return embeddingResponseToOpenAI(c, resp, meta)
	}
	if meta.IsStream {
		return a.streamResponse(c, resp, meta)
	}
	return a.nonStreamResponse(c, resp, meta)
}

func embeddingResponseToOpenAI(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Ollama embedding response: %w", err)
	}
	var upstream embeddingResponse
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode Ollama embedding response: %w", err)
	}
	if upstream.Error != "" {
		return nil, ollamaError(upstream.Error)
	}
	if upstream.PromptEvalCount < 0 {
		return nil, errors.New("Ollama embedding response contains negative usage")
	}
	if len(upstream.Embeddings) == 0 {
		return nil, errors.New("Ollama embedding response contains no vectors")
	}
	expectedVectors := 0
	if meta.Request != nil && meta.Request.Extra != nil {
		switch input := meta.Request.Extra["input"].(type) {
		case string:
			expectedVectors = 1
		case []string:
			expectedVectors = len(input)
		case []any:
			expectedVectors = len(input)
		}
	}
	if expectedVectors > 0 && len(upstream.Embeddings) != expectedVectors {
		return nil, fmt.Errorf("Ollama embedding response returned %d vectors for %d inputs", len(upstream.Embeddings), expectedVectors)
	}
	expectedDimensions := 0
	if meta.Request != nil && meta.Request.Extra != nil {
		expectedDimensions, err = optionalNonNegativeInt(meta.Request.Extra["dimensions"], "dimensions")
		if err != nil {
			return nil, err
		}
	}
	vectorWidth := -1
	for row, vector := range upstream.Embeddings {
		if len(vector) == 0 {
			return nil, fmt.Errorf("Ollama embedding response vector %d is empty", row)
		}
		if vectorWidth < 0 {
			vectorWidth = len(vector)
		} else if len(vector) != vectorWidth {
			return nil, errors.New("Ollama embedding response contains inconsistent vector dimensions")
		}
		if expectedDimensions > 0 && len(vector) != expectedDimensions {
			return nil, fmt.Errorf("Ollama embedding response vector %d has %d dimensions; expected %d", row, len(vector), expectedDimensions)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("Ollama embedding response vector %d contains a non-finite value", row)
			}
		}
	}
	usage := &protocolkit.Usage{PromptTokens: upstream.PromptEvalCount, TotalTokens: upstream.PromptEvalCount}
	data := make([]map[string]any, 0, len(upstream.Embeddings))
	for index, vector := range upstream.Embeddings {
		data = append(data, map[string]any{"object": "embedding", "index": index, "embedding": vector})
	}
	model := upstream.Model
	if model == "" {
		model = meta.ModelName
	}
	return writeJSON(c, resp.StatusCode, map[string]any{
		"object": "list", "data": data, "model": model, "usage": usage,
	}, usage)
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Ollama response: %w", err)
	}
	chunks, err := decodeChunks(body)
	if err != nil {
		return nil, err
	}
	var content, reasoning strings.Builder
	var calls []openAIToolCall
	model := meta.ModelName
	created := time.Now().Unix()
	finishReason := "stop"
	var usage *protocolkit.Usage
	terminalSeen := false
	for _, chunk := range chunks {
		if terminalSeen {
			return nil, errors.New("Ollama response contains data after its terminal frame")
		}
		if chunk.Error != "" {
			return nil, ollamaError(chunk.Error)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if parsed, ok := parseCreated(chunk.CreatedAt); ok {
			created = parsed
		}
		if chunk.Message != nil {
			content.WriteString(chunk.Message.Content)
			reasoning.WriteString(thinkingText(chunk.Message.Thinking))
			converted, err := responseToolCalls(chunk.Message.ToolCalls, len(calls), false)
			if err != nil {
				return nil, err
			}
			calls = append(calls, converted...)
		} else {
			content.WriteString(chunk.Response)
		}
		if chunk.Done {
			terminalSeen = true
			finishReason = normalizeFinishReason(chunk.DoneReason, len(calls) > 0)
			usage, err = usageFromChunk(chunk)
			if err != nil {
				return nil, err
			}
		}
	}
	if usage == nil {
		return nil, errors.New("Ollama response is missing a terminal usage frame")
	}
	if a.mode == channelcatalog.RelayModeCompletions {
		response := map[string]any{
			"id": newResponseID("cmpl-"), "object": "text_completion", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "text": content.String(), "finish_reason": finishReason}},
			"usage":   usage,
		}
		return writeJSON(c, resp.StatusCode, response, usage)
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
		if content.Len() == 0 {
			message["content"] = nil
		}
	}
	response := map[string]any{
		"id": newResponseID("chatcmpl-"), "object": "chat.completion", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   usage,
	}
	return writeJSON(c, resp.StatusCode, response, usage)
}

func decodeChunks(body []byte) ([]responseChunk, error) {
	// A normal non-stream Ollama response is one JSON object and may be
	// pretty-printed across lines. Older/proxied deployments can still return
	// newline-delimited chunks even when stream=false, so fall back to strict
	// per-line decoding only when the complete document is not one object.
	var single responseChunk
	if err := protocolkit.UnmarshalJSON(body, &single); err == nil {
		return []responseChunk{single}, nil
	}
	lines := bytes.Split(body, []byte{'\n'})
	chunks := make([]responseChunk, 0, len(lines))
	for index, raw := range lines {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var chunk responseChunk
		if err := protocolkit.UnmarshalJSON(raw, &chunk); err != nil {
			return nil, fmt.Errorf("decode Ollama response line %d: %w", index+1, err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return nil, errors.New("Ollama response is empty")
	}
	return chunks, nil
}

func (a *Adaptor) streamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	limited := &io.LimitedReader{R: resp.Body, N: maxStreamBodyBytes + 1}
	scanner := relaycommon.NewUpstreamSSEScanner(limited)
	started := false
	done := false
	model := meta.ModelName
	created := time.Now().Unix()
	idPrefix := "chatcmpl-"
	if a.mode == channelcatalog.RelayModeCompletions {
		idPrefix = "cmpl-"
	}
	responseID := newResponseID(idPrefix)
	toolIndex := 0
	contentBytes := 0
	var usage *protocolkit.Usage

	start := func() error {
		if started {
			return nil
		}
		started = true
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Status(resp.StatusCode)
		if a.mode == channelcatalog.RelayModeChatCompletions {
			chunk := streamEnvelope(responseID, created, model, map[string]any{"role": "assistant"}, nil, nil)
			if err := writeSSE(c, chunk); err != nil {
				return err
			}
		}
		return nil
	}

	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var chunk responseChunk
		if err := protocolkit.UnmarshalJSON([]byte(raw), &chunk); err != nil {
			if !started {
				return nil, fmt.Errorf("decode Ollama stream: %w", err)
			}
			return partialStreamUsage(usage, meta.PromptTokens, contentBytes), fmt.Errorf("decode Ollama stream: %w", err)
		}
		if chunk.Error != "" {
			if !started {
				return nil, ollamaError(chunk.Error)
			}
			return partialStreamUsage(usage, meta.PromptTokens, contentBytes), ollamaError(chunk.Error)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if parsed, ok := parseCreated(chunk.CreatedAt); ok {
			created = parsed
		}
		content := chunk.Response
		var reasoning string
		var calls []openAIToolCall
		if chunk.Message != nil {
			content = chunk.Message.Content
			reasoning = thinkingText(chunk.Message.Thinking)
			var err error
			calls, err = responseToolCalls(chunk.Message.ToolCalls, toolIndex, true)
			if err != nil {
				return partialStreamUsage(usage, meta.PromptTokens, contentBytes), err
			}
			toolIndex += len(calls)
		}
		var terminalUsage *protocolkit.Usage
		if chunk.Done {
			var err error
			terminalUsage, err = usageFromChunk(chunk)
			if err != nil {
				if !started {
					return nil, err
				}
				return partialStreamUsage(nil, meta.PromptTokens, contentBytes), err
			}
		}
		if err := start(); err != nil {
			return partialStreamUsage(usage, meta.PromptTokens, contentBytes), err
		}
		contentBytes += len(content) + len(reasoning)
		if content != "" || reasoning != "" || len(calls) > 0 {
			if a.mode == channelcatalog.RelayModeCompletions {
				out := map[string]any{
					"id": responseID, "object": "text_completion", "created": created, "model": model,
					"choices": []any{map[string]any{"index": 0, "text": content, "finish_reason": nil}},
				}
				if err := writeSSE(c, out); err != nil {
					return partialStreamUsage(usage, meta.PromptTokens, contentBytes), err
				}
			} else {
				delta := make(map[string]any)
				if content != "" {
					delta["content"] = content
				}
				if reasoning != "" {
					delta["reasoning_content"] = reasoning
				}
				if len(calls) > 0 {
					delta["tool_calls"] = calls
				}
				if err := writeSSE(c, streamEnvelope(responseID, created, model, delta, nil, nil)); err != nil {
					return partialStreamUsage(usage, meta.PromptTokens, contentBytes), err
				}
			}
		}
		if !chunk.Done {
			continue
		}
		usage = terminalUsage
		finish := normalizeFinishReason(chunk.DoneReason, toolIndex > 0)
		if a.mode == channelcatalog.RelayModeCompletions {
			out := map[string]any{
				"id": responseID, "object": "text_completion", "created": created, "model": model,
				"choices": []any{map[string]any{"index": 0, "text": "", "finish_reason": finish}},
			}
			if err := writeSSE(c, out); err != nil {
				return usage, err
			}
		} else {
			if err := writeSSE(c, streamEnvelope(responseID, created, model, map[string]any{}, &finish, nil)); err != nil {
				return usage, err
			}
		}
		usageEnvelope := map[string]any{
			"id": responseID, "object": streamObject(a.mode), "created": created, "model": model,
			"choices": []any{}, "usage": usage,
		}
		if err := writeSSE(c, usageEnvelope); err != nil {
			return usage, err
		}
		if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
			return usage, fmt.Errorf("write Ollama stream terminator: %w", err)
		}
		c.Writer.Flush()
		done = true
		break
	}
	if err := scanner.Err(); err != nil {
		return partialStreamUsage(usage, meta.PromptTokens, contentBytes), fmt.Errorf(
			"read Ollama stream (maximum line %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err,
		)
	}
	if limited.N <= 0 && !done {
		return partialStreamUsage(usage, meta.PromptTokens, contentBytes), fmt.Errorf("Ollama stream exceeds %d bytes", maxStreamBodyBytes)
	}
	if !done {
		return partialStreamUsage(usage, meta.PromptTokens, contentBytes), errors.New("Ollama stream ended before a terminal frame")
	}
	return usage, nil
}

type openAIToolCall struct {
	Index    *int                   `json:"index,omitempty"`
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func responseToolCalls(inputs []responseToolCall, start int, includeIndex bool) ([]openAIToolCall, error) {
	result := make([]openAIToolCall, 0, len(inputs))
	for offset, input := range inputs {
		if strings.TrimSpace(input.Function.Name) == "" {
			return nil, fmt.Errorf("Ollama tool call %d has no function name", start+offset)
		}
		arguments := input.Function.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		encoded, err := protocolkit.MarshalJSON(arguments)
		if err != nil {
			return nil, fmt.Errorf("encode Ollama tool call %d arguments: %w", start+offset, err)
		}
		index := start + offset
		id := strings.TrimSpace(input.ID)
		if id == "" {
			id = fmt.Sprintf("call_%d", index)
		}
		out := openAIToolCall{
			ID: id, Type: "function",
			Function: openAIToolCallFunction{Name: strings.TrimSpace(input.Function.Name), Arguments: string(encoded)},
		}
		if includeIndex {
			out.Index = &index
		}
		result = append(result, out)
	}
	return result, nil
}

func usageFromChunk(chunk responseChunk) (*protocolkit.Usage, error) {
	if chunk.PromptEvalCount < 0 || chunk.EvalCount < 0 || chunk.PromptEvalCount > math.MaxInt-chunk.EvalCount {
		return nil, errors.New("Ollama response contains invalid usage")
	}
	return &protocolkit.Usage{
		PromptTokens: chunk.PromptEvalCount, CompletionTokens: chunk.EvalCount,
		TotalTokens: chunk.PromptEvalCount + chunk.EvalCount,
	}, nil
}

func thinkingText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		encoded, err := protocolkit.MarshalJSON(typed)
		if err != nil || bytes.Equal(encoded, []byte("null")) {
			return ""
		}
		return string(encoded)
	}
}

func parseCreated(raw string) (int64, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return 0, false
	}
	return parsed.Unix(), true
}

func normalizeFinishReason(reason string, hasTools bool) string {
	if hasTools {
		return "tool_calls"
	}
	switch strings.TrimSpace(reason) {
	case "", "stop", "unload":
		return "stop"
	case "length":
		return "length"
	default:
		return strings.TrimSpace(reason)
	}
}

func streamObject(mode channelcatalog.RelayMode) string {
	if mode == channelcatalog.RelayModeCompletions {
		return "text_completion"
	}
	return "chat.completion.chunk"
}

func streamEnvelope(id string, created int64, model string, delta map[string]any, finish *string, usage *protocolkit.Usage) map[string]any {
	return map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		"usage":   usage,
	}
}

func writeSSE(c *gin.Context, payload any) error {
	encoded, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return fmt.Errorf("encode Ollama stream chunk: %w", err)
	}
	if _, err := c.Writer.WriteString("data: " + string(encoded) + "\n\n"); err != nil {
		return fmt.Errorf("write Ollama stream chunk: %w", err)
	}
	c.Writer.Flush()
	return nil
}

func partialStreamUsage(usage *protocolkit.Usage, promptTokens, outputBytes int) *protocolkit.Usage {
	if usage != nil {
		return usage
	}
	if outputBytes <= 0 {
		return nil
	}
	return relaycommon.EstimateStreamUsage(promptTokens, outputBytes)
}

func ollamaError(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Ollama returned an unspecified error"
	}
	body, err := protocolkit.MarshalJSON(gin.H{"error": protocolkit.OpenAIError{
		Message: message, Type: "upstream_error", Code: "ollama_error",
	}})
	if err != nil {
		return &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Cause: err}
	}
	return &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Body: string(body)}
}

func writeJSON(c *gin.Context, status int, payload any, usage *protocolkit.Usage) (*protocolkit.Usage, error) {
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI response from Ollama: %w", err)
	}
	c.Status(status)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return usage, fmt.Errorf("write OpenAI response from Ollama: %w", err)
	}
	return usage, nil
}

func newResponseID(prefix string) string {
	return prefix + cryptoutil.BestEffortRandomAlphanumeric(24)
}
