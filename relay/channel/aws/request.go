package aws

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type bedrockClaudeRequest struct {
	AnthropicVersion  string                      `json:"anthropic_version"`
	AnthropicBeta     []string                    `json:"anthropic_beta,omitempty"`
	System            any                         `json:"system,omitempty"`
	Messages          []protocolkit.ClaudeMessage `json:"messages"`
	MaxTokens         int                         `json:"max_tokens"`
	Temperature       *float64                    `json:"temperature,omitempty"`
	TopP              *float64                    `json:"top_p,omitempty"`
	TopK              *int                        `json:"top_k,omitempty"`
	StopSequences     []string                    `json:"stop_sequences,omitempty"`
	Tools             any                         `json:"tools,omitempty"`
	ToolChoice        any                         `json:"tool_choice,omitempty"`
	ContextManagement json.RawMessage             `json:"context_management,omitempty"`
	Thinking          *protocolkit.Thinking       `json:"thinking,omitempty"`
	OutputConfig      json.RawMessage             `json:"output_config,omitempty"`
}

type nativeClaudeInput struct {
	Model             string                      `json:"model"`
	Prompt            string                      `json:"prompt,omitempty"`
	AnthropicVersion  string                      `json:"anthropic_version,omitempty"`
	AnthropicBeta     []string                    `json:"anthropic_beta,omitempty"`
	System            any                         `json:"system,omitempty"`
	Messages          []protocolkit.ClaudeMessage `json:"messages"`
	MaxTokens         int                         `json:"max_tokens"`
	Temperature       *float64                    `json:"temperature,omitempty"`
	TopP              *float64                    `json:"top_p,omitempty"`
	TopK              *int                        `json:"top_k,omitempty"`
	StopSequences     []string                    `json:"stop_sequences,omitempty"`
	Tools             any                         `json:"tools,omitempty"`
	ToolChoice        any                         `json:"tool_choice,omitempty"`
	ContextManagement json.RawMessage             `json:"context_management,omitempty"`
	Thinking          *protocolkit.Thinking       `json:"thinking,omitempty"`
	OutputConfig      json.RawMessage             `json:"output_config,omitempty"`
	Metadata          any                         `json:"metadata,omitempty"`
	CacheControl      any                         `json:"cache_control,omitempty"`
	Stream            bool                        `json:"stream,omitempty"`
}

func (a *Adaptor) convertClaudeRequest(meta *relaycommon.Meta) ([]byte, error) {
	passThrough, err := passThroughEnabled(meta)
	if err != nil {
		return nil, err
	}
	if passThrough {
		return a.convertClaudePassThrough(meta)
	}

	format := a.format
	if format == "" {
		format = meta.Format
	}
	var request bedrockClaudeRequest
	switch format {
	case "openai":
		request, err = a.claudeFromOpenAI(meta)
	case "claude":
		request, err = a.claudeFromNative(meta)
	default:
		err = errors.New("AWS Claude request format is unsupported")
	}
	if err != nil {
		return nil, err
	}
	request.AnthropicVersion = AnthropicVersion
	if beta, present, betaErr := anthropicBetaHeader(meta.ClientHeaders); betaErr != nil {
		return nil, betaErr
	} else if present {
		request.AnthropicBeta = beta
	}
	if err := a.validateAndNormalizeClaudeRequest(meta, &request); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func (a *Adaptor) claudeFromOpenAI(meta *relaycommon.Meta) (bedrockClaudeRequest, error) {
	if meta.Request == nil {
		return bedrockClaudeRequest{}, errors.New("AWS OpenAI request is nil")
	}
	if err := ensureOriginalModel(meta); err != nil {
		return bedrockClaudeRequest{}, err
	}
	converted := protocolkit.OpenAIRequestToClaudeRequest(meta.Request)
	converted.Model = relaycommon.PrepareClaudeRequest(converted, meta.OriginalModelName, meta.ModelName,
		meta.Request.MaxTokens != nil || meta.Request.MaxCompletionTokens != nil)
	request := bedrockClaudeRequest{
		System: converted.System, Messages: converted.Messages, MaxTokens: converted.MaxTokens,
		Temperature: converted.Temperature, TopP: converted.TopP, StopSequences: converted.StopSequences,
		Tools: converted.Tools, ToolChoice: converted.ToolChoice, Thinking: converted.Thinking,
		OutputConfig: converted.OutputConfig,
	}
	if len(converted.Tools) == 0 {
		request.Tools = nil
	}
	if value, ok := meta.Request.Extra["top_k"]; ok {
		topK, valid := exactJSONInt(value)
		if !valid {
			return bedrockClaudeRequest{}, errors.New("AWS Claude top_k must be an integer")
		}
		request.TopK = &topK
	}
	if err := copyOptionalField(meta.Request.Extra, "thinking", &request.Thinking); err != nil {
		return bedrockClaudeRequest{}, err
	}
	if err := copyRawOptionalField(meta.Request.Extra, "context_management", &request.ContextManagement); err != nil {
		return bedrockClaudeRequest{}, err
	}
	if err := copyRawOptionalField(meta.Request.Extra, "output_config", &request.OutputConfig); err != nil {
		return bedrockClaudeRequest{}, err
	}
	if value, ok := meta.Request.Extra["anthropic_beta"]; ok {
		if err := decodeStringList(value, &request.AnthropicBeta); err != nil {
			return bedrockClaudeRequest{}, errors.New("AWS anthropic_beta must be a bounded string array")
		}
	}
	return request, nil
}

func (a *Adaptor) claudeFromNative(meta *relaycommon.Meta) (bedrockClaudeRequest, error) {
	if len(bytes.TrimSpace(meta.RawBody)) == 0 {
		return bedrockClaudeRequest{}, errors.New("AWS native Claude request body is empty")
	}
	var input nativeClaudeInput
	if err := strictJSON(meta.RawBody, &input, true); err != nil {
		return bedrockClaudeRequest{}, fmt.Errorf("AWS native Claude request is invalid: %w", err)
	}
	expected := strings.TrimSpace(meta.OriginalModelName)
	if expected == "" && meta.Request != nil {
		expected = strings.TrimSpace(meta.Request.Model)
	}
	if expected != "" && strings.TrimSpace(input.Model) != expected {
		return bedrockClaudeRequest{}, errors.New("AWS native Claude model does not match the selected model")
	}
	if input.Stream != meta.IsStream {
		return bedrockClaudeRequest{}, errors.New("AWS native Claude stream flag does not match relay metadata")
	}
	if input.Prompt != "" {
		return bedrockClaudeRequest{}, errors.New("AWS native Claude legacy prompt is unsupported")
	}
	return bedrockClaudeRequest{
		AnthropicBeta: input.AnthropicBeta, System: input.System, Messages: input.Messages,
		MaxTokens: input.MaxTokens, Temperature: input.Temperature, TopP: input.TopP, TopK: input.TopK,
		StopSequences: input.StopSequences, Tools: input.Tools, ToolChoice: input.ToolChoice,
		ContextManagement: input.ContextManagement, Thinking: input.Thinking, OutputConfig: input.OutputConfig,
	}, nil
}

func (a *Adaptor) convertClaudePassThrough(meta *relaycommon.Meta) ([]byte, error) {
	if len(bytes.TrimSpace(meta.RawBody)) == 0 {
		return nil, errors.New("AWS pass-through request body is empty")
	}
	var fields map[string]json.RawMessage
	if err := strictJSON(meta.RawBody, &fields, false); err != nil || fields == nil {
		return nil, errors.New("AWS pass-through request must be one JSON object")
	}
	modelRaw := fields["model"]
	if len(modelRaw) == 0 {
		return nil, errors.New("AWS pass-through model is missing")
	}
	if len(modelRaw) > 0 {
		var model string
		if err := json.Unmarshal(modelRaw, &model); err != nil {
			return nil, errors.New("AWS pass-through model is invalid")
		}
		expected := strings.TrimSpace(meta.OriginalModelName)
		if expected == "" && meta.Request != nil {
			expected = strings.TrimSpace(meta.Request.Model)
		}
		if expected != "" && strings.TrimSpace(model) != expected {
			return nil, errors.New("AWS pass-through model does not match the selected model")
		}
	}
	if streamRaw := fields["stream"]; len(streamRaw) > 0 {
		var stream bool
		if err := json.Unmarshal(streamRaw, &stream); err != nil || stream != meta.IsStream {
			return nil, errors.New("AWS pass-through stream flag does not match relay metadata")
		}
	} else if meta.IsStream {
		return nil, errors.New("AWS pass-through stream flag is missing")
	}
	var validated bedrockClaudeRequest
	if err := json.Unmarshal(meta.RawBody, &validated); err != nil {
		return nil, errors.New("AWS pass-through request is invalid")
	}
	validated.AnthropicVersion = AnthropicVersion
	if beta, present, err := anthropicBetaHeader(meta.ClientHeaders); err != nil {
		return nil, err
	} else if present {
		validated.AnthropicBeta = beta
	}
	if err := a.validateAndNormalizeClaudeRequest(meta, &validated); err != nil {
		return nil, err
	}
	validatedBody, err := json.Marshal(validated)
	if err != nil {
		return nil, errors.New("encode validated AWS pass-through request")
	}
	var validatedFields map[string]json.RawMessage
	if err := json.Unmarshal(validatedBody, &validatedFields); err != nil {
		return nil, errors.New("encode validated AWS pass-through fields")
	}
	delete(fields, "model")
	delete(fields, "stream")
	for _, name := range []string{
		"anthropic_version", "anthropic_beta", "system", "messages", "max_tokens", "temperature", "top_p", "top_k",
		"stop_sequences", "tools", "tool_choice", "context_management", "thinking", "output_config",
	} {
		delete(fields, name)
		if value, present := validatedFields[name]; present {
			fields[name] = value
		}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.New("encode AWS pass-through request")
	}
	return body, nil
}

func (a *Adaptor) validateAndNormalizeClaudeRequest(meta *relaycommon.Meta, request *bedrockClaudeRequest) error {
	if request == nil {
		return errors.New("AWS Claude request is nil")
	}
	if len(request.Messages) == 0 || len(request.Messages) > MaxMessages {
		return fmt.Errorf("AWS Claude messages must contain 1 to %d entries", MaxMessages)
	}
	if request.MaxTokens <= 0 || request.MaxTokens > MaxOutputTokens {
		return fmt.Errorf("AWS Claude max_tokens must be between 1 and %d", MaxOutputTokens)
	}
	if request.Temperature != nil && !finiteRange(*request.Temperature, 0, 1) {
		return errors.New("AWS Claude temperature is outside the supported range")
	}
	if request.TopP != nil && !finiteRange(*request.TopP, 0, 1) {
		return errors.New("AWS Claude top_p is outside the supported range")
	}
	if request.TopK != nil && (*request.TopK < 0 || *request.TopK > 500) {
		return errors.New("AWS Claude top_k is outside the supported range")
	}
	stops, err := stopSequences(request.StopSequences)
	if err != nil {
		return err
	}
	request.StopSequences = stops
	if err := validateBetaValues(request.AnthropicBeta); err != nil {
		return err
	}
	if countArray(request.Tools) > MaxTools {
		return fmt.Errorf("AWS Claude tools exceed %d entries", MaxTools)
	}
	for index := range request.Messages {
		message := &request.Messages[index]
		if message.Role != "user" && message.Role != "assistant" {
			return errors.New("AWS Claude message role is invalid")
		}
		if err := a.normalizeClaudeContent(meta, &message.Content); err != nil {
			return fmt.Errorf("AWS Claude message %d: %w", index, err)
		}
	}
	if request.Thinking != nil {
		if request.Thinking.Type == "" || request.Thinking.BudgetTokens < 0 || request.Thinking.BudgetTokens > MaxOutputTokens {
			return errors.New("AWS Claude thinking configuration is invalid")
		}
	}
	return nil
}

func (a *Adaptor) normalizeClaudeContent(meta *relaycommon.Meta, content *any) error {
	if content == nil {
		return errors.New("content is nil")
	}
	if text, ok := (*content).(string); ok {
		if !utf8.ValidString(text) || len(text) > MaxMessageTextBytes {
			return errors.New("message text is outside the supported range")
		}
		return nil
	}
	raw, err := json.Marshal(*content)
	if err != nil || int64(len(raw)) > MaxRequestBodyBytes {
		return errors.New("message content is invalid")
	}
	var parts []map[string]any
	if err := strictJSON(raw, &parts, false); err != nil || len(parts) == 0 || len(parts) > MaxContentParts {
		return fmt.Errorf("message content must contain 1 to %d parts", MaxContentParts)
	}
	for _, part := range parts {
		partType, _ := part["type"].(string)
		switch partType {
		case "text", "thinking":
			field := "text"
			if partType == "thinking" {
				field = "thinking"
			}
			text, ok := part[field].(string)
			if !ok || !utf8.ValidString(text) || len(text) > MaxMessageTextBytes {
				return errors.New("message text part is invalid")
			}
		case "image", "document":
			if err := a.normalizeClaudeSource(meta, part); err != nil {
				return err
			}
		case "tool_use", "tool_result":
			// Their nested values were already subjected to the global JSON
			// depth, node, key, number and text limits.
		default:
			return errors.New("message content part type is unsupported")
		}
	}
	*content = parts
	return nil
}

func (a *Adaptor) normalizeClaudeSource(meta *relaycommon.Meta, part map[string]any) error {
	source, ok := part["source"].(map[string]any)
	if !ok {
		return errors.New("media source is missing")
	}
	sourceType, _ := source["type"].(string)
	switch sourceType {
	case "base64":
		mediaType, ok := source["media_type"].(string)
		if !ok {
			return errors.New("media type is missing")
		}
		normalized, err := normalizeMediaType(mediaType)
		if err != nil {
			return err
		}
		if err := validateMediaPartType(part, normalized); err != nil {
			return err
		}
		data, ok := source["data"].(string)
		if !ok || validateBase64Media(data) != nil {
			return errors.New("media base64 is invalid or oversized")
		}
		source["media_type"] = normalized
		delete(source, "url")
	case "url":
		rawURL, _ := source["url"].(string)
		data, mediaType, err := a.fetchMedia(metaContext(meta), rawURL)
		if err != nil {
			return err
		}
		if err := validateMediaPartType(part, mediaType); err != nil {
			return err
		}
		source["type"] = "base64"
		source["media_type"] = mediaType
		source["data"] = base64.StdEncoding.EncodeToString(data)
		delete(source, "url")
	default:
		return errors.New("media source type is unsupported")
	}
	part["source"] = source
	return nil
}

func validateMediaPartType(part map[string]any, mediaType string) error {
	partType, _ := part["type"].(string)
	switch partType {
	case "image":
		if !strings.HasPrefix(mediaType, "image/") {
			return errors.New("AWS image content has a non-image media type")
		}
	case "document":
		if mediaType != "application/pdf" {
			return errors.New("AWS document content has an unsupported media type")
		}
	default:
		return errors.New("AWS media content part type is invalid")
	}
	return nil
}

func (a *Adaptor) fetchMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	if len(rawURL) == 0 || len(rawURL) > MaxBaseURLBytes || strings.ContainsAny(rawURL, "\r\n\x00") {
		return nil, "", errors.New("AWS media URL is invalid")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, "", errors.New("AWS media URL must be HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", errors.New("build AWS media request")
	}
	request.Header.Set("Accept", "image/*, application/pdf")
	client := a.MediaHTTPClient
	if client == nil {
		client = mediaHTTPClient()
	} else {
		bounded := *client
		if bounded.Timeout <= 0 || bounded.Timeout > mediaRequestTimeout {
			bounded.Timeout = mediaRequestTimeout
		}
		bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &bounded
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("AWS media source returned status %d", response.StatusCode)
	}
	data, err := relaycommon.ReadUpstreamBody(response.Body, MaxMediaBytes)
	if err != nil {
		return nil, "", fmt.Errorf("read AWS media source: %w", err)
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	mediaType, err := normalizeMediaType(contentType)
	if err != nil {
		return nil, "", err
	}
	return data, mediaType, nil
}

func metaContext(meta *relaycommon.Meta) context.Context {
	if meta != nil && meta.Context != nil {
		return meta.Context
	}
	return context.Background()
}

func anthropicBetaHeader(header http.Header) ([]string, bool, error) {
	if header == nil {
		return nil, false, nil
	}
	raw := header.Get("anthropic-beta")
	if raw == "" {
		return nil, false, nil
	}
	if len(raw) > MaxHeaderValueBytes || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, true, errors.New("AWS anthropic-beta header is invalid")
	}
	values := strings.Split(raw, ",")
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	if err := validateBetaValues(values); err != nil {
		return nil, true, err
	}
	return values, true, nil
}

func validateBetaValues(values []string) error {
	if len(values) > MaxBetaValues {
		return fmt.Errorf("AWS anthropic_beta exceeds %d entries", MaxBetaValues)
	}
	for _, value := range values {
		if value == "" || len(value) > MaxBetaValueBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("AWS anthropic_beta value is invalid")
		}
	}
	return nil
}

func copyOptionalField[T any](fields map[string]any, name string, destination *T) error {
	value, ok := fields[name]
	if !ok || value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil || strictJSON(raw, destination, true) != nil {
		return fmt.Errorf("AWS %s field is invalid", name)
	}
	return nil
}

func copyRawOptionalField(fields map[string]any, name string, destination *json.RawMessage) error {
	value, ok := fields[name]
	if !ok || value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil || rejectDuplicateJSONKeys(raw) != nil {
		return fmt.Errorf("AWS %s field is invalid", name)
	}
	*destination = append((*destination)[:0], raw...)
	return nil
}

func decodeStringList(value any, destination *[]string) error {
	raw, err := json.Marshal(value)
	if err != nil || strictJSON(raw, destination, true) != nil {
		return errors.New("invalid string list")
	}
	return validateBetaValues(*destination)
}

func countArray(value any) int {
	if value == nil {
		return 0
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return MaxTools + 1
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return MaxTools + 1
	}
	return len(entries)
}
