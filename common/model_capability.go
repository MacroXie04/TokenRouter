package common

import "strings"

var openAIResponseOnlyModelFragments = [...]string{
	"o3-pro",
	"o3-deep-research",
	"o4-mini-deep-research",
}

var imageGenerationModelPatterns = [...]string{
	"dall-e-3",
	"dall-e-2",
	"gpt-image-1",
	"imagen-",
	"flux-",
	"flux.1-",
}

// IsOpenAIResponseOnlyModel recognizes model families that cannot be invoked
// through Chat Completions. Matching is case-insensitive and intentionally
// bounded to the compatibility catalog rather than accepting arbitrary input.
func IsOpenAIResponseOnlyModel(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	for _, fragment := range openAIResponseOnlyModelFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

// IsImageGenerationModel recognizes the image families exposed by the
// compatible pricing catalog.
func IsImageGenerationModel(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	for _, pattern := range imageGenerationModelPatterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}
