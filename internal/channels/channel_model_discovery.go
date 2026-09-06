package channels

import (
	"context"
	"errors"
	"fmt"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	advancedconfig "github.com/tokenrouter/tokenrouter/internal/relay/customconfig"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

const (
	advancedCustomModelsPath        = advancedconfig.ModelListPath
	maxModelDiscoveryHeaders        = 100
	maxModelDiscoveryHeaderName     = 128
	maxModelDiscoveryHeaderValue    = 8 << 10
	advancedCustomAuthNone          = advancedconfig.AuthNone
	advancedCustomAuthHeader        = advancedconfig.AuthHeader
	advancedCustomAuthQuery         = advancedconfig.AuthQuery
	advancedCustomAPIKeyPlaceholder = advancedconfig.APIKeyPlaceholder
)

type channelModelDiscoveryPlan struct {
	requestURL     string
	headers        http.Header
	host           string
	geminiResponse bool
	strictOpenAI   bool
}

type advancedCustomModelSettings = advancedconfig.Settings
type advancedCustomModelConfig = advancedconfig.Config
type advancedCustomModelRoute = advancedconfig.Route

func channelSupportsUpstreamModelDiscovery(channelType channelcatalog.ChannelType) bool {
	switch channelType {
	case channelcatalog.ChannelTypeAnthropic,
		channelcatalog.ChannelTypeGemini,
		channelcatalog.ChannelTypeCodex,
		channelcatalog.ChannelTypeAdvancedCustom,
		channelcatalog.ChannelTypeAli,
		channelcatalog.ChannelTypeZhipuV4,
		channelcatalog.ChannelTypeVolcEngine,
		channelcatalog.ChannelTypeMoonshot:
		return true
	default:
		return channelcatalog.IsOpenAICompatibleChannelType(channelType)
	}
}

func buildChannelModelDiscoveryPlan(ctx context.Context, channel *model.Channel, baseURL, credential string) (channelModelDiscoveryPlan, error) {
	plan := channelModelDiscoveryPlan{headers: make(http.Header)}
	channelType := channelcatalog.ChannelType(channel.Type)
	if channelType == channelcatalog.ChannelTypeAdvancedCustom {
		advancedPlan, err := buildAdvancedCustomModelDiscoveryPlan(channel, baseURL, credential)
		if err != nil {
			return plan, err
		}
		plan = advancedPlan
	} else {
		plan.requestURL = strings.TrimRight(baseURL, "/") + "/v1/models"
		switch channelType {
		case channelcatalog.ChannelTypeGemini:
			if credential == "" {
				return plan, errors.New("Gemini channel API key is empty")
			}
			plan.requestURL = strings.TrimRight(baseURL, "/") + "/v1beta/models"
			plan.headers.Set("x-goog-api-key", credential)
			plan.geminiResponse = true
		case channelcatalog.ChannelTypeAnthropic:
			if credential == "" {
				return plan, errors.New("Anthropic channel API key is empty")
			}
			plan.headers.Set("x-api-key", credential)
			plan.headers.Set("anthropic-version", "2023-06-01")
		case channelcatalog.ChannelTypeAzure:
			if credential == "" {
				return plan, errors.New("Azure OpenAI API key is empty")
			}
			version := strings.TrimSpace(channel.Other)
			if version == "" {
				version = env.GetEnv("AZURE_DEFAULT_API_VERSION", "2025-04-01-preview")
			}
			plan.requestURL = strings.TrimRight(baseURL, "/") + "/openai/models?" + url.Values{"api-version": []string{version}}.Encode()
			plan.headers.Set("api-key", credential)
		case channelcatalog.ChannelTypeAli:
			plan.requestURL = strings.TrimRight(baseURL, "/") + "/compatible-mode/v1/models"
			setBearerCredential(plan.headers, credential)
		case channelcatalog.ChannelTypeZhipuV4:
			if specialBase, ok := channelModelDiscoverySpecialBases[baseURL]; ok {
				plan.requestURL = strings.TrimRight(specialBase, "/") + "/models"
			} else {
				plan.requestURL = strings.TrimRight(baseURL, "/") + "/api/paas/v4/models"
			}
			setBearerCredential(plan.headers, credential)
		case channelcatalog.ChannelTypeVolcEngine:
			if specialBase, ok := channelModelDiscoverySpecialBases[baseURL]; ok {
				plan.requestURL = strings.TrimRight(specialBase, "/") + "/v1/models"
			}
			setBearerCredential(plan.headers, credential)
		case channelcatalog.ChannelTypeMoonshot:
			if specialBase, ok := channelModelDiscoverySpecialBases[baseURL]; ok {
				plan.requestURL = strings.TrimRight(specialBase, "/") + "/models"
			}
			setBearerCredential(plan.headers, credential)
		default:
			setBearerCredential(plan.headers, credential)
			if channelType == channelcatalog.ChannelTypeOpenAI && strings.TrimSpace(channel.OpenAIOrganization) != "" {
				plan.headers.Set("OpenAI-Organization", strings.TrimSpace(channel.OpenAIOrganization))
			}
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, plan.requestURL, nil)
	if err != nil {
		return plan, errors.New("invalid upstream model endpoint")
	}
	request.Header = plan.headers.Clone()
	if err := applyModelDiscoveryHeaderOverrides(channel, credential, request); err != nil {
		return plan, err
	}
	plan.headers = request.Header
	plan.host = request.Host
	return plan, nil
}

var channelModelDiscoverySpecialBases = map[string]string{
	"glm-coding-plan":               "https://open.bigmodel.cn/api/coding/paas/v4",
	"glm-coding-plan-international": "https://api.z.ai/api/coding/paas/v4",
	"kimi-coding-plan":              "https://api.kimi.com/coding/v1",
	"doubao-coding-plan":            "https://ark.cn-beijing.volces.com/api/coding/v3",
}

func setBearerCredential(headers http.Header, credential string) {
	if credential != "" {
		headers.Set("Authorization", "Bearer "+credential)
	}
}

func buildAdvancedCustomModelDiscoveryPlan(channel *model.Channel, baseURL, credential string) (channelModelDiscoveryPlan, error) {
	plan := channelModelDiscoveryPlan{headers: make(http.Header), strictOpenAI: true}
	var settings advancedCustomModelSettings
	if err := jsonutil.UnmarshalJsonStr(strings.TrimSpace(channel.OtherSettings), &settings); err != nil || settings.AdvancedCustom == nil {
		return plan, errors.New("advanced custom model-list configuration is required")
	}
	if _, err := validateAdvancedCustomModelConfig(settings.AdvancedCustom); err != nil {
		return plan, err
	}
	var selected *advancedCustomModelRoute
	for i := range settings.AdvancedCustom.Routes {
		route := &settings.AdvancedCustom.Routes[i]
		if strings.TrimSpace(route.IncomingPath) == advancedCustomModelsPath {
			if selected != nil {
				return plan, errors.New("advanced custom model-list route is duplicated")
			}
			selected = route
		}
	}
	if selected == nil {
		return plan, errors.New("advanced custom model-list route is required")
	}
	resolved, err := resolveAdvancedCustomModelURL(baseURL, selected.UpstreamPath)
	if err != nil {
		return plan, err
	}
	plan.requestURL = resolved
	if selected.Auth == nil {
		setBearerCredential(plan.headers, credential)
		return plan, nil
	}
	authType := strings.TrimSpace(selected.Auth.Type)
	value := strings.ReplaceAll(selected.Auth.Value, advancedCustomAPIKeyPlaceholder, credential)
	if len(value) > maxModelDiscoveryHeaderValue || strings.ContainsAny(value, "\r\n") {
		return plan, errors.New("advanced custom model-list authentication value is invalid")
	}
	switch authType {
	case advancedCustomAuthNone:
	case advancedCustomAuthHeader:
		name := strings.TrimSpace(selected.Auth.Name)
		if !validModelDiscoveryHeaderName(name) || strings.TrimSpace(selected.Auth.Value) == "" {
			return plan, errors.New("advanced custom model-list authentication header is invalid")
		}
		plan.headers.Set(name, value)
	case advancedCustomAuthQuery:
		name := strings.TrimSpace(selected.Auth.Name)
		if name == "" || len(name) > maxModelDiscoveryHeaderName || strings.ContainsAny(name, "&=\r\n") || strings.TrimSpace(selected.Auth.Value) == "" {
			return plan, errors.New("advanced custom model-list authentication query name is invalid")
		}
		parsed, err := url.Parse(plan.requestURL)
		if err != nil {
			return plan, errors.New("advanced custom model-list endpoint is invalid")
		}
		query := parsed.Query()
		query.Set(name, value)
		parsed.RawQuery = query.Encode()
		plan.requestURL = parsed.String()
	default:
		return plan, errors.New("advanced custom model-list authentication type is invalid")
	}
	return plan, nil
}

func validateAdvancedCustomModelConfig(config *advancedCustomModelConfig) (bool, error) {
	return advancedconfig.Validate(config)
}

func resolveAdvancedCustomModelURL(baseURL, upstreamPath string) (string, error) {
	return advancedconfig.ResolveURL(baseURL, advancedconfig.Route{UpstreamPath: upstreamPath}, "", false, "")
}

func applyModelDiscoveryHeaderOverrides(channel *model.Channel, credential string, request *http.Request) error {
	raw := strings.TrimSpace(channel.HeaderOverride)
	if raw == "" {
		return nil
	}
	if len(raw) > maxChannelUpstreamSettingsBytes {
		return errors.New("channel header override is too large")
	}
	overrides := make(map[string]string)
	if err := jsonutil.UnmarshalJsonStr(raw, &overrides); err != nil {
		return errors.New("channel header override must be a JSON object of strings")
	}
	if len(overrides) > maxModelDiscoveryHeaders {
		return fmt.Errorf("channel header override exceeds %d entries", maxModelDiscoveryHeaders)
	}
	for name, value := range overrides {
		name = strings.TrimSpace(name)
		lowerName := strings.ToLower(name)
		if name == "*" || strings.HasPrefix(lowerName, "re:") || strings.HasPrefix(lowerName, "regex:") {
			continue
		}
		if !validModelDiscoveryHeaderName(name) || blockedModelDiscoveryHeader(name) {
			return fmt.Errorf("channel header override %q is not allowed", name)
		}
		value = strings.ReplaceAll(value, advancedCustomAPIKeyPlaceholder, credential)
		if strings.HasPrefix(strings.TrimSpace(value), "{client_header:") {
			continue
		}
		if len(value) > maxModelDiscoveryHeaderValue || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("channel header override %q has an invalid value", name)
		}
		if strings.EqualFold(name, "Host") {
			if value == "" || strings.ContainsAny(value, "/?#@") {
				return errors.New("channel Host override is invalid")
			}
			request.Host = value
			continue
		}
		if value == "" {
			request.Header.Del(name)
		} else {
			request.Header.Set(name, value)
		}
	}
	return nil
}

func validModelDiscoveryHeaderName(name string) bool {
	if name == "" || len(name) > maxModelDiscoveryHeaderName {
		return false
	}
	for _, char := range name {
		if char > unicode.MaxASCII || !(char == '!' || char == '#' || char == '$' || char == '%' || char == '&' || char == '\'' ||
			char == '*' || char == '+' || char == '-' || char == '.' || char == '^' || char == '_' || char == '`' ||
			char == '|' || char == '~' || (char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z') ||
			(char >= 'a' && char <= 'z')) {
			return false
		}
	}
	return true
}

func blockedModelDiscoveryHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "content-length", "cookie", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func parseChannelModelDiscoveryResponse(body []byte, plan channelModelDiscoveryPlan) ([]string, error) {
	ids := make([]string, 0)
	if plan.geminiResponse {
		var payload struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := jsonutil.Unmarshal(body, &payload); err != nil {
			return nil, errors.New("decode Gemini models response")
		}
		for _, item := range payload.Models {
			ids = append(ids, strings.TrimPrefix(strings.TrimSpace(item.Name), "models/"))
		}
	} else if plan.strictOpenAI {
		var payload struct {
			Data *[]struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := jsonutil.Unmarshal(body, &payload); err != nil {
			return nil, errors.New("decode OpenAI models response")
		}
		if payload.Data == nil {
			return nil, errors.New("OpenAI models response requires data")
		}
		for _, item := range *payload.Data {
			ids = append(ids, item.ID)
		}
	} else {
		var payload fetchUpstreamModelsResponse
		if err := jsonutil.Unmarshal(body, &payload); err != nil {
			return nil, errors.New("decode upstream models response")
		}
		for _, item := range payload.Data {
			ids = append(ids, item.ID)
		}
	}
	normalized, err := normalizeUpstreamModelNames(ids)
	if err != nil {
		return nil, fmt.Errorf("upstream model list is invalid: %w", err)
	}
	if plan.strictOpenAI && len(normalized) == 0 {
		return nil, errors.New("OpenAI models response contains no valid model IDs")
	}
	return normalized, nil
}
