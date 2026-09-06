package billing

import (
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
)

func ModelRuleMatches(rule int, pattern, candidate string) bool {
	switch rule {
	case model.ModelNameRulePrefix:
		return strings.HasPrefix(candidate, pattern)
	case model.ModelNameRuleContains:
		return strings.Contains(candidate, pattern)
	case model.ModelNameRuleSuffix:
		return strings.HasSuffix(candidate, pattern)
	default:
		return candidate == pattern
	}
}
