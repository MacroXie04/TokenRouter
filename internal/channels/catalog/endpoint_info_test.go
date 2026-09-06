package catalog

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDefaultEndpointInfoContractAndDefensiveCopy(t *testing.T) {
	want := map[EndpointType]EndpointInfo{
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
	for endpointType, expected := range want {
		actual, ok := GetDefaultEndpointInfo(endpointType)
		require.True(t, ok, endpointType)
		assert.Equal(t, expected, actual)
	}
	copyOne := DefaultEndpointInfo()
	copyOne[string(EndpointTypeOpenAI)] = EndpointInfo{Path: "/mutated", Method: "DELETE"}
	copyTwo := DefaultEndpointInfo()
	assert.Equal(t, "/v1/chat/completions", copyTwo[string(EndpointTypeOpenAI)].Path)
}

func TestEndpointTypesByChannelTypePreferenceOrderAndOwnership(t *testing.T) {
	assert.Equal(t, []EndpointType{
		EndpointTypeAnthropic,
		EndpointTypeOpenAI,
	}, GetEndpointTypesByChannelType(ChannelTypeAnthropic))
	assert.Equal(t, []EndpointType{
		EndpointTypeGemini,
		EndpointTypeOpenAI,
	}, GetEndpointTypesByChannelType(ChannelTypeGemini))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAI},
		GetEndpointTypesByChannelType(ChannelTypeMiniMax))
	assert.Equal(t, []EndpointType{
		EndpointTypeOpenAIResponse,
		EndpointTypeOpenAIResponseCompact,
		EndpointTypeOpenAIAlphaSearch,
	}, GetEndpointTypesByChannelType(ChannelTypeCodex))
	assert.Equal(t, []EndpointType{
		EndpointTypeJinaRerank,
	}, GetEndpointTypesByChannelType(ChannelTypeJina))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAI},
		GetEndpointTypesByChannelType(ChannelTypeOpenRouter))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAI},
		GetEndpointTypesByChannelType(ChannelTypeOllama))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(ChannelTypeAws, "nova-pro-v1:0"))
	assert.Equal(t, []EndpointType{EndpointTypeAnthropic, EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(ChannelTypeAws, "claude-3-5-sonnet-20240620"))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAIVideo},
		GetEndpointTypesByChannelType(ChannelTypeSora))
	assert.Equal(t, []EndpointType{EndpointTypeEmbeddings},
		GetEndpointTypesByChannelType(ChannelTypeMokaAI))
	assert.Equal(t, []EndpointType{EndpointTypeImageGeneration},
		GetEndpointTypesByChannelType(ChannelTypeReplicate))

	owned := GetEndpointTypesByChannelType(ChannelTypeNewAPI)
	owned[0] = EndpointTypeOpenAIVideo
	assert.Equal(t, EndpointTypeOpenAI, GetEndpointTypesByChannelType(ChannelTypeNewAPI)[0])

	assert.Equal(t, []EndpointType{EndpointTypeOpenAIResponse},
		GetEndpointTypesByChannelTypeForModel(ChannelTypeOpenAI, "o3-deep-research"))
	assert.Equal(t, []EndpointType{
		EndpointTypeImageGeneration,
		EndpointTypeOpenAI,
	}, GetEndpointTypesByChannelTypeForModel(ChannelTypeOpenAI, "gpt-image-1"))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(ChannelTypeMiniMax, "image-01-live"))
	assert.Equal(t, []EndpointType{EndpointTypeOpenAI},
		GetEndpointTypesByChannelTypeForModel(ChannelTypeDify, "gpt-image-1"),
		"Dify applications expose only chat-messages even when their configured model name looks image-capable")
	assert.False(t, IsImageGenerationModel("ordinary-chat-model"))
}
