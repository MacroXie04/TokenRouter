package vertex

import "strings"

const ChannelName = "vertex-ai"

type requestMode uint8

const (
	requestModeGemini requestMode = iota + 1
	requestModeClaude
	requestModeOpenSource
)

// supportedModels is the reference Vertex catalog: the Vertex-specific MaaS
// entry followed by the reference Claude and Gemini catalogs. Return an owned
// copy so callers cannot mutate process-wide provider metadata.
var supportedModels = [...]string{
	"meta/llama3-405b-instruct-maas",
	"claude-3-sonnet-20240229", "claude-3-opus-20240229", "claude-3-haiku-20240307",
	"claude-3-5-haiku-20241022", "claude-haiku-4-5-20251001",
	"claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20241022",
	"claude-3-7-sonnet-20250219", "claude-3-7-sonnet-20250219-thinking",
	"claude-sonnet-4-20250514", "claude-sonnet-4-20250514-thinking",
	"claude-opus-4-20250514", "claude-opus-4-20250514-thinking",
	"claude-opus-4-1-20250805", "claude-opus-4-1-20250805-thinking",
	"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929-thinking",
	"claude-opus-4-5-20251101", "claude-opus-4-5-20251101-thinking",
	"claude-opus-4-6", "claude-opus-4-6-max", "claude-opus-4-6-high",
	"claude-opus-4-6-medium", "claude-opus-4-6-low", "claude-sonnet-4-6",
	"claude-opus-4-7", "claude-opus-4-7-max", "claude-opus-4-7-xhigh",
	"claude-opus-4-7-high", "claude-opus-4-7-medium", "claude-opus-4-7-low",
	"claude-opus-4-7-thinking", "claude-opus-4-8", "claude-opus-4-8-max",
	"claude-opus-4-8-xhigh", "claude-opus-4-8-high", "claude-opus-4-8-medium",
	"claude-opus-4-8-low", "claude-opus-4-8-thinking",
	"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash",
	"gemini-2.0-flash-001", "gemini-2.0-flash-lite-001", "gemini-2.0-flash-lite",
	"gemini-2.5-flash-lite", "gemini-3-pro-image", "gemini-3.1-flash-image",
	"gemini-flash-latest", "gemini-flash-lite-latest", "gemini-pro-latest",
	"gemini-2.5-flash-native-audio-latest", "gemini-2.5-flash-preview-tts",
	"gemini-2.5-pro-preview-tts", "gemini-2.5-flash-image",
	"gemini-2.5-flash-lite-preview-09-2025", "gemini-3-pro-preview",
	"gemini-3-flash-preview", "gemini-3.1-pro-preview",
	"gemini-3.1-pro-preview-customtools", "gemini-3.1-flash-lite-preview",
	"gemini-3-pro-image-preview", "nano-banana-pro-preview",
	"gemini-3.1-flash-image-preview", "gemini-robotics-er-1.5-preview",
	"gemini-2.5-computer-use-preview-10-2025", "deep-research-pro-preview-12-2025",
	"gemini-2.5-flash-native-audio-preview-09-2025",
	"gemini-2.5-flash-native-audio-preview-12-2025",
	"gemma-3-1b-it", "gemma-3-4b-it", "gemma-3-12b-it", "gemma-3-27b-it",
	"gemma-3n-e4b-it", "gemma-3n-e2b-it", "gemini-embedding-001",
	"gemini-embedding-2-preview", "imagen-4.0-generate-001",
	"imagen-4.0-ultra-generate-001", "imagen-4.0-fast-generate-001",
	"veo-2.0-generate-001", "veo-3.0-generate-001", "veo-3.0-fast-generate-001",
	"veo-3.1-generate-preview", "veo-3.1-fast-generate-preview", "aqa",
}

func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

func modeForModel(model string) requestMode {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "claude"):
		return requestModeClaude
	case strings.Contains(model, "llama"), strings.Contains(model, "-maas"):
		return requestModeOpenSource
	default:
		return requestModeGemini
	}
}

var claudeModelMap = map[string]string{
	"claude-3-sonnet-20240229":   "claude-3-sonnet@20240229",
	"claude-3-opus-20240229":     "claude-3-opus@20240229",
	"claude-3-haiku-20240307":    "claude-3-haiku@20240307",
	"claude-3-5-sonnet-20240620": "claude-3-5-sonnet@20240620",
	"claude-3-5-sonnet-20241022": "claude-3-5-sonnet-v2@20241022",
	"claude-3-7-sonnet-20250219": "claude-3-7-sonnet@20250219",
	"claude-sonnet-4-20250514":   "claude-sonnet-4@20250514",
	"claude-opus-4-20250514":     "claude-opus-4@20250514",
	"claude-opus-4-1-20250805":   "claude-opus-4-1@20250805",
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5@20250929",
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5@20251001",
	"claude-opus-4-5-20251101":   "claude-opus-4-5@20251101",
	"claude-opus-4-6":            "claude-opus-4-6",
	"claude-opus-4-7":            "claude-opus-4-7",
	"claude-opus-4-8":            "claude-opus-4-8",
}

func vertexClaudeModel(model string) string {
	if base, _, ok := claudeEffortVariant(model); ok {
		model = base
	} else if strings.HasSuffix(model, "-thinking") {
		model = strings.TrimSuffix(model, "-thinking")
	}
	if mapped, ok := claudeModelMap[model]; ok {
		return mapped
	}
	return model
}

// claudeEffortVariant mirrors the reference Claude conversion contract. Only
// the Opus 4.6+ pseudo-models interpret an effort suffix; similarly named
// custom Claude deployments must keep their literal model identifier.
func claudeEffortVariant(model string) (base, effort string, ok bool) {
	for _, suffix := range []string{"-max", "-xhigh", "-high", "-medium", "-low"} {
		if strings.HasSuffix(model, suffix) {
			base = strings.TrimSuffix(model, suffix)
			if base == "claude-opus-4-6" || base == "claude-opus-4-7" || base == "claude-opus-4-8" {
				return base, strings.TrimPrefix(suffix, "-"), true
			}
			break
		}
	}
	return model, "", false
}

func claudeUsesAdaptiveDisplay(model string) bool {
	return model == "claude-opus-4-7" || model == "claude-opus-4-8"
}
