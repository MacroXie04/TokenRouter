package dify

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	appcommon "github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type difyChatRequest struct {
	Inputs           map[string]any `json:"inputs"`
	Query            string         `json:"query"`
	ResponseMode     string         `json:"response_mode"`
	User             string         `json:"user"`
	AutoGenerateName bool           `json:"auto_generate_name"`
	Files            []difyFile     `json:"files"`
}

type difyFile struct {
	Type         string `json:"type"`
	TransferMode string `json:"transfer_mode"`
	URL          string `json:"url,omitempty"`
	UploadFileID string `json:"upload_file_id,omitempty"`
}

type imagePlan struct {
	remoteURL string
	mimeType  string
	filename  string
	data      []byte
}

type messagePart struct {
	typ      string
	text     string
	imageURL string
	mimeType string
}

func (a *Adaptor) convertChatRequest(meta *relaycommon.Meta) ([]byte, error) {
	user, err := difyUser(meta)
	if err != nil {
		return nil, err
	}
	query, plans, err := planDifyMessages(meta.Request.Messages)
	if err != nil {
		return nil, err
	}
	files := make([]difyFile, 0, len(plans))
	for _, plan := range plans {
		if plan.remoteURL != "" {
			files = append(files, difyFile{Type: plan.mimeType, TransferMode: "remote_url", URL: plan.remoteURL})
			continue
		}
		file, err := a.uploadInlineImage(meta, user, plan)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	mode := "blocking"
	if meta.IsStream {
		mode = "streaming"
	}
	payload := difyChatRequest{
		Inputs: map[string]any{}, Query: query, ResponseMode: mode, User: user,
		AutoGenerateName: false, Files: files,
	}
	body, err := protocolkit.MarshalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Dify chat request: %w", err)
	}
	if len(body) > maxDifyRequestBodyBytes {
		return nil, fmt.Errorf("Dify request exceeds %d bytes", maxDifyRequestBodyBytes)
	}
	return body, nil
}

func difyUser(meta *relaycommon.Meta) (string, error) {
	user := meta.Request.User
	if user == "" && meta.ClientHeaders != nil {
		candidate := meta.ClientHeaders.Get("X-Request-Id")
		if validDifyUser(candidate) {
			user = candidate
		}
	}
	if user == "" {
		user = appcommon.BestEffortUUID()
	}
	if !validDifyUser(user) {
		return "", errors.New("Dify user identifier is invalid")
	}
	return user, nil
}

func validDifyUser(value string) bool {
	return value != "" && len(value) <= maxDifyUserBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func planDifyMessages(messages []protocolkit.Message) (string, []imagePlan, error) {
	var query strings.Builder
	plans := make([]imagePlan, 0)
	totalInlineBytes := 0
	appendQuery := func(prefix, text string) error {
		additional := len(prefix) + len(text) + 1
		if additional > maxDifyQueryBytes-query.Len() {
			return fmt.Errorf("Dify query exceeds %d bytes", maxDifyQueryBytes)
		}
		query.WriteString(prefix)
		query.WriteString(text)
		query.WriteByte('\n')
		return nil
	}
	for index, message := range messages {
		parts, err := parseMessageParts(message.Content)
		if err != nil {
			return "", nil, fmt.Errorf("Dify message %d: %w", index, err)
		}
		switch message.Role {
		case "system", "assistant":
			var text strings.Builder
			for _, part := range parts {
				if part.typ != protocolkit.ContentTypeText {
					return "", nil, fmt.Errorf("Dify %s messages support text content only", message.Role)
				}
				text.WriteString(part.text)
			}
			prefix := "SYSTEM: \n"
			if message.Role == "assistant" {
				prefix = "ASSISTANT: \n"
			}
			if err := appendQuery(prefix, text.String()); err != nil {
				return "", nil, err
			}
		default:
			for _, part := range parts {
				switch part.typ {
				case protocolkit.ContentTypeText:
					if err := appendQuery("USER: \n", part.text); err != nil {
						return "", nil, err
					}
				case protocolkit.ContentTypeImageURL:
					if len(plans) >= maxDifyFiles {
						return "", nil, fmt.Errorf("Dify files exceed %d entries", maxDifyFiles)
					}
					plan, err := makeImagePlan(part.imageURL, part.mimeType)
					if err != nil {
						return "", nil, err
					}
					if plan.data != nil {
						if len(plan.data) > maxDifyInlineTotalBytes-totalInlineBytes {
							return "", nil, fmt.Errorf("Dify inline files exceed %d decoded bytes", maxDifyInlineTotalBytes)
						}
						totalInlineBytes += len(plan.data)
					}
					plans = append(plans, plan)
				default:
					return "", nil, fmt.Errorf("Dify does not support content type %q", part.typ)
				}
			}
		}
	}
	return query.String(), plans, nil
}

func parseMessageParts(content any) ([]messagePart, error) {
	switch typed := content.(type) {
	case string:
		return []messagePart{{typ: protocolkit.ContentTypeText, text: typed}}, nil
	case nil:
		return nil, nil
	case []any:
		parts := make([]messagePart, 0, len(typed))
		for _, raw := range typed {
			part, err := parseMessagePart(raw)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		return parts, nil
	case []protocolkit.MediaContent:
		parts := make([]messagePart, 0, len(typed))
		for _, raw := range typed {
			part, err := parseTypedMessagePart(raw)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("content must be a string or array")
	}
}

func parseMessagePart(raw any) (messagePart, error) {
	if typed, ok := raw.(protocolkit.MediaContent); ok {
		return parseTypedMessagePart(typed)
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return messagePart{}, errors.New("content part must be an object")
	}
	typ, ok := value["type"].(string)
	if !ok || strings.TrimSpace(typ) == "" {
		return messagePart{}, errors.New("content part type is required")
	}
	switch typ {
	case protocolkit.ContentTypeText:
		text, ok := value["text"].(string)
		if !ok {
			return messagePart{}, errors.New("text content must be a string")
		}
		return messagePart{typ: typ, text: text}, nil
	case protocolkit.ContentTypeImageURL:
		imageURL, mimeType, err := imagePartValues(value["image_url"])
		if err != nil {
			return messagePart{}, err
		}
		return messagePart{typ: typ, imageURL: imageURL, mimeType: mimeType}, nil
	default:
		return messagePart{}, fmt.Errorf("unsupported content type %q", typ)
	}
}

func parseTypedMessagePart(raw protocolkit.MediaContent) (messagePart, error) {
	switch raw.Type {
	case protocolkit.ContentTypeText:
		return messagePart{typ: raw.Type, text: raw.Text}, nil
	case protocolkit.ContentTypeImageURL:
		if raw.ImageURL == nil {
			return messagePart{}, errors.New("image_url content is missing image_url")
		}
		return messagePart{typ: raw.Type, imageURL: raw.ImageURL.Url, mimeType: raw.ImageURL.MimeType}, nil
	default:
		return messagePart{}, fmt.Errorf("unsupported content type %q", raw.Type)
	}
}

func imagePartValues(raw any) (string, string, error) {
	switch value := raw.(type) {
	case string:
		if value == "" {
			return "", "", errors.New("image_url is empty")
		}
		return value, "", nil
	case map[string]any:
		imageURL, ok := value["url"].(string)
		if !ok || imageURL == "" {
			return "", "", errors.New("image_url.url is required")
		}
		mimeType := ""
		if rawMIME, exists := value["mime_type"]; exists {
			var ok bool
			mimeType, ok = rawMIME.(string)
			if !ok {
				return "", "", errors.New("image_url.mime_type must be a string")
			}
		}
		return imageURL, mimeType, nil
	default:
		return "", "", errors.New("image_url must be a string or object")
	}
}

func makeImagePlan(rawURL, declaredMIME string) (imagePlan, error) {
	if len(rawURL) > maxDifyRequestBodyBytes {
		return imagePlan{}, errors.New("Dify image input is too large")
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		if err := validateRemoteImageURL(rawURL); err != nil {
			return imagePlan{}, err
		}
		mimeType, err := normalizeImageMIME(declaredMIME, false)
		if err != nil {
			return imagePlan{}, err
		}
		return imagePlan{remoteURL: rawURL, mimeType: mimeType}, nil
	}

	encoded := rawURL
	mimeType := declaredMIME
	if strings.HasPrefix(rawURL, "data:") {
		comma := strings.IndexByte(rawURL, ',')
		if comma < 0 {
			return imagePlan{}, errors.New("Dify inline image data URL is malformed")
		}
		metadata := rawURL[len("data:"):comma]
		fields := strings.Split(metadata, ";")
		if len(fields) == 0 || !strings.EqualFold(fields[len(fields)-1], "base64") {
			return imagePlan{}, errors.New("Dify inline image must use base64 encoding")
		}
		if mimeType == "" && len(fields) > 1 {
			mimeType = fields[0]
		}
		encoded = rawURL[comma+1:]
	}
	normalizedMIME, err := normalizeImageMIME(mimeType, true)
	if err != nil {
		return imagePlan{}, err
	}
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maxDifyInlineFileBytes)+2 {
		return imagePlan{}, fmt.Errorf("Dify inline image exceeds %d decoded bytes", maxDifyInlineFileBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return imagePlan{}, errors.New("Dify inline image is not valid base64")
	}
	if len(decoded) == 0 || len(decoded) > maxDifyInlineFileBytes {
		return imagePlan{}, fmt.Errorf("Dify inline image exceeds %d decoded bytes", maxDifyInlineFileBytes)
	}
	subtype := strings.TrimPrefix(normalizedMIME, "image/")
	filename := "image." + safeImageExtension(subtype)
	return imagePlan{mimeType: normalizedMIME, filename: filename, data: decoded}, nil
}

func normalizeImageMIME(value string, useDefault bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if useDefault {
			return difyDefaultInlineImageMIME, nil
		}
		return "", nil
	}
	if len(value) > maxDifyMimeTypeBytes {
		return "", errors.New("Dify image MIME type is too long")
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "image/") || len(mediaType) <= len("image/") {
		return "", errors.New("Dify image MIME type must be image/*")
	}
	return mediaType, nil
}

func safeImageExtension(subtype string) string {
	var builder strings.Builder
	for _, char := range subtype {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '.' || char == '+' || char == '-' {
			builder.WriteRune(char)
		}
	}
	if builder.Len() == 0 {
		return "jpg"
	}
	return builder.String()
}

func validateRemoteImageURL(raw string) error {
	if len(raw) > maxDifyRemoteURLBytes {
		return errors.New("Dify remote image URL is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return errors.New("Dify remote image URL is invalid")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("Dify remote image URL must not contain credentials or a fragment")
	}
	return nil
}

func (a *Adaptor) uploadInlineImage(meta *relaycommon.Meta, user string, plan imagePlan) (difyFile, error) {
	credential, err := validatedCredential(meta.APIKey)
	if err != nil {
		return difyFile{}, err
	}
	base := strings.TrimSpace(meta.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return difyFile{}, err
	}
	uploadURL := relaycommon.JoinURL(base, "/v1/files/upload")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("user", user); err != nil {
		return difyFile{}, errors.New("encode Dify upload user")
	}
	part, err := writer.CreateFormFile("file", plan.filename)
	if err != nil {
		return difyFile{}, errors.New("encode Dify upload file")
	}
	if _, err := part.Write(plan.data); err != nil {
		return difyFile{}, errors.New("encode Dify upload content")
	}
	if err := writer.Close(); err != nil {
		return difyFile{}, errors.New("finish Dify upload body")
	}
	if body.Len() > maxDifyInlineFileBytes+(64<<10) {
		return difyFile{}, errors.New("Dify multipart upload body is too large")
	}

	ctx := meta.Context
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(body.Bytes()))
	if err != nil {
		return difyFile{}, errors.New("create Dify upload request")
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+credential)
	response, err := a.uploadHTTPClient().Do(request)
	if err != nil {
		return difyFile{}, fmt.Errorf("upload Dify inline image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return difyFile{}, difyHTTPError(response, meta)
	}
	responseBody, err := relaycommon.ReadUpstreamBody(response.Body, maxDifyUploadResponseBytes)
	if err != nil {
		return difyFile{}, fmt.Errorf("read Dify upload response: %w", err)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := protocolkit.UnmarshalJSON(responseBody, &result); err != nil {
		return difyFile{}, errors.New("decode Dify upload response")
	}
	if result.ID == "" || len(result.ID) > maxDifyUploadIDBytes || strings.ContainsAny(result.ID, "\r\n\x00") {
		return difyFile{}, errors.New("Dify upload response has an invalid file id")
	}
	return difyFile{Type: "image", TransferMode: "local_file", UploadFileID: result.ID}, nil
}

func (a *Adaptor) uploadHTTPClient() *http.Client {
	if a.uploadClient != nil {
		return a.uploadClient
	}
	return newUploadClient()
}
