package zhipu

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"net/http"
	"strings"
)

const maxV4ImageCount = 128

type V4Adaptor struct {
	mode      channelcatalog.RelayMode
	format    channelcatalog.RelayFormat
	openAI    openai.Adaptor
	auxClient *http.Client
}

var _ relaycommon.Adaptor = (*V4Adaptor)(nil)

func (a *V4Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if meta == nil {
		return
	}
	a.mode = meta.Mode
	a.format = meta.Format
	a.openAI.Init(meta)
	if a.auxClient == nil {
		a.auxClient = newImageClient()
	}
}

func (a *V4Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	return V4RequestURL(meta.BaseURL, a.mode, a.format)
}

func (a *V4Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Zhipu v4 request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	apiKey := strings.TrimSpace(meta.APIKey)
	if apiKey == "" {
		return errors.New("Zhipu v4 API key is required")
	}
	if len(apiKey) > maxCredentialSize || strings.ContainsAny(apiKey, "\r\n") {
		return errors.New("Zhipu v4 API key is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *V4Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	switch a.format {
	case channelcatalog.RelayFormatClaude:
		return convertV4ClaudeRequest(meta)
	case channelcatalog.RelayFormatOpenAI:
		return convertV4ChatRequest(meta)
	case channelcatalog.RelayFormatEmbedding:
		return convertV4EmbeddingRequest(meta)
	case channelcatalog.RelayFormatOpenAIImage:
		return convertV4ImageRequest(meta)
	default:
		return nil, fmt.Errorf("Zhipu v4 does not support relay format %q", a.format)
	}
}

func (a *V4Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Zhipu v4 response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, zhipuHTTPError(response, meta, "zhipu_error")
	}
	if a.format == channelcatalog.RelayFormatClaude {
		return v4ClaudeResponse(c, response, meta)
	}
	if a.mode == channelcatalog.RelayModeImagesGenerations {
		return a.v4ImageResponse(c, response, meta)
	}
	return a.openAI.DoResponse(c, response, meta)
}

func (a *V4Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Zhipu v4 relay metadata is nil")
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return errors.New("Zhipu v4 upstream model is empty")
	}
	var supported bool
	switch a.format {
	case channelcatalog.RelayFormatClaude:
		supported = a.mode == channelcatalog.RelayModeChatCompletions
	case channelcatalog.RelayFormatOpenAI:
		supported = a.mode == channelcatalog.RelayModeChatCompletions
	case channelcatalog.RelayFormatEmbedding:
		supported = a.mode == channelcatalog.RelayModeEmbeddings
	case channelcatalog.RelayFormatOpenAIImage:
		supported = a.mode == channelcatalog.RelayModeImagesGenerations
	default:
		return fmt.Errorf("Zhipu v4 does not support relay format %q", a.format)
	}
	if !supported {
		return fmt.Errorf("Zhipu v4 relay format %q does not support mode %d", a.format, a.mode)
	}
	if meta.IsStream && a.mode != channelcatalog.RelayModeChatCompletions {
		return fmt.Errorf("Zhipu v4 relay mode %d does not support streaming", a.mode)
	}
	if a.mode == channelcatalog.RelayModeImagesGenerations && isMultipart(meta.RequestContentType) {
		return errors.New("Zhipu v4 image generation requires application/json")
	}
	return nil
}

func convertV4ClaudeRequest(meta *relaycommon.Meta) ([]byte, error) {
	if len(meta.RawBody) == 0 {
		return nil, errors.New("Zhipu v4 Claude request body is empty")
	}
	var request map[string]any
	if err := protocolkit.UnmarshalJSON(meta.RawBody, &request); err != nil || request == nil {
		return nil, errors.New("Zhipu v4 Claude request must be a JSON object")
	}
	request["model"] = meta.ModelName
	delete(request, "group")
	return protocolkit.MarshalJSON(request)
}

func convertV4ChatRequest(meta *relaycommon.Meta) ([]byte, error) {
	messages, err := v4Messages(meta.Request.Messages)
	if err != nil {
		return nil, err
	}
	request := map[string]any{"model": meta.ModelName, "messages": messages}
	if rawStream, exists := meta.Request.Extra["stream"]; exists {
		stream, ok := rawStream.(bool)
		if !ok {
			return nil, errors.New("Zhipu v4 stream must be a boolean")
		}
		request["stream"] = stream
	}
	if meta.IsStream {
		request["stream"] = true
		request["stream_options"] = map[string]any{"include_usage": true}
	}
	if meta.Request.Temperature != nil {
		request["temperature"] = *meta.Request.Temperature
	}
	if meta.Request.TopP != nil {
		topP := *meta.Request.TopP
		if math.IsNaN(topP) || math.IsInf(topP, 0) {
			return nil, errors.New("Zhipu v4 top_p must be finite")
		}
		if topP >= 1 {
			topP = 0.99
		}
		request["top_p"] = topP
	}
	if meta.Request.MaxCompletionTokens != nil && *meta.Request.MaxCompletionTokens != 0 {
		request["max_tokens"] = *meta.Request.MaxCompletionTokens
	} else if meta.Request.MaxTokens != nil {
		request["max_tokens"] = *meta.Request.MaxTokens
	} else if meta.Request.MaxCompletionTokens != nil {
		request["max_tokens"] = *meta.Request.MaxCompletionTokens
	}
	if meta.Request.Stop != nil {
		stop, err := normalizedStop(meta.Request.Stop)
		if err != nil {
			return nil, err
		}
		request["stop"] = stop
	}
	if len(meta.Request.Tools) > 0 {
		request["tools"] = meta.Request.Tools
	}
	if meta.Request.ToolChoice != nil {
		request["tool_choice"] = meta.Request.ToolChoice
	}
	if thinking, exists := meta.Request.Extra["thinking"]; exists {
		request["thinking"] = thinking
	}
	return protocolkit.MarshalJSON(request)
}

func v4Messages(source []protocolkit.Message) ([]any, error) {
	messages := make([]any, 0, len(source))
	for _, message := range source {
		contentBody, err := protocolkit.MarshalJSON(message.Content)
		if err != nil {
			return nil, fmt.Errorf("copy Zhipu v4 message content: %w", err)
		}
		var content any
		if err := protocolkit.UnmarshalJSON(contentBody, &content); err != nil {
			return nil, fmt.Errorf("copy Zhipu v4 message content: %w", err)
		}
		if parts, ok := content.([]any); ok {
			for index, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok || part["type"] != protocolkit.ContentTypeImageURL {
					continue
				}
				image, ok := part["image_url"].(map[string]any)
				if !ok {
					continue
				}
				rawURL, _ := image["url"].(string)
				if strings.HasPrefix(rawURL, "data:image/") {
					if comma := strings.IndexByte(rawURL, ','); comma >= 0 {
						image["url"] = rawURL[comma+1:]
					}
				}
				part["image_url"] = image
				parts[index] = part
			}
			content = parts
		}
		out := map[string]any{"role": message.Role, "content": content}
		if len(message.ToolCalls) > 0 {
			out["tool_calls"] = message.ToolCalls
		}
		if strings.TrimSpace(message.ToolCallId) != "" {
			out["tool_call_id"] = message.ToolCallId
		}
		messages = append(messages, out)
	}
	return messages, nil
}

func normalizedStop(value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return []string{typed}, nil
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, raw := range typed {
			text, ok := raw.(string)
			if !ok {
				return nil, errors.New("Zhipu v4 stop entries must be strings")
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, errors.New("Zhipu v4 stop must be a string or string array")
	}
}

func convertV4EmbeddingRequest(meta *relaycommon.Meta) ([]byte, error) {
	input, exists := meta.Request.Extra["input"]
	if !exists || input == nil {
		return nil, errors.New("Zhipu v4 embedding input is required")
	}
	request := map[string]any{"model": meta.ModelName, "input": input}
	for _, field := range []string{
		"encoding_format", "dimensions", "user", "seed", "temperature", "top_p", "frequency_penalty", "presence_penalty",
	} {
		if value, ok := meta.Request.Extra[field]; ok {
			request[field] = value
		}
	}
	return protocolkit.MarshalJSON(request)
}

func convertV4ImageRequest(meta *relaycommon.Meta) ([]byte, error) {
	prompt, _ := meta.Request.Extra["prompt"].(string)
	if prompt == "" {
		prompt, _ = meta.Request.Prompt.(string)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("Zhipu v4 image prompt is required")
	}
	request := map[string]any{"model": meta.ModelName, "prompt": prompt}
	for _, field := range []string{
		"n", "size", "quality", "response_format", "style", "user", "extra_fields", "background", "moderation",
		"output_format", "output_compression", "partial_images", "stream", "images", "mask", "input_fidelity",
		"watermark", "watermark_enabled", "user_id", "image",
	} {
		if value, ok := meta.Request.Extra[field]; ok {
			request[field] = value
		}
	}
	if rawN, exists := request["n"]; exists {
		if _, ok := boundedJSONInteger(rawN, 0, maxV4ImageCount); !ok {
			return nil, fmt.Errorf("Zhipu v4 image n must be an integer between 0 and %d", maxV4ImageCount)
		}
	} else if meta.Request.N != nil {
		if *meta.Request.N < 0 || *meta.Request.N > maxV4ImageCount {
			return nil, fmt.Errorf("Zhipu v4 image n must be between 0 and %d", maxV4ImageCount)
		}
		request["n"] = *meta.Request.N
	}
	return protocolkit.MarshalJSON(request)
}

func v4ClaudeResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if meta.IsStream {
		return v4ClaudeStreamResponse(c, response, meta)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Zhipu v4 Claude response: %w", err)
	}
	var decoded protocolkit.ClaudeResponse
	if err := protocolkit.UnmarshalJSON(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode Zhipu v4 Claude response: %w", err)
	}
	if decoded.Error != nil {
		return nil, relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
			Message: decoded.Error.Message, Type: decoded.Error.Type, Code: decoded.Error.Type,
		}, mappedStatusCode(meta, http.StatusBadRequest))
	}
	if err := relaycommon.ObserveClaudeResponse(meta.ToolHooks(), &decoded); err != nil {
		return nil, fmt.Errorf("observe Zhipu v4 Claude tool usage: %w", err)
	}
	var usage *protocolkit.Usage
	if decoded.Usage == nil {
		contentBytes := 0
		for _, block := range decoded.Content {
			contentBytes += len(block.Text)
		}
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
	} else {
		usage = protocolkit.ClaudeUsageToOpenAIUsage(decoded.Usage)
	}
	if err := validateZhipuUsage(usage); err != nil {
		return nil, err
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return usage, fmt.Errorf("write Zhipu v4 Claude response: %w", err)
	}
	return usage, nil
}

func v4ClaudeStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(response.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var claudeUsage *protocolkit.ClaudeUsage
	contentBytes := 0
	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := c.Writer.WriteString(line + "\n"); err != nil {
			return protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage), fmt.Errorf("write Zhipu v4 Claude stream: %w", err)
		}
		c.Writer.Flush()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if protocolkit.UnmarshalJSON([]byte(data), &event) != nil {
			continue
		}
		if err := observeZhipuClaudeStreamToolUsage(meta.ToolHooks(), event); err != nil {
			return protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage), fmt.Errorf("observe Zhipu v4 Claude stream tool usage: %w", err)
		}
		switch event["type"] {
		case "message_start":
			if message, ok := event["message"].(map[string]any); ok {
				claudeUsage = decodedClaudeUsage(message["usage"])
			}
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					contentBytes += len(text)
				}
				if thinking, ok := delta["thinking"].(string); ok {
					contentBytes += len(thinking)
				}
			}
		case "message_delta":
			if update := decodedClaudeUsage(event["usage"]); update != nil {
				if claudeUsage == nil {
					claudeUsage = update
				} else {
					claudeUsage.OutputTokens = update.OutputTokens
				}
			}
		case "error":
			providerError, _ := event["error"].(map[string]any)
			message, _ := providerError["message"].(string)
			errorType, _ := providerError["type"].(string)
			if strings.TrimSpace(message) == "" {
				message = "Zhipu v4 Claude stream failed"
			}
			if strings.TrimSpace(errorType) == "" {
				errorType = "zhipu_error"
			}
			return protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage), relaycommon.UpstreamErrorFromOpenAI(
				protocolkit.OpenAIError{Message: message, Type: errorType, Code: errorType},
				mappedStatusCode(meta, http.StatusBadGateway),
			)
		}
	}
	var usage *protocolkit.Usage
	if claudeUsage == nil {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
	} else {
		usage = protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage)
	}
	if err := validateZhipuUsage(usage); err != nil {
		return usage, err
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read Zhipu v4 Claude event stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	return usage, nil
}

func observeZhipuClaudeStreamToolUsage(hooks *relaycommon.ToolUsageHooks, event map[string]any) error {
	if hooks == nil || event == nil {
		return nil
	}
	eventType, _ := event["type"].(string)
	switch eventType {
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		blockType, _ := block["type"].(string)
		if blockType != "tool_use" || hooks.ObserveClaudeToolUse == nil {
			return nil
		}
		blockIndex := intFromJSON(event["index"])
		blockID, _ := block["id"].(string)
		blockName, _ := block["name"].(string)
		return hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
			BlockIndex: &blockIndex, ID: blockID, Name: blockName,
		})
	case "message_start":
		message, _ := event["message"].(map[string]any)
		return relaycommon.ObserveClaudeUsage(hooks, decodedClaudeUsage(message["usage"]))
	case "message_delta":
		return relaycommon.ObserveClaudeUsage(hooks, decodedClaudeUsage(event["usage"]))
	default:
		return nil
	}
}

func intFromJSON(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func decodedClaudeUsage(raw any) *protocolkit.ClaudeUsage {
	if raw == nil {
		return nil
	}
	body, err := protocolkit.MarshalJSON(raw)
	if err != nil {
		return nil
	}
	var usage protocolkit.ClaudeUsage
	if protocolkit.UnmarshalJSON(body, &usage) != nil {
		return nil
	}
	return &usage
}
