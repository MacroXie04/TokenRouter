// Package zhipu implements the two persisted Zhipu channel contracts. The
// legacy channel uses BigModel's v3 model-specific API and signed JWTs, while
// Zhipu v4 uses bearer-authenticated OpenAI and Anthropic compatible routes.
package zhipu

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	LegacyChannelName = "zhipu"
	V4ChannelName     = "zhipu_4v"
	defaultBaseURL    = "https://open.bigmodel.cn"
	legacyTokenTTL    = 24 * time.Hour
	maxCredentialSize = 16 << 10
)

var legacyModels = [...]string{
	"chatglm_turbo", "chatglm_pro", "chatglm_std", "chatglm_lite",
}

var v4Models = [...]string{
	"glm-4", "glm-4v", "glm-3-turbo", "glm-4-alltools", "glm-4-plus",
	"glm-4-0520", "glm-4-air", "glm-4-airx", "glm-4-long", "glm-4-flash",
	"glm-4v-plus", "glm-4.6", "glm-4.6v", "glm-4.7", "glm-4.7-flash", "glm-5",
}

type v4SpecialBase struct {
	claude string
	openAI string
}

var v4SpecialBases = map[string]v4SpecialBase{
	"glm-coding-plan": {
		claude: "https://open.bigmodel.cn/api/anthropic",
		openAI: "https://open.bigmodel.cn/api/coding/paas/v4",
	},
	"glm-coding-plan-international": {
		claude: "https://api.z.ai/api/anthropic",
		openAI: "https://api.z.ai/api/coding/paas/v4",
	},
	"kimi-coding-plan": {
		claude: "https://api.kimi.com/coding",
		openAI: "https://api.kimi.com/coding/v1",
	},
	"doubao-coding-plan": {
		claude: "https://ark.cn-beijing.volces.com/api/coding",
		openAI: "https://ark.cn-beijing.volces.com/api/coding/v3",
	},
}

// LegacyModelList returns an owned copy of the legacy v3 model catalog.
func LegacyModelList() []string {
	models := make([]string, len(legacyModels))
	copy(models, legacyModels[:])
	return models
}

// V4ModelList returns an owned copy of the v4 model catalog.
func V4ModelList() []string {
	models := make([]string, len(v4Models))
	copy(models, v4Models[:])
	return models
}

// LegacyAuthorization signs the millisecond-based JWT required by Zhipu's v3
// API. Invalid credentials fail before an upstream request is dispatched.
func LegacyAuthorization(apiKey string) (string, error) {
	if len(apiKey) > maxCredentialSize {
		return "", fmt.Errorf("legacy Zhipu API key exceeds %d bytes", maxCredentialSize)
	}
	parts := strings.Split(strings.TrimSpace(apiKey), ".")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("legacy Zhipu API key must use id.secret format")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"api_key":   parts[0],
		"exp":       now.Add(legacyTokenTTL).UnixMilli(),
		"timestamp": now.UnixMilli(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["sign_type"] = "SIGN"
	signed, err := token.SignedString([]byte(parts[1]))
	if err != nil {
		return "", errors.New("sign legacy Zhipu authorization token")
	}
	return signed, nil
}

// LegacyRequestURL resolves the v3 model-specific invoke route.
func LegacyRequestURL(base, model string, stream bool) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("legacy Zhipu upstream model is empty")
	}
	method := "invoke"
	if stream {
		method = "sse-invoke"
	}
	return relaycommon.JoinURL(base, "/api/paas/v3/model-api/"+url.PathEscape(model)+"/"+method), nil
}

// V4RequestURL resolves standard and coding-plan aliases for the v4 adapter.
func V4RequestURL(base string, mode constant.RelayMode, format constant.RelayFormat) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = defaultBaseURL
	}
	special, hasSpecialBase := v4SpecialBases[base]
	if hasSpecialBase {
		if format == constant.RelayFormatClaude {
			base = special.claude
		} else {
			base = special.openAI
		}
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}

	var path string
	if format == constant.RelayFormatClaude {
		if mode != constant.RelayModeChatCompletions {
			return "", fmt.Errorf("Zhipu v4 Claude format does not support relay mode %d", mode)
		}
		if hasSpecialBase {
			path = "/v1/messages"
		} else {
			path = "/api/anthropic/v1/messages"
		}
		return relaycommon.JoinURL(base, path), nil
	}

	switch mode {
	case constant.RelayModeChatCompletions:
		path = "/api/paas/v4/chat/completions"
	case constant.RelayModeEmbeddings:
		path = "/api/paas/v4/embeddings"
	case constant.RelayModeImagesGenerations:
		path = "/api/paas/v4/images/generations"
	default:
		return "", fmt.Errorf("Zhipu v4 does not support relay mode %d", mode)
	}
	if hasSpecialBase {
		switch mode {
		case constant.RelayModeChatCompletions:
			path = "/chat/completions"
		case constant.RelayModeEmbeddings:
			path = "/embeddings"
		case constant.RelayModeImagesGenerations:
			path = "/images/generations"
		}
	}
	return relaycommon.JoinURL(base, path), nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Zhipu base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Zhipu base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Zhipu base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Zhipu base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Zhipu base URL must not contain a query or fragment")
	}
	return nil
}

func mappedStatusCode(meta *relaycommon.Meta, status int) int {
	if meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.StatusCodeMapping) == "" {
		return status
	}
	var mapping map[string]any
	if protocolkit.UnmarshalJSON([]byte(meta.Channel.StatusCodeMapping), &mapping) != nil {
		return status
	}
	mapped, ok := boundedJSONInteger(mapping[strconv.Itoa(status)], 100, 599)
	if !ok {
		return status
	}
	return mapped
}

func boundedJSONInteger(value any, minimum, maximum int) (int, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) ||
		number < float64(minimum) || number > float64(maximum) {
		return 0, false
	}
	return int(number), true
}

func codeString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}
