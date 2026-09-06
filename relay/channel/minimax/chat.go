package minimax

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type miniMaxBaseResponse struct {
	StatusCode int64  `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

func (a *Adaptor) chatResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read MiniMax chat response: %w", err)
	}
	var envelope struct {
		BaseResp miniMaxBaseResponse `json:"base_resp"`
	}
	if protocolkit.UnmarshalJSON(body, &envelope) == nil && envelope.BaseResp.StatusCode != 0 {
		return nil, miniMaxBusinessError(meta, http.StatusBadRequest, "minimax_chat_error",
			envelope.BaseResp.StatusCode, envelope.BaseResp.StatusMsg)
	}
	copyOfResponse := *resp
	copyOfResponse.Body = io.NopCloser(bytes.NewReader(body))
	return a.openAI.DoResponse(c, &copyOfResponse, meta)
}

func (a *Adaptor) chatStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	var streamedBytes int64
	var generatedBytes int64
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		streamedBytes += int64(len(line)) + 1
		if streamedBytes > maxMiniMaxStreamBodyBytes {
			return usage, fmt.Errorf("MiniMax stream exceeds %d bytes", maxMiniMaxStreamBodyBytes)
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" && data != "[DONE]" {
				var chunk protocolkit.ChatCompletionsStreamResponse
				if protocolkit.UnmarshalJSON([]byte(data), &chunk) == nil {
					if chunk.Error != nil {
						return usage, mapMiniMaxStatus(meta,
							relaycommon.UpstreamErrorFromOpenAI(*chunk.Error, http.StatusBadGateway))
					}
					if chunk.Usage != nil {
						usage = protocolkit.NormalizeOpenAIUsageAliases(chunk.Usage)
					}
					for _, choice := range chunk.Choices {
						generatedBytes += int64(miniMaxStreamChoiceBytes(choice))
					}
					if generatedBytes > maxMiniMaxStreamGeneratedBytes {
						return usage, fmt.Errorf("MiniMax generated stream content exceeds %d bytes", maxMiniMaxStreamGeneratedBytes)
					}
				}
			}
			if _, err := c.Writer.WriteString(line + "\n\n"); err != nil {
				return usage, fmt.Errorf("write MiniMax stream: %w", err)
			}
			c.Writer.Flush()
			continue
		}
		if line != "" {
			if _, err := c.Writer.WriteString(line + "\n"); err != nil {
				return usage, fmt.Errorf("write MiniMax stream: %w", err)
			}
			c.Writer.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan MiniMax stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if usage == nil && generatedBytes > 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, int(generatedBytes))
	}
	return usage, nil
}

func miniMaxStreamChoiceBytes(choice protocolkit.ChatCompletionsStreamResponseChoice) int {
	delta := choice.Delta
	size := len(delta.Content) + len(delta.ReasoningContent) + len(delta.Reasoning)
	for _, call := range delta.ToolCalls {
		size += len(call.Id) + len(call.Type)
		if call.Function != nil {
			size += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	if delta.FunctionCall != nil {
		size += len(delta.FunctionCall.Name) + len(delta.FunctionCall.Arguments)
	}
	return size
}
