package common

import (
	"strings"

	"github.com/tokenrouter/tokenrouter/constant"
)

// EndpointInfo is the default request contract for an advertised endpoint.
type EndpointInfo struct {
	Path   string `json:"path"`
	Method string `json:"method"`
}

var defaultEndpointInfo = map[constant.EndpointType]EndpointInfo{
	constant.EndpointTypeOpenAI:                {Path: "/v1/chat/completions", Method: "POST"},
	constant.EndpointTypeOpenAIResponse:        {Path: "/v1/responses", Method: "POST"},
	constant.EndpointTypeOpenAIResponseCompact: {Path: "/v1/responses/compact", Method: "POST"},
	constant.EndpointTypeOpenAIAlphaSearch:     {Path: "/v1/alpha/search", Method: "POST"},
	constant.EndpointTypeAnthropic:             {Path: "/v1/messages", Method: "POST"},
	constant.EndpointTypeGemini:                {Path: "/v1beta/models/{model}:generateContent", Method: "POST"},
	constant.EndpointTypeJinaRerank:            {Path: "/v1/rerank", Method: "POST"},
	constant.EndpointTypeImageGeneration:       {Path: "/v1/images/generations", Method: "POST"},
	constant.EndpointTypeEmbeddings:            {Path: "/v1/embeddings", Method: "POST"},
	constant.EndpointTypeOpenAIVideo:           {Path: "/v1/videos", Method: "POST"},
}

// GetDefaultEndpointInfo returns the built-in contract for a known endpoint.
func GetDefaultEndpointInfo(endpointType constant.EndpointType) (EndpointInfo, bool) {
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
func GetEndpointTypesByChannelType(channelType constant.ChannelType) []constant.EndpointType {
	return GetEndpointTypesByChannelTypeForModel(channelType, "")
}

// GetEndpointTypesByChannelTypeForModel applies model-specific compatibility
// rules in addition to the channel's protocol family.
func GetEndpointTypesByChannelTypeForModel(channelType constant.ChannelType, modelName string) []constant.EndpointType {
	var result []constant.EndpointType
	switch channelType {
	case constant.ChannelTypeJina:
		result = []constant.EndpointType{constant.EndpointTypeJinaRerank}
	case constant.ChannelTypeDify:
		// A Dify channel represents an application whose active reference
		// contract is chat-messages only. Model-like application names must not
		// accidentally advertise Responses or image-generation routes.
		result = []constant.EndpointType{constant.EndpointTypeOpenAI}
	case constant.ChannelTypeMokaAI:
		result = []constant.EndpointType{constant.EndpointTypeEmbeddings}
	case constant.ChannelTypeReplicate:
		result = []constant.EndpointType{constant.EndpointTypeImageGeneration}
	case constant.ChannelTypeAws:
		if modelName != "" && strings.Contains(strings.ToLower(modelName), "nova-") {
			result = []constant.EndpointType{constant.EndpointTypeOpenAI}
		} else {
			result = []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI}
		}
	case constant.ChannelTypeAnthropic:
		result = []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI}
	case constant.ChannelTypeVertexAi, constant.ChannelTypeGemini:
		result = []constant.EndpointType{constant.EndpointTypeGemini, constant.EndpointTypeOpenAI}
	case constant.ChannelTypeXai:
		result = []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse}
	case constant.ChannelTypeSora:
		result = []constant.EndpointType{constant.EndpointTypeOpenAIVideo}
	case constant.ChannelTypeSub2API, constant.ChannelTypeNewAPI:
		result = []constant.EndpointType{
			constant.EndpointTypeOpenAI,
			constant.EndpointTypeOpenAIResponse,
			constant.EndpointTypeOpenAIResponseCompact,
			constant.EndpointTypeAnthropic,
			constant.EndpointTypeGemini,
			constant.EndpointTypeOpenAIAlphaSearch,
		}
	case constant.ChannelTypeCodex:
		result = []constant.EndpointType{
			constant.EndpointTypeOpenAIResponse,
			constant.EndpointTypeOpenAIResponseCompact,
			constant.EndpointTypeOpenAIAlphaSearch,
		}
	default:
		if IsOpenAIResponseOnlyModel(modelName) {
			result = []constant.EndpointType{constant.EndpointTypeOpenAIResponse}
		} else {
			result = []constant.EndpointType{constant.EndpointTypeOpenAI}
		}
	}
	if channelType != constant.ChannelTypeDify && IsImageGenerationModel(modelName) {
		result = append([]constant.EndpointType{constant.EndpointTypeImageGeneration}, result...)
	}
	return append([]constant.EndpointType(nil), result...)
}
