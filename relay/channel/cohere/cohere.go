// Package cohere implements Cohere's v1 Chat and Rerank wire contracts.
package cohere

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName                  = "cohere"
	defaultBaseURL               = "https://api.cohere.ai"
	defaultMaxTokens             = 4000
	maxCohereRequestBodyBytes    = 16 << 20
	maxCohereModelBytes          = 1024
	maxCohereCredentialBytes     = 8 << 10
	maxCohereRerankDocuments     = 100_000
	maxCohereStreamTextBytes     = 16 << 20
	cohereStreamResponseIDPrefix = "chatcmpl-"
)

var supportedModels = [...]string{
	"command-a-03-2025",
	"command-r",
	"command-r-plus",
	"command-r-08-2024",
	"command-r-plus-08-2024",
	"c4ai-aya-23-35b",
	"c4ai-aya-23-8b",
	"command-light",
	"command-light-nightly",
	"command",
	"command-nightly",
	"rerank-english-v3.0",
	"rerank-multilingual-v3.0",
	"rerank-english-v2.0",
	"rerank-multilingual-v2.0",
}

// ModelList returns an owned copy of Cohere's reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// Adaptor converts OpenAI Chat/Rerank requests to Cohere v1 and converts the
// corresponding responses back to the client-facing OpenAI contracts.
type Adaptor struct {
	mode constant.RelayMode
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	if meta != nil {
		a.mode = meta.Mode
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", errors.New("Cohere relay metadata is nil")
	}
	mode := a.relayMode(meta)
	if err := validateMode(mode, meta.IsStream); err != nil {
		return "", err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	path := "/v1/chat"
	if mode == constant.RelayModeRerank {
		path = "/v1/rerank"
	}
	return relaycommon.JoinURL(base, path), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Cohere request metadata is nil")
	}
	credential := strings.TrimSpace(meta.APIKey)
	if credential == "" {
		return errors.New("Cohere API key is required")
	}
	if len(credential) > maxCohereCredentialBytes || strings.ContainsAny(credential, "\r\n") {
		return errors.New("Cohere API key is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta == nil || meta.Request == nil {
		return nil, errors.New("Cohere request is nil")
	}
	mode := a.relayMode(meta)
	if err := validateMode(mode, meta.IsStream); err != nil {
		return nil, err
	}
	if err := validateModel(meta.ModelName); err != nil {
		return nil, err
	}

	var payload any
	var err error
	switch mode {
	case constant.RelayModeChatCompletions:
		payload, err = convertChatRequest(meta)
	case constant.RelayModeRerank:
		payload, err = convertRerankRequest(meta)
	default:
		return nil, fmt.Errorf("Cohere channel does not support relay mode %d", mode)
	}
	if err != nil {
		return nil, err
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Cohere request: %w", err)
	}
	if int64(len(body)) > maxCohereRequestBodyBytes {
		return nil, fmt.Errorf("Cohere request exceeds %d bytes", maxCohereRequestBodyBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, errors.New("Cohere response metadata is nil")
	}
	mode := a.relayMode(meta)
	if err := validateMode(mode, meta.IsStream); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, cohereHTTPError(resp, meta)
	}
	if mode == constant.RelayModeRerank {
		return rerankResponse(c, resp, meta)
	}
	if meta.IsStream {
		return chatStreamResponse(c, resp, meta)
	}
	return chatResponse(c, resp, meta)
}

func (a *Adaptor) relayMode(meta *relaycommon.Meta) constant.RelayMode {
	if meta != nil && meta.Mode != constant.RelayModeUnknown {
		return meta.Mode
	}
	return a.mode
}

type chatHistory struct {
	Role    string `json:"role"`
	Message string `json:"message"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	ChatHistory []chatHistory `json:"chat_history"`
	Message     string        `json:"message"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens"`
	SafetyMode  string        `json:"safety_mode,omitempty"`
}

func convertChatRequest(meta *relaycommon.Meta) (*chatRequest, error) {
	maxTokens, err := cohereMaxTokens(meta.Request)
	if err != nil {
		return nil, err
	}
	safetyMode, err := configuredSafetyMode()
	if err != nil {
		return nil, err
	}
	out := &chatRequest{
		Model:       meta.ModelName,
		ChatHistory: make([]chatHistory, 0, len(meta.Request.Messages)),
		Stream:      meta.Request.Stream,
		MaxTokens:   maxTokens,
		SafetyMode:  safetyMode,
	}
	for _, message := range meta.Request.Messages {
		text := relaycommon.MessageToText(message)
		if message.Role == "user" {
			// Cohere v1 carries the current user turn separately. The last user
			// message wins, matching the reference gateway's conversion.
			out.Message = text
			continue
		}
		role := "USER"
		switch message.Role {
		case "assistant":
			role = "CHATBOT"
		case "system":
			role = "SYSTEM"
		}
		out.ChatHistory = append(out.ChatHistory, chatHistory{Role: role, Message: text})
	}
	return out, nil
}

func cohereMaxTokens(request *protocolkit.GeneralOpenAIRequest) (int, error) {
	value := 0
	if request.MaxCompletionTokens != nil && *request.MaxCompletionTokens != 0 {
		value = *request.MaxCompletionTokens
	} else if request.MaxTokens != nil && *request.MaxTokens != 0 {
		value = *request.MaxTokens
	}
	if value == 0 {
		return defaultMaxTokens, nil
	}
	if value < 0 || int64(value) > appcommon.MaxQuota/2 {
		return 0, errors.New("Cohere max_tokens is outside the supported range")
	}
	return value, nil
}

func configuredSafetyMode() (string, error) {
	value := strings.ToUpper(strings.TrimSpace(appcommon.GetEnv("COHERE_SAFETY_SETTING", "NONE")))
	switch value {
	case "", "NONE":
		return "", nil
	case "CONTEXTUAL", "STRICT":
		return value, nil
	default:
		return "", errors.New("COHERE_SAFETY_SETTING must be NONE, CONTEXTUAL, or STRICT")
	}
}

type rerankRequest struct {
	Documents       []any  `json:"documents"`
	Query           string `json:"query"`
	Model           string `json:"model"`
	TopN            int    `json:"top_n"`
	ReturnDocuments bool   `json:"return_documents"`
}

func convertRerankRequest(meta *relaycommon.Meta) (*rerankRequest, error) {
	raw, err := protocolkit.MarshalJSON(meta.Request.Extra)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI rerank request: %w", err)
	}
	var incoming struct {
		Documents []any  `json:"documents"`
		Query     string `json:"query"`
		TopN      *int   `json:"top_n"`
	}
	if err := protocolkit.UnmarshalJSON(raw, &incoming); err != nil {
		return nil, fmt.Errorf("decode OpenAI rerank request: %w", err)
	}
	if strings.TrimSpace(incoming.Query) == "" {
		return nil, errors.New("Cohere rerank query is empty")
	}
	if len(incoming.Documents) == 0 {
		return nil, errors.New("Cohere rerank documents are empty")
	}
	if len(incoming.Documents) > maxCohereRerankDocuments {
		return nil, fmt.Errorf("Cohere rerank documents exceed %d entries", maxCohereRerankDocuments)
	}
	topN := 1
	if incoming.TopN != nil && *incoming.TopN > 0 {
		topN = *incoming.TopN
	}
	if int64(topN) > appcommon.MaxQuota {
		return nil, errors.New("Cohere top_n is outside the supported range")
	}
	return &rerankRequest{
		Documents: incoming.Documents,
		Query:     incoming.Query,
		Model:     meta.ModelName,
		TopN:      topN,
		// Cohere must return the documents because the client-facing rerank
		// response contract includes them when the provider supplies them.
		ReturnDocuments: true,
	}, nil
}

type billedUnits struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type responseMeta struct {
	BilledUnits billedUnits `json:"billed_units"`
}

type chatResult struct {
	ResponseID   string       `json:"response_id"`
	FinishReason string       `json:"finish_reason,omitempty"`
	Text         string       `json:"text"`
	Meta         responseMeta `json:"meta"`
}

type streamEvent struct {
	IsFinished   bool        `json:"is_finished"`
	EventType    string      `json:"event_type"`
	Text         string      `json:"text,omitempty"`
	FinishReason string      `json:"finish_reason,omitempty"`
	Message      string      `json:"message,omitempty"`
	Response     *chatResult `json:"response,omitempty"`
}

type rerankResult struct {
	Document       any     `json:"document,omitempty"`
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type rerankResultEnvelope struct {
	Results []rerankResult `json:"results"`
	Meta    responseMeta   `json:"meta"`
}

func chatResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Cohere response: %w", err)
	}
	var upstream chatResult
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode Cohere response: %w", err)
	}
	usage, usageErr := usageFromBilledUnits(upstream.Meta.BilledUnits)
	if usageErr != nil {
		return usage, usageErr
	}
	out := protocolkit.ChatCompletionsResponse{
		Id:      upstream.ResponseID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   meta.ModelName,
		Choices: []protocolkit.ChatCompletionsChoice{{
			Index: 0,
			Message: &protocolkit.ChatResponseMessage{
				Role: "assistant", Content: upstream.Text,
			},
			FinishReason: mapFinishReason(upstream.FinishReason),
		}},
		Usage: usage,
	}
	return writeJSONResponse(c, resp.StatusCode, out, usage)
}

func rerankResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Cohere rerank response: %w", err)
	}
	var upstream rerankResultEnvelope
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode Cohere rerank response: %w", err)
	}
	var usage *protocolkit.Usage
	if upstream.Meta.BilledUnits.InputTokens == 0 {
		promptTokens := meta.PromptTokens
		if promptTokens == 0 {
			promptTokens, err = estimateRerankPromptTokens(meta.Request)
			if err != nil {
				return nil, err
			}
		}
		usage, err = fallbackUsage(promptTokens, 0)
	} else {
		usage, err = usageFromBilledUnits(upstream.Meta.BilledUnits)
	}
	if err != nil {
		return usage, err
	}
	out := struct {
		Results []rerankResult     `json:"results"`
		Usage   *protocolkit.Usage `json:"usage"`
	}{Results: upstream.Results, Usage: usage}
	return writeJSONResponse(c, resp.StatusCode, out, usage)
}

func estimateRerankPromptTokens(request *protocolkit.GeneralOpenAIRequest) (int, error) {
	if request == nil {
		return 0, errors.New("estimate Cohere rerank usage: request is nil")
	}
	query, _ := request.Extra["query"].(string)
	documents, _ := request.Extra["documents"].([]any)
	var combined strings.Builder
	appendText := func(text string) error {
		additional := len(text)
		if combined.Len() > 0 {
			additional++
		}
		if additional > maxCohereRequestBodyBytes-combined.Len() {
			return errors.New("estimate Cohere rerank usage: input exceeds supported size")
		}
		if combined.Len() > 0 {
			combined.WriteByte('\n')
		}
		combined.WriteString(text)
		return nil
	}
	for _, document := range documents {
		if err := appendText(fmt.Sprint(document)); err != nil {
			return 0, err
		}
	}
	if query != "" {
		if err := appendText(query); err != nil {
			return 0, err
		}
	}
	tokens := relaycommon.CountTokens(combined.String())
	if tokens < 0 || int64(tokens) > appcommon.MaxQuota {
		return 0, errors.New("estimated Cohere rerank usage is outside the supported range")
	}
	return tokens, nil
}

func chatStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	responseID := cohereStreamResponseIDPrefix + appcommon.BestEffortRandomAlphanumeric(24)
	created := time.Now().Unix()
	var usage *protocolkit.Usage
	var contentBytes int64
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" || line == "[DONE]" {
			continue
		}
		var event streamEvent
		if err := protocolkit.UnmarshalJSON([]byte(line), &event); err != nil {
			// Cohere v1 streams one JSON object per line. The reference skips
			// malformed provider lines instead of reflecting them to clients.
			continue
		}
		if event.EventType == "error" {
			return cohereStreamUsage(usage, meta.PromptTokens, contentBytes), cohereStreamError(event.Message)
		}
		if event.IsFinished {
			if event.Response != nil {
				var err error
				usage, err = usageFromBilledUnits(event.Response.Meta.BilledUnits)
				if err != nil {
					return usage, err
				}
			}
			finishReason := event.FinishReason
			if finishReason == "" && event.Response != nil {
				finishReason = event.Response.FinishReason
			}
			finishReason = mapFinishReason(finishReason)
			if err := writeChatChunk(c, responseID, created, meta.ModelName,
				protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &finishReason); err != nil {
				return cohereStreamUsage(usage, meta.PromptTokens, contentBytes), err
			}
			continue
		}
		var err error
		contentBytes, err = addCohereStreamTextBytes(contentBytes, len(event.Text))
		if err != nil {
			return cohereStreamUsage(usage, meta.PromptTokens, contentBytes), err
		}
		if err := writeChatChunk(c, responseID, created, meta.ModelName,
			protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant", Content: event.Text}, nil); err != nil {
			return cohereStreamUsage(usage, meta.PromptTokens, contentBytes), err
		}
	}
	if err := scanner.Err(); err != nil {
		if usage == nil || usage.PromptTokens == 0 {
			usage, _ = fallbackUsage(meta.PromptTokens, contentBytes)
		}
		return usage, fmt.Errorf("read Cohere event stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if usage == nil || usage.PromptTokens == 0 {
		var err error
		usage, err = fallbackUsage(meta.PromptTokens, contentBytes)
		if err != nil {
			return usage, err
		}
	}
	if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
		return usage, fmt.Errorf("write Cohere stream terminator: %w", err)
	}
	c.Writer.Flush()
	return usage, nil
}

func addCohereStreamTextBytes(current int64, additional int) (int64, error) {
	if current < 0 || additional < 0 || current > maxCohereStreamTextBytes-int64(additional) {
		return current, fmt.Errorf("Cohere streamed text exceeds %d bytes", maxCohereStreamTextBytes)
	}
	return current + int64(additional), nil
}

func cohereStreamUsage(current *protocolkit.Usage, promptTokens int, contentBytes int64) *protocolkit.Usage {
	if current != nil {
		return current
	}
	usage, _ := fallbackUsage(promptTokens, contentBytes)
	return usage
}

func writeChatChunk(
	c *gin.Context,
	responseID string,
	created int64,
	model string,
	delta protocolkit.ChatCompletionsStreamResponseChoiceDelta,
	finishReason *string,
) error {
	chunk := protocolkit.ChatCompletionsStreamResponse{
		Id: responseID, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
			Index: 0, Delta: delta, FinishReason: finishReason,
		}},
	}
	body, err := protocolkit.MarshalJSON(chunk)
	if err != nil {
		return fmt.Errorf("encode Cohere stream chunk: %w", err)
	}
	if _, err := c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
		return fmt.Errorf("write Cohere stream chunk: %w", err)
	}
	c.Writer.Flush()
	return nil
}

func writeJSONResponse(c *gin.Context, status int, payload any, usage *protocolkit.Usage) (*protocolkit.Usage, error) {
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return usage, fmt.Errorf("encode Cohere client response: %w", err)
	}
	c.Status(status)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return usage, fmt.Errorf("write Cohere client response: %w", err)
	}
	return usage, nil
}

func usageFromBilledUnits(units billedUnits) (*protocolkit.Usage, error) {
	usage := &protocolkit.Usage{
		PromptTokens: units.InputTokens, CompletionTokens: units.OutputTokens,
	}
	total := int64(units.InputTokens) + int64(units.OutputTokens)
	if units.InputTokens < 0 || units.OutputTokens < 0 ||
		int64(units.InputTokens) > appcommon.MaxQuota || int64(units.OutputTokens) > appcommon.MaxQuota ||
		total < 0 || total > appcommon.MaxQuota {
		usage.TotalTokens = -1
		return usage, errors.New("Cohere billed token usage is outside the supported range")
	}
	usage.TotalTokens = int(total)
	return usage, nil
}

func fallbackUsage(promptTokens int, contentBytes int64) (*protocolkit.Usage, error) {
	usage := &protocolkit.Usage{PromptTokens: promptTokens}
	if promptTokens < 0 || int64(promptTokens) > appcommon.MaxQuota || contentBytes < 0 ||
		contentBytes > appcommon.MaxQuota*4-3 {
		usage.TotalTokens = -1
		return usage, errors.New("Cohere estimated token usage is outside the supported range")
	}
	completionTokens := (contentBytes + 3) / 4
	total := int64(promptTokens) + completionTokens
	if total > appcommon.MaxQuota {
		usage.CompletionTokens = int(completionTokens)
		usage.TotalTokens = -1
		return usage, errors.New("Cohere estimated token usage is outside the supported range")
	}
	usage.CompletionTokens = int(completionTokens)
	usage.TotalTokens = int(total)
	return usage, nil
}

func mapFinishReason(reason string) string {
	switch reason {
	case "COMPLETE":
		return "stop"
	case "MAX_TOKENS":
		return "max_tokens"
	default:
		return reason
	}
}

func cohereHTTPError(resp *http.Response, meta *relaycommon.Meta) error {
	err := relaycommon.HandleErrorResponse(resp)
	var upstream *relaycommon.UpstreamError
	if !errors.As(err, &upstream) || upstream == nil {
		return err
	}
	copyOfError := *upstream
	if copyOfError.Cause == nil && copyOfError.Body != "" {
		var payload struct {
			Message string `json:"message"`
			Error   any    `json:"error"`
		}
		if protocolkit.UnmarshalJSON([]byte(copyOfError.Body), &payload) == nil &&
			payload.Error == nil && payload.Message != "" {
			body, marshalErr := protocolkit.MarshalJSON(gin.H{"error": protocolkit.OpenAIError{
				Message: payload.Message, Type: "upstream_error", Code: "cohere_error",
			}})
			if marshalErr == nil {
				copyOfError.Body = string(body)
			}
		}
	}
	copyOfError.StatusCode = mappedStatusCode(meta, copyOfError.StatusCode)
	return &copyOfError
}

func cohereStreamError(message string) error {
	if strings.TrimSpace(message) == "" {
		message = "Cohere stream returned an error"
	}
	if int64(len(message)) > relaycommon.MaxUpstreamErrorBodyBytes {
		return &relaycommon.UpstreamError{
			StatusCode: http.StatusBadGateway,
			Cause: fmt.Errorf("%w: maximum is %d bytes",
				relaycommon.ErrUpstreamResponseTooLarge, relaycommon.MaxUpstreamErrorBodyBytes),
		}
	}
	body, err := protocolkit.MarshalJSON(gin.H{"error": protocolkit.OpenAIError{
		Message: message, Type: "upstream_error", Code: "cohere_error",
	}})
	if err != nil {
		return &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Cause: err}
	}
	if int64(len(body)) > relaycommon.MaxUpstreamErrorBodyBytes {
		return &relaycommon.UpstreamError{
			StatusCode: http.StatusBadGateway,
			Cause: fmt.Errorf("%w: maximum is %d bytes",
				relaycommon.ErrUpstreamResponseTooLarge, relaycommon.MaxUpstreamErrorBodyBytes),
		}
	}
	return &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Body: string(body)}
}

func mappedStatusCode(meta *relaycommon.Meta, status int) int {
	if meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.StatusCodeMapping) == "" {
		return status
	}
	var mapping map[string]any
	if protocolkit.UnmarshalJSON([]byte(meta.Channel.StatusCodeMapping), &mapping) != nil {
		return status
	}
	value, found := mapping[strconv.Itoa(status)]
	if !found {
		return status
	}
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) && typed >= 100 && typed <= 599 {
			return int(typed)
		}
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil && parsed >= 100 && parsed <= 599 {
			return parsed
		}
	}
	return status
}

func validateMode(mode constant.RelayMode, stream bool) error {
	switch mode {
	case constant.RelayModeChatCompletions:
		return nil
	case constant.RelayModeRerank:
		if stream {
			return errors.New("Cohere rerank does not support streaming")
		}
		return nil
	default:
		return fmt.Errorf("Cohere channel does not support relay mode %d", mode)
	}
}

func validateModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("Cohere upstream model is empty")
	}
	if model != strings.TrimSpace(model) || len(model) > maxCohereModelBytes || strings.ContainsAny(model, "\r\n\x00") {
		return errors.New("Cohere upstream model is invalid")
	}
	return nil
}

func validateBaseURL(raw string) error {
	if len(raw) > 4096 {
		return errors.New("Cohere base URL is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Cohere base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Cohere base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Cohere base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Cohere base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Cohere base URL must not contain a query or fragment")
	}
	return nil
}
