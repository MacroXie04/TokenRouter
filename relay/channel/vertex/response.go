package vertex

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	maxResponseCandidates = 8
	maxResponseParts      = 4096
	maxResponseTextBytes  = 16 << 20
	maxStreamBytes        = 64 << 20
	maxStreamEvents       = 100_000
	maxProviderErrorBytes = 8 << 10
)

func (a *Adaptor) geminiNonStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return acceptedUsage(meta), fmt.Errorf("read Vertex AI response: %w", err)
	}
	var provider protocolkit.GeminiChatResponse
	if err := strictJSON(body, &provider, false); err != nil {
		return acceptedUsage(meta), errors.New("Vertex AI returned an invalid Gemini response")
	}
	if provider.Error != nil {
		return nil, geminiProviderError(provider.Error)
	}
	if err := relaycommon.ObserveGeminiResponse(meta.ToolHooks(), &provider); err != nil {
		return acceptedUsage(meta), fmt.Errorf("observe Vertex AI Gemini tool usage: %w", err)
	}
	usage, contentBytes, err := validateGeminiResponse(&provider, false, meta)
	if err != nil {
		return acceptedUsage(meta), err
	}
	if usage == nil {
		usage = relaycommon.EstimateStreamUsage(max(meta.PromptTokens, 1), contentBytes)
	}
	if isNativeGeminiRequest(meta) {
		c.Header("Content-Type", "application/json")
		c.Status(response.StatusCode)
		if _, err := c.Writer.Write(body); err != nil {
			return usage, fmt.Errorf("write Vertex AI native Gemini response: %w", err)
		}
		return usage, nil
	}
	output := protocolkit.GeminiResponseToOpenAIResponse(&provider)
	output.Model = clientModel(meta)
	output.Usage = usage
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return usage, errors.New("encode Vertex AI OpenAI response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(response.StatusCode)
	if _, err := c.Writer.Write(encoded); err != nil {
		return usage, fmt.Errorf("write Vertex AI OpenAI response: %w", err)
	}
	return usage, nil
}

func (a *Adaptor) claudeNonStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return acceptedUsage(meta), fmt.Errorf("read Vertex AI Claude response: %w", err)
	}
	var provider protocolkit.ClaudeResponse
	if err := strictJSON(body, &provider, false); err != nil {
		return acceptedUsage(meta), errors.New("Vertex AI returned an invalid Claude response")
	}
	if provider.Error != nil {
		message := strings.TrimSpace(provider.Error.Message)
		if message == "" || len(message) > maxProviderErrorBytes || !utf8.ValidString(message) {
			message = "Vertex AI Claude request failed"
		}
		return nil, relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
			Message: message, Type: provider.Error.Type,
		}, http.StatusBadRequest)
	}
	if err := relaycommon.ObserveClaudeResponse(meta.ToolHooks(), &provider); err != nil {
		return acceptedUsage(meta), fmt.Errorf("observe Vertex AI Claude tool usage: %w", err)
	}
	if len(provider.Content) == 0 || len(provider.Content) > maxResponseParts {
		return acceptedUsage(meta), errors.New("Vertex AI returned an invalid Claude content list")
	}
	contentBytes := 0
	for _, part := range provider.Content {
		encoded, err := protocolkit.MarshalJSON(part)
		if err != nil || len(encoded) > maxResponseTextBytes || contentBytes > maxResponseTextBytes-len(encoded) {
			return acceptedUsage(meta), errors.New("Vertex AI returned oversized Claude content")
		}
		contentBytes += len(encoded)
	}
	usage := protocolkit.ClaudeUsageToOpenAIUsage(provider.Usage)
	if provider.Usage == nil {
		usage = relaycommon.EstimateStreamUsage(max(meta.PromptTokens, 1), contentBytes)
	}
	if err := validateUsage(usage); err != nil {
		return acceptedUsage(meta), err
	}
	if isNativeClaudeRequest(meta) {
		c.Header("Content-Type", "application/json")
		c.Status(response.StatusCode)
		if _, err := c.Writer.Write(body); err != nil {
			return usage, fmt.Errorf("write Vertex AI native Claude response: %w", err)
		}
		return usage, nil
	}
	output := protocolkit.ClaudeResponseToOpenAIResponse(&provider)
	output.Model = clientModel(meta)
	output.Usage = usage
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return usage, errors.New("encode Vertex AI Claude response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(response.StatusCode)
	if _, err := c.Writer.Write(encoded); err != nil {
		return usage, fmt.Errorf("write Vertex AI Claude response: %w", err)
	}
	return usage, nil
}

type claudeStreamEvent struct {
	Type         string                          `json:"type"`
	Index        int                             `json:"index,omitempty"`
	Message      *claudeStreamMessage            `json:"message,omitempty"`
	ContentBlock *protocolkit.ClaudeMediaMessage `json:"content_block,omitempty"`
	Delta        *claudeStreamDelta              `json:"delta,omitempty"`
	Usage        *protocolkit.ClaudeUsage        `json:"usage,omitempty"`
	Error        *protocolkit.ClaudeError        `json:"error,omitempty"`
}

type claudeStreamMessage struct {
	Usage *protocolkit.ClaudeUsage `json:"usage,omitempty"`
}

type claudeStreamDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

func observeVertexClaudeStreamToolUsage(hooks *relaycommon.ToolUsageHooks, event *claudeStreamEvent) error {
	if hooks == nil || event == nil {
		return nil
	}
	if event.Type == "content_block_start" && event.ContentBlock != nil &&
		event.ContentBlock.Type == "tool_use" && hooks.ObserveClaudeToolUse != nil {
		blockIndex := event.Index
		if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
			BlockIndex: &blockIndex, ID: event.ContentBlock.ID, Name: event.ContentBlock.Name,
		}); err != nil {
			return err
		}
	}
	if event.Type == "message_start" && event.Message != nil {
		return relaycommon.ObserveClaudeUsage(hooks, event.Message.Usage)
	}
	if event.Type == "message_delta" {
		return relaycommon.ObserveClaudeUsage(hooks, event.Usage)
	}
	return nil
}

func (a *Adaptor) claudeNativeStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(response.StatusCode)

	var claudeUsage *protocolkit.ClaudeUsage
	var usage *protocolkit.Usage
	totalBytes := 0
	contentBytes := 0
	events := 0
	pendingBytes := 0
	pendingLines := make([]string, 0, 2)
	sawMessage := false
	wroteEvent := false
	justWroteData := false

	refreshUsage := func(candidate *protocolkit.ClaudeUsage) error {
		if candidate == nil {
			return nil
		}
		converted := protocolkit.ClaudeUsageToOpenAIUsage(candidate)
		if err := validateUsage(converted); err != nil {
			return err
		}
		claudeUsage = candidate
		usage = converted
		return nil
	}
	mergeUsage := func(candidate *protocolkit.ClaudeUsage) error {
		if candidate == nil {
			return nil
		}
		merged := *candidate
		if claudeUsage != nil {
			merged.InputTokens = claudeUsage.InputTokens
			merged.CacheCreationInputTokens = claudeUsage.CacheCreationInputTokens
			merged.CacheReadInputTokens = claudeUsage.CacheReadInputTokens
			merged.CacheCreation = claudeUsage.CacheCreation
			merged.ServerToolUse = claudeUsage.ServerToolUse
		}
		return refreshUsage(&merged)
	}
	writePending := func(dataLine string) error {
		for _, pending := range pendingLines {
			if _, err := c.Writer.WriteString(pending + "\n"); err != nil {
				return fmt.Errorf("write Vertex AI native Claude stream: %w", err)
			}
		}
		pendingLines = pendingLines[:0]
		pendingBytes = 0
		if dataLine != "" {
			if _, err := c.Writer.WriteString(dataLine + "\n"); err != nil {
				return fmt.Errorf("write Vertex AI native Claude stream: %w", err)
			}
		}
		if _, err := c.Writer.WriteString("\n"); err != nil {
			return fmt.Errorf("write Vertex AI native Claude stream: %w", err)
		}
		c.Writer.Flush()
		wroteEvent = true
		return nil
	}

	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) >= maxStreamBytes || totalBytes > maxStreamBytes-len(line)-1 {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI native Claude stream exceeds the total response limit")
		}
		totalBytes += len(line) + 1
		if line == "" {
			if justWroteData {
				justWroteData = false
				continue
			}
			if len(pendingLines) > 0 {
				if err := writePending(""); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			justWroteData = false
			if len(pendingLines) >= 64 || pendingBytes > relaycommon.MaxUpstreamSSEEventBytes-len(line)-1 {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI native Claude stream metadata is too large")
			}
			pendingLines = append(pendingLines, line)
			pendingBytes += len(line) + 1
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if err := writePending(line); err != nil {
				return acceptedStreamUsage(meta, usage, contentBytes), err
			}
			justWroteData = true
			continue
		}
		events++
		if events > maxStreamEvents {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI native Claude stream contains too many events")
		}
		var event claudeStreamEvent
		if err := strictJSON([]byte(data), &event, false); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an invalid native Claude stream event")
		}
		if err := observeVertexClaudeStreamToolUsage(meta.ToolHooks(), &event); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("observe Vertex AI native Claude tool usage: %w", err)
		}

		deltaBytes := 0
		switch event.Type {
		case "ping", "content_block_stop", "message_stop":
		case "message_start":
			if event.Message == nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude message_start is missing its message")
			}
			if err := refreshUsage(event.Message.Usage); err != nil {
				return acceptedUsage(meta), err
			}
			sawMessage = true
		case "content_block_start":
			if event.ContentBlock == nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude content_block_start is missing content")
			}
			encoded, err := protocolkit.MarshalJSON(event.ContentBlock)
			if err != nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude content block is invalid")
			}
			deltaBytes = len(encoded)
		case "content_block_delta":
			if event.Delta == nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude content delta is missing")
			}
			switch event.Delta.Type {
			case "text_delta":
				deltaBytes = len(event.Delta.Text)
			case "thinking_delta":
				deltaBytes = len(event.Delta.Thinking)
			case "input_json_delta":
				deltaBytes = len(event.Delta.PartialJSON)
			case "signature_delta":
				deltaBytes = len(event.Delta.Signature)
			default:
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude returned an unsupported content delta")
			}
		case "message_delta":
			if err := mergeUsage(event.Usage); err != nil {
				return acceptedUsage(meta), err
			}
		case "error":
			providerErr := claudeEventError(event.Error)
			if !wroteEvent && !sawMessage && usage == nil && contentBytes == 0 {
				return nil, providerErr
			}
			if err := writePending(line); err != nil {
				return acceptedStreamUsage(meta, usage, contentBytes), err
			}
			return acceptedStreamUsage(meta, usage, contentBytes), providerErr
		default:
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an unsupported native Claude stream event")
		}
		if deltaBytes > maxResponseTextBytes || contentBytes > maxResponseTextBytes-deltaBytes {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI native Claude stream content is too large")
		}
		contentBytes += deltaBytes
		if err := writePending(line); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), err
		}
		justWroteData = true
	}
	if err := scanner.Err(); err != nil {
		return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("read Vertex AI native Claude event stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if len(pendingLines) > 0 {
		return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI native Claude stream ended with an incomplete event")
	}
	if !sawMessage {
		return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an empty native Claude event stream")
	}
	if usage == nil {
		usage = relaycommon.EstimateStreamUsage(max(meta.PromptTokens, 1), contentBytes)
	}
	if err := validateUsage(usage); err != nil {
		return acceptedUsage(meta), err
	}
	return usage, nil
}

func claudeEventError(provider *protocolkit.ClaudeError) error {
	message := "Vertex AI Claude request failed"
	errorType := "vertex_error"
	if provider != nil {
		if safe := safeProviderText(provider.Message); safe != "" {
			message = safe
		}
		if safe := safeProviderText(provider.Type); safe != "" {
			errorType = safe
		}
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: errorType,
	}, http.StatusBadGateway)
}

func (a *Adaptor) claudeStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(response.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	var claudeUsage *protocolkit.ClaudeUsage
	var usage *protocolkit.Usage
	totalBytes := 0
	contentBytes := 0
	events := 0
	sawData := false
	sawAcceptedEvent := false
	finishSent := false
	doneSent := false

	writeChunk := func(delta protocolkit.ChatCompletionsStreamResponseChoiceDelta, finishReason *string, chunkUsage *protocolkit.Usage) error {
		chunk := protocolkit.ChatCompletionsStreamResponse{
			Object: "chat.completion.chunk", Model: clientModel(meta), Usage: chunkUsage,
		}
		hasDelta := delta.Content != "" || delta.ReasoningContent != "" || delta.Reasoning != "" ||
			delta.Role != "" || len(delta.ToolCalls) > 0 || delta.FunctionCall != nil
		if hasDelta || finishReason != nil {
			chunk.Choices = []protocolkit.ChatCompletionsStreamResponseChoice{{
				Index: 0, Delta: delta, FinishReason: finishReason,
			}}
		} else {
			chunk.Choices = []protocolkit.ChatCompletionsStreamResponseChoice{}
		}
		body, err := protocolkit.MarshalJSON(chunk)
		if err != nil {
			return errors.New("encode Vertex AI Claude stream chunk")
		}
		if _, err := c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
			return fmt.Errorf("write Vertex AI Claude stream chunk: %w", err)
		}
		c.Writer.Flush()
		return nil
	}

	refreshUsage := func(candidate *protocolkit.ClaudeUsage) error {
		if candidate == nil {
			return nil
		}
		converted := protocolkit.ClaudeUsageToOpenAIUsage(candidate)
		if err := validateUsage(converted); err != nil {
			return err
		}
		claudeUsage = candidate
		usage = converted
		return nil
	}

	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) >= maxStreamBytes || totalBytes > maxStreamBytes-len(line)-1 {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude stream exceeds the total response limit")
		}
		totalBytes += len(line) + 1
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if !doneSent {
				if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("write Vertex AI Claude stream terminator: %w", err)
				}
				c.Writer.Flush()
				doneSent = true
			}
			continue
		}
		events++
		if events > maxStreamEvents {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude stream contains too many events")
		}
		sawData = true
		var event claudeStreamEvent
		if err := strictJSON([]byte(data), &event, false); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an invalid Claude stream event")
		}
		if err := observeVertexClaudeStreamToolUsage(meta.ToolHooks(), &event); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("observe Vertex AI Claude tool usage: %w", err)
		}
		switch event.Type {
		case "ping", "content_block_stop":
			continue
		case "error":
			message := "Vertex AI Claude request failed"
			errorType := "vertex_error"
			if event.Error != nil {
				if safe := safeProviderText(event.Error.Message); safe != "" {
					message = safe
				}
				if safe := safeProviderText(event.Error.Type); safe != "" {
					errorType = safe
				}
			}
			providerErr := relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
				Message: message, Type: errorType,
			}, http.StatusBadGateway)
			if sawAcceptedEvent || usage != nil || contentBytes > 0 {
				return acceptedStreamUsage(meta, usage, contentBytes), providerErr
			}
			return nil, providerErr
		case "message_start":
			if event.Message == nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude message_start is missing its message")
			}
			if err := refreshUsage(event.Message.Usage); err != nil {
				return acceptedUsage(meta), err
			}
			if err := writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"}, nil, nil); err != nil {
				return acceptedStreamUsage(meta, usage, contentBytes), err
			}
			sawAcceptedEvent = true
		case "content_block_start":
			if event.ContentBlock == nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude content_block_start is missing content")
			}
			sawAcceptedEvent = true
			switch event.ContentBlock.Type {
			case "tool_use":
				arguments := ""
				if event.ContentBlock.Input != nil {
					arguments = protocolkit.ToJSONString(event.ContentBlock.Input)
					if arguments == "{}" {
						arguments = ""
					}
				}
				tool := protocolkit.ToolCallResponse{
					Index: event.Index, Id: event.ContentBlock.ID, Type: "function",
					Function: &protocolkit.FunctionResponse{Name: event.ContentBlock.Name, Arguments: arguments},
				}
				if err := writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{tool}}, nil, nil); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
			case "text", "thinking":
				// Payload arrives in content_block_delta events.
			default:
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude returned an unsupported content block")
			}
		case "content_block_delta":
			if event.Delta == nil {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude content delta is missing")
			}
			sawAcceptedEvent = true
			var delta protocolkit.ChatCompletionsStreamResponseChoiceDelta
			deltaBytes := 0
			switch event.Delta.Type {
			case "text_delta":
				delta.Content = event.Delta.Text
				deltaBytes = len(event.Delta.Text)
			case "thinking_delta":
				delta.ReasoningContent = event.Delta.Thinking
				deltaBytes = len(event.Delta.Thinking)
			case "input_json_delta":
				deltaBytes = len(event.Delta.PartialJSON)
				delta.ToolCalls = []protocolkit.ToolCallResponse{{
					Index: event.Index, Function: &protocolkit.FunctionResponse{Arguments: event.Delta.PartialJSON},
				}}
			case "signature_delta":
				deltaBytes = len(event.Delta.Signature)
			default:
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude returned an unsupported content delta")
			}
			if deltaBytes > maxResponseTextBytes || contentBytes > maxResponseTextBytes-deltaBytes {
				return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI Claude stream content is too large")
			}
			contentBytes += deltaBytes
			if event.Delta.Type != "signature_delta" {
				if err := writeChunk(delta, nil, nil); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
			}
		case "message_delta":
			sawAcceptedEvent = true
			if event.Usage != nil {
				candidate := *event.Usage
				if claudeUsage != nil {
					candidate.InputTokens = claudeUsage.InputTokens
					candidate.CacheCreationInputTokens = claudeUsage.CacheCreationInputTokens
					candidate.CacheReadInputTokens = claudeUsage.CacheReadInputTokens
					candidate.CacheCreation = claudeUsage.CacheCreation
					candidate.ServerToolUse = claudeUsage.ServerToolUse
				}
				if err := refreshUsage(&candidate); err != nil {
					return acceptedUsage(meta), err
				}
			}
			if event.Delta != nil && strings.TrimSpace(event.Delta.StopReason) != "" && !finishSent {
				reason := mapClaudeFinishReason(event.Delta.StopReason)
				if err := writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason, nil); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
				finishSent = true
			}
		case "message_stop":
			sawAcceptedEvent = true
			if !finishSent {
				reason := "stop"
				if err := writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason, nil); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
				finishSent = true
			}
		default:
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an unsupported Claude stream event")
		}
	}
	if err := scanner.Err(); err != nil {
		return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("read Vertex AI Claude event stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if !sawData || !sawAcceptedEvent {
		return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an empty Claude event stream")
	}
	if usage == nil {
		usage = relaycommon.EstimateStreamUsage(max(meta.PromptTokens, 1), contentBytes)
	}
	if err := validateUsage(usage); err != nil {
		return acceptedUsage(meta), err
	}
	if !finishSent {
		reason := "stop"
		if err := writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason, nil); err != nil {
			return usage, err
		}
	}
	if err := writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, nil, usage); err != nil {
		return usage, err
	}
	if !doneSent {
		if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
			return usage, fmt.Errorf("write Vertex AI Claude stream terminator: %w", err)
		}
		c.Writer.Flush()
	}
	return usage, nil
}

func (a *Adaptor) geminiStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	native := isNativeGeminiRequest(meta)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	var usage *protocolkit.Usage
	totalBytes := 0
	contentBytes := 0
	events := 0
	roleSent := false
	finishSent := false
	doneSent := false
	sawData := false
	sawCandidate := false

	writeOpenAIChunk := func(delta protocolkit.ChatCompletionsStreamResponseChoiceDelta, finishReason *string, chunkUsage *protocolkit.Usage) error {
		chunk := protocolkit.ChatCompletionsStreamResponse{
			Object: "chat.completion.chunk", Model: clientModel(meta), Usage: chunkUsage,
		}
		hasDelta := delta.Content != "" || delta.ReasoningContent != "" || delta.Reasoning != "" ||
			delta.Role != "" || len(delta.ToolCalls) > 0 || delta.FunctionCall != nil
		if hasDelta || finishReason != nil {
			chunk.Choices = []protocolkit.ChatCompletionsStreamResponseChoice{{
				Index: 0, Delta: delta, FinishReason: finishReason,
			}}
		} else {
			chunk.Choices = []protocolkit.ChatCompletionsStreamResponseChoice{}
		}
		body, err := protocolkit.MarshalJSON(chunk)
		if err != nil {
			return errors.New("encode Vertex AI stream chunk")
		}
		if _, err := c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
			return fmt.Errorf("write Vertex AI stream chunk: %w", err)
		}
		c.Writer.Flush()
		return nil
	}

	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if totalBytes > maxStreamBytes-len(line)-1 {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI stream exceeds the total response limit")
		}
		totalBytes += len(line) + 1
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if native {
				if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
				c.Writer.Flush()
				doneSent = true
			}
			continue
		}
		events++
		if events > maxStreamEvents {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI stream contains too many events")
		}
		sawData = true
		var provider protocolkit.GeminiChatResponse
		if err := strictJSON([]byte(data), &provider, false); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an invalid Gemini stream event")
		}
		if err := relaycommon.ObserveGeminiResponse(meta.ToolHooks(), &provider); err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("observe Vertex AI Gemini tool usage: %w", err)
		}
		if provider.Error != nil {
			providerErr := geminiProviderError(provider.Error)
			if usage != nil || sawCandidate {
				return acceptedStreamUsage(meta, usage, contentBytes), providerErr
			}
			return nil, providerErr
		}
		eventUsage, eventContentBytes, err := validateGeminiResponse(&provider, true, meta)
		if err != nil {
			return acceptedStreamUsage(meta, usage, contentBytes), err
		}
		if eventUsage != nil {
			usage = eventUsage
		}
		if contentBytes > maxResponseTextBytes-eventContentBytes {
			return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI stream content is too large")
		}
		contentBytes += eventContentBytes
		if native {
			if _, err := c.Writer.WriteString("data: " + data + "\n\n"); err != nil {
				return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("write Vertex AI native stream: %w", err)
			}
			c.Writer.Flush()
			if len(provider.Candidates) > 0 {
				sawCandidate = true
			}
			continue
		}
		if len(provider.Candidates) == 0 {
			continue
		}
		sawCandidate = true
		candidate := provider.Candidates[0]
		if candidate.Content != nil {
			if !roleSent {
				if err := writeOpenAIChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"}, nil, nil); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
				roleSent = true
			}
			for index, part := range candidate.Content.Parts {
				if part.Text != "" {
					delta := protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: part.Text}
					if part.Thought {
						delta.Content = ""
						delta.ReasoningContent = part.Text
					}
					if err := writeOpenAIChunk(delta, nil, nil); err != nil {
						return acceptedStreamUsage(meta, usage, contentBytes), err
					}
				}
				if part.FunctionCall != nil {
					arguments := protocolkit.ToJSONString(part.FunctionCall.Args)
					tool := protocolkit.ToolCallResponse{
						Index: index, Id: geminiToolCallID(part.FunctionCall.Name), Type: "function",
						Function: &protocolkit.FunctionResponse{Name: part.FunctionCall.Name, Arguments: arguments},
					}
					if err := writeOpenAIChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{tool}}, nil, nil); err != nil {
						return acceptedStreamUsage(meta, usage, contentBytes), err
					}
				}
			}
		}
		if !finishSent {
			if reason := mapGeminiFinishReason(candidate.FinishReason); reason != "" {
				if err := writeOpenAIChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason, nil); err != nil {
					return acceptedStreamUsage(meta, usage, contentBytes), err
				}
				finishSent = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return acceptedStreamUsage(meta, usage, contentBytes), fmt.Errorf("read Vertex AI event stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if !sawData {
		return acceptedStreamUsage(meta, usage, contentBytes), errors.New("Vertex AI returned an empty event stream")
	}
	if usage == nil {
		usage = relaycommon.EstimateStreamUsage(max(meta.PromptTokens, 1), contentBytes)
	}
	if err := validateUsage(usage); err != nil {
		return acceptedUsage(meta), err
	}
	if native {
		if !doneSent {
			// Gemini streams do not require a [DONE] sentinel; preserve that native
			// contract rather than inventing one.
			c.Writer.Flush()
		}
		return usage, nil
	}
	if !finishSent {
		reason := "stop"
		if err := writeOpenAIChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason, nil); err != nil {
			return usage, err
		}
	}
	if err := writeOpenAIChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, nil, usage); err != nil {
		return usage, err
	}
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write Vertex AI stream terminator: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}

func validateGeminiResponse(provider *protocolkit.GeminiChatResponse, stream bool, meta *relaycommon.Meta) (*protocolkit.Usage, int, error) {
	if provider == nil {
		return nil, 0, errors.New("Vertex AI returned an empty Gemini response")
	}
	if provider.PromptFeedback != nil && strings.TrimSpace(provider.PromptFeedback.BlockReason) != "" && len(provider.Candidates) == 0 {
		return nil, 0, fmt.Errorf("Vertex AI blocked the prompt: %s", safeProviderText(provider.PromptFeedback.BlockReason))
	}
	if len(provider.Candidates) > maxResponseCandidates || (!stream && len(provider.Candidates) == 0) {
		return nil, 0, errors.New("Vertex AI returned an invalid candidate count")
	}
	contentBytes := 0
	for _, candidate := range provider.Candidates {
		if candidate.Content == nil {
			if candidate.FinishReason == "" {
				return nil, 0, errors.New("Vertex AI returned a candidate without content or finish reason")
			}
			continue
		}
		if len(candidate.Content.Parts) > maxResponseParts {
			return nil, 0, errors.New("Vertex AI returned too many content parts")
		}
		for _, part := range candidate.Content.Parts {
			encoded, err := protocolkit.MarshalJSON(part)
			if err != nil || len(encoded) > maxResponseTextBytes || contentBytes > maxResponseTextBytes-len(encoded) {
				return nil, 0, errors.New("Vertex AI returned oversized or invalid content")
			}
			contentBytes += len(encoded)
		}
	}
	var usage *protocolkit.Usage
	if provider.UsageMetadata != nil {
		usage = protocolkit.GeminiUsageToOpenAIUsage(provider.UsageMetadata)
		if err := validateUsage(usage); err != nil {
			return nil, contentBytes, err
		}
	}
	_ = meta
	return usage, contentBytes, nil
}

func validateUsage(usage *protocolkit.Usage) error {
	if usage == nil {
		return nil
	}
	values := []int{
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.AudioTokens,
		usage.ReasoningTokens, usage.PromptCacheHitTokens, usage.PromptCacheMissTokens,
		usage.PromptCacheWriteTokens, usage.PromptCacheCreationTokens,
		usage.PromptCacheCreation5mTokens, usage.PromptCacheCreation1hTokens,
	}
	if usage.PromptTokensDetails != nil {
		values = append(values,
			usage.PromptTokensDetails.CachedTokens, usage.PromptTokensDetails.CachedCreationTokens,
			usage.PromptTokensDetails.CacheWriteTokens, usage.PromptTokensDetails.CacheCreation5mTokens,
			usage.PromptTokensDetails.CacheCreation1hTokens, usage.PromptTokensDetails.TextTokens,
			usage.PromptTokensDetails.AudioTokens, usage.PromptTokensDetails.ImageTokens,
			usage.PromptTokensDetails.ReasoningTokens)
	}
	if usage.CompletionTokensDetails != nil {
		values = append(values, usage.CompletionTokensDetails.TextTokens, usage.CompletionTokensDetails.AudioTokens,
			usage.CompletionTokensDetails.ImageTokens, usage.CompletionTokensDetails.ReasoningTokens)
	}
	for _, value := range values {
		if !appcommon.QuotaWithinBounds(value) {
			return errors.New("Vertex AI returned usage outside the supported accounting range")
		}
	}
	return nil
}

func acceptedUsage(meta *relaycommon.Meta) *protocolkit.Usage {
	prompt := 1
	if meta != nil && meta.PromptTokens > prompt && appcommon.QuotaWithinBounds(meta.PromptTokens) {
		prompt = meta.PromptTokens
	}
	return &protocolkit.Usage{PromptTokens: prompt, TotalTokens: prompt}
}

func acceptedStreamUsage(meta *relaycommon.Meta, usage *protocolkit.Usage, contentBytes int) *protocolkit.Usage {
	if usage != nil && validateUsage(usage) == nil {
		return usage
	}
	if contentBytes > 0 {
		promptTokens := 1
		if meta != nil {
			promptTokens = max(meta.PromptTokens, 1)
		}
		estimated := relaycommon.EstimateStreamUsage(promptTokens, contentBytes)
		if validateUsage(estimated) == nil {
			return estimated
		}
	}
	return acceptedUsage(meta)
}

func clientModel(meta *relaycommon.Meta) string {
	if meta != nil && strings.TrimSpace(meta.OriginalModelName) != "" {
		return meta.OriginalModelName
	}
	if meta != nil {
		return meta.ModelName
	}
	return ""
}

func geminiProviderError(provider *protocolkit.GeminiError) error {
	if provider == nil {
		return errors.New("Vertex AI request failed")
	}
	message := safeProviderText(provider.Message)
	if message == "" {
		message = "Vertex AI request failed"
	}
	status := provider.Code
	if status < 400 || status > 599 {
		status = http.StatusBadRequest
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: "vertex_error", Code: provider.Status,
	}, status)
}

func safeProviderText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxProviderErrorBytes || !utf8.ValidString(value) {
		return ""
	}
	return value
}

func mapGeminiFinishReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST":
		return "content_filter"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	default:
		return strings.ToLower(strings.TrimSpace(reason))
	}
}

func mapClaudeFinishReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return strings.TrimSpace(reason)
	}
}

func geminiToolCallID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "call_vertex"
	}
	var id strings.Builder
	id.WriteString("call_")
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-' {
			id.WriteRune(char)
		} else {
			id.WriteByte('_')
		}
	}
	return id.String()
}
