package catalog

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChannelTypeToAPITypeStableMappings(t *testing.T) {
	tests := map[ChannelType]APIType{
		ChannelTypeOpenAI:         APITypeOpenAI,
		ChannelTypeAnthropic:      APITypeAnthropic,
		ChannelTypeGemini:         APITypeGemini,
		ChannelTypeZhipu:          APITypeZhipu,
		ChannelTypeZhipuV4:        APITypeZhipuV4,
		ChannelTypeAli:            APITypeAli,
		ChannelTypeXunfei:         APITypeXunfei,
		ChannelTypeTencent:        APITypeTencent,
		ChannelTypeJimeng:         APITypeJimeng,
		ChannelTypeVolcEngine:     APITypeVolcEngine,
		ChannelTypeOllama:         APITypeOllama,
		ChannelTypePerplexity:     APITypePerplexity,
		ChannelTypeAws:            APITypeAws,
		ChannelTypeVertexAi:       APITypeVertexAi,
		ChannelTypeJina:           APITypeJina,
		ChannelTypeDify:           APITypeDify,
		ChannelCloudflare:         APITypeCloudflare,
		ChannelTypeMiniMax:        APITypeMiniMax,
		ChannelTypeSiliconFlow:    APITypeSiliconFlow,
		ChannelTypeOpenRouter:     APITypeOpenRouter,
		ChannelTypeXai:            APITypeXai,
		ChannelTypeCodex:          APITypeCodex,
		ChannelTypeAdvancedCustom: APITypeAdvancedCustom,
		ChannelTypeSub2API:        APITypeSub2API,
		ChannelTypeNewAPI:         APITypeNewAPI,
	}
	for channelType, expected := range tests {
		actual, ok := ChannelTypeToAPIType(channelType)
		require.True(t, ok, "channel type %d", channelType)
		assert.Equal(t, expected, actual)
	}

	for _, unsupported := range []ChannelType{
		ChannelTypeUnknown,
		ChannelTypeAzure,
		ChannelTypeMidjourney,
		ChannelTypeDummy,
		ChannelType(10_000),
	} {
		actual, ok := ChannelTypeToAPIType(unsupported)
		assert.False(t, ok)
		assert.Equal(t, APITypeOpenAI, actual)
	}
}

func TestAPITypeNumericVocabularyIsStable(t *testing.T) {
	assert.Equal(t, APIType(0), APITypeOpenAI)
	assert.Equal(t, APIType(4), APITypeZhipu)
	assert.Equal(t, APIType(5), APITypeAli)
	assert.Equal(t, APIType(6), APITypeXunfei)
	assert.Equal(t, APIType(8), APITypeTencent)
	assert.Equal(t, APIType(29), APITypeJimeng)
	assert.Equal(t, APIType(23), APITypeVolcEngine)
	assert.Equal(t, APIType(10), APITypeZhipuV4)
	assert.Equal(t, APIType(11), APITypeOllama)
	assert.Equal(t, APIType(13), APITypeAws)
	assert.Equal(t, APIType(19), APITypeVertexAi)
	assert.Equal(t, APIType(15), APITypeDify)
	assert.Equal(t, APIType(17), APITypeCloudflare)
	assert.Equal(t, APIType(25), APITypeOpenRouter)
	assert.Equal(t, APIType(32), APITypeMiniMax)
	assert.Equal(t, APIType(34), APITypeCodex)
	assert.Equal(t, APIType(38), APITypeDummy)
	assert.True(t, SupportsResponsesCompact(ChannelTypeOpenAI, APITypeOpenAI))
	assert.False(t, SupportsResponsesCompact(ChannelTypeOllama, APITypeOllama))
}
