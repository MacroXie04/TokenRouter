package common

import "github.com/tokenrouter/tokenrouter/constant"

// ChannelTypeToAPIType reports the compatible adapter identifier for a
// channel. The returned OpenAI value for an unknown channel is only a legacy
// lookup fallback; callers must honor the false result and must not dispatch
// credentials through it.
func ChannelTypeToAPIType(channelType constant.ChannelType) (constant.APIType, bool) {
	switch channelType {
	case constant.ChannelTypeOpenAI:
		return constant.APITypeOpenAI, true
	case constant.ChannelTypeAnthropic:
		return constant.APITypeAnthropic, true
	case constant.ChannelTypePaLM:
		return constant.APITypePaLM, true
	case constant.ChannelTypeBaidu:
		return constant.APITypeBaidu, true
	case constant.ChannelTypeZhipu:
		return constant.APITypeZhipu, true
	case constant.ChannelTypeAli:
		return constant.APITypeAli, true
	case constant.ChannelTypeXunfei:
		return constant.APITypeXunfei, true
	case constant.ChannelTypeAIProxyLibrary:
		return constant.APITypeAIProxyLibrary, true
	case constant.ChannelTypeTencent:
		return constant.APITypeTencent, true
	case constant.ChannelTypeGemini:
		return constant.APITypeGemini, true
	case constant.ChannelTypeZhipuV4:
		return constant.APITypeZhipuV4, true
	case constant.ChannelTypeOllama:
		return constant.APITypeOllama, true
	case constant.ChannelTypePerplexity:
		return constant.APITypePerplexity, true
	case constant.ChannelTypeAws:
		return constant.APITypeAws, true
	case constant.ChannelTypeCohere:
		return constant.APITypeCohere, true
	case constant.ChannelTypeDify:
		return constant.APITypeDify, true
	case constant.ChannelTypeJina:
		return constant.APITypeJina, true
	case constant.ChannelCloudflare:
		return constant.APITypeCloudflare, true
	case constant.ChannelTypeSiliconFlow:
		return constant.APITypeSiliconFlow, true
	case constant.ChannelTypeVertexAi:
		return constant.APITypeVertexAi, true
	case constant.ChannelTypeMistral:
		return constant.APITypeMistral, true
	case constant.ChannelTypeDeepSeek:
		return constant.APITypeDeepSeek, true
	case constant.ChannelTypeMokaAI:
		return constant.APITypeMokaAI, true
	case constant.ChannelTypeVolcEngine:
		return constant.APITypeVolcEngine, true
	case constant.ChannelTypeBaiduV2:
		return constant.APITypeBaiduV2, true
	case constant.ChannelTypeOpenRouter:
		return constant.APITypeOpenRouter, true
	case constant.ChannelTypeXinference:
		return constant.APITypeXinference, true
	case constant.ChannelTypeXai:
		return constant.APITypeXai, true
	case constant.ChannelTypeCoze:
		return constant.APITypeCoze, true
	case constant.ChannelTypeJimeng:
		return constant.APITypeJimeng, true
	case constant.ChannelTypeMoonshot:
		return constant.APITypeMoonshot, true
	case constant.ChannelTypeSubmodel:
		return constant.APITypeSubmodel, true
	case constant.ChannelTypeMiniMax:
		return constant.APITypeMiniMax, true
	case constant.ChannelTypeReplicate:
		return constant.APITypeReplicate, true
	case constant.ChannelTypeCodex:
		return constant.APITypeCodex, true
	case constant.ChannelTypeAdvancedCustom:
		return constant.APITypeAdvancedCustom, true
	case constant.ChannelTypeSub2API:
		return constant.APITypeSub2API, true
	case constant.ChannelTypeNewAPI:
		return constant.APITypeNewAPI, true
	default:
		return constant.APITypeOpenAI, false
	}
}

// SupportsResponsesCompact identifies adapters whose upstream contract accepts
// the compact Responses endpoint. Channel type is retained in the signature
// for compatibility with callers that already have both identifiers.
func SupportsResponsesCompact(_ constant.ChannelType, apiType constant.APIType) bool {
	switch apiType {
	case constant.APITypeOpenAI, constant.APITypeCodex, constant.APITypeAdvancedCustom,
		constant.APITypeSub2API, constant.APITypeNewAPI:
		return true
	default:
		return false
	}
}
