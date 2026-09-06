package settings

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

const (
	AutomaticDisableChannelEnabledOption = "AutomaticDisableChannelEnabled"
	AutomaticEnableChannelEnabledOption  = "AutomaticEnableChannelEnabled"
	ChannelDisableThresholdOption        = "ChannelDisableThreshold"
	AutomaticDisableKeywordsOption       = "AutomaticDisableKeywords"
	AutomaticDisableStatusCodesOption    = "AutomaticDisableStatusCodes"
	AutomaticRetryStatusCodesOption      = "AutomaticRetryStatusCodes"
	AutoTestChannelEnabledOption         = "monitor_setting.auto_test_channel_enabled"
	AutoTestChannelMinutesOption         = "monitor_setting.auto_test_channel_minutes"
	ChannelTestModeOption                = "monitor_setting.channel_test_mode"

	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModeAutoBanOnly     = "auto_ban_only"
	ChannelTestModePassiveRecovery = "passive_recovery"

	DefaultChannelDisableThreshold = 5.0
	DefaultAutoTestChannelMinutes  = 10
	MaxRetryTimes                  = 10
	MaxAutoTestChannelMinutes      = 365 * 24 * 60
	MaxChannelDisableThreshold     = 15.0

	maxAutomaticDisableKeywordsBytes = 16 << 10
	maxAutomaticDisableKeywordBytes  = 256
	maxAutomaticDisableKeywords      = 128
	maxHTTPStatusCodeRulesBytes      = 4 << 10
	maxHTTPStatusCodeRuleTokens      = 128
)

const defaultAutomaticDisableKeywords = "Your credit balance is too low\n" +
	"This organization has been disabled.\n" +
	"You exceeded your current quota\n" +
	"Permission denied\n" +
	"The security token included in the request is invalid\n" +
	"Operation not allowed\n" +
	"Your account is not authorized"

const defaultAutomaticRetryStatusCodes = "100-199,300-399,401-407,409-499,500-503,505-523,525-599"

// HTTPStatusCodeRange is one canonical, inclusive HTTP status-code interval.
// Parsed ranges are sorted and never overlap or touch.
type HTTPStatusCodeRange struct {
	Start int
	End   int
}

// ChannelReliabilitySetting is an immutable runtime snapshot. Callers receive
// defensive copies from GetChannelReliabilitySetting so slices cannot mutate a
// concurrently published policy.
type ChannelReliabilitySetting struct {
	RetryTimes                      int
	ChannelDisableThreshold         float64
	AutomaticDisableChannelEnabled  bool
	AutomaticEnableChannelEnabled   bool
	AutomaticDisableKeywordsText    string
	AutomaticDisableKeywords        []string
	AutomaticDisableStatusCodesText string
	AutomaticDisableStatusCodes     []HTTPStatusCodeRange
	AutomaticRetryStatusCodesText   string
	AutomaticRetryStatusCodes       []HTTPStatusCodeRange
	AutoTestChannelEnabled          bool
	AutoTestChannelMinutes          int
	ChannelTestMode                 string
}

var channelReliabilityConfig atomic.Pointer[ChannelReliabilitySetting]

func init() {
	config := defaultChannelReliabilitySetting()
	channelReliabilityConfig.Store(&config)
}

func defaultChannelReliabilitySetting() ChannelReliabilitySetting {
	disableKeywordsText, disableKeywords, _ := parseAutomaticDisableKeywords(defaultAutomaticDisableKeywords)
	disableRanges, _ := ParseHTTPStatusCodeRanges("401")
	retryRanges, _ := ParseHTTPStatusCodeRanges(defaultAutomaticRetryStatusCodes)
	return ChannelReliabilitySetting{
		RetryTimes:                      0,
		ChannelDisableThreshold:         DefaultChannelDisableThreshold,
		AutomaticDisableChannelEnabled:  false,
		AutomaticEnableChannelEnabled:   false,
		AutomaticDisableKeywordsText:    disableKeywordsText,
		AutomaticDisableKeywords:        disableKeywords,
		AutomaticDisableStatusCodesText: HTTPStatusCodeRangesString(disableRanges),
		AutomaticDisableStatusCodes:     disableRanges,
		AutomaticRetryStatusCodesText:   defaultAutomaticRetryStatusCodes,
		AutomaticRetryStatusCodes:       retryRanges,
		AutoTestChannelEnabled:          false,
		AutoTestChannelMinutes:          DefaultAutoTestChannelMinutes,
		ChannelTestMode:                 ChannelTestModeScheduledAll,
	}
}

// DefaultChannelReliabilitySetting returns a detached copy of the fail-safe
// policy used when no options or deployment overrides are configured.
func DefaultChannelReliabilitySetting() ChannelReliabilitySetting {
	return cloneChannelReliabilitySetting(defaultChannelReliabilitySetting())
}

// ChannelReliabilityOptionDefaults supplies the full non-secret admin option
// surface before database rows exist.
func ChannelReliabilityOptionDefaults() map[string]string {
	return map[string]string{
		RetryTimesOption:                     "0",
		ChannelDisableThresholdOption:        "5",
		AutomaticDisableChannelEnabledOption: "false",
		AutomaticEnableChannelEnabledOption:  "false",
		AutomaticDisableKeywordsOption:       defaultAutomaticDisableKeywords,
		AutomaticDisableStatusCodesOption:    "401",
		AutomaticRetryStatusCodesOption:      defaultAutomaticRetryStatusCodes,
		AutoTestChannelEnabledOption:         "false",
		AutoTestChannelMinutesOption:         "10",
		ChannelTestModeOption:                ChannelTestModeScheduledAll,
	}
}

func IsChannelReliabilityOption(key string) bool {
	switch key {
	case RetryTimesOption,
		ChannelDisableThresholdOption,
		AutomaticDisableChannelEnabledOption,
		AutomaticEnableChannelEnabledOption,
		AutomaticDisableKeywordsOption,
		AutomaticDisableStatusCodesOption,
		AutomaticRetryStatusCodesOption,
		AutoTestChannelEnabledOption,
		AutoTestChannelMinutesOption,
		ChannelTestModeOption:
		return true
	default:
		return false
	}
}

// GetChannelReliabilitySetting returns one coherent defensive copy.
func GetChannelReliabilitySetting() ChannelReliabilitySetting {
	current := channelReliabilityConfig.Load()
	if current == nil {
		fallback := defaultChannelReliabilitySetting()
		return cloneChannelReliabilitySetting(fallback)
	}
	return cloneChannelReliabilitySetting(*current)
}

func storeChannelReliabilitySetting(config ChannelReliabilitySetting) {
	immutable := cloneChannelReliabilitySetting(config)
	channelReliabilityConfig.Store(&immutable)
}

func cloneChannelReliabilitySetting(config ChannelReliabilitySetting) ChannelReliabilitySetting {
	cloned := config
	cloned.AutomaticDisableKeywords = append([]string(nil), config.AutomaticDisableKeywords...)
	cloned.AutomaticDisableStatusCodes = append([]HTTPStatusCodeRange(nil), config.AutomaticDisableStatusCodes...)
	cloned.AutomaticRetryStatusCodes = append([]HTTPStatusCodeRange(nil), config.AutomaticRetryStatusCodes...)
	return cloned
}

type environmentLookup func(string) (string, bool)

func buildChannelReliabilitySetting(options map[string]string) (ChannelReliabilitySetting, error) {
	return buildChannelReliabilitySettingWithEnv(options, os.LookupEnv)
}

func buildChannelReliabilitySettingWithEnv(options map[string]string, lookup environmentLookup) (ChannelReliabilitySetting, error) {
	config := defaultChannelReliabilitySetting()
	var err error
	if config.RetryTimes, err = reliabilityIntegerOption(options, RetryTimesOption, config.RetryTimes, 0, MaxRetryTimes); err != nil {
		return ChannelReliabilitySetting{}, err
	}
	if config.ChannelDisableThreshold, err = reliabilityFloatOption(options, ChannelDisableThresholdOption, config.ChannelDisableThreshold, 0, MaxChannelDisableThreshold); err != nil {
		return ChannelReliabilitySetting{}, err
	}
	if config.AutomaticDisableChannelEnabled, err = reliabilityBoolOption(options, AutomaticDisableChannelEnabledOption, config.AutomaticDisableChannelEnabled); err != nil {
		return ChannelReliabilitySetting{}, err
	}
	if config.AutomaticEnableChannelEnabled, err = reliabilityBoolOption(options, AutomaticEnableChannelEnabledOption, config.AutomaticEnableChannelEnabled); err != nil {
		return ChannelReliabilitySetting{}, err
	}
	if raw, present := options[AutomaticDisableKeywordsOption]; present {
		config.AutomaticDisableKeywordsText, config.AutomaticDisableKeywords, err = parseAutomaticDisableKeywords(raw)
		if err != nil {
			return ChannelReliabilitySetting{}, err
		}
	}
	if raw, present := options[AutomaticDisableStatusCodesOption]; present {
		config.AutomaticDisableStatusCodes, err = ParseHTTPStatusCodeRanges(raw)
		if err != nil {
			return ChannelReliabilitySetting{}, fmt.Errorf("%s: %w", AutomaticDisableStatusCodesOption, err)
		}
		config.AutomaticDisableStatusCodesText = HTTPStatusCodeRangesString(config.AutomaticDisableStatusCodes)
	}
	if raw, present := options[AutomaticRetryStatusCodesOption]; present {
		config.AutomaticRetryStatusCodes, err = ParseHTTPStatusCodeRanges(raw)
		if err != nil {
			return ChannelReliabilitySetting{}, fmt.Errorf("%s: %w", AutomaticRetryStatusCodesOption, err)
		}
		config.AutomaticRetryStatusCodesText = HTTPStatusCodeRangesString(config.AutomaticRetryStatusCodes)
	}
	if config.AutoTestChannelEnabled, err = reliabilityBoolOption(options, AutoTestChannelEnabledOption, config.AutoTestChannelEnabled); err != nil {
		return ChannelReliabilitySetting{}, err
	}
	if config.AutoTestChannelMinutes, err = reliabilityIntegerOption(options, AutoTestChannelMinutesOption, config.AutoTestChannelMinutes, 1, MaxAutoTestChannelMinutes); err != nil {
		return ChannelReliabilitySetting{}, err
	}
	if raw, present := options[ChannelTestModeOption]; present {
		if !validChannelTestMode(raw) {
			return ChannelReliabilitySetting{}, errors.New(ChannelTestModeOption + " must be scheduled_all, auto_ban_only, or passive_recovery")
		}
		config.ChannelTestMode = raw
	}

	// Preserve the target's established RETRY_TIMES deployment override and
	// the reference channel-test overrides, but parse them once into this
	// coherent snapshot rather than consulting the process environment per use.
	if lookup != nil {
		if raw, present := lookup("RETRY_TIMES"); present {
			config.RetryTimes, err = reliabilityInteger("RETRY_TIMES", raw, 0, MaxRetryTimes)
			if err != nil {
				return ChannelReliabilitySetting{}, err
			}
		}
		if raw, present := lookup("CHANNEL_TEST_FREQUENCY"); present {
			config.AutoTestChannelMinutes, err = reliabilityInteger("CHANNEL_TEST_FREQUENCY", raw, 1, MaxAutoTestChannelMinutes)
			if err != nil {
				return ChannelReliabilitySetting{}, err
			}
			config.AutoTestChannelEnabled = true
			config.ChannelTestMode = ChannelTestModeScheduledAll
		}
		if raw, present := lookup("CHANNEL_TEST_ENABLED"); present {
			config.AutoTestChannelEnabled, err = reliabilityBool("CHANNEL_TEST_ENABLED", raw)
			if err != nil {
				return ChannelReliabilitySetting{}, err
			}
		}
	}
	return config, nil
}

func reliabilityBoolOption(options map[string]string, key string, fallback bool) (bool, error) {
	raw, present := options[key]
	if !present {
		return fallback, nil
	}
	return reliabilityBool(key, raw)
}

func reliabilityBool(key, raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New(key + " must be true or false")
	}
}

func reliabilityIntegerOption(options map[string]string, key string, fallback, minimum, maximum int) (int, error) {
	raw, present := options[key]
	if !present {
		return fallback, nil
	}
	return reliabilityInteger(key, raw, minimum, maximum)
}

func reliabilityInteger(key, raw string, minimum, maximum int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(value) != raw || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum)
	}
	return value, nil
}

func reliabilityFloatOption(options map[string]string, key string, fallback, minimum, maximum float64) (float64, error) {
	raw, present := options[key]
	if !present {
		return fallback, nil
	}
	if raw == "" || raw != strings.TrimSpace(raw) {
		return 0, fmt.Errorf("%s must be a finite number from %g to %g", key, minimum, maximum)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Signbit(value) || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be a finite number from %g to %g", key, minimum, maximum)
	}
	return value, nil
}

func validChannelTestMode(mode string) bool {
	switch mode {
	case ChannelTestModeScheduledAll, ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
		return true
	default:
		return false
	}
}

func invalidReliabilityRune(character rune, allowNewline bool) bool {
	if allowNewline && character == '\n' {
		return false
	}
	return unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
		(character >= 0x202a && character <= 0x202e) ||
		(character >= 0x2066 && character <= 0x2069)
}

func parseAutomaticDisableKeywords(raw string) (string, []string, error) {
	if len(raw) > maxAutomaticDisableKeywordsBytes || !utf8.ValidString(raw) {
		return "", nil, errors.New(AutomaticDisableKeywordsOption + " exceeds its size limit or is not valid UTF-8")
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	for _, character := range raw {
		if invalidReliabilityRune(character, true) {
			return "", nil, errors.New(AutomaticDisableKeywordsOption + " contains unsafe control characters")
		}
	}
	display := make([]string, 0)
	matchers := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if len(line) > maxAutomaticDisableKeywordBytes || len(lower) > maxAutomaticDisableKeywordBytes {
			return "", nil, errors.New(AutomaticDisableKeywordsOption + " contains an overlong keyword")
		}
		if _, duplicate := seen[lower]; duplicate {
			continue
		}
		if len(matchers) >= maxAutomaticDisableKeywords {
			return "", nil, errors.New(AutomaticDisableKeywordsOption + " contains too many keywords")
		}
		seen[lower] = struct{}{}
		display = append(display, line)
		matchers = append(matchers, lower)
	}
	return strings.Join(display, "\n"), matchers, nil
}

// ParseHTTPStatusCodeRanges parses the reference grammar and returns sorted,
// merged inclusive ranges. Empty input intentionally means no matches.
func ParseHTTPStatusCodeRanges(raw string) ([]HTTPStatusCodeRange, error) {
	if len(raw) > maxHTTPStatusCodeRulesBytes || !utf8.ValidString(raw) {
		return nil, errors.New("HTTP status-code rules exceed their size limit or are not valid UTF-8")
	}
	for _, character := range raw {
		if invalidReliabilityRune(character, false) {
			return nil, errors.New("HTTP status-code rules contain control characters")
		}
	}
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "，", ","))
	if raw == "" {
		return nil, nil
	}
	ranges := make([]HTTPStatusCodeRange, 0)
	for _, segment := range strings.Split(raw, ",") {
		segment = strings.ReplaceAll(strings.TrimSpace(segment), " ", "")
		if segment == "" {
			continue
		}
		if len(ranges) >= maxHTTPStatusCodeRuleTokens {
			return nil, errors.New("HTTP status-code rules contain too many entries")
		}
		parsed, err := parseHTTPStatusCodeRange(segment)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, parsed)
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})
	merged := []HTTPStatusCodeRange{ranges[0]}
	for _, current := range ranges[1:] {
		last := &merged[len(merged)-1]
		if current.Start <= last.End+1 {
			if current.End > last.End {
				last.End = current.End
			}
			continue
		}
		merged = append(merged, current)
	}
	return merged, nil
}

func parseHTTPStatusCodeRange(token string) (HTTPStatusCodeRange, error) {
	parts := strings.Split(token, "-")
	if len(parts) > 2 || len(parts) == 0 || parts[0] == "" || len(parts) == 2 && parts[1] == "" {
		return HTTPStatusCodeRange{}, fmt.Errorf("invalid HTTP status-code rule %q", token)
	}
	start, ok := parseHTTPStatusCode(parts[0])
	if !ok {
		return HTTPStatusCodeRange{}, fmt.Errorf("invalid HTTP status-code rule %q", token)
	}
	end := start
	if len(parts) == 2 {
		end, ok = parseHTTPStatusCode(parts[1])
		if !ok || start > end {
			return HTTPStatusCodeRange{}, fmt.Errorf("invalid HTTP status-code rule %q", token)
		}
	}
	return HTTPStatusCodeRange{Start: start, End: end}, nil
}

func parseHTTPStatusCode(raw string) (int, bool) {
	if len(raw) != 3 {
		return 0, false
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	value, _ := strconv.Atoi(raw)
	return value, value >= 100 && value <= 599
}

// HTTPStatusCodeRangesString returns the stable admin/storage representation.
func HTTPStatusCodeRangesString(ranges []HTTPStatusCodeRange) string {
	parts := make([]string, 0, len(ranges))
	for _, statusRange := range ranges {
		if statusRange.Start == statusRange.End {
			parts = append(parts, strconv.Itoa(statusRange.Start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", statusRange.Start, statusRange.End))
		}
	}
	return strings.Join(parts, ",")
}

func statusCodeMatches(ranges []HTTPStatusCodeRange, code int) bool {
	if code < 100 || code > 599 {
		return false
	}
	for _, statusRange := range ranges {
		if code < statusRange.Start {
			return false
		}
		if code <= statusRange.End {
			return true
		}
	}
	return false
}

func (config ChannelReliabilitySetting) ShouldDisableForStatus(code int) bool {
	return statusCodeMatches(config.AutomaticDisableStatusCodes, code)
}

func (config ChannelReliabilitySetting) ShouldRetryForStatus(code int) bool {
	if code == 504 || code == 524 {
		return false
	}
	return statusCodeMatches(config.AutomaticRetryStatusCodes, code)
}

// MatchesAutomaticDisableKeyword only examines bounded text. Upstream error
// bodies are already capped at 16 KiB; rejecting larger caller-provided text
// prevents a policy check from becoming a memory/CPU amplification surface.
func (config ChannelReliabilitySetting) MatchesAutomaticDisableKeyword(message string) bool {
	if len(message) > maxAutomaticDisableKeywordsBytes || !utf8.ValidString(message) {
		return false
	}
	lower := strings.ToLower(message)
	for _, keyword := range config.AutomaticDisableKeywords {
		if keyword != "" && strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
