package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
)

func TestDefaultEndpointInfoContractAndDefensiveCopy(t *testing.T) {
	want := map[constant.EndpointType]EndpointInfo{
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
	for endpointType, expected := range want {
		actual, ok := GetDefaultEndpointInfo(endpointType)
		require.True(t, ok, endpointType)
		assert.Equal(t, expected, actual)
	}
	copyOne := DefaultEndpointInfo()
	copyOne[string(constant.EndpointTypeOpenAI)] = EndpointInfo{Path: "/mutated", Method: "DELETE"}
	copyTwo := DefaultEndpointInfo()
	assert.Equal(t, "/v1/chat/completions", copyTwo[string(constant.EndpointTypeOpenAI)].Path)
}

func TestEndpointTypesByChannelTypePreferenceOrderAndOwnership(t *testing.T) {
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeOpenAI,
	}, GetEndpointTypesByChannelType(constant.ChannelTypeAnthropic))
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeGemini,
		constant.EndpointTypeOpenAI,
	}, GetEndpointTypesByChannelType(constant.ChannelTypeGemini))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelType(constant.ChannelTypeMiniMax))
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeOpenAIResponseCompact,
		constant.EndpointTypeOpenAIAlphaSearch,
	}, GetEndpointTypesByChannelType(constant.ChannelTypeCodex))
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeJinaRerank,
	}, GetEndpointTypesByChannelType(constant.ChannelTypeJina))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelType(constant.ChannelTypeOpenRouter))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelType(constant.ChannelTypeOllama))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(constant.ChannelTypeAws, "nova-pro-v1:0"))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(constant.ChannelTypeAws, "claude-3-5-sonnet-20240620"))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIVideo},
		GetEndpointTypesByChannelType(constant.ChannelTypeSora))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeEmbeddings},
		GetEndpointTypesByChannelType(constant.ChannelTypeMokaAI))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeImageGeneration},
		GetEndpointTypesByChannelType(constant.ChannelTypeReplicate))

	owned := GetEndpointTypesByChannelType(constant.ChannelTypeNewAPI)
	owned[0] = constant.EndpointTypeOpenAIVideo
	assert.Equal(t, constant.EndpointTypeOpenAI, GetEndpointTypesByChannelType(constant.ChannelTypeNewAPI)[0])

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIResponse},
		GetEndpointTypesByChannelTypeForModel(constant.ChannelTypeOpenAI, "o3-deep-research"))
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeImageGeneration,
		constant.EndpointTypeOpenAI,
	}, GetEndpointTypesByChannelTypeForModel(constant.ChannelTypeOpenAI, "gpt-image-1"))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(constant.ChannelTypeMiniMax, "image-01-live"))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(constant.ChannelTypeDify, "gpt-image-1"),
		"Dify applications expose only chat-messages even when their configured model name looks image-capable")
	assert.False(t, IsImageGenerationModel("ordinary-chat-model"))
}
