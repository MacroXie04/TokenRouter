// Package baidu implements the legacy Baidu Wenxin/Qianfan chat and embedding
// wire contracts, including OAuth token acquisition and response conversion.
package baidu

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	ChannelName                 = "baidu"
	defaultBaseURL              = "https://aip.baidubce.com"
	maxBaiduRequestBodyBytes    = 16 << 20
	maxBaiduModelBytes          = 1024
	maxBaiduBaseURLBytes        = 8 << 10
	maxBaiduStreamBodyBytes     = 64 << 20
	maxBaiduStreamTextBytes     = 16 << 20
	maxBaiduEmbeddingInputCount = 100_000
)

var supportedModels = [...]string{
	"ERNIE-4.0-8K",
	"ERNIE-3.5-8K",
	"ERNIE-3.5-8K-0205",
	"ERNIE-3.5-8K-1222",
	"ERNIE-Bot-8K",
	"ERNIE-3.5-4K-0205",
	"ERNIE-Speed-8K",
	"ERNIE-Speed-128K",
	"ERNIE-Lite-8K-0922",
	"ERNIE-Lite-8K-0308",
	"ERNIE-Tiny-8K",
	"BLOOMZ-7B",
	"Embedding-V1",
	"bge-large-zh",
	"bge-large-en",
	"tao-8k",
}

// ModelList returns an owned copy of the reference provider catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// Adaptor owns the legacy Wenxin-specific conversion. Access-token retrieval
// is delayed until headers are prepared so it inherits the relay request's
// cancellation and deadline.
type Adaptor struct {
	mode   channelcatalog.RelayMode
	format channelcatalog.RelayFormat
	tokens *accessTokenManager
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = channelcatalog.RelayModeUnknown
	a.format = channelcatalog.RelayFormatUnknown
	if a.tokens == nil {
		a.tokens = defaultAccessTokens
	}
	if meta != nil {
		a.mode = meta.Mode
		a.format = meta.Format
	}
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if err := a.validate(meta); err != nil {
		return "", err
	}
	base, err := validateAndNormalizeBaseURL(meta.BaseURL, defaultBaseURL)
	if err != nil {
		return "", err
	}
	operation, embedding, err := legacyOperation(meta.ModelName)
	if err != nil {
		return "", err
	}
	if a.mode == channelcatalog.RelayModeEmbeddings && !embedding {
		return "", fmt.Errorf("Baidu embedding mode does not support model %q", meta.ModelName)
	}
	if a.mode == channelcatalog.RelayModeChatCompletions && embedding {
		return "", fmt.Errorf("Baidu chat mode does not support embedding model %q", meta.ModelName)
	}
	family := "chat"
	if embedding {
		family = "embeddings"
	}
	return relaycommon.JoinURL(base, "/rpc/2.0/ai_custom/v1/wenxinworkshop/"+family+"/"+operation), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Baidu request metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return err
	}
	base, err := validateAndNormalizeBaseURL(meta.BaseURL, defaultBaseURL)
	if err != nil {
		return err
	}
	token, err := a.tokens.token(req.Context(), base, meta.APIKey)
	if err != nil {
		return err
	}
	query := req.URL.Query()
	query.Set("access_token", token)
	req.URL.RawQuery = query.Encode()
	// The OAuth access token in the query is the complete legacy inference
	// credential. Never repeat client_id|client_secret in Authorization.
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("api-key")
	req.Header.Set("Content-Type", "application/json")
	if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if len(meta.RawBody) > maxBaiduRequestBodyBytes {
		return nil, fmt.Errorf("Baidu request exceeds %d bytes", maxBaiduRequestBodyBytes)
	}
	var payload any
	var err error
	switch a.mode {
	case channelcatalog.RelayModeChatCompletions:
		payload, err = convertLegacyChatRequest(meta)
	case channelcatalog.RelayModeEmbeddings:
		payload, err = convertLegacyEmbeddingRequest(meta)
	default:
		return nil, fmt.Errorf("Baidu channel does not support relay mode %d", a.mode)
	}
	if err != nil {
		return nil, err
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Baidu request: %w", err)
	}
	if len(body) > maxBaiduRequestBodyBytes {
		return nil, fmt.Errorf("Baidu converted request exceeds %d bytes", maxBaiduRequestBodyBytes)
	}
	return body, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, errors.New("Baidu response metadata is nil")
	}
	if err := a.validate(meta); err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, legacyHTTPError(resp, meta)
	}
	if meta.IsStream {
		return legacyStreamResponse(c, resp, meta)
	}
	if a.mode == channelcatalog.RelayModeEmbeddings {
		return legacyEmbeddingResponse(c, resp, meta)
	}
	return legacyChatResponse(c, resp, meta)
}

func (a *Adaptor) validate(meta *relaycommon.Meta) error {
	if meta == nil || meta.Request == nil {
		return errors.New("Baidu relay metadata is nil")
	}
	mode := meta.Mode
	if mode == channelcatalog.RelayModeUnknown {
		mode = a.mode
	}
	format := meta.Format
	if format == channelcatalog.RelayFormatUnknown {
		format = a.format
	}
	switch mode {
	case channelcatalog.RelayModeChatCompletions:
		if format != channelcatalog.RelayFormatOpenAI {
			return fmt.Errorf("Baidu chat supports only OpenAI format, got %q", format)
		}
	case channelcatalog.RelayModeEmbeddings:
		if format != channelcatalog.RelayFormatEmbedding {
			return fmt.Errorf("Baidu embeddings support only embedding format, got %q", format)
		}
		if meta.IsStream {
			return errors.New("Baidu embeddings do not support streaming")
		}
	default:
		return fmt.Errorf("Baidu channel does not support relay mode %d", mode)
	}
	if meta.Request.Stream != meta.IsStream {
		return errors.New("Baidu request stream flag does not match relay metadata")
	}
	return validateModel(meta.ModelName)
}

type legacyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type legacyChatRequest struct {
	Messages        []legacyMessage `json:"messages"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            float64         `json:"top_p,omitempty"`
	PenaltyScore    float64         `json:"penalty_score,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	System          string          `json:"system,omitempty"`
	DisableSearch   bool            `json:"disable_search,omitempty"`
	EnableCitation  bool            `json:"enable_citation,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	UserID          string          `json:"user_id,omitempty"`
}

func convertLegacyChatRequest(meta *relaycommon.Meta) (*legacyChatRequest, error) {
	request := meta.Request
	out := &legacyChatRequest{
		Messages:    make([]legacyMessage, 0, len(request.Messages)),
		Temperature: request.Temperature,
		Stream:      request.Stream,
		UserID:      request.User,
	}
	if request.TopP != nil {
		out.TopP = *request.TopP
	}
	if request.FrequencyPenalty != nil {
		out.PenaltyScore = *request.FrequencyPenalty
	}
	maxTokens := 0
	if request.MaxCompletionTokens != nil && *request.MaxCompletionTokens != 0 {
		maxTokens = *request.MaxCompletionTokens
	} else if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	if maxTokens < 0 {
		return nil, errors.New("Baidu max tokens is outside the supported range")
	}
	if maxTokens != 0 {
		if maxTokens == 1 {
			maxTokens = 2
		}
		out.MaxOutputTokens = &maxTokens
	}
	for _, message := range request.Messages {
		content := relaycommon.MessageToText(message)
		if message.Role == "system" {
			out.System = content
			continue
		}
		out.Messages = append(out.Messages, legacyMessage{Role: message.Role, Content: content})
	}
	return out, nil
}

type legacyEmbeddingRequest struct {
	Input []string `json:"input"`
}

func convertLegacyEmbeddingRequest(meta *relaycommon.Meta) (*legacyEmbeddingRequest, error) {
	input := make([]string, 0)
	if meta.Request.Extra != nil {
		switch value := meta.Request.Extra["input"].(type) {
		case string:
			input = append(input, value)
		case []string:
			input = append(input, value...)
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok {
					input = append(input, text)
				}
			}
		}
	}
	if len(input) == 0 {
		return nil, errors.New("Baidu embedding input is empty")
	}
	if len(input) > maxBaiduEmbeddingInputCount {
		return nil, fmt.Errorf("Baidu embedding input exceeds %d entries", maxBaiduEmbeddingInputCount)
	}
	return &legacyEmbeddingRequest{Input: input}, nil
}

func legacyOperation(model string) (operation string, embedding bool, err error) {
	operations := map[string]struct {
		path      string
		embedding bool
	}{
		"ERNIE-4.0":          {path: "completions_pro"},
		"ERNIE-Bot-4":        {path: "completions_pro"},
		"ERNIE-Bot":          {path: "completions"},
		"ERNIE-Bot-turbo":    {path: "eb-instant"},
		"ERNIE-Speed":        {path: "ernie_speed"},
		"ERNIE-4.0-8K":       {path: "completions_pro"},
		"ERNIE-3.5-8K":       {path: "completions"},
		"ERNIE-3.5-8K-0205":  {path: "ernie-3.5-8k-0205"},
		"ERNIE-3.5-8K-1222":  {path: "ernie-3.5-8k-1222"},
		"ERNIE-Bot-8K":       {path: "ernie_bot_8k"},
		"ERNIE-3.5-4K-0205":  {path: "ernie-3.5-4k-0205"},
		"ERNIE-Speed-8K":     {path: "ernie_speed"},
		"ERNIE-Speed-128K":   {path: "ernie-speed-128k"},
		"ERNIE-Lite-8K-0922": {path: "eb-instant"},
		"ERNIE-Lite-8K-0308": {path: "ernie-lite-8k"},
		"ERNIE-Tiny-8K":      {path: "ernie-tiny-8k"},
		"BLOOMZ-7B":          {path: "bloomz_7b1"},
		"Embedding-V1":       {path: "embedding-v1", embedding: true},
		"bge-large-zh":       {path: "bge_large_zh", embedding: true},
		"bge-large-en":       {path: "bge_large_en", embedding: true},
		"tao-8k":             {path: "tao_8k", embedding: true},
	}
	if known, ok := operations[model]; ok {
		return known.path, known.embedding, nil
	}
	if err := validateModel(model); err != nil {
		return "", false, err
	}
	return strings.ToLower(model), false, nil
}

func validateModel(model string) error {
	if model == "" || strings.TrimSpace(model) != model || len(model) > maxBaiduModelBytes {
		return errors.New("Baidu upstream model is invalid")
	}
	for _, character := range model {
		if unicode.IsControl(character) || character == '/' || character == '\\' || character == '?' || character == '#' {
			return errors.New("Baidu upstream model is invalid")
		}
	}
	return nil
}

func validateAndNormalizeBaseURL(raw, fallback string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = fallback
	}
	if raw == "" || len(raw) > maxBaiduBaseURLBytes {
		return "", errors.New("Baidu base URL length is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse Baidu base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Baidu base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("Baidu base URL is missing a host")
	}
	if parsed.User != nil {
		return "", errors.New("Baidu base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Baidu base URL must not contain a query or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

func mapLegacyStatus(meta *relaycommon.Meta, err error) error {
	var upstream *relaycommon.UpstreamError
	if !errors.As(err, &upstream) || upstream == nil || meta == nil || meta.Channel == nil {
		return err
	}
	raw := strings.TrimSpace(meta.Channel.StatusCodeMapping)
	if raw == "" {
		return err
	}
	var mapping map[string]any
	if protocolkit.UnmarshalJSON([]byte(raw), &mapping) != nil {
		return err
	}
	value, ok := mapping[strconv.Itoa(upstream.StatusCode)]
	if !ok {
		return err
	}
	mapped := 0
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) {
			mapped = int(typed)
		}
	case string:
		mapped, _ = strconv.Atoi(typed)
	}
	if mapped < 100 || mapped > 599 {
		return err
	}
	copy := *upstream
	copy.StatusCode = mapped
	return &copy
}
