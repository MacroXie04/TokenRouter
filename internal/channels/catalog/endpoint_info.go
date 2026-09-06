package catalog

import (
	"strings"
)

// EndpointInfo is the default request contract for an advertised endpoint.
type EndpointInfo struct {
	Path   string `json:"path"`
	Method string `json:"method"`
}

var defaultEndpointInfo = map[EndpointType]EndpointInfo{
	EndpointTypeOpenAI:                {Path: "/v1/chat/completions", Method: "POST"},
	EndpointTypeOpenAIResponse:        {Path: "/v1/responses", Method: "POST"},
	EndpointTypeOpenAIResponseCompact: {Path: "/v1/responses/compact", Method: "POST"},
	EndpointTypeOpenAIAlphaSearch:     {Path: "/v1/alpha/search", Method: "POST"},
	EndpointTypeAnthropic:             {Path: "/v1/messages", Method: "POST"},
	EndpointTypeGemini:                {Path: "/v1beta/models/{model}:generateContent", Method: "POST"},
	EndpointTypeJinaRerank:            {Path: "/v1/rerank", Method: "POST"},
	EndpointTypeImageGeneration:       {Path: "/v1/images/generations", Method: "POST"},
	EndpointTypeEmbeddings:            {Path: "/v1/embeddings", Method: "POST"},
	EndpointTypeOpenAIVideo:           {Path: "/v1/videos", Method: "POST"},
}

// GetDefaultEndpointInfo returns the built-in contract for a known endpoint.
func GetDefaultEndpointInfo(endpointType EndpointType) (EndpointInfo, bool) {
	info, ok := defaultEndpointInfo[endpointType]
	return info, ok
}

// DefaultEndpointInfo returns an owned copy suitable for API serialization.
func DefaultEndpointInfo() map[string]EndpointInfo {
	result := make(map[string]EndpointInfo, len(defaultEndpointInfo))
	for endpointType, info := range defaultEndpointInfo {
		result[string(endpointType)] = info
	}
	return result
}

// GetEndpointTypesByChannelType returns endpoint types in preference order.
// It describes protocol capability only; relay dispatch still independently
// rejects providers whose adapters are not implemented.
func GetEndpointTypesByChannelType(channelType ChannelType) []EndpointType {
	return GetEndpointTypesByChannelTypeForModel(channelType, "")
}

// GetEndpointTypesByChannelTypeForModel applies model-specific compatibility
// rules in addition to the channel's protocol family.
func GetEndpointTypesByChannelTypeForModel(channelType ChannelType, modelName string) []EndpointType {
	var result []EndpointType
	switch channelType {
	case ChannelTypeJina:
		result = []EndpointType{EndpointTypeJinaRerank}
	case ChannelTypeDify:
		// A Dify channel represents an application whose active reference
		// contract is chat-messages only. Model-like application names must not
		// accidentally advertise Responses or image-generation routes.
		result = []EndpointType{EndpointTypeOpenAI}
	case ChannelTypeMokaAI:
		result = []EndpointType{EndpointTypeEmbeddings}
	case ChannelTypeReplicate:
		result = []EndpointType{EndpointTypeImageGeneration}
	case ChannelTypeAws:
		if modelName != "" && strings.Contains(strings.ToLower(modelName), "nova-") {
			result = []EndpointType{EndpointTypeOpenAI}
		} else {
			result = []EndpointType{EndpointTypeAnthropic, EndpointTypeOpenAI}
		}
	case ChannelTypeAnthropic:
		result = []EndpointType{EndpointTypeAnthropic, EndpointTypeOpenAI}
	case ChannelTypeVertexAi, ChannelTypeGemini:
		result = []EndpointType{EndpointTypeGemini, EndpointTypeOpenAI}
	case ChannelTypeXai:
		result = []EndpointType{EndpointTypeOpenAI, EndpointTypeOpenAIResponse}
	case ChannelTypeSora:
		result = []EndpointType{EndpointTypeOpenAIVideo}
	case ChannelTypeSub2API, ChannelTypeNewAPI:
		result = []EndpointType{
			EndpointTypeOpenAI,
			EndpointTypeOpenAIResponse,
			EndpointTypeOpenAIResponseCompact,
			EndpointTypeAnthropic,
			EndpointTypeGemini,
			EndpointTypeOpenAIAlphaSearch,
		}
	case ChannelTypeCodex:
		result = []EndpointType{
			EndpointTypeOpenAIResponse,
			EndpointTypeOpenAIResponseCompact,
			EndpointTypeOpenAIAlphaSearch,
		}
	default:
		if IsOpenAIResponseOnlyModel(modelName) {
			result = []EndpointType{EndpointTypeOpenAIResponse}
		} else {
			result = []EndpointType{EndpointTypeOpenAI}
		}
	}
	if channelType != ChannelTypeDify && IsImageGenerationModel(modelName) {
		result = append([]EndpointType{EndpointTypeImageGeneration}, result...)
	}
	return append([]EndpointType(nil), result...)
}
