package settings

import (
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	ChannelAffinityEnabledOption           = "channel_affinity_setting.enabled"
	ChannelAffinitySwitchOnSuccessOption   = "channel_affinity_setting.switch_on_success"
	ChannelAffinityKeepOnDisabledOption    = "channel_affinity_setting.keep_on_channel_disabled"
	ChannelAffinityMaxEntriesOption        = "channel_affinity_setting.max_entries"
	ChannelAffinityDefaultTTLSecondsOption = "channel_affinity_setting.default_ttl_seconds"
	ChannelAffinityRulesOption             = "channel_affinity_setting.rules"
	DefaultChannelAffinityMaxEntries       = 10_000
	DefaultChannelAffinityTTLSeconds       = 3600
	MaxChannelAffinityEntries              = 100_000
	MaxChannelAffinityTTLSeconds           = 30 * 24 * 60 * 60

	MaxChannelAffinityRulesJSONBytes       = 256 * 1024
	MaxChannelAffinityRules                = 64
	MaxChannelAffinityRuleNameBytes        = 128
	MaxChannelAffinityStringBytes          = 1024
	MaxChannelAffinityRegexPatternsPerList = 32
	MaxChannelAffinityRegexPatternBytes    = 1024
	MaxChannelAffinityRegexBytesPerRule    = 16 * 1024
	MaxChannelAffinityUserAgentIncludes    = 32
	MaxChannelAffinityKeySources           = 16
	MaxChannelAffinityTemplateBytes        = 16 * 1024
	MaxChannelAffinityTemplateDepth        = 8
	MaxChannelAffinityTemplateNodes        = 256
	MaxChannelAffinityTemplateOperations   = 8
	MaxChannelAffinityHeadersPerOperation  = 64
	MaxChannelAffinityTemplateHeaders      = 128
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

	compiledModelRegex []*regexp.Regexp
	compiledPathRegex  []*regexp.Regexp
	compiledValueRegex *regexp.Regexp
	hasValueRegex      bool
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
	config := ChannelAffinitySetting{
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
	if err := validateChannelAffinityRules(config.Rules); err != nil {
		panic(fmt.Sprintf("invalid default channel affinity setting: %v", err))
	}
	return config
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
	rules, _ := jsonutil.Marshal(config.Rules)
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
	if raw := options[ChannelAffinityRulesOption]; raw != "" {
		if len(raw) > MaxChannelAffinityRulesJSONBytes {
			return ChannelAffinitySetting{}, fmt.Errorf("%s must not exceed %d bytes", ChannelAffinityRulesOption, MaxChannelAffinityRulesJSONBytes)
		}
		raw = strings.TrimSpace(raw)
		if raw != "" {
			if err := jsonutil.ValidateJSONNoDuplicateKeys([]byte(raw)); err != nil {
				return ChannelAffinitySetting{}, fmt.Errorf("invalid %s JSON: %w", ChannelAffinityRulesOption, err)
			}
			var parsedRules []ChannelAffinityRule
			if err := jsonutil.UnmarshalJsonStr(raw, &parsedRules); err != nil {
				return ChannelAffinitySetting{}, fmt.Errorf("invalid %s JSON: %w", ChannelAffinityRulesOption, err)
			}
			config.Rules = parsedRules
		}
	}
	// MaxEntries deliberately keeps an explicitly configured zero. The service
	// interprets it as no-cache mode; only an absent or blank option defaults.
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
	if len(rules) > MaxChannelAffinityRules {
		return fmt.Errorf("channel affinity rules must not contain more than %d rules", MaxChannelAffinityRules)
	}
	seen := make(map[string]struct{}, len(rules))
	for i := range rules {
		rule := &rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Name == "" {
			return fmt.Errorf("channel affinity rule %d has an empty name", i+1)
		}
		if len(rule.Name) > MaxChannelAffinityRuleNameBytes {
			return fmt.Errorf("channel affinity rule %d name must not exceed %d bytes", i+1, MaxChannelAffinityRuleNameBytes)
		}
		if _, exists := seen[rule.Name]; exists {
			return fmt.Errorf("duplicate channel affinity rule name %q", rule.Name)
		}
		seen[rule.Name] = struct{}{}
		if len(rule.ModelRegex) == 0 {
			return fmt.Errorf("channel affinity rule %q requires model_regex", rule.Name)
		}
		if len(rule.ModelRegex) > MaxChannelAffinityRegexPatternsPerList {
			return fmt.Errorf("channel affinity rule %q model_regex must not contain more than %d patterns", rule.Name, MaxChannelAffinityRegexPatternsPerList)
		}
		if len(rule.PathRegex) > MaxChannelAffinityRegexPatternsPerList {
			return fmt.Errorf("channel affinity rule %q path_regex must not contain more than %d patterns", rule.Name, MaxChannelAffinityRegexPatternsPerList)
		}
		if len(rule.UserAgentInclude) > MaxChannelAffinityUserAgentIncludes {
			return fmt.Errorf("channel affinity rule %q user_agent_include must not contain more than %d values", rule.Name, MaxChannelAffinityUserAgentIncludes)
		}
		if len(rule.KeySources) == 0 {
			return fmt.Errorf("channel affinity rule %q requires key_sources", rule.Name)
		}
		if len(rule.KeySources) > MaxChannelAffinityKeySources {
			return fmt.Errorf("channel affinity rule %q key_sources must not contain more than %d sources", rule.Name, MaxChannelAffinityKeySources)
		}

		regexBytes := 0
		var err error
		if rule.compiledModelRegex, regexBytes, err = compileChannelAffinityRegexList(rule.Name, "model_regex", rule.ModelRegex, regexBytes); err != nil {
			return err
		}
		if rule.compiledPathRegex, regexBytes, err = compileChannelAffinityRegexList(rule.Name, "path_regex", rule.PathRegex, regexBytes); err != nil {
			return err
		}
		if rule.ValueRegex != "" {
			rule.hasValueRegex = true
			if len(rule.ValueRegex) > MaxChannelAffinityRegexPatternBytes {
				return fmt.Errorf("channel affinity rule %q value_regex must not exceed %d bytes", rule.Name, MaxChannelAffinityRegexPatternBytes)
			}
			regexBytes += len(rule.ValueRegex)
			if regexBytes > MaxChannelAffinityRegexBytesPerRule {
				return fmt.Errorf("channel affinity rule %q regexes must not exceed %d bytes in total", rule.Name, MaxChannelAffinityRegexBytesPerRule)
			}
			if rule.compiledValueRegex, err = regexp.Compile(rule.ValueRegex); err != nil {
				return fmt.Errorf("channel affinity rule %q has invalid value_regex: %w", rule.Name, err)
			}
		} else {
			rule.compiledValueRegex = nil
			rule.hasValueRegex = false
		}
		if rule.TTLSeconds < 0 || rule.TTLSeconds > MaxChannelAffinityTTLSeconds {
			return fmt.Errorf("channel affinity rule %q ttl_seconds must be between 0 and %d", rule.Name, MaxChannelAffinityTTLSeconds)
		}
		for sourceIndex := range rule.KeySources {
			source := &rule.KeySources[sourceIndex]
			if err := validateChannelAffinityString(rule.Name, fmt.Sprintf("key_sources[%d].type", sourceIndex), source.Type); err != nil {
				return err
			}
			if err := validateChannelAffinityString(rule.Name, fmt.Sprintf("key_sources[%d].key", sourceIndex), source.Key); err != nil {
				return err
			}
			if err := validateChannelAffinityString(rule.Name, fmt.Sprintf("key_sources[%d].path", sourceIndex), source.Path); err != nil {
				return err
			}
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
		for includeIndex, include := range rule.UserAgentInclude {
			if strings.TrimSpace(include) == "" {
				return fmt.Errorf("channel affinity rule %q user_agent_include[%d] must not be empty", rule.Name, includeIndex)
			}
			if err := validateChannelAffinityString(rule.Name, fmt.Sprintf("user_agent_include[%d]", includeIndex), include); err != nil {
				return err
			}
		}
		if err := validateChannelAffinityTemplate(rule.Name, rule.ParamOverrideTemplate); err != nil {
			return err
		}
	}
	return nil
}

func compileChannelAffinityRegexList(ruleName, field string, patterns []string, totalBytes int) ([]*regexp.Regexp, int, error) {
	compiled := make([]*regexp.Regexp, len(patterns))
	for i, pattern := range patterns {
		if pattern == "" {
			return nil, totalBytes, fmt.Errorf("channel affinity rule %q %s[%d] must not be empty", ruleName, field, i)
		}
		if len(pattern) > MaxChannelAffinityRegexPatternBytes {
			return nil, totalBytes, fmt.Errorf("channel affinity rule %q %s[%d] must not exceed %d bytes", ruleName, field, i, MaxChannelAffinityRegexPatternBytes)
		}
		totalBytes += len(pattern)
		if totalBytes > MaxChannelAffinityRegexBytesPerRule {
			return nil, totalBytes, fmt.Errorf("channel affinity rule %q regexes must not exceed %d bytes in total", ruleName, MaxChannelAffinityRegexBytesPerRule)
		}
		var err error
		compiled[i], err = regexp.Compile(pattern)
		if err != nil {
			return nil, totalBytes, fmt.Errorf("channel affinity rule %q has invalid regex in %s pattern %q: %w", ruleName, field, pattern, err)
		}
	}
	return compiled, totalBytes, nil
}

func validateChannelAffinityString(ruleName, field, value string) error {
	if len(value) > MaxChannelAffinityStringBytes {
		return fmt.Errorf("channel affinity rule %q %s must not exceed %d bytes", ruleName, field, MaxChannelAffinityStringBytes)
	}
	return nil
}

func validateChannelAffinityTemplate(ruleName string, template map[string]any) error {
	if len(template) == 0 {
		return nil
	}
	encoded, err := jsonutil.Marshal(template)
	if err != nil {
		return fmt.Errorf("channel affinity rule %q has an invalid override template: %w", ruleName, err)
	}
	if len(encoded) > MaxChannelAffinityTemplateBytes {
		return fmt.Errorf("channel affinity rule %q override template must not exceed %d bytes", ruleName, MaxChannelAffinityTemplateBytes)
	}
	state := channelAffinityTemplateValidationState{}
	if err := validateChannelAffinityTemplateValue(ruleName, template, 1, &state); err != nil {
		return err
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
	if len(operations) > MaxChannelAffinityTemplateOperations {
		return fmt.Errorf("channel affinity rule %q override template must not contain more than %d operations", ruleName, MaxChannelAffinityTemplateOperations)
	}
	totalHeaders := 0
	for operationIndex, rawOperation := range operations {
		operation, ok := rawOperation.(map[string]any)
		if !ok {
			return fmt.Errorf("channel affinity rule %q override operation %d must be an object", ruleName, operationIndex)
		}
		for key := range operation {
			switch key {
			case "mode", "value", "keep_origin":
			default:
				return fmt.Errorf("channel affinity rule %q has unsupported override operation key %q", ruleName, key)
			}
		}
		mode, ok := operation["mode"].(string)
		if !ok || mode != "pass_headers" {
			return fmt.Errorf("channel affinity rule %q only supports pass_headers override operations", ruleName)
		}
		if keepOrigin, present := operation["keep_origin"]; present {
			if _, ok := keepOrigin.(bool); !ok {
				return fmt.Errorf("channel affinity rule %q pass_headers keep_origin must be a boolean", ruleName)
			}
		}
		headers, ok := operation["value"].([]any)
		if !ok || len(headers) == 0 {
			return fmt.Errorf("channel affinity rule %q pass_headers operation requires a header array", ruleName)
		}
		if len(headers) > MaxChannelAffinityHeadersPerOperation {
			return fmt.Errorf("channel affinity rule %q pass_headers operation must not contain more than %d headers", ruleName, MaxChannelAffinityHeadersPerOperation)
		}
		totalHeaders += len(headers)
		if totalHeaders > MaxChannelAffinityTemplateHeaders {
			return fmt.Errorf("channel affinity rule %q override template must not contain more than %d headers in total", ruleName, MaxChannelAffinityTemplateHeaders)
		}
		for _, rawHeader := range headers {
			header, ok := rawHeader.(string)
			if !ok || !IsChannelAffinityPassthroughHeaderAllowed(header) {
				return fmt.Errorf("channel affinity rule %q pass_headers contains an unsafe header", ruleName)
			}
		}
	}
	return nil
}

// IsChannelAffinityPassthroughHeaderAllowed reports whether a client header
// may be copied onto an already-authenticated provider request. Affinity rules
// are intended for trace/session hints, never credentials, authority,
// forwarding metadata, or hop-by-hop transport state.
func IsChannelAffinityPassthroughHeaderAllowed(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || !validHTTPHeaderName(name) {
		return false
	}
	canonical := http.CanonicalHeaderKey(name)
	if canonical == "" {
		return false
	}
	lower := strings.ToLower(canonical)
	switch lower {
	case "authorization", "proxy-authorization", "proxy-authenticate",
		"cookie", "set-cookie", "host", "content-length", "content-type",
		"connection", "proxy-connection", "keep-alive", "te", "trailer",
		"transfer-encoding", "upgrade", "forwarded", "via", "x-real-ip",
		"cf-connecting-ip", "true-client-ip", "x-api-key", "api-key",
		"x-goog-api-key", "ocp-apim-subscription-key", "openai-organization",
		"openai-project", "x-goog-user-project", "sec-websocket-key",
		"sec-websocket-protocol", "sec-websocket-accept",
		"sec-websocket-extensions", "sec-websocket-version":
		return false
	}
	if strings.HasPrefix(lower, "x-forwarded-") ||
		strings.Contains(lower, "auth") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "key") || strings.Contains(lower, "secret") ||
		strings.Contains(lower, "credential") || strings.Contains(lower, "password") ||
		strings.Contains(lower, "signature") {
		return false
	}

	// A denylist alone cannot anticipate provider-specific credential names.
	// Keep passthrough confined to the trace/session metadata families used by
	// the built-in CLI affinity rules and common distributed-tracing clients.
	switch lower {
	case "originator", "session_id", "thread_id", "session-id", "thread-id",
		"user-agent", "x-client-request-id", "x-app", "anthropic-beta",
		"anthropic-dangerous-direct-browser-access", "anthropic-version",
		"x-openai-subagent", "x-openai-memgen-request",
		"x-openai-internal-codex-responses-lite",
		"x-responsesapi-include-timing-metrics", "traceparent", "tracestate",
		"baggage", "x-trace", "x-request-id", "x-correlation-id":
		return true
	}
	return strings.HasPrefix(lower, "x-codex-") ||
		strings.HasPrefix(lower, "x-stainless-") ||
		strings.HasPrefix(lower, "x-trace-") ||
		strings.HasPrefix(lower, "x-request-") ||
		strings.HasPrefix(lower, "x-correlation-")
}

type channelAffinityTemplateValidationState struct {
	nodes int
}

func validateChannelAffinityTemplateValue(ruleName string, value any, depth int, state *channelAffinityTemplateValidationState) error {
	if depth > MaxChannelAffinityTemplateDepth {
		return fmt.Errorf("channel affinity rule %q override template must not exceed depth %d", ruleName, MaxChannelAffinityTemplateDepth)
	}
	state.nodes++
	if state.nodes > MaxChannelAffinityTemplateNodes {
		return fmt.Errorf("channel affinity rule %q override template must not contain more than %d nodes", ruleName, MaxChannelAffinityTemplateNodes)
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if err := validateChannelAffinityString(ruleName, "override template key", key); err != nil {
				return err
			}
			if err := validateChannelAffinityTemplateValue(ruleName, child, depth+1, state); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateChannelAffinityTemplateValue(ruleName, child, depth+1, state); err != nil {
				return err
			}
		}
	case string:
		if err := validateChannelAffinityString(ruleName, "override template string", typed); err != nil {
			return err
		}
	case nil, bool, float64:
	default:
		return fmt.Errorf("channel affinity rule %q override template contains unsupported value type %T", ruleName, value)
	}
	return nil
}

// MatchesModel uses the immutable regexp snapshot compiled before this rule
// was published. It is safe for concurrent use and never compiles request data.
func (rule ChannelAffinityRule) MatchesModel(value string) bool {
	return matchesChannelAffinityRegex(rule.compiledModelRegex, value)
}

// HasPathMatchers reports whether the validated rule constrains request paths.
func (rule ChannelAffinityRule) HasPathMatchers() bool {
	return len(rule.compiledPathRegex) > 0
}

// MatchesPath uses the immutable path-regexp snapshot compiled at publication.
func (rule ChannelAffinityRule) MatchesPath(value string) bool {
	return matchesChannelAffinityRegex(rule.compiledPathRegex, value)
}

// MatchesValue accepts any non-empty value when value_regex is unset, and
// otherwise uses the immutable value-regexp snapshot compiled at publication.
func (rule ChannelAffinityRule) MatchesValue(value string) bool {
	if value == "" {
		return false
	}
	if !rule.hasValueRegex {
		return true
	}
	return rule.compiledValueRegex != nil && rule.compiledValueRegex.MatchString(value)
}

func matchesChannelAffinityRegex(patterns []*regexp.Regexp, value string) bool {
	if value == "" {
		return false
	}
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
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
		cloned.Rules[i].compiledModelRegex = append([]*regexp.Regexp(nil), rule.compiledModelRegex...)
		cloned.Rules[i].compiledPathRegex = append([]*regexp.Regexp(nil), rule.compiledPathRegex...)
		if rule.ParamOverrideTemplate != nil {
			cloned.Rules[i].ParamOverrideTemplate = cloneChannelAffinityTemplateValue(rule.ParamOverrideTemplate).(map[string]any)
		}
	}
	return cloned
}

func cloneChannelAffinityTemplateValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, child := range typed {
			cloned[key] = cloneChannelAffinityTemplateValue(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, child := range typed {
			cloned[i] = cloneChannelAffinityTemplateValue(child)
		}
		return cloned
	default:
		return typed
	}
}
