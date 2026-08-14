// Package relaycommon holds the shared types and helpers used by both the relay
// engine and provider adapters. It has no dependency on the adapters, breaking
// the import cycle between the engine and the per-provider channel packages.
package relaycommon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"

	"github.com/tiktoken-go/tokenizer"
)

// Meta carries the per-request state handed to a provider adapter.
type Meta struct {
	Channel      *model.Channel
	Mode         constant.RelayMode
	Format       constant.RelayFormat
	ModelName    string // mapped upstream model name
	BaseURL      string
	APIKey       string
	Request      *protocolkit.GeneralOpenAIRequest
	IsStream     bool
	PromptTokens int
}

// Adaptor is the provider adapter contract.
type Adaptor interface {
	Init(meta *Meta)
	GetRequestURL(meta *Meta) (string, error)
	SetupRequestHeader(req *http.Request, meta *Meta) error
	ConvertRequest(meta *Meta) ([]byte, error)
	DoResponse(c *gin.Context, resp *http.Response, meta *Meta) (*protocolkit.Usage, error)
}

// GetRelayFormat determines the wire protocol family for a channel type.
func GetRelayFormat(channelType constant.ChannelType, mode constant.RelayMode) constant.RelayFormat {
	switch channelType {
	case constant.ChannelTypeAnthropic:
		return constant.RelayFormatClaude
	case constant.ChannelTypeGemini, constant.ChannelTypeVertexAi:
		if mode == constant.RelayModeEmbeddings {
			return constant.RelayFormatEmbedding
		}
		return constant.RelayFormatGemini
	}
	switch mode {
	case constant.RelayModeResponses:
		return constant.RelayFormatOpenAIResponses
	case constant.RelayModeResponsesCompact:
		return constant.RelayFormatOpenAIResponsesCompaction
	case constant.RelayModeRealtime:
		return constant.RelayFormatOpenAIRealtime
	case constant.RelayModeAudioSpeech, constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
		return constant.RelayFormatOpenAIAudio
	case constant.RelayModeImagesGenerations, constant.RelayModeImagesEdits:
		return constant.RelayFormatOpenAIImage
	case constant.RelayModeRerank:
		return constant.RelayFormatRerank
	case constant.RelayModeEmbeddings:
		return constant.RelayFormatEmbedding
	default:
		return constant.RelayFormatOpenAI
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
	StatusCode int
	Body       string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned status %d: %s", e.StatusCode, e.Body)
}

// IsRetryableUpstreamError reports whether an error is retryable. 5xx server
// errors and transport/dial errors are retryable; 4xx client errors are not.
func IsRetryableUpstreamError(err error) bool {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.StatusCode >= 500
	}
	return true
}

// HandleErrorResponse reads an upstream error body and returns it as an error.
// It does not write to the client; the caller decides whether to retry.
func HandleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
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
		return &UpstreamError{StatusCode: statusCode, Body: e.Message}
	}
	return &UpstreamError{StatusCode: statusCode, Body: string(body)}
}

// WriteUpstreamError writes an upstream error to the client as an
// OpenAI-compatible JSON error, passing through an upstream error body when it
// is already in the OpenAI error shape.
func WriteUpstreamError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	msg := err.Error()
	body := ""
	if ue, ok := err.(*UpstreamError); ok {
		status = ue.StatusCode
		body = ue.Body
		msg = ue.Body
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
		return generic.Usage
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
