// Package advancedcustom defines the persisted route contract used by
// Advanced Custom channels. It deliberately contains no relay or database
// dependencies so save-time validation, discovery, and request dispatch can
// share the same bounded parser and matching semantics.
package customconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	ConverterNone                  = "none"
	ConverterClaudeToOpenAIChat    = "anthropic_messages_to_openai_chat_completions"
	ConverterOpenAIChatToClaude    = "openai_chat_completions_to_anthropic_messages"
	ConverterOpenAIChatToResponses = "openai_chat_completions_to_openai_responses"
	ConverterResponsesToOpenAIChat = "openai_responses_to_openai_chat_completions"
	ConverterResponsesToGemini     = "openai_responses_to_gemini_generate_content"
	ConverterGeminiToOpenAIChat    = "gemini_generate_content_to_openai_chat_completions"
	ConverterOpenAIChatToGemini    = "openai_chat_completions_to_gemini_generate_content"
	AuthNone                       = "none"
	AuthHeader                     = "header"
	AuthQuery                      = "query"
	APIKeyPlaceholder              = "{api_key}"
	ModelPlaceholder               = "{model}"
	ModelListPath                  = "/v1/models"
	MaxRoutes                      = 100
	MaxModelsPerRoute              = 10_000
	MaxModelNameBytes              = 512
	MaxAuthNameBytes               = 128
	MaxAuthValueBytes              = 8 << 10
	MaxSettingsBytes               = 1 << 20
)

type Settings struct {
	AdvancedCustom *Config `json:"advanced_custom"`
}

type Config struct {
	Routes []Route `json:"advanced_routes"`
}

type Route struct {
	IncomingPath string   `json:"incoming_path"`
	UpstreamPath string   `json:"upstream_path"`
	Converter    string   `json:"converter"`
	Models       []string `json:"models"`
	Auth         *Auth    `json:"auth"`
}

type Auth struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseSettings decodes and validates the advanced_custom object while
// allowing unrelated channel settings to coexist in the same JSON document.
func ParseSettings(raw string) (*Config, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("advanced_custom is required")
	}
	if len(raw) > MaxSettingsBytes {
		return nil, fmt.Errorf("advanced custom channel settings exceed %d bytes", MaxSettingsBytes)
	}
	var settings Settings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, errors.New("advanced custom channel settings must be valid JSON")
	}
	if settings.AdvancedCustom == nil {
		return nil, errors.New("advanced_custom is required")
	}
	if _, err := Validate(settings.AdvancedCustom); err != nil {
		return nil, err
	}
	return settings.AdvancedCustom, nil
}

// Validate checks all route data before it can reach the credential-bearing
// dispatch path. The bool reports whether an explicit model-list route exists.
func Validate(config *Config) (bool, error) {
	if config == nil {
		return false, errors.New("advanced_custom is required")
	}
	if len(config.Routes) == 0 || len(config.Routes) > MaxRoutes {
		return false, fmt.Errorf("advanced custom routes must contain 1 to %d entries", MaxRoutes)
	}

	modelListFound := false
	catchAllByPath := make(map[string]bool, len(config.Routes))
	modelsByPath := make(map[string]map[string]struct{}, len(config.Routes))
	for index := range config.Routes {
		route := &config.Routes[index]
		incomingPath := strings.TrimSpace(route.IncomingPath)
		if !validIncomingPath(incomingPath) {
			return false, fmt.Errorf("advanced custom route %d has an invalid incoming path", index)
		}
		if strings.Count(incomingPath, ModelPlaceholder) > 1 {
			return false, fmt.Errorf("advanced custom route %d has multiple model placeholders", index)
		}
		upstreamPath := strings.TrimSpace(route.UpstreamPath)
		if upstreamPath == "" {
			return false, fmt.Errorf("advanced custom route %d requires an upstream path", index)
		}
		if err := validateUpstreamTarget(upstreamPath); err != nil {
			return false, fmt.Errorf("advanced custom route %d: %w", index, err)
		}

		converter := normalizeConverter(route.Converter)
		if !AllowedConverter(converter) {
			return false, fmt.Errorf("advanced custom route %d has an unsupported converter", index)
		}
		if !converterMatchesPath(converter, incomingPath) {
			return false, fmt.Errorf("advanced custom route %d converter does not match its incoming path", index)
		}

		models, err := normalizeModels(route.Models)
		if err != nil {
			return false, fmt.Errorf("advanced custom route %d models: %w", index, err)
		}
		if incomingPath == ModelListPath {
			if modelListFound {
				return false, errors.New("advanced custom model-list route is duplicated")
			}
			modelListFound = true
			if converter != ConverterNone {
				return false, errors.New("advanced custom model-list converter must be none")
			}
			if len(models) != 0 {
				return false, errors.New("advanced custom model-list route models must be empty")
			}
			if strings.Contains(upstreamPath, ModelPlaceholder) {
				return false, errors.New("advanced custom model-list upstream path cannot contain {model}")
			}
		}

		if len(models) == 0 {
			if catchAllByPath[incomingPath] {
				return false, fmt.Errorf("advanced custom route %d duplicates a catch-all route", index)
			}
			catchAllByPath[incomingPath] = true
		} else {
			if catchAllByPath[incomingPath] {
				return false, fmt.Errorf("advanced custom route %d appears after a catch-all route", index)
			}
			seen := modelsByPath[incomingPath]
			if seen == nil {
				seen = make(map[string]struct{}, len(models))
				modelsByPath[incomingPath] = seen
			}
			for _, rule := range models {
				if expression, ok := strings.CutPrefix(rule, "re:"); ok {
					if expression == "" {
						return false, fmt.Errorf("advanced custom route %d has an empty model regex", index)
					}
					if _, err := regexp.Compile(expression); err != nil {
						return false, fmt.Errorf("advanced custom route %d has an invalid model regex", index)
					}
				}
				if _, exists := seen[rule]; exists {
					return false, fmt.Errorf("advanced custom route %d overlaps an earlier model rule", index)
				}
				seen[rule] = struct{}{}
			}
		}
		if err := validateAuth(index, route.Auth); err != nil {
			return false, err
		}
	}
	return modelListFound, nil
}

func validIncomingPath(path string) bool {
	return path != "" && strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") &&
		!strings.ContainsAny(path, "?#\r\n")
}

func validateUpstreamTarget(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("upstream path is invalid")
	}
	if parsed.IsAbs() {
		if parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("upstream URL must use http or https")
		}
		return nil
	}
	if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return errors.New("upstream path must be an absolute URL or start with /")
	}
	return nil
}

func normalizeConverter(converter string) string {
	converter = strings.TrimSpace(converter)
	if converter == "" {
		return ConverterNone
	}
	return converter
}

func AllowedConverter(converter string) bool {
	switch normalizeConverter(converter) {
	case ConverterNone, ConverterClaudeToOpenAIChat, ConverterOpenAIChatToClaude,
		ConverterOpenAIChatToResponses, ConverterResponsesToOpenAIChat,
		ConverterResponsesToGemini, ConverterGeminiToOpenAIChat,
		ConverterOpenAIChatToGemini:
		return true
	default:
		return false
	}
}

func converterMatchesPath(converter, incomingPath string) bool {
	switch converter {
	case ConverterNone:
		return true
	case ConverterClaudeToOpenAIChat:
		return incomingPath == "/v1/messages"
	case ConverterOpenAIChatToClaude, ConverterOpenAIChatToResponses, ConverterOpenAIChatToGemini:
		return incomingPath == "/v1/chat/completions"
	case ConverterResponsesToOpenAIChat, ConverterResponsesToGemini:
		return incomingPath == "/v1/responses"
	case ConverterGeminiToOpenAIChat:
		return strings.Contains(incomingPath, ":generateContent") || strings.Contains(incomingPath, ":streamGenerateContent")
	default:
		return false
	}
}

func normalizeModels(models []string) ([]string, error) {
	if len(models) > MaxModelsPerRoute {
		return nil, fmt.Errorf("model list exceeds %d entries", MaxModelsPerRoute)
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if !utf8.ValidString(model) || len(model) > MaxModelNameBytes {
			return nil, fmt.Errorf("model name must be valid UTF-8 and at most %d bytes", MaxModelNameBytes)
		}
		if strings.IndexFunc(model, unicode.IsControl) >= 0 {
			return nil, errors.New("model name contains a control character")
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	return result, nil
}

func validateAuth(index int, auth *Auth) error {
	if auth == nil {
		return nil
	}
	switch strings.TrimSpace(auth.Type) {
	case AuthNone:
		return nil
	case AuthHeader:
		if !ValidHeaderName(strings.TrimSpace(auth.Name)) || strings.TrimSpace(auth.Value) == "" {
			return fmt.Errorf("advanced custom route %d has invalid header authentication", index)
		}
	case AuthQuery:
		name := strings.TrimSpace(auth.Name)
		if name == "" || len(name) > MaxAuthNameBytes || strings.ContainsAny(name, "&=\r\n") || strings.TrimSpace(auth.Value) == "" {
			return fmt.Errorf("advanced custom route %d has invalid query authentication", index)
		}
	default:
		return fmt.Errorf("advanced custom route %d has an invalid authentication type", index)
	}
	if len(auth.Value) > MaxAuthValueBytes || strings.ContainsAny(auth.Value, "\r\n") {
		return fmt.Errorf("advanced custom route %d has an invalid authentication value", index)
	}
	return nil
}

func ValidHeaderName(name string) bool {
	if name == "" || len(name) > MaxAuthNameBytes {
		return false
	}
	for _, char := range name {
		if char > unicode.MaxASCII || !(char == '!' || char == '#' || char == '$' || char == '%' || char == '&' || char == '\'' ||
			char == '*' || char == '+' || char == '-' || char == '.' || char == '^' || char == '_' || char == '`' ||
			char == '|' || char == '~' || char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z') {
			return false
		}
	}
	return true
}

var modelRegexCache sync.Map

func matchModel(rules []string, model string) bool {
	if len(rules) == 0 {
		return true
	}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if expression, ok := strings.CutPrefix(rule, "re:"); ok {
			cached, exists := modelRegexCache.Load(expression)
			var compiled *regexp.Regexp
			if exists {
				compiled, _ = cached.(*regexp.Regexp)
			} else if candidate, err := regexp.Compile(expression); err == nil {
				actual, _ := modelRegexCache.LoadOrStore(expression, candidate)
				compiled, _ = actual.(*regexp.Regexp)
			}
			if compiled != nil && compiled.MatchString(model) {
				return true
			}
			continue
		}
		if rule == model {
			return true
		}
	}
	return false
}

func matchPath(configured, requested string) bool {
	configured = strings.TrimSpace(configured)
	requested = strings.TrimSpace(requested)
	if configured == requested {
		return true
	}
	if strings.Contains(configured, ":generateContent") || strings.Contains(configured, ":streamGenerateContent") {
		configured = strings.Replace(configured, ":streamGenerateContent", ":generateContent", 1)
		requested = strings.Replace(requested, ":streamGenerateContent", ":generateContent", 1)
	}
	if !strings.Contains(configured, ModelPlaceholder) {
		return configured == requested
	}
	parts := strings.Split(configured, ModelPlaceholder)
	if len(parts) != 2 || !strings.HasPrefix(requested, parts[0]) || !strings.HasSuffix(requested, parts[1]) {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(requested, parts[0]), parts[1])
	return middle != "" && !strings.Contains(middle, "/")
}

// Match returns the first path-and-model route. Model-specific routes are
// evaluated in persisted order; save-time validation requires catch-alls last.
func Match(config *Config, requestPath, originalModel string) (Route, bool) {
	if config == nil {
		return Route{}, false
	}
	requestPath = strings.Split(requestPath, "?")[0]
	for _, route := range config.Routes {
		if matchPath(route.IncomingPath, requestPath) && matchModel(route.Models, strings.TrimSpace(originalModel)) {
			route.Converter = normalizeConverter(route.Converter)
			return route, true
		}
	}
	return Route{}, false
}

// IncomingPathsForModel returns the configured downstream surfaces available
// to a model. It validates first so malformed legacy settings cannot cause the
// public model/pricing catalogs to advertise a route the live adapter rejects.
func IncomingPathsForModel(config *Config, originalModel string) ([]string, error) {
	if _, err := Validate(config); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(config.Routes))
	paths := make([]string, 0, len(config.Routes))
	for _, route := range config.Routes {
		if !matchModel(route.Models, strings.TrimSpace(originalModel)) {
			continue
		}
		path := strings.TrimSpace(route.IncomingPath)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths, nil
}

// ResolveURL constructs a route target without inheriting client query data.
// Relative paths append to any configured base path, matching channel UI
// expectations. Route query authentication is added last.
func ResolveURL(baseURL string, route Route, mappedModel string, stream bool, apiKey string) (string, error) {
	if strings.Contains(route.UpstreamPath, ModelPlaceholder) {
		if !utf8.ValidString(mappedModel) || mappedModel == "" || len(mappedModel) > MaxModelNameBytes ||
			strings.IndexFunc(mappedModel, unicode.IsControl) >= 0 || strings.ContainsAny(mappedModel, "?#") {
			return "", errors.New("advanced custom mapped model is invalid for URL substitution")
		}
	}
	target := strings.ReplaceAll(strings.TrimSpace(route.UpstreamPath), ModelPlaceholder, mappedModel)
	var resolved *url.URL
	parsed, err := url.Parse(target)
	if err != nil {
		return "", errors.New("advanced custom upstream URL is invalid")
	}
	if parsed.IsAbs() {
		resolved = parsed
	} else {
		base, parseErr := url.Parse(strings.TrimSpace(baseURL))
		if parseErr != nil || base.Scheme == "" || base.Host == "" || base.User != nil ||
			(base.Scheme != "http" && base.Scheme != "https") {
			return "", errors.New("advanced custom channel base URL is invalid")
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(parsed.Path, "/")
		base.RawPath = ""
		base.RawQuery = parsed.RawQuery
		resolved = base
	}
	if resolved.User != nil || resolved.Fragment != "" || resolved.Host == "" ||
		(resolved.Scheme != "http" && resolved.Scheme != "https") {
		return "", errors.New("advanced custom upstream URL is invalid")
	}
	converter := normalizeConverter(route.Converter)
	if stream && (converter == ConverterOpenAIChatToGemini || converter == ConverterResponsesToGemini) {
		resolved.Path = strings.Replace(resolved.Path, ":generateContent", ":streamGenerateContent", 1)
		if strings.Contains(resolved.Path, ":streamGenerateContent") {
			query := resolved.Query()
			query.Set("alt", "sse")
			resolved.RawQuery = query.Encode()
		}
	}
	if route.Auth != nil && strings.TrimSpace(route.Auth.Type) == AuthQuery {
		value := strings.ReplaceAll(route.Auth.Value, APIKeyPlaceholder, apiKey)
		if len(value) > MaxAuthValueBytes || strings.ContainsAny(value, "\r\n") {
			return "", errors.New("advanced custom query authentication value is invalid")
		}
		query := resolved.Query()
		query.Set(strings.TrimSpace(route.Auth.Name), value)
		resolved.RawQuery = query.Encode()
	}
	return resolved.String(), nil
}
