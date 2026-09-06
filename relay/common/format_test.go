package relaycommon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tokenrouter/tokenrouter/constant"
)

func TestGetRelayFormatCoversSpecializedOpenAIModes(t *testing.T) {
	tests := []struct {
		mode   constant.RelayMode
		format constant.RelayFormat
	}{
		{constant.RelayModeResponses, constant.RelayFormatOpenAIResponses},
		{constant.RelayModeResponsesCompact, constant.RelayFormatOpenAIResponsesCompaction},
		{constant.RelayModeAlphaSearch, constant.RelayFormatOpenAIAlphaSearch},
		{constant.RelayModeAudioSpeech, constant.RelayFormatOpenAIAudio},
		{constant.RelayModeAudioTranscription, constant.RelayFormatOpenAIAudio},
		{constant.RelayModeAudioTranslation, constant.RelayFormatOpenAIAudio},
		{constant.RelayModeImagesGenerations, constant.RelayFormatOpenAIImage},
		{constant.RelayModeImagesEdits, constant.RelayFormatOpenAIImage},
		{constant.RelayModeRerank, constant.RelayFormatRerank},
		{constant.RelayModeEmbeddings, constant.RelayFormatEmbedding},
		{constant.RelayModeChatCompletions, constant.RelayFormatOpenAI},
	}
	for _, test := range tests {
		assert.Equal(t, test.format, GetRelayFormat(constant.ChannelTypeOpenAI, test.mode))
	}
}
