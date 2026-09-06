package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"strings"
)

const (
	openRouterReferer = "https://github.com/MacroXie04/TokenRouter"
	openRouterTitle   = "TokenRouter"
)

func (a *Adaptor) providerType(meta *relaycommon.Meta) channelcatalog.ChannelType {
	if a.ChannelType != channelcatalog.ChannelTypeUnknown {
		return a.ChannelType
	}
	if meta != nil && meta.Channel != nil {
		return channelcatalog.ChannelType(meta.Channel.Type)
	}
	return channelcatalog.ChannelTypeOpenAI
}

func providerDefaultBaseURL(channelType channelcatalog.ChannelType) string {
	index := int(channelType)
	if index >= 0 && index < len(channelcatalog.ChannelBaseURLs) {
		return strings.TrimSpace(channelcatalog.ChannelBaseURLs[index])
	}
	return ""
}

func validateProviderMode(channelType channelcatalog.ChannelType, mode channelcatalog.RelayMode) error {
	var supported bool
	switch channelType {
	case channelcatalog.ChannelTypePerplexity:
		supported = mode == channelcatalog.RelayModeChatCompletions || mode == channelcatalog.RelayModeResponses
	case channelcatalog.ChannelTypeJina:
		supported = mode == channelcatalog.RelayModeRerank || mode == channelcatalog.RelayModeEmbeddings
	case channelcatalog.ChannelTypeSubmodel:
		supported = mode == channelcatalog.RelayModeChatCompletions || mode == channelcatalog.RelayModeCompletions
	default:
		return nil
	}
	if !supported {
		return fmt.Errorf("%s channel does not support relay mode %d", channelcatalog.ChannelTypeName(channelType), mode)
	}
	return nil
}

// joinProviderURL accepts either the documented provider base URL or a custom
// base that already ends in /v1. The latter must not become /v1/v1/... .
func joinProviderURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return relaycommon.JoinURL(base, path)
}

func buildRequestMap(request *protocolkit.GeneralOpenAIRequest, includeTypedFields bool) (map[string]any, error) {
	if request == nil {
		return nil, fmt.Errorf("OpenAI-compatible request is nil")
	}
	body := make(map[string]any)
	if includeTypedFields {
		typedJSON, err := protocolkit.MarshalJSON(request)
		if err != nil {
			return nil, fmt.Errorf("marshal OpenAI-compatible request: %w", err)
		}
		if err := protocolkit.UnmarshalJSON(typedJSON, &body); err != nil {
			return nil, fmt.Errorf("decode OpenAI-compatible request: %w", err)
		}
	}
	if len(request.Extra) == 0 {
		return body, nil
	}
	// Marshal/unmarshal gives the adapter an owned copy. Provider rewrites must
	// never mutate the request snapshot used by retries, billing, or logging.
	extraJSON, err := protocolkit.MarshalJSON(request.Extra)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI-compatible extra fields: %w", err)
	}
	var extra map[string]any
	if err := protocolkit.UnmarshalJSON(extraJSON, &extra); err != nil {
		return nil, fmt.Errorf("decode OpenAI-compatible extra fields: %w", err)
	}
	for key, value := range extra {
		body[key] = value
	}
	// `group` is consumed by TokenRouter's dashboard Playground to choose an
	// authorized billing/routing group. It is relay control-plane state, not an
	// OpenAI request field, and must never cross the provider trust boundary.
	delete(body, "group")
	return body, nil
}

func applyProviderRequest(channelType channelcatalog.ChannelType, mode channelcatalog.RelayMode, stream bool, body map[string]any) error {
	if mode == channelcatalog.RelayModeCompletions && emptyJSONArray(body["messages"]) {
		delete(body, "messages")
	}
	switch channelType {
	case channelcatalog.ChannelTypeOpenRouter:
		applyOpenRouterRequest(body)
	case channelcatalog.ChannelTypePerplexity:
		if mode == channelcatalog.RelayModeChatCompletions {
			applyPerplexityRequest(body)
		}
	case channelcatalog.ChannelTypeJina:
		applyJinaRequest(mode, body)
	case channelcatalog.ChannelTypeDeepSeek:
		ensureStreamUsage(body, stream)
		applyDeepSeekRequest(body)
	case channelcatalog.ChannelTypeMistral:
		delete(body, "stream_options")
		applyMistralRequest(body)
	case channelcatalog.ChannelTypeXai:
		ensureStreamUsage(body, stream)
		applyXAIRequest(mode, body)
	case channelcatalog.ChannelTypeSiliconFlow:
		ensureStreamUsage(body, stream)
		applySiliconFlowRequest(mode, body)
	}
	return nil
}

func retainRequestFields(body map[string]any, allowed ...string) {
	keep := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		keep[key] = struct{}{}
	}
	for key := range body {
		if _, ok := keep[key]; !ok {
			delete(body, key)
		}
	}
}

func applyJinaRequest(mode channelcatalog.RelayMode, body map[string]any) {
	switch mode {
	case channelcatalog.RelayModeEmbeddings:
		retainRequestFields(body, "model", "input", "dimensions", "user", "seed", "temperature", "top_p", "frequency_penalty", "presence_penalty")
	case channelcatalog.RelayModeRerank:
		retainRequestFields(body, "model", "documents", "query", "top_n", "return_documents", "max_chunk_per_doc", "overlap_tokens")
	}
}

func applyPerplexityRequest(body map[string]any) {
	// Perplexity's chat endpoint accepts a narrower contract than OpenAI. Keep
	// search controls, but do not forward tool definitions, message metadata,
	// dashboard controls, or arbitrary extensions to a different trust domain.
	retainRequestFields(body,
		"model", "stream", "messages", "temperature", "top_p",
		"frequency_penalty", "presence_penalty", "search_domain_filter",
		"search_recency_filter", "return_images", "return_related_questions",
		"search_mode", "max_tokens", "max_completion_tokens",
	)

	if value, ok := body["top_p"].(float64); ok && value >= 1 {
		body["top_p"] = 0.99
	}
	if completion, exists := body["max_completion_tokens"]; exists {
		if numberIsNonZero(completion) {
			body["max_tokens"] = completion
		} else if _, hasLegacyLimit := body["max_tokens"]; !hasLegacyLimit {
			body["max_tokens"] = completion
		}
	}
	delete(body, "max_completion_tokens")

	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	for index, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		if message == nil {
			continue
		}
		messages[index] = map[string]any{
			"role":    message["role"],
			"content": message["content"],
		}
	}
}

func ensureStreamUsage(body map[string]any, stream bool) {
	if !stream {
		return
	}
	options, _ := body["stream_options"].(map[string]any)
	if options == nil {
		options = make(map[string]any)
	}
	options["include_usage"] = true
	body["stream_options"] = options
}

func applyOpenRouterRequest(body map[string]any) {
	// OpenRouter uses its own usage request object and rejects OpenAI's
	// stream_options on some routed providers.
	delete(body, "stream_options")
	if usage, exists := body["usage"]; !exists || usage == nil {
		body["usage"] = map[string]any{"include": true}
	}

	model, _ := body["model"].(string)
	effort, _ := body["reasoning_effort"].(string)
	if strings.HasSuffix(model, "-thinking") {
		model = strings.TrimSuffix(model, "-thinking")
		body["model"] = model
		if _, configured := body["reasoning"]; !configured {
			reasoning := map[string]any{"enabled": true}
			if effort != "" && effort != "none" {
				reasoning["effort"] = effort
			}
			body["reasoning"] = reasoning
		}
	} else if _, configured := body["reasoning"]; !configured && effort != "" {
		reasoning := map[string]any{"enabled": effort != "none"}
		if effort != "none" {
			reasoning["effort"] = effort
		}
		body["reasoning"] = reasoning
	}
	delete(body, "reasoning_effort")
}

func applyDeepSeekRequest(body map[string]any) {
	model, _ := body["model"].(string)
	if !strings.HasPrefix(model, "deepseek-v4-") {
		return
	}
	switch {
	case strings.HasSuffix(model, "-none"):
		body["model"] = strings.TrimSuffix(model, "-none")
		body["thinking"] = map[string]any{"type": "disabled"}
		delete(body, "reasoning_effort")
	case strings.HasSuffix(model, "-max"):
		body["model"] = strings.TrimSuffix(model, "-max")
		body["thinking"] = map[string]any{"type": "enabled"}
		body["reasoning_effort"] = "max"
	}
}

func applyMistralRequest(body map[string]any) {
	if maxCompletion, exists := body["max_completion_tokens"]; exists {
		if numberIsNonZero(maxCompletion) {
			body["max_tokens"] = maxCompletion
		}
		delete(body, "max_completion_tokens")
	}
	normalizeMistralMessages(body)
}

func normalizeMistralMessages(body map[string]any) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	used := make(map[string]string)
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		toolCalls, _ := message["tool_calls"].([]any)
		for _, rawCall := range toolCalls {
			call, _ := rawCall.(map[string]any)
			id, _ := call["id"].(string)
			if validMistralToolCallID(id) {
				used[id] = id
			}
		}
	}

	idMap := make(map[string]string)
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		if message == nil {
			continue
		}
		toolCalls, _ := message["tool_calls"].([]any)
		for _, rawCall := range toolCalls {
			call, _ := rawCall.(map[string]any)
			if call == nil {
				continue
			}
			oldID, _ := call["id"].(string)
			if validMistralToolCallID(oldID) {
				continue
			}
			newID, found := idMap[oldID]
			if !found {
				newID = newMistralToolCallID(oldID, used)
				idMap[oldID] = newID
				used[newID] = oldID
			}
			call["id"] = newID
		}
		if oldID, _ := message["tool_call_id"].(string); oldID != "" && !validMistralToolCallID(oldID) {
			newID, found := idMap[oldID]
			if !found {
				newID = newMistralToolCallID(oldID, used)
				idMap[oldID] = newID
				used[newID] = oldID
			}
			message["tool_call_id"] = newID
		}
		if len(toolCalls) > 0 && message["role"] == "assistant" {
			if content, exists := message["content"]; !exists || content == nil || content == "" {
				message["content"] = []any{}
			}
		}
		normalizeMistralImageContent(message)
	}
}

func normalizeMistralImageContent(message map[string]any) {
	parts, ok := message["content"].([]any)
	if !ok {
		return
	}
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil || part["type"] != "image_url" {
			continue
		}
		if image, ok := part["image_url"].(map[string]any); ok {
			if imageURL, ok := image["url"].(string); ok {
				part["image_url"] = imageURL
			}
		}
	}
}

func validMistralToolCallID(id string) bool {
	if len(id) != 9 {
		return false
	}
	for _, char := range []byte(id) {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func newMistralToolCallID(original string, used map[string]string) string {
	for salt := 0; ; salt++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", original, salt)))
		candidate := hex.EncodeToString(digest[:])[:9]
		if owner, exists := used[candidate]; !exists || owner == original {
			return candidate
		}
	}
}

func numberIsNonZero(value any) bool {
	switch typed := value.(type) {
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint64:
		return typed != 0
	default:
		return false
	}
}

func applyXAIRequest(mode channelcatalog.RelayMode, body map[string]any) {
	model, _ := body["model"].(string)
	if strings.HasSuffix(model, "-search") {
		body["model"] = strings.TrimSuffix(model, "-search")
		body["search_parameters"] = map[string]any{"mode": "on"}
	} else if strings.HasPrefix(model, "grok-3-mini") {
		if maxCompletion, exists := body["max_completion_tokens"]; !exists || !numberIsNonZero(maxCompletion) {
			if maxTokens, ok := body["max_tokens"]; ok && numberIsNonZero(maxTokens) {
				body["max_completion_tokens"] = maxTokens
				delete(body, "max_tokens")
			}
		}
		switch {
		case strings.HasSuffix(model, "-high"):
			body["model"] = strings.TrimSuffix(model, "-high")
			body["reasoning_effort"] = "high"
		case strings.HasSuffix(model, "-low"):
			body["model"] = strings.TrimSuffix(model, "-low")
			body["reasoning_effort"] = "low"
		}
	}

	if mode == channelcatalog.RelayModeImagesGenerations || mode == channelcatalog.RelayModeImagesEdits {
		for _, unsupported := range []string{"size", "quality", "style", "user"} {
			delete(body, unsupported)
		}
	}
}

func applySiliconFlowRequest(mode channelcatalog.RelayMode, body map[string]any) {
	if (body["prefix"] != nil || body["suffix"] != nil) && emptyJSONArray(body["messages"]) {
		body["messages"] = []any{map[string]any{"role": "user", "content": ""}}
	}
	if mode != channelcatalog.RelayModeImagesGenerations && mode != channelcatalog.RelayModeImagesEdits {
		return
	}
	if _, exists := body["image_size"]; !exists {
		if size, ok := body["size"]; ok {
			body["image_size"] = size
		}
	}
	if _, exists := body["batch_size"]; !exists {
		if count, ok := body["n"]; ok {
			body["batch_size"] = count
		}
	}
	delete(body, "size")
	delete(body, "n")
}

func emptyJSONArray(value any) bool {
	items, ok := value.([]any)
	return !ok || len(items) == 0
}

// normalizeProviderUsage applies documented provider quirks before the shared
// billing validation sees usage. It reports whether the client response should
// be rewritten to expose the normalized values as well.
func normalizeProviderUsage(channelType channelcatalog.ChannelType, mode channelcatalog.RelayMode, usage *protocolkit.Usage) bool {
	if usage == nil {
		return false
	}
	switch channelType {
	case channelcatalog.ChannelTypeDeepSeek:
		if usage.PromptCacheHitTokens > 0 {
			if usage.PromptTokensDetails == nil {
				usage.PromptTokensDetails = &protocolkit.InputTokenDetails{}
			}
			if usage.PromptTokensDetails.CachedTokens == 0 {
				usage.PromptTokensDetails.CachedTokens = usage.PromptCacheHitTokens
			}
		}
	case channelcatalog.ChannelTypeXai:
		usage.CompletionTokens = usage.TotalTokens - usage.PromptTokens
		if usage.CompletionTokensDetails != nil {
			usage.CompletionTokensDetails.TextTokens = usage.CompletionTokens - usage.CompletionTokensDetails.ReasoningTokens
			if usage.ReasoningTokens == 0 {
				usage.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
			}
		}
		return true
	case channelcatalog.ChannelTypeJina:
		if mode == channelcatalog.RelayModeRerank && usage.TotalTokens > 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
			usage.PromptTokens = usage.TotalTokens
			return true
		}
	}
	return false
}

func responsesUsage(usage *protocolkit.ResponsesUsage) *protocolkit.Usage {
	if usage == nil {
		return nil
	}
	out := &protocolkit.Usage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}
	if usage.InputTokensDetails != nil {
		out.PromptTokensDetails = &protocolkit.InputTokenDetails{
			CachedTokens:          usage.InputTokensDetails.CachedTokens,
			CachedCreationTokens:  usage.InputTokensDetails.CachedCreationTokens,
			CacheWriteTokens:      usage.InputTokensDetails.CacheWriteTokens,
			CacheCreation5mTokens: usage.InputTokensDetails.CacheCreation5mTokens,
			CacheCreation1hTokens: usage.InputTokensDetails.CacheCreation1hTokens,
			TextTokens:            usage.InputTokensDetails.TextTokens,
			AudioTokens:           usage.InputTokensDetails.AudioTokens,
			ImageTokens:           usage.InputTokensDetails.ImageTokens,
			ReasoningTokens:       usage.InputTokensDetails.ReasoningTokens,
		}
	}
	if usage.OutputTokensDetails != nil {
		out.CompletionTokensDetails = &protocolkit.OutputTokenDetails{
			TextTokens:      usage.OutputTokensDetails.TextTokens,
			AudioTokens:     usage.OutputTokensDetails.AudioTokens,
			ImageTokens:     usage.OutputTokensDetails.ImageTokens,
			ReasoningTokens: usage.OutputTokensDetails.ReasoningTokens,
		}
		out.ReasoningTokens = usage.OutputTokensDetails.ReasoningTokens
	}
	return protocolkit.NormalizeOpenAIUsageAliases(out)
}

func streamChoiceTextLength(choice protocolkit.ChatCompletionsStreamResponseChoice) int {
	delta := choice.Delta
	length := len(delta.Content) + len(delta.ReasoningContent) + len(delta.Reasoning)
	for _, tool := range delta.ToolCalls {
		length += len(tool.Id) + len(tool.Type)
		if tool.Function != nil {
			length += len(tool.Function.Name) + len(tool.Function.Arguments)
		}
	}
	return length
}
