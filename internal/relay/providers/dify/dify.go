// Package dify implements Dify's application-oriented chat-messages API.
// Dify applications own their upstream model configuration, so client model
// mappings are used for channel selection and billing but are never put on the
// provider wire.
package dify

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ChannelName                  = "dify"
	defaultBaseURL               = "https://api.dify.ai"
	maxDifyBaseURLBytes          = 4096
	maxDifyCredentialBytes       = 16 << 10
	maxDifyRequestBodyBytes      = 16 << 20
	maxDifyQueryBytes            = 16 << 20
	maxDifyUserBytes             = 1024
	maxDifyFiles                 = 32
	maxDifyInlineFileBytes       = 8 << 20
	maxDifyInlineTotalBytes      = 16 << 20
	maxDifyRemoteURLBytes        = 8 << 10
	maxDifyMimeTypeBytes         = 128
	maxDifyUploadIDBytes         = 1024
	maxDifyUploadResponseBytes   = 1 << 20
	maxDifyStreamBodyBytes       = 64 << 20
	maxDifyStreamTextBytes       = 16 << 20
	maxDifyStreamEvents          = 1_000_000
	difyUploadRequestTimeout     = 30 * time.Second
	difyStreamModel              = "dify"
	difyDefaultInlineImageMIME   = "image/jpeg"
	difyThinkingOpenReplacement  = "<think>"
	difyThinkingClose            = "</details>"
	difyThinkingCloseReplacement = "</think>"
)

const difyThinkingOpen = "<details style=\"color:gray;background-color: #f8f8f8;padding: 8px;border-radius: 4px;\" open> <summary> Thinking... </summary>\n"

// ModelList returns an owned empty catalog. A Dify channel points at one
// configured application; that application, rather than the relay request,
// selects its model.
func ModelList() []string { return []string{} }

// Adaptor implements the sole active reference contract: OpenAI Chat
// Completions translated to Dify chat-messages, in blocking or streaming mode.
type Adaptor struct {
	mode         channelcatalog.RelayMode
	format       channelcatalog.RelayFormat
	uploadClient *http.Client
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
	if a.uploadClient == nil {
		a.uploadClient = newUploadClient()
	}
}

// RequestURL resolves the Dify chat-messages endpoint from a channel base URL.
func RequestURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	return relaycommon.JoinURL(base, "/v1/chat-messages"), nil
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	return RequestURL(meta.BaseURL)
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("Dify request is nil")
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
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if _, err := validatedCredential(meta.APIKey); err != nil {
		return nil, err
	}
	return a.convertChatRequest(meta)
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("Dify response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, difyHTTPError(response, meta)
	}
	if meta.IsStream {
		return difyStreamResponse(c, response, meta)
	}
	return difyBlockingResponse(c, response, meta)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Dify relay metadata is nil")
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
		return fmt.Errorf("Dify channel does not support relay mode %d", mode)
	}
	if format != channelcatalog.RelayFormatOpenAI {
		return fmt.Errorf("Dify channel does not support relay format %q", format)
	}
	return nil
}

func validatedCredential(raw string) (string, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" {
		return "", errors.New("Dify API key is required")
	}
	if len(credential) > maxDifyCredentialBytes || strings.ContainsAny(credential, "\r\n\x00") {
		return "", errors.New("Dify API key is invalid")
	}
	return credential, nil
}

func validateBaseURL(raw string) error {
	if len(raw) > maxDifyBaseURLBytes {
		return errors.New("Dify base URL is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Dify base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Dify base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Dify base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Dify base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Dify base URL must not contain a query or fragment")
	}
	return nil
}

func newUploadClient() *http.Client {
	return &http.Client{
		Timeout: difyUploadRequestTimeout,
		Transport: &http.Transport{
			DialContext: httpx.SafeDialContext,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: env.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false),
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
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
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return status
		}
		number = parsed
	default:
		return status
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) || number < 100 || number > 599 {
		return status
	}
	return int(number)
}
