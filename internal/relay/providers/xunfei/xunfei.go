// Package xunfei implements the iFlytek Spark WebSocket chat contract.
package xunfei

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ChannelName = "xunfei"

	providerHost                      = "spark-api.xf-yun.com"
	maxCredentialComponentBytes       = 1 << 10
	maxRequestBodyBytes               = 4 << 20
	maxMessageCount                   = 1_024
	maxMessageTextBytes               = 1 << 20
	maxRequestTextBytes               = 2 << 20
	maxOutputTokens                   = 8_192
	maxTopK                           = 6
	maxResponseFrameBytes       int64 = 1 << 20
	maxResponseBodyBytes        int64 = 16 << 20
	maxResponseTextBytes              = 8 << 20
	maxResponseFrames                 = 4_096
	maxProviderStringBytes            = 4 << 10
	websocketHandshakeTimeout         = 5 * time.Second
	websocketWriteTimeout             = 10 * time.Second
	websocketResponseTimeout          = 2 * time.Minute
)

var supportedModels = [...]string{
	"SparkDesk",
	"SparkDesk-v1.1",
	"SparkDesk-v2.1",
	"SparkDesk-v3.1",
	"SparkDesk-v3.5",
	"SparkDesk-v4.0",
}

var versionDomains = map[string]string{
	"v1.1": "lite",
	"v2.1": "generalv2",
	"v3.1": "generalv3",
	"v3.5": "generalv3.5",
	"v4.0": "4.0Ultra",
}

// ModelList returns an owned copy of the exact reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// DialContextFunc is injectable for deterministic tests. Production adapters
// leave it nil and therefore always use the fixed, SSRF-safe Spark dialer.
type DialContextFunc func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)

// Adaptor converts OpenAI chat requests to Spark's signed WebSocket protocol.
type Adaptor struct {
	mode        channelcatalog.RelayMode
	DialContext DialContextFunc
	Now         func() time.Time
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)
var _ relaycommon.DirectAdaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	if meta != nil {
		a.mode = meta.Mode
	}
}

func (a *Adaptor) now() time.Time {
	if a != nil && a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	version, _, err := resolveVersion(meta)
	if err != nil {
		return "", err
	}
	// Channel BaseURL is intentionally ignored. Spark credentials are valid for
	// this provider host only and must never be replayed to an operator-supplied
	// URL.
	return "wss://" + providerHost + "/" + version + "/chat", nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Xunfei request metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	// This provider is dispatched directly over a signed WebSocket. If this
	// method is called by tooling, make sure no client or provider credential is
	// copied to a conventional HTTP header.
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("api-key")
	req.Header.Set("Content-Type", "application/json")
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	credentials, err := parseCredentials(meta.APIKey)
	if err != nil {
		return nil, err
	}
	_, domain, err := resolveVersion(meta)
	if err != nil {
		return nil, err
	}
	payload, err := convertRequest(meta, credentials.appID, domain)
	if err != nil {
		return nil, err
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, errors.New("encode Xunfei request")
	}
	if len(body) > maxRequestBodyBytes {
		return nil, fmt.Errorf("Xunfei request exceeds %d bytes", maxRequestBodyBytes)
	}
	return body, nil
}

// DoResponse is unreachable for direct WebSocket dispatch and fails closed if
// a caller accidentally tries to use the ordinary HTTP response path.
func (a *Adaptor) DoResponse(*gin.Context, *http.Response, *relaycommon.Meta) (*protocolkit.Usage, error) {
	return nil, errors.New("Xunfei requires direct WebSocket dispatch")
}

func (a *Adaptor) DoDirectRequest(c *gin.Context, requestURL string, body []byte, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || meta == nil || meta.Context == nil {
		return nil, errors.New("Xunfei direct request metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	expectedURL, err := a.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	if requestURL != expectedURL || len(body) == 0 || len(body) > maxRequestBodyBytes {
		return nil, errors.New("Xunfei direct request is invalid")
	}
	var converted sparkRequest
	if err := strictJSON(body, &converted); err != nil {
		return nil, errors.New("Xunfei converted request is invalid")
	}
	credentials, err := parseCredentials(meta.APIKey)
	if err != nil {
		return nil, err
	}
	_, domain, err := resolveVersion(meta)
	if err != nil {
		return nil, err
	}
	expectedPayload, err := convertRequest(meta, credentials.appID, domain)
	if err != nil {
		return nil, err
	}
	expectedBody, err := protocolkit.MarshalJSON(expectedPayload)
	if err != nil || !bytes.Equal(body, expectedBody) {
		return nil, errors.New("Xunfei converted request does not match relay metadata")
	}
	signedURL, err := buildAuthURL(expectedURL, credentials.apiKey, credentials.apiSecret, a.now())
	if err != nil {
		return nil, err
	}
	dial := a.DialContext
	if dial == nil {
		dial = newProductionDialer().DialContext
	}
	connection, response, err := dial(meta.Context, signedURL, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, safeHandshakeError(response, err)
	}
	if connection == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errors.New("Xunfei WebSocket connection is unavailable")
	}
	defer connection.Close()
	connection.SetReadLimit(maxResponseFrameBytes)
	if err := connection.SetWriteDeadline(time.Now().Add(websocketWriteTimeout)); err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	if err := connection.WriteMessage(websocket.TextMessage, body); err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	if err := connection.SetReadDeadline(responseDeadline(meta.Context)); err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}

	var usage protocolkit.Usage
	var content strings.Builder
	var rawBytes int64
	lastSequence := -1
	lastStatus := -1
	terminal := false
	streamStarted := false
	for frameCount := 0; frameCount < maxResponseFrames; frameCount++ {
		if err := meta.Context.Err(); err != nil {
			return usageAfterPartial(streamStarted, usage), relaycommon.SanitizeTransportError(err)
		}
		messageType, raw, readErr := connection.ReadMessage()
		if readErr != nil {
			return usageAfterPartial(streamStarted, usage), relaycommon.SanitizeTransportError(readErr)
		}
		if messageType != websocket.TextMessage || int64(len(raw)) > maxResponseFrameBytes {
			return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
		}
		rawBytes += int64(len(raw))
		if rawBytes > maxResponseBodyBytes || containsCredential(raw, credentials, signedURL) {
			return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
		}
		var frame sparkResponse
		if err := strictJSON(raw, &frame); err != nil {
			return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
		}
		if frame.Header.Code != 0 {
			return usageAfterPartial(streamStarted, usage), rejectedProviderResponse(frame.Header.Code)
		}
		if err := validateResponseFrame(&frame, lastSequence, lastStatus); err != nil {
			return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
		}
		lastSequence = frame.Payload.Choices.Seq
		lastStatus = frame.Payload.Choices.Status
		if err := addUsage(&usage, frame.Payload.Usage.Text); err != nil {
			return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
		}
		piece := ""
		if len(frame.Payload.Choices.Text) == 1 {
			piece = frame.Payload.Choices.Text[0].Content
		}
		if content.Len()+len(piece) > maxResponseTextBytes {
			return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
		}
		content.WriteString(piece)
		terminal = frame.Payload.Choices.Status == 2
		if meta.IsStream {
			if err := writeStreamChunk(c, piece, terminal, a.now()); err != nil {
				return &usage, fmt.Errorf("write Xunfei stream response: %w", err)
			}
			streamStarted = true
		}
		if terminal {
			break
		}
	}
	if !terminal {
		return usageAfterPartial(streamStarted, usage), invalidProviderResponse()
	}
	if meta.IsStream {
		if _, err := c.Writer.WriteString("data: [DONE]\n\n"); err != nil {
			return &usage, fmt.Errorf("write Xunfei stream terminator: %w", err)
		}
		c.Writer.Flush()
		return &usage, nil
	}
	if err := writeNonStreamResponse(c, content.String(), usage, a.now()); err != nil {
		return &usage, err
	}
	return &usage, nil
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Xunfei relay metadata is nil")
	}
	mode := meta.Mode
	if mode == channelcatalog.RelayModeUnknown {
		mode = a.mode
	}
	if mode != channelcatalog.RelayModeChatCompletions || meta.Format != channelcatalog.RelayFormatOpenAI {
		return errors.New("Xunfei supports only OpenAI chat completions")
	}
	if meta.Request.Stream != meta.IsStream {
		return errors.New("Xunfei request stream flag does not match relay metadata")
	}
	_, _, err := resolveVersion(meta)
	return err
}

type credentials struct {
	appID     string
	apiSecret string
	apiKey    string
}

func parseCredentials(raw string) (credentials, error) {
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return credentials{}, errors.New("Xunfei credential must be appId|apiSecret|apiKey")
	}
	for index, part := range parts {
		if len(part) < 4 || len(part) > maxCredentialComponentBytes || !utf8.ValidString(part) ||
			strings.TrimSpace(part) != part || hasControl(part) || hasSpace(part) ||
			(index == 2 && strings.ContainsAny(part, `"\`)) {
			return credentials{}, errors.New("Xunfei credential is invalid")
		}
	}
	return credentials{appID: parts[0], apiSecret: parts[1], apiKey: parts[2]}, nil
}

func resolveVersion(meta *relaycommon.Meta) (string, string, error) {
	if meta == nil {
		return "", "", errors.New("Xunfei relay metadata is nil")
	}
	model := strings.TrimSpace(meta.ModelName)
	modelVersion := ""
	switch model {
	case "SparkDesk":
		modelVersion = "v1.1"
	case "SparkDesk-v1.1", "SparkDesk-v2.1", "SparkDesk-v3.1", "SparkDesk-v3.5", "SparkDesk-v4.0":
		modelVersion = strings.TrimPrefix(model, "SparkDesk-")
	default:
		return "", "", errors.New("unsupported Xunfei model")
	}
	version := strings.TrimSpace(meta.APIVersion)
	if version == "" {
		version = modelVersion
	}
	domain, ok := versionDomains[version]
	if !ok {
		return "", "", errors.New("unsupported Xunfei API version")
	}
	return version, domain, nil
}

type sparkMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type sparkRequest struct {
	Header struct {
		AppID string `json:"app_id"`
	} `json:"header"`
	Parameter struct {
		Chat struct {
			Domain      string   `json:"domain,omitempty"`
			Temperature *float64 `json:"temperature,omitempty"`
			TopK        int      `json:"top_k,omitempty"`
			MaxTokens   int      `json:"max_tokens,omitempty"`
			Auditing    bool     `json:"auditing,omitempty"`
		} `json:"chat"`
	} `json:"parameter"`
	Payload struct {
		Message struct {
			Text []sparkMessage `json:"text"`
		} `json:"message"`
	} `json:"payload"`
}

func convertRequest(meta *relaycommon.Meta, appID, domain string) (*sparkRequest, error) {
	request := meta.Request
	if len(request.Messages) == 0 || len(request.Messages) > maxMessageCount {
		return nil, fmt.Errorf("Xunfei messages must contain between 1 and %d items", maxMessageCount)
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1 ||
		*request.Temperature != *request.Temperature) {
		return nil, errors.New("Xunfei temperature must be between 0 and 1")
	}
	if request.N != nil && (*request.N < 0 || *request.N > maxTopK) {
		return nil, fmt.Errorf("Xunfei n/top_k must be between 0 and %d", maxTopK)
	}
	maxTokens, err := requestMaxTokens(request)
	if err != nil {
		return nil, err
	}
	output := &sparkRequest{}
	output.Header.AppID = appID
	output.Parameter.Chat.Domain = domain
	output.Parameter.Chat.Temperature = request.Temperature
	if request.N != nil {
		output.Parameter.Chat.TopK = *request.N
	}
	output.Parameter.Chat.MaxTokens = maxTokens
	output.Payload.Message.Text = make([]sparkMessage, 0, len(request.Messages)+2)
	convertSystem := !strings.HasSuffix(meta.ModelName, "3.5")
	totalText := 0
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return nil, errors.New("Xunfei message role is unsupported")
		}
		content, err := strictMessageText(message)
		if err != nil {
			return nil, err
		}
		totalText += len(content)
		if totalText > maxRequestTextBytes {
			return nil, errors.New("Xunfei request text is too large")
		}
		if message.Role == "system" && convertSystem {
			output.Payload.Message.Text = append(output.Payload.Message.Text,
				sparkMessage{Role: "user", Content: content},
				sparkMessage{Role: "assistant", Content: "Okay"},
			)
			continue
		}
		output.Payload.Message.Text = append(output.Payload.Message.Text,
			sparkMessage{Role: message.Role, Content: content})
	}
	if len(output.Payload.Message.Text) > maxMessageCount*2 {
		return nil, errors.New("Xunfei converted message count is too large")
	}
	return output, nil
}

func requestMaxTokens(request *protocolkit.GeneralOpenAIRequest) (int, error) {
	for _, value := range []*int{request.MaxTokens, request.MaxCompletionTokens} {
		if value != nil && (*value < 0 || *value > maxOutputTokens) {
			return 0, fmt.Errorf("Xunfei max tokens must be between 0 and %d", maxOutputTokens)
		}
	}
	if request.MaxCompletionTokens != nil && *request.MaxCompletionTokens != 0 {
		return *request.MaxCompletionTokens, nil
	}
	if request.MaxTokens != nil {
		return *request.MaxTokens, nil
	}
	return 0, nil
}

func strictMessageText(message protocolkit.Message) (string, error) {
	var text string
	switch content := message.Content.(type) {
	case string:
		text = content
	case []any:
		var builder strings.Builder
		for _, rawPart := range content {
			switch part := rawPart.(type) {
			case map[string]any:
				kind, _ := part["type"].(string)
				value, valueOK := part["text"].(string)
				if kind != protocolkit.ContentTypeText || !valueOK {
					return "", errors.New("Xunfei supports only text message content")
				}
				builder.WriteString(value)
			case protocolkit.MediaContent:
				if part.Type != protocolkit.ContentTypeText {
					return "", errors.New("Xunfei supports only text message content")
				}
				builder.WriteString(part.Text)
			default:
				return "", errors.New("Xunfei message content is invalid")
			}
		}
		text = builder.String()
	default:
		return "", errors.New("Xunfei message content must be text")
	}
	if len(text) > maxMessageTextBytes || !utf8.ValidString(text) {
		return "", errors.New("Xunfei message content is invalid or too large")
	}
	for _, character := range text {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return "", errors.New("Xunfei message content contains unsupported control characters")
		}
	}
	return text, nil
}

type sparkResponseText struct {
	Content string `json:"content"`
	Role    string `json:"role"`
	Index   int    `json:"index"`
}

type sparkUsage struct {
	QuestionTokens   int `json:"question_tokens,omitempty"`
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type sparkResponse struct {
	Header struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		SID     string `json:"sid"`
		Status  int    `json:"status"`
	} `json:"header"`
	Payload struct {
		Choices struct {
			Status int                 `json:"status"`
			Seq    int                 `json:"seq"`
			Text   []sparkResponseText `json:"text"`
		} `json:"choices"`
		Usage struct {
			Text sparkUsage `json:"text"`
		} `json:"usage"`
	} `json:"payload"`
}

func validateResponseFrame(frame *sparkResponse, lastSequence, lastStatus int) error {
	if frame == nil || frame.Header.Code < 0 || frame.Header.Code > 1_000_000 ||
		frame.Header.Status < 0 || frame.Header.Status > 2 || frame.Payload.Choices.Status < 0 ||
		frame.Payload.Choices.Status > 2 || frame.Payload.Choices.Seq < 0 ||
		(lastSequence >= 0 && frame.Payload.Choices.Seq <= lastSequence) ||
		(lastStatus >= 0 && frame.Payload.Choices.Status < lastStatus) {
		return errors.New("invalid Xunfei response sequence")
	}
	for _, value := range []string{frame.Header.Message, frame.Header.SID} {
		if len(value) > maxProviderStringBytes || !utf8.ValidString(value) || hasControl(value) {
			return errors.New("invalid Xunfei response header")
		}
	}
	if len(frame.Payload.Choices.Text) > 1 {
		return errors.New("invalid Xunfei response choices")
	}
	for _, item := range frame.Payload.Choices.Text {
		if item.Index != 0 || (item.Role != "" && item.Role != "assistant") ||
			len(item.Content) > maxResponseTextBytes || !utf8.ValidString(item.Content) {
			return errors.New("invalid Xunfei response text")
		}
		for _, character := range item.Content {
			if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
				return errors.New("invalid Xunfei response text")
			}
		}
	}
	return validateProviderUsage(frame.Payload.Usage.Text)
}

func validateProviderUsage(usage sparkUsage) error {
	values := []int{usage.QuestionTokens, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens}
	for _, value := range values {
		if value < 0 || int64(value) > quotamath.MaxQuota {
			return errors.New("invalid Xunfei usage")
		}
	}
	if usage.TotalTokens != 0 && usage.TotalTokens < usage.PromptTokens+usage.CompletionTokens {
		return errors.New("invalid Xunfei total usage")
	}
	return nil
}

func addUsage(total *protocolkit.Usage, update sparkUsage) error {
	if total == nil || validateProviderUsage(update) != nil {
		return errors.New("invalid Xunfei usage")
	}
	values := []struct {
		current *int
		delta   int
	}{
		{&total.PromptTokens, update.PromptTokens},
		{&total.CompletionTokens, update.CompletionTokens},
		{&total.TotalTokens, update.TotalTokens},
	}
	for _, value := range values {
		if int64(*value.current) > quotamath.MaxQuota-int64(value.delta) {
			return errors.New("Xunfei usage exceeds supported range")
		}
		*value.current += value.delta
	}
	return nil
}

func writeNonStreamResponse(c *gin.Context, content string, usage protocolkit.Usage, now time.Time) error {
	response := protocolkit.ChatCompletionsResponse{
		Object: "chat.completion", Created: now.Unix(),
		Choices: []protocolkit.ChatCompletionsChoice{{
			Index: 0, Message: &protocolkit.ChatResponseMessage{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: &usage,
	}
	body, err := protocolkit.MarshalJSON(response)
	if err != nil {
		return errors.New("encode Xunfei response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, err = c.Writer.Write(body)
	return err
}

func writeStreamChunk(c *gin.Context, content string, terminal bool, now time.Time) error {
	var finishReason *string
	if terminal {
		stop := "stop"
		finishReason = &stop
	}
	response := protocolkit.ChatCompletionsStreamResponse{
		Object: "chat.completion.chunk", Created: now.Unix(), Model: "SparkDesk",
		Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
			Index: 0, FinishReason: finishReason,
			Delta: protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: content},
		}},
	}
	body, err := protocolkit.MarshalJSON(response)
	if err != nil {
		return errors.New("encode Xunfei stream response")
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	if _, err := c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}

func buildAuthURL(endpoint, apiKey, apiSecret string, now time.Time) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "wss" || parsed.Host != providerHost || parsed.RawQuery != "" ||
		parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("invalid Xunfei provider endpoint")
	}
	date := now.UTC().Format(time.RFC1123)
	signatureInput := "host: " + parsed.Host + "\n" + "date: " + date + "\n" +
		"GET " + parsed.EscapedPath() + " HTTP/1.1"
	mac := hmac.New(sha256.New, []byte(apiSecret))
	_, _ = mac.Write([]byte(signatureInput))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	authorization := fmt.Sprintf(
		`hmac username="%s", algorithm="hmac-sha256", headers="host date request-line", signature="%s"`,
		apiKey, signature,
	)
	query := url.Values{}
	query.Set("host", parsed.Host)
	query.Set("date", date)
	query.Set("authorization", base64.StdEncoding.EncodeToString([]byte(authorization)))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func newProductionDialer() *websocket.Dialer {
	return &websocket.Dialer{
		NetDialContext: httpx.SafeDialContext,
		Proxy:          nil, HandshakeTimeout: websocketHandshakeTimeout,
		ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10,
		EnableCompression: false,
	}
}

func responseDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(websocketResponseTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func safeHandshakeError(response *http.Response, _ error) error {
	if response == nil {
		return relaycommon.ErrUpstreamTransportFailed
	}
	status := response.StatusCode
	if status < 400 || status > 499 {
		status = http.StatusBadGateway
	}
	return &relaycommon.UpstreamError{StatusCode: status, Cause: errors.New("Xunfei WebSocket handshake failed")}
}

func invalidProviderResponse() error {
	return &relaycommon.UpstreamError{StatusCode: http.StatusBadGateway, Cause: errors.New("invalid Xunfei provider response")}
}

func rejectedProviderResponse(code int) error {
	if code < 0 || code > 1_000_000 {
		code = 0
	}
	return &relaycommon.UpstreamError{
		StatusCode: http.StatusBadRequest,
		Cause:      fmt.Errorf("Xunfei provider rejected request with code %d", code),
	}
}

func usageAfterPartial(started bool, usage protocolkit.Usage) *protocolkit.Usage {
	if !started {
		return nil
	}
	return &usage
}

func containsCredential(raw []byte, credential credentials, signedURL string) bool {
	fragments := []string{credential.appID, credential.apiSecret, credential.apiKey, signedURL}
	if parsed, err := url.Parse(signedURL); err == nil {
		encodedAuthorization := parsed.Query().Get("authorization")
		fragments = append(fragments, encodedAuthorization)
		if decoded, decodeErr := base64.StdEncoding.DecodeString(encodedAuthorization); decodeErr == nil {
			fragments = append(fragments, string(decoded))
		}
	}
	for _, fragment := range fragments {
		if fragment != "" && bytes.Contains(raw, []byte(fragment)) {
			return true
		}
	}
	return false
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func hasSpace(value string) bool {
	for _, character := range value {
		if unicode.IsSpace(character) {
			return true
		}
	}
	return false
}

func strictJSON(raw []byte, destination any) error {
	if len(raw) == 0 {
		return errors.New("empty Xunfei JSON")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing Xunfei JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing Xunfei JSON")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid Xunfei JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("invalid Xunfei JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid Xunfei JSON object")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate Xunfei JSON field")
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid Xunfei JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid Xunfei JSON array")
		}
	default:
		return errors.New("invalid Xunfei JSON")
	}
	return nil
}
