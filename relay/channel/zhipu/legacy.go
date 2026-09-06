package zhipu

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type LegacyAdaptor struct {
	mode   constant.RelayMode
	format constant.RelayFormat
}

var _ relaycommon.Adaptor = (*LegacyAdaptor)(nil)

type legacyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type legacyRequest struct {
	Prompt      []legacyMessage `json:"prompt"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Incremental bool            `json:"incremental,omitempty"`
}

type legacyResponse struct {
	Code      any    `json:"code"`
	Msg       string `json:"msg"`
	Success   bool   `json:"success"`
	RequestID string `json:"request_id,omitempty"`
	Data      struct {
		TaskID     string            `json:"task_id"`
		RequestID  string            `json:"request_id"`
		TaskStatus string            `json:"task_status"`
		Choices    []legacyMessage   `json:"choices"`
		Usage      protocolkit.Usage `json:"usage"`
	} `json:"data"`
}

type legacyStreamMeta struct {
	RequestID  string            `json:"request_id"`
	TaskID     string            `json:"task_id"`
	TaskStatus string            `json:"task_status"`
	Usage      protocolkit.Usage `json:"usage"`
}

func (a *LegacyAdaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
}

func (a *LegacyAdaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	return LegacyRequestURL(meta.BaseURL, meta.ModelName, meta.IsStream)
}

func (a *LegacyAdaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("legacy Zhipu request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	authorization, err := LegacyAuthorization(meta.APIKey)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *LegacyAdaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	messages := make([]legacyMessage, 0, len(meta.Request.Messages)+1)
	for _, message := range meta.Request.Messages {
		content := legacyContentText(message.Content)
		messages = append(messages, legacyMessage{Role: message.Role, Content: content})
		if message.Role == "system" {
			messages = append(messages, legacyMessage{Role: "user", Content: "Okay"})
		}
	}
	request := legacyRequest{
		Prompt: messages, Temperature: meta.Request.Temperature, Incremental: false,
	}
	if meta.Request.TopP != nil {
		topP := *meta.Request.TopP
		if topP >= 1 {
			topP = 0.99
		}
		request.TopP = &topP
	}
	return protocolkit.MarshalJSON(request)
}

func (a *LegacyAdaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("legacy Zhipu response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, zhipuHTTPError(response, meta, "zhipu_error")
	}
	if meta.IsStream {
		return legacyStreamResponse(c, response, meta)
	}
	return legacyNonStreamResponse(c, response, meta)
}

func (a *LegacyAdaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("legacy Zhipu relay metadata is nil")
	}
	if a.mode != constant.RelayModeChatCompletions || a.format != constant.RelayFormatOpenAI {
		return fmt.Errorf("legacy Zhipu supports only OpenAI chat completions, got mode %d format %q", a.mode, a.format)
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return errors.New("legacy Zhipu upstream model is empty")
	}
	return nil
}

func legacyContentText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	parts, ok := content.([]any)
	if !ok {
		return ""
	}
	var output strings.Builder
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok || part["type"] != protocolkit.ContentTypeText {
			continue
		}
		if text, ok := part["text"].(string); ok {
			output.WriteString(text)
		}
	}
	return output.String()
}

func legacyNonStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read legacy Zhipu response: %w", err)
	}
	var provider legacyResponse
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, fmt.Errorf("decode legacy Zhipu response: %w", err)
	}
	if !provider.Success {
		status := mappedStatusCode(meta, http.StatusBadRequest)
		code := codeString(provider.Code)
		if code == "" {
			code = "zhipu_error"
		}
		message := strings.TrimSpace(provider.Msg)
		if message == "" {
			message = "legacy Zhipu request failed"
		}
		return nil, relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
			Message: message, Type: "zhipu_error", Code: code, Param: provider.RequestID,
		}, status)
	}
	usage := provider.Data.Usage
	protocolkit.NormalizeOpenAIUsageAliases(&usage)
	if err := validateZhipuUsage(&usage); err != nil {
		return nil, err
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		contentBytes := 0
		for _, choice := range provider.Data.Choices {
			contentBytes += len(choice.Content)
		}
		usage = *relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
	}
	choices := make([]protocolkit.ChatCompletionsChoice, 0, len(provider.Data.Choices))
	for index, choice := range provider.Data.Choices {
		finishReason := ""
		if index == len(provider.Data.Choices)-1 {
			finishReason = "stop"
		}
		choices = append(choices, protocolkit.ChatCompletionsChoice{
			Index: index,
			Message: &protocolkit.ChatResponseMessage{
				Role: choice.Role, Content: strings.Trim(choice.Content, "\""),
			},
			FinishReason: finishReason,
		})
	}
	out, err := protocolkit.MarshalJSON(protocolkit.ChatCompletionsResponse{
		Id: provider.Data.TaskID, Object: "chat.completion", Created: time.Now().Unix(),
		Model: meta.ModelName, Choices: choices, Usage: &usage,
	})
	if err != nil {
		return nil, fmt.Errorf("encode legacy Zhipu response: %w", err)
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return &usage, fmt.Errorf("write legacy Zhipu response: %w", err)
	}
	return &usage, nil
}

func legacyStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(response.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	contentBytes := 0
	scanner := relaycommon.NewUpstreamSSEScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			content := strings.TrimPrefix(line, "data:")
			contentBytes += len(content)
			chunk := protocolkit.ChatCompletionsStreamResponse{
				Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: "chatglm",
				Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
					Delta: protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: content},
				}},
			}
			encoded, err := protocolkit.MarshalJSON(chunk)
			if err != nil {
				return usage, fmt.Errorf("encode legacy Zhipu stream chunk: %w", err)
			}
			if _, err := c.Writer.WriteString("data: " + string(encoded) + "\n\n"); err != nil {
				return usage, fmt.Errorf("write legacy Zhipu stream chunk: %w", err)
			}
			c.Writer.Flush()
		case strings.HasPrefix(line, "meta:"):
			var provider legacyStreamMeta
			if err := protocolkit.UnmarshalJSON([]byte(strings.TrimPrefix(line, "meta:")), &provider); err != nil {
				return usage, fmt.Errorf("decode legacy Zhipu stream metadata: %w", err)
			}
			candidate := provider.Usage
			protocolkit.NormalizeOpenAIUsageAliases(&candidate)
			if err := validateZhipuUsage(&candidate); err != nil {
				return usage, err
			}
			usage = &candidate
			stop := "stop"
			chunk := protocolkit.ChatCompletionsStreamResponse{
				Id: provider.RequestID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: "chatglm",
				Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
					Delta: protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: ""}, FinishReason: &stop,
				}},
			}
			encoded, err := protocolkit.MarshalJSON(chunk)
			if err != nil {
				return usage, fmt.Errorf("encode legacy Zhipu stream metadata: %w", err)
			}
			if _, err := c.Writer.WriteString("data: " + string(encoded) + "\n\n"); err != nil {
				return usage, fmt.Errorf("write legacy Zhipu stream metadata: %w", err)
			}
			c.Writer.Flush()
		case line == "":
		default:
			// The legacy endpoint may emit keep-alives or event labels in
			// addition to its data/meta records. They carry no client payload.
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read legacy Zhipu event stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if usage == nil || usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentBytes)
	}
	if _, err := io.WriteString(c.Writer, "data: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write legacy Zhipu stream terminator: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}

func zhipuHTTPError(response *http.Response, meta *relaycommon.Meta, errorType string) error {
	if response == nil {
		return errors.New("Zhipu upstream response is nil")
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamErrorBodyBytes)
	if err != nil {
		return &relaycommon.UpstreamError{
			StatusCode: mappedStatusCode(meta, response.StatusCode),
			Cause:      fmt.Errorf("read Zhipu upstream error: %w", err),
		}
	}
	var envelope struct {
		Code      any                      `json:"code"`
		Msg       string                   `json:"msg"`
		Message   string                   `json:"message"`
		RequestID string                   `json:"request_id"`
		Error     *protocolkit.OpenAIError `json:"error"`
	}
	if protocolkit.UnmarshalJSON(body, &envelope) == nil {
		if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
			return relaycommon.UpstreamErrorFromOpenAI(*envelope.Error, mappedStatusCode(meta, response.StatusCode))
		}
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = strings.TrimSpace(envelope.Msg)
		}
		if message != "" || codeString(envelope.Code) != "" {
			code := codeString(envelope.Code)
			if code == "" {
				code = errorType
			}
			return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
				Message: message, Type: errorType, Code: code, Param: envelope.RequestID,
			}, mappedStatusCode(meta, response.StatusCode))
		}
	}
	return &relaycommon.UpstreamError{StatusCode: mappedStatusCode(meta, response.StatusCode), Body: string(body)}
}

func validateZhipuUsage(usage *protocolkit.Usage) error {
	if usage == nil {
		return nil
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 ||
		usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return errors.New("Zhipu response contains negative usage")
	}
	return nil
}
