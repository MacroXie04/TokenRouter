// Package volcengine implements ByteDance VolcEngine Ark's OpenAI-compatible
// HTTP surface and its binary WebSocket text-to-speech protocol.
package volcengine

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
	"math"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ChannelName = "volcengine"

	defaultBaseURL       = "https://ark.cn-beijing.volces.com"
	codingPlanAlias      = "doubao-coding-plan"
	codingPlanOpenAIBase = "https://ark.cn-beijing.volces.com/api/coding/v3"
	codingPlanClaudeBase = "https://ark.cn-beijing.volces.com/api/coding"
	defaultTTSURL        = "wss://openspeech.bytedance.com/api/v1/tts/ws_binary"
	maxModelBytes        = 512
	maxCredentialBytes   = 16 << 10
)

var supportedModels = [...]string{
	"Doubao-pro-128k",
	"Doubao-pro-32k",
	"Doubao-pro-4k",
	"Doubao-lite-128k",
	"Doubao-lite-32k",
	"Doubao-lite-4k",
	"Doubao-embedding",
	"doubao-seedream-4-0-250828",
	"seedream-4-0-250828",
	"doubao-seedance-1-0-pro-250528",
	"seedance-1-0-pro-250528",
	"doubao-seed-1-6-thinking-250715",
	"seed-1-6-thinking-250715",
}

// ModelList returns an owned copy of the exact reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// IsCodingPlanBase reports whether base is the reference's symbolic Doubao
// coding-plan endpoint. It is exported for native Claude dispatch.
func IsCodingPlanBase(base string) bool {
	return strings.TrimSpace(base) == codingPlanAlias
}

// Adaptor implements the VolcEngine relay contract. HTTP responses delegate
// to the hardened shared OpenAI/Claude codecs; only the fixed TTS WebSocket
// endpoint uses DirectAdaptor.
type Adaptor struct {
	mode   channelcatalog.RelayMode
	format channelcatalog.RelayFormat
	openAI openai.Adaptor
	claude claude.Adaptor

	// DialContext is injectable only for deterministic protocol tests.
	DialContext TTSDialContextFunc
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)
var _ relaycommon.DirectAdaptor = (*Adaptor)(nil)

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
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	format := a.relayFormat(meta)
	if base == codingPlanAlias {
		if meta.Mode != channelcatalog.RelayModeChatCompletions {
			return "", errors.New("VolcEngine coding plan supports only chat completions")
		}
		if format == channelcatalog.RelayFormatClaude {
			return relaycommon.JoinURL(codingPlanClaudeBase, "/v1/messages"), nil
		}
		return relaycommon.JoinURL(codingPlanOpenAIBase, "/chat/completions"), nil
	}
	if meta.Mode == channelcatalog.RelayModeAudioSpeech && base == defaultBaseURL {
		return defaultTTSURL, nil
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	if format == channelcatalog.RelayFormatClaude {
		if strings.HasPrefix(meta.ModelName, "bot") {
			return relaycommon.JoinURL(base, "/api/v3/bots/chat/completions"), nil
		}
		return relaycommon.JoinURL(base, "/api/v3/chat/completions"), nil
	}
	var path string
	switch meta.Mode {
	case channelcatalog.RelayModeChatCompletions:
		if strings.HasPrefix(meta.ModelName, "bot") {
			path = "/api/v3/bots/chat/completions"
		} else {
			path = "/api/v3/chat/completions"
		}
	case channelcatalog.RelayModeEmbeddings:
		path = "/api/v3/embeddings"
	case channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits:
		path = "/api/v3/images/generations"
	case channelcatalog.RelayModeRerank:
		path = "/api/v3/rerank"
	case channelcatalog.RelayModeResponses:
		path = "/api/v3/responses"
	case channelcatalog.RelayModeAudioSpeech:
		path = "/v1/audio/speech"
	default:
		return "", fmt.Errorf("VolcEngine does not support relay mode %d", meta.Mode)
	}
	return relaycommon.JoinURL(base, path), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil || meta == nil {
		return errors.New("VolcEngine request metadata is nil")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if meta.Mode == channelcatalog.RelayModeAudioSpeech {
		_, token, err := parseTTSCredential(meta.APIKey)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer;"+token)
		return nil
	}
	key, err := validateBearerCredential(meta.APIKey)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Del("x-api-key")
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if meta.Mode == channelcatalog.RelayModeAudioSpeech {
		return buildTTSRequest(meta)
	}
	if a.relayFormat(meta) == channelcatalog.RelayFormatClaude {
		return a.claude.ConvertRequest(meta)
	}
	body, err := a.openAI.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(meta.ModelName, "deepseek") || !strings.HasSuffix(meta.ModelName, "-thinking") {
		return body, nil
	}
	var request map[string]any
	if err := protocolkit.UnmarshalJSON(body, &request); err != nil {
		return nil, fmt.Errorf("decode VolcEngine request: %w", err)
	}
	model := strings.TrimSuffix(meta.ModelName, "-thinking")
	if model == "" || len(model) > maxModelBytes {
		return nil, errors.New("VolcEngine upstream model is invalid")
	}
	meta.ModelName = model
	request["model"] = model
	request["thinking"] = map[string]any{"type": "enabled"}
	encoded, err := protocolkit.MarshalJSON(request)
	if err != nil {
		return nil, errors.New("encode VolcEngine request")
	}
	return encoded, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil || meta == nil {
		return nil, errors.New("VolcEngine response metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if meta.Mode == channelcatalog.RelayModeAudioSpeech {
		return handleTTSHTTPResponse(c, response, meta)
	}
	if a.relayFormat(meta) == channelcatalog.RelayFormatClaude {
		return a.claude.DoResponse(c, response, meta)
	}
	return a.openAI.DoResponse(c, response, meta)
}

func (a *Adaptor) relayFormat(meta *relaycommon.Meta) channelcatalog.RelayFormat {
	if meta != nil && meta.Format != channelcatalog.RelayFormatUnknown {
		return meta.Format
	}
	return a.format
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("VolcEngine relay metadata is nil")
	}
	if strings.TrimSpace(meta.ModelName) == "" || len(meta.ModelName) > maxModelBytes ||
		!utf8.ValidString(meta.ModelName) || hasUnsafeText(meta.ModelName) {
		return errors.New("VolcEngine upstream model is invalid")
	}
	format := a.relayFormat(meta)
	if format == channelcatalog.RelayFormatClaude {
		if meta.Mode != channelcatalog.RelayModeChatCompletions {
			return errors.New("VolcEngine Claude format supports only chat completions")
		}
		return nil
	}
	switch meta.Mode {
	case channelcatalog.RelayModeChatCompletions, channelcatalog.RelayModeEmbeddings,
		channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits,
		channelcatalog.RelayModeRerank, channelcatalog.RelayModeResponses,
		channelcatalog.RelayModeAudioSpeech:
		return nil
	default:
		return fmt.Errorf("VolcEngine does not support relay mode %d", meta.Mode)
	}
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse VolcEngine base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("VolcEngine base URL must use http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("VolcEngine base URL is invalid")
	}
	return nil
}

func validateBearerCredential(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" || key != raw || len(key) > maxCredentialBytes || !utf8.ValidString(key) || hasUnsafeText(key) {
		return "", errors.New("VolcEngine API key is invalid")
	}
	return key, nil
}

func hasUnsafeText(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return true
		}
	}
	return false
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func equalJSON(left, right []byte) bool {
	var a, b any
	if protocolkit.UnmarshalJSON(left, &a) != nil || protocolkit.UnmarshalJSON(right, &b) != nil {
		return false
	}
	encodedA, errA := protocolkit.MarshalJSON(a)
	encodedB, errB := protocolkit.MarshalJSON(b)
	return errA == nil && errB == nil && bytes.Equal(encodedA, encodedB)
}
