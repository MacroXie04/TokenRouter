package protocolkit

import "strings"

// This file implements protocol conversion between Claude, Gemini and the
// OpenAI-compatible wire format, plus usage normalization. These functions are
// pure and independently testable.

// ClaudeUsageToOpenAIUsage normalizes Anthropic usage into OpenAI usage.
//
// Anthropic reports input_tokens excluding cache; cache is separate
// (cache_creation_input_tokens, cache_read_input_tokens). OpenAI-style
// prompt_tokens must therefore include cache so that token accounting matches
// the "prompt_tokens is everything" convention.
func ClaudeUsageToOpenAIUsage(u *ClaudeUsage) *Usage {
	if u == nil {
		return &Usage{}
	}
	cacheCreation5m, cacheCreation1h, cacheCreationTotal, valid := normalizeClaudeCacheCreation(u)
	prompt, promptOK := checkedUsageSum(u.InputTokens, cacheCreationTotal, u.CacheReadInputTokens)
	completion := u.OutputTokens
	total, totalOK := checkedUsageSum(prompt, completion)
	if !valid || !promptOK || !totalOK {
		// Preserve a fail-closed sentinel for the settlement validator. Returning
		// a wrapped value is not possible because this conversion is also used by
		// wire-format adapters with a long-standing non-error signature.
		prompt = -1
		total = -1
	}
	return &Usage{
		PromptTokens:                prompt,
		CompletionTokens:            completion,
		TotalTokens:                 total,
		BillingSemantic:             "anthropic",
		PromptCacheHitTokens:        u.CacheReadInputTokens,
		PromptCacheMissTokens:       cacheCreationTotal,
		PromptCacheWriteTokens:      cacheCreationTotal,
		PromptCacheCreationTokens:   cacheCreationTotal,
		PromptCacheCreation5mTokens: cacheCreation5m,
		PromptCacheCreation1hTokens: cacheCreation1h,
		PromptTokensDetails: &InputTokenDetails{
			CachedTokens:          u.CacheReadInputTokens,
			CachedCreationTokens:  cacheCreationTotal,
			CacheWriteTokens:      cacheCreationTotal,
			CacheCreation5mTokens: cacheCreation5m,
			CacheCreation1hTokens: cacheCreation1h,
		},
	}
}

func normalizeClaudeCacheCreation(u *ClaudeUsage) (fiveMinute, oneHour, total int, valid bool) {
	if u == nil {
		return 0, 0, 0, true
	}
	if u.CacheCreation != nil {
		fiveMinute = u.CacheCreation.Ephemeral5mInputTokens
		oneHour = u.CacheCreation.Ephemeral1hInputTokens
	}
	splitTotal, ok := checkedUsageSum(fiveMinute, oneHour)
	if !ok || u.CacheCreationInputTokens < 0 {
		return fiveMinute, oneHour, u.CacheCreationInputTokens, false
	}
	total = u.CacheCreationInputTokens
	if splitTotal > total {
		total = splitTotal
	} else {
		fiveMinute += total - splitTotal
	}
	return fiveMinute, oneHour, total, true
}

func checkedUsageSum(values ...int) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, value := range values {
		if value < 0 || value > maxInt-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

// GeminiUsageToOpenAIUsage normalizes Gemini usage into OpenAI usage.
func GeminiUsageToOpenAIUsage(meta *GeminiUsageMetadata) *Usage {
	if meta == nil {
		return &Usage{}
	}
	prompt, promptOK := checkedUsageSum(meta.PromptTokenCount, meta.ToolUsePromptTokenCount)
	completion, completionOK := checkedUsageSum(meta.CandidatesTokenCount, meta.ThoughtsTokenCount)
	total := meta.TotalTokenCount
	totalOK := total >= 0
	if total == 0 {
		total, totalOK = checkedUsageSum(prompt, completion)
	}
	u := &Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		ReasoningTokens:  meta.ThoughtsTokenCount,
	}
	in := &InputTokenDetails{CachedTokens: meta.CachedContentTokenCount}
	out := &OutputTokenDetails{ReasoningTokens: meta.ThoughtsTokenCount}
	detailsOK := true
	for _, details := range [][]GeminiPromptTokensDetails{
		meta.PromptTokensDetails,
		meta.ToolUsePromptTokensDetails,
	} {
		for _, d := range details {
			if !addGeminiInputTokenDetail(in, d) {
				detailsOK = false
			}
		}
	}
	for _, d := range meta.CandidatesTokensDetails {
		if !addGeminiOutputTokenDetail(out, d) {
			detailsOK = false
		}
	}
	u.AudioTokens = in.AudioTokens
	u.PromptTokensDetails = in
	u.CompletionTokensDetails = out
	if !promptOK {
		u.PromptTokens = -1
	}
	if !completionOK {
		u.CompletionTokens = -1
	}
	if !promptOK || !completionOK || !totalOK || !detailsOK {
		u.TotalTokens = -1
	}
	return u
}

func addGeminiInputTokenDetail(details *InputTokenDetails, d GeminiPromptTokensDetails) bool {
	var target *int
	switch strings.ToUpper(strings.TrimSpace(d.Modality)) {
	case "AUDIO":
		target = &details.AudioTokens
	case "IMAGE":
		target = &details.ImageTokens
	case "TEXT":
		target = &details.TextTokens
	default:
		return d.TokenCount >= 0
	}
	next, ok := checkedUsageSum(*target, d.TokenCount)
	if !ok {
		*target = -1
		return false
	}
	*target = next
	return true
}

func addGeminiOutputTokenDetail(details *OutputTokenDetails, d GeminiCandidatesTokensDetails) bool {
	var target *int
	switch strings.ToUpper(strings.TrimSpace(d.Modality)) {
	case "AUDIO":
		target = &details.AudioTokens
	case "IMAGE":
		target = &details.ImageTokens
	case "TEXT":
		target = &details.TextTokens
	default:
		return d.TokenCount >= 0
	}
	next, ok := checkedUsageSum(*target, d.TokenCount)
	if !ok {
		*target = -1
		return false
	}
	*target = next
	return true
}

// ClaudeRequestToOpenAIRequest converts a Claude Messages request into an
// OpenAI-compatible chat completions request.
func ClaudeRequestToOpenAIRequest(req *ClaudeRequest) *GeneralOpenAIRequest {
	if req == nil {
		return &GeneralOpenAIRequest{}
	}
	out := &GeneralOpenAIRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		MaxTokens:   &req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}
	if req.Metadata != nil {
		out.User = req.Metadata.UserId
	}

	// System prompt becomes a leading system message.
	if req.System != nil {
		if s, ok := req.System.(string); ok && s != "" {
			out.Messages = append(out.Messages, Message{Role: "system", Content: s})
		} else {
			out.Messages = append(out.Messages, Message{Role: "system", Content: req.System})
		}
	}
	if req.Prompt != "" {
		out.Messages = append(out.Messages, Message{Role: "user", Content: req.Prompt})
	}

	for _, m := range req.Messages {
		out.Messages = append(out.Messages, claudeMessageToOpenAI(m)...)
	}

	// Tools.
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, claudeToolToOpenAI(t))
	}
	if req.ToolChoice != nil {
		out.ToolChoice = req.ToolChoice
	}
	return out
}

func claudeMessageToOpenAI(m ClaudeMessage) []Message {
	switch content := m.Content.(type) {
	case string:
		return []Message{{Role: m.Role, Content: content}}
	case []any:
		var texts []MediaContent
		var toolCalls []ToolCallRequest
		var toolResults []Message
		for _, raw := range content {
			obj, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			t, _ := obj["type"].(string)
			switch t {
			case "text":
				if s, ok := obj["text"].(string); ok {
					texts = append(texts, MediaContent{Type: ContentTypeText, Text: s})
				}
			case "image":
				src := extractSource(obj["source"])
				texts = append(texts, MediaContent{Type: ContentTypeImageURL, ImageURL: &MessageImageUrl{Url: src}})
			case "tool_use":
				tc := ToolCallRequest{Id: strOr(obj["id"]), Type: "function"}
				args := ""
				if a, ok := obj["input"]; ok {
					args = ToJSONString(a)
				}
				name := strOr(obj["name"])
				tc.Function = &FunctionRequest{Name: name, Arguments: args}
				toolCalls = append(toolCalls, tc)
			case "tool_result":
				toolResults = append(toolResults, Message{
					Role:       "tool",
					ToolCallId: strOr(obj["tool_use_id"]),
					Content:    claudeToolResultContent(obj["content"]),
				})
			}
		}
		msg := Message{Role: m.Role}
		if len(texts) > 0 {
			// Collapse to a plain string when only text is present (matches
			// the OpenAI convention of string content for simple messages).
			if len(texts) == 1 && texts[0].Type == ContentTypeText {
				msg.Content = texts[0].Text
			} else {
				anyParts := make([]any, len(texts))
				for i := range texts {
					anyParts[i] = texts[i]
				}
				msg.Content = anyParts
			}
		}
		if len(toolCalls) > 0 {
			msg.Role = "assistant"
			msg.ToolCalls = toolCalls
		}
		out := make([]Message, 0, len(toolResults)+1)
		out = append(out, toolResults...)
		if len(texts) > 0 || len(toolCalls) > 0 {
			out = append(out, msg)
		}
		if len(out) == 0 {
			out = append(out, Message{Role: m.Role, Content: content})
		}
		return out
	default:
		return []Message{{Role: m.Role, Content: content}}
	}
}

func claudeToolResultContent(content any) any {
	if content == nil {
		return ""
	}
	if text, ok := content.(string); ok {
		return text
	}
	if parts, ok := content.([]any); ok {
		var text strings.Builder
		for _, raw := range parts {
			if part, ok := raw.(map[string]any); ok && part["type"] == "text" {
				text.WriteString(strOr(part["text"]))
			}
		}
		if text.Len() > 0 {
			return text.String()
		}
	}
	return content
}

func claudeToolToOpenAI(t Tool) ToolCallRequest {
	parameters := map[string]any{"type": "object"}
	if t.InputSchema != nil {
		if t.InputSchema.Type != "" {
			parameters["type"] = t.InputSchema.Type
		}
		if t.InputSchema.Properties != nil {
			parameters["properties"] = t.InputSchema.Properties
		}
		if len(t.InputSchema.Required) > 0 {
			parameters["required"] = append([]string(nil), t.InputSchema.Required...)
		}
	}
	return ToolCallRequest{
		Type:     "function",
		Function: &FunctionRequest{Name: t.Name, Description: t.Description, Parameters: parameters},
	}
}

// ClaudeResponseToOpenAIResponse converts a non-stream Claude response into an
// OpenAI chat completions response.
func ClaudeResponseToOpenAIResponse(resp *ClaudeResponse) *ChatCompletionsResponse {
	if resp == nil {
		return &ChatCompletionsResponse{}
	}
	var content strings.Builder
	var toolCalls []ToolCallResponse
	for _, part := range resp.Content {
		switch part.Type {
		case "text":
			content.WriteString(part.Text)
		case "tool_use":
			toolCalls = append(toolCalls, ToolCallResponse{
				Id:       part.ID,
				Type:     "function",
				Function: &FunctionResponse{Name: part.Name, Arguments: ToJSONString(part.Input)},
			})
		}
	}
	msg := ChatResponseMessage{
		Role:    "assistant",
		Content: content.String(),
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
		if content.Len() == 0 {
			msg.Content = nil
		}
	}
	finishReason := mapClaudeStopReason(resp.StopReason)
	return &ChatCompletionsResponse{
		Id:      resp.Id,
		Object:  "chat.completion",
		Model:   resp.Model,
		Choices: []ChatCompletionsChoice{{Index: 0, Message: &msg, FinishReason: finishReason}},
		Usage:   ClaudeUsageToOpenAIUsage(resp.Usage),
		Error:   claudeErrorToOpenAI(resp.Error),
	}
}

func mapClaudeStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

func claudeErrorToOpenAI(e *ClaudeError) *OpenAIError {
	if e == nil {
		return nil
	}
	return &OpenAIError{Message: e.Message, Type: e.Type}
}

// GeminiRequestToOpenAIRequest converts a Gemini GenerateContent request into
// an OpenAI-compatible chat completions request.
func GeminiRequestToOpenAIRequest(req *GeminiChatRequest) *GeneralOpenAIRequest {
	if req == nil {
		return &GeneralOpenAIRequest{}
	}
	out := &GeneralOpenAIRequest{Model: req.Model}
	if req.GenerationConfig != nil {
		out.Temperature = req.GenerationConfig.Temperature
		out.TopP = req.GenerationConfig.TopP
		out.MaxTokens = req.GenerationConfig.MaxOutputTokens
		if len(req.GenerationConfig.StopSequences) > 0 {
			out.Stop = req.GenerationConfig.StopSequences
		}
	}
	for _, c := range req.Contents {
		out.Messages = append(out.Messages, geminiContentToOpenAI(c)...)
	}
	for _, t := range req.Tools {
		for _, f := range t.FunctionDeclarations {
			out.Tools = append(out.Tools, ToolCallRequest{
				Type:     "function",
				Function: &FunctionRequest{Name: f.Name, Description: f.Description, Parameters: f.Parameters},
			})
		}
	}
	return out
}

func geminiContentToOpenAI(c GeminiChatContent) []Message {
	var texts []MediaContent
	var toolCalls []ToolCallRequest
	var toolResults []Message
	for _, p := range c.Parts {
		if p.Text != "" {
			texts = append(texts, MediaContent{Type: ContentTypeText, Text: p.Text})
		}
		if p.InlineData != nil {
			texts = append(texts, MediaContent{
				Type:     ContentTypeImageURL,
				ImageURL: &MessageImageUrl{Url: "data:" + p.InlineData.MimeType + ";base64," + p.InlineData.Data},
			})
		}
		if p.FileData != nil {
			texts = append(texts, MediaContent{
				Type:     ContentTypeImageURL,
				ImageURL: &MessageImageUrl{Url: p.FileData.FileUri},
			})
		}
		if p.FunctionCall != nil {
			toolCalls = append(toolCalls, ToolCallRequest{
				Id:       geminiToolCallID(p.FunctionCall.Name),
				Type:     "function",
				Function: &FunctionRequest{Name: p.FunctionCall.Name, Arguments: ToJSONString(p.FunctionCall.Args)},
			})
		}
		if p.FunctionResponse != nil {
			toolResults = append(toolResults, Message{
				Role:       "tool",
				Name:       p.FunctionResponse.Name,
				ToolCallId: geminiToolCallID(p.FunctionResponse.Name),
				Content:    ToJSONString(p.FunctionResponse.Response),
			})
		}
	}
	role := c.Role
	if role == "" {
		role = "user"
	}
	if role == "model" {
		role = "assistant"
	}
	msg := Message{Role: role}
	if len(texts) > 0 {
		if len(texts) == 1 && texts[0].Type == ContentTypeText {
			msg.Content = texts[0].Text
		} else {
			parts := make([]any, len(texts))
			for i := range texts {
				parts[i] = texts[i]
			}
			msg.Content = parts
		}
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	out := make([]Message, 0, len(toolResults)+1)
	out = append(out, toolResults...)
	if len(texts) > 0 || len(toolCalls) > 0 {
		out = append(out, msg)
	}
	if len(out) == 0 {
		out = append(out, msg)
	}
	return out
}

func geminiToolCallID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "call_gemini"
	}
	var id strings.Builder
	id.WriteString("call_")
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			id.WriteRune(r)
		} else {
			id.WriteByte('_')
		}
	}
	return id.String()
}

// GeminiResponseToOpenAIResponse converts a non-stream Gemini response into an
// OpenAI chat completions response.
func GeminiResponseToOpenAIResponse(resp *GeminiChatResponse) *ChatCompletionsResponse {
	if resp == nil {
		return &ChatCompletionsResponse{}
	}
	out := &ChatCompletionsResponse{Object: "chat.completion", Model: "gemini"}
	if len(resp.Candidates) > 0 {
		cand := resp.Candidates[0]
		var content string
		var toolCalls []ToolCallResponse
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				if p.Text != "" {
					content += p.Text
				}
				if p.FunctionCall != nil {
					toolCalls = append(toolCalls, ToolCallResponse{
						Id:       geminiToolCallID(p.FunctionCall.Name),
						Type:     "function",
						Function: &FunctionResponse{Name: p.FunctionCall.Name, Arguments: ToJSONString(p.FunctionCall.Args)},
					})
				}
			}
		}
		msg := ChatResponseMessage{Role: "assistant", Content: content}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
			if content == "" {
				msg.Content = nil
			}
		}
		finishReason := mapGeminiFinishReason(cand.FinishReason)
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		}
		out.Choices = append(out.Choices, ChatCompletionsChoice{
			Index:        0,
			Message:      &msg,
			FinishReason: finishReason,
		})
	}
	out.Usage = GeminiUsageToOpenAIUsage(resp.UsageMetadata)
	if resp.Error != nil {
		out.Error = &OpenAIError{Message: resp.Error.Message, Code: resp.Error.Status}
	}
	return out
}

func mapGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	case "BLOCKLIST":
		return "content_filter"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	default:
		return reason
	}
}

func extractSource(v any) string {
	if m, ok := v.(map[string]any); ok {
		switch m["type"] {
		case "base64":
			if d, ok := m["data"].(string); ok {
				return "data:" + strOr(m["media_type"]) + ";base64," + d
			}
		case "url":
			return strOr(m["url"])
		}
	}
	return ""
}

// OpenAIRequestToClaudeRequest converts an OpenAI chat request to an Anthropic
// Messages request. This is the request direction for the gateway (OpenAI is
// always client-facing).
func OpenAIRequestToClaudeRequest(req *GeneralOpenAIRequest) *ClaudeRequest {
	if req == nil {
		return &ClaudeRequest{}
	}
	out := &ClaudeRequest{
		Model:         req.Model,
		Stream:        req.Stream,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		MaxTokens:     maxTokensFromRequest(req),
		StopSequences: stopToStrings(req.Stop),
	}
	if req.User != "" {
		out.Metadata = &ClaudeMetadata{UserId: req.User}
	}
	// System message(s) -> system field.
	var messages []ClaudeMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			out.System = m.Content
			continue
		}
		messages = append(messages, openAIMessageToClaude(m))
	}
	out.Messages = messages
	// Tools.
	for _, t := range req.Tools {
		if t.Function == nil {
			continue
		}
		schema := openAIToolSchemaToClaude(t.Function.Parameters)
		out.Tools = append(out.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
	}
	if req.ToolChoice != nil {
		out.ToolChoice = req.ToolChoice
	}
	return out
}

func openAIToolSchemaToClaude(parameters map[string]any) *InputSchema {
	schema := &InputSchema{Type: "object"}
	if len(parameters) == 0 {
		return schema
	}
	if schemaType, ok := parameters["type"].(string); ok && schemaType != "" {
		schema.Type = schemaType
	}
	if properties, ok := parameters["properties"].(map[string]any); ok {
		schema.Properties = properties
	} else {
		// Older TokenRouter callers supplied a property map directly. Retain
		// compatibility while emitting the canonical Anthropic schema shape.
		schema.Properties = make(map[string]any)
		for key, value := range parameters {
			if key != "type" && key != "required" {
				schema.Properties[key] = value
			}
		}
	}
	switch required := parameters["required"].(type) {
	case []string:
		schema.Required = append([]string(nil), required...)
	case []any:
		for _, item := range required {
			if name, ok := item.(string); ok {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	return schema
}

func openAIMessageToClaude(m Message) ClaudeMessage {
	if m.Role == "tool" {
		content := m.Content
		if content == nil {
			content = ""
		}
		return ClaudeMessage{Role: "user", Content: []any{ClaudeMediaMessage{
			Type: "tool_result", ToolUseID: m.ToolCallId, Content: content,
		}}}
	}

	cm := ClaudeMessage{Role: m.Role}
	parts := openAIContentToClaudeParts(m.Content)
	for _, call := range m.ToolCalls {
		if call.Function == nil {
			continue
		}
		parts = append(parts, ClaudeMediaMessage{
			Type: "tool_use", ID: call.Id, Name: call.Function.Name,
			Input: decodeFunctionArguments(call.Function.Arguments),
		})
	}
	if m.FunctionCall != nil {
		parts = append(parts, ClaudeMediaMessage{
			Type: "tool_use", Name: m.FunctionCall.Name,
			Input: decodeFunctionArguments(m.FunctionCall.Arguments),
		})
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		cm.Content = parts[0].Text
	} else if len(parts) > 0 {
		content := make([]any, len(parts))
		for i := range parts {
			content[i] = parts[i]
		}
		cm.Content = content
	} else if m.Content != nil {
		cm.Content = m.Content
	}
	return cm
}

func openAIContentToClaudeParts(content any) []ClaudeMediaMessage {
	switch value := content.(type) {
	case string:
		if value == "" {
			return nil
		}
		return []ClaudeMediaMessage{{Type: "text", Text: value}}
	case []any:
		parts := make([]ClaudeMediaMessage, 0, len(value))
		for _, raw := range value {
			part, ok := raw.(MediaContent)
			if !ok {
				encoded, err := MarshalJSON(raw)
				if err != nil || UnmarshalJSON(encoded, &part) != nil {
					continue
				}
			}
			switch part.Type {
			case ContentTypeText:
				parts = append(parts, ClaudeMediaMessage{Type: "text", Text: part.Text})
			case ContentTypeImageURL:
				if part.ImageURL == nil {
					continue
				}
				source := &ClaudeMessageSource{Type: "url", Url: part.ImageURL.Url}
				if strings.HasPrefix(part.ImageURL.Url, "data:") {
					source = dataURLToSource(part.ImageURL.Url)
				}
				parts = append(parts, ClaudeMediaMessage{Type: "image", Source: source})
			}
		}
		return parts
	default:
		return nil
	}
}

func decodeFunctionArguments(arguments string) any {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}
	}
	var decoded any
	if err := UnmarshalJSON([]byte(arguments), &decoded); err != nil {
		// Preserve malformed input rather than silently changing its meaning;
		// Anthropic will reject a non-object input as an invalid request.
		return arguments
	}
	return decoded
}

func dataURLToSource(dataURL string) *ClaudeMessageSource {
	// data:<media_type>;base64,<data>
	rest := strings.TrimPrefix(dataURL, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return &ClaudeMessageSource{Type: "url", Url: dataURL}
	}
	meta := rest[:comma]
	data := rest[comma+1:]
	mediaType := "image/png"
	if i := strings.Index(meta, ";"); i >= 0 {
		mediaType = meta[:i]
	}
	if !strings.HasSuffix(meta, ";base64") {
		return &ClaudeMessageSource{Type: "url", Url: dataURL}
	}
	return &ClaudeMessageSource{Type: "base64", MediaType: mediaType, Data: data}
}

func maxTokensFromRequest(req *GeneralOpenAIRequest) int {
	if req.MaxCompletionTokens != nil {
		return *req.MaxCompletionTokens
	}
	if req.MaxTokens != nil {
		return *req.MaxTokens
	}
	return 4096
}

func stopToStrings(stop any) []string {
	switch s := stop.(type) {
	case string:
		if s == "" {
			return nil
		}
		return []string{s}
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, v := range s {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// OpenAIRequestToGeminiRequest converts an OpenAI chat request to a Gemini
// GenerateContent request.
func OpenAIRequestToGeminiRequest(req *GeneralOpenAIRequest) *GeminiChatRequest {
	if req == nil {
		return &GeminiChatRequest{}
	}
	out := &GeminiChatRequest{Model: req.Model}
	genCfg := &GeminiChatGenerationConfig{
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: intPtr(maxTokensFromRequest(req)),
		StopSequences:   stopToStrings(req.Stop),
	}
	out.GenerationConfig = genCfg
	toolNames := make(map[string]string)
	for _, m := range req.Messages {
		if m.Role == "system" {
			out.SystemInstruction = &GeminiChatContent{Role: "system", Parts: []GeminiPart{{Text: contentToText(m.Content)}}}
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model"
		} else if role == "tool" {
			role = "user"
		}
		content := GeminiChatContent{Role: role}
		content.Parts = openAIContentToGeminiParts(m.Content)
		for _, call := range m.ToolCalls {
			if call.Function == nil {
				continue
			}
			toolNames[call.Id] = call.Function.Name
			args := decodeFunctionArguments(call.Function.Arguments)
			arguments, ok := args.(map[string]any)
			if !ok {
				arguments = map[string]any{"raw": args}
			}
			content.Parts = append(content.Parts, GeminiPart{
				FunctionCall:     &FunctionCall{Name: call.Function.Name, Args: arguments},
				ThoughtSignature: GeminiThoughtSignatureBypass,
			})
		}
		if m.FunctionCall != nil {
			args := decodeFunctionArguments(m.FunctionCall.Arguments)
			arguments, ok := args.(map[string]any)
			if !ok {
				arguments = map[string]any{"raw": args}
			}
			content.Parts = append(content.Parts, GeminiPart{
				FunctionCall:     &FunctionCall{Name: m.FunctionCall.Name, Args: arguments},
				ThoughtSignature: GeminiThoughtSignatureBypass,
			})
		}
		if m.Role == "tool" {
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = toolNames[m.ToolCallId]
			}
			if name == "" {
				name = strings.TrimPrefix(m.ToolCallId, "call_")
			}
			content.Parts = []GeminiPart{{FunctionResponse: &GeminiFunctionResponse{
				Name: name, Response: geminiFunctionResponseValue(m.Content),
			}}}
		}
		out.Contents = append(out.Contents, content)
	}
	for _, t := range req.Tools {
		if t.Function == nil {
			continue
		}
		out.Tools = append(out.Tools, GeminiChatTool{
			FunctionDeclarations: []GeminiFunction{{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			}},
		})
	}
	return out
}

func geminiFunctionResponseValue(content any) any {
	if text, ok := content.(string); ok {
		var decoded any
		if strings.TrimSpace(text) != "" && UnmarshalJSON([]byte(text), &decoded) == nil {
			return decoded
		}
		return map[string]any{"result": text}
	}
	if content == nil {
		return map[string]any{}
	}
	return content
}

func contentToText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, raw := range c {
			if pm, ok := raw.(MediaContent); ok && pm.Type == ContentTypeText {
				sb.WriteString(pm.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func openAIContentToGeminiParts(content any) []GeminiPart {
	switch c := content.(type) {
	case string:
		return []GeminiPart{{Text: c}}
	case []any:
		var parts []GeminiPart
		for _, raw := range c {
			pm, ok := raw.(MediaContent)
			if !ok {
				if m, ok := raw.(map[string]any); ok {
					pm = MediaContent{Type: strOr(m["type"]), Text: strOr(m["text"])}
				} else {
					continue
				}
			}
			switch pm.Type {
			case ContentTypeText:
				parts = append(parts, GeminiPart{Text: pm.Text})
			case ContentTypeImageURL:
				if pm.ImageURL != nil {
					if strings.HasPrefix(pm.ImageURL.Url, "data:") {
						if inline := dataURLToInline(pm.ImageURL.Url); inline != nil {
							parts = append(parts, GeminiPart{InlineData: inline})
							continue
						}
					}
					parts = append(parts, GeminiPart{FileData: &GeminiFileData{MimeType: "image/png", FileUri: pm.ImageURL.Url}})
				}
			}
		}
		return parts
	}
	return nil
}

func dataURLToInline(dataURL string) *GeminiInlineData {
	rest := strings.TrimPrefix(dataURL, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return nil
	}
	meta := rest[:comma]
	data := rest[comma+1:]
	mediaType := "image/png"
	if i := strings.Index(meta, ";"); i >= 0 {
		mediaType = meta[:i]
	}
	if !strings.HasSuffix(meta, ";base64") {
		return nil
	}
	return &GeminiInlineData{MimeType: mediaType, Data: data}
}

func intPtr(v int) *int { return &v }

// OpenAIFinishReasonToClaudeStopReason maps an OpenAI finish_reason to the
// closest Anthropic stop_reason.
func OpenAIFinishReasonToClaudeStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	case "stop", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// OpenAIResponseToClaudeResponse converts a non-stream OpenAI chat completion
// into an Anthropic Messages response.
func OpenAIResponseToClaudeResponse(resp *ChatCompletionsResponse) *ClaudeResponse {
	if resp == nil {
		return nil
	}
	out := &ClaudeResponse{
		Id:    resp.Id,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
	}
	for _, ch := range resp.Choices {
		if ch.Message != nil {
			switch content := ch.Message.Content.(type) {
			case string:
				out.Content = append(out.Content, ClaudeMediaMessage{Type: "text", Text: content})
			case nil:
			default:
				out.Content = append(out.Content, ClaudeMediaMessage{Type: "text", Text: contentString(content)})
			}
			for _, call := range ch.Message.ToolCalls {
				if call.Function == nil {
					continue
				}
				out.Content = append(out.Content, ClaudeMediaMessage{
					Type: "tool_use", ID: call.Id, Name: call.Function.Name,
					Input: decodeFunctionArguments(call.Function.Arguments),
				})
			}
		}
		out.StopReason = OpenAIFinishReasonToClaudeStopReason(ch.FinishReason)
	}
	if resp.Usage != nil {
		out.Usage = &ClaudeUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
	}
	return out
}

func contentString(v any) string {
	if b, err := MarshalJSON(v); err == nil {
		return string(b)
	}
	return ""
}

// OpenAIResponseToGeminiResponse converts a non-stream OpenAI chat completion
// into a native Gemini GenerateContent response. It is the response direction
// for gemini-format relays routed to OpenAI-compatible channels.
func OpenAIResponseToGeminiResponse(resp *ChatCompletionsResponse) *GeminiChatResponse {
	if resp == nil {
		return &GeminiChatResponse{}
	}
	out := &GeminiChatResponse{}
	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		cand := GeminiChatCandidate{Index: 0, FinishReason: openAIFinishReasonToGemini(ch.FinishReason)}
		if ch.Message != nil {
			content := &GeminiChatContent{Role: "model"}
			if ch.Message.Content != nil {
				switch text := ch.Message.Content.(type) {
				case string:
					if text != "" {
						content.Parts = append(content.Parts, GeminiPart{Text: text})
					}
				case nil:
				default:
					content.Parts = append(content.Parts, GeminiPart{Text: contentString(text)})
				}
			}
			for _, tc := range ch.Message.ToolCalls {
				if tc.Function == nil {
					continue
				}
				args := map[string]any{}
				if tc.Function.Arguments != "" {
					var parsed any
					if UnmarshalJSON([]byte(tc.Function.Arguments), &parsed) == nil {
						if m, ok := parsed.(map[string]any); ok {
							args = m
						}
					}
				}
				content.Parts = append(content.Parts, GeminiPart{
					FunctionCall: &FunctionCall{Name: tc.Function.Name, Args: args},
				})
			}
			if len(content.Parts) > 0 {
				cand.Content = content
			}
		}
		out.Candidates = append(out.Candidates, cand)
	}
	if resp.Usage != nil {
		out.UsageMetadata = &GeminiUsageMetadata{
			PromptTokenCount:     resp.Usage.PromptTokens,
			CandidatesTokenCount: resp.Usage.CompletionTokens,
			TotalTokenCount:      resp.Usage.TotalTokens,
		}
	}
	if resp.Error != nil {
		out.Error = &GeminiError{Code: 500, Message: resp.Error.Message, Status: resp.Error.Code}
	}
	return out
}

// openAIFinishReasonToGemini maps an OpenAI finish reason to a Gemini one.
func openAIFinishReasonToGemini(reason string) string {
	switch reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls", "function_call":
		return "STOP"
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}
