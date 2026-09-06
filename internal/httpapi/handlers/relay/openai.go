package relay

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/internal/relay/engine"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

const maxRelayRequestBodyBytes int64 = 16 << 20

// Relay is the main relay handler for OpenAI-compatible paths.
func relayOpenAI(c *gin.Context) {
	ctx, cancel := engine.WithRequestDeadline(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	state := middleware.CaptureRelayRequestState(c)
	defer cancel()
	mode := channelcatalog.PathToRelayMode(c.Request.URL.Path)
	if mode == channelcatalog.RelayModeUnknown {
		c.JSON(http.StatusNotFound, gin.H{"error": protocolkit.OpenAIError{Message: "不支持的接口路径", Type: "invalid_request_error", Code: "not_found"}})
		return
	}

	request, rawBody, err := parseRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}
	if err := validateAndNormalizeRelayRequest(mode, request, c.GetHeader("Content-Type")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}

	info := &engine.RelayInfo{
		Mode:               mode,
		Format:             relaycommon.GetRelayFormat(channelcatalog.ChannelTypeOpenAI, mode),
		Request:            request,
		RawBody:            rawBody,
		RequestContentType: c.GetHeader("Content-Type"),
		APIVersion:         c.Query("api-version"),
		ModelName:          request.Model,
		UserGroup:          state.UserGroup,
		Group:              state.FirstGroup(),
	}

	if err := engine.Execute(c, state, info); err != nil {
		// The lifecycle writes a client-visible error whenever the response is
		// still retractable. Once a stream has started, surface the failure to
		// operators without appending a second protocol envelope to the stream.
		logging.SysError("ordinary relay lifecycle failed: " + err.Error())
		return
	}
}

// parseRequest reads and parses the request body into the OpenAI DTO.
func parseRequest(c *gin.Context) (*protocolkit.GeneralOpenAIRequest, []byte, error) {
	body, err := httpx.ReadAllLimited(c.Request.Body, maxRelayRequestBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			return nil, nil, fmt.Errorf("请求体超过 %d 字节限制: %w", maxRelayRequestBodyBytes, err)
		}
		return nil, nil, errors.New("读取请求体失败")
	}
	mediaType, params, mediaErr := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaErr == nil && mediaType == "multipart/form-data" {
		req, parseErr := parseMultipartRelayRequest(body, params["boundary"])
		if parseErr != nil {
			return nil, nil, parseErr
		}
		return req, body, nil
	}
	req := &protocolkit.GeneralOpenAIRequest{}
	if err := protocolkit.UnmarshalJSON(body, req); err != nil {
		return nil, nil, errors.New("无效的 JSON 请求体")
	}
	// Preserve the raw body for provider-specific passthrough fields.
	var extra map[string]any
	_ = protocolkit.UnmarshalJSON(body, &extra)
	if extra == nil {
		extra = make(map[string]any)
	}
	req.Extra = extra
	if req.Model == "" {
		switch channelcatalog.PathToRelayMode(c.Request.URL.Path) {
		case channelcatalog.RelayModeModerations:
			req.Model = "omni-moderation-latest"
			req.Extra["model"] = req.Model
		case channelcatalog.RelayModeEmbeddings:
			if strings.Contains(c.Request.URL.Path, "/engines/") {
				req.Model = strings.TrimSpace(c.Param("model"))
				if req.Model == "" {
					parts := strings.Split(strings.Trim(c.Request.URL.Path, "/"), "/")
					if len(parts) >= 3 && parts[len(parts)-1] == "embeddings" {
						req.Model = parts[len(parts)-2]
					}
				}
				if req.Model != "" {
					req.Extra["model"] = req.Model
				}
			}
		}
	}
	if req.Model == "" {
		return nil, nil, errors.New("缺少 model 字段")
	}
	return req, body, nil
}

func parseMultipartRelayRequest(body []byte, boundary string) (*protocolkit.GeneralOpenAIRequest, error) {
	if strings.TrimSpace(boundary) == "" {
		return nil, errors.New("multipart 请求缺少 boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	fields := make(map[string]any)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("无效的 multipart 请求体")
		}
		name := part.FormName()
		if name == "" || part.FileName() != "" {
			continue
		}
		value, err := io.ReadAll(part)
		if err != nil {
			return nil, errors.New("读取 multipart 字段失败")
		}
		text := string(value)
		if previous, exists := fields[name]; exists {
			switch values := previous.(type) {
			case []any:
				fields[name] = append(values, text)
			default:
				fields[name] = []any{previous, text}
			}
		} else {
			fields[name] = text
		}
	}
	modelName, _ := fields["model"].(string)
	if strings.TrimSpace(modelName) == "" {
		return nil, errors.New("缺少 model 字段")
	}
	req := &protocolkit.GeneralOpenAIRequest{Model: modelName, Extra: fields}
	if prompt, ok := fields["prompt"].(string); ok {
		req.Prompt = prompt
	}
	if stream, ok := fields["stream"].(string); ok {
		parsed, err := strconv.ParseBool(stream)
		if err != nil {
			return nil, errors.New("stream 必须是布尔值")
		}
		req.Stream = parsed
	}
	if rawN, ok := fields["n"].(string); ok && strings.TrimSpace(rawN) != "" {
		n, err := strconv.Atoi(rawN)
		if err != nil {
			return nil, errors.New("n 必须是整数")
		}
		req.N = &n
	}
	return req, nil
}

const maxImageCount = 128

func validateAndNormalizeRelayRequest(mode channelcatalog.RelayMode, req *protocolkit.GeneralOpenAIRequest, contentType string) error {
	if req == nil {
		return errors.New("请求体不能为空")
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		switch mode {
		case channelcatalog.RelayModeImagesEdits, channelcatalog.RelayModeAudioTranscription, channelcatalog.RelayModeAudioTranslation:
		default:
			return errors.New("此接口不支持 multipart/form-data")
		}
	}
	switch mode {
	case channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits:
		if req.N != nil && (*req.N < 0 || *req.N > maxImageCount) {
			return fmt.Errorf("n 必须是 1 到 %d 之间的整数", maxImageCount)
		}
		if req.N == nil || *req.N == 0 {
			one := 1
			req.N = &one
			if mediaType != "multipart/form-data" {
				req.Extra["n"] = one
			}
		}
	case channelcatalog.RelayModeEmbeddings, channelcatalog.RelayModeModerations:
		if req.Extra["input"] == nil {
			return errors.New("input 不能为空")
		}
	case channelcatalog.RelayModeResponses:
		if req.Extra["input"] == nil {
			return errors.New("input 不能为空")
		}
	case channelcatalog.RelayModeRerank:
		query, _ := req.Extra["query"].(string)
		documents, documentsOK := req.Extra["documents"].([]any)
		if strings.TrimSpace(query) == "" {
			return errors.New("query 不能为空")
		}
		if !documentsOK || len(documents) == 0 {
			return errors.New("documents 不能为空")
		}
	}
	return nil
}

// relayAndSettle runs the full lifecycle for a relay request.
