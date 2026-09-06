// Package minimax implements MiniMax's chat, image-generation, speech, and
// Anthropic-compatible Messages wire contracts.
package minimax

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/relay/channel/claude"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName                    = "minimax"
	defaultBaseURL                 = "https://api.minimax.chat"
	maxMiniMaxRequestBodyBytes     = 16 << 20
	maxMiniMaxCredentialBytes      = 16 << 10
	maxMiniMaxBaseURLBytes         = 8 << 10
	maxMiniMaxModelBytes           = 1024
	maxMiniMaxVoiceIDBytes         = 1024
	maxMiniMaxMediaURLBytes        = 8 << 10
	maxMiniMaxStreamBodyBytes      = 64 << 20
	maxMiniMaxStreamGeneratedBytes = 16 << 20
)

var supportedModels = [...]string{
	"abab6.5-chat",
	"abab6.5s-chat",
	"abab6-chat",
	"abab5.5-chat",
	"abab5.5s-chat",
	"MiniMax-M2.7",
	"MiniMax-M2.7-highspeed",
	"speech-2.5-hd-preview",
	"speech-2.5-turbo-preview",
	"speech-02-hd",
	"speech-02-turbo",
	"speech-01-hd",
	"speech-01-turbo",
	"MiniMax-M2.1",
	"MiniMax-M2.1-highspeed",
	"MiniMax-M2",
	"MiniMax-M2.5",
	"MiniMax-M2.5-highspeed",
	"image-01",
	"image-01-live",
}

// ModelList returns an owned copy of MiniMax's reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// Adaptor owns MiniMax's provider-specific endpoints and media envelopes. The
// shared OpenAI and Claude adapters are used only after this adapter has
// enforced MiniMax's narrower protocol/mode matrix.
type Adaptor struct {
	mode        constant.RelayMode
	format      constant.RelayFormat
	openAI      openai.Adaptor
	claude      claude.Adaptor
	audioFormat string
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	a.audioFormat = ""
	a.openAI = openai.Adaptor{}
	a.claude = claude.Adaptor{}
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
		return "", errors.New("MiniMax relay metadata is nil")
	}
	mode, format := a.relayMode(meta), a.relayFormat(meta)
	if err := validateContract(mode, format, meta.IsStream); err != nil {
		return "", err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	var path string
	switch format {
	case constant.RelayFormatClaude:
		path = "/anthropic/v1/messages"
	case constant.RelayFormatOpenAI:
		path = "/v1/text/chatcompletion_v2"
	case constant.RelayFormatOpenAIImage:
		path = "/v1/image_generation"
	case constant.RelayFormatOpenAIAudio:
		path = "/v1/t2a_v2"
	default:
		return "", fmt.Errorf("MiniMax channel does not support relay format %q", format)
	}
	return relaycommon.JoinURL(base, path), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("MiniMax request metadata is nil")
	}
	if err := validateContract(a.relayMode(meta), a.relayFormat(meta), meta.IsStream); err != nil {
		return err
	}
	key := strings.TrimSpace(meta.APIKey)
	if key == "" {
		return errors.New("MiniMax API key is required")
	}
	if len(key) > maxMiniMaxCredentialBytes || strings.ContainsAny(key, "\r\n") {
		return errors.New("MiniMax API key is invalid")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Del("x-api-key")
	req.Header.Del("api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("anthropic-version")
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
		return nil, errors.New("MiniMax request is nil")
	}
	mode, format := a.relayMode(meta), a.relayFormat(meta)
	if err := validateContract(mode, format, meta.IsStream); err != nil {
		return nil, err
	}
	if err := validateModel(meta.ModelName); err != nil {
		return nil, err
	}
	if len(meta.RawBody) > maxMiniMaxRequestBodyBytes {
		return nil, fmt.Errorf("MiniMax request exceeds %d bytes", maxMiniMaxRequestBodyBytes)
	}
	if meta.Request.Stream != meta.IsStream {
		return nil, errors.New("MiniMax request stream flag does not match relay metadata")
	}

	var body []byte
	var err error
	switch format {
	case constant.RelayFormatOpenAI:
		body, err = a.openAI.ConvertRequest(meta)
	case constant.RelayFormatClaude:
		body, err = a.claude.ConvertRequest(meta)
	case constant.RelayFormatOpenAIImage:
		body, err = a.convertImageRequest(meta)
	case constant.RelayFormatOpenAIAudio:
		body, err = a.convertSpeechRequest(meta)
	default:
		return nil, fmt.Errorf("MiniMax channel does not support relay format %q", format)
	}
	if err != nil {
		return nil, err
	}
	if len(body) > maxMiniMaxRequestBodyBytes {
		return nil, fmt.Errorf("MiniMax converted request exceeds %d bytes", maxMiniMaxRequestBodyBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, errors.New("MiniMax response metadata is nil")
	}
	mode, format := a.relayMode(meta), a.relayFormat(meta)
	if err := validateContract(mode, format, meta.IsStream); err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return a.openAI.DoResponse(c, resp, meta)
	}
	switch format {
	case constant.RelayFormatOpenAIImage:
		return a.imageResponse(c, resp, meta)
	case constant.RelayFormatOpenAIAudio:
		return a.speechResponse(c, resp, meta)
	case constant.RelayFormatClaude:
		return a.claude.DoResponse(c, resp, meta)
	case constant.RelayFormatOpenAI:
		if meta.IsStream {
			return a.chatStreamResponse(c, resp, meta)
		}
		return a.chatResponse(c, resp, meta)
	default:
		return nil, fmt.Errorf("MiniMax channel does not support relay format %q", format)
	}
}

func (a *Adaptor) relayMode(meta *relaycommon.Meta) constant.RelayMode {
	if meta != nil && meta.Mode != constant.RelayModeUnknown {
		return meta.Mode
	}
	return a.mode
}

func (a *Adaptor) relayFormat(meta *relaycommon.Meta) constant.RelayFormat {
	if meta != nil && meta.Format != constant.RelayFormatUnknown {
		return meta.Format
	}
	return a.format
}

func validateContract(mode constant.RelayMode, format constant.RelayFormat, stream bool) error {
	switch format {
	case constant.RelayFormatOpenAI, constant.RelayFormatClaude:
		if mode != constant.RelayModeChatCompletions {
			return fmt.Errorf("MiniMax text endpoint does not support relay mode %d", mode)
		}
	case constant.RelayFormatOpenAIImage:
		if mode != constant.RelayModeImagesGenerations {
			return fmt.Errorf("MiniMax image endpoint does not support relay mode %d", mode)
		}
		if stream {
			return errors.New("MiniMax image generation does not support streaming")
		}
	case constant.RelayFormatOpenAIAudio:
		if mode != constant.RelayModeAudioSpeech {
			return fmt.Errorf("MiniMax speech endpoint does not support relay mode %d", mode)
		}
		if stream {
			return errors.New("MiniMax speech relay does not support streaming")
		}
	default:
		return fmt.Errorf("MiniMax channel does not support relay format %q", format)
	}
	return nil
}

func validateModel(model string) error {
	if model == "" || strings.TrimSpace(model) != model {
		return errors.New("MiniMax upstream model is invalid")
	}
	if len(model) > maxMiniMaxModelBytes {
		return errors.New("MiniMax upstream model is invalid")
	}
	for _, character := range model {
		if unicode.IsControl(character) {
			return errors.New("MiniMax upstream model is invalid")
		}
	}
	return nil
}

func validateBaseURL(raw string) error {
	if raw == "" || len(raw) > maxMiniMaxBaseURLBytes {
		return errors.New("MiniMax base URL length is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse MiniMax base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("MiniMax base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("MiniMax base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("MiniMax base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("MiniMax base URL must not contain a query or fragment")
	}
	return nil
}

func miniMaxBusinessError(meta *relaycommon.Meta, status int, typ string, code any, message string) error {
	providerError := relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message,
		Type:    typ,
		Code:    fmt.Sprint(code),
	}, status)
	return mapMiniMaxStatus(meta, providerError)
}

func mapMiniMaxStatus(meta *relaycommon.Meta, err error) error {
	var upstream *relaycommon.UpstreamError
	if !errors.As(err, &upstream) || upstream == nil || meta == nil || meta.Channel == nil {
		return err
	}
	rawMapping := strings.TrimSpace(meta.Channel.StatusCodeMapping)
	if rawMapping == "" {
		return err
	}
	var mapping map[string]any
	if protocolkit.UnmarshalJSON([]byte(rawMapping), &mapping) != nil {
		return err
	}
	raw, exists := mapping[strconv.Itoa(upstream.StatusCode)]
	if !exists {
		return err
	}
	mapped, ok := miniMaxMappedStatus(raw)
	if !ok {
		return err
	}
	copyOfError := *upstream
	copyOfError.StatusCode = mapped
	return &copyOfError
}

func miniMaxMappedStatus(value any) (int, bool) {
	status := 0
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) || typed < 100 || typed > 599 {
			return 0, false
		}
		status = int(typed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		status = parsed
	default:
		return 0, false
	}
	return status, status >= 100 && status <= 599
}
