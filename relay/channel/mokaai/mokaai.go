// Package mokaai implements MokaAI's embedding-only m3e provider contract.
package mokaai

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName               = "mokaai"
	defaultBaseURL            = "https://api.moka.ai"
	maxBaseURLBytes           = 4 << 10
	maxCredentialBytes        = 16 << 10
	maxModelBytes             = 1024
	maxInputItems             = 2048
	maxInputItemBytes         = 1 << 20
	maxEmbeddingItems         = 100_000
	maxEmbeddingVectorEntries = 8_000_000
	responseModelName         = "baidu-embedding"
)

var supportedModels = [...]string{"m3e-large", "m3e-base", "m3e-small"}

// ModelList returns an owned copy of the reference model catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// Adaptor accepts only the OpenAI embeddings surface. Other modes fail before
// an upstream credential can leave the process.
type Adaptor struct {
	mode constant.RelayMode
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	if meta != nil {
		a.mode = meta.Mode
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	return relaycommon.JoinURL(base, "/embeddings"), nil
}

func (a *Adaptor) SetupRequestHeader(request *http.Request, meta *relaycommon.Meta) error {
	if request == nil {
		return errors.New("MokaAI request is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	credential := strings.TrimSpace(meta.APIKey)
	if credential == "" {
		return errors.New("MokaAI API key is required")
	}
	if len(credential) > maxCredentialBytes || strings.ContainsAny(credential, "\r\n\x00") {
		return errors.New("MokaAI API key is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Del("x-api-key")
	request.Header.Del("x-goog-api-key")
	return nil
}

type embeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	inputs, err := normalizeInput(meta.Request.Extra["input"])
	if err != nil {
		return nil, err
	}
	body, err := protocolkit.MarshalJSON(embeddingRequest{Input: inputs, Model: meta.ModelName})
	if err != nil {
		return nil, fmt.Errorf("encode MokaAI embedding request: %w", err)
	}
	if int64(len(body)) > relaycommon.MaxUpstreamJSONBodyBytes {
		return nil, errors.New("MokaAI embedding request is too large")
	}
	return body, nil
}

type embeddingItem struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingResponse struct {
	Object string            `json:"object"`
	Data   []embeddingItem   `json:"data"`
	Model  string            `json:"model"`
	Usage  protocolkit.Usage `json:"usage"`
	Error  any               `json:"error,omitempty"`
}

func (a *Adaptor) DoResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || response == nil {
		return nil, errors.New("MokaAI response is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, relaycommon.HandleErrorResponse(response)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read MokaAI embedding response: %w", err)
	}
	var provider embeddingResponse
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, errors.New("MokaAI returned an invalid embedding response")
	}
	if provider.Error != nil {
		return nil, errors.New("MokaAI returned an embedding error")
	}
	if len(provider.Data) > maxEmbeddingItems {
		return nil, errors.New("MokaAI returned too many embedding items")
	}
	totalEntries := 0
	for _, item := range provider.Data {
		if item.Index < 0 || len(item.Object) > 128 {
			return nil, errors.New("MokaAI returned invalid embedding metadata")
		}
		if len(item.Embedding) > maxEmbeddingVectorEntries-totalEntries {
			return nil, errors.New("MokaAI embedding vectors exceed the configured limit")
		}
		totalEntries += len(item.Embedding)
		for _, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, errors.New("MokaAI returned an invalid embedding value")
			}
		}
	}
	usage := protocolkit.NormalizeOpenAIUsageAliases(&provider.Usage)
	out := embeddingResponse{
		Object: "list", Data: provider.Data, Model: responseModelName, Usage: *usage,
	}
	encoded, err := protocolkit.MarshalJSON(out)
	if err != nil {
		return nil, fmt.Errorf("encode MokaAI embedding response: %w", err)
	}
	c.Data(response.StatusCode, "application/json", encoded)
	return usage, nil
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("MokaAI relay metadata is nil")
	}
	mode := a.mode
	if meta.Mode != constant.RelayModeUnknown {
		mode = meta.Mode
	}
	if mode != constant.RelayModeEmbeddings {
		return fmt.Errorf("MokaAI channel does not support relay mode %d", mode)
	}
	modelName := strings.TrimSpace(meta.ModelName)
	if modelName == "" || len(modelName) > maxModelBytes || modelName != meta.ModelName ||
		!strings.HasPrefix(modelName, "m3e") || strings.ContainsAny(modelName, "\r\n\x00") {
		return errors.New("MokaAI supports only a valid mapped m3e embedding model")
	}
	return nil
}

func normalizeInput(value any) ([]string, error) {
	var inputs []string
	switch typed := value.(type) {
	case string:
		inputs = []string{typed}
	case []string:
		inputs = append([]string(nil), typed...)
	case []any:
		if len(typed) > maxInputItems {
			return nil, errors.New("MokaAI embedding input has too many items")
		}
		inputs = make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("MokaAI embedding input must contain only strings")
			}
			inputs = append(inputs, text)
		}
	default:
		return nil, errors.New("MokaAI embedding input must be a string or string array")
	}
	if len(inputs) == 0 || len(inputs) > maxInputItems {
		return nil, errors.New("MokaAI embedding input is empty or too large")
	}
	for _, input := range inputs {
		if input == "" || len(input) > maxInputItemBytes || !utf8.ValidString(input) {
			return nil, errors.New("MokaAI embedding input contains an invalid item")
		}
	}
	return inputs, nil
}

func validateBaseURL(raw string) error {
	if raw == "" || len(raw) > maxBaseURLBytes {
		return errors.New("MokaAI base URL is missing or too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return errors.New("MokaAI base URL must be a valid HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("MokaAI base URL must not contain credentials, query, or fragment")
	}
	return nil
}
