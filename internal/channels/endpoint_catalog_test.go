package channels

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"testing"
)

func TestModelSupportedEndpointTypesUsesOnlyAuthorizedEnabledImplementedChannels(t *testing.T) {
	testutil.InitTestDB(t)
	channels := []model.Channel{
		{Type: int(channelcatalog.ChannelTypeOpenAI), Key: "one", Name: "one", Status: channelcatalog.ChannelStatusEnabled},
		{Type: int(channelcatalog.ChannelTypeAnthropic), Key: "two", Name: "two", Status: channelcatalog.ChannelStatusEnabled},
		{Type: int(channelcatalog.ChannelTypeGemini), Key: "three", Name: "three", Status: channelcatalog.ChannelStatusManuallyDisabled},
		{Type: int(channelcatalog.ChannelTypeAws), Key: "four", Name: "four", Status: channelcatalog.ChannelStatusEnabled},
		{Type: int(channelcatalog.ChannelTypeOllama), Key: "five", Name: "five", Status: channelcatalog.ChannelStatusEnabled},
		{Type: int(channelcatalog.ChannelTypeAdvancedCustom), Key: "six", Name: "six", Status: channelcatalog.ChannelStatusEnabled,
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
	assert.Equal(t, []channelcatalog.EndpointType{
		channelcatalog.EndpointTypeOpenAI,
		channelcatalog.EndpointTypeAnthropic,
	}, got["shared"])
	assert.Empty(t, got["disabled-channel"])
	assert.Equal(t, []channelcatalog.EndpointType{
		channelcatalog.EndpointTypeAnthropic,
		channelcatalog.EndpointTypeOpenAI,
	}, got["aws-channel"])
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAI}, got["ollama-model"])
	assert.NotContains(t, got, "private-model")
	assert.Equal(t, []channelcatalog.EndpointType{
		channelcatalog.EndpointTypeImageGeneration,
		channelcatalog.EndpointTypeOpenAI,
	}, got["gpt-image-1"])
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAIResponse}, got["o3-pro"])

	advanced, err := GetModelSupportedEndpointTypes([]string{"default"}, map[string]bool{
		"advanced-claude": true, "advanced-responses": true,
	})
	require.NoError(t, err)
	assert.Equal(t, []channelcatalog.EndpointType{
		channelcatalog.EndpointTypeAnthropic,
		channelcatalog.EndpointTypeGemini,
		channelcatalog.EndpointTypeOpenAI,
	}, advanced["advanced-claude"])
	assert.Equal(t, []channelcatalog.EndpointType{
		channelcatalog.EndpointTypeAnthropic,
		channelcatalog.EndpointTypeOpenAIResponse,
		channelcatalog.EndpointTypeGemini,
		channelcatalog.EndpointTypeOpenAI,
	}, advanced["advanced-responses"])

	got["shared"][0] = channelcatalog.EndpointTypeOpenAIVideo
	again, err := GetModelSupportedEndpointTypes([]string{"default", "vip"}, map[string]bool{"shared": true})
	require.NoError(t, err)
	assert.Equal(t, channelcatalog.EndpointTypeOpenAI, again["shared"][0])
}

func TestModelSupportedEndpointTypesBoundsAndUnavailableDatabase(t *testing.T) {
	tooManyGroups := make([]string, EndpointCatalogMaxGroups+1)
	_, err := GetModelSupportedEndpointTypes(tooManyGroups, map[string]bool{"model": true})
	assert.ErrorIs(t, err, ErrEndpointCatalogTooLarge)

	models := make(map[string]bool, EndpointCatalogMaxModels+1)
	for index := 0; index <= EndpointCatalogMaxModels; index++ {
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
	for _, channelType := range []channelcatalog.ChannelType{
		channelcatalog.ChannelTypeCohere,
		channelcatalog.ChannelCloudflare,
		channelcatalog.ChannelTypeDify,
		channelcatalog.ChannelTypeBaidu,
		channelcatalog.ChannelTypeBaiduV2,
	} {
		assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAI},
			EndpointTypesForChannelTypes([]int{int(channelType)}), int(channelType))
	}

	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeAnthropic, channelcatalog.EndpointTypeOpenAI},
		EndpointTypesForChannelTypes([]int{int(channelcatalog.ChannelTypeAws)}))
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAI},
		EndpointTypesForModelAndChannelTypes("amazon.nova-pro-v1:0", []int{int(channelcatalog.ChannelTypeAws)}))
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAIVideo},
		EndpointTypesForChannelTypes([]int{int(channelcatalog.ChannelTypeSora)}))
}

func TestEndpointCatalogKeepsDifyOnChatForImageNamedApplications(t *testing.T) {
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAI},
		EndpointTypesForModelAndChannelTypes("gpt-image-1", []int{int(channelcatalog.ChannelTypeDify)}))
}

func TestEndpointCatalogIncludesMiniMaxReferenceAdvertisedSurface(t *testing.T) {
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAI},
		EndpointTypesForChannelTypes([]int{int(channelcatalog.ChannelTypeMiniMax)}))
	assert.Equal(t, []channelcatalog.EndpointType{channelcatalog.EndpointTypeOpenAI},
		EndpointTypesForModelAndChannelTypes("image-01", []int{int(channelcatalog.ChannelTypeMiniMax)}))
}
