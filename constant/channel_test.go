package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChannelTypePersistedCatalogMatchesReferenceIdentifiers(t *testing.T) {
	expected := map[ChannelType]int{
		ChannelTypeZhipu:          16,
		ChannelTypeXunfei:         18,
		ChannelTypeTencent:        23,
		ChannelTypeZhipuV4:        26,
		ChannelTypePerplexity:     27,
		ChannelTypeLingYiWanWu:    31,
		ChannelTypeAws:            33,
		ChannelTypeCohere:         34,
		ChannelTypeMiniMax:        35,
		ChannelTypeDify:           37,
		ChannelTypeSiliconFlow:    40,
		ChannelTypeVertexAi:       41,
		ChannelTypeMistral:        42,
		ChannelTypeDeepSeek:       43,
		ChannelTypeXai:            48,
		ChannelTypeJimeng:         51,
		ChannelTypeCodex:          57,
		ChannelTypeAdvancedCustom: 58,
		ChannelTypeSub2API:        59,
		ChannelTypeNewAPI:         60,
		ChannelTypeDummy:          61,
	}
	for channelType, persistedID := range expected {
		assert.Equal(t, persistedID, int(channelType), ChannelTypeName(channelType))
	}
	for _, reserved := range []int{28, 29, 30, 32} {
		assert.Equal(t, "", ChannelBaseURLs[reserved], "reserved catalog ID %d must remain empty", reserved)
		assert.Equal(t, "Unknown", ChannelTypeName(ChannelType(reserved)))
	}
	assert.Len(t, ChannelBaseURLs, int(ChannelTypeDummy)+1)
	assert.Equal(t, "https://open.bigmodel.cn", ChannelBaseURLs[int(ChannelTypeZhipu)])
	assert.Equal(t, "https://open.bigmodel.cn", ChannelBaseURLs[int(ChannelTypeZhipuV4)])
	assert.Equal(t, "", ChannelBaseURLs[int(ChannelTypeXunfei)])
	assert.Equal(t, "Xunfei", ChannelTypeName(ChannelTypeXunfei))
	assert.Equal(t, "https://hunyuan.tencentcloudapi.com", ChannelBaseURLs[int(ChannelTypeTencent)])
	assert.Equal(t, "Tencent", ChannelTypeName(ChannelTypeTencent))
	assert.Equal(t, "https://api.deepseek.com", ChannelBaseURLs[int(ChannelTypeDeepSeek)])
	assert.Equal(t, "", ChannelBaseURLs[int(ChannelTypeVertexAi)])
	assert.Equal(t, "Vertex AI", ChannelTypeName(ChannelTypeVertexAi))
	assert.Equal(t, "https://api.minimax.chat", ChannelBaseURLs[int(ChannelTypeMiniMax)])
	assert.Equal(t, "MiniMax", ChannelTypeName(ChannelTypeMiniMax))
	assert.Equal(t, "https://api.dify.ai", ChannelBaseURLs[int(ChannelTypeDify)])
	assert.Equal(t, "Dify", ChannelTypeName(ChannelTypeDify))
	assert.Equal(t, "https://visual.volcengineapi.com", ChannelBaseURLs[int(ChannelTypeJimeng)])
}
