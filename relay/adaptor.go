package relay

import (
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/relay/channel/advancedcustom"
	"github.com/tokenrouter/tokenrouter/relay/channel/ali"
	awsprovider "github.com/tokenrouter/tokenrouter/relay/channel/aws"
	"github.com/tokenrouter/tokenrouter/relay/channel/baidu"
	"github.com/tokenrouter/tokenrouter/relay/channel/baidu_v2"
	"github.com/tokenrouter/tokenrouter/relay/channel/claude"
	"github.com/tokenrouter/tokenrouter/relay/channel/cloudflare"
	"github.com/tokenrouter/tokenrouter/relay/channel/codex"
	"github.com/tokenrouter/tokenrouter/relay/channel/cohere"
	"github.com/tokenrouter/tokenrouter/relay/channel/coze"
	"github.com/tokenrouter/tokenrouter/relay/channel/dify"
	"github.com/tokenrouter/tokenrouter/relay/channel/gemini"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
	"github.com/tokenrouter/tokenrouter/relay/channel/minimax"
	"github.com/tokenrouter/tokenrouter/relay/channel/mokaai"
	"github.com/tokenrouter/tokenrouter/relay/channel/moonshot"
	"github.com/tokenrouter/tokenrouter/relay/channel/newapi"
	"github.com/tokenrouter/tokenrouter/relay/channel/ollama"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	"github.com/tokenrouter/tokenrouter/relay/channel/palm"
	"github.com/tokenrouter/tokenrouter/relay/channel/replicate"
	"github.com/tokenrouter/tokenrouter/relay/channel/sub2api"
	"github.com/tokenrouter/tokenrouter/relay/channel/tencent"
	"github.com/tokenrouter/tokenrouter/relay/channel/vertex"
	"github.com/tokenrouter/tokenrouter/relay/channel/volcengine"
	"github.com/tokenrouter/tokenrouter/relay/channel/xunfei"
	"github.com/tokenrouter/tokenrouter/relay/channel/zhipu"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

// GetAdaptor returns the adapter for an implemented channel type. Provider
// identifiers are persisted beside secrets, so unknown or unimplemented types
// must fail closed instead of falling through to a different vendor.
func GetAdaptor(channelType constant.ChannelType) relaycommon.Adaptor {
	switch channelType {
	case constant.ChannelTypeZhipu:
		return &zhipu.LegacyAdaptor{}
	case constant.ChannelTypeZhipuV4:
		return &zhipu.V4Adaptor{}
	case constant.ChannelTypeAli:
		return &ali.Adaptor{}
	case constant.ChannelTypeBaidu:
		return &baidu.Adaptor{}
	case constant.ChannelTypeBaiduV2:
		return &baidu_v2.Adaptor{}
	case constant.ChannelTypeAnthropic:
		return &claude.Adaptor{}
	case constant.ChannelTypeAws:
		return &awsprovider.Adaptor{}
	case constant.ChannelTypeGemini:
		return &gemini.Adaptor{}
	case constant.ChannelTypeCodex:
		return &codex.Adaptor{}
	case constant.ChannelTypeOllama:
		return &ollama.Adaptor{}
	case constant.ChannelTypeMoonshot:
		return &moonshot.Adaptor{}
	case constant.ChannelCloudflare:
		return &cloudflare.Adaptor{}
	case constant.ChannelTypeCohere:
		return &cohere.Adaptor{}
	case constant.ChannelTypeCoze:
		return &coze.Adaptor{}
	case constant.ChannelTypeDify:
		return &dify.Adaptor{}
	case constant.ChannelTypeMiniMax:
		return &minimax.Adaptor{}
	case constant.ChannelTypeMokaAI:
		return &mokaai.Adaptor{}
	case constant.ChannelTypePaLM:
		return &palm.Adaptor{}
	case constant.ChannelTypeReplicate:
		return &replicate.Adaptor{}
	case constant.ChannelTypeSub2API:
		return &sub2api.Adaptor{}
	case constant.ChannelTypeNewAPI:
		return &newapi.Adaptor{}
	case constant.ChannelTypeXunfei:
		return &xunfei.Adaptor{}
	case constant.ChannelTypeTencent:
		return &tencent.Adaptor{}
	case constant.ChannelTypeVertexAi:
		return &vertex.Adaptor{}
	case constant.ChannelTypeJimeng:
		return &jimeng.Adaptor{}
	case constant.ChannelTypeVolcEngine:
		return &volcengine.Adaptor{}
	case constant.ChannelTypeAdvancedCustom:
		return &advancedcustom.Adaptor{}
	case constant.ChannelTypeJina, constant.ChannelTypeSubmodel:
		// These providers use OpenAI-family JSON on a deliberately narrow set
		// of endpoints. They stay out of the broad compatibility catalog so
		// native Claude/Gemini requests cannot be converted and dispatched to
		// endpoints that the providers do not implement.
		return &openai.Adaptor{}
	}
	if isOpenAICompatibleChannelType(channelType) {
		return &openai.Adaptor{}
	}
	return nil
}

func isOpenAICompatibleChannelType(channelType constant.ChannelType) bool {
	return constant.IsOpenAICompatibleChannelType(channelType)
}
