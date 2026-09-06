package constant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPathToRelayModeCanonicalAndUnknownPaths(t *testing.T) {
	tests := []struct {
		path string
		mode RelayMode
	}{
		{"/v1/chat/completions", RelayModeChatCompletions},
		{"/v1/completions", RelayModeCompletions},
		{"/v1/embeddings", RelayModeEmbeddings},
		{"/v1/moderations", RelayModeModerations},
		{"/v1/images/generations", RelayModeImagesGenerations},
		{"/v1/images/edits", RelayModeImagesEdits},
		{"/v1/audio/speech", RelayModeAudioSpeech},
		{"/v1/audio/transcriptions", RelayModeAudioTranscription},
		{"/v1/audio/translations", RelayModeAudioTranslation},
		{"/v1/responses", RelayModeResponses},
		{"/v1/responses/compact", RelayModeResponsesCompact},
		{"/v1/alpha/search", RelayModeAlphaSearch},
		{"/v1/rerank", RelayModeRerank},
		{"/v1/realtime", RelayModeRealtime},
		{"/v1/video/submit", RelayModeVideoSubmit},
		{"/v1/video/generations", RelayModeVideoSubmit},
		{"/v1/videos", RelayModeVideoSubmit},
		{"/v1/videos/task_123/remix", RelayModeVideoSubmit},
		{"/v1/video/generations/task_123", RelayModeVideoFetchByID},
		{"/v1/videos/task_123", RelayModeVideoFetchByID},
		{"/v1/videos/task_123/content", RelayModeVideoFetchByID},
		{"/not/a/relay", RelayModeUnknown},
	}
	for _, test := range tests {
		assert.Equal(t, test.mode, PathToRelayMode(test.path), test.path)
	}
}

func TestPathToRelayModeMidjourneyExactRootAndModeRoutes(t *testing.T) {
	routes := map[string]RelayMode{
		"/submit/action":                RelayModeMidjourneyAction,
		"/submit/shorten":               RelayModeMidjourneyShorten,
		"/submit/modal":                 RelayModeMidjourneyModal,
		"/submit/imagine":               RelayModeMidjourneyImagine,
		"/submit/change":                RelayModeMidjourneyChange,
		"/submit/simple-change":         RelayModeMidjourneySimpleChange,
		"/submit/describe":              RelayModeMidjourneyDescribe,
		"/submit/blend":                 RelayModeMidjourneyBlend,
		"/submit/edits":                 RelayModeMidjourneyEdits,
		"/submit/video":                 RelayModeMidjourneyVideo,
		"/task/task_123/fetch":          RelayModeMidjourneyTaskFetch,
		"/task/task_123/image-seed":     RelayModeMidjourneyTaskImageSeed,
		"/task/list-by-condition":       RelayModeMidjourneyTaskFetchByCondition,
		"/insight-face/swap":            RelayModeSwapFace,
		"/submit/upload-discord-images": RelayModeMidjourneyUpload,
	}
	for suffix, mode := range routes {
		for _, prefix := range []string{"/mj", "/fast/mj"} {
			path := prefix + suffix
			assert.Equal(t, mode, PathToRelayModeMidjourney(path), path)
			assert.Equal(t, mode, PathToRelayMode(path), path)
		}
	}
	assert.Equal(t, RelayModeMidjourneyNotify, PathToRelayModeMidjourney("/mj/notify"))
	for _, path := range []string{
		"/mj/image/task_123", "/mj/submit/imagines", "/nested/fast/mj/submit/imagine",
		"/mj/task//fetch", "/mj/task/task_123/other", "/not-mj/submit/imagine",
	} {
		assert.Equal(t, RelayModeUnknown, PathToRelayModeMidjourney(path), path)
	}
}
