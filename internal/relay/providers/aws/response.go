package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func awsHTTPError(response *http.Response) error {
	body, err := relaycommon.ReadUpstreamBody(response.Body, MaxErrorBodyBytes)
	if err != nil {
		return &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: fmt.Errorf("read AWS error response: %w", err)}
	}
	message := "AWS Bedrock request failed"
	code := "http_" + fmt.Sprint(response.StatusCode)
	if len(bytes.TrimSpace(body)) > 0 && rejectDuplicateJSONKeys(body) == nil {
		var envelope struct {
			Message string `json:"message"`
			Type    string `json:"__type"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if validProviderMessage(envelope.Message) {
				message = envelope.Message
			}
			if envelope.Code != "" {
				code = safeProviderCode(envelope.Code)
			} else if envelope.Type != "" {
				code = safeProviderCode(envelope.Type)
			}
		}
	}
	encoded, marshalErr := json.Marshal(map[string]any{"error": map[string]any{
		"message": message, "type": "aws_bedrock_error", "code": code,
	}})
	if marshalErr != nil {
		return &relaycommon.UpstreamError{StatusCode: response.StatusCode, Cause: errors.New("encode AWS error response")}
	}
	return &relaycommon.UpstreamError{StatusCode: response.StatusCode, Body: string(encoded)}
}

func validProviderMessage(message string) bool {
	return message != "" && utf8.ValidString(message) && len(message) <= 4096 &&
		strings.IndexFunc(message, unicode.IsControl) < 0
}

func safeProviderCode(code string) string {
	code = strings.TrimSpace(code)
	if index := strings.LastIndexAny(code, "#:"); index >= 0 && index+1 < len(code) {
		code = code[index+1:]
	}
	if code == "" || len(code) > 128 {
		return "aws_bedrock_error"
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "aws_bedrock_error"
		}
	}
	return code
}

func (a *Adaptor) claudeResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, MaxResponseBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read AWS Claude response: %w", err)
	}
	var providerResponse protocolkit.ClaudeResponse
	if err := strictJSON(body, &providerResponse, false); err != nil {
		return nil, errors.New("AWS Bedrock returned an invalid Claude response")
	}
	if providerResponse.Error != nil {
		message := providerResponse.Error.Message
		if !validProviderMessage(message) {
			message = "AWS Bedrock returned a Claude error"
		}
		encoded, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": message, "type": "aws_bedrock_error", "code": safeProviderCode(providerResponse.Error.Type),
		}})
		return nil, &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Body: string(encoded)}
	}
	if err := validateClaudeResponse(&providerResponse); err != nil {
		return nil, err
	}
	usage := protocolkit.ClaudeUsageToOpenAIUsage(providerResponse.Usage)
	if meta.Format == channelcatalog.RelayFormatClaude {
		c.Status(response.StatusCode)
		c.Header("Content-Type", "application/json")
		if _, err := c.Writer.Write(body); err != nil {
			return usage, fmt.Errorf("write AWS Claude response: %w", err)
		}
		return usage, nil
	}
	synthetic := *response
	synthetic.Body = io.NopCloser(bytes.NewReader(body))
	return a.claude.DoResponse(c, &synthetic, meta)
}

func validateClaudeResponse(response *protocolkit.ClaudeResponse) error {
	if response == nil || response.Id == "" || len(response.Id) > 2048 || !utf8.ValidString(response.Id) ||
		strings.IndexFunc(response.Id, unicode.IsControl) >= 0 {
		return errors.New("AWS Claude response ID is invalid")
	}
	if response.Type != "message" || response.Role != "assistant" {
		return errors.New("AWS Claude response envelope is invalid")
	}
	if err := validateModelID(response.Model); err != nil {
		return errors.New("AWS Claude response model is invalid")
	}
	if len(response.Content) > MaxContentParts {
		return fmt.Errorf("AWS Claude response contains more than %d content parts", MaxContentParts)
	}
	for _, content := range response.Content {
		if int64(len(content.Text)) > MaxResponseBodyBytes || len(content.Name) > MaxJSONKeyBytes || len(content.ID) > 2048 {
			return errors.New("AWS Claude response content is outside the supported range")
		}
	}
	if len(response.StopReason) > 128 || len(response.StopSequence) > MaxStopSequenceBytes {
		return errors.New("AWS Claude response stop metadata is invalid")
	}
	if response.Usage == nil {
		return errors.New("AWS Claude response usage is missing")
	}
	return validateClaudeUsage(response.Usage)
}

func validateClaudeUsage(usage *protocolkit.ClaudeUsage) error {
	if usage == nil {
		return errors.New("AWS Claude usage is missing")
	}
	values := []int{
		usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens,
	}
	if usage.CacheCreation != nil {
		values = append(values, usage.CacheCreation.Ephemeral5mInputTokens, usage.CacheCreation.Ephemeral1hInputTokens)
	}
	if usage.ServerToolUse != nil {
		values = append(values, usage.ServerToolUse.WebSearchRequests)
	}
	for _, value := range values {
		if value < 0 || int64(value) > MaxUsageTokens {
			return errors.New("AWS Claude usage is outside the supported range")
		}
	}
	normalized := protocolkit.ClaudeUsageToOpenAIUsage(usage)
	if normalized.PromptTokens < 0 || normalized.CompletionTokens < 0 || normalized.TotalTokens < 0 ||
		int64(normalized.PromptTokens) > MaxUsageTokens || int64(normalized.CompletionTokens) > MaxUsageTokens ||
		int64(normalized.TotalTokens) > MaxUsageTokens {
		return errors.New("AWS Claude usage total is outside the supported range")
	}
	return nil
}

func (a *Adaptor) claudeStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	return a.claudeStreamResponseWithLimit(c, response, meta, MaxResponseBodyBytes)
}

func (a *Adaptor) claudeStreamResponseWithLimit(c *gin.Context, response *http.Response, meta *relaycommon.Meta, maxBytes int64) (*protocolkit.Usage, error) {
	if maxBytes <= 0 {
		return nil, errors.New("AWS event stream limit is invalid")
	}
	limited := &io.LimitedReader{R: response.Body, N: maxBytes + 1}
	synthetic := *response
	synthetic.StatusCode = http.StatusOK
	synthetic.Header = response.Header.Clone()
	synthetic.Header.Set("Content-Type", "text/event-stream")
	synthetic.Body = io.NopCloser(newEventStreamSSEReader(limited))
	var usage *protocolkit.Usage
	var err error
	if meta.Format == channelcatalog.RelayFormatClaude {
		usage, err = nativeClaudeStreamResponse(c, &synthetic, meta)
	} else {
		usage, err = a.claude.DoResponse(c, &synthetic, meta)
	}
	if limited.N == 0 {
		limitErr := fmt.Errorf("%w: AWS event stream maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, maxBytes)
		return usage, errors.Join(limitErr, err)
	}
	if err != nil {
		return usage, err
	}
	return usage, nil
}

func nativeClaudeStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(response.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var claudeUsage *protocolkit.ClaudeUsage
	contentChars := 0
	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := c.Writer.WriteString(line + "\n"); err != nil {
			return usageOrEstimate(claudeUsage, meta.PromptTokens, contentChars), fmt.Errorf("write AWS Claude stream: %w", err)
		}
		c.Writer.Flush()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			return usageOrEstimate(claudeUsage, meta.PromptTokens, contentChars), errors.New("AWS Claude stream event became invalid")
		}
		switch event["type"] {
		case "message_start":
			if message, ok := event["message"].(map[string]any); ok {
				claudeUsage = claudeUsageValue(message["usage"])
			}
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					contentChars += len(text)
				}
				if text, ok := delta["thinking"].(string); ok {
					contentChars += len(text)
				}
			}
		case "message_delta":
			if current := claudeUsageValue(event["usage"]); current != nil {
				if claudeUsage == nil {
					claudeUsage = current
				} else {
					claudeUsage.OutputTokens = current.OutputTokens
				}
			}
		}
	}
	usage := usageOrEstimate(claudeUsage, meta.PromptTokens, contentChars)
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read AWS event stream (maximum event %d bytes): %w", MaxEventPayloadBytes, err)
	}
	return usage, nil
}

func claudeUsageValue(value any) *protocolkit.ClaudeUsage {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var usage protocolkit.ClaudeUsage
	if json.Unmarshal(raw, &usage) != nil || validateClaudeUsage(&usage) != nil {
		return nil
	}
	return &usage
}

func usageOrEstimate(usage *protocolkit.ClaudeUsage, promptTokens, contentChars int) *protocolkit.Usage {
	if usage != nil {
		return protocolkit.ClaudeUsageToOpenAIUsage(usage)
	}
	return relaycommon.EstimateStreamUsage(promptTokens, contentChars)
}

type novaResponseEnvelope struct {
	Output struct {
		Message struct {
			Role    string        `json:"role"`
			Content []novaContent `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func (a *Adaptor) novaResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, MaxResponseBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read AWS Nova response: %w", err)
	}
	var provider novaResponseEnvelope
	if err := strictJSON(body, &provider, false); err != nil {
		return nil, errors.New("AWS Bedrock returned an invalid Nova response")
	}
	if provider.Output.Message.Role != "assistant" || len(provider.Output.Message.Content) == 0 ||
		len(provider.Output.Message.Content) > MaxContentParts || int64(len(provider.Output.Message.Content[0].Text)) > MaxResponseBodyBytes {
		return nil, errors.New("AWS Bedrock returned invalid Nova output")
	}
	usage := &protocolkit.Usage{
		PromptTokens: provider.Usage.InputTokens, CompletionTokens: provider.Usage.OutputTokens,
		TotalTokens: provider.Usage.TotalTokens,
	}
	if err := validateNovaUsage(usage); err != nil {
		return nil, err
	}
	created := time.Now().Unix()
	if a.Now != nil {
		created = a.Now().Unix()
	}
	identifier := strings.TrimSpace(response.Header.Get("x-amzn-requestid"))
	if identifier == "" || len(identifier) > 256 || strings.ContainsAny(identifier, "\r\n\x00") {
		identifier = cryptoutil.BestEffortRandomAlphanumeric(24)
	}
	finishReason := novaFinishReason(provider.StopReason)
	output := protocolkit.ChatCompletionsResponse{
		Id: "chatcmpl-" + identifier, Object: "chat.completion", Created: created, Model: meta.ModelName,
		Choices: []protocolkit.ChatCompletionsChoice{{
			Index: 0, Message: &protocolkit.ChatResponseMessage{Role: "assistant", Content: provider.Output.Message.Content[0].Text},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, errors.New("encode AWS Nova response")
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(encoded); err != nil {
		return usage, fmt.Errorf("write AWS Nova response: %w", err)
	}
	return usage, nil
}

func validateNovaUsage(usage *protocolkit.Usage) error {
	if usage == nil || usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 ||
		int64(usage.PromptTokens) > MaxUsageTokens || int64(usage.CompletionTokens) > MaxUsageTokens ||
		int64(usage.TotalTokens) > MaxUsageTokens {
		return errors.New("AWS Nova usage is outside the supported range")
	}
	if usage.PromptTokens > int(MaxUsageTokens)-usage.CompletionTokens ||
		usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		return errors.New("AWS Nova usage total is inconsistent")
	}
	return nil
}

func novaFinishReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return safeProviderCode(reason)
	}
}
