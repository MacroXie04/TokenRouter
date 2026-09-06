package settings

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxAnnouncementsOptionBytes = 1 << 20
	maxAnnouncements            = 100
	maxAnnouncementContentRunes = 500
	maxAnnouncementExtraRunes   = 100
	maxAnnouncementIDBytes      = 128
)

var announcementTypes = map[string]struct{}{
	"default": {},
	"ongoing": {},
	"success": {},
	"warning": {},
	"error":   {},
}

// Announcement is the bounded public timeline contract. ID is optional and
// accepts only a short string or a non-negative JavaScript-safe integer.
type Announcement struct {
	ID          any    `json:"id,omitempty"`
	Type        string `json:"type,omitempty"`
	Content     string `json:"content"`
	Extra       string `json:"extra,omitempty"`
	PublishDate string `json:"publishDate"`
}

func parseStrictOptionBool(raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("option must be true or false")
	}
}

func parseAnnouncements(raw string) ([]Announcement, error) {
	if raw == "" {
		return []Announcement{}, nil
	}
	if len(raw) > maxAnnouncementsOptionBytes || !utf8.ValidString(raw) {
		return nil, errors.New("announcements exceed the safe size limit")
	}
	var announcements []Announcement
	if err := jsonutil.UnmarshalJsonStr(raw, &announcements); err != nil || announcements == nil {
		return nil, errors.New("announcements must be a JSON array")
	}
	if len(announcements) > maxAnnouncements {
		return nil, fmt.Errorf("announcements cannot exceed %d entries", maxAnnouncements)
	}
	seenIDs := make(map[string]struct{}, len(announcements))
	for index := range announcements {
		announcement := &announcements[index]
		if announcement.Content == "" || utf8.RuneCountInString(announcement.Content) > maxAnnouncementContentRunes ||
			!validAnnouncementText(announcement.Content, true) {
			return nil, fmt.Errorf("announcement %d has invalid content", index+1)
		}
		if utf8.RuneCountInString(announcement.Extra) > maxAnnouncementExtraRunes ||
			!validAnnouncementText(announcement.Extra, true) {
			return nil, fmt.Errorf("announcement %d has invalid extra text", index+1)
		}
		if announcement.Type != "" {
			if _, valid := announcementTypes[announcement.Type]; !valid {
				return nil, fmt.Errorf("announcement %d has invalid type", index+1)
			}
		}
		published, err := time.Parse(time.RFC3339, announcement.PublishDate)
		if err != nil || published.Format(time.RFC3339) == "" {
			return nil, fmt.Errorf("announcement %d has invalid publishDate", index+1)
		}
		if !validAnnouncementID(announcement.ID) {
			return nil, fmt.Errorf("announcement %d has invalid id", index+1)
		}
		if key, present := announcementIDKey(announcement.ID); present {
			if _, duplicate := seenIDs[key]; duplicate {
				return nil, fmt.Errorf("announcement %d has duplicate id", index+1)
			}
			seenIDs[key] = struct{}{}
		}
	}
	sort.SliceStable(announcements, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339, announcements[i].PublishDate)
		right, _ := time.Parse(time.RFC3339, announcements[j].PublishDate)
		return left.After(right)
	})
	return announcements, nil
}

func announcementIDKey(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", false
	case string:
		return "s:" + typed, true
	case float64:
		return fmt.Sprintf("n:%.0f", typed), true
	case int:
		return fmt.Sprintf("n:%d", typed), true
	case int64:
		return fmt.Sprintf("n:%d", typed), true
	default:
		return "", false
	}
}

func validAnnouncementID(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed != "" && len(typed) <= maxAnnouncementIDBytes && validAnnouncementText(typed, false)
	case float64:
		return typed >= 0 && typed <= float64(1<<53-1) && !math.IsNaN(typed) && !math.IsInf(typed, 0) && math.Trunc(typed) == typed
	case int:
		return typed >= 0
	case int64:
		return typed >= 0 && typed <= 1<<53-1
	default:
		return false
	}
}

func validAnnouncementText(value string, allowLineBreaks bool) bool {
	if !utf8.ValidString(value) || value != strings.ToValidUTF8(value, "") {
		return false
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

// GetAnnouncements returns a detached newest-first snapshot from the same
// coherent console-content publication used by the other public panels.
func GetAnnouncements() []Announcement {
	return GetConsoleContentSetting().Announcements
}

func AnnouncementsEnabled() bool {
	return GetConsoleContentSetting().AnnouncementsEnabled
}
