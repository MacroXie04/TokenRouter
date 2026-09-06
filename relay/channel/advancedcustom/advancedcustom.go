// Package advancedcustom implements model-aware, administrator-configured
// upstream routes. Route configuration is validated again on the relay hot
// path so legacy or directly-written database rows fail before credentials
// leave the process.
package advancedcustom

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	advancedconfig "github.com/tokenrouter/tokenrouter/pkg/advancedcustom"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/relay/channel/claude"
	"github.com/tokenrouter/tokenrouter/relay/channel/gemini"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	maxHeaderOverrides = 100
	clientHeaderPrefix = "{client_header:"
)

type Adaptor struct {
	mode      constant.RelayMode
	format    constant.RelayFormat
	resolved  bool
	route     advancedconfig.Route
	converter string
	openAI    openai.Adaptor
	claude    claude.Adaptor
	gemini    gemini.Adaptor
}

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.resolved = false
	a.route = advancedconfig.Route{}
	a.converter = ""
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
	a.openAI.Init(meta)
	// Advanced Custom's pass-through behavior is the generic OpenAI wire
	// contract, never a provider profile selected from the persisted type.
	a.openAI.ChannelType = constant.ChannelTypeOpenAI
	a.claude.Init(meta)
	a.gemini.Init(meta)
}

func (a *Adaptor) resolve(meta *relaycommon.Meta) error {
	if a.resolved {
		return nil
	}
	if meta == nil || meta.Channel == nil {
		return errors.New("advanced custom relay metadata is nil")
	}
	config, err := advancedconfig.ParseSettings(meta.Channel.OtherSettings)
	if err != nil {
		return err
	}
	originalModel := strings.TrimSpace(meta.OriginalModelName)
	if originalModel == "" {
		originalModel = strings.TrimSpace(meta.ModelName)
	}
	route, ok := advancedconfig.Match(config, meta.RequestPath, originalModel)
	if !ok {
		return fmt.Errorf("advanced custom channel does not support request path %s for model %s", meta.RequestPath, originalModel)
	}
	a.route = route
	a.converter = route.Converter
	a.resolved = true
	return nil
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.resolve(meta); err != nil {
		return "", err
	}
	return advancedconfig.ResolveURL(meta.BaseURL, a.route, meta.ModelName, meta.IsStream, meta.APIKey)
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil {
		return errors.New("advanced custom upstream request is nil")
	}
	if err := a.resolve(meta); err != nil {
		return err
	}
	contentType := strings.TrimSpace(meta.RequestContentType)
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	if accept := meta.ClientHeaders.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	} else if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	}

	auth := a.route.Auth
	if auth == nil {
		if meta.APIKey != "" {
			value := "Bearer " + meta.APIKey
			if !validHeaderValue(value) {
				return errors.New("advanced custom default bearer credential is invalid")
			}
			req.Header.Set("Authorization", value)
		}
	} else {
		switch strings.TrimSpace(auth.Type) {
		case advancedconfig.AuthNone, advancedconfig.AuthQuery:
		case advancedconfig.AuthHeader:
			value := strings.ReplaceAll(auth.Value, advancedconfig.APIKeyPlaceholder, meta.APIKey)
			if !validHeaderValue(value) {
				return errors.New("advanced custom header authentication value is invalid")
			}
			req.Header.Set(strings.TrimSpace(auth.Name), value)
		default:
			return errors.New("advanced custom authentication type is invalid")
		}
	}
	if a.converter == advancedconfig.ConverterOpenAIChatToClaude ||
		a.converter == advancedconfig.ConverterNone && a.format == constant.RelayFormatClaude {
		version := meta.ClientHeaders.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
	}
	return applyHeaderOverrides(req, meta)
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.resolve(meta); err != nil {
		return nil, err
	}
	switch a.converter {
	case advancedconfig.ConverterNone:
		return a.openAI.ConvertRequest(meta)
	case advancedconfig.ConverterClaudeToOpenAIChat, advancedconfig.ConverterGeminiToOpenAIChat:
		// Native relay handlers already construct a semantically equivalent
		// OpenAI request for moderation and accounting. Serialize that immutable
		// snapshot through the generic wire adapter after route resolution.
		if a.mode != constant.RelayModeChatCompletions {
			return nil, errors.New("advanced custom native converter requires chat completions upstream")
		}
		return a.openAI.ConvertRequest(meta)
	case advancedconfig.ConverterOpenAIChatToClaude:
		if a.mode != constant.RelayModeChatCompletions {
			return nil, errors.New("advanced custom Claude converter requires chat completions")
		}
		return a.claude.ConvertRequest(meta)
	case advancedconfig.ConverterOpenAIChatToGemini:
		if a.mode != constant.RelayModeChatCompletions {
			return nil, errors.New("advanced custom Gemini converter requires chat completions")
		}
		return a.gemini.ConvertRequest(meta)
	case advancedconfig.ConverterOpenAIChatToResponses:
		if a.mode != constant.RelayModeChatCompletions {
			return nil, errors.New("advanced custom Responses converter requires chat completions")
		}
		body, err := protocolkit.OpenAIChatRequestToResponsesMap(meta.Request, meta.ModelName)
		if err != nil {
			return nil, err
		}
		return protocolkit.MarshalJSON(body)
	case advancedconfig.ConverterResponsesToOpenAIChat, advancedconfig.ConverterResponsesToGemini:
		if a.mode != constant.RelayModeResponses {
			return nil, errors.New("advanced custom Responses input converter requires the Responses API")
		}
		chatRequest, err := protocolkit.ResponsesMapToOpenAIChatRequest(meta.Request.Extra, meta.ModelName)
		if err != nil {
			return nil, err
		}
		convertedMeta := *meta
		convertedMeta.Mode = constant.RelayModeChatCompletions
		convertedMeta.Request = chatRequest
		if a.converter == advancedconfig.ConverterResponsesToGemini {
			return a.gemini.ConvertRequest(&convertedMeta)
		}
		convertedOpenAI := a.openAI
		convertedOpenAI.Mode = constant.RelayModeChatCompletions
		return convertedOpenAI.ConvertRequest(&convertedMeta)
	default:
		return nil, fmt.Errorf("advanced custom converter %q is not implemented for this relay format", a.converter)
	}
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if err := a.resolve(meta); err != nil {
		return nil, err
	}
	switch a.converter {
	case advancedconfig.ConverterNone:
		return a.openAI.DoResponse(c, resp, meta)
	case advancedconfig.ConverterOpenAIChatToClaude:
		return a.claude.DoResponse(c, resp, meta)
	case advancedconfig.ConverterOpenAIChatToGemini:
		return a.gemini.DoResponse(c, resp, meta)
	case advancedconfig.ConverterOpenAIChatToResponses:
		return responsesStreamOrResponseToChat(c, resp, meta)
	case advancedconfig.ConverterResponsesToOpenAIChat:
		return chatStreamOrResponseToResponses(c, resp, meta)
	case advancedconfig.ConverterResponsesToGemini:
		return geminiStreamOrResponseToResponses(c, resp, meta)
	default:
		return nil, fmt.Errorf("advanced custom converter %q is not implemented for this response format", a.converter)
	}
}

var headerRegexCache sync.Map

func applyHeaderOverrides(req *http.Request, meta *relaycommon.Meta) error {
	if meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.HeaderOverride) == "" {
		return nil
	}
	if len(meta.Channel.HeaderOverride) > advancedconfig.MaxSettingsBytes {
		return errors.New("advanced custom header override is too large")
	}
	var overrides map[string]string
	if err := protocolkit.UnmarshalJSON([]byte(meta.Channel.HeaderOverride), &overrides); err != nil {
		return errors.New("advanced custom header override must be a JSON object of strings")
	}
	if len(overrides) > maxHeaderOverrides {
		return fmt.Errorf("advanced custom header override exceeds %d entries", maxHeaderOverrides)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	explicitNames := make(map[string]string, len(overrides))
	for _, name := range keys {
		trimmed := strings.TrimSpace(name)
		if trimmed == "*" || strings.HasPrefix(strings.ToLower(trimmed), "re:") || strings.HasPrefix(strings.ToLower(trimmed), "regex:") {
			continue
		}
		canonical := strings.ToLower(trimmed)
		if previous, duplicate := explicitNames[canonical]; duplicate {
			return fmt.Errorf("advanced custom header overrides %q and %q differ only by case", previous, trimmed)
		}
		explicitNames[canonical] = trimmed
	}
	// Passthrough rules run first. Explicit configured headers below always win.
	for _, rule := range keys {
		matcher, all, err := passthroughMatcher(rule)
		if err != nil {
			return err
		}
		if !all && matcher == nil {
			continue
		}
		for name, values := range meta.ClientHeaders {
			if unsafePassthroughHeader(name) || !all && !matcher.MatchString(name) {
				continue
			}
			for _, value := range values {
				if validHeaderValue(value) {
					req.Header.Add(name, value)
				}
			}
		}
	}
	for _, name := range keys {
		template := overrides[name]
		trimmed := strings.TrimSpace(name)
		if trimmed == "*" || strings.HasPrefix(strings.ToLower(trimmed), "re:") || strings.HasPrefix(strings.ToLower(trimmed), "regex:") {
			continue
		}
		if !advancedconfig.ValidHeaderName(trimmed) || blockedExplicitHeader(trimmed) {
			return fmt.Errorf("advanced custom header override %q is not allowed", trimmed)
		}
		value, include, err := resolveHeaderValue(template, meta)
		if err != nil {
			return fmt.Errorf("advanced custom header override %q: %w", trimmed, err)
		}
		if strings.EqualFold(trimmed, "Host") {
			if !include || strings.ContainsAny(value, "/?#@\r\n") {
				return errors.New("advanced custom Host override is invalid")
			}
			req.Host = value
			continue
		}
		if !include {
			req.Header.Del(trimmed)
		} else {
			req.Header.Set(trimmed, value)
		}
	}
	return nil
}

func passthroughMatcher(rule string) (*regexp.Regexp, bool, error) {
	rule = strings.TrimSpace(rule)
	if rule == "*" {
		return nil, true, nil
	}
	lower := strings.ToLower(rule)
	prefixLength := 0
	if strings.HasPrefix(lower, "re:") {
		prefixLength = len("re:")
	} else if strings.HasPrefix(lower, "regex:") {
		prefixLength = len("regex:")
	} else {
		return nil, false, nil
	}
	pattern := strings.TrimSpace(rule[prefixLength:])
	if pattern == "" {
		return nil, false, errors.New("advanced custom header passthrough regex is empty")
	}
	if cached, ok := headerRegexCache.Load(pattern); ok {
		if compiled, valid := cached.(*regexp.Regexp); valid {
			return compiled, false, nil
		}
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, errors.New("advanced custom header passthrough regex is invalid")
	}
	actual, _ := headerRegexCache.LoadOrStore(pattern, compiled)
	resolved, _ := actual.(*regexp.Regexp)
	return resolved, false, nil
}

func resolveHeaderValue(template string, meta *relaycommon.Meta) (string, bool, error) {
	trimmed := strings.TrimSpace(template)
	if strings.HasPrefix(trimmed, clientHeaderPrefix) {
		if !strings.HasSuffix(trimmed, "}") || strings.Count(trimmed, "}") != 1 {
			return "", false, errors.New("client_header placeholder must be the complete value")
		}
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, clientHeaderPrefix), "}"))
		if !advancedconfig.ValidHeaderName(name) {
			return "", false, errors.New("client_header placeholder name is invalid")
		}
		value := meta.ClientHeaders.Get(name)
		if strings.TrimSpace(value) == "" {
			return "", false, nil
		}
		if !validHeaderValue(value) {
			return "", false, errors.New("client header value is invalid")
		}
		return value, true, nil
	}
	value := strings.ReplaceAll(template, advancedconfig.APIKeyPlaceholder, meta.APIKey)
	if strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	if len(value) > advancedconfig.MaxAuthValueBytes || !validHeaderValue(value) {
		return "", false, errors.New("configured header value is invalid")
	}
	return value, true, nil
}

func validHeaderValue(value string) bool {
	return len(value) <= advancedconfig.MaxAuthValueBytes && !strings.ContainsAny(value, "\r\n")
}

func unsafePassthroughHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
		"transfer-encoding", "upgrade", "cookie", "host", "content-length", "accept-encoding",
		"authorization", "x-api-key", "x-goog-api-key", "sec-websocket-key", "sec-websocket-version",
		"sec-websocket-extensions":
		return true
	default:
		return false
	}
}

func blockedExplicitHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "content-length", "cookie", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
