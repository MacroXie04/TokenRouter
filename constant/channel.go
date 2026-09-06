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
	// Values 28-30 and 32 are reserved by the reference catalog. Keep these
	// gaps: channel type is a persisted and externally visible identifier.
	ChannelTypeLingYiWanWu    ChannelType = 31
	ChannelTypeAws            ChannelType = 33
	ChannelTypeCohere         ChannelType = 34
	ChannelTypeMiniMax        ChannelType = 35
	ChannelTypeSunoAPI        ChannelType = 36
	ChannelTypeDify           ChannelType = 37
	ChannelTypeJina           ChannelType = 38
	ChannelCloudflare         ChannelType = 39
	ChannelTypeSiliconFlow    ChannelType = 40
	ChannelTypeVertexAi       ChannelType = 41
	ChannelTypeMistral        ChannelType = 42
	ChannelTypeDeepSeek       ChannelType = 43
	ChannelTypeMokaAI         ChannelType = 44
	ChannelTypeVolcEngine     ChannelType = 45
	ChannelTypeBaiduV2        ChannelType = 46
	ChannelTypeXinference     ChannelType = 47
	ChannelTypeXai            ChannelType = 48
	ChannelTypeCoze           ChannelType = 49
	ChannelTypeKling          ChannelType = 50
	ChannelTypeJimeng         ChannelType = 51
	ChannelTypeVidu           ChannelType = 52
	ChannelTypeSubmodel       ChannelType = 53
	ChannelTypeDoubaoVideo    ChannelType = 54
	ChannelTypeSora           ChannelType = 55
	ChannelTypeReplicate      ChannelType = 56
	ChannelTypeCodex          ChannelType = 57
	ChannelTypeAdvancedCustom ChannelType = 58
	ChannelTypeSub2API        ChannelType = 59
	ChannelTypeNewAPI         ChannelType = 60
	ChannelTypeDummy          ChannelType = 61
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

// IsOpenAICompatibleChannelType reports whether a provider supports the broad
// OpenAI-compatible surface used by relay dispatch, native-format conversion,
// model discovery, and operational probes. Narrow OpenAI-family providers such
// as Jina and Submodel are registered explicitly at dispatch so their smaller
// endpoint sets cannot accidentally opt into these additional capabilities.
func IsOpenAICompatibleChannelType(t ChannelType) bool {
	switch t {
	case ChannelTypeOpenAI,
		ChannelTypeAzure,
		ChannelTypeOllama,
		ChannelTypeOpenAIMax,
		ChannelTypeOhMyGPT,
		ChannelTypeCustom,
		ChannelTypeAILS,
		ChannelTypeAIProxy,
		ChannelTypeAPI2GPT,
		ChannelTypeAIGC2D,
		ChannelType360,
		ChannelTypeOpenRouter,
		ChannelTypeFastGPT,
		ChannelTypePerplexity,
		ChannelTypeLingYiWanWu,
		ChannelTypeSiliconFlow,
		ChannelTypeMistral,
		ChannelTypeDeepSeek,
		ChannelTypeXinference,
		ChannelTypeXai,
		ChannelTypeSub2API,
		ChannelTypeNewAPI:
		return true
	default:
		return false
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
	AutoBanTimeSeconds      = 60
	DefaultChannelWeight    = 1
	DefaultChannelPriority  = 0
	MaxChannelWeight        = 100
	DefaultChannelTestModel = ""
)

// ChannelBaseURLs maps a channel type to its default upstream base URL
// (factual provider endpoints; empty where the type has no default).
var ChannelBaseURLs = []string{
	"",                                    // 0 Unknown
	"https://api.openai.com",              // 1 OpenAI
	"",                                    // 2 Midjourney
	"",                                    // 3 Azure
	"http://localhost:11434",              // 4 Ollama
	"",                                    // 5 MidjourneyPlus
	"https://api.openaimax.com",           // 6 OpenAIMax
	"https://api.ohmygpt.com",             // 7 OhMyGPT
	"",                                    // 8 Custom
	"https://api.caipacity.com",           // 9 AILS
	"https://api.aiproxy.io",              // 10 AIProxy
	"",                                    // 11 PaLM
	"https://api.api2gpt.com",             // 12 API2GPT
	"https://api.aigc2d.com",              // 13 AIGC2D
	"https://api.anthropic.com",           // 14 Anthropic
	"https://aip.baidubce.com",            // 15 Baidu
	"https://open.bigmodel.cn",            // 16 Zhipu
	"https://dashscope.aliyuncs.com",      // 17 Ali
	"",                                    // 18 Xunfei
	"https://api.360.cn",                  // 19 360
	"https://openrouter.ai/api",           // 20 OpenRouter
	"https://api.aiproxy.io",              // 21 AIProxyLibrary
	"https://fastgpt.run/api/openapi",     // 22 FastGPT
	"https://hunyuan.tencentcloudapi.com", // 23 Tencent
	"https://generativelanguage.googleapis.com", // 24 Gemini
	"https://api.moonshot.cn",                   // 25 Moonshot
	"https://open.bigmodel.cn",                  // 26 ZhipuV4
	"https://api.perplexity.ai",                 // 27 Perplexity
	"",                                          // 28 reserved
	"",                                          // 29 reserved
	"",                                          // 30 reserved
	"https://api.lingyiwanwu.com",               // 31 LingYiWanWu
	"",                                          // 32 reserved
	"",                                          // 33 Aws
	"https://api.cohere.ai",                     // 34 Cohere
	"https://api.minimax.chat",                  // 35 MiniMax
	"",                                          // 36 SunoAPI
	"https://api.dify.ai",                       // 37 Dify
	"https://api.jina.ai",                       // 38 Jina
	"https://api.cloudflare.com",                // 39 Cloudflare
	"https://api.siliconflow.cn",                // 40 SiliconFlow
	"",                                          // 41 VertexAi
	"https://api.mistral.ai",                    // 42 Mistral
	"https://api.deepseek.com",                  // 43 DeepSeek
	"https://api.moka.ai",                       // 44 MokaAI
	"https://ark.cn-beijing.volces.com",         // 45 VolcEngine
	"https://qianfan.baidubce.com",              // 46 BaiduV2
	"",                                          // 47 Xinference
	"https://api.x.ai",                          // 48 Xai
	"https://api.coze.cn",                       // 49 Coze
	"https://api.klingai.com",                   // 50 Kling
	"https://visual.volcengineapi.com",          // 51 Jimeng
	"https://api.vidu.cn",                       // 52 Vidu
	"https://llm.submodel.ai",                   // 53 Submodel
	"https://ark.cn-beijing.volces.com",         // 54 DoubaoVideo
	"https://api.openai.com",                    // 55 Sora
	"https://api.replicate.com",                 // 56 Replicate
	"https://chatgpt.com",                       // 57 Codex
	"",                                          // 58 AdvancedCustom
	"",                                          // 59 Sub2API
	"",                                          // 60 NewAPI
	"",                                          // 61 Dummy
}
