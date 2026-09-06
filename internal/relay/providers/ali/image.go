package ali

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxAliImageCount        = 128
	maxAliEditImages        = 16
	maxAliTaskIDBytes       = 256
	defaultPollAttempts     = 20
	defaultPollInterval     = 10 * time.Second
	auxiliaryRequestTimeout = 30 * time.Second
)

var syncImageModelPatterns = [...]string{
	"z-image",
	"qwen-image",
	"wan2.6",
	"wan2.7",
	"qwen-image-edit",
	"qwen-image-edit-max",
	"qwen-image-edit-max-2026-01-16",
	"qwen-image-edit-plus",
	"qwen-image-edit-plus-2025-12-15",
	"qwen-image-edit-plus-2025-10-30",
}

type aliImageResponse struct {
	Output    aliImageOutput `json:"output"`
	Usage     aliUsage       `json:"usage"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
}

type aliImageOutput struct {
	TaskID     string           `json:"task_id"`
	TaskStatus string           `json:"task_status"`
	Message    string           `json:"message"`
	Code       string           `json:"code"`
	Results    []aliImageResult `json:"results"`
	Choices    []aliImageChoice `json:"choices"`
}

type aliImageResult struct {
	B64Image string `json:"b64_image"`
	URL      string `json:"url"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type aliImageChoice struct {
	Message struct {
		Content []aliImageContent `json:"content"`
	} `json:"message"`
}

type aliImageContent struct {
	Image string `json:"image"`
	Text  string `json:"text"`
}

type openAIImageData struct {
	URL           string `json:"url"`
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
}

func imageRequestIsSynchronous(meta *relaycommon.Meta) bool {
	if meta == nil {
		return false
	}
	modelName := imageRoutingModel(meta)
	if meta.Mode == channelcatalog.RelayModeImagesEdits && isWanModel(modelName) {
		return false
	}
	return isSyncImageModel(modelName)
}

func imageRoutingModel(meta *relaycommon.Meta) string {
	if meta == nil {
		return ""
	}
	if original := strings.TrimSpace(meta.OriginalModelName); original != "" {
		return original
	}
	return strings.TrimSpace(meta.ModelName)
}

func isSyncImageModel(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	for _, pattern := range syncImageModelPatterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func isWanModel(modelName string) bool {
	return strings.Contains(strings.ToLower(modelName), "wan")
}

func isOldWanModel(modelName string) bool {
	normalized := strings.ToLower(modelName)
	return strings.Contains(normalized, "wan") &&
		!strings.Contains(normalized, "wan2.6") && !strings.Contains(normalized, "wan2.7")
}

func convertImageRequest(meta *relaycommon.Meta, synchronous bool) ([]byte, error) {
	if meta == nil || meta.Request == nil {
		return nil, errors.New("Ali image request is empty")
	}
	if meta.IsStream {
		return nil, errors.New("Ali image requests do not support streaming")
	}
	if meta.Mode == channelcatalog.RelayModeImagesEdits && isMultipart(meta.RequestContentType) {
		return convertMultipartImageEdit(meta)
	}
	if meta.Mode == channelcatalog.RelayModeImagesGenerations && isMultipart(meta.RequestContentType) {
		return nil, errors.New("Ali image generation requires application/json")
	}

	extra := meta.Request.Extra
	input, inputPresent, err := copiedField(extra, "input")
	if err != nil {
		return nil, fmt.Errorf("copy Ali image input: %w", err)
	}
	if inputPresent && input == nil {
		return nil, errors.New("Ali image input must not be null")
	}
	parameters, parametersPresent, err := imageParameters(meta)
	if err != nil {
		return nil, err
	}
	prompt, _ := stringField(extra, "prompt")
	if prompt == "" {
		prompt, _ = meta.Request.Prompt.(string)
	}
	if !inputPresent {
		if strings.TrimSpace(prompt) == "" {
			return nil, errors.New("Ali image prompt or provider-native input is required")
		}
		if synchronous {
			input = map[string]any{"messages": []any{map[string]any{
				"role": "user", "content": []any{map[string]any{"text": prompt}},
			}}}
		} else {
			input = map[string]any{"prompt": prompt}
		}
	}
	request := map[string]any{"model": meta.ModelName, "input": input}
	if parametersPresent || len(parameters) > 0 {
		request["parameters"] = parameters
	}
	if responseFormat, ok := stringField(extra, "response_format"); ok && responseFormat != "" {
		request["response_format"] = responseFormat
	}
	return protocolkit.MarshalJSON(request)
}

func imageParameters(meta *relaycommon.Meta) (map[string]any, bool, error) {
	extra := meta.Request.Extra
	if extra != nil {
		if _, present := extra["parameters"]; present {
			copied, _, err := copiedField(extra, "parameters")
			if err != nil {
				return nil, false, fmt.Errorf("copy Ali image parameters: %w", err)
			}
			parameters, ok := copied.(map[string]any)
			if !ok {
				return nil, false, errors.New("Ali image parameters must be a JSON object")
			}
			if rawN, exists := parameters["n"]; exists {
				if _, valid := boundedJSONInteger(rawN, 0, maxAliImageCount); !valid {
					return nil, false, fmt.Errorf("Ali image parameters.n must be an integer between 0 and %d", maxAliImageCount)
				}
			}
			return parameters, true, nil
		}
	}
	parameters := make(map[string]any)
	if size, ok := stringField(extra, "size"); ok && size != "" {
		parameters["size"] = strings.ReplaceAll(size, "x", "*")
	}
	n := 1
	if meta.Request.N != nil {
		n = *meta.Request.N
	}
	if n < 0 || n > maxAliImageCount {
		return nil, false, fmt.Errorf("Ali image n must be between 0 and %d", maxAliImageCount)
	}
	parameters["n"] = n
	if watermark, ok := boolField(extra, "watermark"); ok {
		parameters["watermark"] = watermark
	}
	return parameters, false, nil
}

func convertMultipartImageEdit(meta *relaycommon.Meta) ([]byte, error) {
	images, err := imageDataURLsFromMultipart(meta.RawBody, meta.RequestContentType)
	if err != nil {
		return nil, err
	}
	prompt, _ := stringField(meta.Request.Extra, "prompt")
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("Ali image edit prompt is required")
	}
	n := 1
	if meta.Request.N != nil {
		n = *meta.Request.N
	}
	if n < 0 || n > maxAliImageCount {
		return nil, fmt.Errorf("Ali image n must be between 0 and %d", maxAliImageCount)
	}
	var input any
	parameters := map[string]any{"n": n}
	if isOldWanModel(imageRoutingModel(meta)) {
		input = map[string]any{
			"prompt": prompt, "images": images,
		}
		if negative, ok := stringField(meta.Request.Extra, "negative_prompt"); ok && negative != "" {
			input.(map[string]any)["negative_prompt"] = negative
		}
	} else {
		content := make([]any, 0, len(images)+1)
		for _, image := range images {
			content = append(content, map[string]any{"image": image})
		}
		content = append(content, map[string]any{"text": prompt})
		input = map[string]any{"messages": []any{map[string]any{"role": "user", "content": content}}}
		if watermark, ok := boolField(meta.Request.Extra, "watermark"); ok {
			parameters["watermark"] = watermark
		}
	}
	request := map[string]any{"model": meta.ModelName, "input": input, "parameters": parameters}
	if responseFormat, ok := stringField(meta.Request.Extra, "response_format"); ok && responseFormat != "" {
		request["response_format"] = responseFormat
	}
	return protocolkit.MarshalJSON(request)
}

func imageDataURLsFromMultipart(body []byte, contentType string) ([]string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(params["boundary"]) == "" {
		return nil, errors.New("Ali image edit has invalid multipart content type")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	standard, array, indexed := make([]string, 0), make([]string, 0), make([]string, 0)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Ali image edit multipart: %w", err)
		}
		if part.FileName() == "" {
			continue
		}
		name := part.FormName()
		if name != "image" && name != "image[]" && !strings.HasPrefix(name, "image[") {
			continue
		}
		data, err := relaycommon.ReadUpstreamBody(part, relaycommon.MaxUpstreamJSONBodyBytes)
		if err != nil {
			return nil, fmt.Errorf("read Ali image edit file: %w", err)
		}
		if len(data) == 0 {
			return nil, errors.New("Ali image edit file is empty")
		}
		dataURL := "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
		switch name {
		case "image":
			standard = append(standard, dataURL)
		case "image[]":
			array = append(array, dataURL)
		default:
			indexed = append(indexed, dataURL)
		}
	}
	images := standard
	if len(images) == 0 {
		images = array
	}
	if len(images) == 0 {
		images = indexed
	}
	if len(images) == 0 {
		return nil, errors.New("Ali image edit requires an image file")
	}
	if len(images) > maxAliEditImages {
		return nil, fmt.Errorf("Ali image edit exceeds %d images", maxAliEditImages)
	}
	return images, nil
}

func (a *Adaptor) doImageResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Ali image response: %w", err)
	}
	if providerErr := aliErrorFromBody(meta, body, resp.StatusCode); providerErr != nil {
		return nil, providerErr
	}
	response, err := decodeAliImageResponse(body)
	if err != nil {
		return nil, err
	}
	originBody := body
	if !a.imageSync {
		if strings.TrimSpace(response.Output.TaskID) == "" {
			return nil, errors.New("Ali asynchronous image response is missing task_id")
		}
		response, originBody, err = a.waitForImageTask(c.Request.Context(), meta, response.Output.TaskID)
		if err != nil {
			return nil, err
		}
		if response.Output.TaskStatus != "SUCCEEDED" {
			code := strings.TrimSpace(response.Output.Code)
			if code == "" {
				code = "ali_task_" + strings.ToLower(strings.TrimSpace(response.Output.TaskStatus))
			}
			message := strings.TrimSpace(response.Output.Message)
			if message == "" {
				message = "DashScope image task did not succeed"
			}
			return nil, relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
				Message: message, Type: "ali_error", Code: code,
			}, mappedStatusCode(meta, http.StatusBadGateway))
		}
	}

	responseFormat, _ := stringField(meta.Request.Extra, "response_format")
	data, err := a.convertImageOutput(c.Request.Context(), response.Output, responseFormat)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("Ali image response contains no images")
	}
	out, err := protocolkit.MarshalJSON(struct {
		Data     []openAIImageData `json:"data"`
		Created  int64             `json:"created"`
		Metadata any               `json:"metadata,omitempty"`
	}{Data: data, Created: time.Now().Unix(), Metadata: rawJSON(originBody)})
	if err != nil {
		return nil, fmt.Errorf("encode Ali image response: %w", err)
	}
	usage, err := normalizeImageUsage(response.Usage, len(data), meta.PromptTokens)
	if err != nil {
		return nil, err
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return usage, fmt.Errorf("write Ali image response: %w", err)
	}
	return usage, nil
}

type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	var value any
	if err := protocolkit.UnmarshalJSON(r, &value); err != nil {
		return nil, err
	}
	return protocolkit.MarshalJSON(value)
}

func decodeAliImageResponse(body []byte) (*aliImageResponse, error) {
	var response aliImageResponse
	if err := protocolkit.UnmarshalJSON(body, &response); err != nil {
		return nil, fmt.Errorf("decode Ali image response: %w", err)
	}
	return &response, nil
}

func (a *Adaptor) waitForImageTask(ctx context.Context, meta *relaycommon.Meta, taskID string) (*aliImageResponse, []byte, error) {
	if len(taskID) == 0 || len(taskID) > maxAliTaskIDBytes {
		return nil, nil, errors.New("Ali image task_id is invalid")
	}
	for _, char := range taskID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return nil, nil, errors.New("Ali image task_id contains unsupported characters")
	}
	attempts := defaultPollAttempts
	interval := defaultPollInterval
	client := a.auxiliaryClient()
	for attempt := 0; attempt < attempts; attempt++ {
		response, body, err := pollImageTask(ctx, client, meta, taskID)
		if err == nil {
			switch response.Output.TaskStatus {
			case "SUCCEEDED", "FAILED", "CANCELED", "UNKNOWN":
				return response, body, nil
			}
		} else if !relaycommon.IsRetryableUpstreamError(err) {
			return nil, nil, fmt.Errorf("poll Ali image task: %w", err)
		}
		if attempt == attempts-1 {
			if err != nil {
				return nil, nil, fmt.Errorf("poll Ali image task: %w", err)
			}
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, nil, errors.New("Ali image task polling timed out")
}

func pollImageTask(ctx context.Context, client *http.Client, meta *relaycommon.Meta, taskID string) (*aliImageResponse, []byte, error) {
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	requestURL := relaycommon.JoinURL(base, "/api/v1/tasks/"+url.PathEscape(taskID))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(meta.APIKey))
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	limit := relaycommon.MaxUpstreamLargeJSONBodyBytes
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		limit = relaycommon.MaxUpstreamErrorBodyBytes
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, limit)
	if err != nil {
		return nil, nil, err
	}
	if providerErr := aliErrorFromBody(meta, body, response.StatusCode); providerErr != nil {
		return nil, nil, providerErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, &relaycommon.UpstreamError{StatusCode: mappedStatusCode(meta, response.StatusCode), Body: string(body)}
	}
	decoded, err := decodeAliImageResponse(body)
	return decoded, body, err
}

func (a *Adaptor) convertImageOutput(ctx context.Context, output aliImageOutput, responseFormat string) ([]openAIImageData, error) {
	data := make([]openAIImageData, 0, len(output.Results)+len(output.Choices))
	for _, result := range output.Results {
		if result.Code != "" {
			return nil, fmt.Errorf("Ali image result failed: %s", result.Message)
		}
		item := openAIImageData{URL: result.URL}
		if responseFormat == "b64_json" {
			if result.B64Image != "" {
				item.B64JSON = result.B64Image
			} else {
				encoded, err := a.fetchImageAsBase64(ctx, result.URL)
				if err != nil {
					return nil, err
				}
				item.B64JSON = encoded
			}
		} else {
			item.B64JSON = result.B64Image
		}
		if item.URL == "" && item.B64JSON == "" {
			return nil, errors.New("Ali image result contains neither URL nor base64 data")
		}
		data = append(data, item)
	}
	for _, choice := range output.Choices {
		item := openAIImageData{}
		for _, content := range choice.Message.Content {
			if content.Text != "" {
				item.RevisedPrompt = content.Text
			}
			if content.Image == "" {
				continue
			}
			if strings.HasPrefix(content.Image, "http://") || strings.HasPrefix(content.Image, "https://") {
				item.URL = content.Image
				if responseFormat == "b64_json" {
					encoded, err := a.fetchImageAsBase64(ctx, content.Image)
					if err != nil {
						return nil, err
					}
					item.B64JSON = encoded
				}
			} else {
				item.B64JSON = content.Image
			}
		}
		if item.URL == "" && item.B64JSON == "" {
			return nil, errors.New("Ali image choice contains no image")
		}
		data = append(data, item)
	}
	return data, nil
}

func (a *Adaptor) fetchImageAsBase64(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("Ali image result URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := a.auxiliaryClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch Ali image result: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("fetch Ali image result: status %d", response.StatusCode)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamBinaryBodyBytes)
	if err != nil {
		return "", fmt.Errorf("read Ali image result: %w", err)
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

func normalizeImageUsage(raw aliUsage, imageCount, promptTokens int) (*protocolkit.Usage, error) {
	if raw.InputTokens < 0 || raw.OutputTokens < 0 || raw.TotalTokens < 0 || raw.ImageCount < 0 {
		return nil, errors.New("Ali image response contains negative usage")
	}
	if raw.InputTokens != 0 || raw.OutputTokens != 0 || raw.TotalTokens != 0 {
		usage := &protocolkit.Usage{
			PromptTokens: raw.InputTokens, CompletionTokens: raw.OutputTokens, TotalTokens: raw.TotalTokens,
		}
		return protocolkit.NormalizeOpenAIUsageAliases(usage), nil
	}
	if raw.ImageCount > 0 {
		imageCount = raw.ImageCount
	}
	if imageCount <= 0 {
		imageCount = 1
	}
	if promptTokens <= 0 {
		promptTokens = 1
	}
	return &protocolkit.Usage{
		PromptTokens: promptTokens, CompletionTokens: imageCount, TotalTokens: promptTokens + imageCount,
		PromptTokensDetails:     &protocolkit.InputTokenDetails{TextTokens: promptTokens},
		CompletionTokensDetails: &protocolkit.OutputTokenDetails{ImageTokens: imageCount},
	}, nil
}

func (a *Adaptor) auxiliaryClient() *http.Client {
	if a.auxClient != nil {
		return a.auxClient
	}
	return newAuxiliaryClient()
}

func newAuxiliaryClient() *http.Client {
	return &http.Client{
		Timeout: auxiliaryRequestTimeout,
		Transport: &http.Transport{
			DialContext: httpx.SafeDialContext,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: env.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false),
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func copiedField(source map[string]any, key string) (any, bool, error) {
	if source == nil {
		return nil, false, nil
	}
	value, exists := source[key]
	if !exists {
		return nil, false, nil
	}
	body, err := protocolkit.MarshalJSON(value)
	if err != nil {
		return nil, false, err
	}
	var copied any
	if err := protocolkit.UnmarshalJSON(body, &copied); err != nil {
		return nil, false, err
	}
	return copied, true, nil
}

func stringField(source map[string]any, key string) (string, bool) {
	if source == nil {
		return "", false
	}
	value, exists := source[key]
	if !exists {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func boolField(source map[string]any, key string) (bool, bool) {
	if source == nil {
		return false, false
	}
	value, exists := source[key]
	if !exists {
		return false, false
	}
	boolean, ok := value.(bool)
	return boolean, ok
}

func isMultipart(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "multipart/form-data"
}
