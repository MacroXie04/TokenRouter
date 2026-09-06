package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	advancedconfig "github.com/tokenrouter/tokenrouter/internal/relay/customconfig"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/ali"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/volcengine"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"strings"
)

const maxClaudeMessagesRequestBodyBytes int64 = 16 << 20

// RelayClaudeMessages serves POST /v1/messages (Anthropic Messages format).
// The request is moderated, billed, and settled like any relay; dispatch is
// Claude-native passthrough for Anthropic channels or Claude↔OpenAI conversion
// for OpenAI-compatible channels.
func RelayClaudeMessages(c *gin.Context, state relaycommon.RequestState) {
	cancel := applyRelayRequestDeadline(c)
	defer cancel()
	body, err := httpx.ReadAllLimited(c.Request.Body, maxClaudeMessagesRequestBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			abortClaude(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "请求体过大")
			return
		}
		abortClaude(c, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}
	var claudeReq protocolkit.ClaudeRequest
	if err := protocolkit.UnmarshalJSON(body, &claudeReq); err != nil {
		abortClaude(c, http.StatusBadRequest, "invalid_request_error", "无效的 JSON 请求体")
		return
	}
	if claudeReq.Model == "" {
		abortClaude(c, http.StatusBadRequest, "invalid_request_error", "缺少 model 字段")
		return
	}
	maxTokensProvided := claudeReq.MaxTokens != 0
	if !maxTokensProvided {
		claudeReq.MaxTokens = setting.GetClaudeDefaultMaxTokens(claudeReq.Model)
	}

	// The converted OpenAI request drives moderation, token estimation, and
	// billing-expression inputs; the original Claude request is dispatched.
	openaiReq := protocolkit.ClaudeRequestToOpenAIRequest(&claudeReq)
	openaiReq.Stream = claudeReq.Stream

	info := &RelayInfo{
		Mode:                    channelcatalog.RelayModeChatCompletions,
		Format:                  channelcatalog.RelayFormatClaude,
		Request:                 openaiReq,
		ClaudeRequest:           &claudeReq,
		ClaudeMaxTokensProvided: maxTokensProvided,
		RawBody:                 body,
		ModelName:               claudeReq.Model,
		UserGroup:               state.UserGroup,
		Group:                   state.FirstGroup(),
		IsStream:                claudeReq.Stream,
	}
	if err := relayAndSettleWithDispatch(c, state, info, dispatchClaudeUpstream); err != nil {
		logging.SysError("Claude relay lifecycle failed: " + err.Error())
		return
	}
}

func abortClaude(c *gin.Context, status int, typ, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": typ, "message": message}})
}

// dispatchClaudeUpstream sends a Claude-format request to the channel: native
// passthrough for Anthropic channels, OpenAI conversion otherwise.
func dispatchClaudeUpstream(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	if info.Channel == nil || info.ClaudeRequest == nil {
		return nil, errors.New("channel not selected")
	}
	restorePolicy, err := prepareClaudeRelayPolicy(info, false)
	if err != nil {
		return nil, err
	}
	defer restorePolicy()
	configuredHeaders := c.Request.Header.Clone()
	setting.ApplyClaudeModelHeaders(info.ModelName, configuredHeaders)
	originalHeaders := c.Request.Header
	c.Request.Header = configuredHeaders
	defer func() { c.Request.Header = originalHeaders }()

	base := info.Channel.BaseURL
	apiKey := channelssvc.GetChannelKey(info.Channel)
	var usage *protocolkit.Usage
	channelType := channelcatalog.ChannelType(info.Channel.Type)
	switch {
	case channelType == channelcatalog.ChannelTypeAnthropic:
		usage, err = sendClaudeNative(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeAws:
		usage, err = sendClaudeViaAWS(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeVertexAi:
		usage, err = sendClaudeViaVertex(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeMoonshot:
		usage, err = sendClaudeViaMoonshot(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeMiniMax:
		usage, err = sendClaudeViaMiniMax(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeZhipuV4:
		usage, err = sendClaudeViaZhipuV4(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeBaiduV2:
		usage, err = sendClaudeViaOpenAI(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeVolcEngine:
		usage, err = sendClaudeViaVolcEngine(c, info, base, apiKey)
	case channelType == channelcatalog.ChannelTypeAli:
		mappedModel := info.ClaudeRequest.Model
		if ali.SupportsAnthropicMessages(mappedModel) {
			usage, err = sendClaudeViaAli(c, info, base, apiKey)
		} else {
			usage, err = sendClaudeViaOpenAI(c, info, base, apiKey)
		}
	case channelType == channelcatalog.ChannelTypeAdvancedCustom:
		usage, err = sendClaudeViaAdvancedCustom(c, info)
	case channelType == channelcatalog.ChannelTypeSub2API || channelType == channelcatalog.ChannelTypeNewAPI:
		usage, err = sendClaudeViaGateway(c, info, base, apiKey)
	case channelcatalog.IsOpenAICompatibleChannelType(channelType):
		usage, err = sendClaudeViaOpenAI(c, info, base, apiKey)
	default:
		err = fmt.Errorf("channel type %d does not implement native Claude relay", info.Channel.Type)
	}
	return usage, relaycommon.SanitizeUpstreamError(err, apiKey)
}

// prepareClaudeRelayPolicy is safe at both the dispatcher and individual
// provider-helper boundaries. The prepared marker prevents a hot reload from
// changing policy halfway through a normally dispatched request, while direct
// helper callers still receive model mapping and policy normalization. Direct
// helpers infer that a nonzero limit was supplied because they bypass the
// native route parser that normally records field presence.
func prepareClaudeRelayPolicy(info *RelayInfo, inferNonzeroLimit bool) (func(), error) {
	if info == nil || info.Channel == nil || info.ClaudeRequest == nil {
		return nil, errors.New("Claude relay metadata is nil")
	}
	if info.claudePolicyPrepared {
		return func() {}, nil
	}
	originalRequest := info.ClaudeRequest
	originalOpenAIRequest := info.Request
	originalRawBody := info.RawBody
	originalPrepared := info.claudePolicyPrepared
	prepared := *originalRequest
	limitProvided := info.ClaudeMaxTokensProvided || inferNonzeroLimit && prepared.MaxTokens != 0
	prepared.Model = relaycommon.PrepareClaudeRequest(&prepared, info.ModelName,
		relaycommon.GetMappedModel(info.Channel, info.ModelName), limitProvided)
	preparedRaw, prepareErr := protocolkit.MarshalJSON(&prepared)
	if prepareErr != nil {
		return nil, fmt.Errorf("encode configured Claude request: %w", prepareErr)
	}
	info.ClaudeRequest = &prepared
	info.Request = protocolkit.ClaudeRequestToOpenAIRequest(&prepared)
	info.Request.Stream = info.IsStream
	info.RawBody = preparedRaw
	info.claudePolicyPrepared = true
	return func() {
		info.ClaudeRequest = originalRequest
		info.Request = originalOpenAIRequest
		info.RawBody = originalRawBody
		info.claudePolicyPrepared = originalPrepared
	}, nil
}

// sendClaudeViaVertex preserves the native Anthropic Messages contract while
// delegating the Vertex publisher URL, channel-controlled location, mapped
// model, and service-account/API-key authentication to the explicit adapter.
func sendClaudeViaVertex(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	mappedModel := info.ClaudeRequest.Model
	meta := &relaycommon.Meta{
		Context:            c.Request.Context(),
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             channelcatalog.RelayFormatClaude,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          mappedModel,
		BaseURL:            base,
		APIKey:             apiKey,
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            info.RawBody,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeVertexAi)
	if adaptor == nil {
		return nil, errors.New("Vertex AI relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	return adaptor.DoResponse(c, response, meta)
}

// sendClaudeViaVolcEngine matches Ark's split contract: ordinary bases expose
// OpenAI chat completions and therefore use the shared bidirectional Claude
// conversion, while the symbolic coding-plan base exposes native Messages.
func sendClaudeViaVolcEngine(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	restorePolicy, err := prepareClaudeRelayPolicy(info, true)
	if err != nil {
		return nil, err
	}
	defer restorePolicy()
	if !volcengine.IsCodingPlanBase(base) {
		return sendClaudeViaOpenAI(c, info, base, apiKey)
	}
	claudeRequest := *info.ClaudeRequest
	claudeRequest.Model = info.ClaudeRequest.Model
	body, err := protocolkit.MarshalJSON(&claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("encode VolcEngine native Claude request: %w", err)
	}
	meta := &relaycommon.Meta{
		Context: c.Request.Context(), Channel: info.Channel,
		Mode: channelcatalog.RelayModeChatCompletions, Format: channelcatalog.RelayFormatClaude,
		RequestPath: c.Request.URL.Path, OriginalModelName: info.ModelName,
		ModelName: claudeRequest.Model, BaseURL: base, APIKey: apiKey,
		ClientHeaders: c.Request.Header.Clone(), Request: info.Request, RawBody: body,
		RequestContentType: c.GetHeader("Content-Type"), IsStream: info.IsStream,
		PromptTokens: info.PromptTokens, ToolUsage: info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeVolcEngine)
	if adaptor == nil {
		return nil, errors.New("VolcEngine relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	if info.IsStream {
		return claudeNativeStreamResponse(c, response, info)
	}
	return claudeNativeNonStreamResponseWithInfo(c, response, info)
}

// sendClaudeViaAWS keeps the native Anthropic document and delegates the
// Bedrock envelope, model-path protection, authentication, and AWS event-stream
// decoding to the dedicated adapter. The ordinary relay lifecycle still owns
// quota reservation, retry decisions, and settlement.
func sendClaudeViaAWS(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	mappedModel := info.ClaudeRequest.Model
	meta := &relaycommon.Meta{
		Context:            c.Request.Context(),
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             channelcatalog.RelayFormatClaude,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ClaudeRequest.Model,
		ModelName:          mappedModel,
		BaseURL:            base,
		APIKey:             apiKey,
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            info.RawBody,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeAws)
	if adaptor == nil {
		return nil, errors.New("AWS relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	return adaptor.DoResponse(c, response, meta)
}

// sendClaudeViaZhipuV4 preserves the Anthropic Messages document and lets the
// explicit v4 adapter select the standard or coding-plan native route. The
// adapter also owns bounded Anthropic response parsing and usage extraction so
// this path participates in the ordinary reservation and settlement lifecycle.
func sendClaudeViaZhipuV4(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	mappedModel := info.ClaudeRequest.Model
	meta := &relaycommon.Meta{
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             channelcatalog.RelayFormatClaude,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          mappedModel,
		BaseURL:            base,
		APIKey:             apiKey,
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            info.RawBody,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeZhipuV4)
	if adaptor == nil {
		return nil, errors.New("Zhipu v4 relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	return adaptor.DoResponse(c, response, meta)
}

// sendClaudeViaAli preserves native Anthropic Messages for the model families
// Alibaba exposes on /apps/anthropic/v1/messages. Models outside that explicit
// family are converted by sendClaudeViaOpenAI and use DashScope's compatible
// chat endpoint instead.
func sendClaudeViaAli(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	claudeRequest := *info.ClaudeRequest
	mappedModel := info.ClaudeRequest.Model
	claudeRequest.Model = mappedModel
	body, err := protocolkit.MarshalJSON(&claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("encode Ali native Claude request: %w", err)
	}
	meta := &relaycommon.Meta{
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             channelcatalog.RelayFormatClaude,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          mappedModel,
		BaseURL:            base,
		APIKey:             apiKey,
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            body,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeAli)
	if adaptor == nil {
		return nil, errors.New("Ali relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return adaptor.DoResponse(c, response, meta)
	}
	if info.IsStream {
		return claudeNativeStreamResponse(c, response, info)
	}
	return claudeNativeNonStreamResponseWithInfo(c, response, info)
}

// sendClaudeViaMoonshot preserves the caller's native Anthropic Messages
// document while delegating Moonshot's URL and bearer-auth contract to its
// registered adapter. The native response helpers retain the Anthropic wire
// format and extract usage for the normal reservation/settlement lifecycle.
func sendClaudeViaMoonshot(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	claudeRequest := *info.ClaudeRequest
	mappedModel := info.ClaudeRequest.Model
	claudeRequest.Model = mappedModel
	body, err := protocolkit.MarshalJSON(&claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("encode Moonshot native Claude request: %w", err)
	}
	meta := &relaycommon.Meta{
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             channelcatalog.RelayFormatClaude,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          mappedModel,
		BaseURL:            base,
		APIKey:             apiKey,
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            body,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeMoonshot)
	if adaptor == nil {
		return nil, errors.New("Moonshot relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return adaptor.DoResponse(c, response, meta)
	}
	if info.IsStream {
		return claudeNativeStreamResponse(c, response, info)
	}
	return claudeNativeNonStreamResponseWithInfo(c, response, info)
}

// sendClaudeViaMiniMax preserves the caller's native Anthropic Messages
// document while the dedicated adapter supplies MiniMax's native path and
// bearer-auth contract. Native response helpers retain the Anthropic wire
// representation and return normalized usage to the relay lifecycle.
func sendClaudeViaMiniMax(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	claudeRequest := *info.ClaudeRequest
	mappedModel := info.ClaudeRequest.Model
	claudeRequest.Model = mappedModel
	body, err := protocolkit.MarshalJSON(&claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("encode MiniMax native Claude request: %w", err)
	}
	meta := &relaycommon.Meta{
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             channelcatalog.RelayFormatClaude,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          mappedModel,
		BaseURL:            base,
		APIKey:             apiKey,
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            body,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeMiniMax)
	if adaptor == nil {
		return nil, errors.New("MiniMax relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return adaptor.DoResponse(c, response, meta)
	}
	if info.IsStream {
		return miniMaxClaudeNativeStreamResponse(c, response, info)
	}
	return claudeNativeNonStreamResponseWithInfo(c, response, info)
}

func miniMaxClaudeNativeStreamResponse(c *gin.Context, response *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	const maxStreamBytes int64 = 64 << 20
	return miniMaxClaudeNativeStreamResponseWithLimit(c, response, info, maxStreamBytes)
}

func miniMaxClaudeNativeStreamResponseWithLimit(c *gin.Context, response *http.Response, info *RelayInfo, maxStreamBytes int64) (*protocolkit.Usage, error) {
	limited := &io.LimitedReader{R: response.Body, N: maxStreamBytes + 1}
	bounded := *response
	bounded.Body = io.NopCloser(limited)
	usage, err := claudeNativeStreamResponse(c, &bounded, info)
	if err != nil {
		return usage, err
	}
	if limited.N == 0 {
		return usage, fmt.Errorf("%w: MiniMax native Claude stream maximum is %d bytes",
			relaycommon.ErrUpstreamResponseTooLarge, maxStreamBytes)
	}
	return usage, nil
}

// sendClaudeNative posts the Claude request to the Anthropic Messages API and
// passes the response through unchanged (SSE events for streams).
func sendClaudeNative(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	if strings.TrimSpace(base) == "" {
		base = "https://api.anthropic.com"
	}
	url := relaycommon.JoinURL(base, "/v1/messages")
	claudeReq := *info.ClaudeRequest
	claudeReq.Model = info.ClaudeRequest.Model
	body, err := protocolkit.MarshalJSON(&claudeReq)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	setting.ApplyClaudeModelHeaders(info.ModelName, req.Header)
	channelssvc.ApplyChannelAffinityRequestHeaders(c, req)
	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if !info.IsStream {
		return claudeNativeNonStreamResponseWithInfo(c, resp, info)
	}
	return claudeNativeStreamResponse(c, resp, info)
}

// claudeNativeStreamResponse passes native Anthropic events through while
// extracting the terminal usage required for settlement.
func claudeNativeStreamResponse(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()
	var claudeUsage *protocolkit.ClaudeUsage
	var contentChars int
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := c.Writer.WriteString(line + "\n"); err != nil {
			return nil, fmt.Errorf("write Claude native stream: %w", err)
		}
		c.Writer.Flush()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := protocolkit.UnmarshalJSON([]byte(data), &event); err != nil {
			continue
		}
		switch event["type"] {
		case "message_start":
			if message, ok := event["message"].(map[string]any); ok {
				if decoded := decodeClaudeUsage(message["usage"]); decoded != nil {
					if err := relaycommon.ObserveClaudeUsage(info.ToolUsageHooks, decoded); err != nil {
						return nil, fmt.Errorf("observe Claude native stream tool usage: %w", err)
					}
					claudeUsage = decoded
				}
			}
		case "content_block_start":
			if block, ok := event["content_block"].(map[string]any); ok && block["type"] == "tool_use" &&
				info.ToolUsageHooks != nil && info.ToolUsageHooks.ObserveClaudeToolUse != nil {
				blockIndex := anyToInt(event["index"])
				blockID, _ := block["id"].(string)
				blockName, _ := block["name"].(string)
				if err := info.ToolUsageHooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
					BlockIndex: &blockIndex, ID: blockID, Name: blockName,
				}); err != nil {
					return nil, fmt.Errorf("observe Claude native stream tool use: %w", err)
				}
			}
		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
				if text, ok := delta["text"].(string); ok {
					contentChars += len(text)
				}
			}
		case "message_delta":
			if decoded := decodeClaudeUsage(event["usage"]); decoded != nil {
				if err := relaycommon.ObserveClaudeUsage(info.ToolUsageHooks, decoded); err != nil {
					return protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage), fmt.Errorf("observe Claude native stream tool usage: %w", err)
				}
				if claudeUsage == nil {
					claudeUsage = decoded
				} else {
					claudeUsage.OutputTokens = decoded.OutputTokens
				}
			}
		}
	}
	var usage *protocolkit.Usage
	if claudeUsage != nil {
		usage = protocolkit.ClaudeUsageToOpenAIUsage(claudeUsage)
	} else {
		usage = relaycommon.EstimateStreamUsage(info.PromptTokens, contentChars)
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan Claude native stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	return usage, nil
}

func sendClaudeViaAdvancedCustom(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	claudeRequest := *info.ClaudeRequest
	claudeRequest.Model = info.ClaudeRequest.Model
	nativeBody, err := protocolkit.MarshalJSON(&claudeRequest)
	if err != nil {
		return nil, err
	}
	call, err := callAdvancedCustomNative(c, info, channelcatalog.RelayFormatClaude, nativeBody, info.Request)
	if err != nil {
		return nil, err
	}
	defer call.response.Body.Close()
	if call.response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(call.response)
	}
	switch call.converter {
	case advancedconfig.ConverterNone:
		if info.IsStream {
			return claudeNativeStreamResponse(c, call.response, info)
		}
		return claudeNativeNonStreamResponseWithInfo(c, call.response, info)
	case advancedconfig.ConverterClaudeToOpenAIChat:
		if info.IsStream {
			return claudeStreamFromOpenAI(c, call.response, info)
		}
		return claudeNonStreamFromOpenAIWithInfo(c, call.response, info)
	default:
		return nil, fmt.Errorf("advanced custom converter %q cannot serve Anthropic Messages", call.converter)
	}
}

func decodeClaudeUsage(value any) *protocolkit.ClaudeUsage {
	if value == nil {
		return nil
	}
	raw, err := protocolkit.MarshalJSON(value)
	if err != nil {
		return nil
	}
	var usage protocolkit.ClaudeUsage
	if err := protocolkit.UnmarshalJSON(raw, &usage); err != nil {
		return nil
	}
	return &usage
}

func claudeNativeNonStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	return claudeNativeNonStreamResponseWithInfo(c, resp, nil)
}

func claudeNativeNonStreamResponseWithInfo(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	raw, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Claude native response: %w", err)
	}
	var claudeResp protocolkit.ClaudeResponse
	if err := protocolkit.UnmarshalJSON(raw, &claudeResp); err != nil {
		return nil, errors.New("上游返回无效响应")
	}
	var hooks *relaycommon.ToolUsageHooks
	if info != nil {
		hooks = info.ToolUsageHooks
	}
	if err := relaycommon.ObserveClaudeResponse(hooks, &claudeResp); err != nil {
		return nil, fmt.Errorf("observe Claude native tool usage: %w", err)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(raw); err != nil {
		return nil, fmt.Errorf("write Claude native response: %w", err)
	}
	if claudeResp.Usage != nil {
		return protocolkit.ClaudeUsageToOpenAIUsage(claudeResp.Usage), nil
	}
	return &protocolkit.Usage{}, nil
}

// sendClaudeViaOpenAI converts the Claude request to OpenAI format, calls the
// channel's chat-completions endpoint, and converts the response (including
// SSE chunks) back into Claude Messages events.
func sendClaudeViaOpenAI(c *gin.Context, info *RelayInfo, base, apiKey string) (*protocolkit.Usage, error) {
	openaiReq := protocolkit.ClaudeRequestToOpenAIRequest(info.ClaudeRequest)
	openaiReq.Stream = info.IsStream
	meta := &relaycommon.Meta{
		Channel:      info.Channel,
		Mode:         channelcatalog.RelayModeChatCompletions,
		Format:       channelcatalog.RelayFormatOpenAI,
		ModelName:    info.ClaudeRequest.Model,
		BaseURL:      base,
		APIKey:       apiKey,
		Request:      openaiReq,
		IsStream:     info.IsStream,
		PromptTokens: info.PromptTokens,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelType(info.Channel.Type))
	if adaptor == nil {
		return nil, fmt.Errorf("channel type %d has no implemented relay adapter", info.Channel.Type)
	}
	adaptor.Init(meta)
	url, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(req, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, req)
	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer resp.Body.Close()
	if channelcatalog.ChannelType(info.Channel.Type) == channelcatalog.ChannelTypeAli &&
		(resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) {
		// The Ali adapter owns DashScope's top-level error envelope and channel
		// status-code mapping even when the Claude request used compatible-mode.
		return adaptor.DoResponse(c, resp, meta)
	}
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if !info.IsStream {
		return claudeNonStreamFromOpenAIWithInfo(c, resp, info)
	}
	return claudeStreamFromOpenAI(c, resp, info)
}

// claudeNonStreamFromOpenAI converts a full OpenAI response to Claude format.
func claudeNonStreamFromOpenAI(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	return claudeNonStreamFromOpenAIWithInfo(c, resp, nil)
}

func claudeNonStreamFromOpenAIWithInfo(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	raw, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI response for Claude conversion: %w", err)
	}
	var openaiResp protocolkit.ChatCompletionsResponse
	if err := protocolkit.UnmarshalJSON(raw, &openaiResp); err != nil {
		return nil, errors.New("上游返回无效响应")
	}
	var hooks *relaycommon.ToolUsageHooks
	if info != nil {
		hooks = info.ToolUsageHooks
	}
	if err := observeOpenAIResponseAsClaude(hooks, &openaiResp); err != nil {
		return nil, fmt.Errorf("observe converted Claude tool usage: %w", err)
	}
	claudeResp := protocolkit.OpenAIResponseToClaudeResponse(&openaiResp)
	out, err := protocolkit.MarshalJSON(claudeResp)
	if err != nil {
		return nil, fmt.Errorf("encode Claude response: %w", err)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return nil, fmt.Errorf("write Claude response: %w", err)
	}
	return openaiResp.Usage, nil
}

// claudeStreamFromOpenAI converts OpenAI SSE chunks into Claude Messages SSE
// events (message_start → content_block_start → deltas → stop → message_delta
// → message_stop) and extracts usage.
func claudeStreamFromOpenAI(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	writeEvent := func(typ string, payload any) error {
		b, err := protocolkit.MarshalJSON(payload)
		if err != nil {
			return fmt.Errorf("encode Claude %s event: %w", typ, err)
		}
		if _, err := c.Writer.WriteString("event: " + typ + "\n"); err != nil {
			return fmt.Errorf("write Claude %s event header: %w", typ, err)
		}
		if _, err := c.Writer.WriteString("data: " + string(b) + "\n\n"); err != nil {
			return fmt.Errorf("write Claude %s event data: %w", typ, err)
		}
		c.Writer.Flush()
		return nil
	}

	msgID := "msg_" + cryptoutil.BestEffortRandomAlphanumeric(24)
	if err := writeEvent("message_start", gin.H{
		"type": "message_start",
		"message": gin.H{"id": msgID, "type": "message", "role": "assistant",
			"content": []any{}, "model": info.ModelName, "stop_reason": nil, "stop_sequence": nil,
			"usage": gin.H{"input_tokens": info.PromptTokens, "output_tokens": 1}},
	}); err != nil {
		return nil, err
	}
	if err := writeEvent("content_block_start", gin.H{
		"type": "content_block_start", "index": 0,
		"content_block": gin.H{"type": "text", "text": ""},
	}); err != nil {
		return nil, err
	}

	var usage *protocolkit.Usage
	var openaiUsage *protocolkit.Usage
	var contentChars int
	var finishReason string
	blockStopped := false

	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk protocolkit.ChatCompletionsStreamResponse
		if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err != nil {
			continue
		}
		if err := observeOpenAIChunkAsClaude(info.ToolUsageHooks, &chunk); err != nil {
			return usage, fmt.Errorf("observe converted Claude stream tool usage: %w", err)
		}
		if chunk.Usage != nil {
			openaiUsage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				contentChars += len(choice.Delta.Content)
				if err := writeEvent("content_block_delta", gin.H{
					"type": "content_block_delta", "index": 0,
					"delta": gin.H{"type": "text_delta", "text": choice.Delta.Content},
				}); err != nil {
					return usage, err
				}
			}
			if choice.FinishReason != nil && !blockStopped {
				blockStopped = true
				finishReason = *choice.FinishReason
			}
		}
	}

	if openaiUsage != nil {
		usage = openaiUsage
	} else {
		usage = relaycommon.EstimateStreamUsage(info.PromptTokens, contentChars)
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan OpenAI stream for Claude conversion (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if !blockStopped {
		finishReason = "stop"
	}
	if err := writeEvent("content_block_stop", gin.H{"type": "content_block_stop", "index": 0}); err != nil {
		return usage, err
	}
	outputTokens := 0
	if openaiUsage != nil {
		outputTokens = openaiUsage.CompletionTokens
	}
	if err := writeEvent("message_delta", gin.H{
		"type": "message_delta",
		"delta": gin.H{
			"stop_reason":   protocolkit.OpenAIFinishReasonToClaudeStopReason(finishReason),
			"stop_sequence": nil,
		},
		"usage": gin.H{"output_tokens": outputTokens},
	}); err != nil {
		return usage, err
	}
	if err := writeEvent("message_stop", gin.H{"type": "message_stop"}); err != nil {
		return usage, err
	}

	return usage, nil
}

func observeOpenAIResponseAsClaude(hooks *relaycommon.ToolUsageHooks, response *protocolkit.ChatCompletionsResponse) error {
	if hooks == nil || hooks.ObserveClaudeToolUse == nil || response == nil {
		return nil
	}
	for _, choice := range response.Choices {
		if choice.Message == nil {
			continue
		}
		for index, tool := range choice.Message.ToolCalls {
			if tool.Function == nil {
				continue
			}
			blockIndex := tool.Index
			if tool.Id == "" && blockIndex == 0 {
				blockIndex = index
			}
			if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
				BlockIndex: &blockIndex, ID: tool.Id, Name: tool.Function.Name,
			}); err != nil {
				return err
			}
		}
		if function := choice.Message.FunctionCall; function != nil {
			blockIndex := len(choice.Message.ToolCalls)
			if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
				BlockIndex: &blockIndex, Name: function.Name,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func observeOpenAIChunkAsClaude(hooks *relaycommon.ToolUsageHooks, chunk *protocolkit.ChatCompletionsStreamResponse) error {
	if hooks == nil || hooks.ObserveClaudeToolUse == nil || chunk == nil {
		return nil
	}
	for _, choice := range chunk.Choices {
		for index, tool := range choice.Delta.ToolCalls {
			if tool.Function == nil {
				continue
			}
			blockIndex := tool.Index
			if tool.Id == "" && blockIndex == 0 {
				blockIndex = index
			}
			if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
				BlockIndex: &blockIndex, ID: tool.Id, Name: tool.Function.Name,
			}); err != nil {
				return err
			}
		}
		if function := choice.Delta.FunctionCall; function != nil {
			blockIndex := len(choice.Delta.ToolCalls)
			if err := hooks.ObserveClaudeToolUse(relaycommon.ToolClaudeObservation{
				BlockIndex: &blockIndex, Name: function.Name,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func anyToInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}
