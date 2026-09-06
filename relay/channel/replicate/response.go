package replicate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

const (
	maxOutputURLBytes       = 8 << 10
	maxDownloadedImageBytes = 32 << 20
	maxDownloadedTotalBytes = 64 << 20
	maxPredictionErrorBytes = 8 << 10
)

type predictionResponse struct {
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
}

type imageData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type imageResponse struct {
	Created int64       `json:"created"`
	Data    []imageData `json:"data"`
}

func convertPredictionResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return acceptedPredictionUsage(meta), fmt.Errorf("read Replicate prediction response: %w", err)
	}
	var prediction predictionResponse
	if err := strictProviderJSON(body, &prediction); err != nil {
		return acceptedPredictionUsage(meta), errors.New("Replicate returned an invalid prediction response")
	}
	trimmedError := strings.TrimSpace(string(prediction.Error))
	if trimmedError != "" && trimmedError != "null" {
		return nil, predictionError(prediction.Error)
	}
	if !strings.EqualFold(strings.TrimSpace(prediction.Status), "succeeded") {
		return nil, relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
			Message: "Replicate prediction did not succeed", Type: "provider_error", Code: "prediction_not_succeeded",
		}, http.StatusBadGateway)
	}
	urls, err := decodeOutputURLs(prediction.Output)
	if err != nil {
		return acceptedPredictionUsage(meta), err
	}
	responseFormat, _ := stringField(meta.Request.Extra, "response_format")
	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	if responseFormat == "" {
		responseFormat = "url"
	}
	if responseFormat != "url" && responseFormat != "b64_json" {
		return acceptedPredictionUsage(meta), errors.New("Replicate response_format must be url or b64_json")
	}

	output := imageResponse{Created: time.Now().Unix(), Data: make([]imageData, 0, len(urls))}
	if responseFormat == "url" {
		for _, rawURL := range urls {
			output.Data = append(output.Data, imageData{URL: rawURL})
		}
	} else {
		total := int64(0)
		for _, rawURL := range urls {
			image, err := downloadImage(meta.Context, rawURL)
			if err != nil {
				return acceptedPredictionUsage(meta), err
			}
			if total > maxDownloadedTotalBytes-int64(len(image)) {
				return acceptedPredictionUsage(meta), errors.New("Replicate downloaded images exceed the total size limit")
			}
			total += int64(len(image))
			output.Data = append(output.Data, imageData{B64JSON: base64.StdEncoding.EncodeToString(image)})
		}
	}
	encoded, err := protocolkit.MarshalJSON(output)
	if err != nil {
		return acceptedPredictionUsage(meta), errors.New("encode Replicate client response")
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	if _, err := c.Writer.Write(encoded); err != nil {
		return acceptedPredictionUsage(meta), fmt.Errorf("write Replicate client response: %w", err)
	}
	// Replicate's synchronous prediction response has no token usage. Returning
	// nil preserves the relay's accepted-work fallback or immutable fixed price.
	return nil, nil
}

// acceptedPredictionUsage marks a 2xx prediction as provider-accepted even
// when its result cannot be safely decoded or downloaded. Returning usage
// beside the error makes the relay settle the durable hold instead of
// refunding work that Replicate may already have charged.
func acceptedPredictionUsage(meta *relaycommon.Meta) *protocolkit.Usage {
	promptTokens := 0
	if meta != nil {
		promptTokens = meta.PromptTokens
		if promptTokens <= 0 {
			promptTokens = relaycommon.EstimatePromptTokens(meta.Request)
		}
	}
	if promptTokens <= 0 {
		promptTokens = 1
	}
	return &protocolkit.Usage{PromptTokens: promptTokens, TotalTokens: promptTokens}
}

func decodeOutputURLs(raw json.RawMessage) ([]string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, errors.New("Replicate prediction output is empty")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if err := validateRemoteImageURL(single); err != nil {
			return nil, errors.New("Replicate prediction returned an invalid image URL")
		}
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil || len(many) == 0 || len(many) > maxImageCount {
		return nil, errors.New("Replicate prediction output must be a bounded URL array")
	}
	seen := make(map[string]struct{}, len(many))
	for _, item := range many {
		if err := validateRemoteImageURL(item); err != nil {
			return nil, errors.New("Replicate prediction returned an invalid image URL")
		}
		if _, duplicate := seen[item]; duplicate {
			return nil, errors.New("Replicate prediction returned a duplicate image URL")
		}
		seen[item] = struct{}{}
	}
	return many, nil
}

func validateRemoteImageURL(raw string) error {
	if raw == "" || len(raw) > maxOutputURLBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("image URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("image URL is invalid")
	}
	return nil
}

func downloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	if err := validateRemoteImageURL(rawURL); err != nil {
		return nil, errors.New("Replicate output image URL is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("create Replicate image download")
	}
	request.Header.Set("Accept", "image/*")
	response, err := replicateAuxiliaryHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("Replicate image download returned an empty response")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, relaycommon.MaxUpstreamErrorBodyBytes))
		return nil, errors.New("Replicate image download failed")
	}
	if response.ContentLength > maxDownloadedImageBytes {
		return nil, errors.New("Replicate output image exceeds the size limit")
	}
	image, err := relaycommon.ReadUpstreamBody(response.Body, maxDownloadedImageBytes)
	if err != nil {
		return nil, fmt.Errorf("read Replicate output image: %w", err)
	}
	if len(image) == 0 {
		return nil, errors.New("Replicate output image is empty")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = strings.ToLower(strings.TrimSpace(http.DetectContentType(image)))
	}
	if !supportedImageContentType(contentType) {
		return nil, errors.New("Replicate output image type is unsupported")
	}
	return image, nil
}

func predictionError(raw json.RawMessage) error {
	message := "Replicate prediction failed"
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && validPredictionErrorText(text) {
		message = strings.TrimSpace(text)
	} else {
		var object struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
			Code    string `json:"code"`
		}
		if err := json.Unmarshal(raw, &object); err == nil {
			for _, candidate := range []string{object.Message, object.Detail, object.Code} {
				if validPredictionErrorText(candidate) {
					message = strings.TrimSpace(candidate)
					break
				}
			}
		}
	}
	return relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
		Message: message, Type: "provider_error", Code: "prediction_failed",
	}, http.StatusBadGateway)
}

func validPredictionErrorText(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maxPredictionErrorBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
