package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxConsoleContentOptionBytes = 1 << 20
	maxConsoleAPIInfoEntries     = 50
	maxConsoleFAQEntries         = 100
	maxConsoleUptimeGroups       = 20
	maxConsoleItemID             = int64(1<<53 - 1)
)

var consoleAPIColors = map[string]struct{}{
	"blue": {}, "green": {}, "cyan": {}, "purple": {}, "pink": {},
	"red": {}, "orange": {}, "amber": {}, "yellow": {}, "lime": {},
	"light-green": {}, "teal": {}, "light-blue": {}, "indigo": {},
	"violet": {}, "grey": {}, "slate": {},
}

// ConsoleAPIInfo is one public API-address card. ID is optional presentation
// metadata; all other fields are required and bounded before publication.
type ConsoleAPIInfo struct {
	ID          *int64 `json:"id,omitempty"`
	URL         string `json:"url"`
	Route       string `json:"route"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

// ConsoleFAQ is one public question and answer pair.
type ConsoleFAQ struct {
	ID       *int64 `json:"id,omitempty"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// ConsoleUptimeKumaGroup identifies a public Uptime Kuma status page. The URL
// is later contacted only through the process-wide SSRF-safe transport.
type ConsoleUptimeKumaGroup struct {
	ID           *int64 `json:"id,omitempty"`
	CategoryName string `json:"categoryName"`
	URL          string `json:"url"`
	Slug         string `json:"slug"`
	Description  string `json:"description,omitempty"`
}

// ConsoleContentSetting is an immutable, coherent runtime snapshot. A failed
// local update or remote Sync never replaces the last validated snapshot.
type ConsoleContentSetting struct {
	APIInfo              []ConsoleAPIInfo
	FAQ                  []ConsoleFAQ
	UptimeKumaGroups     []ConsoleUptimeKumaGroup
	Announcements        []Announcement
	APIInfoEnabled       bool
	FAQEnabled           bool
	UptimeKumaEnabled    bool
	AnnouncementsEnabled bool
}

var consoleContentConfig atomic.Pointer[ConsoleContentSetting]

func defaultConsoleContentSetting() ConsoleContentSetting {
	return ConsoleContentSetting{
		APIInfo:              []ConsoleAPIInfo{},
		FAQ:                  []ConsoleFAQ{},
		UptimeKumaGroups:     []ConsoleUptimeKumaGroup{},
		Announcements:        []Announcement{},
		APIInfoEnabled:       true,
		FAQEnabled:           true,
		UptimeKumaEnabled:    true,
		AnnouncementsEnabled: true,
	}
}

func buildConsoleContentSetting(options map[string]string) (ConsoleContentSetting, error) {
	config := defaultConsoleContentSetting()
	var err error
	if config.APIInfo, err = parseConsoleAPIInfo(options[ConsoleAPIInfoOption]); err != nil {
		return ConsoleContentSetting{}, err
	}
	if config.FAQ, err = parseConsoleFAQ(options[ConsoleFAQOption]); err != nil {
		return ConsoleContentSetting{}, err
	}
	if config.UptimeKumaGroups, err = parseConsoleUptimeKumaGroups(options[ConsoleUptimeKumaGroupsOption]); err != nil {
		return ConsoleContentSetting{}, err
	}
	if config.Announcements, err = parseAnnouncements(options[ConsoleAnnouncementsOption]); err != nil {
		return ConsoleContentSetting{}, err
	}
	for key, destination := range map[string]*bool{
		ConsoleAPIInfoEnabledOption:       &config.APIInfoEnabled,
		ConsoleFAQEnabledOption:           &config.FAQEnabled,
		ConsoleUptimeKumaEnabledOption:    &config.UptimeKumaEnabled,
		ConsoleAnnouncementsEnabledOption: &config.AnnouncementsEnabled,
	} {
		raw, configured := options[key]
		if !configured {
			continue
		}
		parsed, parseErr := parseStrictOptionBool(raw)
		if parseErr != nil {
			return ConsoleContentSetting{}, fmt.Errorf("%s: %w", key, parseErr)
		}
		*destination = parsed
	}
	return config, nil
}

// GetConsoleContentSetting returns a detached copy so request handlers cannot
// mutate the shared snapshot while another request is reading it.
func GetConsoleContentSetting() ConsoleContentSetting {
	config := consoleContentConfig.Load()
	if config == nil {
		fallback := defaultConsoleContentSetting()
		config = &fallback
	}
	copyOf := *config
	copyOf.APIInfo = append([]ConsoleAPIInfo(nil), config.APIInfo...)
	copyOf.FAQ = append([]ConsoleFAQ(nil), config.FAQ...)
	copyOf.UptimeKumaGroups = append([]ConsoleUptimeKumaGroup(nil), config.UptimeKumaGroups...)
	copyOf.Announcements = append([]Announcement(nil), config.Announcements...)
	for index := range copyOf.APIInfo {
		copyOf.APIInfo[index].ID = cloneConsoleID(copyOf.APIInfo[index].ID)
	}
	for index := range copyOf.FAQ {
		copyOf.FAQ[index].ID = cloneConsoleID(copyOf.FAQ[index].ID)
	}
	for index := range copyOf.UptimeKumaGroups {
		copyOf.UptimeKumaGroups[index].ID = cloneConsoleID(copyOf.UptimeKumaGroups[index].ID)
	}
	return copyOf
}

func cloneConsoleID(id *int64) *int64 {
	if id == nil {
		return nil
	}
	copyOf := *id
	return &copyOf
}

func parseConsoleAPIInfo(raw string) ([]ConsoleAPIInfo, error) {
	entries, err := decodeConsoleList[ConsoleAPIInfo](raw, "API information")
	if err != nil {
		return nil, err
	}
	if len(entries) > maxConsoleAPIInfoEntries {
		return nil, fmt.Errorf("API information cannot exceed %d entries", maxConsoleAPIInfoEntries)
	}
	seenIDs := map[int64]struct{}{}
	for index, entry := range entries {
		if !validConsoleID(entry.ID, seenIDs) {
			return nil, fmt.Errorf("API information entry %d has an invalid or duplicate id", index+1)
		}
		if !validConsoleURL(entry.URL, 500, false) {
			return nil, fmt.Errorf("API information entry %d has an invalid URL", index+1)
		}
		if !validConsoleSingleLine(entry.Route, 100, false) {
			return nil, fmt.Errorf("API information entry %d has an invalid route", index+1)
		}
		if !validConsoleSingleLine(entry.Description, 200, false) {
			return nil, fmt.Errorf("API information entry %d has an invalid description", index+1)
		}
		if _, ok := consoleAPIColors[entry.Color]; !ok {
			return nil, fmt.Errorf("API information entry %d has an invalid color", index+1)
		}
	}
	return entries, nil
}

func parseConsoleFAQ(raw string) ([]ConsoleFAQ, error) {
	entries, err := decodeConsoleList[ConsoleFAQ](raw, "FAQ")
	if err != nil {
		return nil, err
	}
	if len(entries) > maxConsoleFAQEntries {
		return nil, fmt.Errorf("FAQ cannot exceed %d entries", maxConsoleFAQEntries)
	}
	seenIDs := map[int64]struct{}{}
	for index, entry := range entries {
		if !validConsoleID(entry.ID, seenIDs) {
			return nil, fmt.Errorf("FAQ entry %d has an invalid or duplicate id", index+1)
		}
		if !validConsoleMultiline(entry.Question, 200, false) {
			return nil, fmt.Errorf("FAQ entry %d has an invalid question", index+1)
		}
		if !validConsoleMultiline(entry.Answer, 1000, false) {
			return nil, fmt.Errorf("FAQ entry %d has an invalid answer", index+1)
		}
	}
	return entries, nil
}

func parseConsoleUptimeKumaGroups(raw string) ([]ConsoleUptimeKumaGroup, error) {
	groups, err := decodeConsoleList[ConsoleUptimeKumaGroup](raw, "Uptime Kuma groups")
	if err != nil {
		return nil, err
	}
	if len(groups) > maxConsoleUptimeGroups {
		return nil, fmt.Errorf("Uptime Kuma groups cannot exceed %d entries", maxConsoleUptimeGroups)
	}
	seenIDs := map[int64]struct{}{}
	seenNames := make(map[string]struct{}, len(groups))
	for index, group := range groups {
		if !validConsoleID(group.ID, seenIDs) {
			return nil, fmt.Errorf("Uptime Kuma group %d has an invalid or duplicate id", index+1)
		}
		if !validConsoleSingleLine(group.CategoryName, 50, false) {
			return nil, fmt.Errorf("Uptime Kuma group %d has an invalid category name", index+1)
		}
		if _, duplicate := seenNames[group.CategoryName]; duplicate {
			return nil, fmt.Errorf("Uptime Kuma group %d has a duplicate category name", index+1)
		}
		seenNames[group.CategoryName] = struct{}{}
		if !validConsoleURL(group.URL, 500, true) {
			return nil, fmt.Errorf("Uptime Kuma group %d has an invalid URL", index+1)
		}
		if len(group.Slug) == 0 || utf16Length(group.Slug) > 100 {
			return nil, fmt.Errorf("Uptime Kuma group %d has an invalid slug", index+1)
		}
		for _, character := range group.Slug {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' || character == '_' {
				continue
			}
			return nil, fmt.Errorf("Uptime Kuma group %d has an invalid slug", index+1)
		}
		if !validConsoleSingleLine(group.Description, 200, true) {
			return nil, fmt.Errorf("Uptime Kuma group %d has an invalid description", index+1)
		}
	}
	return groups, nil
}

func decodeConsoleList[T any](raw, label string) ([]T, error) {
	if raw == "" {
		return []T{}, nil
	}
	if len(raw) > maxConsoleContentOptionBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("%s exceeds the safe size limit", label)
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var result []T
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, fmt.Errorf("%s must be a JSON array", label)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s must contain one JSON value", label)
	}
	return result, nil
}

func validConsoleID(id *int64, seen map[int64]struct{}) bool {
	if id == nil {
		return true
	}
	if *id < 0 || *id > maxConsoleItemID {
		return false
	}
	if _, duplicate := seen[*id]; duplicate {
		return false
	}
	seen[*id] = struct{}{}
	return true
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func validConsoleSingleLine(value string, maximumCharacters int, allowEmpty bool) bool {
	return validConsoleText(value, maximumCharacters, allowEmpty, false)
}

func validConsoleMultiline(value string, maximumCharacters int, allowEmpty bool) bool {
	return validConsoleText(value, maximumCharacters, allowEmpty, true)
}

func validConsoleText(value string, maximumCharacters int, allowEmpty, allowLineBreaks bool) bool {
	if !utf8.ValidString(value) || utf16Length(value) > maximumCharacters || value != strings.TrimSpace(value) {
		return false
	}
	if value == "" {
		return allowEmpty
	}
	for _, character := range value {
		if allowLineBreaks && (character == '\n' || character == '\r' || character == '\t') {
			continue
		}
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func validConsoleURL(raw string, maximumCharacters int, outbound bool) bool {
	if !validConsoleSingleLine(raw, maximumCharacters, false) || strings.Contains(raw, `\`) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Port() != "" {
		port, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || port < 1 || port > 65535 {
			return false
		}
	}
	if !outbound {
		return true
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return httpx.SSRFDisabled()
	}
	if address := net.ParseIP(host); address != nil && httpx.IsUnsafeIP(address) {
		return httpx.SSRFDisabled()
	}
	return parsed.Scheme == "https" || httpx.SSRFDisabled()
}
