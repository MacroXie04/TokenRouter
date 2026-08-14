package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// RelayClaudeMessages serves POST /v1/messages (Anthropic Messages format).
// The request is moderated, billed, and settled like any relay; dispatch is
// Claude-native passthrough for Anthropic channels or Claude↔OpenAI conversion
// for OpenAI-compatible channels.
func RelayClaudeMessages(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16*1024*1024))
	if err != nil {
		abortClaude(c, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}
	var claudeReq protocolkit.ClaudeRequest
	if err := protocolkit.UnmarshalJSON(body, &claudeReq); err != nil {
		abortClaude(c, http.StatusBadRequest, "invalid_request_error", "无效的 JSON 请求体")
		return
	}
	if claudeReq.Model == "" {
		abortClaude(c, http.StatusBadRequest, "invalid_request_error", "缺少 model 字段")
		return
	}

	// The converted OpenAI request drives moderation, token estimation, and
	// billing-expression inputs; the original Claude request is dispatched.
	openaiReq := protocolkit.ClaudeRequestToOpenAIRequest(&claudeReq)
	openaiReq.Stream = claudeReq.Stream

	info := &RelayInfo{
		Mode:          constant.RelayModeChatCompletions,
		Format:        constant.RelayFormatClaude,
		Request:       openaiReq,
		ClaudeRequest: &claudeReq,
		RawBody:       body,
		ModelName:     claudeReq.Model,
		Group:         getRelayGroup(c),
		IsStream:      claudeReq.Stream,
	}
	if err := relayAndSettleWithDispatch(c, info, dispatchClaudeUpstream); err != nil {
		return
	}
}

func abortClaude(c *gin.Context, status int, typ, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": typ, "message": message}})
}

// dispatchClaudeUpstream sends a Claude-format request to the channel: native
// passthrough for Anthropic channels, OpenAI conversion otherwise.
func dispatchClaudeUpstream(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	if info.Channel == nil || info.ClaudeRequest == nil {
		return nil, errors.New("channel not selected")
	}
	base := info.Channel.BaseURL
	apiKey := service.GetChannelKey(info.Channel)
	if info.Channel.Type == int(constant.ChannelTypeAnthropic) {
		return sendClaudeNative(c, info, base, apiKey)
	}
	return sendClaudeViaOpenAI(c, info, base, apiKey)
}

// sendClaudeNative posts the Claude request to the Anthropic Messages API and
// passes the response through unchanged (SSE events for streams).
func sendClaudeNative(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	url := relaycommon.JoinURL(base, "/v1/messages")
	body, err := protocolkit.MarshalJSON(info.ClaudeRequest)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	service.ApplyChannelAffinityRequestHeaders(c, req)
	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if !info.IsStream {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var claudeResp protocolkit.ClaudeResponse
		if err := protocolkit.UnmarshalJSON(raw, &claudeResp); err != nil {
			return nil, errors.New("上游返回无效响应")
		}
		c.Status(resp.StatusCode)
		c.Header("Content-Type", "application/json")
		_, _ = c.Writer.Write(raw)
		if claudeResp.Usage != nil {
			return protocolkit.ClaudeUsageToOpenAIUsage(claudeResp.Usage), nil
		}
		return &protocolkit.Usage{}, nil
	}

	// Stream passthrough: forward events and extract usage from message_delta.
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()
	var claudeUsage *protocolkit.ClaudeUsage
	var contentChars int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = c.Writer.WriteString(line + "\n")
		c.Writer.Flush()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := protocolkit.UnmarshalJSON([]byte(data), &event); err != nil {
			continue
		}
		switch event["type"] {
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
				if text, ok := delta["text"].(string); ok {
					contentChars += len(text)
				}
			}
		case "message_delta":
			if u, ok := event["usage"].(map[string]any); ok {
				claudeUsage = &protocolkit.ClaudeUsage{OutputTokens: anyToInt(u["output_tokens"])}
			}
		}
	}
	if claudeUsage != nil {
		return protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage), nil
	}
	return relaycommon.EstimateStreamUsage(info.PromptTokens, contentChars), nil
}

// sendClaudeViaOpenAI converts the Claude request to OpenAI format, calls the
// channel's chat-completions endpoint, and converts the response (including
// SSE chunks) back into Claude Messages events.
func sendClaudeViaOpenAI(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	openaiReq := protocolkit.ClaudeRequestToOpenAIRequest(info.ClaudeRequest)
	openaiReq.Stream = info.IsStream
	meta := &relaycommon.Meta{
		Channel:      info.Channel,
		Mode:         constant.RelayModeChatCompletions,
		Format:       constant.RelayFormatOpenAI,
		ModelName:    relaycommon.GetMappedModel(info.Channel, info.ModelName),
		BaseURL:      base,
		APIKey:       apiKey,
		Request:      openaiReq,
		IsStream:     info.IsStream,
		PromptTokens: info.PromptTokens,
	}
	adaptor := GetAdaptor(constant.ChannelType(info.Channel.Type))
	adaptor.Init(meta)
	url, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(req, meta); err != nil {
		return nil, err
	}
	service.ApplyChannelAffinityRequestHeaders(c, req)
	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if !info.IsStream {
		return claudeNonStreamFromOpenAI(c, resp)
	}
	return claudeStreamFromOpenAI(c, resp, info)
}

// claudeNonStreamFromOpenAI converts a full OpenAI response to Claude format.
func claudeNonStreamFromOpenAI(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var openaiResp protocolkit.ChatCompletionsResponse
	if err := protocolkit.UnmarshalJSON(raw, &openaiResp); err != nil {
		return nil, errors.New("上游返回无效响应")
	}
	claudeResp := protocolkit.OpenAIResponseToClaudeResponse(&openaiResp)
	out, _ := protocolkit.MarshalJSON(claudeResp)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(out)
	return openaiResp.Usage, nil
}

// claudeStreamFromOpenAI converts OpenAI SSE chunks into Claude Messages SSE
// events (message_start → content_block_start → deltas → stop → message_delta
// → message_stop) and extracts usage.
func claudeStreamFromOpenAI(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	writeEvent := func(typ string, payload any) {
		b, _ := protocolkit.MarshalJSON(payload)
		_, _ = c.Writer.WriteString("event: " + typ + "\n")
		_, _ = c.Writer.WriteString("data: " + string(b) + "\n\n")
		c.Writer.Flush()
	}

	msgID := "msg_" + common.RandomAlphanumeric(24)
	writeEvent("message_start", gin.H{
		"type": "message_start",
		"message": gin.H{"id": msgID, "type": "message", "role": "assistant",
			"content": []any{}, "model": info.ModelName, "stop_reason": nil, "stop_sequence": nil,
			"usage": gin.H{"input_tokens": info.PromptTokens, "output_tokens": 1}},
	})
	writeEvent("content_block_start", gin.H{
		"type": "content_block_start", "index": 0,
		"content_block": gin.H{"type": "text", "text": ""},
	})

	var usage *protocolkit.Usage
	var openaiUsage *protocolkit.Usage
	var contentChars int
	var finishReason string
	blockStopped := false

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
			continue
		}
		if chunk.Usage != nil {
			openaiUsage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				contentChars += len(choice.Delta.Content)
				writeEvent("content_block_delta", gin.H{
					"type": "content_block_delta", "index": 0,
					"delta": gin.H{"type": "text_delta", "text": choice.Delta.Content},
				})
			}
			if choice.FinishReason != nil && !blockStopped {
				blockStopped = true
				finishReason = *choice.FinishReason
			}
		}
	}

	if !blockStopped {
		finishReason = "stop"
	}
	writeEvent("content_block_stop", gin.H{"type": "content_block_stop", "index": 0})
	outputTokens := 0
	if openaiUsage != nil {
		outputTokens = openaiUsage.CompletionTokens
	}
	writeEvent("message_delta", gin.H{
		"type": "message_delta",
		"delta": gin.H{
			"stop_reason":   protocolkit.OpenAIFinishReasonToClaudeStopReason(finishReason),
			"stop_sequence": nil,
		},
		"usage": gin.H{"output_tokens": outputTokens},
	})
	writeEvent("message_stop", gin.H{"type": "message_stop"})

	if openaiUsage != nil {
		usage = openaiUsage
	} else {
		usage = relaycommon.EstimateStreamUsage(info.PromptTokens, contentChars)
	}
	return usage, nil
}

func anyToInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}
