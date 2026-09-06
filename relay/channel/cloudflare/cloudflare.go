// Package cloudflare implements the Cloudflare Workers AI provider contract.
package cloudflare

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/relay/channel/openai"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	ChannelName                    = "cloudflare"
	defaultBaseURL                 = "https://api.cloudflare.com"
	maxCloudflareAccountIDBytes    = 128
	maxCloudflareModelNameBytes    = 512
	maxCloudflareJSONRequestBytes  = 16 << 20
	maxCloudflareAudioRequestBytes = 16 << 20
)

var supportedModels = [...]string{
	"@cf/meta/llama-3.1-8b-instruct",
	"@cf/meta/llama-2-7b-chat-fp16",
	"@cf/meta/llama-2-7b-chat-int8",
	"@cf/mistral/mistral-7b-instruct-v0.1",
	"@hf/thebloke/deepseek-coder-6.7b-base-awq",
	"@hf/thebloke/deepseek-coder-6.7b-instruct-awq",
	"@cf/deepseek-ai/deepseek-math-7b-base",
	"@cf/deepseek-ai/deepseek-math-7b-instruct",
	"@cf/thebloke/discolm-german-7b-v1-awq",
	"@cf/tiiuae/falcon-7b-instruct",
	"@cf/google/gemma-2b-it-lora",
	"@hf/google/gemma-7b-it",
	"@cf/google/gemma-7b-it-lora",
	"@hf/nousresearch/hermes-2-pro-mistral-7b",
	"@hf/thebloke/llama-2-13b-chat-awq",
	"@cf/meta-llama/llama-2-7b-chat-hf-lora",
	"@cf/meta/llama-3-8b-instruct",
	"@hf/thebloke/llamaguard-7b-awq",
	"@hf/thebloke/mistral-7b-instruct-v0.1-awq",
	"@hf/mistralai/mistral-7b-instruct-v0.2",
	"@cf/mistral/mistral-7b-instruct-v0.2-lora",
	"@hf/thebloke/neural-chat-7b-v3-1-awq",
	"@cf/openchat/openchat-3.5-0106",
	"@hf/thebloke/openhermes-2.5-mistral-7b-awq",
	"@cf/microsoft/phi-2",
	"@cf/qwen/qwen1.5-0.5b-chat",
	"@cf/qwen/qwen1.5-1.8b-chat",
	"@cf/qwen/qwen1.5-14b-chat-awq",
	"@cf/qwen/qwen1.5-7b-chat-awq",
	"@cf/defog/sqlcoder-7b-2",
	"@hf/nexusflow/starling-lm-7b-beta",
	"@cf/tinyllama/tinyllama-1.1b-chat-v1.0",
	"@hf/thebloke/zephyr-7b-beta-awq",
}

// ModelList returns an independent copy of the reference provider catalog.
func ModelList() []string {
	models := make([]string, len(supportedModels))
	copy(models, supportedModels[:])
	return models
}

// Adaptor implements the account-scoped OpenAI-compatible endpoints and the
// model-scoped Workers AI run endpoint.
type Adaptor struct {
	mode   constant.RelayMode
	format constant.RelayFormat
	openAI openai.Adaptor
}

var _ relaycommon.Adaptor = (*Adaptor)(nil)

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	a.mode = constant.RelayModeUnknown
	a.format = constant.RelayFormatUnknown
	a.openAI = openai.Adaptor{}
	if meta == nil {
		return
	}
	a.mode = meta.Mode
	a.format = meta.Format
	a.openAI.Init(meta)
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", errors.New("Cloudflare relay metadata is nil")
	}
	format := a.relayFormat(meta)
	if err := validateContract(a.mode, format, meta.IsStream); err != nil {
		return "", err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	accountID, err := cloudflareAccountID(meta)
	if err != nil {
		return "", err
	}
	accountBase := relaycommon.JoinURL(base, "/client/v4/accounts/"+url.PathEscape(accountID)+"/ai")
	switch a.mode {
	case constant.RelayModeChatCompletions:
		return relaycommon.JoinURL(accountBase, "/v1/chat/completions"), nil
	case constant.RelayModeEmbeddings:
		return relaycommon.JoinURL(accountBase, "/v1/embeddings"), nil
	case constant.RelayModeResponses:
		return relaycommon.JoinURL(accountBase, "/v1/responses"), nil
	case constant.RelayModeCompletions, constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
		modelPath, err := cloudflareModelPath(meta.ModelName)
		if err != nil {
			return "", err
		}
		return relaycommon.JoinURL(accountBase, "/run/"+modelPath), nil
	default:
		return "", fmt.Errorf("Cloudflare channel does not support relay mode %d", a.mode)
	}
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return errors.New("Cloudflare request metadata is nil")
	}
	key := strings.TrimSpace(meta.APIKey)
	if key == "" {
		return errors.New("Cloudflare API token is required")
	}
	if len(key) > int(relaycommon.MaxUpstreamErrorBodyBytes) || strings.ContainsAny(key, "\r\n") {
		return errors.New("Cloudflare API token is invalid")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Del("x-api-key")
	req.Header.Del("api-key")
	req.Header.Del("x-goog-api-key")
	if isCloudflareAudioMode(a.mode) {
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	if meta.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta == nil || meta.Request == nil {
		return nil, errors.New("Cloudflare request is nil")
	}
	if err := validateContract(a.mode, a.relayFormat(meta), meta.IsStream); err != nil {
		return nil, err
	}
	if _, err := cloudflareAccountID(meta); err != nil {
		return nil, err
	}
	if _, err := cloudflareModelPath(meta.ModelName); err != nil {
		return nil, err
	}
	if meta.Request.Stream != meta.IsStream {
		return nil, errors.New("Cloudflare request stream flag does not match relay metadata")
	}
	for name, value := range map[string]*int{
		"max_tokens": meta.Request.MaxTokens, "max_completion_tokens": meta.Request.MaxCompletionTokens,
	} {
		if value != nil && (*value < 0 || int64(*value) > common.MaxQuota/2) {
			return nil, fmt.Errorf("Cloudflare %s is outside the supported range", name)
		}
	}
	switch a.mode {
	case constant.RelayModeChatCompletions:
		if len(meta.Request.Messages) == 0 {
			return nil, errors.New("Cloudflare chat messages are required")
		}
	case constant.RelayModeEmbeddings, constant.RelayModeResponses:
		if meta.Request.Extra == nil || meta.Request.Extra["input"] == nil {
			return nil, errors.New("Cloudflare input is required")
		}
	}

	if isCloudflareAudioMode(a.mode) {
		return extractCloudflareAudio(meta)
	}
	if a.mode == constant.RelayModeCompletions {
		prompt, ok := meta.Request.Prompt.(string)
		if !ok {
			return nil, errors.New("Cloudflare completions prompt must be a string")
		}
		maxTokens := meta.Request.MaxTokens
		if meta.Request.MaxCompletionTokens != nil && *meta.Request.MaxCompletionTokens != 0 {
			maxTokens = meta.Request.MaxCompletionTokens
		}
		body, err := protocolkit.MarshalJSON(cloudflareCompletionRequest{
			Prompt: prompt, MaxTokens: maxTokens, Stream: meta.Request.Stream, Temperature: meta.Request.Temperature,
		})
		if err != nil {
			return nil, fmt.Errorf("encode Cloudflare completion request: %w", err)
		}
		return limitCloudflareJSONRequest(body)
	}

	body, err := a.openAI.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	return limitCloudflareJSONRequest(body)
}

func (a *Adaptor) relayFormat(meta *relaycommon.Meta) constant.RelayFormat {
	if meta != nil && meta.Format != constant.RelayFormatUnknown {
		return meta.Format
	}
	return a.format
}

func validateContract(mode constant.RelayMode, format constant.RelayFormat, stream bool) error {
	var expected constant.RelayFormat
	streamSupported := false
	switch mode {
	case constant.RelayModeChatCompletions, constant.RelayModeCompletions:
		expected = constant.RelayFormatOpenAI
		streamSupported = true
	case constant.RelayModeEmbeddings:
		expected = constant.RelayFormatEmbedding
	case constant.RelayModeResponses:
		expected = constant.RelayFormatOpenAIResponses
		streamSupported = true
	case constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
		expected = constant.RelayFormatOpenAIAudio
	default:
		return fmt.Errorf("Cloudflare channel does not support relay mode %d", mode)
	}
	if format != expected {
		return fmt.Errorf("Cloudflare relay mode %d requires format %q, got %q", mode, expected, format)
	}
	if stream && !streamSupported {
		return fmt.Errorf("Cloudflare relay mode %d does not support streaming", mode)
	}
	return nil
}

func isCloudflareAudioMode(mode constant.RelayMode) bool {
	return mode == constant.RelayModeAudioTranscription || mode == constant.RelayModeAudioTranslation
}

func cloudflareAccountID(meta *relaycommon.Meta) (string, error) {
	if meta == nil || meta.Channel == nil {
		return "", errors.New("Cloudflare channel metadata is missing")
	}
	accountID := strings.TrimSpace(meta.Channel.Other)
	if accountID == "" {
		return "", errors.New("Cloudflare account ID is required in channel other")
	}
	if len(accountID) > maxCloudflareAccountIDBytes {
		return "", errors.New("Cloudflare account ID is too long")
	}
	for _, char := range accountID {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return "", errors.New("Cloudflare account ID contains an invalid character")
	}
	return accountID, nil
}

func cloudflareModelPath(model string) (string, error) {
	if model == "" || strings.TrimSpace(model) != model {
		return "", errors.New("Cloudflare upstream model is invalid")
	}
	if len(model) > maxCloudflareModelNameBytes {
		return "", errors.New("Cloudflare upstream model is too long")
	}
	segments := strings.Split(model, "/")
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("Cloudflare upstream model contains an invalid path segment")
		}
		for _, char := range segment {
			if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
				char == '-' || char == '_' || char == '.' || char == '~' || char == '@' || char == ':' {
				continue
			}
			return "", errors.New("Cloudflare upstream model contains an invalid character")
		}
		segments[index] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/"), nil
}

func validateBaseURL(raw string) error {
	if len(raw) > 4096 {
		return errors.New("Cloudflare base URL is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse Cloudflare base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("Cloudflare base URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("Cloudflare base URL is missing a host")
	}
	if parsed.User != nil {
		return errors.New("Cloudflare base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("Cloudflare base URL must not contain a query, fragment, or opaque path")
	}
	return nil
}

type cloudflareCompletionRequest struct {
	Prompt      string   `json:"prompt"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Stream      bool     `json:"stream,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

func limitCloudflareJSONRequest(body []byte) ([]byte, error) {
	if len(body) > maxCloudflareJSONRequestBytes {
		return nil, fmt.Errorf("Cloudflare request exceeds %d bytes", maxCloudflareJSONRequestBytes)
	}
	return body, nil
}

func extractCloudflareAudio(meta *relaycommon.Meta) ([]byte, error) {
	if len(meta.RawBody) > maxCloudflareAudioRequestBytes {
		return nil, fmt.Errorf("Cloudflare audio request exceeds %d bytes", maxCloudflareAudioRequestBytes)
	}
	mediaType, parameters, err := mime.ParseMediaType(meta.RequestContentType)
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(parameters["boundary"]) == "" {
		return nil, errors.New("Cloudflare audio request must be multipart/form-data with a boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(meta.RawBody), parameters["boundary"])
	var audio []byte
	found := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid Cloudflare audio multipart body")
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		if found {
			return nil, errors.New("Cloudflare audio request contains multiple file fields")
		}
		found = true
		audio, err = io.ReadAll(io.LimitReader(part, maxCloudflareAudioRequestBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read Cloudflare audio file: %w", err)
		}
		if len(audio) > maxCloudflareAudioRequestBytes {
			return nil, fmt.Errorf("Cloudflare audio file exceeds %d bytes", maxCloudflareAudioRequestBytes)
		}
	}
	if !found {
		return nil, errors.New("Cloudflare audio file is required")
	}
	if len(audio) == 0 {
		return nil, errors.New("Cloudflare audio file is empty")
	}
	return audio, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	return a.doResponse(c, resp, meta)
}
