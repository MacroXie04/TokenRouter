package engine

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"net/url"
)

var relayHTTPClient = newRelayHTTPClient(defaultRelayHTTPTransportConfig())

func selectAuthorizedRelayChannel(c *gin.Context, info *RelayInfo, groups []string, ignore map[int]struct{}) (*model.Channel, string, bool, bool, error) {
	for _, group := range groups {
		preferredChannelID, affinityFound := channelssvc.GetPreferredChannelByAffinity(c, info.ModelName, group, info.RawBody)
		channel, usedAffinity, err := channelssvc.GetSatisfiedChannelWithPreferred(group, info.ModelName, preferredChannelID, ignore, nil)
		if err == nil {
			return channel, group, usedAffinity, affinityFound, nil
		}
		if !errors.Is(err, channelssvc.ErrChannelNotFound) {
			return nil, "", false, affinityFound, err
		}
	}
	return nil, "", false, false, channelssvc.ErrChannelNotFound
}

// dispatchUpstream builds the provider request and performs the upstream call.
func dispatchUpstream(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	// Alpha search is a Codex-standalone endpoint: only the same upstream
	// families the reference gates support it; other channel types error so
	// the retry loop can fall through to another channel.
	if info.Mode == channelcatalog.RelayModeAlphaSearch {
		switch channelcatalog.ChannelType(info.Channel.Type) {
		case channelcatalog.ChannelTypeSub2API, channelcatalog.ChannelTypeNewAPI,
			channelcatalog.ChannelTypeCodex, channelcatalog.ChannelTypeAdvancedCustom:
		default:
			return nil, errors.New("channel does not support /v1/alpha/search")
		}
	}
	adaptor := GetAdaptor(channelcatalog.ChannelType(info.Channel.Type))
	if adaptor == nil {
		return nil, fmt.Errorf("channel type %d has no implemented relay adapter", info.Channel.Type)
	}
	meta := &relaycommon.Meta{
		Context:            c.Request.Context(),
		Channel:            info.Channel,
		Mode:               info.Mode,
		Format:             relaycommon.GetRelayFormat(channelcatalog.ChannelType(info.Channel.Type), info.Mode),
		RequestPath:        c.Request.URL.Path,
		OriginalModelName:  info.ModelName,
		ModelName:          relaycommon.GetMappedModel(info.Channel, info.ModelName),
		BaseURL:            info.Channel.BaseURL,
		APIKey:             channelssvc.GetChannelKey(info.Channel),
		ClientHeaders:      c.Request.Header.Clone(),
		Request:            info.Request,
		RawBody:            info.RawBody,
		RequestContentType: info.RequestContentType,
		APIVersion:         info.APIVersion,
		IsStream:           info.IsStream,
		PromptTokens:       info.PromptTokens,
		ToolUsage:          info.ToolUsageHooks,
	}
	adaptor.Init(meta)

	url, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		// Some provider conversions perform bounded, credentialed auxiliary
		// operations (for example uploading an inline file). Sanitize those
		// upstream errors just like the primary response path.
		return nil, relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	}
	if direct, ok := adaptor.(relaycommon.DirectAdaptor); ok && isDirectRelayURL(url) {
		usage, directErr := direct.DoDirectRequest(c, url, body, meta)
		return usage, relaycommon.SanitizeUpstreamError(directErr, meta.APIKey)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(req, meta); err != nil {
		return nil, relaycommon.SanitizeUpstreamError(err, meta.APIKey)
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer resp.Body.Close()

	usage, responseErr := adaptor.DoResponse(c, resp, meta)
	return usage, relaycommon.SanitizeUpstreamError(responseErr, meta.APIKey)
}

func isDirectRelayURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "ws" || parsed.Scheme == "wss"
}

// headerToMap flattens an http.Header to a single-value string map.
func headerToMap(h http.Header) map[string]string {
	if len(h) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
