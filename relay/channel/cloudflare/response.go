package cloudflare

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func (a *Adaptor) doResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, errors.New("Cloudflare response metadata is nil")
	}
	if err := validateContract(a.mode, a.relayFormat(meta), meta.IsStream); err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return a.openAI.DoResponse(c, resp, meta)
	}
	switch a.mode {
	case constant.RelayModeChatCompletions:
		if meta.IsStream {
			return a.chatStreamResponse(c, resp, meta)
		}
		return a.chatResponse(c, resp, meta)
	case constant.RelayModeCompletions:
		if meta.IsStream {
			return a.completionStreamResponse(c, resp, meta)
		}
		return a.completionResponse(c, resp, meta)
	case constant.RelayModeEmbeddings:
		return a.embeddingResponse(c, resp, meta)
	case constant.RelayModeResponses:
		return a.openAI.DoResponse(c, resp, meta)
	case constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
		return a.audioResponse(c, resp, meta)
	default:
		return nil, fmt.Errorf("Cloudflare channel does not support relay mode %d", a.mode)
	}
}

func (a *Adaptor) chatResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Cloudflare chat response: %w", err)
	}
	var response protocolkit.ChatCompletionsResponse
	if err := protocolkit.UnmarshalJSON(body, &response); err != nil {
		return nil, fmt.Errorf("decode Cloudflare chat response: %w", err)
	}
	if response.Error != nil {
		return a.openAIError(c, meta, http.StatusBadGateway, *response.Error)
	}
	completionText := strings.Builder{}
	for _, choice := range response.Choices {
		if choice.Message != nil {
			completionText.WriteString(contentText(choice.Message.Content))
		}
	}
	usage := estimatedCloudflareUsage(meta.PromptTokens, completionText.String())
	response.Id = newCloudflareResponseID("chatcmpl-")
	response.Model = meta.ModelName
	response.Usage = usage
	out, err := protocolkit.MarshalJSON(response)
	if err != nil {
		return nil, fmt.Errorf("encode Cloudflare chat response: %w", err)
	}
	if err := writeCloudflareJSON(c, resp.StatusCode, out); err != nil {
		return usage, err
	}
	return usage, nil
}

func (a *Adaptor) embeddingResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Cloudflare embedding response: %w", err)
	}
	var envelope map[string]any
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil || envelope == nil {
		return nil, errors.New("decode Cloudflare embedding response: expected a JSON object")
	}
	if value, exists := envelope["error"]; exists && value != nil {
		openAIError := decodeOpenAIError(value)
		return a.openAIError(c, meta, http.StatusBadGateway, openAIError)
	}
	usage := relaycommon.ExtractUsageFromBody(body)
	if usage == nil || usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		usage = &protocolkit.Usage{PromptTokens: meta.PromptTokens, TotalTokens: meta.PromptTokens}
	} else if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens > 0 {
		usage.PromptTokens = usage.TotalTokens
	}
	envelope["model"] = meta.ModelName
	envelope["usage"] = usage
	out, err := protocolkit.MarshalJSON(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode Cloudflare embedding response: %w", err)
	}
	if err := writeCloudflareJSON(c, resp.StatusCode, out); err != nil {
		return usage, err
	}
	return usage, nil
}

func (a *Adaptor) completionResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Cloudflare completion response: %w", err)
	}
	text, upstreamError, err := cloudflareResultText(body, "response")
	if err != nil {
		return nil, err
	}
	if upstreamError != nil {
		return a.openAIError(c, meta, http.StatusBadGateway, *upstreamError)
	}
	usage := estimatedCloudflareUsage(meta.PromptTokens, text)
	response := protocolkit.OpenAITextResponse{
		Id: newCloudflareResponseID("cmpl-"), Model: meta.ModelName, Object: "text_completion", Created: time.Now().Unix(),
		Choices: []protocolkit.OpenAITextResponseChoice{{Index: 0, Text: text, FinishReason: "stop"}}, Usage: usage,
	}
	out, err := protocolkit.MarshalJSON(response)
	if err != nil {
		return nil, fmt.Errorf("encode Cloudflare completion response: %w", err)
	}
	if err := writeCloudflareJSON(c, resp.StatusCode, out); err != nil {
		return usage, err
	}
	return usage, nil
}

func (a *Adaptor) audioResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Cloudflare audio response: %w", err)
	}
	text, upstreamError, err := cloudflareResultText(body, "text")
	if err != nil {
		return nil, err
	}
	if upstreamError != nil {
		return a.openAIError(c, meta, http.StatusBadGateway, *upstreamError)
	}
	usage := estimatedCloudflareUsage(meta.PromptTokens, text)
	out, err := protocolkit.MarshalJSON(map[string]any{"text": text})
	if err != nil {
		return nil, fmt.Errorf("encode Cloudflare audio response: %w", err)
	}
	if err := writeCloudflareJSON(c, resp.StatusCode, out); err != nil {
		return usage, err
	}
	return usage, nil
}

func (a *Adaptor) chatStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	setCloudflareStreamHeaders(c, resp.StatusCode)
	id := newCloudflareResponseID("chatcmpl-")
	var completionText strings.Builder
	wrote := false
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		data, ok, done := cloudflareSSEData(scanner.Text())
		if done {
			break
		}
		if !ok {
			continue
		}
		var chunk protocolkit.ChatCompletionsStreamResponse
		if err := protocolkit.UnmarshalJSON(data, &chunk); err != nil {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("decode Cloudflare chat stream: %w", err)
		}
		if chunk.Error != nil {
			_, mappedErr := a.openAIError(c, meta, http.StatusBadGateway, *chunk.Error)
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), mappedErr
		}
		for index := range chunk.Choices {
			chunk.Choices[index].Delta.Role = "assistant"
			completionText.WriteString(chunk.Choices[index].Delta.Content)
			if completionText.Len() > int(relaycommon.MaxUpstreamJSONBodyBytes) {
				return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("Cloudflare stream text exceeds %d bytes", relaycommon.MaxUpstreamJSONBodyBytes)
			}
		}
		chunk.Id = id
		chunk.Model = meta.ModelName
		encoded, err := protocolkit.MarshalJSON(chunk)
		if err != nil {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("encode Cloudflare chat stream: %w", err)
		}
		if err := writeCloudflareSSE(c, encoded); err != nil {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), err
		}
		wrote = true
	}
	if err := scanner.Err(); err != nil {
		return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("scan Cloudflare stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	usage := estimatedCloudflareUsage(meta.PromptTokens, completionText.String())
	if cloudflareIncludeUsage(meta) {
		terminal := protocolkit.ChatCompletionsStreamResponse{
			Id: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: meta.ModelName,
			Choices: []protocolkit.ChatCompletionsStreamResponseChoice{}, Usage: usage,
		}
		encoded, err := protocolkit.MarshalJSON(terminal)
		if err != nil {
			return usage, fmt.Errorf("encode Cloudflare terminal usage: %w", err)
		}
		if err := writeCloudflareSSE(c, encoded); err != nil {
			return usage, err
		}
	}
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write Cloudflare stream terminator: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}

func (a *Adaptor) completionStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	setCloudflareStreamHeaders(c, resp.StatusCode)
	id := newCloudflareResponseID("cmpl-")
	var completionText strings.Builder
	wrote := false
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		data, ok, done := cloudflareSSEData(scanner.Text())
		if done {
			break
		}
		if !ok {
			continue
		}
		text, upstreamError, err := cloudflareResultText(data, "response")
		if err != nil {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("decode Cloudflare completion stream: %w", err)
		}
		if upstreamError != nil {
			_, mappedErr := a.openAIError(c, meta, http.StatusBadGateway, *upstreamError)
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), mappedErr
		}
		completionText.WriteString(text)
		if completionText.Len() > int(relaycommon.MaxUpstreamJSONBodyBytes) {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("Cloudflare stream text exceeds %d bytes", relaycommon.MaxUpstreamJSONBodyBytes)
		}
		chunk := protocolkit.CompletionsStreamResponse{
			Id: id, Object: "text_completion", Created: time.Now().Unix(), Model: meta.ModelName,
			Choices: []protocolkit.CompletionsStreamResponseChoice{{Index: 0, Text: text}},
		}
		encoded, err := protocolkit.MarshalJSON(chunk)
		if err != nil {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("encode Cloudflare completion stream: %w", err)
		}
		if err := writeCloudflareSSE(c, encoded); err != nil {
			return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), err
		}
		wrote = true
	}
	if err := scanner.Err(); err != nil {
		return partialCloudflareUsage(meta.PromptTokens, completionText.String(), wrote), fmt.Errorf("scan Cloudflare completion stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	usage := estimatedCloudflareUsage(meta.PromptTokens, completionText.String())
	finish := protocolkit.CompletionsStreamResponse{
		Id: id, Object: "text_completion", Created: time.Now().Unix(), Model: meta.ModelName,
		Choices: []protocolkit.CompletionsStreamResponseChoice{{Index: 0, FinishReason: "stop"}},
	}
	encoded, err := protocolkit.MarshalJSON(finish)
	if err != nil {
		return usage, fmt.Errorf("encode Cloudflare completion finish: %w", err)
	}
	if err := writeCloudflareSSE(c, encoded); err != nil {
		return usage, err
	}
	if cloudflareIncludeUsage(meta) {
		terminal := protocolkit.CompletionsStreamResponse{
			Id: id, Object: "text_completion", Created: time.Now().Unix(), Model: meta.ModelName,
			Choices: []protocolkit.CompletionsStreamResponseChoice{}, Usage: usage,
		}
		encoded, err = protocolkit.MarshalJSON(terminal)
		if err != nil {
			return usage, fmt.Errorf("encode Cloudflare completion terminal usage: %w", err)
		}
		if err := writeCloudflareSSE(c, encoded); err != nil {
			return usage, err
		}
	}
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write Cloudflare completion stream terminator: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}

func (a *Adaptor) openAIError(c *gin.Context, meta *relaycommon.Meta, status int, openAIError protocolkit.OpenAIError) (*protocolkit.Usage, error) {
	body, err := protocolkit.MarshalJSON(map[string]any{"error": openAIError})
	if err != nil {
		return nil, fmt.Errorf("encode Cloudflare error: %w", err)
	}
	response := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
	return a.openAI.DoResponse(c, response, meta)
}

func cloudflareResultText(body []byte, field string) (string, *protocolkit.OpenAIError, error) {
	var envelope map[string]any
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil || envelope == nil {
		return "", nil, errors.New("Cloudflare returned an invalid JSON object")
	}
	if value, exists := envelope["error"]; exists && value != nil {
		openAIError := decodeOpenAIError(value)
		return "", &openAIError, nil
	}
	if success, exists := envelope["success"].(bool); exists && !success {
		openAIError := cloudflareEnvelopeError(envelope)
		return "", &openAIError, nil
	}
	if direct, ok := envelope[field].(string); ok {
		return direct, nil, nil
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		if direct, ok := envelope["result"].(string); ok && field == "response" {
			return direct, nil, nil
		}
		return "", nil, errors.New("Cloudflare response is missing result")
	}
	text, ok := result[field].(string)
	if !ok {
		return "", nil, fmt.Errorf("Cloudflare response result is missing %s", field)
	}
	return text, nil, nil
}

func cloudflareEnvelopeError(envelope map[string]any) protocolkit.OpenAIError {
	message := "Cloudflare request failed"
	code := "cloudflare_error"
	if errorsValue, ok := envelope["errors"].([]any); ok && len(errorsValue) > 0 {
		if first, ok := errorsValue[0].(map[string]any); ok {
			if value, ok := first["message"].(string); ok && strings.TrimSpace(value) != "" {
				message = value
			}
			if value, exists := first["code"]; exists && value != nil {
				code = fmt.Sprint(value)
			}
		}
	}
	return protocolkit.OpenAIError{Message: message, Type: "cloudflare_error", Code: code}
}

func decodeOpenAIError(value any) protocolkit.OpenAIError {
	raw, err := protocolkit.MarshalJSON(value)
	if err == nil {
		var decoded protocolkit.OpenAIError
		if protocolkit.UnmarshalJSON(raw, &decoded) == nil && strings.TrimSpace(decoded.Message) != "" {
			return decoded
		}
	}
	return protocolkit.OpenAIError{Message: "Cloudflare request failed", Type: "cloudflare_error", Code: "cloudflare_error"}
}

func estimatedCloudflareUsage(promptTokens int, text string) *protocolkit.Usage {
	completionTokens := relaycommon.CountTokens(text)
	return &protocolkit.Usage{
		PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: promptTokens + completionTokens,
	}
}

func partialCloudflareUsage(promptTokens int, text string, wrote bool) *protocolkit.Usage {
	if !wrote {
		return nil
	}
	return estimatedCloudflareUsage(promptTokens, text)
}

func cloudflareIncludeUsage(meta *relaycommon.Meta) bool {
	return meta != nil && meta.Request != nil && meta.Request.StreamOptions != nil && meta.Request.StreamOptions.IncludeUsage
}

func cloudflareSSEData(line string) ([]byte, bool, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil, false, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" {
		return nil, false, false
	}
	if data == "[DONE]" {
		return nil, false, true
	}
	return []byte(data), true, false
}

func contentText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		var text strings.Builder
		for _, value := range typed {
			part, _ := value.(map[string]any)
			if partText, ok := part["text"].(string); ok {
				text.WriteString(partText)
			}
		}
		return text.String()
	default:
		return ""
	}
}

func newCloudflareResponseID(prefix string) string {
	return prefix + common.BestEffortRandomAlphanumeric(24)
}

func writeCloudflareJSON(c *gin.Context, status int, body []byte) error {
	c.Status(status)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return fmt.Errorf("write Cloudflare response: %w", err)
	}
	return nil
}

func setCloudflareStreamHeaders(c *gin.Context, status int) {
	c.Status(status)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()
}

func writeCloudflareSSE(c *gin.Context, body []byte) error {
	if _, err := c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
		return fmt.Errorf("write Cloudflare stream: %w", err)
	}
	c.Writer.Flush()
	return nil
}
