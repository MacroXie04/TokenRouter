package replicate

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	maxMultipartParts     = 64
	maxEditImageBytes     = 8 << 20
	maxUploadResponseSize = 1 << 20
	auxiliaryTimeout      = 30 * time.Second
)

var replicateAuxiliaryHTTPClient = newAuxiliaryHTTPClient()

func newAuxiliaryHTTPClient() *http.Client {
	return &http.Client{
		Timeout: auxiliaryTimeout,
		Transport: &http.Transport{
			DialContext:            httpx.SafeDialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: env.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false)},
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  20 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			ForceAttemptHTTP2:      true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type multipartImage struct {
	data        []byte
	contentType string
}

func uploadMultipartImage(meta *relaycommon.Meta) (string, error) {
	image, err := parseMultipartImage(meta.RawBody, meta.RequestContentType)
	if err != nil {
		return "", err
	}
	base, err := validatedBaseURL(meta.BaseURL)
	if err != nil {
		return "", err
	}
	credential, err := validatedCredential(meta.APIKey)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="content"; filename="image`+extensionForContentType(image.contentType)+`"`)
	header.Set("Content-Type", image.contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", errors.New("create Replicate upload envelope")
	}
	if _, err := part.Write(image.data); err != nil {
		return "", errors.New("write Replicate upload envelope")
	}
	if err := writer.Close(); err != nil {
		return "", errors.New("close Replicate upload envelope")
	}
	if body.Len() > maxEditImageBytes+(64<<10) {
		return "", errors.New("Replicate upload envelope is too large")
	}

	ctx := meta.Context
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, relaycommon.JoinURL(base, "/v1/files"), bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", errors.New("create Replicate upload request")
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	response, err := replicateAuxiliaryHTTPClient.Do(request)
	if err != nil {
		return "", relaycommon.SanitizeTransportError(err)
	}
	if response == nil || response.Body == nil {
		return "", errors.New("Replicate upload returned an empty response")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", relaycommon.HandleErrorResponse(response)
	}
	responseBody, err := relaycommon.ReadUpstreamBody(response.Body, maxUploadResponseSize)
	if err != nil {
		return "", fmt.Errorf("read Replicate upload response: %w", err)
	}
	var envelope struct {
		URLs struct {
			Get string `json:"get"`
		} `json:"urls"`
	}
	if err := strictProviderJSON(responseBody, &envelope); err != nil {
		return "", errors.New("Replicate upload returned invalid JSON")
	}
	if err := validateRemoteImageURL(envelope.URLs.Get); err != nil {
		return "", errors.New("Replicate upload returned an invalid image URL")
	}
	return envelope.URLs.Get, nil
}

func parseMultipartImage(raw []byte, contentType string) (multipartImage, error) {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || strings.TrimSpace(parameters["boundary"]) == "" {
		return multipartImage{}, errors.New("Replicate image edit has invalid multipart content type")
	}
	reader := multipart.NewReader(bytes.NewReader(raw), parameters["boundary"])
	var selected multipartImage
	parts := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return multipartImage{}, errors.New("Replicate image edit has an invalid multipart body")
		}
		parts++
		if parts > maxMultipartParts {
			return multipartImage{}, errors.New("Replicate image edit has too many multipart parts")
		}
		field := part.FormName()
		if field != "image" && field != "image[]" && field != "image_prompt" {
			continue
		}
		if len(selected.data) != 0 {
			return multipartImage{}, errors.New("Replicate image edit accepts exactly one image")
		}
		data, err := httpx.ReadAllLimited(part, maxEditImageBytes)
		if err != nil {
			return multipartImage{}, errors.New("Replicate edit image exceeds the size limit")
		}
		if len(data) == 0 {
			return multipartImage{}, errors.New("Replicate edit image is empty")
		}
		declaredType := strings.ToLower(strings.TrimSpace(strings.Split(part.Header.Get("Content-Type"), ";")[0]))
		detectedType := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
		if !supportedImageContentType(detectedType) {
			return multipartImage{}, errors.New("Replicate edit image type is unsupported")
		}
		if declaredType != "" && declaredType != "application/octet-stream" && declaredType != detectedType {
			return multipartImage{}, errors.New("Replicate edit image content type does not match its bytes")
		}
		selected = multipartImage{data: data, contentType: detectedType}
	}
	if len(selected.data) == 0 {
		return multipartImage{}, errors.New("Replicate image edit requires an image file")
	}
	return selected, nil
}

func supportedImageContentType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func extensionForContentType(value string) string {
	switch value {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}
