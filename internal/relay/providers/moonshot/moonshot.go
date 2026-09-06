// Package moonshot implements the Moonshot/Kimi OpenAI-compatible wire
// contract. Moonshot also exposes an Anthropic Messages endpoint; its URL and
// bearer-header contract live here for the relay's native-Claude dispatcher.
package moonshot

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/claude"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	ChannelName        = "moonshot"
	defaultBaseURL     = "https://api.moonshot.cn"
	kimiCodingPlanBase = "kimi-coding-plan"
	kimiOpenAIBaseURL  = "https://api.kimi.com/coding/v1"
	kimiClaudeBaseURL  = "https://api.kimi.com/coding"
)

var supportedModels = [...]string{
	"kimi-k2.5",
	"kimi-k2-0905-preview",
	"kimi-k2-turbo-preview",
	"kimi-k2-thinking",
	"kimi-k2-thinking-turbo",
}

// ModelList returns a copy so callers cannot mutate the provider catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// Adaptor implements Moonshot's OpenAI-compatible endpoints and the
// OpenAI-to-Anthropic conversion used when an OpenAI request is deliberately
// sent to Moonshot's Anthropic endpoint.
type Adaptor struct {
	mode   channelcatalog.RelayMode
	format channelcatalog.RelayFormat
	openAI openai.Adaptor
	claude claude.Adaptor
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if meta == nil {
		return
	}
	a.mode = meta.Mode
	a.format = meta.Format
	a.openAI.Init(meta)
	a.claude.Init(meta)
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", errors.New("Moonshot relay metadata is nil")
	}
	if err := validateMode(a.mode, a.relayFormat(meta)); err != nil {
		return "", err
	}

	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if base == kimiCodingPlanBase {
		if a.relayFormat(meta) == channelcatalog.RelayFormatClaude {
			return relaycommon.JoinURL(kimiClaudeBaseURL, "/v1/messages"), nil
		}
		return joinOpenAIPath(kimiOpenAIBaseURL, moonshotOpenAIPath(a.mode)), nil
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	if a.relayFormat(meta) == channelcatalog.RelayFormatClaude {
		return relaycommon.JoinURL(base, "/anthropic/v1/messages"), nil
	}
	return joinOpenAIPath(base, moonshotOpenAIPath(a.mode)), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Moonshot request metadata is nil")
	}
	key := strings.TrimSpace(meta.APIKey)
	if key == "" {
		return errors.New("Moonshot API key is required")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Del("x-api-key")
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
		return nil, errors.New("Moonshot request is nil")
	}
	format := a.relayFormat(meta)
	if err := validateMode(a.mode, format); err != nil {
		return nil, err
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return nil, errors.New("Moonshot upstream model is empty")
	}
	if format == channelcatalog.RelayFormatClaude {
		return a.claude.ConvertRequest(meta)
	}

	body, err := a.openAI.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	if a.mode != channelcatalog.RelayModeChatCompletions || !strings.EqualFold(meta.ModelName, "kimi-k2.6") {
		return body, nil
	}
	var request map[string]any
	if err := protocolkit.UnmarshalJSON(body, &request); err != nil {
		return nil, fmt.Errorf("decode Moonshot request: %w", err)
	}
	if _, present := request["temperature"]; !present {
		return body, nil
	}
	request["temperature"] = 1.0
	body, err = protocolkit.MarshalJSON(request)
	if err != nil {
		return nil, fmt.Errorf("encode Moonshot request: %w", err)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, errors.New("Moonshot response metadata is nil")
	}
	if err := validateMode(a.mode, a.relayFormat(meta)); err != nil {
		return nil, err
	}
	// The shared OpenAI handler owns status-code mapping and bounded error
	// reflection for both Moonshot wire formats.
	if resp.StatusCode >= http.StatusBadRequest {
		return a.openAI.DoResponse(c, resp, meta)
	}
	if a.relayFormat(meta) == channelcatalog.RelayFormatClaude {
		return a.claude.DoResponse(c, resp, meta)
	}
	if meta.IsStream {
		observer := &streamCacheObserver{}
		copyOfResponse := *resp
		copyOfResponse.Body = io.NopCloser(io.TeeReader(resp.Body, observer))
		usage, err := a.openAI.DoResponse(c, &copyOfResponse, meta)
		if err != nil {
			return usage, err
		}
		observer.finish()
		applyCachedTokens(usage, observer.cachedTokens, observer.found)
		return usage, nil
	}

	limit := relaycommon.MaxUpstreamJSONBodyBytes
	if a.mode == channelcatalog.RelayModeEmbeddings {
		limit = relaycommon.MaxUpstreamLargeJSONBodyBytes
	}
	body, err := relaycommon.ReadUpstreamBody(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("read Moonshot response: %w", err)
	}
	body, extractedUsage, cachedTokens, cachedFound, err := normalizeNonStreamBody(body, meta, a.mode)
	if err != nil {
		return nil, err
	}
	copyOfResponse := *resp
	copyOfResponse.Body = io.NopCloser(bytes.NewReader(body))
	usage, err := a.openAI.DoResponse(c, &copyOfResponse, meta)
	if err != nil {
		return usage, err
	}
	if extractedUsage != nil {
		usage = extractedUsage
	}
	applyCachedTokens(usage, cachedTokens, cachedFound)
	return usage, nil
}

func (a *Adaptor) relayFormat(meta *relaycommon.Meta) channelcatalog.RelayFormat {
	if meta != nil && meta.Format != channelcatalog.RelayFormatUnknown {
		return meta.Format
	}
	return a.format
}

func validateMode(mode channelcatalog.RelayMode, format channelcatalog.RelayFormat) error {
	switch format {
	case channelcatalog.RelayFormatClaude:
		if mode != channelcatalog.RelayModeChatCompletions {
			return fmt.Errorf("Moonshot Anthropic endpoint does not support relay mode %d", mode)
		}
	case channelcatalog.RelayFormatOpenAI:
		if mode != channelcatalog.RelayModeChatCompletions && mode != channelcatalog.RelayModeCompletions {
			return fmt.Errorf("Moonshot OpenAI endpoint does not support relay mode %d", mode)
		}
	case channelcatalog.RelayFormatEmbedding:
		if mode != channelcatalog.RelayModeEmbeddings {
			return fmt.Errorf("Moonshot embedding endpoint does not support relay mode %d", mode)
		}
	case channelcatalog.RelayFormatRerank:
		if mode != channelcatalog.RelayModeRerank {
			return fmt.Errorf("Moonshot rerank endpoint does not support relay mode %d", mode)
		}
	default:
		return fmt.Errorf("Moonshot channel does not support relay format %q", format)
	}
	return nil
}

func moonshotOpenAIPath(mode channelcatalog.RelayMode) string {
	switch mode {
	case channelcatalog.RelayModeCompletions:
		return "/v1/completions"
	case channelcatalog.RelayModeEmbeddings:
		return "/v1/embeddings"
	case channelcatalog.RelayModeRerank:
		return "/v1/rerank"
	default:
		return "/v1/chat/completions"
	}
}

func joinOpenAIPath(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return relaycommon.JoinURL(base, path)
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Moonshot base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Moonshot base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Moonshot base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Moonshot base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Moonshot base URL must not contain a query or fragment")
	}
	return nil
}

type cacheUsageEnvelope struct {
	Usage *struct {
		PromptTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		CachedTokens         *int `json:"cached_tokens"`
		PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
	Choices []struct {
		Usage *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"usage"`
	} `json:"choices"`
}

func extractCachedTokens(body []byte) (int, bool) {
	var envelope cacheUsageEnvelope
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil {
		return 0, false
	}
	// Match the reference's Moonshot precedence. Standard non-zero prompt
	// details are already present on the decoded Usage and win in
	// applyCachedTokens. The input alias comes next, then Moonshot's nested
	// choices field, followed by the generic top-level fallbacks.
	if envelope.Usage != nil && envelope.Usage.InputTokensDetails != nil &&
		envelope.Usage.InputTokensDetails.CachedTokens != nil &&
		*envelope.Usage.InputTokensDetails.CachedTokens > 0 {
		return *envelope.Usage.InputTokensDetails.CachedTokens, true
	}
	for _, choice := range envelope.Choices {
		if choice.Usage != nil && choice.Usage.CachedTokens != nil && *choice.Usage.CachedTokens > 0 {
			return *choice.Usage.CachedTokens, true
		}
	}
	if envelope.Usage != nil {
		if envelope.Usage.PromptTokensDetails != nil && envelope.Usage.PromptTokensDetails.CachedTokens != nil {
			return *envelope.Usage.PromptTokensDetails.CachedTokens, true
		}
		if envelope.Usage.CachedTokens != nil {
			return *envelope.Usage.CachedTokens, true
		}
		if envelope.Usage.PromptCacheHitTokens != nil {
			return *envelope.Usage.PromptCacheHitTokens, true
		}
	}
	return 0, false
}

func applyCachedTokens(usage *protocolkit.Usage, cachedTokens int, found bool) {
	if usage == nil {
		return
	}
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens != 0 {
		return
	}
	if !found && usage.PromptCacheHitTokens > 0 {
		cachedTokens = usage.PromptCacheHitTokens
		found = true
	}
	if found && cachedTokens <= 0 {
		return
	}
	if !found {
		return
	}
	if usage.PromptTokensDetails == nil {
		usage.PromptTokensDetails = &protocolkit.InputTokenDetails{}
	}
	usage.PromptTokensDetails.CachedTokens = cachedTokens
}

func normalizeNonStreamBody(body []byte, meta *relaycommon.Meta, mode channelcatalog.RelayMode) ([]byte, *protocolkit.Usage, int, bool, error) {
	var envelope map[string]any
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil || envelope == nil {
		return body, nil, 0, false, nil
	}
	usage := relaycommon.ExtractUsageFromBody(body)
	usageRewritten := false
	if mode == channelcatalog.RelayModeChatCompletions || mode == channelcatalog.RelayModeCompletions {
		if usage == nil || usage.PromptTokens == 0 {
			completionTokens := 0
			if usage != nil {
				completionTokens = usage.CompletionTokens
			}
			if completionTokens == 0 {
				completionTokens = estimateCompletionTokens(body, mode)
			}
			usage = &protocolkit.Usage{
				PromptTokens:     meta.PromptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      meta.PromptTokens + completionTokens,
			}
			usageRewritten = true
		}
	}
	cachedTokens, cachedFound := extractCachedTokens(body)
	applyCachedTokens(usage, cachedTokens, cachedFound)
	if !usageRewritten {
		return body, usage, cachedTokens, cachedFound, nil
	}
	envelope["usage"] = usage
	normalized, err := protocolkit.MarshalJSON(envelope)
	if err != nil {
		return nil, nil, 0, false, fmt.Errorf("encode normalized Moonshot response: %w", err)
	}
	return normalized, usage, cachedTokens, cachedFound, nil
}

func estimateCompletionTokens(body []byte, mode channelcatalog.RelayMode) int {
	if mode == channelcatalog.RelayModeCompletions {
		var response protocolkit.OpenAITextResponse
		if protocolkit.UnmarshalJSON(body, &response) != nil {
			return 0
		}
		tokens := 0
		for _, choice := range response.Choices {
			tokens += relaycommon.CountTokens(choice.Text)
		}
		return tokens
	}
	var response protocolkit.ChatCompletionsResponse
	if protocolkit.UnmarshalJSON(body, &response) != nil {
		return 0
	}
	tokens := 0
	for _, choice := range response.Choices {
		if choice.Message == nil {
			continue
		}
		tokens += relaycommon.CountTokens(responseContentText(choice.Message.Content))
		tokens += relaycommon.CountTokens(choice.Message.ReasoningContent)
		tokens += relaycommon.CountTokens(choice.Message.Reasoning)
	}
	return tokens
}

func responseContentText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		var text strings.Builder
		for _, raw := range typed {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := part["text"].(string); ok {
				text.WriteString(value)
			}
		}
		return text.String()
	default:
		return ""
	}
}

// streamCacheObserver keeps only one bounded partial SSE line while observing
// Moonshot's non-standard choices[].usage.cached_tokens field. The shared
// OpenAI scanner remains authoritative for stream framing and oversize errors.
type streamCacheObserver struct {
	pending      []byte
	discardLine  bool
	cachedTokens int
	found        bool
}

func (o *streamCacheObserver) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			o.appendPartial(data)
			break
		}
		o.appendPartial(data[:newline])
		if !o.discardLine {
			o.observeLine(o.pending)
		}
		o.pending = o.pending[:0]
		o.discardLine = false
		data = data[newline+1:]
	}
	return written, nil
}

func (o *streamCacheObserver) appendPartial(data []byte) {
	if o.discardLine {
		return
	}
	if len(o.pending)+len(data) > relaycommon.MaxUpstreamSSEEventBytes {
		o.pending = o.pending[:0]
		o.discardLine = true
		return
	}
	o.pending = append(o.pending, data...)
}

func (o *streamCacheObserver) observeLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if cachedTokens, found := extractCachedTokens(payload); found {
		o.cachedTokens = cachedTokens
		o.found = true
	}
}

func (o *streamCacheObserver) finish() {
	if !o.discardLine && len(o.pending) > 0 {
		o.observeLine(o.pending)
	}
	o.pending = o.pending[:0]
}
