package setting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

const (
	ModelRequestRateLimitEnabledOption         = "ModelRequestRateLimitEnabled"
	ModelRequestRateLimitDurationMinutesOption = "ModelRequestRateLimitDurationMinutes"
	ModelRequestRateLimitCountOption           = "ModelRequestRateLimitCount"
	ModelRequestRateLimitSuccessCountOption    = "ModelRequestRateLimitSuccessCount"
	ModelRequestRateLimitGroupOption           = "ModelRequestRateLimitGroup"

	defaultModelRequestRateLimitDurationMinutes = 1
	defaultModelRequestRateLimitCount           = 0
	defaultModelRequestRateLimitSuccessCount    = 1000
	maxModelRequestRateLimitDurationMinutes     = 43_200
	maxModelRequestRateLimitCount               = 100_000_000
	maxModelRequestRateLimitGroupCount          = 2_147_483_647
	maxModelRequestRateLimitGroups              = 256
	maxModelRequestRateLimitGroupBytes          = 64
	maxModelRequestRateLimitJSONBytes           = 64 << 10
)

// ModelRequestRateLimitSetting is published as an immutable snapshot. A group
// entry stores [total requests, successful requests], matching the observable
// reference option. A zero total limit means unlimited; the successful limit
// is always positive.
type ModelRequestRateLimitSetting struct {
	Enabled         bool
	DurationMinutes int
	TotalLimit      int
	SuccessLimit    int
	Groups          map[string][2]int
}

var modelRequestRateLimitConfig atomic.Pointer[ModelRequestRateLimitSetting]

func init() {
	config := defaultModelRequestRateLimitSetting()
	modelRequestRateLimitConfig.Store(&config)
}

func defaultModelRequestRateLimitSetting() ModelRequestRateLimitSetting {
	return ModelRequestRateLimitSetting{
		DurationMinutes: defaultModelRequestRateLimitDurationMinutes,
		TotalLimit:      defaultModelRequestRateLimitCount,
		SuccessLimit:    defaultModelRequestRateLimitSuccessCount,
		Groups:          map[string][2]int{},
	}
}

// ModelRequestRateLimitOptionDefaults exposes a complete non-secret settings
// surface to root operators even before any option row has been persisted.
func ModelRequestRateLimitOptionDefaults() map[string]string {
	return map[string]string{
		ModelRequestRateLimitEnabledOption:         "false",
		ModelRequestRateLimitDurationMinutesOption: strconv.Itoa(defaultModelRequestRateLimitDurationMinutes),
		ModelRequestRateLimitCountOption:           strconv.Itoa(defaultModelRequestRateLimitCount),
		ModelRequestRateLimitSuccessCountOption:    strconv.Itoa(defaultModelRequestRateLimitSuccessCount),
		ModelRequestRateLimitGroupOption:           "{}",
	}
}

func buildModelRequestRateLimitSetting(options map[string]string) (ModelRequestRateLimitSetting, error) {
	config := defaultModelRequestRateLimitSetting()
	var err error
	if config.Enabled, err = parseModelRateLimitBool(
		options, ModelRequestRateLimitEnabledOption, config.Enabled,
	); err != nil {
		return ModelRequestRateLimitSetting{}, err
	}
	if config.DurationMinutes, err = parseBoundedModelRateLimitInteger(
		options, ModelRequestRateLimitDurationMinutesOption, config.DurationMinutes,
		1, maxModelRequestRateLimitDurationMinutes,
	); err != nil {
		return ModelRequestRateLimitSetting{}, err
	}
	if config.TotalLimit, err = parseBoundedModelRateLimitInteger(
		options, ModelRequestRateLimitCountOption, config.TotalLimit,
		0, maxModelRequestRateLimitCount,
	); err != nil {
		return ModelRequestRateLimitSetting{}, err
	}
	if config.SuccessLimit, err = parseBoundedModelRateLimitInteger(
		options, ModelRequestRateLimitSuccessCountOption, config.SuccessLimit,
		1, maxModelRequestRateLimitCount,
	); err != nil {
		return ModelRequestRateLimitSetting{}, err
	}
	if raw, present := options[ModelRequestRateLimitGroupOption]; present && raw != "" {
		config.Groups, err = parseModelRequestRateLimitGroups(raw)
		if err != nil {
			return ModelRequestRateLimitSetting{}, err
		}
	}
	return config, nil
}

func parseModelRateLimitBool(options map[string]string, key string, fallback bool) (bool, error) {
	raw, present := options[key]
	if !present || raw == "" {
		return fallback, nil
	}
	if raw != strings.TrimSpace(raw) {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func parseBoundedModelRateLimitInteger(
	options map[string]string,
	key string,
	fallback, minimum, maximum int,
) (int, error) {
	raw, present := options[key]
	if !present || raw == "" {
		return fallback, nil
	}
	if raw != strings.TrimSpace(raw) {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum)
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum)
	}
	return int(parsed), nil
}

func parseModelRequestRateLimitGroups(raw string) (map[string][2]int, error) {
	if len(raw) > maxModelRequestRateLimitJSONBytes || !utf8.ValidString(raw) {
		return nil, errors.New("ModelRequestRateLimitGroup exceeds the safe JSON size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("ModelRequestRateLimitGroup must be a JSON object")
	}
	groups := make(map[string][2]int)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("ModelRequestRateLimitGroup must be a JSON object")
		}
		group, ok := keyToken.(string)
		if !ok || !validModelRequestRateLimitGroup(group) {
			return nil, errors.New("ModelRequestRateLimitGroup contains an invalid group")
		}
		if _, duplicate := groups[group]; duplicate {
			return nil, fmt.Errorf("ModelRequestRateLimitGroup contains duplicate group %q", group)
		}
		var limits []json.Number
		if err := decoder.Decode(&limits); err != nil || len(limits) != 2 {
			return nil, fmt.Errorf("ModelRequestRateLimitGroup entry %q must contain exactly two integers", group)
		}
		parsed := [2]int{}
		for index, number := range limits {
			value, err := strconv.ParseInt(string(number), 10, 32)
			minimum := int64(0)
			if index == 1 {
				minimum = 1
			}
			if err != nil || value < minimum || value > maxModelRequestRateLimitGroupCount {
				return nil, fmt.Errorf("ModelRequestRateLimitGroup entry %q contains an invalid limit", group)
			}
			parsed[index] = int(value)
		}
		groups[group] = parsed
		if len(groups) > maxModelRequestRateLimitGroups {
			return nil, fmt.Errorf("ModelRequestRateLimitGroup may contain at most %d groups", maxModelRequestRateLimitGroups)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("ModelRequestRateLimitGroup must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("ModelRequestRateLimitGroup must contain exactly one JSON value")
	}
	return groups, nil
}

func validModelRequestRateLimitGroup(group string) bool {
	if group == "" || group != strings.TrimSpace(group) || len(group) > maxModelRequestRateLimitGroupBytes ||
		!utf8.ValidString(group) {
		return false
	}
	for _, character := range group {
		if unicode.IsControl(character) || character == 0x061c || character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

// GetModelRequestRateLimitSetting returns a detached copy of the live policy.
func GetModelRequestRateLimitSetting() ModelRequestRateLimitSetting {
	config := modelRequestRateLimitConfig.Load()
	if config == nil {
		fallback := defaultModelRequestRateLimitSetting()
		return cloneModelRequestRateLimitSetting(fallback)
	}
	return cloneModelRequestRateLimitSetting(*config)
}

func cloneModelRequestRateLimitSetting(config ModelRequestRateLimitSetting) ModelRequestRateLimitSetting {
	copyOf := config
	copyOf.Groups = make(map[string][2]int, len(config.Groups))
	for group, limits := range config.Groups {
		copyOf.Groups[group] = limits
	}
	return copyOf
}

// ModelRequestRateLimits returns the effective total/success pair for a group.
func ModelRequestRateLimits(group string) (total, success int) {
	config := modelRequestRateLimitConfig.Load()
	if config == nil {
		fallback := defaultModelRequestRateLimitSetting()
		config = &fallback
	}
	if limits, found := config.Groups[group]; found {
		return limits[0], limits[1]
	}
	return config.TotalLimit, config.SuccessLimit
}
