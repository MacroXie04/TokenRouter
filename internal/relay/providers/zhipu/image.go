package zhipu

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const imageRequestTimeout = 30 * time.Second

type v4ImageResponse struct {
	Created *int64 `json:"created"`
	Data    []struct {
		URL      string `json:"url"`
		ImageURL string `json:"image_url"`
		B64JSON  string `json:"b64_json"`
		B64Image string `json:"b64_image"`
	} `json:"data"`
	Usage     *protocolkit.Usage `json:"usage"`
	RequestID string             `json:"request_id"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type openAIImageResponse struct {
	Created int64             `json:"created"`
	Data    []openAIImageData `json:"data"`
}

type openAIImageData struct {
	B64JSON string `json:"b64_json"`
}

func (a *V4Adaptor) v4ImageResponse(c *gin.Context, response *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Zhipu v4 image response: %w", err)
	}
	var provider v4ImageResponse
	if err := protocolkit.UnmarshalJSON(body, &provider); err != nil {
		return nil, fmt.Errorf("decode Zhipu v4 image response: %w", err)
	}
	if provider.Error != nil && strings.TrimSpace(provider.Error.Message) != "" {
		status := response.StatusCode
		if status < http.StatusBadRequest {
			status = http.StatusBadRequest
		}
		return nil, relaycommon.UpstreamErrorFromOpenAI(protocolkit.OpenAIError{
			Message: provider.Error.Message, Type: "zhipu_image_error", Code: provider.Error.Code,
			Param: provider.RequestID,
		}, mappedStatusCode(meta, status))
	}

	data := make([]openAIImageData, 0, len(provider.Data))
	for _, result := range provider.Data {
		encoded := result.B64JSON
		if encoded == "" {
			encoded = result.B64Image
		}
		if encoded == "" {
			resultURL := result.URL
			if resultURL == "" {
				resultURL = result.ImageURL
			}
			if strings.TrimSpace(resultURL) == "" {
				return nil, errors.New("Zhipu v4 image result contains no image")
			}
			encoded, err = a.fetchImage(c.Request.Context(), resultURL)
			if err != nil {
				return nil, err
			}
		}
		if strings.TrimSpace(encoded) == "" {
			return nil, errors.New("Zhipu v4 image result is empty")
		}
		data = append(data, openAIImageData{B64JSON: encoded})
	}
	if len(data) == 0 {
		return nil, errors.New("Zhipu v4 image response contains no images")
	}

	created := time.Now().Unix()
	if provider.Created != nil && *provider.Created != 0 {
		created = *provider.Created
	}
	output, err := protocolkit.MarshalJSON(openAIImageResponse{Created: created, Data: data})
	if err != nil {
		return nil, fmt.Errorf("encode Zhipu v4 image response: %w", err)
	}
	usage := provider.Usage
	if usage != nil {
		protocolkit.NormalizeOpenAIUsageAliases(usage)
		if err := validateZhipuUsage(usage); err != nil {
			return nil, err
		}
	}
	if usage == nil || usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		promptTokens := meta.PromptTokens
		if promptTokens <= 0 {
			promptTokens = 1
		}
		usage = &protocolkit.Usage{
			PromptTokens: promptTokens, CompletionTokens: len(data), TotalTokens: promptTokens + len(data),
			PromptTokensDetails:     &protocolkit.InputTokenDetails{TextTokens: promptTokens},
			CompletionTokensDetails: &protocolkit.OutputTokenDetails{ImageTokens: len(data)},
		}
	}
	c.Status(response.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(output); err != nil {
		return usage, fmt.Errorf("write Zhipu v4 image response: %w", err)
	}
	return usage, nil
}

func (a *V4Adaptor) fetchImage(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("Zhipu v4 image result URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := a.imageClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch Zhipu v4 image result: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("fetch Zhipu v4 image result: status %d", response.StatusCode)
	}
	body, err := relaycommon.ReadUpstreamBody(response.Body, relaycommon.MaxUpstreamBinaryBodyBytes)
	if err != nil {
		return "", fmt.Errorf("read Zhipu v4 image result: %w", err)
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

func (a *V4Adaptor) imageClient() *http.Client {
	if a.auxClient != nil {
		return a.auxClient
	}
	return newImageClient()
}

func newImageClient() *http.Client {
	return &http.Client{
		Timeout: imageRequestTimeout,
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

func isMultipart(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "multipart/form-data"
}
