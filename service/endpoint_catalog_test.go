package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestModelSupportedEndpointTypesUsesOnlyAuthorizedEnabledImplementedChannels(t *testing.T) {
	initTestDB(t)
	channels := []model.Channel{
		{Type: int(constant.ChannelTypeOpenAI), Key: "one", Name: "one", Status: constant.ChannelStatusEnabled},
		{Type: int(constant.ChannelTypeAnthropic), Key: "two", Name: "two", Status: constant.ChannelStatusEnabled},
		{Type: int(constant.ChannelTypeGemini), Key: "three", Name: "three", Status: constant.ChannelStatusManuallyDisabled},
		{Type: int(constant.ChannelTypeAws), Key: "four", Name: "four", Status: constant.ChannelStatusEnabled},
		{Type: int(constant.ChannelTypeOllama), Key: "five", Name: "five", Status: constant.ChannelStatusEnabled},
		{Type: int(constant.ChannelTypeAdvancedCustom), Key: "six", Name: "six", Status: constant.ChannelStatusEnabled,
			OtherSettings: `{"advanced_custom":{"advanced_routes":[
				{"incoming_path":"/v1/messages","upstream_path":"/claude","models":["re:^advanced-"]},
				{"incoming_path":"/v1/responses","upstream_path":"/responses","models":["advanced-responses"]},
				{"incoming_path":"/v1beta/models/{model}:generateContent","upstream_path":"/gemini/{model}"},
				{"incoming_path":"/v1/chat/completions","upstream_path":"/chat"}
			]}}`},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.Ability{
		{Group: "default", Model: "shared", ChannelId: channels[0].Id, Enabled: true},
		{Group: "default", Model: "gpt-image-1", ChannelId: channels[0].Id, Enabled: true},
		{Group: "default", Model: "o3-pro", ChannelId: channels[0].Id, Enabled: true},
		{Group: "vip", Model: "shared", ChannelId: channels[1].Id, Enabled: true},
		{Group: "vip", Model: "disabled-channel", ChannelId: channels[2].Id, Enabled: true},
		{Group: "vip", Model: "aws-channel", ChannelId: channels[3].Id, Enabled: true},
		{Group: "default", Model: "ollama-model", ChannelId: channels[4].Id, Enabled: true},
		{Group: "private", Model: "private-model", ChannelId: channels[0].Id, Enabled: true},
		{Group: "default", Model: "advanced-claude", ChannelId: channels[5].Id, Enabled: true},
		{Group: "default", Model: "advanced-responses", ChannelId: channels[5].Id, Enabled: true},
	}).Error)
	require.NoError(t, InitAbilityCache())

	got, err := GetModelSupportedEndpointTypes([]string{"vip", "default"}, map[string]bool{
		"shared": true, "gpt-image-1": true, "o3-pro": true,
		"disabled-channel": true, "aws-channel": true, "ollama-model": true,
	})
	require.NoError(t, err)
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeOpenAI,
		constant.EndpointTypeAnthropic,
	}, got["shared"])
	assert.Empty(t, got["disabled-channel"])
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeOpenAI,
	}, got["aws-channel"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, got["ollama-model"])
	assert.NotContains(t, got, "private-model")
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeImageGeneration,
		constant.EndpointTypeOpenAI,
	}, got["gpt-image-1"])
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}, got["o3-pro"])

	advanced, err := GetModelSupportedEndpointTypes([]string{"default"}, map[string]bool{
		"advanced-claude": true, "advanced-responses": true,
	})
	require.NoError(t, err)
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeGemini,
		constant.EndpointTypeOpenAI,
	}, advanced["advanced-claude"])
	assert.Equal(t, []constant.EndpointType{
		constant.EndpointTypeAnthropic,
		constant.EndpointTypeOpenAIResponse,
		constant.EndpointTypeGemini,
		constant.EndpointTypeOpenAI,
	}, advanced["advanced-responses"])

	got["shared"][0] = constant.EndpointTypeOpenAIVideo
	again, err := GetModelSupportedEndpointTypes([]string{"default", "vip"}, map[string]bool{"shared": true})
	require.NoError(t, err)
	assert.Equal(t, constant.EndpointTypeOpenAI, again["shared"][0])
}

func TestModelSupportedEndpointTypesBoundsAndUnavailableDatabase(t *testing.T) {
	tooManyGroups := make([]string, endpointCatalogMaxGroups+1)
	_, err := GetModelSupportedEndpointTypes(tooManyGroups, map[string]bool{"model": true})
	assert.ErrorIs(t, err, ErrEndpointCatalogTooLarge)

	models := make(map[string]bool, endpointCatalogMaxModels+1)
	for index := 0; index <= endpointCatalogMaxModels; index++ {
		models[itoa(index+1)] = true
	}
	_, err = GetModelSupportedEndpointTypes([]string{"default"}, models)
	assert.ErrorIs(t, err, ErrEndpointCatalogTooLarge)

	oldDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = oldDB })
	abilityMu.Lock()
	abilityCache = map[string][]*model.Ability{
		abilityKey("default", "model"): {{Group: "default", Model: "model", ChannelId: 1, Enabled: true}},
	}
	abilityMu.Unlock()
	_, err = GetModelSupportedEndpointTypes([]string{"default"}, map[string]bool{"model": true})
	assert.ErrorIs(t, err, ErrEndpointCatalogUnavailable)
}

func TestEndpointCatalogIncludesDedicatedCohereCloudflareDifyAndBaiduAdapters(t *testing.T) {
	for _, channelType := range []constant.ChannelType{
		constant.ChannelTypeCohere,
		constant.ChannelCloudflare,
		constant.ChannelTypeDify,
		constant.ChannelTypeBaidu,
		constant.ChannelTypeBaiduV2,
	} {
		assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
			EndpointTypesForChannelTypes([]int{int(channelType)}), int(channelType))
	}

	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeAnthropic, constant.EndpointTypeOpenAI},
		EndpointTypesForChannelTypes([]int{int(constant.ChannelTypeAws)}))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		EndpointTypesForModelAndChannelTypes("amazon.nova-pro-v1:0", []int{int(constant.ChannelTypeAws)}))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAIVideo},
		EndpointTypesForChannelTypes([]int{int(constant.ChannelTypeSora)}))
}

func TestEndpointCatalogKeepsDifyOnChatForImageNamedApplications(t *testing.T) {
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		EndpointTypesForModelAndChannelTypes("gpt-image-1", []int{int(constant.ChannelTypeDify)}))
}

func TestEndpointCatalogIncludesMiniMaxReferenceAdvertisedSurface(t *testing.T) {
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		EndpointTypesForChannelTypes([]int{int(constant.ChannelTypeMiniMax)}))
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI},
		EndpointTypesForModelAndChannelTypes("image-01", []int{int(constant.ChannelTypeMiniMax)}))
}
