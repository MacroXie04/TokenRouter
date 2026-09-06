package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

const (
	GeminiSafetySettingsOption               = "gemini.safety_settings"
	GeminiVersionSettingsOption              = "gemini.version_settings"
	GeminiSupportedImagineModelsOption       = "gemini.supported_imagine_models"
	GeminiThinkingAdapterEnabledOption       = "gemini.thinking_adapter_enabled"
	GeminiThinkingBudgetPercentageOption     = "gemini.thinking_adapter_budget_tokens_percentage"
	GeminiFunctionCallThoughtSignatureOption = "gemini.function_call_thought_signature_enabled"
	GeminiRemoveFunctionResponseIDOption     = "gemini.remove_function_response_id_enabled"
	ClaudeModelHeadersSettingsOption         = "claude.model_headers_settings"
	ClaudeDefaultMaxTokensOption             = "claude.default_max_tokens"
	ClaudeThinkingAdapterEnabledOption       = "claude.thinking_adapter_enabled"
	ClaudeThinkingBudgetPercentageOption     = "claude.thinking_adapter_budget_tokens_percentage"
	DefaultClaudeMaxTokens                   = 8192
	maxModelPolicyOptionBytes                = 1 << 20
	maxModelPolicyEntries                    = 256
	maxModelPolicyIdentifierBytes            = 256
	maxClaudeHeaderNamesPerModel             = 32
	maxClaudeHeaderValuesPerName             = 32
	maxClaudeHeaderValueBytes                = 2048
	maxClaudeHeaderAggregateBytes            = 64 << 10
	maxConfiguredClaudeOutputTokens          = 1_000_000
	maxModelPolicyJSONDepth                  = 64
	minimumGeminiThinkingBudgetPercentage    = 0.002
	minimumClaudeThinkingBudgetPercentage    = 0.1
)

var supportedGeminiSafetyThresholds = map[string]struct{}{
	"OFF":                              {},
	"BLOCK_NONE":                       {},
	"BLOCK_ONLY_HIGH":                  {},
	"BLOCK_MEDIUM_AND_ABOVE":           {},
	"BLOCK_LOW_AND_ABOVE":              {},
	"HARM_BLOCK_THRESHOLD_UNSPECIFIED": {},
}

var defaultGeminiImagineModels = []string{
	"gemini-2.0-flash-exp-image-generation",
	"gemini-2.0-flash-exp",
	"gemini-3-pro-image-preview",
	"gemini-3-pro-image",
	"gemini-2.5-flash-image",
	"gemini-3.1-flash-image",
	"gemini-3.1-flash-image-preview",
}

// GeminiModelPolicy is the immutable Gemini portion of the live model-policy
// snapshot. Callers receive detached maps and slices.
type GeminiModelPolicy struct {
	SafetySettings                        map[string]string
	VersionSettings                       map[string]string
	SupportedImagineModels                []string
	ThinkingAdapterEnabled                bool
	ThinkingAdapterBudgetTokensPercentage float64
	FunctionCallThoughtSignatureEnabled   bool
	RemoveFunctionResponseIDEnabled       bool
}

// ClaudeModelPolicy is the immutable Claude portion of the live model-policy
// snapshot. Header settings are validated before publication and may not
// replace credentials, routing headers, or hop-by-hop transport headers.
type ClaudeModelPolicy struct {
	ModelHeadersSettings                  map[string]map[string][]string
	DefaultMaxTokens                      map[string]int
	ThinkingAdapterEnabled                bool
	ThinkingAdapterBudgetTokensPercentage float64
}

type ModelPolicySetting struct {
	Gemini GeminiModelPolicy
	Claude ClaudeModelPolicy
}

var modelPolicyConfig atomic.Pointer[ModelPolicySetting]

func init() {
	config := defaultModelPolicySetting()
	modelPolicyConfig.Store(&config)
}

func defaultModelPolicySetting() ModelPolicySetting {
	return ModelPolicySetting{
		Gemini: GeminiModelPolicy{
			SafetySettings: map[string]string{"default": "OFF"},
			VersionSettings: map[string]string{
				"default":        "v1beta",
				"gemini-1.0-pro": "v1",
			},
			SupportedImagineModels:                append([]string(nil), defaultGeminiImagineModels...),
			ThinkingAdapterEnabled:                false,
			ThinkingAdapterBudgetTokensPercentage: 0.6,
			FunctionCallThoughtSignatureEnabled:   true,
			RemoveFunctionResponseIDEnabled:       true,
		},
		Claude: ClaudeModelPolicy{
			ModelHeadersSettings:                  map[string]map[string][]string{},
			DefaultMaxTokens:                      map[string]int{"default": DefaultClaudeMaxTokens},
			ThinkingAdapterEnabled:                true,
			ThinkingAdapterBudgetTokensPercentage: 0.8,
		},
	}
}

// ModelPolicyOptionDefaults returns the complete reference-compatible default
// surface exposed by the root settings endpoint.
func ModelPolicyOptionDefaults() map[string]string {
	config := defaultModelPolicySetting()
	return map[string]string{
		GeminiSafetySettingsOption:               mustPolicyJSON(config.Gemini.SafetySettings),
		GeminiVersionSettingsOption:              mustPolicyJSON(config.Gemini.VersionSettings),
		GeminiSupportedImagineModelsOption:       mustPolicyJSON(config.Gemini.SupportedImagineModels),
		GeminiThinkingAdapterEnabledOption:       strconv.FormatBool(config.Gemini.ThinkingAdapterEnabled),
		GeminiThinkingBudgetPercentageOption:     strconv.FormatFloat(config.Gemini.ThinkingAdapterBudgetTokensPercentage, 'f', -1, 64),
		GeminiFunctionCallThoughtSignatureOption: strconv.FormatBool(config.Gemini.FunctionCallThoughtSignatureEnabled),
		GeminiRemoveFunctionResponseIDOption:     strconv.FormatBool(config.Gemini.RemoveFunctionResponseIDEnabled),
		ClaudeModelHeadersSettingsOption:         mustPolicyJSON(config.Claude.ModelHeadersSettings),
		ClaudeDefaultMaxTokensOption:             mustPolicyJSON(config.Claude.DefaultMaxTokens),
		ClaudeThinkingAdapterEnabledOption:       strconv.FormatBool(config.Claude.ThinkingAdapterEnabled),
		ClaudeThinkingBudgetPercentageOption:     strconv.FormatFloat(config.Claude.ThinkingAdapterBudgetTokensPercentage, 'f', -1, 64),
	}
}

func mustPolicyJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func buildModelPolicySetting(options map[string]string) (ModelPolicySetting, error) {
	config := defaultModelPolicySetting()
	if options == nil {
		return config, nil
	}
	var err error
	if raw := strings.TrimSpace(options[GeminiSafetySettingsOption]); raw != "" {
		if err = decodeBoundedPolicyJSON(raw, &config.Gemini.SafetySettings); err != nil {
			return ModelPolicySetting{}, fmt.Errorf("%s: %w", GeminiSafetySettingsOption, err)
		}
		if err = validateGeminiSafetySettings(config.Gemini.SafetySettings); err != nil {
			return ModelPolicySetting{}, err
		}
	}
	if raw := strings.TrimSpace(options[GeminiVersionSettingsOption]); raw != "" {
		if err = decodeBoundedPolicyJSON(raw, &config.Gemini.VersionSettings); err != nil {
			return ModelPolicySetting{}, fmt.Errorf("%s: %w", GeminiVersionSettingsOption, err)
		}
		if err = validateGeminiVersionSettings(config.Gemini.VersionSettings); err != nil {
			return ModelPolicySetting{}, err
		}
	}
	if raw := strings.TrimSpace(options[GeminiSupportedImagineModelsOption]); raw != "" {
		if err = decodeBoundedPolicyJSON(raw, &config.Gemini.SupportedImagineModels); err != nil {
			return ModelPolicySetting{}, fmt.Errorf("%s: %w", GeminiSupportedImagineModelsOption, err)
		}
		if err = validateModelIdentifierList(config.Gemini.SupportedImagineModels, GeminiSupportedImagineModelsOption); err != nil {
			return ModelPolicySetting{}, err
		}
	}
	if config.Gemini.ThinkingAdapterEnabled, err = modelPolicyBool(options, GeminiThinkingAdapterEnabledOption, config.Gemini.ThinkingAdapterEnabled); err != nil {
		return ModelPolicySetting{}, err
	}
	if config.Gemini.ThinkingAdapterBudgetTokensPercentage, err = modelPolicyRatio(
		options, GeminiThinkingBudgetPercentageOption, config.Gemini.ThinkingAdapterBudgetTokensPercentage,
		minimumGeminiThinkingBudgetPercentage,
	); err != nil {
		return ModelPolicySetting{}, err
	}
	if config.Gemini.FunctionCallThoughtSignatureEnabled, err = modelPolicyBool(
		options, GeminiFunctionCallThoughtSignatureOption, config.Gemini.FunctionCallThoughtSignatureEnabled,
	); err != nil {
		return ModelPolicySetting{}, err
	}
	if config.Gemini.RemoveFunctionResponseIDEnabled, err = modelPolicyBool(
		options, GeminiRemoveFunctionResponseIDOption, config.Gemini.RemoveFunctionResponseIDEnabled,
	); err != nil {
		return ModelPolicySetting{}, err
	}

	if raw := strings.TrimSpace(options[ClaudeModelHeadersSettingsOption]); raw != "" {
		if err = decodeBoundedPolicyJSON(raw, &config.Claude.ModelHeadersSettings); err != nil {
			return ModelPolicySetting{}, fmt.Errorf("%s: %w", ClaudeModelHeadersSettingsOption, err)
		}
		if err = validateClaudeModelHeaders(config.Claude.ModelHeadersSettings); err != nil {
			return ModelPolicySetting{}, err
		}
	}
	if raw := strings.TrimSpace(options[ClaudeDefaultMaxTokensOption]); raw != "" {
		if err = decodeBoundedPolicyJSON(raw, &config.Claude.DefaultMaxTokens); err != nil {
			return ModelPolicySetting{}, fmt.Errorf("%s: %w", ClaudeDefaultMaxTokensOption, err)
		}
		if err = validateClaudeDefaultMaxTokens(config.Claude.DefaultMaxTokens); err != nil {
			return ModelPolicySetting{}, err
		}
	}
	if config.Claude.ThinkingAdapterEnabled, err = modelPolicyBool(options, ClaudeThinkingAdapterEnabledOption, config.Claude.ThinkingAdapterEnabled); err != nil {
		return ModelPolicySetting{}, err
	}
	if config.Claude.ThinkingAdapterBudgetTokensPercentage, err = modelPolicyRatio(
		options, ClaudeThinkingBudgetPercentageOption, config.Claude.ThinkingAdapterBudgetTokensPercentage,
		minimumClaudeThinkingBudgetPercentage,
	); err != nil {
		return ModelPolicySetting{}, err
	}
	return config, nil
}

func decodeBoundedPolicyJSON(raw string, destination any) error {
	if len(raw) > maxModelPolicyOptionBytes || !utf8.ValidString(raw) {
		return errors.New("value exceeds the safe JSON size limit")
	}
	if err := rejectDuplicatePolicyJSON([]byte(raw)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	if err := decoder.Decode(destination); err != nil {
		return errors.New("value must contain valid JSON of the expected shape")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("value must contain exactly one JSON value")
	}
	return nil
}

func rejectDuplicatePolicyJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanPolicyJSONValue(decoder, 0); err != nil {
		return errors.New("value must contain valid JSON without duplicate object keys")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("value must contain exactly one JSON value")
	}
	return nil
}

func scanPolicyJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxModelPolicyJSONDepth {
		return errors.New("JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := scanPolicyJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid object closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := scanPolicyJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid array closing delimiter")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func modelPolicyBool(options map[string]string, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(options[key])
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func modelPolicyRatio(options map[string]string, key string, fallback, minimum float64) (float64, error) {
	raw := strings.TrimSpace(options[key])
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > 1 {
		return 0, fmt.Errorf("%s must be between %s and 1", key, strconv.FormatFloat(minimum, 'f', -1, 64))
	}
	return parsed, nil
}

func validateGeminiSafetySettings(settings map[string]string) error {
	if settings == nil || len(settings) > maxModelPolicyEntries {
		return fmt.Errorf("%s must be a JSON object with at most %d entries", GeminiSafetySettingsOption, maxModelPolicyEntries)
	}
	for category, threshold := range settings {
		if !validModelPolicyIdentifier(category) {
			return fmt.Errorf("%s contains an invalid category", GeminiSafetySettingsOption)
		}
		if threshold == "" {
			continue
		}
		if _, exists := supportedGeminiSafetyThresholds[threshold]; !exists {
			return fmt.Errorf("%s contains an unsupported threshold for %q", GeminiSafetySettingsOption, category)
		}
	}
	return nil
}

func validateGeminiVersionSettings(settings map[string]string) error {
	if settings == nil || len(settings) > maxModelPolicyEntries {
		return fmt.Errorf("%s must be a JSON object with at most %d entries", GeminiVersionSettingsOption, maxModelPolicyEntries)
	}
	for model, version := range settings {
		if !validModelPolicyIdentifier(model) || (version != "v1" && version != "v1beta") {
			return fmt.Errorf("%s accepts only bounded model keys and v1 or v1beta values", GeminiVersionSettingsOption)
		}
	}
	return nil
}

func validateModelIdentifierList(models []string, option string) error {
	if models == nil || len(models) > maxModelPolicyEntries {
		return fmt.Errorf("%s must be a JSON array with at most %d entries", option, maxModelPolicyEntries)
	}
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if !validModelPolicyIdentifier(model) {
			return fmt.Errorf("%s contains an invalid model identifier", option)
		}
		if _, duplicate := seen[model]; duplicate {
			return fmt.Errorf("%s contains duplicate model %q", option, model)
		}
		seen[model] = struct{}{}
	}
	return nil
}

func validModelPolicyIdentifier(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxModelPolicyIdentifierBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f || (character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func validateClaudeDefaultMaxTokens(settings map[string]int) error {
	if settings == nil || len(settings) > maxModelPolicyEntries {
		return fmt.Errorf("%s must be a JSON object with at most %d entries", ClaudeDefaultMaxTokensOption, maxModelPolicyEntries)
	}
	for model, tokens := range settings {
		if !validModelPolicyIdentifier(model) || tokens < 0 || tokens > maxConfiguredClaudeOutputTokens {
			return fmt.Errorf("%s accepts bounded model keys and values from 0 to %d", ClaudeDefaultMaxTokensOption, maxConfiguredClaudeOutputTokens)
		}
	}
	return nil
}

func validateClaudeModelHeaders(settings map[string]map[string][]string) error {
	if settings == nil || len(settings) > maxModelPolicyEntries {
		return fmt.Errorf("%s must be a JSON object with at most %d model entries", ClaudeModelHeadersSettingsOption, maxModelPolicyEntries)
	}
	aggregate := 0
	for model, headers := range settings {
		if !validModelPolicyIdentifier(model) || headers == nil || len(headers) > maxClaudeHeaderNamesPerModel {
			return fmt.Errorf("%s contains an invalid model or too many headers", ClaudeModelHeadersSettingsOption)
		}
		seenNames := make(map[string]struct{}, len(headers))
		for name, values := range headers {
			canonical := http.CanonicalHeaderKey(name)
			if !validHTTPHeaderName(name) || canonical == "" || canonical != http.CanonicalHeaderKey(strings.TrimSpace(name)) || !allowedClaudePolicyHeader(canonical) {
				return fmt.Errorf("%s contains unsafe header %q", ClaudeModelHeadersSettingsOption, name)
			}
			lower := strings.ToLower(canonical)
			if _, duplicate := seenNames[lower]; duplicate {
				return fmt.Errorf("%s contains duplicate header %q", ClaudeModelHeadersSettingsOption, name)
			}
			seenNames[lower] = struct{}{}
			if len(values) == 0 || len(values) > maxClaudeHeaderValuesPerName {
				return fmt.Errorf("%s contains an invalid value list for %q", ClaudeModelHeadersSettingsOption, name)
			}
			for _, value := range values {
				trimmed := strings.TrimSpace(value)
				if trimmed == "" || trimmed != value || len(value) > maxClaudeHeaderValueBytes || !validHeaderValue(value) {
					return fmt.Errorf("%s contains an unsafe value for %q", ClaudeModelHeadersSettingsOption, name)
				}
				aggregate += len(name) + len(value)
				if aggregate > maxClaudeHeaderAggregateBytes {
					return fmt.Errorf("%s exceeds the aggregate header limit", ClaudeModelHeadersSettingsOption)
				}
			}
		}
	}
	return nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') {
			continue
		}
		switch character {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func allowedClaudePolicyHeader(canonical string) bool {
	switch strings.ToLower(canonical) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key", "anthropic-version", "host",
		"content-length", "content-type", "connection", "keep-alive", "proxy-authenticate",
		"te", "trailer", "transfer-encoding", "upgrade", "cookie", "set-cookie", "forwarded":
		return false
	}
	lower := strings.ToLower(canonical)
	return strings.HasPrefix(lower, "anthropic-") || strings.HasPrefix(lower, "x-")
}

func validHeaderValue(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f || character > 0xff {
			return false
		}
	}
	return true
}

func GetModelPolicySetting() ModelPolicySetting {
	config := modelPolicyConfig.Load()
	if config == nil {
		fallback := defaultModelPolicySetting()
		return cloneModelPolicySetting(fallback)
	}
	return cloneModelPolicySetting(*config)
}

func cloneModelPolicySetting(config ModelPolicySetting) ModelPolicySetting {
	copyOf := config
	copyOf.Gemini.SafetySettings = cloneStringMap(config.Gemini.SafetySettings)
	copyOf.Gemini.VersionSettings = cloneStringMap(config.Gemini.VersionSettings)
	copyOf.Gemini.SupportedImagineModels = append([]string(nil), config.Gemini.SupportedImagineModels...)
	copyOf.Claude.DefaultMaxTokens = make(map[string]int, len(config.Claude.DefaultMaxTokens))
	for key, value := range config.Claude.DefaultMaxTokens {
		copyOf.Claude.DefaultMaxTokens[key] = value
	}
	copyOf.Claude.ModelHeadersSettings = make(map[string]map[string][]string, len(config.Claude.ModelHeadersSettings))
	for model, headers := range config.Claude.ModelHeadersSettings {
		copyOf.Claude.ModelHeadersSettings[model] = make(map[string][]string, len(headers))
		for name, values := range headers {
			copyOf.Claude.ModelHeadersSettings[model][name] = append([]string(nil), values...)
		}
	}
	return copyOf
}

func cloneStringMap(source map[string]string) map[string]string {
	copyOf := make(map[string]string, len(source))
	for key, value := range source {
		copyOf[key] = value
	}
	return copyOf
}

func GetGeminiSafetyThreshold(category string) string {
	policy := modelPolicyConfig.Load()
	if policy == nil {
		return "OFF"
	}
	if threshold := policy.Gemini.SafetySettings[category]; threshold != "" {
		return threshold
	}
	if threshold := policy.Gemini.SafetySettings["default"]; threshold != "" {
		return threshold
	}
	return "OFF"
}

func GetGeminiAPIVersion(model string) string {
	policy := modelPolicyConfig.Load()
	if policy == nil {
		return "v1beta"
	}
	if version := policy.Gemini.VersionSettings[model]; version != "" {
		return version
	}
	if version := policy.Gemini.VersionSettings["default"]; version != "" {
		return version
	}
	return "v1beta"
}

func GeminiModelSupportsImagine(model string) bool {
	policy := modelPolicyConfig.Load()
	if policy == nil {
		return false
	}
	for _, supported := range policy.Gemini.SupportedImagineModels {
		if supported == model {
			return true
		}
	}
	return false
}

func GetClaudeDefaultMaxTokens(model string) int {
	policy := modelPolicyConfig.Load()
	if policy == nil {
		return DefaultClaudeMaxTokens
	}
	if tokens, exists := policy.Claude.DefaultMaxTokens[model]; exists {
		return tokens
	}
	if tokens, exists := policy.Claude.DefaultMaxTokens["default"]; exists {
		return tokens
	}
	return DefaultClaudeMaxTokens
}

// ApplyClaudeModelHeaders merges bounded administrator-controlled headers for
// the client-visible model. Credential and transport headers are rejected at
// settings publication time, so this cannot replace upstream authentication.
func ApplyClaudeModelHeaders(model string, headers http.Header) {
	if headers == nil {
		return
	}
	policy := modelPolicyConfig.Load()
	if policy == nil {
		return
	}
	configured := policy.Claude.ModelHeadersSettings[model]
	keys := make([]string, 0, len(configured))
	for key := range configured {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := append([]string(nil), headers.Values(key)...)
		values = append(values, configured[key]...)
		merged := make([]string, 0, len(values))
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			for _, item := range strings.Split(value, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				if _, duplicate := seen[item]; duplicate {
					continue
				}
				seen[item] = struct{}{}
				merged = append(merged, item)
			}
		}
		if len(merged) > 0 {
			headers.Set(key, strings.Join(merged, ","))
		}
	}
}
