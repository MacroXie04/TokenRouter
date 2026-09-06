package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
)

func TestChannelTypeToAPITypeStableMappings(t *testing.T) {
	tests := map[constant.ChannelType]constant.APIType{
		constant.ChannelTypeOpenAI:         constant.APITypeOpenAI,
		constant.ChannelTypeAnthropic:      constant.APITypeAnthropic,
		constant.ChannelTypeGemini:         constant.APITypeGemini,
		constant.ChannelTypeZhipu:          constant.APITypeZhipu,
		constant.ChannelTypeZhipuV4:        constant.APITypeZhipuV4,
		constant.ChannelTypeAli:            constant.APITypeAli,
		constant.ChannelTypeXunfei:         constant.APITypeXunfei,
		constant.ChannelTypeTencent:        constant.APITypeTencent,
		constant.ChannelTypeJimeng:         constant.APITypeJimeng,
		constant.ChannelTypeVolcEngine:     constant.APITypeVolcEngine,
		constant.ChannelTypeOllama:         constant.APITypeOllama,
		constant.ChannelTypePerplexity:     constant.APITypePerplexity,
		constant.ChannelTypeAws:            constant.APITypeAws,
		constant.ChannelTypeVertexAi:       constant.APITypeVertexAi,
		constant.ChannelTypeJina:           constant.APITypeJina,
		constant.ChannelTypeDify:           constant.APITypeDify,
		constant.ChannelCloudflare:         constant.APITypeCloudflare,
		constant.ChannelTypeMiniMax:        constant.APITypeMiniMax,
		constant.ChannelTypeSiliconFlow:    constant.APITypeSiliconFlow,
		constant.ChannelTypeOpenRouter:     constant.APITypeOpenRouter,
		constant.ChannelTypeXai:            constant.APITypeXai,
		constant.ChannelTypeCodex:          constant.APITypeCodex,
		constant.ChannelTypeAdvancedCustom: constant.APITypeAdvancedCustom,
		constant.ChannelTypeSub2API:        constant.APITypeSub2API,
		constant.ChannelTypeNewAPI:         constant.APITypeNewAPI,
	}
	for channelType, expected := range tests {
		actual, ok := ChannelTypeToAPIType(channelType)
		require.True(t, ok, "channel type %d", channelType)
		assert.Equal(t, expected, actual)
	}

	for _, unsupported := range []constant.ChannelType{
		constant.ChannelTypeUnknown,
		constant.ChannelTypeAzure,
		constant.ChannelTypeMidjourney,
		constant.ChannelTypeDummy,
		constant.ChannelType(10_000),
	} {
		actual, ok := ChannelTypeToAPIType(unsupported)
		assert.False(t, ok)
		assert.Equal(t, constant.APITypeOpenAI, actual)
	}
}

func TestAPITypeNumericVocabularyIsStable(t *testing.T) {
	assert.Equal(t, constant.APIType(0), constant.APITypeOpenAI)
	assert.Equal(t, constant.APIType(4), constant.APITypeZhipu)
	assert.Equal(t, constant.APIType(5), constant.APITypeAli)
	assert.Equal(t, constant.APIType(6), constant.APITypeXunfei)
	assert.Equal(t, constant.APIType(8), constant.APITypeTencent)
	assert.Equal(t, constant.APIType(29), constant.APITypeJimeng)
	assert.Equal(t, constant.APIType(23), constant.APITypeVolcEngine)
	assert.Equal(t, constant.APIType(10), constant.APITypeZhipuV4)
	assert.Equal(t, constant.APIType(11), constant.APITypeOllama)
	assert.Equal(t, constant.APIType(13), constant.APITypeAws)
	assert.Equal(t, constant.APIType(19), constant.APITypeVertexAi)
	assert.Equal(t, constant.APIType(15), constant.APITypeDify)
	assert.Equal(t, constant.APIType(17), constant.APITypeCloudflare)
	assert.Equal(t, constant.APIType(25), constant.APITypeOpenRouter)
	assert.Equal(t, constant.APIType(32), constant.APITypeMiniMax)
	assert.Equal(t, constant.APIType(34), constant.APITypeCodex)
	assert.Equal(t, constant.APIType(38), constant.APITypeDummy)
	assert.True(t, SupportsResponsesCompact(constant.ChannelTypeOpenAI, constant.APITypeOpenAI))
	assert.False(t, SupportsResponsesCompact(constant.ChannelTypeOllama, constant.APITypeOllama))
}
