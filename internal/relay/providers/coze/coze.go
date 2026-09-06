// Package coze implements Coze's bot-oriented v3 chat contract.
package coze

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ChannelName             = "coze"
	defaultBaseURL          = "https://api.coze.cn"
	maxBaseURLBytes         = 4 << 10
	maxCredentialBytes      = 16 << 10
	maxBotIDBytes           = 128
	maxUserIDBytes          = 256
	maxModelBytes           = 1024
	maxMessages             = 256
	maxMessageBytes         = 1 << 20
	maxPromptBytes          = 2 << 20
	maxRequestBodyBytes     = 2 << 20
	maxProviderErrorBytes   = 8 << 10
	maxProviderIDBytes      = 1024
	maxProviderResponseBody = 8 << 20
	maxStreamBodyBytes      = 64 << 20
	maxStreamTextBytes      = 16 << 20
	maxStreamEvents         = 1_000_000
)

var (
	supportedModels = [...]string{
		"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k", "Baichuan4",
		"abab6.5s-chat-pro", "glm-4-0520", "qwen-max", "deepseek-r1", "deepseek-v3",
		"deepseek-r1-distill-qwen-32b", "deepseek-r1-distill-qwen-7b", "step-1v-8k",
		"step-1.5v-mini", "Doubao-pro-32k", "Doubao-pro-256k", "Doubao-lite-128k",
		"Doubao-lite-32k", "Doubao-vision-lite-32k", "Doubao-vision-pro-32k",
		"Doubao-1.5-pro-vision-32k", "Doubao-1.5-lite-32k", "Doubao-1.5-pro-32k",
		"Doubao-1.5-thinking-pro", "Doubao-1.5-pro-256k",
	}
	botIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ModelList returns an owned copy of the reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

type Adaptor struct {
	mode            channelcatalog.RelayMode
	format          channelcatalog.RelayFormat
	pollClient      *http.Client
	pollInterval    time.Duration
	maxPollAttempts int
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
	if a.pollClient == nil {
		a.pollClient = newPollClient()
	}
	if a.pollInterval <= 0 {
		a.pollInterval = defaultPollInterval
	}
	if a.maxPollAttempts <= 0 {
		a.maxPollAttempts = defaultMaxPollAttempts
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base, err := validatedBaseURL(meta.BaseURL)
	if err != nil {
		return "", err
	}
	return relaycommon.JoinURL(base, "/v3/chat"), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Coze request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	credential, err := validatedCredential(meta.APIKey)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Del("api-key")
	request.Header.Del("x-api-key")
	request.Header.Del("x-goog-api-key")
	return nil
}

type enterMessage struct {
	Role        string `json:"role"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
}

type chatRequest struct {
	BotID              string         `json:"bot_id"`
	UserID             string         `json:"user_id"`
	AdditionalMessages []enterMessage `json:"additional_messages"`
	Stream             bool           `json:"stream,omitempty"`
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if len(meta.RawBody) > maxRequestBodyBytes {
		return nil, fmt.Errorf("Coze request exceeds %d bytes", maxRequestBodyBytes)
	}
	if len(strings.TrimSpace(string(meta.RawBody))) != 0 {
		if !isJSONContentType(meta.RequestContentType) {
			return nil, errors.New("Coze chat requires application/json")
		}
		if err := rejectDuplicateJSONKeys(meta.RawBody); err != nil {
			return nil, errors.New("Coze request contains invalid or duplicate JSON fields")
		}
	}
	if len(meta.Request.Tools) != 0 || meta.Request.ToolChoice != nil || meta.Request.FunctionCall != nil || meta.Request.Functions != nil {
		return nil, errors.New("Coze v3 chat does not support OpenAI tool calls")
	}
	if meta.Request.ResponseFormat != nil {
		return nil, errors.New("Coze v3 chat does not support response_format")
	}
	if meta.Request.N != nil && *meta.Request.N != 1 {
		return nil, errors.New("Coze v3 chat supports exactly one response")
	}

	messages := make([]enterMessage, 0, len(meta.Request.Messages))
	totalBytes := 0
	for _, message := range meta.Request.Messages {
		if message.Role != "user" {
			// The reference bot contract deliberately sends only user turns; the
			// bot owns its own system instructions and conversation history.
			continue
		}
		content, err := textContent(message.Content)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(content) == "" || len(content) > maxMessageBytes || !utf8.ValidString(content) {
			return nil, errors.New("Coze user message is empty, oversized, or invalid UTF-8")
		}
		if len(messages) >= maxMessages || totalBytes > maxPromptBytes-len(content) {
			return nil, errors.New("Coze user messages exceed the configured bounds")
		}
		totalBytes += len(content)
		messages = append(messages, enterMessage{Role: "user", Content: content, ContentType: "text"})
	}
	if len(messages) == 0 {
		return nil, errors.New("Coze chat requires at least one user message")
	}
	userID := strings.TrimSpace(meta.Request.User)
	if userID == "" {
		generated, err := randomUserID()
		if err != nil {
			return nil, err
		}
		userID = generated
	}
	if len(userID) > maxUserIDBytes || !utf8.ValidString(userID) || strings.ContainsAny(userID, "\r\n\x00") {
		return nil, errors.New("Coze user identifier is invalid")
	}
	body, err := protocolkit.MarshalJSON(chatRequest{
		BotID: validatedBotID(meta), UserID: userID, AdditionalMessages: messages, Stream: meta.IsStream,
	})
	if err != nil {
		return nil, errors.New("encode Coze chat request")
	}
	if len(body) > maxRequestBodyBytes {
		return nil, fmt.Errorf("Coze request exceeds %d bytes", maxRequestBodyBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Coze response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if meta.IsStream {
		return a.convertStreamResponse(c, response, meta)
	}
	return a.convertBlockingResponse(c, response, meta)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil || meta.Channel == nil {
		return errors.New("Coze relay metadata is nil")
	}
	mode := a.mode
	if meta.Mode != channelcatalog.RelayModeUnknown {
		mode = meta.Mode
	}
	format := a.format
	if meta.Format != channelcatalog.RelayFormatUnknown {
		format = meta.Format
	}
	if mode != channelcatalog.RelayModeChatCompletions {
		return fmt.Errorf("Coze channel does not support relay mode %d", mode)
	}
	if format != channelcatalog.RelayFormatOpenAI {
		return fmt.Errorf("Coze channel does not support relay format %q", format)
	}
	if _, err := validatedCredential(meta.APIKey); err != nil {
		return err
	}
	if validatedBotID(meta) == "" {
		return errors.New("Coze channel requires a valid bot ID in channel other")
	}
	modelName := strings.TrimSpace(meta.ModelName)
	if modelName == "" || len(modelName) > maxModelBytes || !utf8.ValidString(modelName) || strings.ContainsAny(modelName, "\r\n\x00") {
		return errors.New("Coze mapped model is invalid")
	}
	return nil
}

func validatedBotID(meta *relaycommon.Meta) string {
	if meta == nil || meta.Channel == nil {
		return ""
	}
	botID := strings.TrimSpace(meta.Channel.Other)
	if botID == "" || len(botID) > maxBotIDBytes || !botIDPattern.MatchString(botID) {
		return ""
	}
	return botID
}

func validatedCredential(raw string) (string, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" || len(credential) > maxCredentialBytes || !utf8.ValidString(credential) || strings.ContainsAny(credential, "\r\n\x00") {
		return "", errors.New("Coze API key is invalid")
	}
	return credential, nil
}

func validatedBaseURL(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = defaultBaseURL
	}
	if len(base) > maxBaseURLBytes {
		return "", errors.New("Coze base URL is too long")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("Coze base URL must be a valid HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Coze base URL must not contain credentials, query, or fragment")
	}
	return strings.TrimRight(base, "/"), nil
}

func isJSONContentType(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && mediaType == "application/json"
}

func textContent(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []any:
		var builder strings.Builder
		for _, item := range typed {
			switch part := item.(type) {
			case map[string]any:
				kind, _ := part["type"].(string)
				text, ok := part["text"].(string)
				if kind != "text" || !ok {
					return "", errors.New("Coze supports only text message parts")
				}
				builder.WriteString(text)
			case protocolkit.MediaContent:
				if part.Type != protocolkit.ContentTypeText {
					return "", errors.New("Coze supports only text message parts")
				}
				builder.WriteString(part.Text)
			default:
				return "", errors.New("Coze supports only text message parts")
			}
		}
		return builder.String(), nil
	default:
		return "", errors.New("Coze user message content must be text")
	}
}

func randomUserID() (string, error) {
	return randomHexID("tokenrouter-")
}

func randomResponseID() (string, error) {
	return randomHexID("chatcmpl-coze-")
}

func randomHexID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate Coze identifier")
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
