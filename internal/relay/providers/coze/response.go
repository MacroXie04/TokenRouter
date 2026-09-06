package coze

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type cozeError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cozeUsage struct {
	TokenCount  int `json:"token_count"`
	OutputCount int `json:"output_count"`
	InputCount  int `json:"input_count"`
}

type chatData struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	BotID          string    `json:"bot_id"`
	CreatedAt      int64     `json:"created_at"`
	LastError      cozeError `json:"last_error"`
	Status         string    `json:"status"`
	Usage          cozeUsage `json:"usage"`
}

type chatEnvelope struct {
	Code int      `json:"code"`
	Msg  string   `json:"msg"`
	Data chatData `json:"data"`
}

type messageListEnvelope struct {
	Data []messageDetail `json:"data"`
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
}

type messageDetail struct {
	ID             string          `json:"id"`
	Role           string          `json:"role"`
	Type           string          `json:"type"`
	BotID          string          `json:"bot_id"`
	ChatID         string          `json:"chat_id"`
	Content        json.RawMessage `json:"content"`
	CreatedAt      int64           `json:"created_at"`
	ContentType    string          `json:"content_type"`
	ConversationID string          `json:"conversation_id"`
}

func (a *Adaptor) convertBlockingResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, maxProviderResponseBody)
	if err != nil {
		return acceptedCozeUsage(meta), fmt.Errorf("read Coze chat creation response: %w", err)
	}
	var creation chatEnvelope
	if err := strictJSON(body, &creation); err != nil {
		return acceptedCozeUsage(meta), errors.New("Coze returned an invalid chat creation response")
	}
	if creation.Code != 0 {
		return nil, cozeProviderError(creation.Code, creation.Msg, http.StatusBadGateway)
	}
	if err := validateProviderID("conversation", creation.Data.ConversationID); err != nil {
		return acceptedCozeUsage(meta), err
	}
	if err := validateProviderID("chat", creation.Data.ID); err != nil {
		return acceptedCozeUsage(meta), err
	}

	providerUsage, err := a.pollUntilComplete(meta, creation.Data.ConversationID, creation.Data.ID)
	if err != nil {
		return acceptedCozeUsage(meta), err
	}
	messages, err := a.getMessageList(meta, creation.Data.ConversationID, creation.Data.ID)
	if err != nil {
		return acceptedCozeUsage(meta), err
	}
	answer, createdAt, err := cozeAnswer(messages, creation.Data.ConversationID, creation.Data.ID)
	if err != nil {
		return acceptedCozeUsage(meta), err
	}
	usage, err := normalizedCozeUsage(providerUsage, meta, answer)
	if err != nil {
		return acceptedCozeUsage(meta), err
	}
	responseID, err := randomResponseID()
	if err != nil {
		return usage, err
	}
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	output := protocolkit.ChatCompletionsResponse{
		Id:      responseID,
		Object:  "chat.completion",
		Created: createdAt,
		Model:   cozeClientModel(meta),
		Choices: []protocolkit.ChatCompletionsChoice{{
			Index: 0,
			Message: &protocolkit.ChatResponseMessage{
				Role:    "assistant",
				Content: answer,
			},
			FinishReason: "stop",
		}},
		Usage: usage,
	}
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return usage, errors.New("encode Coze client response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	if _, err := c.Writer.Write(encoded); err != nil {
		return usage, fmt.Errorf("write Coze client response: %w", err)
	}
	return usage, nil
}

func cozeAnswer(envelope messageListEnvelope, conversationID, chatID string) (string, int64, error) {
	found := false
	answer := ""
	createdAt := int64(0)
	for _, message := range envelope.Data {
		if strings.ToLower(strings.TrimSpace(message.Type)) != "answer" {
			continue
		}
		if message.ConversationID != "" && message.ConversationID != conversationID {
			return "", 0, errors.New("Coze answer returned a mismatched conversation ID")
		}
		if message.ChatID != "" && message.ChatID != chatID {
			return "", 0, errors.New("Coze answer returned a mismatched chat ID")
		}
		if len(message.Content) > maxStreamTextBytes+2 {
			return "", 0, fmt.Errorf("Coze answer exceeds %d bytes", maxStreamTextBytes)
		}
		content, err := cozeContentString(message.Content)
		if err != nil || !utf8.ValidString(content) {
			return "", 0, errors.New("Coze answer content is invalid")
		}
		if len(content) > maxStreamTextBytes {
			return "", 0, fmt.Errorf("Coze answer exceeds %d bytes", maxStreamTextBytes)
		}
		found = true
		answer = content
		createdAt = message.CreatedAt
	}
	if !found {
		return "", 0, errors.New("Coze response contains no answer message")
	}
	return answer, createdAt, nil
}

func normalizedCozeUsage(provider cozeUsage, meta *relaycommon.Meta, completionText string) (*protocolkit.Usage, error) {
	values := []int{provider.InputCount, provider.OutputCount, provider.TokenCount}
	for _, value := range values {
		if value < 0 || int64(value) > quotamath.MaxQuota {
			return nil, errors.New("Coze usage is outside the supported range")
		}
	}
	input, output, total := provider.InputCount, provider.OutputCount, provider.TokenCount
	fallbackPrompt := cozePromptTokens(meta)
	if input == 0 {
		input = fallbackPrompt
	}
	if output == 0 {
		output = relaycommon.CountTokens(completionText)
	}
	sum := int64(input) + int64(output)
	if sum < 0 || sum > quotamath.MaxQuota {
		return nil, errors.New("Coze usage is outside the supported range")
	}
	if int64(total) < sum {
		total = int(sum)
	}
	if int64(total) > quotamath.MaxQuota {
		return nil, errors.New("Coze usage is outside the supported range")
	}
	return &protocolkit.Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: total}, nil
}

func acceptedCozeUsage(meta *relaycommon.Meta) *protocolkit.Usage {
	prompt := cozePromptTokens(meta)
	if prompt <= 0 {
		prompt = 1
	}
	return &protocolkit.Usage{PromptTokens: prompt, TotalTokens: prompt}
}

func cozePromptTokens(meta *relaycommon.Meta) int {
	prompt := 0
	if meta != nil {
		prompt = meta.PromptTokens
		if prompt <= 0 {
			prompt = relaycommon.EstimatePromptTokens(meta.Request)
		}
	}
	if prompt < 0 || int64(prompt) > quotamath.MaxQuota {
		return 0
	}
	return prompt
}

func cozeClientModel(meta *relaycommon.Meta) string {
	if meta == nil {
		return ""
	}
	if model := strings.TrimSpace(meta.OriginalModelName); model != "" {
		return model
	}
	if meta.Request != nil {
		if model := strings.TrimSpace(meta.Request.Model); model != "" {
			return model
		}
	}
	return strings.TrimSpace(meta.ModelName)
}

type cozeStreamState struct {
	c        *gin.Context
	status   int
	meta     *relaycommon.Meta
	id       string
	model    string
	started  bool
	done     bool
	rejected bool
	events   int
	text     strings.Builder
	usage    *protocolkit.Usage
}

func (a *Adaptor) convertStreamResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	id, err := randomResponseID()
	if err != nil {
		return acceptedCozeUsage(meta), err
	}
	state := &cozeStreamState{
		c: c, status: response.StatusCode, meta: meta,
		id: id, model: cozeClientModel(meta),
	}
	limited := &io.LimitedReader{R: response.Body, N: maxStreamBodyBytes + 1}
	scanner := relaycommon.NewUpstreamSSEScanner(limited)
	eventName := ""
	dataLines := make([]string, 0, 1)
	eventBytes := 0
	processPending := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			eventBytes = 0
			return nil
		}
		if strings.TrimSpace(eventName) == "" {
			return errors.New("Coze stream event is missing its event name")
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		name := eventName
		eventName = ""
		eventBytes = 0
		return state.processEvent(name, data)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := processPending(); err != nil {
				return state.failureUsage(), err
			}
			if state.done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if len(eventName) > maxProviderIDBytes || !utf8.ValidString(eventName) {
				return state.failureUsage(), errors.New("Coze stream event name is invalid")
			}
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
			return state.failureUsage(), fmt.Errorf("Coze stream event exceeds %d bytes", relaycommon.MaxUpstreamSSEEventBytes)
		}
		eventBytes += additional
		dataLines = append(dataLines, value)
	}
	if !state.done {
		if err := processPending(); err != nil {
			return state.failureUsage(), err
		}
	}
	if err := scanner.Err(); err != nil {
		return state.failureUsage(), fmt.Errorf("read Coze event stream: %w", err)
	}
	if limited.N == 0 {
		return state.failureUsage(), fmt.Errorf("%w: Coze stream maximum is %d bytes", relaycommon.ErrUpstreamResponseTooLarge, maxStreamBodyBytes)
	}
	if !state.done {
		return state.failureUsage(), errors.New("Coze stream ended before a completion event")
	}
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return state.failureUsage(), fmt.Errorf("write Coze stream terminator: %w", err)
	}
	c.Writer.Flush()
	return state.usage, nil
}

func (state *cozeStreamState) processEvent(eventName, data string) error {
	state.events++
	if state.events > maxStreamEvents {
		return fmt.Errorf("Coze stream exceeds %d events", maxStreamEvents)
	}
	if strings.TrimSpace(data) == "" {
		return errors.New("Coze stream event data is empty")
	}
	switch strings.TrimSpace(eventName) {
	case "conversation.message.delta":
		var message messageDetail
		if err := strictJSON([]byte(data), &message); err != nil {
			return errors.New("decode Coze message delta")
		}
		content, err := cozeContentString(message.Content)
		if len(message.Content) > maxStreamTextBytes+2 || err != nil || !utf8.ValidString(content) {
			return errors.New("Coze message delta content is invalid")
		}
		if len(content) > maxStreamTextBytes-state.text.Len() {
			return fmt.Errorf("Coze streamed text exceeds %d bytes", maxStreamTextBytes)
		}
		if content == "" {
			return nil
		}
		state.text.WriteString(content)
		delta := protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: content}
		if !state.started {
			delta.Role = "assistant"
		}
		return state.writeChunk(delta, nil, nil)
	case "conversation.chat.completed":
		var completed chatData
		if err := strictJSON([]byte(data), &completed); err != nil {
			return errors.New("decode Coze completion event")
		}
		if status := strings.ToLower(strings.TrimSpace(completed.Status)); status != "" && status != "completed" {
			return errors.New("Coze completion event has an invalid status")
		}
		usage, err := normalizedCozeUsage(completed.Usage, state.meta, state.text.String())
		if err != nil {
			return err
		}
		finish := "stop"
		state.usage = usage
		if err := state.writeChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &finish, usage); err != nil {
			return err
		}
		state.done = true
		return nil
	case "error":
		var provider cozeError
		if err := strictJSON([]byte(data), &provider); err != nil {
			return errors.New("decode Coze stream error")
		}
		if !state.started {
			state.rejected = true
		}
		return cozeProviderError(provider.Code, provider.Message, http.StatusBadGateway)
	default:
		return nil
	}
}

func cozeContentString(raw json.RawMessage) (string, error) {
	var value any
	if err := strictJSON(raw, &value); err != nil {
		return "", err
	}
	content, ok := value.(string)
	if !ok {
		return "", errors.New("Coze content is not a string")
	}
	return content, nil
}

func (state *cozeStreamState) writeChunk(delta protocolkit.ChatCompletionsStreamResponseChoiceDelta, finish *string, usage *protocolkit.Usage) error {
	chunk := protocolkit.ChatCompletionsStreamResponse{
		Id: state.id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: state.model,
		Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
			Index: 0, Delta: delta, FinishReason: finish,
		}},
		Usage: usage,
	}
	body, err := protocolkit.MarshalJSON(chunk)
	if err != nil {
		return errors.New("encode Coze stream chunk")
	}
	state.ensureStarted()
	if _, err := state.c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
		return fmt.Errorf("write Coze stream chunk: %w", err)
	}
	state.c.Writer.Flush()
	return nil
}

func (state *cozeStreamState) ensureStarted() {
	if state.started {
		return
	}
	state.c.Header("Content-Type", "text/event-stream")
	state.c.Header("Cache-Control", "no-cache")
	state.c.Header("Connection", "keep-alive")
	state.c.Status(state.status)
	state.started = true
}

func (state *cozeStreamState) failureUsage() *protocolkit.Usage {
	if state.rejected && !state.started {
		return nil
	}
	if state.usage != nil {
		return state.usage
	}
	if state.started {
		usage, err := normalizedCozeUsage(cozeUsage{}, state.meta, state.text.String())
		if err == nil {
			return usage
		}
	}
	return acceptedCozeUsage(state.meta)
}
