package baidu

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type legacyError struct {
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

type legacyChatResponseBody struct {
	ID               string            `json:"id"`
	Object           string            `json:"object"`
	Created          int64             `json:"created"`
	Result           string            `json:"result"`
	IsTruncated      bool              `json:"is_truncated"`
	NeedClearHistory bool              `json:"need_clear_history"`
	Usage            protocolkit.Usage `json:"usage"`
	legacyError
}

type legacyStreamResponseBody struct {
	legacyChatResponseBody
	SentenceID int  `json:"sentence_id"`
	IsEnd      bool `json:"is_end"`
}

type legacyEmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type legacyEmbeddingResponseBody struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Data    []legacyEmbeddingData `json:"data"`
	Usage   protocolkit.Usage     `json:"usage"`
	legacyError
}

func legacyChatResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Baidu chat response: %w", err)
	}
	var provider legacyChatResponseBody
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, errors.New("Baidu returned an invalid chat response")
	}
	if provider.ErrorMsg != "" || provider.ErrorCode != 0 {
		return nil, legacyBusinessError(meta, http.StatusBadRequest, provider.legacyError)
	}
	usage, err := normalizedLegacyUsage(provider.Usage)
	if err != nil {
		return nil, err
	}
	response := protocolkit.ChatCompletionsResponse{
		Id: provider.ID, Object: "chat.completion", Created: provider.Created, Model: meta.ModelName,
		Choices: []protocolkit.ChatCompletionsChoice{{
			Index: 0, Message: &protocolkit.ChatResponseMessage{Role: "assistant", Content: provider.Result}, FinishReason: "stop",
		}},
		Usage: usage,
	}
	out, err := protocolkit.MarshalJSON(response)
	if err != nil {
		return nil, fmt.Errorf("encode Baidu chat response: %w", err)
	}
	if err := writeLegacyJSON(c, resp.StatusCode, out); err != nil {
		return usage, err
	}
	return usage, nil
}

func legacyEmbeddingResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Baidu embedding response: %w", err)
	}
	var provider legacyEmbeddingResponseBody
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, errors.New("Baidu returned an invalid embedding response")
	}
	if provider.ErrorMsg != "" || provider.ErrorCode != 0 {
		return nil, legacyBusinessError(meta, http.StatusBadRequest, provider.legacyError)
	}
	usage, err := normalizedLegacyUsage(provider.Usage)
	if err != nil {
		return nil, err
	}
	data := make([]map[string]any, 0, len(provider.Data))
	for _, item := range provider.Data {
		data = append(data, map[string]any{
			"object": item.Object, "embedding": item.Embedding, "index": item.Index,
		})
	}
	response := map[string]any{"object": "list", "data": data, "model": "baidu-embedding", "usage": usage}
	out, err := protocolkit.MarshalJSON(response)
	if err != nil {
		return nil, fmt.Errorf("encode Baidu embedding response: %w", err)
	}
	if err := writeLegacyJSON(c, resp.StatusCode, out); err != nil {
		return usage, err
	}
	return usage, nil
}

func legacyStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	limited := &io.LimitedReader{R: resp.Body, N: maxBaiduStreamBodyBytes + 1}
	scanner := relaycommon.NewUpstreamSSEScanner(limited)
	var usage *protocolkit.Usage
	contentBytes := 0
	wrote := false
	doneWritten := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
				return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("write Baidu stream terminator: %w", err)
			}
			c.Writer.Flush()
			doneWritten = true
			break
		}
		var provider legacyStreamResponseBody
		if err := protocolkit.UnmarshalJSON([]byte(data), &provider); err != nil {
			return partialLegacyUsage(meta, contentBytes, wrote, usage), errors.New("Baidu returned an invalid stream event")
		}
		if provider.ErrorMsg != "" || provider.ErrorCode != 0 {
			return partialLegacyUsage(meta, contentBytes, wrote, usage), legacyBusinessError(meta, http.StatusBadRequest, provider.legacyError)
		}
		if provider.Usage.TotalTokens != 0 || provider.Usage.PromptTokens != 0 || provider.Usage.CompletionTokens != 0 {
			usage, _ = normalizedLegacyUsage(provider.Usage)
		}
		contentBytes += len(provider.Result)
		if contentBytes > maxBaiduStreamTextBytes {
			return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("Baidu stream text exceeds %d bytes", maxBaiduStreamTextBytes)
		}
		finishReason := (*string)(nil)
		if provider.IsEnd {
			stop := "stop"
			finishReason = &stop
		}
		chunk := protocolkit.ChatCompletionsStreamResponse{
			Id: provider.ID, Object: "chat.completion.chunk", Created: provider.Created, Model: "ernie-bot",
			Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
				Index:        0,
				Delta:        protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: provider.Result},
				FinishReason: finishReason,
			}},
		}
		encoded, err := protocolkit.MarshalJSON(chunk)
		if err != nil {
			return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("encode Baidu stream event: %w", err)
		}
		if _, err := c.Writer.WriteString("data: " + string(encoded) + "\n\n"); err != nil {
			return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("write Baidu stream event: %w", err)
		}
		c.Writer.Flush()
		wrote = true
	}
	if err := scanner.Err(); err != nil {
		return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("read Baidu event stream (maximum event %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if limited.N == 0 {
		return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("%w: Baidu stream maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, maxBaiduStreamBodyBytes)
	}
	if !doneWritten {
		if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
			return partialLegacyUsage(meta, contentBytes, wrote, usage), fmt.Errorf("write Baidu stream terminator: %w", err)
		}
		c.Writer.Flush()
	}
	if usage == nil {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
	}
	return usage, nil
}

func partialLegacyUsage(meta *relaycommon.Meta, contentBytes int, wrote bool, usage *protocolkit.Usage) *protocolkit.Usage {
	if usage != nil {
		return usage
	}
	if !wrote {
		return nil
	}
	return relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
}

func normalizedLegacyUsage(value protocolkit.Usage) (*protocolkit.Usage, error) {
	usage := value
	protocolkit.NormalizeOpenAIUsageAliases(&usage)
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return nil, errors.New("Baidu returned invalid negative usage")
	}
	if usage.TotalTokens > 0 && usage.CompletionTokens == 0 {
		if usage.TotalTokens < usage.PromptTokens {
			return nil, errors.New("Baidu returned inconsistent usage")
		}
		usage.CompletionTokens = usage.TotalTokens - usage.PromptTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return &usage, nil
}

func legacyHTTPError(resp *http.Response, meta *relaycommon.Meta) error {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamErrorBodyBytes)
	if err != nil {
		return mapLegacyStatus(meta, &relaycommon.UpstreamError{StatusCode: resp.StatusCode, Cause: err})
	}
	var provider legacyError
	if protocolkit.UnmarshalJSON(body, &provider) == nil && (provider.ErrorMsg != "" || provider.ErrorCode != 0) {
		return legacyBusinessError(meta, resp.StatusCode, provider)
	}
	return mapLegacyStatus(meta, &relaycommon.UpstreamError{StatusCode: resp.StatusCode, Body: string(body)})
}

func legacyBusinessError(meta *relaycommon.Meta, status int, provider legacyError) error {
	message := strings.TrimSpace(provider.ErrorMsg)
	if message == "" {
		message = "Baidu request failed"
	}
	code := "baidu_error"
	if provider.ErrorCode != 0 {
		code = fmt.Sprint(provider.ErrorCode)
	}
	err := relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: "baidu_error", Code: code,
	}, status)
	return mapLegacyStatus(meta, err)
}

func writeLegacyJSON(c *gin.Context, status int, body []byte) error {
	c.Status(status)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return fmt.Errorf("write Baidu response: %w", err)
	}
	return nil
}
