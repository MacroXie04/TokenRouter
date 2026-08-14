// Package constant defines TokenRouter's domain vocabulary: channel types,
// relay formats, relay modes, and shared status/limit constants. The integer
// values are stable and persisted in the database (and compatible with legacy
// one-api data imports); do not reorder existing entries.
package constant

// ChannelType identifies an upstream provider (channel).
type ChannelType int

const (
	ChannelTypeUnknown        ChannelType = 0
	ChannelTypeOpenAI         ChannelType = 1
	ChannelTypeMidjourney     ChannelType = 2
	ChannelTypeAzure          ChannelType = 3
	ChannelTypeOllama         ChannelType = 4
	ChannelTypeMidjourneyPlus ChannelType = 5
	ChannelTypeOpenAIMax      ChannelType = 6
	ChannelTypeOhMyGPT        ChannelType = 7
	ChannelTypeCustom         ChannelType = 8
	ChannelTypeAILS           ChannelType = 9
	ChannelTypeAIProxy        ChannelType = 10
	ChannelTypePaLM           ChannelType = 11
	ChannelTypeAPI2GPT        ChannelType = 12
	ChannelTypeAIGC2D         ChannelType = 13
	ChannelTypeAnthropic      ChannelType = 14
	ChannelTypeBaidu          ChannelType = 15
	ChannelTypeZhipu          ChannelType = 16
	ChannelTypeAli            ChannelType = 17
	ChannelTypeXunfei         ChannelType = 18
	ChannelType360            ChannelType = 19
	ChannelTypeOpenRouter     ChannelType = 20
	ChannelTypeAIProxyLibrary ChannelType = 21
	ChannelTypeFastGPT        ChannelType = 22
	ChannelTypeTencent        ChannelType = 23
	ChannelTypeGemini         ChannelType = 24
	ChannelTypeMoonshot       ChannelType = 25
	ChannelTypeZhipuV4        ChannelType = 26
	ChannelTypePerplexity     ChannelType = 27
	ChannelTypeLingYiWanWu    ChannelType = 28
	ChannelTypeAws            ChannelType = 29
	ChannelTypeCohere         ChannelType = 30
	ChannelTypeMiniMax        ChannelType = 31
	ChannelTypeSunoAPI        ChannelType = 32
	ChannelTypeDify           ChannelType = 33
	ChannelTypeJina           ChannelType = 34
	ChannelCloudflare         ChannelType = 35
	ChannelTypeSiliconFlow    ChannelType = 36
	ChannelTypeVertexAi       ChannelType = 37
	ChannelTypeMistral        ChannelType = 38
	ChannelTypeDeepSeek       ChannelType = 39
	ChannelTypeMokaAI         ChannelType = 40
	ChannelTypeVolcEngine     ChannelType = 41
	ChannelTypeBaiduV2        ChannelType = 42
	ChannelTypeXinference     ChannelType = 43
	ChannelTypeXai            ChannelType = 44
	ChannelTypeCoze           ChannelType = 45
	ChannelTypeKling          ChannelType = 46
	ChannelTypeJimeng         ChannelType = 47
	ChannelTypeVidu           ChannelType = 48
	ChannelTypeSubmodel       ChannelType = 49
	ChannelTypeDoubaoVideo    ChannelType = 50
	ChannelTypeSora           ChannelType = 51
	ChannelTypeReplicate      ChannelType = 52
	ChannelTypeCodex          ChannelType = 53
	ChannelTypeAdvancedCustom ChannelType = 54
	ChannelTypeSub2API        ChannelType = 55
	ChannelTypeNewAPI         ChannelType = 56
	ChannelTypeDummy          ChannelType = 57
)

// ChannelTypeName maps a channel type to its canonical display name.
func ChannelTypeName(t ChannelType) string {
	switch t {
	case ChannelTypeOpenAI:
		return "OpenAI"
	case ChannelTypeMidjourney:
		return "Midjourney"
	case ChannelTypeAzure:
		return "Azure"
	case ChannelTypeOllama:
		return "Ollama"
	case ChannelTypeMidjourneyPlus:
		return "Midjourney Plus"
	case ChannelTypeOpenAIMax:
		return "OpenAI-Max"
	case ChannelTypeOhMyGPT:
		return "OhMyGPT"
	case ChannelTypeCustom:
		return "Custom"
	case ChannelTypeAILS:
		return "AILS"
	case ChannelTypeAIProxy:
		return "AIProxy"
	case ChannelTypePaLM:
		return "PaLM"
	case ChannelTypeAPI2GPT:
		return "API2GPT"
	case ChannelTypeAIGC2D:
		return "AIGC2D"
	case ChannelTypeAnthropic:
		return "Anthropic"
	case ChannelTypeBaidu:
		return "Baidu"
	case ChannelTypeZhipu:
		return "Zhipu"
	case ChannelTypeAli:
		return "Aliyun"
	case ChannelTypeXunfei:
		return "Xunfei"
	case ChannelType360:
		return "360"
	case ChannelTypeOpenRouter:
		return "OpenRouter"
	case ChannelTypeAIProxyLibrary:
		return "AIProxyLibrary"
	case ChannelTypeFastGPT:
		return "FastGPT"
	case ChannelTypeTencent:
		return "Tencent"
	case ChannelTypeGemini:
		return "Gemini"
	case ChannelTypeMoonshot:
		return "Moonshot"
	case ChannelTypeZhipuV4:
		return "Zhipu v4"
	case ChannelTypePerplexity:
		return "Perplexity"
	case ChannelTypeLingYiWanWu:
		return "LingYiWanWu"
	case ChannelTypeAws:
		return "AWS"
	case ChannelTypeCohere:
		return "Cohere"
	case ChannelTypeMiniMax:
		return "MiniMax"
	case ChannelTypeSunoAPI:
		return "Suno"
	case ChannelTypeDify:
		return "Dify"
	case ChannelTypeJina:
		return "Jina"
	case ChannelCloudflare:
		return "Cloudflare"
	case ChannelTypeSiliconFlow:
		return "SiliconFlow"
	case ChannelTypeVertexAi:
		return "Vertex AI"
	case ChannelTypeMistral:
		return "Mistral"
	case ChannelTypeDeepSeek:
		return "DeepSeek"
	case ChannelTypeMokaAI:
		return "MokaAI"
	case ChannelTypeVolcEngine:
		return "VolcEngine"
	case ChannelTypeBaiduV2:
		return "Baidu V2"
	case ChannelTypeXinference:
		return "Xinference"
	case ChannelTypeXai:
		return "xAI"
	case ChannelTypeCoze:
		return "Coze"
	case ChannelTypeKling:
		return "Kling"
	case ChannelTypeJimeng:
		return "Jimeng"
	case ChannelTypeVidu:
		return "Vidu"
	case ChannelTypeSubmodel:
		return "Submodel"
	case ChannelTypeDoubaoVideo:
		return "Doubao Video"
	case ChannelTypeSora:
		return "Sora"
	case ChannelTypeReplicate:
		return "Replicate"
	case ChannelTypeCodex:
		return "Codex"
	case ChannelTypeAdvancedCustom:
		return "Advanced Custom"
	case ChannelTypeSub2API:
		return "Sub2API"
	case ChannelTypeNewAPI:
		return "NewAPI"
	case ChannelTypeDummy:
		return "Dummy"
	default:
		return "Unknown"
	}
}

// ChannelStatus values.
const (
	ChannelStatusUnknown          = 0
	ChannelStatusEnabled          = 1
	ChannelStatusAutoDisabled     = 2
	ChannelStatusManuallyDisabled = 3
)

// Channel selection / auto-ban defaults.
const (
	AutoBanTimeSeconds        = 60
	DefaultChannelWeight      = 1
	DefaultChannelPriority    = 0
	MaxChannelWeight          = 100
	DefaultChannelTestModel   = ""
)
