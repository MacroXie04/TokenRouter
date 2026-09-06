package advancedcustom

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

func responsesStreamOrResponseToChat(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if meta.IsStream {
		return responsesStreamToChat(c, response, meta)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read advanced custom Responses response: %w", err)
	}
	var upstream protocolkit.OpenAIResponsesResponse
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode advanced custom Responses response: %w", err)
	}
	if upstream.Error != nil {
		return nil, relaycommon.UpstreamErrorFromOpenAI(*upstream.Error, http.StatusBadGateway)
	}
	converted := protocolkit.ResponsesResponseToOpenAIChat(&upstream)
	encoded, err := protocolkit.MarshalJSON(converted)
	if err != nil {
		return nil, fmt.Errorf("encode converted chat response: %w", err)
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(encoded); err != nil {
		return converted.Usage, fmt.Errorf("write converted chat response: %w", err)
	}
	return converted.Usage, nil
}

func chatStreamOrResponseToResponses(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if meta.IsStream {
		return chatStreamToResponses(c, response, meta)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read advanced custom chat response: %w", err)
	}
	var upstream protocolkit.ChatCompletionsResponse
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode advanced custom chat response: %w", err)
	}
	if upstream.Error != nil {
		return nil, relaycommon.UpstreamErrorFromOpenAI(*upstream.Error, http.StatusBadGateway)
	}
	converted := protocolkit.OpenAIChatResponseToResponses(&upstream)
	encoded, err := protocolkit.MarshalJSON(converted)
	if err != nil {
		return nil, fmt.Errorf("encode converted Responses response: %w", err)
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(encoded); err != nil {
		return upstream.Usage, fmt.Errorf("write converted Responses response: %w", err)
	}
	return upstream.Usage, nil
}

func geminiStreamOrResponseToResponses(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if meta.IsStream {
		return geminiStreamToResponses(c, response, meta)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read advanced custom Gemini response: %w", err)
	}
	var geminiResponse protocolkit.GeminiChatResponse
	if err := protocolkit.UnmarshalJSON(body, &geminiResponse); err != nil {
		return nil, fmt.Errorf("decode advanced custom Gemini response: %w", err)
	}
	if geminiResponse.Error != nil {
		return nil, fmt.Errorf("Gemini upstream error %s", geminiResponse.Error.Status)
	}
	chat := protocolkit.GeminiResponseToOpenAIResponse(&geminiResponse)
	converted := protocolkit.OpenAIChatResponseToResponses(chat)
	encoded, err := protocolkit.MarshalJSON(converted)
	if err != nil {
		return nil, fmt.Errorf("encode Gemini-converted Responses response: %w", err)
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(encoded); err != nil {
		return chat.Usage, fmt.Errorf("write Gemini-converted Responses response: %w", err)
	}
	return chat.Usage, nil
}

func responsesStreamToChat(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	prepareSSE(c, response.StatusCode)
	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	var usage *protocolkit.Usage
	id, model := "", meta.ModelName
	contentBytes := 0
	roleSent, terminalSent, toolSeen := false, false, false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := protocolkit.UnmarshalJSON([]byte(data), &event); err != nil {
			return usage, fmt.Errorf("decode advanced custom Responses stream event: %w", err)
		}
		eventType, _ := event["type"].(string)
		if rawError := event["error"]; rawError != nil {
			return usage, errors.New("advanced custom Responses stream returned an error event")
		}
		if rawResponse, ok := event["response"].(map[string]any); ok {
			if value, ok := rawResponse["id"].(string); ok {
				id = value
			}
			if value, ok := rawResponse["model"].(string); ok {
				model = value
			}
			if rawUsage := rawResponse["usage"]; rawUsage != nil {
				var converted protocolkit.ResponsesUsage
				if encoded, err := protocolkit.MarshalJSON(rawUsage); err == nil && protocolkit.UnmarshalJSON(encoded, &converted) == nil {
					usage = protocolkit.ResponsesUsageToOpenAI(&converted)
				}
			}
		}
		if eventType == "response.created" && !roleSent {
			if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"}, nil, nil); err != nil {
				return usage, err
			}
			roleSent = true
			continue
		}
		switch eventType {
		case "response.output_text.delta":
			delta, _ := event["delta"].(string)
			contentBytes += len(delta)
			if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: delta}, nil, nil); err != nil {
				return usage, err
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			delta, _ := event["delta"].(string)
			contentBytes += len(delta)
			if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: delta}, nil, nil); err != nil {
				return usage, err
			}
		case "response.output_item.added":
			item, _ := event["item"].(map[string]any)
			if item != nil && item["type"] == "function_call" {
				toolSeen = true
				index := protocolkit.IntFromAny(event["output_index"])
				call := protocolkit.ToolCallResponse{Index: index, Id: stringFromMap(item, "call_id"), Type: "function", Function: &protocolkit.FunctionResponse{Name: stringFromMap(item, "name")}}
				if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{call}}, nil, nil); err != nil {
					return usage, err
				}
			}
		case "response.function_call_arguments.delta":
			toolSeen = true
			index := protocolkit.IntFromAny(event["output_index"])
			call := protocolkit.ToolCallResponse{Index: index, Id: stringFromMap(event, "item_id"), Type: "function", Function: &protocolkit.FunctionResponse{Arguments: stringFromMap(event, "delta")}}
			if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{call}}, nil, nil); err != nil {
				return usage, err
			}
		case "response.completed":
			finish := "stop"
			if toolSeen {
				finish = "tool_calls"
			}
			if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &finish, usage); err != nil {
				return usage, err
			}
			terminalSent = true
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan advanced custom Responses stream: %w", err)
	}
	if usage == nil && contentBytes > 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
	}
	if !terminalSent {
		finish := "stop"
		if toolSeen {
			finish = "tool_calls"
		}
		if err := writeChatStreamChunk(c, id, model, protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &finish, usage); err != nil {
			return usage, err
		}
	}
	_, err := c.Writer.WriteString("data: [DONE]\n\n")
	c.Writer.Flush()
	return usage, err
}

func chatStreamToResponses(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	prepareSSE(c, response.StatusCode)
	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	id, model := "", meta.ModelName
	var usage *protocolkit.Usage
	var text, reasoning strings.Builder
	started := false
	toolStarted := make(map[int]bool)
	toolCalls := make(map[int]protocolkit.ToolCallResponse)

	ensureStarted := func() error {
		if started {
			return nil
		}
		started = true
		if err := writeResponsesEvent(c, "response.created", map[string]any{"response": responsesSkeleton(id, model, "in_progress", nil)}); err != nil {
			return err
		}
		return writeResponsesEvent(c, "response.output_item.added", map[string]any{
			"output_index": 0,
			"item":         map[string]any{"type": "message", "id": id + "_msg", "status": "in_progress", "role": "assistant", "content": []any{}},
		})
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk protocolkit.ChatCompletionsStreamResponse
		if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err != nil {
			return usage, fmt.Errorf("decode advanced custom chat stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return usage, relaycommon.UpstreamErrorFromOpenAI(*chunk.Error, http.StatusBadGateway)
		}
		if chunk.Id != "" {
			id = chunk.Id
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if err := ensureStarted(); err != nil {
			return usage, err
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if err := writeResponsesEvent(c, "response.output_text.delta", map[string]any{
					"delta": choice.Delta.Content, "output_index": 0, "content_index": 0, "item_id": id + "_msg",
				}); err != nil {
					return usage, err
				}
			}
			reasoningDelta := choice.Delta.ReasoningContent
			if reasoningDelta == "" {
				reasoningDelta = choice.Delta.Reasoning
			}
			if reasoningDelta != "" {
				reasoning.WriteString(reasoningDelta)
				if err := writeResponsesEvent(c, "response.reasoning_summary_text.delta", map[string]any{"delta": reasoningDelta, "output_index": 1}); err != nil {
					return usage, err
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				index := call.Index
				current := toolCalls[index]
				if call.Id != "" {
					current.Id = call.Id
				}
				current.Index = index
				current.Type = "function"
				if current.Function == nil {
					current.Function = &protocolkit.FunctionResponse{}
				}
				if call.Function != nil {
					if call.Function.Name != "" {
						current.Function.Name = call.Function.Name
					}
					current.Function.Arguments += call.Function.Arguments
				}
				toolCalls[index] = current
				if !toolStarted[index] {
					toolStarted[index] = true
					if err := writeResponsesEvent(c, "response.output_item.added", map[string]any{
						"output_index": index + 1,
						"item":         map[string]any{"type": "function_call", "id": current.Id, "call_id": current.Id, "name": current.Function.Name, "arguments": "", "status": "in_progress"},
					}); err != nil {
						return usage, err
					}
				}
				if call.Function != nil && call.Function.Arguments != "" {
					if err := writeResponsesEvent(c, "response.function_call_arguments.delta", map[string]any{
						"delta": call.Function.Arguments, "output_index": index + 1, "item_id": current.Id,
					}); err != nil {
						return usage, err
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan advanced custom chat stream: %w", err)
	}
	if err := ensureStarted(); err != nil {
		return usage, err
	}
	if usage == nil && text.Len()+reasoning.Len() > 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, text.Len()+reasoning.Len())
	}
	outputs := []any{map[string]any{
		"type": "message", "id": id + "_msg", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
	}}
	toolIndexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		toolIndexes = append(toolIndexes, index)
	}
	sort.Ints(toolIndexes)
	for _, index := range toolIndexes {
		call := toolCalls[index]
		if call.Function == nil {
			continue
		}
		outputs = append(outputs, map[string]any{
			"type": "function_call", "id": call.Id, "call_id": call.Id, "name": call.Function.Name,
			"arguments": call.Function.Arguments, "status": "completed",
		})
	}
	completed := responsesSkeleton(id, model, "completed", usage)
	completed["output"] = outputs
	if err := writeResponsesEvent(c, "response.completed", map[string]any{"response": completed}); err != nil {
		return usage, err
	}
	return usage, nil
}

func geminiStreamToResponses(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	prepareSSE(c, response.StatusCode)
	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	id, model := "advanced-gemini", meta.ModelName
	var usage *protocolkit.Usage
	started := false
	var text strings.Builder
	toolIndex := 0
	outputs := make([]any, 0)

	ensureStarted := func() error {
		if started {
			return nil
		}
		started = true
		if err := writeResponsesEvent(c, "response.created", map[string]any{"response": responsesSkeleton(id, model, "in_progress", nil)}); err != nil {
			return err
		}
		return writeResponsesEvent(c, "response.output_item.added", map[string]any{
			"output_index": 0, "item": map[string]any{"type": "message", "id": id + "_msg", "status": "in_progress", "role": "assistant", "content": []any{}},
		})
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk protocolkit.GeminiChatResponse
		if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err != nil {
			return usage, fmt.Errorf("decode advanced custom Gemini stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return usage, fmt.Errorf("Gemini upstream stream error %s", chunk.Error.Status)
		}
		if chunk.UsageMetadata != nil {
			usage = protocolkit.GeminiUsageToOpenAIUsage(chunk.UsageMetadata)
		}
		if err := ensureStarted(); err != nil {
			return usage, err
		}
		for _, candidate := range chunk.Candidates {
			if candidate.Content == nil {
				continue
			}
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					text.WriteString(part.Text)
					eventType := "response.output_text.delta"
					if part.Thought {
						eventType = "response.reasoning_summary_text.delta"
					}
					if err := writeResponsesEvent(c, eventType, map[string]any{"delta": part.Text, "output_index": 0, "content_index": 0, "item_id": id + "_msg"}); err != nil {
						return usage, err
					}
				}
				if part.FunctionCall != nil {
					arguments, _ := protocolkit.MarshalJSON(part.FunctionCall.Args)
					callID := fmt.Sprintf("%s_call_%d", id, toolIndex)
					item := map[string]any{"type": "function_call", "id": callID, "call_id": callID, "name": part.FunctionCall.Name, "arguments": string(arguments), "status": "completed"}
					outputs = append(outputs, item)
					if err := writeResponsesEvent(c, "response.output_item.added", map[string]any{"output_index": toolIndex + 1, "item": item}); err != nil {
						return usage, err
					}
					toolIndex++
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan advanced custom Gemini stream: %w", err)
	}
	if err := ensureStarted(); err != nil {
		return usage, err
	}
	if usage == nil && text.Len() > 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, text.Len())
	}
	message := map[string]any{
		"type": "message", "id": id + "_msg", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
	}
	allOutputs := append([]any{message}, outputs...)
	completed := responsesSkeleton(id, model, "completed", usage)
	completed["output"] = allOutputs
	if err := writeResponsesEvent(c, "response.completed", map[string]any{"response": completed}); err != nil {
		return usage, err
	}
	return usage, nil
}

func prepareSSE(c *gin.Context, status int) {
	c.Status(status)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()
}

func writeChatStreamChunk(c *gin.Context, id, model string, delta protocolkit.ChatCompletionsStreamResponseChoiceDelta, finish *string, usage *protocolkit.Usage) error {
	chunk := protocolkit.ChatCompletionsStreamResponse{
		Id: id, Object: "chat.completion.chunk", Model: model, Usage: usage,
		Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
	encoded, err := protocolkit.MarshalJSON(chunk)
	if err != nil {
		return err
	}
	if _, err := c.Writer.WriteString("data: " + string(encoded) + "\n\n"); err != nil {
		return fmt.Errorf("write converted chat stream: %w", err)
	}
	c.Writer.Flush()
	return nil
}

func writeResponsesEvent(c *gin.Context, eventType string, fields map[string]any) error {
	event := make(map[string]any, len(fields)+1)
	event["type"] = eventType
	for key, value := range fields {
		event[key] = value
	}
	encoded, err := protocolkit.MarshalJSON(event)
	if err != nil {
		return err
	}
	if _, err := c.Writer.WriteString("event: " + eventType + "\ndata: " + string(encoded) + "\n\n"); err != nil {
		return fmt.Errorf("write converted Responses stream: %w", err)
	}
	c.Writer.Flush()
	return nil
}

func responsesSkeleton(id, model, status string, usage *protocolkit.Usage) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "status": status, "model": model, "output": []any{},
		"usage": protocolkit.OpenAIUsageToResponses(usage),
	}
}

func stringFromMap(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
