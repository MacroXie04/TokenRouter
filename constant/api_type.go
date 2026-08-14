package constant

import "strings"

// RelayFormat identifies the wire protocol family of an upstream response. It
// drives token normalization and settlement semantics.
type RelayFormat string

const (
	RelayFormatUnknown                   RelayFormat = ""
	RelayFormatOpenAI                    RelayFormat = "openai"
	RelayFormatClaude                    RelayFormat = "claude"
	RelayFormatGemini                    RelayFormat = "gemini"
	RelayFormatOpenAIResponses           RelayFormat = "openai_responses"
	RelayFormatOpenAIResponsesCompaction RelayFormat = "openai_responses_compaction"
	RelayFormatOpenAIAlphaSearch         RelayFormat = "openai_alpha_search"
	RelayFormatOpenAIAudio               RelayFormat = "openai_audio"
	RelayFormatOpenAIImage               RelayFormat = "openai_image"
	RelayFormatOpenAIRealtime            RelayFormat = "openai_realtime"
	RelayFormatRerank                    RelayFormat = "rerank"
	RelayFormatEmbedding                 RelayFormat = "embedding"
	RelayFormatTask                      RelayFormat = "task"
	RelayFormatMjProxy                   RelayFormat = "mj_proxy"
)

// RelayMode is a fine-grained operation discriminator used by the relay engine
// and billing/settlement. Values are stable and persisted in logs.
type RelayMode int

const (
	RelayModeUnknown                        RelayMode = 0
	RelayModeChatCompletions                RelayMode = 1
	RelayModeCompletions                    RelayMode = 2
	RelayModeEmbeddings                     RelayMode = 3
	RelayModeModerations                    RelayMode = 4
	RelayModeImagesGenerations              RelayMode = 5
	RelayModeImagesEdits                    RelayMode = 6
	RelayModeEdits                          RelayMode = 7
	RelayModeMidjourneyImagine              RelayMode = 8
	RelayModeMidjourneyDescribe             RelayMode = 9
	RelayModeMidjourneyBlend                RelayMode = 10
	RelayModeMidjourneyChange               RelayMode = 11
	RelayModeMidjourneySimpleChange         RelayMode = 12
	RelayModeMidjourneyNotify               RelayMode = 13
	RelayModeMidjourneyTaskFetch            RelayMode = 14
	RelayModeMidjourneyTaskImageSeed        RelayMode = 15
	RelayModeMidjourneyTaskFetchByCondition RelayMode = 16
	RelayModeMidjourneyAction               RelayMode = 17
	RelayModeMidjourneyModal                RelayMode = 18
	RelayModeMidjourneyShorten              RelayMode = 19
	RelayModeSwapFace                       RelayMode = 20
	RelayModeMidjourneyUpload               RelayMode = 21
	RelayModeMidjourneyVideo                RelayMode = 22
	RelayModeMidjourneyEdits                RelayMode = 23
	RelayModeAudioSpeech                    RelayMode = 24
	RelayModeAudioTranscription             RelayMode = 25
	RelayModeAudioTranslation               RelayMode = 26
	RelayModeSunoFetch                      RelayMode = 27
	RelayModeSunoFetchByID                  RelayMode = 28
	RelayModeSunoSubmit                     RelayMode = 29
	RelayModeVideoFetchByID                 RelayMode = 30
	RelayModeVideoSubmit                    RelayMode = 31
	RelayModeRerank                         RelayMode = 32
	RelayModeResponses                      RelayMode = 33
	RelayModeRealtime                       RelayMode = 34
	RelayModeGemini                         RelayMode = 35
	RelayModeResponsesCompact               RelayMode = 36
	RelayModeAlphaSearch                    RelayMode = 37
)

// RelayModeName returns a stable string for a relay mode.
func RelayModeName(m RelayMode) string {
	if name, ok := relayModeNames[m]; ok {
		return name
	}
	return "unknown"
}

var relayModeNames = map[RelayMode]string{
	RelayModeUnknown:                        "unknown",
	RelayModeChatCompletions:                "chat_completions",
	RelayModeCompletions:                    "completions",
	RelayModeEmbeddings:                     "embeddings",
	RelayModeModerations:                    "moderations",
	RelayModeImagesGenerations:              "images_generations",
	RelayModeImagesEdits:                    "images_edits",
	RelayModeEdits:                          "edits",
	RelayModeMidjourneyImagine:              "midjourney_imagine",
	RelayModeMidjourneyDescribe:             "midjourney_describe",
	RelayModeMidjourneyBlend:                "midjourney_blend",
	RelayModeMidjourneyChange:               "midjourney_change",
	RelayModeMidjourneySimpleChange:         "midjourney_simple_change",
	RelayModeMidjourneyNotify:               "midjourney_notify",
	RelayModeMidjourneyTaskFetch:            "midjourney_task_fetch",
	RelayModeMidjourneyTaskImageSeed:        "midjourney_task_image_seed",
	RelayModeMidjourneyTaskFetchByCondition: "midjourney_task_fetch_by_condition",
	RelayModeMidjourneyAction:               "midjourney_action",
	RelayModeMidjourneyModal:                "midjourney_modal",
	RelayModeMidjourneyShorten:              "midjourney_shorten",
	RelayModeSwapFace:                       "swap_face",
	RelayModeMidjourneyUpload:               "midjourney_upload",
	RelayModeMidjourneyVideo:                "midjourney_video",
	RelayModeMidjourneyEdits:                "midjourney_edits",
	RelayModeAudioSpeech:                    "audio_speech",
	RelayModeAudioTranscription:             "audio_transcription",
	RelayModeAudioTranslation:               "audio_translation",
	RelayModeSunoFetch:                      "suno_fetch",
	RelayModeSunoFetchByID:                  "suno_fetch_by_id",
	RelayModeSunoSubmit:                     "suno_submit",
	RelayModeVideoFetchByID:                 "video_fetch_by_id",
	RelayModeVideoSubmit:                    "video_submit",
	RelayModeRerank:                         "rerank",
	RelayModeResponses:                      "responses",
	RelayModeRealtime:                       "realtime",
	RelayModeGemini:                         "gemini",
	RelayModeResponsesCompact:               "responses_compact",
	RelayModeAlphaSearch:                    "alpha_search",
}

// PathToRelayMode maps a relay request path to its RelayMode. Unknown paths
// return RelayModeUnknown.
func PathToRelayMode(path string) RelayMode {
	p := strings.TrimSuffix(path, "/")
	switch {
	case p == "/v1/chat/completions", p == "/pg/chat/completions":
		return RelayModeChatCompletions
	case p == "/v1/completions":
		return RelayModeCompletions
	case p == "/v1/embeddings":
		return RelayModeEmbeddings
	case p == "/v1/moderations":
		return RelayModeModerations
	case p == "/v1/images/generations":
		return RelayModeImagesGenerations
	case p == "/v1/images/edits":
		return RelayModeImagesEdits
	case p == "/v1/edits":
		return RelayModeEdits
	case p == "/v1/audio/speech":
		return RelayModeAudioSpeech
	case p == "/v1/audio/transcriptions":
		return RelayModeAudioTranscription
	case p == "/v1/audio/translations":
		return RelayModeAudioTranslation
	case p == "/v1/responses":
		return RelayModeResponses
	case p == "/v1/responses/compact":
		return RelayModeResponsesCompact
	case p == "/v1/alpha/search":
		return RelayModeAlphaSearch
	case strings.HasSuffix(p, "/embeddings"):
		// /v1/engines/:model/embeddings — gemini-format embedding relay
		return RelayModeEmbeddings
	case strings.HasPrefix(p, "/v1/models/"):
		// POST /v1/models/*path — native gemini generateContent passthrough
		return RelayModeGemini
	case strings.HasPrefix(p, "/v1beta/models"):
		// POST /v1beta/models/*path — native gemini generateContent passthrough
		return RelayModeGemini
	case p == "/v1/rerank":
		return RelayModeRerank
	case p == "/v1/realtime":
		return RelayModeRealtime
	case p == "/v1/video/submit":
		return RelayModeVideoSubmit
	case p == "/v1/video/tasks", strings.HasPrefix(p, "/v1/video/tasks/"):
		return RelayModeVideoFetchByID
	case strings.HasPrefix(p, "/mj/submit/imagine"), strings.HasPrefix(p, "/v1/mj/submit/imagine"):
		return RelayModeMidjourneyImagine
	case strings.HasPrefix(p, "/mj"):
		return RelayModeMidjourneyTaskFetch
	case strings.HasPrefix(p, "/v1/suno"):
		return RelayModeSunoSubmit
	default:
		return RelayModeUnknown
	}
}
