package policy

import (
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenModelPolicy is the immutable model-access policy derived from an
// authenticated relay token. An enabled policy with an empty or malformed
// limit list intentionally allows no models.
type TokenModelPolicy struct {
	limited bool
	valid   bool
	allowed map[string]struct{}
}

// NewTokenModelPolicy parses the token's comma-separated model allow-list.
// A nil token is treated as restricted and invalid so callers fail closed if
// relay authentication context is missing.
func NewTokenModelPolicy(token *model.Token) TokenModelPolicy {
	if token == nil {
		return TokenModelPolicy{limited: true}
	}
	if !token.ModelLimitsEnabled {
		return TokenModelPolicy{valid: true}
	}

	allowed, valid := parseTokenModelLimits(token.ModelLimits)
	return TokenModelPolicy{limited: true, valid: valid, allowed: allowed}
}

// Limited reports whether the token has model restrictions enabled.
func (policy TokenModelPolicy) Limited() bool {
	return policy.limited
}

// Valid reports whether a restricted policy contained a well-formed,
// non-empty allow-list. Unrestricted policies are always valid.
func (policy TokenModelPolicy) Valid() bool {
	return policy.valid
}

// Allows reports whether modelName is explicitly authorized. Matching is
// exact and case-sensitive because model identifiers are routed that way.
func (policy TokenModelPolicy) Allows(modelName string) bool {
	if !policy.limited {
		return true
	}
	if !policy.valid || modelName == "" {
		return false
	}
	_, ok := policy.allowed[modelName]
	return ok
}

// Filter returns a new catalog containing only models allowed by the policy.
// It never mutates the shared ability-cache snapshot supplied by the caller.
func (policy TokenModelPolicy) Filter(models map[string]bool) map[string]bool {
	filtered := make(map[string]bool, len(models))
	for modelName, enabled := range models {
		if enabled && policy.Allows(modelName) {
			filtered[modelName] = true
		}
	}
	return filtered
}

func parseTokenModelLimits(raw string) (map[string]struct{}, bool) {
	if strings.TrimSpace(raw) == "" || !utf8.ValidString(raw) {
		return nil, false
	}

	allowed := make(map[string]struct{})
	for _, field := range strings.Split(raw, ",") {
		modelName := strings.TrimSpace(field)
		if modelName == "" || strings.IndexFunc(modelName, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) >= 0 {
			return nil, false
		}
		allowed[modelName] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, false
	}
	return allowed, true
}
