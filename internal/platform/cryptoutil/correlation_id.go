package cryptoutil

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

const maxProviderCorrelationIDBytes = 128

var providerCorrelationFingerprint = regexp.MustCompile(`^sha256:[0-9a-f]{32}$`)

// NormalizeProviderCorrelationID turns every provider-controlled identifier
// into a deterministic, non-reversible 128-bit SHA-256 fingerprint. Providers
// know the server-side channel credential and can echo an arbitrary opaque key
// as a syntactically ordinary request ID, so allow-list heuristics are not a
// sufficient secret boundary. The function is idempotent for stored values.
func NormalizeProviderCorrelationID(value string) string {
	if value == "" {
		return ""
	}
	if providerCorrelationFingerprint.MatchString(value) {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:16])
}
