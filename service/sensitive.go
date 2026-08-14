package service

import (
	"strings"

	"github.com/tokenrouter/tokenrouter/setting"
)

// sensitiveWords is the in-memory sensitive-word list, loaded from the
// SensitiveWords option (comma/newline separated).
var sensitiveWords []string

// LoadSensitiveWords reloads the sensitive-word list from settings.
func LoadSensitiveWords() {
	raw := setting.GetOptionOrDefault("SensitiveWords", "")
	sensitiveWords = parseWordList(raw)
}

// parseWordList splits a comma/newline separated list into trimmed words.
func parseWordList(raw string) []string {
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '，'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if w := strings.TrimSpace(f); w != "" {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CheckSensitiveContent reports whether text contains any sensitive word.
func CheckSensitiveContent(text string) bool {
	for _, w := range sensitiveWords {
		if w != "" && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// ShouldCheckPromptSensitive reports whether prompt moderation is active:
// both CheckSensitiveEnabled and CheckSensitiveOnPromptEnabled default to true
// (matching the reference), so moderation is on whenever words are configured.
func ShouldCheckPromptSensitive() bool {
	return setting.GetOptionBool(setting.CheckSensitiveEnabledOption, true) &&
		setting.GetOptionBool(setting.CheckSensitiveOnPromptEnabledOption, true)
}

// SensitiveWordCount returns the number of loaded sensitive words (test hook).
func SensitiveWordCount() int {
	return len(sensitiveWords)
}
