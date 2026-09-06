package engine

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	advancedconfig "github.com/tokenrouter/tokenrouter/internal/relay/customconfig"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"strings"
)

const maxGeminiNativeRequestBodyBytes int64 = 16 << 20

// RelayGeminiNative serves the native Gemini passthrough routes
// (POST /v1/models/*path and POST /v1beta/models/*path). The client speaks the
// Gemini protocol end to end: the body is a GenerateContent request, the model
// name comes from the URL path, and the response is returned in native Gemini
// format. Channels of Gemini type pass the body through verbatim; other
// OpenAI-compatible channels convert the request to OpenAI chat completions
// and convert the response back (mirroring the reference's RelayFormatGemini).
func RelayGeminiNative(c *gin.Context, state relaycommon.RequestState) {
	cancel := applyRelayRequestDeadline(c)
	defer cancel()
	rawBody, err := httpx.ReadAllLimited(c.Request.Body, maxGeminiNativeRequestBodyBytes)
	if err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": protocolkit.OpenAIError{Message: "请求体过大", Type: "invalid_request_error"}})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: "读取请求体失败", Type: "invalid_request_error"}})
		return
	}
	var native protocolkit.GeminiChatRequest
	if err := protocolkit.UnmarshalJSON(rawBody, &native); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: "无效的 JSON 请求体", Type: "invalid_request_error"}})
		return
	}
	modelName := extractModelNameFromGeminiPath(c.Request.URL.Path)
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: "模型名称不能为空", Type: "invalid_request_error"}})
		return
	}
	// The request model field (if present) must agree with the path model;
	// the path is authoritative, mirroring the reference distributor.
	if native.Model != "" && !strings.EqualFold(strings.TrimPrefix(native.Model, "models/"), modelName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": protocolkit.OpenAIError{Message: "路径与请求体中的模型不一致", Type: "invalid_request_error"}})
		return
	}

	converted := protocolkit.GeminiRequestToOpenAIRequest(&native)
	info := &RelayInfo{
		Mode:          channelcatalog.RelayModeGemini,
		Format:        channelcatalog.RelayFormatGemini,
		ModelName:     modelName,
		Request:       converted,
		GeminiRequest: &native,
		RawBody:       rawBody,
		UserGroup:     state.UserGroup,
		Group:         state.FirstGroup(),
		IsStream:      geminiStreamRequested(c, &native),
	}
	info.Request.Stream = info.IsStream
	if err := relayAndSettleWithDispatch(c, state, info, dispatchGeminiNative); err != nil {
		logging.SysError("Gemini relay lifecycle failed: " + err.Error())
	}
}

// geminiStreamRequested reports whether the client asked for streaming: the
// SSE alt query (streamGenerateContent?alt=sse), the standard ?alt=sse query,
// or an Accept: text/event-stream header.
func geminiStreamRequested(c *gin.Context, native *protocolkit.GeminiChatRequest) bool {
	_ = native
	if strings.HasSuffix(c.Request.URL.Path, ":streamGenerateContent") {
		return true
	}
	if c.Query("alt") == "sse" {
		return true
	}
	return strings.Contains(c.GetHeader("Accept"), "text/event-stream")
}

// extractModelNameFromGeminiPath extracts the model name from a Gemini API
// path: /v1beta/models/gemini-2.0-flash:generateContent -> gemini-2.0-flash.
func extractModelNameFromGeminiPath(path string) string {
	idx := strings.Index(path, "/models/")
	if idx == -1 {
		return ""
	}
	rest := path[idx+len("/models/"):]
	if colon := strings.Index(rest, ":"); colon != -1 {
		rest = rest[:colon]
	}
	if slash := strings.Index(rest, "/"); slash != -1 {
		rest = rest[:slash]
	}
	return rest
}

// dispatchGeminiNative performs the upstream call for native Gemini relays.
func dispatchGeminiNative(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	if info == nil || info.Channel == nil || info.GeminiRequest == nil {
		return nil, errors.New("channel not selected")
	}
	originalGeminiRequest := info.GeminiRequest
	originalOpenAIRequest := info.Request
	originalRawBody := info.RawBody
	originalUpstreamModel := info.GeminiUpstreamModel
	patchedBody, upstreamModel, err := relaycommon.PrepareGeminiNativeBody(
		info.RawBody, info.ModelName, relaycommon.GetMappedModel(info.Channel, info.ModelName),
	)
	if err != nil {
		return nil, fmt.Errorf("apply configured Gemini policy: %w", err)
	}
	var prepared protocolkit.GeminiChatRequest
	if err := protocolkit.UnmarshalJSON(patchedBody, &prepared); err != nil {
		return nil, fmt.Errorf("decode configured Gemini request: %w", err)
	}
	info.GeminiRequest = &prepared
	info.Request = protocolkit.GeminiRequestToOpenAIRequest(&prepared)
	info.Request.Stream = info.IsStream
	info.RawBody = patchedBody
	info.GeminiUpstreamModel = upstreamModel
	defer func() {
		info.GeminiRequest = originalGeminiRequest
		info.Request = originalOpenAIRequest
		info.RawBody = originalRawBody
		info.GeminiUpstreamModel = originalUpstreamModel
	}()
	channelType := channelcatalog.ChannelType(info.Channel.Type)
	apiKey := channelssvc.GetChannelKey(info.Channel)
	var usage *protocolkit.Usage
	err = nil
	switch {
	case channelType == channelcatalog.ChannelTypeGemini:
		usage, err = geminiNativePassthrough(c, info)
	case channelType == channelcatalog.ChannelTypeVertexAi:
		usage, err = geminiViaVertex(c, info)
	case channelType == channelcatalog.ChannelTypeAdvancedCustom:
		usage, err = geminiViaAdvancedCustom(c, info)
	case channelType == channelcatalog.ChannelTypeSub2API || channelType == channelcatalog.ChannelTypeNewAPI:
		usage, err = geminiGatewayPassthrough(c, info)
	case channelcatalog.IsOpenAICompatibleChannelType(channelType) && channelType != channelcatalog.ChannelTypePerplexity:
		usage, err = geminiViaOpenAIChannel(c, info)
	default:
		err = fmt.Errorf("channel type %d does not implement native Gemini relay", info.Channel.Type)
	}
	return usage, relaycommon.SanitizeUpstreamError(err, apiKey)
}

func preparedGeminiModel(info *RelayInfo) string {
	if info == nil {
		return ""
	}
	if model := strings.TrimSpace(info.GeminiUpstreamModel); model != "" {
		return model
	}
	return relaycommon.GetMappedModel(info.Channel, info.ModelName)
}

// geminiViaVertex preserves the native Gemini request/response contract while
// delegating project/location URL construction and service-account or API-key
// authentication to the explicit Vertex adapter. The root relay retains the
// shared SSRF-safe client and the ordinary reservation/settlement lifecycle.
func geminiViaVertex(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	if c == nil || info == nil || info.Channel == nil || info.GeminiRequest == nil || info.Request == nil {
		return nil, errors.New("Vertex AI Gemini relay metadata is nil")
	}
	mappedModel := preparedGeminiModel(info)
	requestCopy := *info.Request
	meta := &relaycommon.Meta{
		Context:           c.Request.Context(),
		Channel:           info.Channel,
		Mode:              channelcatalog.RelayModeGemini,
		Format:            channelcatalog.RelayFormatGemini,
		RequestPath:       c.Request.URL.Path,
		OriginalModelName: info.ModelName,
		ModelName:         mappedModel,
		BaseURL:           info.Channel.BaseURL,
		APIKey:            channelssvc.GetChannelKey(info.Channel),
		ClientHeaders:     c.Request.Header.Clone(),
		Request:           &requestCopy,
		RawBody:           info.RawBody,
		IsStream:          info.IsStream,
		PromptTokens:      info.PromptTokens,
		ToolUsage:         info.ToolUsageHooks,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelTypeVertexAi)
	if adaptor == nil {
		return nil, errors.New("Vertex AI relay adapter is unavailable")
	}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(request, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, request)
	response, err := relayHTTPClient.Do(request)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer response.Body.Close()
	return adaptor.DoResponse(c, response, meta)
}

// geminiNativePassthrough forwards the native Gemini body verbatim to a Gemini
// channel and streams the native response back unchanged (extracting usage for
// settlement).
func geminiNativePassthrough(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	base := info.Channel.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	mappedModel := preparedGeminiModel(info)

	action := "generateContent"
	if info.IsStream {
		action = "streamGenerateContent?alt=sse"
	}
	version := setting.GetGeminiAPIVersion(mappedModel)
	url := relaycommon.JoinURL(base, "/"+version+"/models/"+mappedModel+":"+action)

	// Rewrite the model field when the request carries one and it was mapped;
	// otherwise forward the raw body byte-for-byte.
	body := info.RawBody
	if info.GeminiRequest != nil && info.GeminiRequest.Model != "" && mappedModel != info.ModelName {
		var m map[string]any
		if err := protocolkit.UnmarshalJSON(body, &m); err == nil {
			m["model"] = "models/" + mappedModel
			if b, err := protocolkit.MarshalJSON(m); err == nil {
				body = b
			}
		}
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", channelssvc.GetChannelKey(info.Channel))
	channelssvc.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if info.IsStream {
		return geminiNativeStreamResponseWithInfo(c, resp, info)
	}
	return geminiNativeNonStreamResponseWithInfo(c, resp, info)
}

// geminiNativeNonStreamResponse parses the native response for usage, then
// copies it verbatim to the client.
func geminiNativeNonStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	return geminiNativeNonStreamResponseWithInfo(c, resp, nil)
}

func geminiNativeNonStreamResponseWithInfo(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Gemini native response: %w", err)
	}
	var geminiResp protocolkit.GeminiChatResponse
	if err := protocolkit.UnmarshalJSON(body, &geminiResp); err != nil {
		return nil, &relaycommon.UpstreamError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	var hooks *relaycommon.ToolUsageHooks
	if info != nil {
		hooks = info.ToolUsageHooks
	}
	if err := relaycommon.ObserveGeminiResponse(hooks, &geminiResp); err != nil {
		return nil, fmt.Errorf("observe Gemini native Google Search usage: %w", err)
	}
	var usage *protocolkit.Usage
	if geminiResp.UsageMetadata != nil {
		usage = protocolkit.GeminiUsageToOpenAIUsage(geminiResp.UsageMetadata)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(body); err != nil {
		return nil, fmt.Errorf("write Gemini native response: %w", err)
	}
	return usage, nil
}

// geminiNativeStreamResponse proxies the upstream SSE stream verbatim and
// extracts usage metadata from streamed chunks.
func geminiNativeStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	return geminiNativeStreamResponseWithInfo(c, resp, nil)
}

func geminiNativeStreamResponseWithInfo(c *gin.Context, resp *http.Response, info *RelayInfo) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" {
				var chunk protocolkit.GeminiChatResponse
				if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err == nil {
					var hooks *relaycommon.ToolUsageHooks
					if info != nil {
						hooks = info.ToolUsageHooks
					}
					if observeErr := relaycommon.ObserveGeminiResponse(hooks, &chunk); observeErr != nil {
						return usage, fmt.Errorf("observe Gemini native stream Google Search usage: %w", observeErr)
					}
					if chunk.UsageMetadata != nil {
						usage = protocolkit.GeminiUsageToOpenAIUsage(chunk.UsageMetadata)
					}
				}
			}
		}
		if _, err := c.Writer.WriteString(line + "\n"); err != nil {
			return usage, fmt.Errorf("write Gemini native stream: %w", err)
		}
		c.Writer.Flush()
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan Gemini native stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	return usage, nil
}

func geminiViaAdvancedCustom(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	mappedModel := preparedGeminiModel(info)
	nativeBody := info.RawBody
	if info.GeminiRequest != nil && info.GeminiRequest.Model != "" && mappedModel != info.ModelName {
		var body map[string]any
		if err := protocolkit.UnmarshalJSON(nativeBody, &body); err != nil {
			return nil, errors.New("invalid Gemini request body")
		}
		body["model"] = "models/" + mappedModel
		encoded, err := protocolkit.MarshalJSON(body)
		if err != nil {
			return nil, fmt.Errorf("encode mapped Gemini request: %w", err)
		}
		nativeBody = encoded
	}
	call, err := callAdvancedCustomNative(c, info, channelcatalog.RelayFormatGemini, nativeBody, info.Request)
	if err != nil {
		return nil, err
	}
	defer call.response.Body.Close()
	if call.response.StatusCode >= http.StatusBadRequest {
		return nil, relaycommon.HandleErrorResponse(call.response)
	}
	switch call.converter {
	case advancedconfig.ConverterNone:
		if info.IsStream {
			return geminiNativeStreamResponseWithInfo(c, call.response, info)
		}
		return geminiNativeNonStreamResponseWithInfo(c, call.response, info)
	case advancedconfig.ConverterGeminiToOpenAIChat:
		if info.IsStream {
			return geminiStreamFromOpenAI(c, call.response)
		}
		return geminiNonStreamFromOpenAI(c, call.response)
	default:
		return nil, fmt.Errorf("advanced custom converter %q cannot serve Gemini generateContent", call.converter)
	}
}

// geminiViaOpenAIChannel converts the native Gemini request to OpenAI chat
// completions, calls an OpenAI-compatible channel, and converts the response
// back to native Gemini format.
func geminiViaOpenAIChannel(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	base := info.Channel.BaseURL
	mappedModel := preparedGeminiModel(info)
	if info.Request == nil {
		return nil, errors.New("invalid gemini request")
	}
	// Keep the request snapshot used for moderation and accounting immutable.
	// Provider adapters receive a shallow copy and apply the mapped model via
	// metadata when they serialize the upstream request.
	openaiReq := *info.Request
	meta := &relaycommon.Meta{
		Channel:      info.Channel,
		Mode:         channelcatalog.RelayModeChatCompletions,
		Format:       channelcatalog.RelayFormatOpenAI,
		ModelName:    mappedModel,
		BaseURL:      base,
		APIKey:       channelssvc.GetChannelKey(info.Channel),
		Request:      &openaiReq,
		IsStream:     info.IsStream,
		PromptTokens: info.PromptTokens,
	}
	adaptor := GetAdaptor(channelcatalog.ChannelType(info.Channel.Type))
	if adaptor == nil {
		return nil, fmt.Errorf("channel type %d has no implemented relay adapter", info.Channel.Type)
	}
	adaptor.Init(meta)
	url, err := adaptor.GetRequestURL(meta)
	if err != nil {
		return nil, err
	}
	body, err := adaptor.ConvertRequest(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := adaptor.SetupRequestHeader(req, meta); err != nil {
		return nil, err
	}
	channelssvc.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, relaycommon.SanitizeTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if info.IsStream {
		return geminiStreamFromOpenAI(c, resp)
	}
	return geminiNonStreamFromOpenAI(c, resp)
}

// geminiNonStreamFromOpenAI converts an OpenAI chat completion into a native
// Gemini GenerateContent response.
func geminiNonStreamFromOpenAI(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI response for Gemini conversion: %w", err)
	}
	var openaiResp protocolkit.ChatCompletionsResponse
	if err := protocolkit.UnmarshalJSON(body, &openaiResp); err != nil {
		return nil, err
	}
	geminiResp := protocolkit.OpenAIResponseToGeminiResponse(&openaiResp)
	out, err := protocolkit.MarshalJSON(geminiResp)
	if err != nil {
		return nil, fmt.Errorf("encode Gemini response: %w", err)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return nil, fmt.Errorf("write Gemini response: %w", err)
	}
	return openaiResp.Usage, nil
}

// geminiStreamFromOpenAI converts an OpenAI SSE stream into native Gemini SSE
// chunks. Each content delta becomes a Gemini chunk with a candidate part;
// the stream terminates with a STOP candidate.
func geminiStreamFromOpenAI(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	var contentChars int
	index := 0
	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk protocolkit.ChatCompletionsStreamResponse
		if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			contentChars += len(ch.Delta.Content)
			geminiChunk := protocolkit.GeminiChatResponse{
				Candidates: []protocolkit.GeminiChatCandidate{{
					Index: index,
					Content: &protocolkit.GeminiChatContent{
						Role:  "model",
						Parts: []protocolkit.GeminiPart{{Text: ch.Delta.Content}},
					},
				}},
			}
			b, err := protocolkit.MarshalJSON(geminiChunk)
			if err != nil {
				return usage, fmt.Errorf("encode Gemini stream chunk: %w", err)
			}
			if _, err := c.Writer.WriteString("data: " + string(b) + "\n\n"); err != nil {
				return usage, fmt.Errorf("write Gemini stream chunk: %w", err)
			}
			c.Writer.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("scan OpenAI stream for Gemini conversion (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	final := protocolkit.GeminiChatResponse{
		Candidates: []protocolkit.GeminiChatCandidate{{
			Index:        index,
			Content:      &protocolkit.GeminiChatContent{Role: "model"},
			FinishReason: "STOP",
		}},
	}
	if usage != nil {
		final.UsageMetadata = &protocolkit.GeminiUsageMetadata{
			PromptTokenCount:     usage.PromptTokens,
			CandidatesTokenCount: usage.CompletionTokens,
			TotalTokenCount:      usage.TotalTokens,
		}
	}
	b, err := protocolkit.MarshalJSON(final)
	if err != nil {
		return usage, fmt.Errorf("encode final Gemini stream chunk: %w", err)
	}
	if _, err := c.Writer.WriteString("data: " + string(b) + "\n\n"); err != nil {
		return usage, fmt.Errorf("write final Gemini stream chunk: %w", err)
	}
	c.Writer.Flush()

	if usage == nil && contentChars > 0 {
		usage = relaycommon.EstimateStreamUsage(0, contentChars)
	}
	return usage, nil
}
