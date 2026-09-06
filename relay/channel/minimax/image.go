package minimax

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const maxMiniMaxImageCount = 128

type imageRequest struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	AspectRatio     string `json:"aspect_ratio,omitempty"`
	ResponseFormat  string `json:"response_format,omitempty"`
	N               int    `json:"n,omitempty"`
	PromptOptimizer *bool  `json:"prompt_optimizer,omitempty"`
	AIGCWatermark   *bool  `json:"aigc_watermark,omitempty"`
}

type imageResponse struct {
	ID   string `json:"id"`
	Data struct {
		ImageURLs   []string `json:"image_urls"`
		ImageBase64 []string `json:"image_base64"`
	} `json:"data"`
	Metadata map[string]any      `json:"metadata"`
	BaseResp miniMaxBaseResponse `json:"base_resp"`
}

type openAIImageData struct {
	URL           string `json:"url"`
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
}

type openAIImageResponse struct {
	Created  int64             `json:"created"`
	Data     []openAIImageData `json:"data"`
	Metadata map[string]any    `json:"metadata,omitempty"`
}

func (a *Adaptor) convertImageRequest(meta *relaycommon.Meta) ([]byte, error) {
	prompt, ok := meta.Request.Prompt.(string)
	if !ok {
		prompt, ok = stringExtra(meta.Request.Extra, "prompt")
	}
	if !ok || strings.TrimSpace(prompt) == "" {
		return nil, errors.New("MiniMax image prompt is required")
	}
	n := 1
	if meta.Request.N != nil && *meta.Request.N != 0 {
		n = *meta.Request.N
	}
	if n < 1 || n > maxMiniMaxImageCount {
		return nil, fmt.Errorf("MiniMax image n must be between 1 and %d", maxMiniMaxImageCount)
	}

	responseFormat, _ := stringExtra(meta.Request.Extra, "response_format")
	if responseFormat == "" && meta.Request.ResponseFormat != nil {
		responseFormat = meta.Request.ResponseFormat.Type
	}
	responseFormat, err := normalizeImageResponseFormat(responseFormat)
	if err != nil {
		return nil, err
	}

	aspectRatio, explicitAspect := stringExtra(meta.Request.Extra, "aspect_ratio")
	if explicitAspect {
		if !supportedAspectRatio(aspectRatio) {
			return nil, errors.New("MiniMax image aspect_ratio is unsupported")
		}
	} else {
		size, _ := stringExtra(meta.Request.Extra, "size")
		aspectRatio = aspectRatioFromSize(size)
	}
	promptOptimizer, err := optionalBoolExtra(meta.Request.Extra, "prompt_optimizer")
	if err != nil {
		return nil, err
	}
	watermark, err := optionalBoolExtra(meta.Request.Extra, "watermark")
	if err != nil {
		return nil, err
	}

	payload := imageRequest{
		Model: meta.ModelName, Prompt: prompt, AspectRatio: aspectRatio,
		ResponseFormat: responseFormat, N: n, PromptOptimizer: promptOptimizer,
		AIGCWatermark: watermark,
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode MiniMax image request: %w", err)
	}
	return body, nil
}

func (a *Adaptor) imageResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read MiniMax image response: %w", err)
	}
	var upstream imageResponse
	if err := protocolkit.UnmarshalJSON(body, &upstream); err != nil {
		return nil, fmt.Errorf("decode MiniMax image response: %w", err)
	}
	if upstream.BaseResp.StatusCode != 0 {
		return nil, miniMaxBusinessError(meta, http.StatusBadRequest, "minimax_image_error",
			upstream.BaseResp.StatusCode, upstream.BaseResp.StatusMsg)
	}
	imageCount := len(upstream.Data.ImageURLs) + len(upstream.Data.ImageBase64)
	if imageCount == 0 {
		return nil, errors.New("MiniMax image response contains no images")
	}
	if imageCount > maxMiniMaxImageCount {
		return nil, fmt.Errorf("MiniMax image response exceeds %d images", maxMiniMaxImageCount)
	}
	out := openAIImageResponse{
		Created:  time.Now().Unix(),
		Data:     make([]openAIImageData, 0, imageCount),
		Metadata: upstream.Metadata,
	}
	for _, rawURL := range upstream.Data.ImageURLs {
		if err := validateMediaURL(rawURL); err != nil {
			return nil, fmt.Errorf("MiniMax image response URL is invalid: %w", err)
		}
		out.Data = append(out.Data, openAIImageData{URL: rawURL})
	}
	var decodedBytes int64
	for _, encoded := range upstream.Data.ImageBase64 {
		if encoded == "" {
			return nil, errors.New("MiniMax image response contains empty base64 data")
		}
		count, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)))
		if err != nil {
			return nil, errors.New("MiniMax image response contains invalid base64 data")
		}
		decodedBytes += count
		if decodedBytes > relaycommon.MaxUpstreamBinaryBodyBytes {
			return nil, fmt.Errorf("MiniMax decoded image response exceeds %d bytes", relaycommon.MaxUpstreamBinaryBodyBytes)
		}
		out.Data = append(out.Data, openAIImageData{B64JSON: encoded})
	}
	encoded, err := protocolkit.MarshalJSON(out)
	if err != nil {
		return nil, fmt.Errorf("encode MiniMax image response: %w", err)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(encoded); err != nil {
		return nil, fmt.Errorf("write MiniMax image response: %w", err)
	}
	promptTokens := meta.PromptTokens
	if promptTokens < 1 {
		promptTokens = 1
	}
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
	}, nil
}

func normalizeImageResponseFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "url":
		return "url", nil
	case "b64_json", "base64":
		return "base64", nil
	default:
		return "", errors.New("MiniMax image response_format must be url or b64_json")
	}
}

func aspectRatioFromSize(size string) string {
	switch strings.TrimSpace(size) {
	case "1024x1024":
		return "1:1"
	case "1792x1024":
		return "16:9"
	case "1024x1792":
		return "9:16"
	case "1536x1024", "1248x832":
		return "3:2"
	case "1024x1536", "832x1248":
		return "2:3"
	case "1152x864":
		return "4:3"
	case "864x1152":
		return "3:4"
	case "1344x576":
		return "21:9"
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return ""
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return ""
	}
	divisor := greatestCommonDivisor(width, height)
	ratio := fmt.Sprintf("%d:%d", width/divisor, height/divisor)
	if supportedAspectRatio(ratio) {
		return ratio
	}
	return ""
}

func greatestCommonDivisor(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	if left == 0 {
		return 1
	}
	return left
}

func supportedAspectRatio(value string) bool {
	switch value {
	case "1:1", "16:9", "4:3", "3:2", "2:3", "3:4", "9:16", "21:9":
		return true
	default:
		return false
	}
}

func stringExtra(extra map[string]any, key string) (string, bool) {
	if extra == nil {
		return "", false
	}
	value, exists := extra[key]
	if !exists || value == nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func optionalBoolExtra(extra map[string]any, key string) (*bool, error) {
	if extra == nil {
		return nil, nil
	}
	value, exists := extra[key]
	if !exists || value == nil {
		return nil, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("MiniMax %s must be a boolean", key)
	}
	return &boolean, nil
}

func validateMediaURL(raw string) error {
	if raw == "" || len(raw) > maxMiniMaxMediaURLBytes {
		return errors.New("media URL length is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("media URL must use http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("media URL host or credentials are invalid")
	}
	return nil
}
