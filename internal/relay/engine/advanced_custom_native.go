package engine

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	advancedconfig "github.com/tokenrouter/tokenrouter/internal/relay/customconfig"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"strings"
)

type advancedCustomNativeCall struct {
	response  *http.Response
	converter string
}

// callAdvancedCustomNative resolves the administrator-configured route using
// the original client model, then dispatches either a native pass-through body
// or the already-normalized OpenAI representation. The caller owns and closes
// the returned response body.
func callAdvancedCustomNative(
	c *gin.Context,
	info *RelayInfo,
	format channelcatalog.RelayFormat,
	nativeBody []byte,
	openAIRequest *protocolkit.GeneralOpenAIRequest,
) (advancedCustomNativeCall, error) {
	if c == nil || c.Request == nil || info == nil || info.Channel == nil || openAIRequest == nil {
		return advancedCustomNativeCall{}, errors.New("advanced custom native relay metadata is nil")
	}
	config, err := advancedconfig.ParseSettings(info.Channel.OtherSettings)
	if err != nil {
		return advancedCustomNativeCall{}, err
	}
	route, ok := advancedconfig.Match(config, c.Request.URL.Path, info.ModelName)
	if !ok {
		return advancedCustomNativeCall{}, fmt.Errorf(
			"advanced custom channel does not support request path %s for model %s",
			c.Request.URL.Path, info.ModelName,
		)
	}

	requestCopy := *openAIRequest
	requestCopy.Stream = info.IsStream
	upstreamModel := relaycommon.GetMappedModel(info.Channel, info.ModelName)
	if format == channelcatalog.RelayFormatGemini && info.GeminiUpstreamModel != "" {
		upstreamModel = info.GeminiUpstreamModel
	}
	if format == channelcatalog.RelayFormatClaude && info.ClaudeRequest != nil && info.ClaudeRequest.Model != "" {
		upstreamModel = info.ClaudeRequest.Model
	}
	meta := &relaycommon.Meta{
		Channel:            info.Channel,
		Mode:               channelcatalog.RelayModeChatCompletions,
		Format:             format,
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          upstreamModel,
		BaseURL:            info.Channel.BaseURL,
		APIKey:             channelssvc.GetChannelKey(info.Channel),
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            &requestCopy,
		RawBody:            nativeBody,
		RequestContentType: c.GetHeader("Content-Type"),
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeAdvancedCustom)
	if adaptor == nil {
		return advancedCustomNativeCall{}, errors.New("advanced custom relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return advancedCustomNativeCall{}, err
	}
	body := nativeBody
	if strings.TrimSpace(route.Converter) != "" && strings.TrimSpace(route.Converter) != advancedconfig.ConverterNone {
		body, err = adaptor.ConvertRequest(meta)
		if err != nil {
			return advancedCustomNativeCall{}, err
		}
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return advancedCustomNativeCall{}, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return advancedCustomNativeCall{}, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return advancedCustomNativeCall{}, relaycommon.SanitizeTransportError(err)
	}
	converter := strings.TrimSpace(route.Converter)
	if converter == "" {
		converter = advancedconfig.ConverterNone
	}
	return advancedCustomNativeCall{response: response, converter: converter}, nil
}
