package engine

import (
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/advancedcustom"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/ali"
	awsprovider "github.com/tokenrouter/tokenrouter/internal/relay/providers/aws"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/baidu"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/baidu_v2"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/claude"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/cloudflare"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/codex"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/cohere"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/coze"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/dify"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/gemini"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/minimax"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/mokaai"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/moonshot"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/newapi"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/ollama"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/openai"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/palm"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/replicate"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/sub2api"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/tencent"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/vertex"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/volcengine"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/xunfei"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/zhipu"
)

// GetAdaptor returns the adapter for an implemented channel type. Provider
// identifiers are persisted beside secrets, so unknown or unimplemented types
// must fail closed instead of falling through to a different vendor.
func GetAdaptor(channelType channelcatalog.ChannelType) relaycommon.Adaptor {
	switch channelType {
	case channelcatalog.ChannelTypeZhipu:
		return &zhipu.LegacyAdaptor{}
	case channelcatalog.ChannelTypeZhipuV4:
		return &zhipu.V4Adaptor{}
	case channelcatalog.ChannelTypeAli:
		return &ali.Adaptor{}
	case channelcatalog.ChannelTypeBaidu:
		return &baidu.Adaptor{}
	case channelcatalog.ChannelTypeBaiduV2:
		return &baidu_v2.Adaptor{}
	case channelcatalog.ChannelTypeAnthropic:
		return &claude.Adaptor{}
	case channelcatalog.ChannelTypeAws:
		return &awsprovider.Adaptor{}
	case channelcatalog.ChannelTypeGemini:
		return &gemini.Adaptor{}
	case channelcatalog.ChannelTypeCodex:
		return &codex.Adaptor{}
	case channelcatalog.ChannelTypeOllama:
		return &ollama.Adaptor{}
	case channelcatalog.ChannelTypeMoonshot:
		return &moonshot.Adaptor{}
	case channelcatalog.ChannelCloudflare:
		return &cloudflare.Adaptor{}
	case channelcatalog.ChannelTypeCohere:
		return &cohere.Adaptor{}
	case channelcatalog.ChannelTypeCoze:
		return &coze.Adaptor{}
	case channelcatalog.ChannelTypeDify:
		return &dify.Adaptor{}
	case channelcatalog.ChannelTypeMiniMax:
		return &minimax.Adaptor{}
	case channelcatalog.ChannelTypeMokaAI:
		return &mokaai.Adaptor{}
	case channelcatalog.ChannelTypePaLM:
		return &palm.Adaptor{}
	case channelcatalog.ChannelTypeReplicate:
		return &replicate.Adaptor{}
	case channelcatalog.ChannelTypeSub2API:
		return &sub2api.Adaptor{}
	case channelcatalog.ChannelTypeNewAPI:
		return &newapi.Adaptor{}
	case channelcatalog.ChannelTypeXunfei:
		return &xunfei.Adaptor{}
	case channelcatalog.ChannelTypeTencent:
		return &tencent.Adaptor{}
	case channelcatalog.ChannelTypeVertexAi:
		return &vertex.Adaptor{}
	case channelcatalog.ChannelTypeJimeng:
		return &jimeng.Adaptor{}
	case channelcatalog.ChannelTypeVolcEngine:
		return &volcengine.Adaptor{}
	case channelcatalog.ChannelTypeAdvancedCustom:
		return &advancedcustom.Adaptor{}
	case channelcatalog.ChannelTypeJina, channelcatalog.ChannelTypeSubmodel:
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

func isOpenAICompatibleChannelType(channelType channelcatalog.ChannelType) bool {
	return channelcatalog.IsOpenAICompatibleChannelType(channelType)
}
