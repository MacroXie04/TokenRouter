// Package contract holds the shared types and helpers used by both the relay
// engine and provider adapters. It has no dependency on the adapters, breaking
// the import cycle between the engine and the per-provider channel packages.
package contract

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tiktoken-go/tokenizer"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Upstream response limits are deliberately independent of request limits.
// Text model responses are normally small, while embedding vectors and image
// base64 payloads can be substantially larger. Generated binary media gets a
// larger ceiling without allowing an upstream to exhaust process memory.
const (
	MaxUpstreamJSONBodyBytes      int64 = 16 << 20
	MaxUpstreamLargeJSONBodyBytes int64 = 64 << 20
	MaxUpstreamBinaryBodyBytes    int64 = 128 << 20
	MaxUpstreamErrorBodyBytes     int64 = 16 << 10
	MaxUpstreamSSEEventBytes            = 1 << 20
)

var ErrUpstreamResponseTooLarge = errors.New("upstream response body exceeds limit")
var ErrUpstreamTransportFailed = errors.New("upstream transport request failed")

// Meta carries the per-request state handed to a provider adapter.
type Meta struct {
	Context            context.Context
	Channel            *model.Channel
	Mode               channelcatalog.RelayMode
	Format             channelcatalog.RelayFormat
	RequestPath        string
	OriginalModelName  string // client-visible model name before channel mapping
	ModelName          string // mapped upstream model name
	BaseURL            string
	APIKey             string
	ClientHeaders      http.Header
	Request            *protocolkit.GeneralOpenAIRequest
	RawBody            []byte
	RequestContentType string
	APIVersion         string
	IsStream           bool
	PromptTokens       int
	ToolUsage          *ToolUsageHooks
}

// Adaptor is the provider adapter contract.
type Adaptor interface {
	Init(meta *Meta)
	GetRequestURL(meta *Meta) (string, error)
	SetupRequestHeader(req *http.Request, meta *Meta) error
	ConvertRequest(meta *Meta) ([]byte, error)
	DoResponse(c *gin.Context, resp *http.Response, meta *Meta) (*protocolkit.Usage, error)
}

// DirectAdaptor is implemented by providers whose native transport is not an
// HTTP POST. The ordinary relay lifecycle still owns request validation,
// channel selection, quota reservation, retries, settlement, and logging;
// only the credentialed network dispatch is delegated to the adapter.
type DirectAdaptor interface {
	DoDirectRequest(c *gin.Context, requestURL string, body []byte, meta *Meta) (*protocolkit.Usage, error)
}

// GetRelayFormat determines the wire protocol family for a channel type.
func GetRelayFormat(channelType channelcatalog.ChannelType, mode channelcatalog.RelayMode) channelcatalog.RelayFormat {
	switch channelType {
	case channelcatalog.ChannelTypeAnthropic:
		return channelcatalog.RelayFormatClaude
	case channelcatalog.ChannelTypeGemini, channelcatalog.ChannelTypeVertexAi:
		if mode == channelcatalog.RelayModeEmbeddings {
			return channelcatalog.RelayFormatEmbedding
		}
		return channelcatalog.RelayFormatGemini
	}
	switch mode {
	case channelcatalog.RelayModeResponses:
		return channelcatalog.RelayFormatOpenAIResponses
	case channelcatalog.RelayModeResponsesCompact:
		return channelcatalog.RelayFormatOpenAIResponsesCompaction
	case channelcatalog.RelayModeAlphaSearch:
		return channelcatalog.RelayFormatOpenAIAlphaSearch
	case channelcatalog.RelayModeRealtime:
		return channelcatalog.RelayFormatOpenAIRealtime
	case channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayModeAudioTranscription, channelcatalog.RelayModeAudioTranslation:
		return channelcatalog.RelayFormatOpenAIAudio
	case channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits:
		return channelcatalog.RelayFormatOpenAIImage
	case channelcatalog.RelayModeRerank:
		return channelcatalog.RelayFormatRerank
	case channelcatalog.RelayModeEmbeddings:
		return channelcatalog.RelayFormatEmbedding
	default:
		return channelcatalog.RelayFormatOpenAI
	}
}

// GetMappedModel applies a channel's model mapping to the requested model.
func GetMappedModel(channel *model.Channel, modelName string) string {
	if channel == nil || channel.ModelMapping == "" {
		return modelName
	}
	var mapping map[string]string
	if err := protocolkit.UnmarshalJSON([]byte(channel.ModelMapping), &mapping); err != nil || len(mapping) == 0 {
		return modelName
	}
	if mapped, ok := mapping[modelName]; ok && mapped != "" {
		return mapped
	}
	if wildcard, ok := mapping["*"]; ok && wildcard != "" {
		return wildcard
	}
	return modelName
}

// JoinURL joins a base URL and a request path, avoiding double slashes.
func JoinURL(base, path string) string {
	if base == "" {
		return path
	}
	if path == "" {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}

// UpstreamError is an error returned by an upstream provider call.
type UpstreamError struct {
	StatusCode     int
	Body           string
	Cause          error
	SkipRetry      bool
	NormalizedCode string
}

func (e *UpstreamError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("upstream returned status %d (%T)", e.StatusCode, e.Cause)
	}
	if int64(len(e.Body)) > MaxUpstreamErrorBodyBytes {
		return fmt.Sprintf("upstream returned status %d: %v", e.StatusCode, upstreamBodyTooLargeError())
	}
	// The body remains available to WriteUpstreamError for the client-facing
	// provider contract, but Error deliberately excludes it: lifecycle errors
	// are logged and an upstream body may contain echoed credentials.
	return fmt.Sprintf("upstream returned status %d", e.StatusCode)
}

func (e *UpstreamError) Unwrap() error { return e.Cause }

// IsRetryableUpstreamError reports whether an error is retryable. 5xx server
// errors and transport/dial errors are retryable; 4xx client errors are not.
func IsRetryableUpstreamError(err error) bool {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		if ue.SkipRetry {
			return false
		}
		return ue.StatusCode >= 500
	}
	return true
}

// ReadUpstreamBody buffers an upstream response up to maxBytes. It reads one
// extra byte so an exact-limit response is accepted while an oversized body is
// rejected without returning a partial payload.
func ReadUpstreamBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("read upstream response body: nil reader")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("read upstream response body: invalid limit %d", maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream response body: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrUpstreamResponseTooLarge, maxBytes)
	}
	return body, nil
}

func upstreamBodyTooLargeError() error {
	return fmt.Errorf("%w: maximum is %d bytes", ErrUpstreamResponseTooLarge, MaxUpstreamErrorBodyBytes)
}

// NewUpstreamSSEScanner incrementally scans an event stream while bounding a
// single line/event. The extra byte is scanner delimiter headroom, so a line
// of exactly MaxUpstreamSSEEventBytes is accepted. It never buffers the
// complete stream.
func NewUpstreamSSEScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxUpstreamSSEEventBytes+1)
	return scanner
}

// HandleErrorResponse reads a small, bounded upstream error body and returns
// it as an error. Oversized or unreadable bodies are discarded entirely so
// attacker-controlled prefixes cannot be reflected to clients or logs.
func HandleErrorResponse(resp *http.Response) error {
	if resp == nil {
		return errors.New("upstream response is nil")
	}
	body, err := ReadUpstreamBody(resp.Body, MaxUpstreamErrorBodyBytes)
	if err != nil {
		return &UpstreamError{StatusCode: resp.StatusCode, Cause: fmt.Errorf("read upstream error body: %w", err)}
	}
	return &UpstreamError{StatusCode: resp.StatusCode, Body: string(body)}
}

// UpstreamErrorFromOpenAI wraps an OpenAI error envelope as an UpstreamError
// so WriteUpstreamError relays it to the client unchanged.
func UpstreamErrorFromOpenAI(e protocolkit.OpenAIError, statusCode int) error {
	if statusCode == 0 {
		statusCode = http.StatusBadRequest
	}
	body, err := protocolkit.MarshalJSON(gin.H{"error": e})
	if err != nil {
		if int64(len(e.Message)) > MaxUpstreamErrorBodyBytes {
			return &UpstreamError{StatusCode: statusCode, Cause: upstreamBodyTooLargeError()}
		}
		return &UpstreamError{StatusCode: statusCode, Body: e.Message}
	}
	if int64(len(body)) > MaxUpstreamErrorBodyBytes {
		return &UpstreamError{StatusCode: statusCode, Cause: upstreamBodyTooLargeError()}
	}
	return &UpstreamError{StatusCode: statusCode, Body: string(body)}
}

// SanitizeUpstreamError returns an equivalent upstream error with credentials
// removed from its client-facing body. secrets should contain only values
// selected for the current channel; JSON OAuth bundles and the provider
// credential convention token|application-id are expanded so an upstream
// cannot leak only one component.
func SanitizeUpstreamError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream == nil || upstream.Body == "" {
		return err
	}
	copy := *upstream
	body := copy.Body
	fragments := upstreamSecretFragments(secrets)
	for _, fragment := range fragments {
		body = strings.ReplaceAll(body, fragment, "[REDACTED]")
	}
	copy.Body = logging.RedactSensitiveText(body)
	return &copy
}

// SanitizeTransportError discards net/http's credential-bearing request URL
// while preserving only cancellation/deadline classification needed by
// callers. Provider keys can legitimately live in a query string, so a raw
// *url.Error must never cross the relay boundary or enter logs.
func SanitizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrUpstreamTransportFailed, context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrUpstreamTransportFailed, context.Canceled)
	}
	return ErrUpstreamTransportFailed
}

func upstreamSecretFragments(values []string) []string {
	seen := make(map[string]struct{})
	var collect func(any, bool)
	collect = func(value any, sensitive bool) {
		switch typed := value.(type) {
		case string:
			trimmed := strings.TrimSpace(typed)
			if sensitive && len(trimmed) >= 4 {
				seen[trimmed] = struct{}{}
			}
		case []any:
			for _, item := range typed {
				collect(item, sensitive)
			}
		case map[string]any:
			for key, item := range typed {
				normalized := strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(strings.ToLower(strings.TrimSpace(key)))
				keySensitive := normalized == "authorization" || normalized == "password" || normalized == "passwd" ||
					normalized == "api_key" || normalized == "apikey" || normalized == "access_key" ||
					normalized == "access_token" || normalized == "refresh_token" || normalized == "id_token" ||
					normalized == "client_secret" || normalized == "private_key" || normalized == "secret" ||
					strings.HasSuffix(normalized, "_token") || strings.HasSuffix(normalized, "_secret")
				collect(item, sensitive || keySensitive)
			}
		}
	}
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if len(trimmed) >= 4 {
			seen[trimmed] = struct{}{}
		}
		for _, component := range strings.Split(trimmed, "|") {
			component = strings.TrimSpace(component)
			if len(component) >= 4 {
				seen[component] = struct{}{}
			}
		}
		var decoded any
		if protocolkit.UnmarshalJSON([]byte(trimmed), &decoded) == nil {
			collect(decoded, false)
		}
	}
	fragments := make([]string, 0, len(seen))
	for fragment := range seen {
		fragments = append(fragments, fragment)
	}
	// Replace longer values first so one credential that contains another does
	// not leave a recognizable suffix behind.
	sort.Slice(fragments, func(i, j int) bool { return len(fragments[i]) > len(fragments[j]) })
	return fragments
}

// WriteUpstreamError writes an upstream error to the client as an
// OpenAI-compatible JSON error, passing through an upstream error body when it
// is already in the OpenAI error shape.
func WriteUpstreamError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	msg := logging.RedactSensitiveText(err.Error())
	body := ""
	var ue *UpstreamError
	if errors.As(err, &ue) && ue != nil {
		status = ue.StatusCode
		if int64(len(ue.Body)) > MaxUpstreamErrorBodyBytes {
			msg = ue.Error()
		} else if ue.Cause != nil {
			msg = ue.Error()
		} else {
			body = logging.RedactSensitiveText(ue.Body)
			msg = body
		}
	}
	if body != "" {
		var parsed map[string]any
		if protocolkit.UnmarshalJSON([]byte(body), &parsed) == nil {
			if _, ok := parsed["error"]; ok {
				c.Status(status)
				c.Header("Content-Type", "application/json")
				_, _ = c.Writer.Write([]byte(body))
				return
			}
		}
	}
	c.JSON(status, gin.H{"error": protocolkit.OpenAIError{Message: msg, Type: "upstream_error", Code: "upstream_error"}})
}

// ExtractUsageFromBody extracts a usage object from any OpenAI-compatible body.
func ExtractUsageFromBody(body []byte) *protocolkit.Usage {
	var generic struct {
		Usage *protocolkit.Usage `json:"usage"`
	}
	if err := protocolkit.UnmarshalJSON(body, &generic); err == nil && generic.Usage != nil {
		return protocolkit.NormalizeOpenAIUsageAliases(generic.Usage)
	}
	return nil
}

var tiktokenEncoder tokenizer.Codec

func init() {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err == nil {
		tiktokenEncoder = enc
	}
}

// CountTokens estimates token count using tiktoken with a chars/4 fallback.
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	if tiktokenEncoder != nil {
		ids, _, err := tiktokenEncoder.Encode(text)
		if err == nil {
			return len(ids)
		}
	}
	return (len(text) + 3) / 4
}

// EstimatePromptTokens estimates the prompt token count for a request.
func EstimatePromptTokens(req *protocolkit.GeneralOpenAIRequest) int {
	if req == nil {
		return 0
	}
	total := 0
	for _, m := range req.Messages {
		total += CountTokens(MessageToText(m))
	}
	if s, ok := req.Prompt.(string); ok {
		total += CountTokens(s)
	}
	// Embedding requests carry the text in `input` (string or array).
	if req.Extra != nil {
		switch in := req.Extra["input"].(type) {
		case string:
			total += CountTokens(in)
		case []any:
			for _, item := range in {
				if s, ok := item.(string); ok {
					total += CountTokens(s)
				}
			}
		case []string:
			for _, s := range in {
				total += CountTokens(s)
			}
		}
	}
	return total
}

// MessageToText flattens a message's text content.
func MessageToText(m protocolkit.Message) string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, part := range c {
			if pm, ok := part.(protocolkit.MediaContent); ok && pm.Type == protocolkit.ContentTypeText {
				sb.WriteString(pm.Text)
			} else if pm, ok := part.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// EstimateStreamUsage builds a usage estimate for a stream without usage.
func EstimateStreamUsage(promptTokens, completionChars int) *protocolkit.Usage {
	completion := (completionChars + 3) / 4
	return &protocolkit.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completion,
		TotalTokens:      promptTokens + completion,
	}
}
