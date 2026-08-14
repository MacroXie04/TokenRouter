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
	prompt := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	completion := u.OutputTokens
	return &Usage{
		PromptTokens:            prompt,
		CompletionTokens:        completion,
		TotalTokens:             prompt + completion,
		PromptCacheHitTokens:    u.CacheReadInputTokens,
		PromptCacheMissTokens:   u.CacheCreationInputTokens,
		PromptCacheWriteTokens:  u.CacheCreationInputTokens,
		PromptTokensDetails: &InputTokenDetails{
			CachedTokens:         u.CacheReadInputTokens,
			CachedCreationTokens: u.CacheCreationInputTokens,
			CacheWriteTokens:     u.CacheCreationInputTokens,
		},
	}
}

// GeminiUsageToOpenAIUsage normalizes Gemini usage into OpenAI usage.
func GeminiUsageToOpenAIUsage(meta *GeminiUsageMetadata) *Usage {
	if meta == nil {
		return &Usage{}
	}
	u := &Usage{
		PromptTokens:     meta.PromptTokenCount,
		CompletionTokens: meta.CandidatesTokenCount,
		TotalTokens:      meta.TotalTokenCount,
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = meta.PromptTokenCount + meta.CandidatesTokenCount
	}
	in := &InputTokenDetails{}
	out := &OutputTokenDetails{}
	for _, d := range meta.PromptTokensDetails {
		switch d.Modality {
		case "AUDIO":
			in.AudioTokens += d.TokenCount
			u.AudioTokens += d.TokenCount
		case "IMAGE":
			in.ImageTokens += d.TokenCount
		}
	}
	for _, d := range meta.CandidatesTokensDetails {
		switch d.Modality {
		case "AUDIO":
			out.AudioTokens += d.TokenCount
		case "IMAGE":
			out.ImageTokens += d.TokenCount
		}
	}
	u.PromptTokensDetails = in
	u.CompletionTokensDetails = out
	return u
}

// ClaudeRequestToOpenAIRequest converts a Claude Messages request into an
// OpenAI-compatible chat completions request.
func ClaudeRequestToOpenAIRequest(req *ClaudeRequest) *GeneralOpenAIRequest {
	if req == nil {
		return &GeneralOpenAIRequest{}
	}
	out := &GeneralOpenAIRequest{
		Model:     req.Model,
		Stream:    req.Stream,
		MaxTokens: &req.MaxTokens,
		Temperature: req.Temperature,
		TopP:       req.TopP,
		Stop:       req.StopSequences,
		User:       req.Metadata.UserId,
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
		out.Messages = append(out.Messages, claudeMessageToOpenAI(m))
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

func claudeMessageToOpenAI(m ClaudeMessage) Message {
	switch content := m.Content.(type) {
	case string:
		return Message{Role: m.Role, Content: content}
	case []any:
		var texts []MediaContent
		var toolCalls []ToolCallRequest
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
		return msg
	default:
		return Message{Role: m.Role, Content: content}
	}
}

func claudeToolToOpenAI(t Tool) ToolCallRequest {
	return ToolCallRequest{
		Type:     "function",
		Function: &FunctionRequest{Name: t.Name, Description: t.Description, Parameters: t.InputSchema.Properties},
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
				Id:       strOr(part.Source),
				Type:     "function",
				Function: &FunctionResponse{Name: part.Model, Arguments: ToJSONString(part.Usage)},
			})
		}
	}
	msg := ChatResponseMessage{
		Role:    "assistant",
		Content: content.String(),
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
		msg.Content = nil
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
		out.Messages = append(out.Messages, geminiContentToOpenAI(c))
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

func geminiContentToOpenAI(c GeminiChatContent) Message {
	var texts []MediaContent
	var toolCalls []ToolCallRequest
	var funcResponse *FunctionRequest
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
				Id:       "",
				Type:     "function",
				Function: &FunctionRequest{Name: p.FunctionCall.Name, Arguments: ToJSONString(p.FunctionCall.Args)},
			})
		}
		if p.FunctionResponse != nil {
			funcResponse = &FunctionRequest{Name: p.FunctionResponse.Name, Arguments: ToJSONString(p.FunctionResponse.Response)}
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
	if funcResponse != nil {
		msg.FunctionCall = funcResponse
		msg.ToolCallId = ""
	}
	return msg
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
						Type:     "function",
						Function: &FunctionResponse{Name: p.FunctionCall.Name, Arguments: ToJSONString(p.FunctionCall.Args)},
					})
				}
			}
		}
		msg := ChatResponseMessage{Role: "assistant", Content: content}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
			msg.Content = nil
		}
		out.Choices = append(out.Choices, ChatCompletionsChoice{
			Index:        0,
			Message:      &msg,
			FinishReason: mapGeminiFinishReason(cand.FinishReason),
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
		Model:        req.Model,
		Stream:       req.Stream,
		Temperature:  req.Temperature,
		TopP:         req.TopP,
		MaxTokens:    maxTokensFromRequest(req),
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
		schema := &InputSchema{Type: "object"}
		if t.Function.Parameters != nil {
			schema.Properties = t.Function.Parameters
		}
		out.Tools = append(out.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
	}
	if req.ToolChoice != nil {
		out.ToolChoice = req.ToolChoice
	}
	return out
}

func openAIMessageToClaude(m Message) ClaudeMessage {
	cm := ClaudeMessage{Role: m.Role}
	switch content := m.Content.(type) {
	case string:
		cm.Content = content
	case []any:
		var parts []ClaudeMediaMessage
		for _, raw := range content {
			part, ok := raw.(MediaContent)
			if !ok {
				if pm, ok := raw.(map[string]any); ok {
					part = MediaContent{Type: strOr(pm["type"]), Text: strOr(pm["text"])}
				} else {
					continue
				}
			}
			switch part.Type {
			case ContentTypeText:
				parts = append(parts, ClaudeMediaMessage{Type: "text", Text: part.Text})
			case ContentTypeImageURL:
				if part.ImageURL != nil {
					src := &ClaudeMessageSource{Type: "url", Url: part.ImageURL.Url}
					if strings.HasPrefix(part.ImageURL.Url, "data:") {
						src = dataURLToSource(part.ImageURL.Url)
					}
					parts = append(parts, ClaudeMediaMessage{Type: "image", Source: src})
				}
			}
		}
		// Collapse single text part to a string for Claude compatibility.
		if len(parts) == 1 && parts[0].Type == "text" {
			cm.Content = parts[0].Text
		} else {
			anyParts := make([]any, len(parts))
			for i := range parts {
				anyParts[i] = parts[i]
			}
			cm.Content = anyParts
		}
	}
	return cm
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
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxOutputTokens: intPtr(maxTokensFromRequest(req)),
		StopSequences: stopToStrings(req.Stop),
	}
	out.GenerationConfig = genCfg
	for _, m := range req.Messages {
		if m.Role == "system" {
			out.SystemInstruction = &GeminiChatContent{Role: "system", Parts: []GeminiPart{{Text: contentToText(m.Content)}}}
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		content := GeminiChatContent{Role: role}
		content.Parts = openAIContentToGeminiParts(m.Content)
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
