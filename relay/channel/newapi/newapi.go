// Package newapi implements the gateway-to-gateway NewAPI provider. Unlike a
// normal OpenAI-compatible provider, a NewAPI upstream accepts several client
// protocols on their original paths. Native Claude and Gemini dispatch is
// performed by the relay package; this adapter owns the shared OpenAI-family
// request and response behavior.
package newapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName             = "newapi"
	maxGatewayBaseURLBytes  = 4096
	maxGatewayRequestURI    = 4096
	maxGatewayCredentialLen = 16 << 10
	maxAnthropicVersionLen  = 128
	defaultAnthropicVersion = "2023-06-01"
)

// ModelList is intentionally empty. Gateway channels discover their model
// catalog from the configured upstream rather than shipping a stale snapshot.
func ModelList() []string { return []string{} }

// Adaptor preserves the mounted request path while delegating OpenAI-family
// serialization and response accounting to the hardened shared adapter.
type Adaptor struct {
	delegate openai.Adaptor
	mode     constant.RelayMode
	format   constant.RelayFormat
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
	a.delegate.Init(meta)
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", errors.New("NewAPI relay metadata is nil")
	}
	wantPath, err := gatewayPathForMode(a.mode)
	if err != nil {
		return "", err
	}
	requestPath := strings.TrimSpace(meta.RequestPath)
	if a.mode == constant.RelayModeAlphaSearch {
		requestPath = wantPath
	}
	if requestPath != wantPath {
		return "", fmt.Errorf("NewAPI request path %q does not match relay mode %d", requestPath, a.mode)
	}
	return RequestURL(meta.BaseURL, requestPath)
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil || meta == nil {
		return errors.New("NewAPI request metadata is nil")
	}
	credential, err := ValidateCredential(meta.APIKey)
	if err != nil {
		return err
	}
	if err := a.delegate.SetupRequestHeader(request, meta); err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	switch a.format {
	case constant.RelayFormatClaude:
		request.Header.Set("x-api-key", credential)
		version := defaultAnthropicVersion
		if meta.ClientHeaders != nil && strings.TrimSpace(meta.ClientHeaders.Get("anthropic-version")) != "" {
			version = strings.TrimSpace(meta.ClientHeaders.Get("anthropic-version"))
		}
		if err := ValidateAnthropicVersion(version); err != nil {
			return err
		}
		request.Header.Set("anthropic-version", version)
	case constant.RelayFormatGemini:
		request.Header.Set("x-goog-api-key", credential)
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta == nil {
		return nil, errors.New("NewAPI relay metadata is nil")
	}
	if _, err := ValidateCredential(meta.APIKey); err != nil {
		return nil, err
	}
	return a.delegate.ConvertRequest(meta)
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	return a.delegate.DoResponse(c, response, meta)
}

// RequestURL joins one validated gateway base with one validated request URI.
// It intentionally preserves a base path and the incoming API version.
func RequestURL(base, requestURI string) (string, error) {
	base = strings.TrimSpace(base)
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	if err := validateRequestURI(requestURI); err != nil {
		return "", err
	}
	return strings.TrimRight(base, "/") + requestURI, nil
}

// ValidateCredential rejects missing, oversized, or control-bearing secrets
// before a credentialed request can be constructed.
func ValidateCredential(raw string) (string, error) {
	credential := strings.TrimSpace(raw)
	if credential == "" {
		return "", errors.New("NewAPI upstream credential is empty")
	}
	if len(credential) > maxGatewayCredentialLen {
		return "", errors.New("NewAPI upstream credential is too large")
	}
	for _, char := range credential {
		if char < 0x21 || char == 0x7f {
			return "", errors.New("NewAPI upstream credential contains invalid characters")
		}
	}
	return credential, nil
}

// ValidateAnthropicVersion accepts one bounded visible-ASCII header value.
func ValidateAnthropicVersion(version string) error {
	if version == "" || len(version) > maxAnthropicVersionLen {
		return errors.New("NewAPI anthropic-version header is invalid")
	}
	for _, char := range version {
		if char < 0x21 || char > 0x7e {
			return errors.New("NewAPI anthropic-version header is invalid")
		}
	}
	return nil
}

func validateBaseURL(base string) error {
	if base == "" || len(base) > maxGatewayBaseURLBytes || strings.ContainsAny(base, "\r\n\x00") {
		return errors.New("NewAPI upstream base URL is invalid")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("NewAPI upstream base URL is invalid")
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || hasDotSegment(decodedPath) || strings.Contains(decodedPath, "\\") {
		return errors.New("NewAPI upstream base URL is invalid")
	}
	return nil
}

func validateRequestURI(requestURI string) error {
	if requestURI == "" || len(requestURI) > maxGatewayRequestURI || !strings.HasPrefix(requestURI, "/") ||
		strings.HasPrefix(requestURI, "//") || strings.ContainsAny(requestURI, "\r\n\x00\\") {
		return errors.New("NewAPI upstream request path is invalid")
	}
	parsed, err := url.ParseRequestURI(requestURI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return errors.New("NewAPI upstream request path is invalid")
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || hasDotSegment(decodedPath) {
		return errors.New("NewAPI upstream request path is invalid")
	}
	query := parsed.Query()
	for key, values := range query {
		if key != "alt" || len(values) != 1 || values[0] != "sse" {
			return errors.New("NewAPI upstream request query is invalid")
		}
	}
	return nil
}

func hasDotSegment(value string) bool {
	cleaned := path.Clean("/" + strings.TrimPrefix(value, "/"))
	if cleaned != "/"+strings.TrimPrefix(value, "/") && strings.TrimSuffix(cleaned, "/") != strings.TrimSuffix("/"+strings.TrimPrefix(value, "/"), "/") {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func gatewayPathForMode(mode constant.RelayMode) (string, error) {
	switch mode {
	case constant.RelayModeChatCompletions:
		return "/v1/chat/completions", nil
	case constant.RelayModeCompletions:
		return "/v1/completions", nil
	case constant.RelayModeEmbeddings:
		return "/v1/embeddings", nil
	case constant.RelayModeModerations:
		return "/v1/moderations", nil
	case constant.RelayModeImagesGenerations:
		return "/v1/images/generations", nil
	case constant.RelayModeImagesEdits:
		return "/v1/images/edits", nil
	case constant.RelayModeEdits:
		return "/v1/edits", nil
	case constant.RelayModeResponses:
		return "/v1/responses", nil
	case constant.RelayModeResponsesCompact:
		return "/v1/responses/compact", nil
	case constant.RelayModeAlphaSearch:
		return "/v1/alpha/search", nil
	default:
		return "", fmt.Errorf("NewAPI channel does not support relay mode %d", mode)
	}
}
