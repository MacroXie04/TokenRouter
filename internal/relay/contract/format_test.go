package contract

import (
	"github.com/stretchr/testify/assert"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"testing"
)

func TestGetRelayFormatCoversSpecializedOpenAIModes(t *testing.T) {
	tests := []struct {
		mode   channelcatalog.RelayMode
		format channelcatalog.RelayFormat
	}{
		{channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses},
		{channelcatalog.RelayModeResponsesCompact, channelcatalog.RelayFormatOpenAIResponsesCompaction},
		{channelcatalog.RelayModeAlphaSearch, channelcatalog.RelayFormatOpenAIAlphaSearch},
		{channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio},
		{channelcatalog.RelayModeAudioTranscription, channelcatalog.RelayFormatOpenAIAudio},
		{channelcatalog.RelayModeAudioTranslation, channelcatalog.RelayFormatOpenAIAudio},
		{channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage},
		{channelcatalog.RelayModeImagesEdits, channelcatalog.RelayFormatOpenAIImage},
		{channelcatalog.RelayModeRerank, channelcatalog.RelayFormatRerank},
		{channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding},
		{channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI},
	}
	for _, test := range tests {
		assert.Equal(t, test.format, GetRelayFormat(channelcatalog.ChannelTypeOpenAI, test.mode))
	}
}
