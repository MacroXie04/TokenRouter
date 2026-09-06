// Package claude implements the Anthropic Messages adapter.
package claude

import (
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"strings"
)

// Adaptor is the Anthropic adapter.
type Adaptor struct {
	Mode channelcatalog.RelayMode
}

func (a *Adaptor) Init(meta *relaycommon.Meta) { a.Mode = meta.Mode }

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	base := meta.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	return relaycommon.JoinURL(base, "/v1/messages"), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", meta.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	setting.ApplyClaudeModelHeaders(meta.OriginalModelName, req.Header)
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	claudeReq := protocolkit.OpenAIRequestToClaudeRequest(meta.Request)
	claudeReq.Model = relaycommon.PrepareClaudeRequest(claudeReq, meta.OriginalModelName, meta.ModelName,
		meta.Request != nil && (meta.Request.MaxTokens != nil || meta.Request.MaxCompletionTokens != nil))
	return protocolkit.MarshalJSON(claudeReq)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if meta.IsStream {
		return a.streamResponse(c, resp, meta)
	}
	return a.nonStreamResponse(c, resp, meta)
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Claude response: %w", err)
	}
	var claudeResp protocolkit.ClaudeResponse
	if err := protocolkit.UnmarshalJSON(body, &claudeResp); err != nil {
		return nil, fmt.Errorf("decode Claude response: %w", err)
	}
	if err := observeClaudeResponse(meta.ToolHooks(), &claudeResp); err != nil {
		return nil, fmt.Errorf("observe Claude tool usage: %w", err)
	}
	openaiResp := protocolkit.ClaudeResponseToOpenAIResponse(&claudeResp)
	out, _ := protocolkit.MarshalJSON(openaiResp)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return openaiResp.Usage, fmt.Errorf("write Claude response: %w", err)
	}
	return openaiResp.Usage, nil
}

func (a *Adaptor) streamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	var contentChars int
	var claudeUsage *protocolkit.ClaudeUsage

	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)

	finishSent := false
	sendChunk := func(delta protocolkit.ChatCompletionsStreamResponseChoiceDelta, finishReason *string) {
		chunk := protocolkit.ChatCompletionsStreamResponse{
			Object: "chat.completion.chunk",
			Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
				Index: 0, Delta: delta, FinishReason: finishReason,
			}},
		}
		b, _ := protocolkit.MarshalJSON(chunk)
		_, _ = c.Writer.WriteString("data: " + string(b) + "\n\n")
		c.Writer.Flush()
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var event map[string]any
		if err := protocolkit.UnmarshalJSON([]byte(data), &event); err != nil {
			continue
		}
		switch event["type"] {
		case "message_start":
			if message, ok := event["message"].(map[string]any); ok {
				if decoded := decodeClaudeUsage(message["usage"]); decoded != nil {
					if err := observeClaudeUsage(meta.ToolHooks(), decoded); err != nil {
						return usage, fmt.Errorf("observe Claude stream tool usage: %w", err)
					}
					claudeUsage = decoded
				}
				sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"}, nil)
			}
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				switch delta["type"] {
				case "text_delta":
					text := strOr(delta["text"])
					contentChars += len(text)
					sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: text}, nil)
				case "thinking_delta":
					text := strOr(delta["thinking"])
					contentChars += len(text)
					sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: text}, nil)
				case "input_json_delta":
					partial := strOr(delta["partial_json"])
					toolIndex := intFromAny(event["index"])
					sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{{
						Index: toolIndex, Function: &protocolkit.FunctionResponse{Arguments: partial},
					}}}, nil)
				}
			}
		case "content_block_start":
			if block, ok := event["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
				toolIndex := intFromAny(event["index"])
				if hooks := meta.ToolHooks(); hooks != nil && hooks.ObserveClaudeToolUse != nil {
					if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
						BlockIndex: &toolIndex, ID: strOr(block["id"]), Name: strOr(block["name"]),
					}); err != nil {
						return usage, fmt.Errorf("observe Claude stream tool use: %w", err)
					}
				}
				arguments := ""
				if input, exists := block["input"]; exists {
					arguments = protocolkit.ToJSONString(input)
					if arguments == "{}" {
						arguments = ""
					}
				}
				sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{{
					Index: toolIndex, Id: strOr(block["id"]), Type: "function",
					Function: &protocolkit.FunctionResponse{Name: strOr(block["name"]), Arguments: arguments},
				}}}, nil)
			}
		case "message_delta":
			if decoded := decodeClaudeUsage(event["usage"]); decoded != nil {
				if err := observeClaudeUsage(meta.ToolHooks(), decoded); err != nil {
					return usage, fmt.Errorf("observe Claude stream tool usage: %w", err)
				}
				if claudeUsage == nil {
					claudeUsage = decoded
				} else {
					claudeUsage.OutputTokens = decoded.OutputTokens
				}
			}
			if delta, ok := event["delta"].(map[string]any); ok {
				if reason := mapClaudeStreamStopReason(strOr(delta["stop_reason"])); reason != "" {
					sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason)
					finishSent = true
				}
			}
		case "message_stop":
			if !finishSent {
				reason := "stop"
				sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason)
				finishSent = true
			}
			_, _ = c.Writer.WriteString("data: [DONE]\n\n")
			c.Writer.Flush()
		}
	}

	if claudeUsage == nil {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentChars)
	} else {
		usage = protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage)
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read Claude event stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	return usage, nil
}

func observeClaudeResponse(hooks *relaycommon.ToolUsageHooks, response *protocolkit.ClaudeResponse) error {
	if hooks == nil || response == nil {
		return nil
	}
	for index := range response.Content {
		block := &response.Content[index]
		if block.Type != "tool_use" || hooks.ObserveClaudeToolUse == nil {
			continue
		}
		blockIndex := index
		if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
			BlockIndex: &blockIndex, ID: block.ID, Name: block.Name,
		}); err != nil {
			return err
		}
	}
	return observeClaudeUsage(hooks, response.Usage)
}

func observeClaudeUsage(hooks *relaycommon.ToolUsageHooks, usage *protocolkit.ClaudeUsage) error {
	if hooks == nil || hooks.SetClaudeWebSearchCount == nil || usage == nil || usage.ServerToolUse == nil {
		return nil
	}
	return hooks.SetClaudeWebSearchCount(usage.ServerToolUse.WebSearchRequests)
}

func mapClaudeStreamStopReason(reason string) string {
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

func decodeClaudeUsage(value any) *protocolkit.ClaudeUsage {
	if value == nil {
		return nil
	}
	raw, err := protocolkit.MarshalJSON(value)
	if err != nil {
		return nil
	}
	var usage protocolkit.ClaudeUsage
	if err := protocolkit.UnmarshalJSON(raw, &usage); err != nil {
		return nil
	}
	return &usage
}

func strOr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	}
	return 0
}
