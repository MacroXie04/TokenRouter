package dify

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type difyMetadata struct {
	Usage protocolkit.Usage `json:"usage"`
}

type difyBlockingEnvelope struct {
	ConversationID string       `json:"conversation_id"`
	Answer         string       `json:"answer"`
	CreateAt       int64        `json:"create_at"`
	Metadata       difyMetadata `json:"metadata"`
}

type difyEventData struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	NodeType   string `json:"node_type"`
	Status     string `json:"status"`
}

type difyStreamEnvelope struct {
	Event          string        `json:"event"`
	ConversationID string        `json:"conversation_id"`
	Answer         string        `json:"answer"`
	Message        string        `json:"message"`
	Code           any           `json:"code"`
	Data           difyEventData `json:"data"`
	Metadata       difyMetadata  `json:"metadata"`
}

func difyBlockingResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Dify response: %w", err)
	}
	var provider difyBlockingEnvelope
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, errors.New("decode Dify response")
	}
	usage, err := normalizedDifyUsage(provider.Metadata.Usage, meta.PromptTokens, provider.Answer, 0)
	if err != nil {
		return usage, err
	}
	output := protocolkit.ChatCompletionsResponse{
		Id: provider.ConversationID, Object: "chat.completion", Created: time.Now().Unix(), Model: "",
		Choices: []protocolkit.ChatCompletionsChoice{{
			Index:        0,
			Message:      &protocolkit.ChatResponseMessage{Role: "assistant", Content: provider.Answer},
			FinishReason: "stop",
		}},
		Usage: usage,
	}
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return usage, errors.New("encode Dify client response")
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(encoded); err != nil {
		return usage, fmt.Errorf("write Dify client response: %w", err)
	}
	return usage, nil
}

type difyStreamState struct {
	c               *gin.Context
	status          int
	meta            *relaycommon.Meta
	started         bool
	content         strings.Builder
	contentBytes    int
	reasoningEvents int
	events          int
	usage           *protocolkit.Usage
}

func difyStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	return difyStreamResponseWithLimit(c, response, meta, maxDifyStreamBodyBytes)
}

func difyStreamResponseWithLimit(c *gin.Context, response *http.Response, meta *relaycommon.Meta, maxBodyBytes int64) (*protocolkit.Usage, error) {
	if maxBodyBytes <= 0 {
		return nil, errors.New("Dify stream limit is invalid")
	}
	state := &difyStreamState{c: c, status: response.StatusCode, meta: meta}
	limited := &io.LimitedReader{R: response.Body, N: maxBodyBytes + 1}
	scanner := relaycommon.NewUpstreamSSEScanner(limited)
	dataLines := make([]string, 0, 1)
	eventBytes := 0
	done := false
	processPending := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		eventBytes = 0
		var err error
		done, err = state.processEvent(data)
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := processPending(); err != nil {
				return state.partialUsage(), err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		value := strings.TrimPrefix(line, "data:")
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		additional := len(value)
		if len(dataLines) > 0 {
			additional++
		}
		if additional > relaycommon.MaxUpstreamSSEEventBytes-eventBytes {
			return state.partialUsage(), fmt.Errorf("read Dify event stream: event exceeds %d bytes", relaycommon.MaxUpstreamSSEEventBytes)
		}
		eventBytes += additional
		dataLines = append(dataLines, value)
	}
	if !done {
		if err := processPending(); err != nil {
			return state.partialUsage(), err
		}
	}
	if err := scanner.Err(); err != nil {
		return state.partialUsage(), fmt.Errorf("read Dify event stream (maximum line %d bytes): %w", relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if limited.N == 0 {
		return state.partialUsage(), fmt.Errorf("%w: Dify stream maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, maxBodyBytes)
	}
	usage, err := state.finalUsage()
	if err != nil {
		return usage, err
	}
	state.ensureStarted()
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write Dify stream terminator: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}

func (state *difyStreamState) processEvent(data string) (bool, error) {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return false, nil
	}
	state.events++
	if state.events > maxDifyStreamEvents {
		return false, fmt.Errorf("Dify stream exceeds %d events", maxDifyStreamEvents)
	}
	var event difyStreamEnvelope
	if err := protocolkit.UnmarshalJSON([]byte(data), &event); err != nil {
		return false, errors.New("decode Dify stream event")
	}
	switch event.Event {
	case "message_end":
		usage := event.Metadata.Usage
		state.usage = &usage
		return true, nil
	case "error":
		return false, difyEventError(event, state.meta)
	}

	delta := protocolkit.ChatCompletionsStreamResponseChoiceDelta{}
	if event.Event == "message" || event.Event == "agent_message" {
		answer := event.Answer
		if answer == difyThinkingOpen {
			answer = difyThinkingOpenReplacement
		} else if answer == difyThinkingClose {
			answer = difyThinkingCloseReplacement
		}
		if len(answer) > maxDifyStreamTextBytes-state.contentBytes {
			return false, fmt.Errorf("Dify streamed text exceeds %d bytes", maxDifyStreamTextBytes)
		}
		state.contentBytes += len(answer)
		state.content.WriteString(answer)
		delta.Content = answer
	} else if strings.HasPrefix(event.Event, "workflow_") && env.GetEnvBool("DIFY_DEBUG", true) {
		reasoning := "Workflow: " + event.Data.WorkflowID
		if event.Event == "workflow_finished" {
			reasoning += " " + event.Data.Status
		}
		delta.ReasoningContent = reasoning + "\n"
		state.reasoningEvents++
	} else if strings.HasPrefix(event.Event, "node_") && env.GetEnvBool("DIFY_DEBUG", true) {
		reasoning := "Node: " + event.Data.NodeType
		if event.Event == "node_finished" {
			reasoning += " " + event.Data.Status
		}
		delta.ReasoningContent = reasoning + "\n"
		state.reasoningEvents++
	}
	return false, state.writeChunk(delta)
}

func (state *difyStreamState) writeChunk(delta protocolkit.ChatCompletionsStreamResponseChoiceDelta) error {
	chunk := protocolkit.ChatCompletionsStreamResponse{
		Id: "", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: difyStreamModel,
		Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{Index: 0, Delta: delta}},
	}
	body, err := protocolkit.MarshalJSON(chunk)
	if err != nil {
		return errors.New("encode Dify stream chunk")
	}
	state.ensureStarted()
	if _, err := state.c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
		return fmt.Errorf("write Dify stream chunk: %w", err)
	}
	state.c.Writer.Flush()
	return nil
}

func (state *difyStreamState) ensureStarted() {
	if state.started {
		return
	}
	state.c.Header("Content-Type", "text/event-stream")
	state.c.Header("Cache-Control", "no-cache")
	state.c.Header("Connection", "keep-alive")
	state.c.Status(state.status)
	state.started = true
}

func (state *difyStreamState) finalUsage() (*protocolkit.Usage, error) {
	provider := protocolkit.Usage{}
	if state.usage != nil {
		provider = *state.usage
	}
	return normalizedDifyUsage(provider, state.meta.PromptTokens, state.content.String(), state.reasoningEvents)
}

func (state *difyStreamState) partialUsage() *protocolkit.Usage {
	// Before the first valid provider event reaches the client there is no
	// successful upstream result to settle. Returning nil keeps the ordinary
	// relay response retractable and lets its reservation refund in full.
	if !state.started {
		return nil
	}
	usage, err := state.finalUsage()
	if err != nil {
		return nil
	}
	return usage
}

func normalizedDifyUsage(provider protocolkit.Usage, fallbackPrompt int, completionText string, reasoningEvents int) (*protocolkit.Usage, error) {
	usage := provider
	protocolkit.NormalizeOpenAIUsageAliases(&usage)
	if fallbackPrompt < 0 || reasoningEvents < 0 {
		return &usage, errors.New("Dify usage is outside the supported range")
	}
	if err := validateDifyUsageFields(&usage); err != nil {
		return &usage, err
	}
	for _, value := range []int{fallbackPrompt, reasoningEvents} {
		if value < 0 || int64(value) > quotamath.MaxQuota {
			return &usage, errors.New("Dify usage is outside the supported range")
		}
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		usage.PromptTokens = fallbackPrompt
		usage.CompletionTokens = relaycommon.CountTokens(completionText)
	}
	completion := int64(usage.CompletionTokens) + int64(reasoningEvents)
	total := int64(usage.PromptTokens) + completion
	if completion > quotamath.MaxQuota || total > quotamath.MaxQuota {
		return &usage, errors.New("Dify usage is outside the supported range")
	}
	usage.CompletionTokens = int(completion)
	usage.TotalTokens = int(total)
	if err := validateDifyUsageFields(&usage); err != nil {
		return &usage, err
	}
	return &usage, nil
}

func validateDifyUsageFields(usage *protocolkit.Usage) error {
	if usage == nil {
		return nil
	}
	values := []int{
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		usage.InputTokens, usage.OutputTokens,
		usage.PromptCacheHitTokens, usage.PromptCacheMissTokens,
		usage.PromptCacheWriteTokens, usage.PromptCacheCreationTokens,
		usage.PromptCacheCreation5mTokens, usage.PromptCacheCreation1hTokens,
		usage.AudioTokens, usage.ReasoningTokens,
	}
	appendInputDetails := func(details *protocolkit.InputTokenDetails) {
		if details == nil {
			return
		}
		values = append(values,
			details.CachedTokens, details.CachedCreationTokens, details.CacheWriteTokens,
			details.CacheCreation5mTokens, details.CacheCreation1hTokens,
			details.TextTokens, details.AudioTokens, details.ImageTokens, details.ReasoningTokens,
		)
	}
	appendOutputDetails := func(details *protocolkit.OutputTokenDetails) {
		if details == nil {
			return
		}
		values = append(values, details.TextTokens, details.AudioTokens, details.ImageTokens, details.ReasoningTokens)
	}
	appendInputDetails(usage.PromptTokensDetails)
	appendInputDetails(usage.InputTokensDetails)
	appendOutputDetails(usage.CompletionTokensDetails)
	appendOutputDetails(usage.OutputTokensDetails)
	for _, value := range values {
		if value < 0 || int64(value) > quotamath.MaxQuota {
			return errors.New("Dify usage is outside the supported range")
		}
	}
	return nil
}

func difyHTTPError(response *http.Response, meta *relaycommon.Meta) error {
	err := relaycommon.HandleErrorResponse(response)
	var upstream *relaycommon.UpstreamError
	if !errors.As(err, &upstream) || upstream == nil {
		return err
	}
	copyOfError := *upstream
	copyOfError.StatusCode = mappedStatusCode(meta, copyOfError.StatusCode)
	if copyOfError.Cause != nil || copyOfError.Body == "" {
		return &copyOfError
	}
	var payload struct {
		Code    any                      `json:"code"`
		Message string                   `json:"message"`
		Error   *protocolkit.OpenAIError `json:"error"`
	}
	if protocolkit.UnmarshalJSON([]byte(copyOfError.Body), &payload) != nil {
		return &copyOfError
	}
	if payload.Error != nil {
		return &copyOfError
	}
	if strings.TrimSpace(payload.Message) == "" {
		return &copyOfError
	}
	return difyOpenAIError(copyOfError.StatusCode, payload.Message, difyCodeString(payload.Code))
}

func difyEventError(event difyStreamEnvelope, meta *relaycommon.Meta) error {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = "Dify stream returned an error"
	}
	return difyOpenAIError(mappedStatusCode(meta, http.StatusBadGateway), message, difyCodeString(event.Code))
}

func difyOpenAIError(status int, message, code string) error {
	if len(message) > int(relaycommon.MaxUpstreamErrorBodyBytes) || len(code) > maxDifyUploadIDBytes {
		return &relaycommon.UpstreamError{
			StatusCode: status,
			Cause:      fmt.Errorf("%w: maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, relaycommon.MaxUpstreamErrorBodyBytes),
		}
	}
	body, err := protocolkit.MarshalJSON(gin.H{"error": protocolkit.OpenAIError{
		Message: message, Type: "upstream_error", Code: code,
	}})
	if err != nil || len(body) > int(relaycommon.MaxUpstreamErrorBodyBytes) {
		if err == nil {
			err = relaycommon.ErrUpstreamResponseTooLarge
		}
		return &relaycommon.UpstreamError{StatusCode: status, Cause: err}
	}
	return &relaycommon.UpstreamError{StatusCode: status, Body: string(body)}
}

func difyCodeString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}
