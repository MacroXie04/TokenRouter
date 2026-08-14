package relay

import (
	"github.com/tokenrouter/tokenrouter/constant"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/relay/channel/claude"
	"github.com/tokenrouter/tokenrouter/relay/channel/gemini"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
)

// GetAdaptor returns the adapter for a channel type. All OpenAI-compatible
// providers share the OpenAI adapter; Claude and Gemini use dedicated adapters.
func GetAdaptor(channelType constant.ChannelType) relaycommon.Adaptor {
	switch channelType {
	case constant.ChannelTypeAnthropic:
		return &claude.Adaptor{}
	case constant.ChannelTypeGemini, constant.ChannelTypeVertexAi:
		return &gemini.Adaptor{}
	default:
		return &openai.Adaptor{}
	}
}
