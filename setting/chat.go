package setting

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const (
	// ChatsOption stores the public chat-launcher catalog as a JSON array of
	// single-entry name-to-URL objects, matching the established API shape.
	ChatsOption = "Chats"

	maxChatConfigBytes = 64 * 1024
	maxChatPresets     = 64
	maxChatNameBytes   = 128
	maxChatURLBytes    = 4096
)

var (
	ErrInvalidChatSetting = errors.New("invalid chat preset setting")
	chatSchemePattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]{0,31}$`)
)

// ChatPreset is the normalized, bounded representation consumed by the
// status endpoint. The stored wire format intentionally remains compatible
// with the reference's array of single-entry objects.
type ChatPreset struct {
	Name string
	URL  string
}

func parseChatPresets(raw string) ([]ChatPreset, error) {
	if raw == "" {
		return []ChatPreset{}, nil
	}
	if len(raw) > maxChatConfigBytes {
		return nil, ErrInvalidChatSetting
	}
	var entries []map[string]string
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || entries == nil || len(entries) > maxChatPresets {
		return nil, ErrInvalidChatSetting
	}
	seen := make(map[string]struct{}, len(entries))
	presets := make([]ChatPreset, 0, len(entries))
	for _, entry := range entries {
		if len(entry) != 1 {
			return nil, ErrInvalidChatSetting
		}
		for name, rawURL := range entry {
			if name == "" || name != strings.TrimSpace(name) || len(name) > maxChatNameBytes || containsControl(name) {
				return nil, ErrInvalidChatSetting
			}
			if _, exists := seen[name]; exists {
				return nil, ErrInvalidChatSetting
			}
			if !validChatURL(rawURL) {
				return nil, ErrInvalidChatSetting
			}
			seen[name] = struct{}{}
			presets = append(presets, ChatPreset{Name: name, URL: rawURL})
		}
	}
	return presets, nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validChatURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > maxChatURLBytes || containsControl(raw) {
		return false
	}
	separator := strings.IndexByte(raw, ':')
	if separator < 1 || !chatSchemePattern.MatchString(raw[:separator]) {
		// Non-URL app identifiers are retained for compatible informational
		// presets, but are never made clickable by the frontend.
		return chatSchemePattern.MatchString(raw)
	}
	scheme := strings.ToLower(raw[:separator])
	switch scheme {
	case "javascript", "data", "file", "vbscript":
		return false
	case "https", "http":
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil {
			return false
		}
		if scheme == "https" {
			return true
		}
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	default:
		return true
	}
}

// GetChatPresets returns a newly allocated validated snapshot. Malformed
// legacy database values fail closed to an empty public catalog.
func GetChatPresets() []ChatPreset {
	presets, err := parseChatPresets(GetOption(ChatsOption))
	if err != nil {
		return []ChatPreset{}
	}
	return presets
}

// GetChatPresetMaps returns the reference-compatible public JSON shape.
func GetChatPresetMaps() []map[string]string {
	presets := GetChatPresets()
	result := make([]map[string]string, 0, len(presets))
	for _, preset := range presets {
		result = append(result, map[string]string{preset.Name: preset.URL})
	}
	return result
}
