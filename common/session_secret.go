package common

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

const minSessionSecretBytes = 32

var (
	ephemeralSessionSecretOnce sync.Once
	ephemeralSessionSecret     string
	ephemeralSessionSecretErr  error
)

// InitializeSessionSecret validates the session-secret configuration before
// the process starts accepting requests. Production-like operation requires a
// durable, non-placeholder secret. DEBUG=true is the explicit local-development
// escape hatch: when SESSION_SECRET is absent, a cryptographically random,
// process-local secret is generated instead.
func InitializeSessionSecret() error {
	secret, configured := os.LookupEnv("SESSION_SECRET")
	if !configured || secret == "" {
		if !GetEnvBool("DEBUG", false) {
			return errors.New("SESSION_SECRET is required and must contain at least 32 bytes")
		}
		if _, err := generatedSessionSecret(); err != nil {
			return fmt.Errorf("generate ephemeral SESSION_SECRET: %w", err)
		}
		Logger.Warn("SESSION_SECRET is unset while DEBUG=true; using an ephemeral key that invalidates sessions and encrypted data on restart and is unsuitable for multiple nodes")
		return nil
	}
	if err := validateConfiguredSessionSecret(secret); err != nil {
		return fmt.Errorf("invalid SESSION_SECRET: %w", err)
	}
	return nil
}

// SessionSecret returns the configured secret. Code paths which are exercised
// without the application startup sequence (notably package tests and embedded
// use) receive a process-local CSPRNG secret rather than a predictable default.
// The main executable calls InitializeSessionSecret first and therefore rejects
// this implicit fallback unless DEBUG=true.
func SessionSecret() string {
	if secret := os.Getenv("SESSION_SECRET"); secret != "" {
		return secret
	}
	secret, err := generatedSessionSecret()
	if err != nil {
		panic(fmt.Sprintf("generate ephemeral SESSION_SECRET: %v", err))
	}
	return secret
}

func generatedSessionSecret() (string, error) {
	ephemeralSessionSecretOnce.Do(func() {
		buffer, err := SecureRandomBytes(minSessionSecretBytes)
		if err != nil {
			ephemeralSessionSecretErr = err
			return
		}
		ephemeralSessionSecret = hex.EncodeToString(buffer)
	})
	return ephemeralSessionSecret, ephemeralSessionSecretErr
}

func validateConfiguredSessionSecret(secret string) error {
	if strings.TrimSpace(secret) != secret {
		return errors.New("must not contain leading or trailing whitespace")
	}
	normalized := normalizeSessionSecret(secret)
	if isKnownSessionSecretPlaceholder(normalized) {
		return errors.New("must not use a known placeholder value")
	}
	if len(secret) < minSessionSecretBytes {
		return fmt.Errorf("must contain at least %d bytes", minSessionSecretBytes)
	}
	unique := make(map[rune]struct{})
	for _, character := range secret {
		unique[character] = struct{}{}
	}
	if len(unique) < 8 || isRepeatedSessionSecretPattern(secret) {
		return errors.New("does not contain enough variation")
	}
	return nil
}

func normalizeSessionSecret(secret string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(secret) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func isKnownSessionSecretPlaceholder(normalized string) bool {
	known := map[string]struct{}{
		"tokenrouterdevsessionsecretchangeme": {},
		"changemetoarandomstring":             {},
		"changemerandomstring":                {},
		"randomstring":                        {},
		"replacemewitharandomstring":          {},
		"yoursessionsecret":                   {},
		"sessionsecret":                       {},
	}
	if _, found := known[normalized]; found {
		return true
	}
	return strings.Contains(normalized, "changeme") ||
		strings.Contains(normalized, "placeholder")
}

func isRepeatedSessionSecretPattern(secret string) bool {
	for patternLength := 1; patternLength <= len(secret)/2; patternLength++ {
		if len(secret)%patternLength != 0 {
			continue
		}
		if strings.Repeat(secret[:patternLength], len(secret)/patternLength) == secret {
			return true
		}
	}
	return false
}
