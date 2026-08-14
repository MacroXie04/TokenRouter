// Package claude implements the Anthropic Messages adapter.
package claude

import (
	"bufio"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

// Adaptor is the Anthropic adapter.
type Adaptor struct {
	Mode constant.RelayMode
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
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	claudeReq := protocolkit.OpenAIRequestToClaudeRequest(meta.Request)
	return protocolkit.MarshalJSON(claudeReq)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if meta.IsStream {
		return a.streamResponse(c, resp, meta)
	}
	return a.nonStreamResponse(c, resp)
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var claudeResp protocolkit.ClaudeResponse
	if err := protocolkit.UnmarshalJSON(body, &claudeResp); err != nil {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	openaiResp := protocolkit.ClaudeResponseToOpenAIResponse(&claudeResp)
	out, _ := protocolkit.MarshalJSON(openaiResp)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(out)
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

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	sendChunk := func(delta map[string]any) {
		chunk := protocolkit.ChatCompletionsStreamResponse{
			Object:  "chat.completion.chunk",
			Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
				Index: 0,
				Delta: protocolkit.ChatCompletionsStreamResponseChoiceDelta{
					Content: strOr(delta["content"]),
					Role:    strOr(delta["role"]),
				},
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
			if u := event["message"]; u != nil {
				sendChunk(map[string]any{"role": "assistant"})
			}
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
				text := strOr(delta["text"])
				contentChars += len(text)
				sendChunk(map[string]any{"content": text})
			}
		case "content_block_start":
			// Ignore non-text blocks (tool_use) in this pass-through mode.
		case "message_delta":
			if u, ok := event["usage"].(map[string]any); ok {
				claudeUsage = &protocolkit.ClaudeUsage{
					OutputTokens: intFromAny(u["output_tokens"]),
				}
			}
		case "message_stop":
			_, _ = c.Writer.WriteString("data: [DONE]\n\n")
			c.Writer.Flush()
		}
	}

	if claudeUsage == nil {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentChars)
	} else {
		usage = protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage)
	}
	return usage, nil
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
