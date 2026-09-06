// Package openai implements the OpenAI-compatible provider adapter. It serves
// as the shared adapter for all OpenAI-compatible channels (custom endpoints,
// Azure, DeepSeek, OpenRouter, etc.).
package openai

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

// Adaptor is the OpenAI-compatible adapter.
type Adaptor struct {
	ChannelType constant.ChannelType
	Format      constant.RelayFormat
	Mode        constant.RelayMode
}

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	if meta.Channel != nil {
		a.ChannelType = constant.ChannelType(meta.Channel.Type)
	}
	a.Format = meta.Format
	a.Mode = meta.Mode
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	if meta == nil {
		return "", fmt.Errorf("OpenAI-compatible relay metadata is nil")
	}
	channelType := a.providerType(meta)
	if err := validateProviderMode(channelType, a.Mode); err != nil {
		return "", err
	}
	base := meta.BaseURL
	if channelType == constant.ChannelTypeCustom {
		base = strings.TrimSpace(base)
		if base == "" {
			return "", fmt.Errorf("custom channel request URL is empty")
		}
		return strings.ReplaceAll(base, "{model}", meta.ModelName), nil
	}
	if base == "" {
		base = providerDefaultBaseURL(channelType)
		if base == "" {
			return "", fmt.Errorf("channel type %d requires an explicit upstream base URL", channelType)
		}
	}
	if channelType == constant.ChannelTypeAzure {
		return azureRequestURL(base, meta, a.Mode)
	}
	if channelType == constant.ChannelTypeDeepSeek {
		switch a.Mode {
		case constant.RelayModeCompletions:
			if strings.HasSuffix(strings.TrimRight(base, "/"), "/beta") {
				return joinProviderURL(base, "/completions"), nil
			}
			return joinProviderURL(base, "/beta/completions"), nil
		case constant.RelayModeResponses:
			return joinProviderURL(base, "/responses"), nil
		}
	}
	if channelType == constant.ChannelTypePerplexity && a.Mode == constant.RelayModeChatCompletions {
		return joinProviderURL(base, "/chat/completions"), nil
	}
	switch a.Mode {
	case constant.RelayModeChatCompletions:
		return joinProviderURL(base, "/v1/chat/completions"), nil
	case constant.RelayModeCompletions:
		return joinProviderURL(base, "/v1/completions"), nil
	case constant.RelayModeEmbeddings:
		return joinProviderURL(base, "/v1/embeddings"), nil
	case constant.RelayModeModerations:
		return joinProviderURL(base, "/v1/moderations"), nil
	case constant.RelayModeImagesGenerations:
		return joinProviderURL(base, "/v1/images/generations"), nil
	case constant.RelayModeImagesEdits:
		return joinProviderURL(base, "/v1/images/edits"), nil
	case constant.RelayModeEdits:
		return joinProviderURL(base, "/v1/edits"), nil
	case constant.RelayModeAudioSpeech:
		return joinProviderURL(base, "/v1/audio/speech"), nil
	case constant.RelayModeAudioTranscription:
		return joinProviderURL(base, "/v1/audio/transcriptions"), nil
	case constant.RelayModeAudioTranslation:
		return joinProviderURL(base, "/v1/audio/translations"), nil
	case constant.RelayModeResponses:
		return joinProviderURL(base, "/v1/responses"), nil
	case constant.RelayModeResponsesCompact:
		return joinProviderURL(base, "/v1/responses/compact"), nil
	case constant.RelayModeAlphaSearch:
		return joinProviderURL(base, "/v1/alpha/search"), nil
	case constant.RelayModeRerank:
		return joinProviderURL(base, "/v1/rerank"), nil
	case constant.RelayModeRealtime:
		if strings.TrimSpace(meta.ModelName) == "" {
			return "", fmt.Errorf("OpenAI-compatible realtime model is empty")
		}
		requestURL, err := url.Parse(joinProviderURL(base, "/v1/realtime"))
		if err != nil {
			return "", fmt.Errorf("parse OpenAI-compatible realtime URL: %w", err)
		}
		query := requestURL.Query()
		query.Set("model", meta.ModelName)
		requestURL.RawQuery = query.Encode()
		return requestURL.String(), nil
	default:
		return "", fmt.Errorf("unsupported relay mode %d for openai adapter", a.Mode)
	}
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	if req == nil || meta == nil {
		return fmt.Errorf("OpenAI-compatible request metadata is nil")
	}
	contentType := "application/json"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(meta.RequestContentType)), "multipart/form-data") {
		contentType = meta.RequestContentType
	}
	req.Header.Set("Content-Type", contentType)
	if a.providerType(meta) == constant.ChannelTypeAzure {
		if strings.TrimSpace(meta.APIKey) == "" {
			return fmt.Errorf("Azure OpenAI API key is empty")
		}
		req.Header.Set("api-key", meta.APIKey)
		req.Header.Del("Authorization")
	} else if meta.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+meta.APIKey)
	}
	channelType := a.providerType(meta)
	if channelType == constant.ChannelTypeOpenAI && meta.Channel != nil && meta.Channel.OpenAIOrganization != "" {
		req.Header.Set("OpenAI-Organization", meta.Channel.OpenAIOrganization)
	}
	if channelType == constant.ChannelTypeOpenRouter {
		if req.Header.Get("HTTP-Referer") == "" {
			req.Header.Set("HTTP-Referer", openRouterReferer)
		}
		if req.Header.Get("X-OpenRouter-Title") == "" {
			req.Header.Set("X-OpenRouter-Title", openRouterTitle)
		}
	}
	return nil
}

func azureRequestURL(base string, meta *relaycommon.Meta, mode constant.RelayMode) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", fmt.Errorf("Azure OpenAI base URL is empty")
	}
	version := azureAPIVersion(meta)
	if !validAzureAPIVersion(version) {
		return "", fmt.Errorf("Azure OpenAI API version is invalid")
	}
	if mode == constant.RelayModeRealtime {
		if meta == nil || strings.TrimSpace(meta.ModelName) == "" {
			return "", fmt.Errorf("Azure OpenAI deployment name is empty")
		}
		query := url.Values{
			"api-version": []string{version},
			"deployment":  []string{azureDeploymentName(meta)},
		}.Encode()
		return base + "/openai/realtime?" + query, nil
	}
	if mode == constant.RelayModeResponses || mode == constant.RelayModeResponsesCompact {
		responsesVersion := "preview"
		path := "/openai/v1/responses"
		if strings.Contains(strings.ToLower(base), "cognitiveservices.azure.com") {
			path = "/openai/responses"
			responsesVersion = version
		}
		configuredVersion, err := azureResponsesAPIVersion(meta)
		if err != nil {
			return "", err
		}
		if configuredVersion != "" {
			responsesVersion = configuredVersion
		}
		if !validAzureAPIVersion(responsesVersion) {
			return "", fmt.Errorf("Azure OpenAI Responses API version is invalid")
		}
		if mode == constant.RelayModeResponsesCompact {
			path += "/compact"
		}
		query := url.Values{"api-version": []string{responsesVersion}}.Encode()
		return base + path + "?" + query, nil
	}
	task, err := azureTaskPath(mode)
	if err != nil {
		return "", err
	}
	if meta == nil || strings.TrimSpace(meta.ModelName) == "" {
		return "", fmt.Errorf("Azure OpenAI deployment name is empty")
	}
	query := url.Values{"api-version": []string{version}}.Encode()
	return base + "/openai/deployments/" + url.PathEscape(azureDeploymentName(meta)) + "/" + task + "?" + query, nil
}

const azurePreserveDeploymentDotsAfter = int64(1746835200) // 2025-05-10T00:00:00Z

func azureAPIVersion(meta *relaycommon.Meta) string {
	if meta != nil && strings.TrimSpace(meta.APIVersion) != "" {
		return strings.TrimSpace(meta.APIVersion)
	}
	if meta != nil && meta.Channel != nil && strings.TrimSpace(meta.Channel.Other) != "" {
		return strings.TrimSpace(meta.Channel.Other)
	}
	return common.GetEnv("AZURE_DEFAULT_API_VERSION", "2025-04-01-preview")
}

func azureResponsesAPIVersion(meta *relaycommon.Meta) (string, error) {
	if meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.OtherSettings) == "" {
		return "", nil
	}
	var settings struct {
		AzureResponsesVersion string `json:"azure_responses_version"`
	}
	if err := protocolkit.UnmarshalJSON([]byte(meta.Channel.OtherSettings), &settings); err != nil {
		return "", fmt.Errorf("decode Azure OpenAI channel settings: %w", err)
	}
	return strings.TrimSpace(settings.AzureResponsesVersion), nil
}

func azureDeploymentName(meta *relaycommon.Meta) string {
	name := strings.TrimSpace(meta.ModelName)
	if meta.Channel != nil && meta.Channel.CreatedTime < azurePreserveDeploymentDotsAfter {
		name = strings.ReplaceAll(name, ".", "")
	}
	return name
}

func azureTaskPath(mode constant.RelayMode) (string, error) {
	switch mode {
	case constant.RelayModeChatCompletions:
		return "chat/completions", nil
	case constant.RelayModeCompletions:
		return "completions", nil
	case constant.RelayModeEmbeddings:
		return "embeddings", nil
	case constant.RelayModeImagesGenerations:
		return "images/generations", nil
	case constant.RelayModeImagesEdits:
		return "images/edits", nil
	case constant.RelayModeAudioSpeech:
		return "audio/speech", nil
	case constant.RelayModeAudioTranscription:
		return "audio/transcriptions", nil
	case constant.RelayModeAudioTranslation:
		return "audio/translations", nil
	case constant.RelayModeRealtime:
		return "realtime", nil
	default:
		return "", fmt.Errorf("unsupported Azure OpenAI relay mode %d", mode)
	}
}

func validAzureAPIVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	for _, char := range version {
		if (char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z') ||
			(char >= 'a' && char <= 'z') || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if meta == nil {
		return nil, fmt.Errorf("OpenAI-compatible relay metadata is nil")
	}
	if err := validateProviderMode(a.providerType(meta), a.Mode); err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(meta.RequestContentType)), "multipart/form-data") {
		switch a.Mode {
		case constant.RelayModeImagesEdits, constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
		default:
			return nil, fmt.Errorf("multipart/form-data is unsupported for relay mode %d", a.Mode)
		}
		return rewriteMultipartModel(meta.RawBody, meta.RequestContentType, meta.ModelName)
	}
	includeTypedFields := a.Mode == constant.RelayModeChatCompletions || a.Mode == constant.RelayModeCompletions
	body, err := buildRequestMap(meta.Request, includeTypedFields)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(meta.ModelName) == "" {
		return nil, fmt.Errorf("OpenAI-compatible upstream model is empty")
	}
	body["model"] = meta.ModelName
	if err := applyProviderRequest(a.providerType(meta), a.Mode, meta.IsStream, body); err != nil {
		return nil, err
	}
	return protocolkit.MarshalJSON(body)
}

func rewriteMultipartModel(body []byte, contentType, modelName string) ([]byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || strings.TrimSpace(params["boundary"]) == "" {
		return nil, fmt.Errorf("invalid multipart content type")
	}
	boundary := params["boundary"]
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, fmt.Errorf("preserve multipart boundary: %w", err)
	}
	foundModel := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read multipart request: %w", err)
		}
		headers := cloneMIMEHeader(part.Header)
		destination, err := writer.CreatePart(headers)
		if err != nil {
			return nil, fmt.Errorf("write multipart headers: %w", err)
		}
		if part.FormName() == "model" && part.FileName() == "" {
			foundModel = true
			if _, err := io.WriteString(destination, modelName); err != nil {
				return nil, fmt.Errorf("write multipart model: %w", err)
			}
			continue
		}
		if _, err := io.Copy(destination, part); err != nil {
			return nil, fmt.Errorf("copy multipart part: %w", err)
		}
	}
	if !foundModel {
		return nil, fmt.Errorf("multipart request is missing model field")
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish multipart request: %w", err)
	}
	return output.Bytes(), nil
}

func cloneMIMEHeader(header textproto.MIMEHeader) textproto.MIMEHeader {
	cloned := make(textproto.MIMEHeader, len(header))
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

// DoResponse converts the upstream OpenAI response to the client. For streams
// it proxies the SSE and extracts usage from the final chunk (or estimates).
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if c == nil || resp == nil || meta == nil {
		return nil, fmt.Errorf("OpenAI-compatible response metadata is nil")
	}
	if resp.StatusCode >= 400 {
		return nil, mapStatusCode(meta, relaycommon.HandleErrorResponse(resp))
	}
	switch a.Mode {
	case constant.RelayModeAlphaSearch:
		return a.alphaSearchResponse(c, resp)
	case constant.RelayModeResponsesCompact:
		return a.compactResponse(c, resp)
	}
	if meta.IsStream {
		return a.streamResponse(c, resp, meta)
	}
	return a.nonStreamResponse(c, resp, meta)
}

// alphaSearchResponse copies the upstream response verbatim (preserving the
// upstream content type). Upstream alpha search carries no usage; the caller
// settles with the prompt estimate, mirroring the reference's per-call billing.
func (a *Adaptor) alphaSearchResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI alpha-search response: %w", err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Header("Content-Type", ct)
	}
	c.Status(resp.StatusCode)
	if _, err := c.Writer.Write(body); err != nil {
		return nil, fmt.Errorf("write OpenAI alpha-search response: %w", err)
	}
	return nil, nil
}

// compactResponse parses the Responses-compaction envelope: an upstream error
// field is surfaced, and input/output usage becomes the settlement usage.
func (a *Adaptor) compactResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI compact response: %w", err)
	}
	var compact struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Error any `json:"error"`
	}
	if err := protocolkit.UnmarshalJSON(body, &compact); err != nil {
		return nil, err
	}
	if compact.Error != nil {
		var e struct {
			Error protocolkit.OpenAIError `json:"error"`
		}
		_ = protocolkit.UnmarshalJSON(body, &e)
		return nil, relaycommon.UpstreamErrorFromOpenAI(e.Error, resp.StatusCode)
	}
	var usage *protocolkit.Usage
	if compact.Usage != nil {
		usage = &protocolkit.Usage{
			PromptTokens:     compact.Usage.InputTokens,
			CompletionTokens: compact.Usage.OutputTokens,
			TotalTokens:      compact.Usage.TotalTokens,
		}
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return usage, fmt.Errorf("write OpenAI compact response: %w", err)
	}
	return usage, nil
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, a.bufferedResponseLimit())
	if err != nil {
		return nil, fmt.Errorf("read OpenAI response: %w", err)
	}
	if a.Mode == constant.RelayModeAudioSpeech {
		if contentType := resp.Header.Get("Content-Type"); contentType != "" {
			c.Header("Content-Type", contentType)
		} else {
			c.Header("Content-Type", "application/octet-stream")
		}
		c.Status(resp.StatusCode)
		if _, err := c.Writer.Write(body); err != nil {
			return nil, fmt.Errorf("write OpenAI audio response: %w", err)
		}
		return fallbackAudioSpeechUsage(meta.PromptTokens, len(body)), nil
	}

	var envelope map[string]any
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode OpenAI-compatible response: %w", err)
	}
	if envelope == nil {
		return nil, fmt.Errorf("decode OpenAI-compatible response: expected a JSON object")
	}
	if upstreamError, exists := envelope["error"]; exists && upstreamError != nil {
		statusCode := resp.StatusCode
		if statusCode < http.StatusBadRequest {
			statusCode = http.StatusBadRequest
		}
		return nil, mapStatusCode(meta, &relaycommon.UpstreamError{StatusCode: statusCode, Body: string(body)})
	}

	channelType := a.providerType(meta)
	var usage *protocolkit.Usage
	if a.Mode == constant.RelayModeResponses {
		var responses protocolkit.OpenAIResponsesResponse
		if err := protocolkit.UnmarshalJSON(body, &responses); err != nil {
			return nil, fmt.Errorf("decode OpenAI Responses response: %w", err)
		}
		if err := observeResponsesResponse(meta.ToolHooks(), &responses); err != nil {
			return nil, fmt.Errorf("observe OpenAI Responses tool usage: %w", err)
		}
		usage = responsesUsage(responses.Usage)
	} else if a.Mode == constant.RelayModeRerank && channelType == constant.ChannelTypeSiliconFlow {
		body, usage, err = siliconFlowRerankResponse(body)
		if err != nil {
			return nil, err
		}
	} else {
		if a.Mode == constant.RelayModeChatCompletions {
			var chatResponse protocolkit.ChatCompletionsResponse
			if err := protocolkit.UnmarshalJSON(body, &chatResponse); err != nil {
				return nil, fmt.Errorf("decode OpenAI chat response for tool usage: %w", err)
			}
			if err := observeChatResponse(meta.ToolHooks(), &chatResponse); err != nil {
				return nil, fmt.Errorf("observe OpenAI chat tool usage: %w", err)
			}
		}
		usage = relaycommon.ExtractUsageFromBody(body)
	}
	if usage == nil || usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		switch a.Mode {
		case constant.RelayModeImagesGenerations, constant.RelayModeImagesEdits:
			usage = fallbackImageUsage(envelope, meta)
		case constant.RelayModeAudioTranscription, constant.RelayModeAudioTranslation:
			usage = fallbackAudioInputUsage(meta.PromptTokens, len(meta.RawBody))
		}
	}
	if normalizeProviderUsage(channelType, a.Mode, usage) {
		envelope["usage"] = usage
		body, err = protocolkit.MarshalJSON(envelope)
		if err != nil {
			return nil, fmt.Errorf("encode normalized OpenAI-compatible response: %w", err)
		}
	}

	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return usage, fmt.Errorf("write OpenAI response: %w", err)
	}
	return usage, nil
}

func fallbackAudioSpeechUsage(promptTokens, responseBytes int) *protocolkit.Usage {
	promptTokens = max(promptTokens, 1)
	audioTokens := max((responseBytes+999)/1000, 1)
	return &protocolkit.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: audioTokens,
		TotalTokens:      promptTokens + audioTokens,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			TextTokens: promptTokens,
		},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{
			AudioTokens: audioTokens,
		},
	}
}

func fallbackAudioInputUsage(promptTokens, requestBytes int) *protocolkit.Usage {
	audioTokens := max((requestBytes+999)/1000, 1)
	promptTokens = max(promptTokens, 0)
	total := promptTokens + audioTokens
	return &protocolkit.Usage{
		PromptTokens: promptTokens + audioTokens,
		TotalTokens:  total,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			TextTokens:  promptTokens,
			AudioTokens: audioTokens,
		},
	}
}

func fallbackImageUsage(envelope map[string]any, meta *relaycommon.Meta) *protocolkit.Usage {
	promptTokens := max(meta.PromptTokens, 1)
	imageCount := 0
	if data, ok := envelope["data"].([]any); ok {
		imageCount = len(data)
	}
	if imageCount == 0 && meta.Request != nil && meta.Request.N != nil {
		imageCount = *meta.Request.N
	}
	imageCount = max(imageCount, 1)
	return &protocolkit.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: imageCount,
		TotalTokens:      promptTokens + imageCount,
		PromptTokensDetails: &protocolkit.InputTokenDetails{
			TextTokens: promptTokens,
		},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{
			ImageTokens: imageCount,
		},
	}
}

func (a *Adaptor) bufferedResponseLimit() int64 {
	switch a.Mode {
	case constant.RelayModeEmbeddings, constant.RelayModeImagesGenerations, constant.RelayModeImagesEdits:
		// Embedding vectors and image responses containing base64 data can be
		// much larger than ordinary text JSON.
		return relaycommon.MaxUpstreamLargeJSONBodyBytes
	case constant.RelayModeAudioSpeech:
		return relaycommon.MaxUpstreamBinaryBodyBytes
	default:
		return relaycommon.MaxUpstreamJSONBodyBytes
	}
}

func (a *Adaptor) streamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	var contentLength int
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				if hooks := meta.ToolHooks(); a.Mode == constant.RelayModeResponses && hooks != nil && hooks.FinishResponses != nil {
					if err := hooks.FinishResponses("done"); err != nil {
						return usage, fmt.Errorf("finish OpenAI Responses tool usage: %w", err)
					}
				}
				_, _ = c.Writer.WriteString("data: [DONE]\n\n")
				c.Writer.Flush()
				continue
			}
			if a.Mode == constant.RelayModeResponses {
				var event protocolkit.ResponsesStreamResponse
				if err := protocolkit.UnmarshalJSON([]byte(data), &event); err != nil {
					return usage, fmt.Errorf("decode OpenAI Responses stream event: %w", err)
				}
				if event.Error != nil {
					return usage, relaycommon.UpstreamErrorFromOpenAI(*event.Error, http.StatusBadGateway)
				}
				if err := observeResponsesStreamEvent(meta.ToolHooks(), &event); err != nil {
					return usage, fmt.Errorf("observe OpenAI Responses stream tool usage: %w", err)
				}
				if event.Response != nil && event.Response.Usage != nil {
					usage = responsesUsage(event.Response.Usage)
					normalizeProviderUsage(a.providerType(meta), a.Mode, usage)
				}
				contentLength += len(event.Delta)
			} else {
				var chunk protocolkit.ChatCompletionsStreamResponse
				if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err != nil {
					return usage, fmt.Errorf("decode OpenAI chat stream event: %w", err)
				}
				if chunk.Error != nil {
					return usage, relaycommon.UpstreamErrorFromOpenAI(*chunk.Error, http.StatusBadGateway)
				}
				if err := observeChatStreamChunk(meta.ToolHooks(), &chunk); err != nil {
					return usage, fmt.Errorf("observe OpenAI chat stream tool usage: %w", err)
				}
				if chunk.Usage != nil {
					usage = chunk.Usage
					if normalizeProviderUsage(a.providerType(meta), a.Mode, usage) {
						normalized, marshalErr := protocolkit.MarshalJSON(chunk)
						if marshalErr != nil {
							return usage, fmt.Errorf("encode normalized OpenAI stream chunk: %w", marshalErr)
						}
						line = "data: " + string(normalized)
					}
				}
				// Include reasoning and tool-call payloads in fallback billing.
				for _, choice := range chunk.Choices {
					contentLength += streamChoiceTextLength(choice)
				}
			}
			_, _ = c.Writer.WriteString(line + "\n\n")
			c.Writer.Flush()
		} else if line != "" {
			_, _ = c.Writer.WriteString(line + "\n")
			c.Writer.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read OpenAI event stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	if usage == nil && contentLength > 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentLength)
	}
	return usage, nil
}

func observeResponsesResponse(hooks *relaycommon.ToolUsageHooks, response *protocolkit.OpenAIResponsesResponse) error {
	if hooks == nil || hooks.ObserveResponsesOutput == nil || response == nil {
		return nil
	}
	for index := range response.Output {
		item := &response.Output[index]
		outputIndex := index
		if err := hooks.ObserveResponsesOutput(relaycommon.ToolResponsesObservation{
			Type: item.Type, ID: item.Id, CallID: item.CallId, OutputIndex: &outputIndex,
			Name: item.Name, Status: item.Status, Result: item.Result,
		}); err != nil {
			return err
		}
	}
	if response.Status != "" && hooks.FinishResponses != nil {
		return hooks.FinishResponses(response.Status)
	}
	return nil
}

func observeResponsesStreamEvent(hooks *relaycommon.ToolUsageHooks, event *protocolkit.ResponsesStreamResponse) error {
	if hooks == nil || event == nil {
		return nil
	}
	if event.Type == "response.output_item.done" && event.Item != nil && hooks.ObserveResponsesOutput != nil {
		outputIndex := event.OutputIndex
		if err := hooks.ObserveResponsesOutput(relaycommon.ToolResponsesObservation{
			Type: event.Item.Type, ID: event.Item.Id, CallID: event.Item.CallId, OutputIndex: &outputIndex,
			Name: event.Item.Name, Status: event.Item.Status, Result: event.Item.Result,
		}); err != nil {
			return err
		}
	}
	if hooks.FinishResponses == nil {
		return nil
	}
	status := ""
	if event.Response != nil {
		status = event.Response.Status
	}
	if status == "" {
		switch event.Type {
		case "response.completed":
			status = "completed"
		case "response.failed":
			status = "failed"
		case "response.incomplete":
			status = "incomplete"
		case "response.cancelled", "response.canceled":
			status = "cancelled"
		}
	}
	if status != "" {
		return hooks.FinishResponses(status)
	}
	return nil
}

func observeChatResponse(hooks *relaycommon.ToolUsageHooks, response *protocolkit.ChatCompletionsResponse) error {
	if hooks == nil || hooks.ObserveChatToolCall == nil || response == nil {
		return nil
	}
	for _, choice := range response.Choices {
		if choice.Message == nil {
			continue
		}
		for arrayIndex, tool := range choice.Message.ToolCalls {
			if tool.Function == nil {
				continue
			}
			toolIndex := tool.Index
			if err := hooks.ObserveChatToolCall(relaycommon.ToolChatObservation{
				ChoiceIndex: choice.Index, ToolIndex: &toolIndex, ArrayIndex: arrayIndex,
				ID: tool.Id, Name: tool.Function.Name,
			}); err != nil {
				return err
			}
		}
		if function := choice.Message.FunctionCall; function != nil {
			if err := hooks.ObserveChatToolCall(relaycommon.ToolChatObservation{
				ChoiceIndex: choice.Index, ArrayIndex: len(choice.Message.ToolCalls), Name: function.Name,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func observeChatStreamChunk(hooks *relaycommon.ToolUsageHooks, chunk *protocolkit.ChatCompletionsStreamResponse) error {
	if hooks == nil || hooks.ObserveChatToolCall == nil || chunk == nil {
		return nil
	}
	for _, choice := range chunk.Choices {
		for arrayIndex, tool := range choice.Delta.ToolCalls {
			if tool.Function == nil {
				continue
			}
			toolIndex := tool.Index
			if err := hooks.ObserveChatToolCall(relaycommon.ToolChatObservation{
				ChoiceIndex: choice.Index, ToolIndex: &toolIndex, ArrayIndex: arrayIndex,
				ID: tool.Id, Name: tool.Function.Name,
			}); err != nil {
				return err
			}
		}
		if function := choice.Delta.FunctionCall; function != nil {
			if err := hooks.ObserveChatToolCall(relaycommon.ToolChatObservation{
				ChoiceIndex: choice.Index, ArrayIndex: len(choice.Delta.ToolCalls), Name: function.Name,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func mapStatusCode(meta *relaycommon.Meta, err error) error {
	upstream, ok := err.(*relaycommon.UpstreamError)
	if !ok || meta == nil || meta.Channel == nil || strings.TrimSpace(meta.Channel.StatusCodeMapping) == "" {
		return err
	}
	var mapping map[string]any
	if decodeErr := protocolkit.UnmarshalJSON([]byte(meta.Channel.StatusCodeMapping), &mapping); decodeErr != nil {
		return err
	}
	value, exists := mapping[strconv.Itoa(upstream.StatusCode)]
	if !exists {
		return err
	}
	mapped, valid := mappedStatusCode(value)
	if !valid {
		return err
	}
	return &relaycommon.UpstreamError{StatusCode: mapped, Body: upstream.Body, Cause: upstream.Cause}
}

func mappedStatusCode(value any) (int, bool) {
	var status int
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) {
			return 0, false
		}
		status = int(typed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		status = parsed
	case int:
		status = typed
	default:
		return 0, false
	}
	return status, status >= 100 && status <= 599
}

func siliconFlowRerankResponse(body []byte) ([]byte, *protocolkit.Usage, error) {
	var response struct {
		Results any `json:"results"`
		Meta    struct {
			Tokens struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"meta"`
	}
	if err := protocolkit.UnmarshalJSON(body, &response); err != nil {
		return nil, nil, fmt.Errorf("decode SiliconFlow rerank response: %w", err)
	}
	usage := &protocolkit.Usage{
		PromptTokens:     response.Meta.Tokens.InputTokens,
		CompletionTokens: response.Meta.Tokens.OutputTokens,
		TotalTokens:      response.Meta.Tokens.InputTokens + response.Meta.Tokens.OutputTokens,
	}
	out, err := protocolkit.MarshalJSON(map[string]any{"results": response.Results, "usage": usage})
	if err != nil {
		return nil, nil, fmt.Errorf("encode SiliconFlow rerank response: %w", err)
	}
	return out, usage, nil
}
