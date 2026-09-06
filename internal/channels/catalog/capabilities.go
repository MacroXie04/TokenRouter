package catalog

import ()

// ChannelTypeToAPIType reports the compatible adapter identifier for a
// channel. The returned OpenAI value for an unknown channel is only a legacy
// lookup fallback; callers must honor the false result and must not dispatch
// credentials through it.
func ChannelTypeToAPIType(channelType ChannelType) (APIType, bool) {
	switch channelType {
	case ChannelTypeOpenAI:
		return APITypeOpenAI, true
	case ChannelTypeAnthropic:
		return APITypeAnthropic, true
	case ChannelTypePaLM:
		return APITypePaLM, true
	case ChannelTypeBaidu:
		return APITypeBaidu, true
	case ChannelTypeZhipu:
		return APITypeZhipu, true
	case ChannelTypeAli:
		return APITypeAli, true
	case ChannelTypeXunfei:
		return APITypeXunfei, true
	case ChannelTypeAIProxyLibrary:
		return APITypeAIProxyLibrary, true
	case ChannelTypeTencent:
		return APITypeTencent, true
	case ChannelTypeGemini:
		return APITypeGemini, true
	case ChannelTypeZhipuV4:
		return APITypeZhipuV4, true
	case ChannelTypeOllama:
		return APITypeOllama, true
	case ChannelTypePerplexity:
		return APITypePerplexity, true
	case ChannelTypeAws:
		return APITypeAws, true
	case ChannelTypeCohere:
		return APITypeCohere, true
	case ChannelTypeDify:
		return APITypeDify, true
	case ChannelTypeJina:
		return APITypeJina, true
	case ChannelCloudflare:
		return APITypeCloudflare, true
	case ChannelTypeSiliconFlow:
		return APITypeSiliconFlow, true
	case ChannelTypeVertexAi:
		return APITypeVertexAi, true
	case ChannelTypeMistral:
		return APITypeMistral, true
	case ChannelTypeDeepSeek:
		return APITypeDeepSeek, true
	case ChannelTypeMokaAI:
		return APITypeMokaAI, true
	case ChannelTypeVolcEngine:
		return APITypeVolcEngine, true
	case ChannelTypeBaiduV2:
		return APITypeBaiduV2, true
	case ChannelTypeOpenRouter:
		return APITypeOpenRouter, true
	case ChannelTypeXinference:
		return APITypeXinference, true
	case ChannelTypeXai:
		return APITypeXai, true
	case ChannelTypeCoze:
		return APITypeCoze, true
	case ChannelTypeJimeng:
		return APITypeJimeng, true
	case ChannelTypeMoonshot:
		return APITypeMoonshot, true
	case ChannelTypeSubmodel:
		return APITypeSubmodel, true
	case ChannelTypeMiniMax:
		return APITypeMiniMax, true
	case ChannelTypeReplicate:
		return APITypeReplicate, true
	case ChannelTypeCodex:
		return APITypeCodex, true
	case ChannelTypeAdvancedCustom:
		return APITypeAdvancedCustom, true
	case ChannelTypeSub2API:
		return APITypeSub2API, true
	case ChannelTypeNewAPI:
		return APITypeNewAPI, true
	default:
		return APITypeOpenAI, false
	}
}

// SupportsResponsesCompact identifies adapters whose upstream contract accepts
// the compact Responses endpoint. Channel type is retained in the signature
// for compatibility with callers that already have both identifiers.
func SupportsResponsesCompact(_ ChannelType, apiType APIType) bool {
	switch apiType {
	case APITypeOpenAI, APITypeCodex, APITypeAdvancedCustom,
		APITypeSub2API, APITypeNewAPI:
		return true
	default:
		return false
	}
}
