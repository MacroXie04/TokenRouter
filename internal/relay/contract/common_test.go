package contract

import (
	"github.com/stretchr/testify/assert"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"testing"
)

func TestGetMappedModel(t *testing.T) {
	ch := &model.Channel{ModelMapping: `{"gpt-4":"gpt-4-turbo","*":"fallback-model"}`}

	// Exact key match.
	assert.Equal(t, "gpt-4-turbo", GetMappedModel(ch, "gpt-4"))
	// Wildcard fallback.
	assert.Equal(t, "fallback-model", GetMappedModel(ch, "unknown-model"))

	// Empty mapping -> passthrough.
	empty := &model.Channel{}
	assert.Equal(t, "gpt-4", GetMappedModel(empty, "gpt-4"))

	// Nil channel -> passthrough.
	assert.Equal(t, "gpt-4", GetMappedModel(nil, "gpt-4"))
}

func TestGetRelayFormat(t *testing.T) {
	assert.Equal(t, channelcatalog.RelayFormatClaude, GetRelayFormat(channelcatalog.ChannelTypeAnthropic, channelcatalog.RelayModeChatCompletions))
	assert.Equal(t, channelcatalog.RelayFormatGemini, GetRelayFormat(channelcatalog.ChannelTypeGemini, channelcatalog.RelayModeChatCompletions))
	assert.Equal(t, channelcatalog.RelayFormatEmbedding, GetRelayFormat(channelcatalog.ChannelTypeOpenAI, channelcatalog.RelayModeEmbeddings))
	assert.Equal(t, channelcatalog.RelayFormatOpenAI, GetRelayFormat(channelcatalog.ChannelTypeOpenAI, channelcatalog.RelayModeChatCompletions))
}

func TestCountTokens(t *testing.T) {
	assert.Equal(t, 0, CountTokens(""))
	assert.Greater(t, CountTokens("hello world"), 0)
	// The chars/4 fallback is monotonic in length.
	assert.True(t, CountTokens("aaaaaaaaaaaaaaaaaaaa") > CountTokens("aaaa"))
}

func TestEstimatePromptTokens(t *testing.T) {
	req := &protocolkit.GeneralOpenAIRequest{
		Messages: []protocolkit.Message{{Role: "user", Content: "hello world, how are you?"}},
	}
	assert.Greater(t, EstimatePromptTokens(req), 0)

	empty := &protocolkit.GeneralOpenAIRequest{}
	assert.Equal(t, 0, EstimatePromptTokens(empty))
}
