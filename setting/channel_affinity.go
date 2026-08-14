package setting

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/tokenrouter/tokenrouter/common"
)

const (
	ChannelAffinityEnabledOption           = "channel_affinity_setting.enabled"
	ChannelAffinitySwitchOnSuccessOption   = "channel_affinity_setting.switch_on_success"
	ChannelAffinityKeepOnDisabledOption    = "channel_affinity_setting.keep_on_channel_disabled"
	ChannelAffinityMaxEntriesOption        = "channel_affinity_setting.max_entries"
	ChannelAffinityDefaultTTLSecondsOption = "channel_affinity_setting.default_ttl_seconds"
	ChannelAffinityRulesOption             = "channel_affinity_setting.rules"
	DefaultChannelAffinityMaxEntries       = 100000
	DefaultChannelAffinityTTLSeconds       = 3600
	MaxChannelAffinityEntries              = 1000000
	MaxChannelAffinityTTLSeconds           = 30 * 24 * 60 * 60
)

type ChannelAffinityKeySource struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`
	Path string `json:"path,omitempty"`
}

type ChannelAffinityRule struct {
	Name                  string                     `json:"name"`
	ModelRegex            []string                   `json:"model_regex"`
	PathRegex             []string                   `json:"path_regex"`
	UserAgentInclude      []string                   `json:"user_agent_include,omitempty"`
	KeySources            []ChannelAffinityKeySource `json:"key_sources"`
	ValueRegex            string                     `json:"value_regex"`
	TTLSeconds            int                        `json:"ttl_seconds"`
	ParamOverrideTemplate map[string]any             `json:"param_override_template,omitempty"`
	SkipRetryOnFailure    bool                       `json:"skip_retry_on_failure"`
	IncludeUsingGroup     bool                       `json:"include_using_group"`
	IncludeModelName      bool                       `json:"include_model_name"`
	IncludeRuleName       bool                       `json:"include_rule_name"`
}

type ChannelAffinitySetting struct {
	Enabled               bool
	SwitchOnSuccess       bool
	KeepOnChannelDisabled bool
	MaxEntries            int
	DefaultTTLSeconds     int
	Rules                 []ChannelAffinityRule
}

var channelAffinityConfig atomic.Pointer[ChannelAffinitySetting]

func init() {
	config := defaultChannelAffinitySetting()
	channelAffinityConfig.Store(&config)
}

func defaultChannelAffinitySetting() ChannelAffinitySetting {
	return ChannelAffinitySetting{
		Enabled:               true,
		SwitchOnSuccess:       true,
		KeepOnChannelDisabled: false,
		MaxEntries:            DefaultChannelAffinityMaxEntries,
		DefaultTTLSeconds:     DefaultChannelAffinityTTLSeconds,
		Rules: []ChannelAffinityRule{
			{
				Name:       "codex cli trace",
				ModelRegex: []string{"^gpt-.*$"},
				PathRegex:  []string{"/v1/responses"},
				KeySources: []ChannelAffinityKeySource{{Type: "gjson", Path: "prompt_cache_key"}},
				ParamOverrideTemplate: passHeadersTemplate([]string{
					"Originator", "Session_id", "Thread_id", "Session-Id", "Thread-Id",
					"X-Client-Request-Id", "User-Agent", "X-Codex-Beta-Features",
					"X-Codex-Turn-State", "X-Codex-Turn-Metadata", "X-Codex-Window-Id",
					"X-Codex-Parent-Thread-Id", "X-OpenAI-Subagent", "X-OpenAI-Memgen-Request",
					"X-ResponsesAPI-Include-Timing-Metrics", "X-OpenAI-Internal-Codex-Responses-Lite",
				}),
				SkipRetryOnFailure: true,
				IncludeUsingGroup:  true,
				IncludeRuleName:    true,
			},
			{
				Name:       "claude cli trace",
				ModelRegex: []string{"^claude-.*$"},
				PathRegex:  []string{"/v1/messages"},
				KeySources: []ChannelAffinityKeySource{{Type: "gjson", Path: "metadata.user_id"}},
				ParamOverrideTemplate: passHeadersTemplate([]string{
					"X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-Os",
					"X-Stainless-Package-Version", "X-Stainless-Retry-Count", "X-Stainless-Runtime",
					"X-Stainless-Runtime-Version", "X-Stainless-Timeout", "User-Agent", "X-App",
					"Anthropic-Beta", "Anthropic-Dangerous-Direct-Browser-Access", "Anthropic-Version",
				}),
				SkipRetryOnFailure: true,
				IncludeUsingGroup:  true,
				IncludeRuleName:    true,
			},
		},
	}
}

func passHeadersTemplate(headers []string) map[string]any {
	values := make([]any, len(headers))
	for i, header := range headers {
		values[i] = header
	}
	return map[string]any{
		"operations": []any{map[string]any{
			"mode": "pass_headers", "value": values, "keep_origin": true,
		}},
	}
}

func ChannelAffinityOptionDefaults() map[string]string {
	config := defaultChannelAffinitySetting()
	rules, _ := common.Marshal(config.Rules)
	return map[string]string{
		ChannelAffinityEnabledOption:           strconv.FormatBool(config.Enabled),
		ChannelAffinitySwitchOnSuccessOption:   strconv.FormatBool(config.SwitchOnSuccess),
		ChannelAffinityKeepOnDisabledOption:    strconv.FormatBool(config.KeepOnChannelDisabled),
		ChannelAffinityMaxEntriesOption:        strconv.Itoa(config.MaxEntries),
		ChannelAffinityDefaultTTLSecondsOption: strconv.Itoa(config.DefaultTTLSeconds),
		ChannelAffinityRulesOption:             string(rules),
	}
}

func GetChannelAffinitySetting() ChannelAffinitySetting {
	config := channelAffinityConfig.Load()
	if config == nil {
		fallback := defaultChannelAffinitySetting()
		return fallback
	}
	return cloneChannelAffinitySetting(*config)
}

func IsChannelAffinityOption(key string) bool {
	switch key {
	case ChannelAffinityEnabledOption,
		ChannelAffinitySwitchOnSuccessOption,
		ChannelAffinityKeepOnDisabledOption,
		ChannelAffinityMaxEntriesOption,
		ChannelAffinityDefaultTTLSecondsOption,
		ChannelAffinityRulesOption:
		return true
	default:
		return false
	}
}

func buildChannelAffinitySetting(options map[string]string) (ChannelAffinitySetting, error) {
	config := defaultChannelAffinitySetting()
	var err error
	if config.Enabled, err = affinityBoolOption(options, ChannelAffinityEnabledOption, config.Enabled); err != nil {
		return ChannelAffinitySetting{}, err
	}
	if config.SwitchOnSuccess, err = affinityBoolOption(options, ChannelAffinitySwitchOnSuccessOption, config.SwitchOnSuccess); err != nil {
		return ChannelAffinitySetting{}, err
	}
	if config.KeepOnChannelDisabled, err = affinityBoolOption(options, ChannelAffinityKeepOnDisabledOption, config.KeepOnChannelDisabled); err != nil {
		return ChannelAffinitySetting{}, err
	}
	if config.MaxEntries, err = affinityIntOption(options, ChannelAffinityMaxEntriesOption, config.MaxEntries, 0, MaxChannelAffinityEntries); err != nil {
		return ChannelAffinitySetting{}, err
	}
	if config.DefaultTTLSeconds, err = affinityIntOption(options, ChannelAffinityDefaultTTLSecondsOption, config.DefaultTTLSeconds, 0, MaxChannelAffinityTTLSeconds); err != nil {
		return ChannelAffinitySetting{}, err
	}
	if raw := strings.TrimSpace(options[ChannelAffinityRulesOption]); raw != "" {
		if err := common.UnmarshalJsonStr(raw, &config.Rules); err != nil {
			return ChannelAffinitySetting{}, fmt.Errorf("invalid %s JSON: %w", ChannelAffinityRulesOption, err)
		}
	}
	if config.MaxEntries == 0 {
		config.MaxEntries = DefaultChannelAffinityMaxEntries
	}
	if config.DefaultTTLSeconds == 0 {
		config.DefaultTTLSeconds = DefaultChannelAffinityTTLSeconds
	}
	if err := validateChannelAffinityRules(config.Rules); err != nil {
		return ChannelAffinitySetting{}, err
	}
	return config, nil
}

func affinityBoolOption(options map[string]string, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(options[key])
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value, nil
}

func affinityIntOption(options map[string]string, key string, fallback, minValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(options[key])
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minValue, maxValue)
	}
	return value, nil
}

func validateChannelAffinityRules(rules []ChannelAffinityRule) error {
	seen := make(map[string]struct{}, len(rules))
	for i := range rules {
		rule := &rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Name == "" {
			return fmt.Errorf("channel affinity rule %d has an empty name", i+1)
		}
		if _, exists := seen[rule.Name]; exists {
			return fmt.Errorf("duplicate channel affinity rule name %q", rule.Name)
		}
		seen[rule.Name] = struct{}{}
		if len(rule.ModelRegex) == 0 {
			return fmt.Errorf("channel affinity rule %q requires model_regex", rule.Name)
		}
		if len(rule.KeySources) == 0 {
			return fmt.Errorf("channel affinity rule %q requires key_sources", rule.Name)
		}
		for _, pattern := range append(append([]string{}, rule.ModelRegex...), rule.PathRegex...) {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("channel affinity rule %q has invalid regex %q: %w", rule.Name, pattern, err)
			}
		}
		if rule.ValueRegex != "" {
			if _, err := regexp.Compile(rule.ValueRegex); err != nil {
				return fmt.Errorf("channel affinity rule %q has invalid value_regex: %w", rule.Name, err)
			}
		}
		if rule.TTLSeconds < 0 || rule.TTLSeconds > MaxChannelAffinityTTLSeconds {
			return fmt.Errorf("channel affinity rule %q ttl_seconds must be between 0 and %d", rule.Name, MaxChannelAffinityTTLSeconds)
		}
		for _, source := range rule.KeySources {
			switch source.Type {
			case "context_int", "context_string", "request_header":
				if strings.TrimSpace(source.Key) == "" {
					return fmt.Errorf("channel affinity rule %q source %q requires key", rule.Name, source.Type)
				}
			case "gjson":
				if strings.TrimSpace(source.Path) == "" {
					return fmt.Errorf("channel affinity rule %q gjson source requires path", rule.Name)
				}
			default:
				return fmt.Errorf("channel affinity rule %q has unsupported key source %q", rule.Name, source.Type)
			}
		}
		if err := validateChannelAffinityTemplate(rule.Name, rule.ParamOverrideTemplate); err != nil {
			return err
		}
	}
	return nil
}

func validateChannelAffinityTemplate(ruleName string, template map[string]any) error {
	if len(template) == 0 {
		return nil
	}
	for key := range template {
		if key != "operations" {
			return fmt.Errorf("channel affinity rule %q has unsupported override template key %q", ruleName, key)
		}
	}
	operations, ok := template["operations"].([]any)
	if !ok {
		return fmt.Errorf("channel affinity rule %q override operations must be an array", ruleName)
	}
	for _, rawOperation := range operations {
		operation, ok := rawOperation.(map[string]any)
		if !ok || operation["mode"] != "pass_headers" {
			return fmt.Errorf("channel affinity rule %q only supports pass_headers override operations", ruleName)
		}
		headers, ok := operation["value"].([]any)
		if !ok || len(headers) == 0 {
			return fmt.Errorf("channel affinity rule %q pass_headers operation requires a header array", ruleName)
		}
		for _, rawHeader := range headers {
			header, ok := rawHeader.(string)
			if !ok || strings.TrimSpace(header) == "" {
				return fmt.Errorf("channel affinity rule %q pass_headers contains an invalid header", ruleName)
			}
		}
	}
	return nil
}

func cloneChannelAffinitySetting(config ChannelAffinitySetting) ChannelAffinitySetting {
	cloned := config
	cloned.Rules = make([]ChannelAffinityRule, len(config.Rules))
	for i, rule := range config.Rules {
		cloned.Rules[i] = rule
		cloned.Rules[i].ModelRegex = append([]string(nil), rule.ModelRegex...)
		cloned.Rules[i].PathRegex = append([]string(nil), rule.PathRegex...)
		cloned.Rules[i].UserAgentInclude = append([]string(nil), rule.UserAgentInclude...)
		cloned.Rules[i].KeySources = append([]ChannelAffinityKeySource(nil), rule.KeySources...)
		if len(rule.ParamOverrideTemplate) > 0 {
			encoded, _ := common.Marshal(rule.ParamOverrideTemplate)
			var template map[string]any
			_ = common.Unmarshal(encoded, &template)
			cloned.Rules[i].ParamOverrideTemplate = template
		}
	}
	return cloned
}
