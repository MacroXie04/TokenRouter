package protocolkit

import (
	"errors"
	"fmt"
	"strings"
)

// OpenAIChatRequestToResponsesMap converts the common Chat Completions fields
// into the Responses wire shape while preserving compatible extensions. The
// returned map is owned by the caller.
func OpenAIChatRequestToResponsesMap(request *GeneralOpenAIRequest, mappedModel string) (map[string]any, error) {
	if request == nil {
		return nil, errors.New("chat completions request is nil")
	}
	if request.N != nil && *request.N > 1 {
		return nil, errors.New("Responses conversion does not support n greater than 1")
	}
	body := cloneStringAnyMap(request.Extra)
	for _, key := range []string{"messages", "prompt", "prefix", "suffix", "n", "max_tokens", "max_completion_tokens", "functions", "function_call", "group"} {
		delete(body, key)
	}
	body["model"] = mappedModel
	body["stream"] = request.Stream
	if request.MaxCompletionTokens != nil {
		body["max_output_tokens"] = *request.MaxCompletionTokens
	} else if request.MaxTokens != nil {
		body["max_output_tokens"] = *request.MaxTokens
	}

	input := make([]any, 0, len(request.Messages))
	instructions := make([]string, 0, 2)
	for _, message := range request.Messages {
		switch message.Role {
		case "system", "developer":
			if text := contentText(message.Content); text != "" {
				instructions = append(instructions, text)
			}
			continue
		case "tool":
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": message.ToolCallId,
				"output": responseOutputValue(message.Content),
			})
			continue
		}

		content := chatContentToResponses(message.Content, message.Role)
		if content != nil || len(message.ToolCalls) == 0 {
			input = append(input, map[string]any{"role": message.Role, "content": content})
		}
		for _, toolCall := range message.ToolCalls {
			if toolCall.Function == nil {
				continue
			}
			input = append(input, map[string]any{
				"type": "function_call", "call_id": toolCall.Id, "name": toolCall.Function.Name,
				"arguments": toolCall.Function.Arguments,
			})
		}
	}
	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n\n")
	}
	if len(input) > 0 {
		body["input"] = input
	} else if request.Prompt != nil {
		body["input"] = request.Prompt
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Function == nil {
				continue
			}
			tools = append(tools, map[string]any{
				"type": "function", "name": tool.Function.Name,
				"description": tool.Function.Description, "parameters": tool.Function.Parameters,
			})
		}
		body["tools"] = tools
	}
	return body, nil
}

// ResponsesMapToOpenAIChatRequest converts a decoded Responses request into a
// Chat Completions request. Text, image, audio, file, and function-call items
// retain their semantic roles; unsupported extension fields remain in Extra.
func ResponsesMapToOpenAIChatRequest(source map[string]any, mappedModel string) (*GeneralOpenAIRequest, error) {
	if source == nil {
		return nil, errors.New("Responses request is nil")
	}
	request := &GeneralOpenAIRequest{Model: mappedModel, Extra: cloneStringAnyMap(source)}
	request.Extra["model"] = mappedModel
	delete(request.Extra, "input")
	delete(request.Extra, "instructions")
	delete(request.Extra, "max_output_tokens")
	delete(request.Extra, "group")

	if stream, ok := source["stream"].(bool); ok {
		request.Stream = stream
	}
	if value, ok := numberToInt(source["max_output_tokens"]); ok {
		request.MaxCompletionTokens = &value
		request.Extra["max_completion_tokens"] = value
	}
	if instructions := contentText(source["instructions"]); instructions != "" {
		request.Messages = append(request.Messages, Message{Role: "system", Content: instructions})
	}

	switch input := source["input"].(type) {
	case string:
		request.Messages = append(request.Messages, Message{Role: "user", Content: input})
	case []any:
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(item["type"]) {
			case "function_call":
				arguments := responseOutputValue(item["arguments"])
				call := ToolCallRequest{Id: stringValue(item["call_id"]), Type: "function", Function: &FunctionRequest{
					Name: stringValue(item["name"]), Arguments: arguments,
				}}
				if len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == "assistant" &&
					request.Messages[len(request.Messages)-1].Content == nil {
					request.Messages[len(request.Messages)-1].ToolCalls = append(request.Messages[len(request.Messages)-1].ToolCalls, call)
				} else {
					request.Messages = append(request.Messages, Message{Role: "assistant", Content: nil, ToolCalls: []ToolCallRequest{call}})
				}
			case "function_call_output":
				request.Messages = append(request.Messages, Message{
					Role: "tool", ToolCallId: stringValue(item["call_id"]), Content: responseOutputValue(item["output"]),
				})
			default:
				role := stringValue(item["role"])
				if role == "" {
					role = "user"
				}
				request.Messages = append(request.Messages, Message{Role: role, Content: responsesContentToChat(item["content"])})
			}
		}
	case nil:
	default:
		request.Messages = append(request.Messages, Message{Role: "user", Content: input})
	}

	if rawTools, ok := source["tools"].([]any); ok {
		for _, raw := range rawTools {
			tool, ok := raw.(map[string]any)
			if !ok || stringValue(tool["type"]) != "function" {
				continue
			}
			parameters, _ := tool["parameters"].(map[string]any)
			request.Tools = append(request.Tools, ToolCallRequest{Type: "function", Function: &FunctionRequest{
				Name: stringValue(tool["name"]), Description: stringValue(tool["description"]), Parameters: parameters,
			}})
		}
	}
	if len(request.Messages) == 0 {
		return nil, errors.New("Responses request contains no convertible input")
	}
	return request, nil
}

func cloneStringAnyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	if source == nil {
		return result
	}
	encoded, err := MarshalJSON(source)
	if err == nil && UnmarshalJSON(encoded, &result) == nil {
		return result
	}
	for key, value := range source {
		result[key] = value
	}
	return result
}

func genericValue(value any) any {
	encoded, err := MarshalJSON(value)
	if err != nil {
		return value
	}
	var result any
	if UnmarshalJSON(encoded, &result) != nil {
		return value
	}
	return result
}

func chatContentToResponses(content any, role string) any {
	generic := genericValue(content)
	parts, ok := generic.([]any)
	if !ok {
		return generic
	}
	converted := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			converted = append(converted, raw)
			continue
		}
		copyPart := cloneStringAnyMap(part)
		switch stringValue(copyPart["type"]) {
		case "text":
			if role == "assistant" {
				copyPart["type"] = "output_text"
			} else {
				copyPart["type"] = "input_text"
			}
		case "image_url":
			copyPart["type"] = "input_image"
			if image, ok := copyPart["image_url"].(map[string]any); ok {
				copyPart["image_url"] = image["url"]
			}
		case "input_audio":
			copyPart["type"] = "input_audio"
		case "file":
			copyPart["type"] = "input_file"
		}
		converted = append(converted, copyPart)
	}
	return converted
}

func responsesContentToChat(content any) any {
	parts, ok := genericValue(content).([]any)
	if !ok {
		return genericValue(content)
	}
	converted := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			converted = append(converted, raw)
			continue
		}
		copyPart := cloneStringAnyMap(part)
		switch stringValue(copyPart["type"]) {
		case "input_text", "output_text":
			copyPart["type"] = "text"
		case "input_image":
			copyPart["type"] = "image_url"
			if imageURL, ok := copyPart["image_url"].(string); ok {
				copyPart["image_url"] = map[string]any{"url": imageURL}
			}
		case "input_file":
			copyPart["type"] = "file"
		}
		converted = append(converted, copyPart)
	}
	return converted
}

func contentText(value any) string {
	switch typed := genericValue(value).(type) {
	case string:
		return typed
	case []any:
		var parts []string
		for _, raw := range typed {
			if item, ok := raw.(map[string]any); ok {
				if text := stringValue(item["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
	}
	return ""
}

func responseOutputValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := MarshalJSON(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func numberToInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), typed == float64(int(typed))
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	default:
		return 0, false
	}
}

// ResponsesResponseToOpenAIChat converts a completed Responses result to the
// Chat Completions response shape.
func ResponsesResponseToOpenAIChat(response *OpenAIResponsesResponse) *ChatCompletionsResponse {
	if response == nil {
		return &ChatCompletionsResponse{}
	}
	message := &ChatResponseMessage{Role: "assistant"}
	var text, reasoning strings.Builder
	for _, output := range response.Output {
		switch output.Type {
		case "message":
			for _, content := range output.Content {
				if content.Type == "output_text" || content.Type == "text" {
					text.WriteString(content.Text)
				}
			}
		case "reasoning":
			for _, content := range output.Content {
				reasoning.WriteString(content.Text)
			}
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, ToolCallResponse{
				Id: output.CallId, Type: "function", Function: &FunctionResponse{Name: output.Name, Arguments: output.Arguments},
			})
		}
	}
	message.Content = text.String()
	message.ReasoningContent = reasoning.String()
	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	return &ChatCompletionsResponse{
		Id: response.Id, Object: "chat.completion", Created: response.CreatedAt, Model: response.Model,
		Choices: []ChatCompletionsChoice{{Index: 0, Message: message, FinishReason: finishReason}},
		Usage:   ResponsesUsageToOpenAI(response.Usage), Error: response.Error,
	}
}

// OpenAIChatResponseToResponses converts a completed Chat Completions result
// into a completed Responses result.
func OpenAIChatResponseToResponses(response *ChatCompletionsResponse) *OpenAIResponsesResponse {
	if response == nil {
		return &OpenAIResponsesResponse{}
	}
	result := &OpenAIResponsesResponse{
		Id: response.Id, Object: "response", CreatedAt: response.Created, Status: "completed", Model: response.Model,
		Usage: OpenAIUsageToResponses(response.Usage), Error: response.Error,
	}
	for _, choice := range response.Choices {
		if choice.Message == nil {
			continue
		}
		message := choice.Message
		if text := contentText(message.Content); text != "" {
			result.Output = append(result.Output, ResponsesOutput{
				Type: "message", Id: response.Id + "_msg", Status: "completed", Role: "assistant",
				Content: []ResponsesOutputContent{{Type: "output_text", Text: text, Annotations: []any{}}},
			})
		}
		if message.ReasoningContent != "" || message.Reasoning != "" {
			text := message.ReasoningContent
			if text == "" {
				text = message.Reasoning
			}
			result.Output = append(result.Output, ResponsesOutput{
				Type: "reasoning", Id: response.Id + "_reasoning", Status: "completed",
				Content: []ResponsesOutputContent{{Type: "summary_text", Text: text}},
			})
		}
		for _, call := range message.ToolCalls {
			if call.Function == nil {
				continue
			}
			result.Output = append(result.Output, ResponsesOutput{
				Type: "function_call", Id: call.Id, CallId: call.Id, Status: "completed",
				Name: call.Function.Name, Arguments: call.Function.Arguments,
			})
		}
	}
	return result
}

func ResponsesUsageToOpenAI(usage *ResponsesUsage) *Usage {
	if usage == nil {
		return nil
	}
	result := &Usage{
		PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens,
	}
	if usage.InputTokensDetails != nil {
		result.PromptTokensDetails = &InputTokenDetails{
			CachedTokens: usage.InputTokensDetails.CachedTokens, CachedCreationTokens: usage.InputTokensDetails.CachedCreationTokens,
			CacheWriteTokens: usage.InputTokensDetails.CacheWriteTokens, CacheCreation5mTokens: usage.InputTokensDetails.CacheCreation5mTokens,
			CacheCreation1hTokens: usage.InputTokensDetails.CacheCreation1hTokens, TextTokens: usage.InputTokensDetails.TextTokens,
			AudioTokens: usage.InputTokensDetails.AudioTokens, ImageTokens: usage.InputTokensDetails.ImageTokens,
			ReasoningTokens: usage.InputTokensDetails.ReasoningTokens,
		}
	}
	if usage.OutputTokensDetails != nil {
		result.CompletionTokensDetails = &OutputTokenDetails{
			TextTokens: usage.OutputTokensDetails.TextTokens, AudioTokens: usage.OutputTokensDetails.AudioTokens,
			ImageTokens: usage.OutputTokensDetails.ImageTokens, ReasoningTokens: usage.OutputTokensDetails.ReasoningTokens,
		}
		result.ReasoningTokens = usage.OutputTokensDetails.ReasoningTokens
	}
	return result
}

func OpenAIUsageToResponses(usage *Usage) *ResponsesUsage {
	if usage == nil {
		return nil
	}
	copyOfUsage := *usage
	NormalizeOpenAIUsageAliases(&copyOfUsage)
	result := &ResponsesUsage{InputTokens: copyOfUsage.PromptTokens, OutputTokens: copyOfUsage.CompletionTokens, TotalTokens: copyOfUsage.TotalTokens}
	if copyOfUsage.PromptTokensDetails != nil {
		details := copyOfUsage.PromptTokensDetails
		result.InputTokensDetails = &ResponsesInputTokenDetails{
			CachedTokens: details.CachedTokens, CachedCreationTokens: details.CachedCreationTokens,
			CacheWriteTokens: details.CacheWriteTokens, CacheCreation5mTokens: details.CacheCreation5mTokens,
			CacheCreation1hTokens: details.CacheCreation1hTokens, TextTokens: details.TextTokens,
			AudioTokens: details.AudioTokens, ImageTokens: details.ImageTokens, ReasoningTokens: details.ReasoningTokens,
		}
	}
	if copyOfUsage.CompletionTokensDetails != nil {
		details := copyOfUsage.CompletionTokensDetails
		result.OutputTokensDetails = &ResponsesOutputTokenDetails{
			TextTokens: details.TextTokens, AudioTokens: details.AudioTokens, ImageTokens: details.ImageTokens,
			ReasoningTokens: details.ReasoningTokens,
		}
	}
	return result
}
