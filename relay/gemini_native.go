package relay

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
)

// RelayGeminiNative serves the native Gemini passthrough routes
// (POST /v1/models/*path and POST /v1beta/models/*path). The client speaks the
// Gemini protocol end to end: the body is a GenerateContent request, the model
// name comes from the URL path, and the response is returned in native Gemini
// format. Channels of Gemini type pass the body through verbatim; other
// OpenAI-compatible channels convert the request to OpenAI chat completions
// and convert the response back (mirroring the reference's RelayFormatGemini).
func RelayGeminiNative(c *gin.Context) {
	rawBody, err := io.ReadAll(io.LimitReader(c.Request.Body, 16*1024*1024))
	if err != nil {
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
		Mode:          constant.RelayModeGemini,
		Format:        constant.RelayFormatGemini,
		ModelName:     modelName,
		Request:       converted,
		GeminiRequest: &native,
		RawBody:       rawBody,
		Group:         getRelayGroup(c),
		IsStream:      geminiStreamRequested(c, &native),
	}
	info.Request.Stream = info.IsStream
	_ = relayAndSettleWithDispatch(c, info, dispatchGeminiNative)
}

// geminiStreamRequested reports whether the client asked for streaming: the
// SSE alt query (streamGenerateContent?alt=sse), the standard ?alt=sse query,
// or an Accept: text/event-stream header.
func geminiStreamRequested(c *gin.Context, native *protocolkit.GeminiChatRequest) bool {
	_ = native
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
	channelType := constant.ChannelType(info.Channel.Type)
	if channelType == constant.ChannelTypeGemini || channelType == constant.ChannelTypeVertexAi {
		return geminiNativePassthrough(c, info)
	}
	return geminiViaOpenAIChannel(c, info)
}

// geminiNativePassthrough forwards the native Gemini body verbatim to a Gemini
// channel and streams the native response back unchanged (extracting usage for
// settlement).
func geminiNativePassthrough(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	base := info.Channel.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	mappedModel := relaycommon.GetMappedModel(info.Channel, info.ModelName)

	action := "generateContent"
	if info.IsStream {
		action = "streamGenerateContent?alt=sse"
	}
	url := relaycommon.JoinURL(base, "/v1beta/models/"+mappedModel+":"+action)

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
	req.Header.Set("x-goog-api-key", service.GetChannelKey(info.Channel))
	service.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if info.IsStream {
		return geminiNativeStreamResponse(c, resp)
	}
	return geminiNativeNonStreamResponse(c, resp)
}

// geminiNativeNonStreamResponse parses the native response for usage, then
// copies it verbatim to the client.
func geminiNativeNonStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var geminiResp protocolkit.GeminiChatResponse
	if err := protocolkit.UnmarshalJSON(body, &geminiResp); err != nil {
		return nil, &relaycommon.UpstreamError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	var usage *protocolkit.Usage
	if geminiResp.UsageMetadata != nil {
		usage = protocolkit.GeminiUsageToOpenAIUsage(geminiResp.UsageMetadata)
	}
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(body)
	return usage, nil
}

// geminiNativeStreamResponse proxies the upstream SSE stream verbatim and
// extracts usage metadata from streamed chunks.
func geminiNativeStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" {
				var chunk protocolkit.GeminiChatResponse
				if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err == nil && chunk.UsageMetadata != nil {
					usage = protocolkit.GeminiUsageToOpenAIUsage(chunk.UsageMetadata)
				}
			}
		}
		_, _ = c.Writer.WriteString(line + "\n")
		c.Writer.Flush()
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	return usage, nil
}

// geminiViaOpenAIChannel converts the native Gemini request to OpenAI chat
// completions, calls an OpenAI-compatible channel, and converts the response
// back to native Gemini format.
func geminiViaOpenAIChannel(c *gin.Context, info *RelayInfo) (*protocolkit.Usage, error) {
	base := info.Channel.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	mappedModel := relaycommon.GetMappedModel(info.Channel, info.ModelName)
	openaiReq := info.Request
	if openaiReq == nil {
		return nil, errors.New("invalid gemini request")
	}
	openaiReq.Model = mappedModel

	body, err := protocolkit.MarshalJSON(openaiReq)
	if err != nil {
		return nil, err
	}
	url := relaycommon.JoinURL(base, "/v1/chat/completions")
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+service.GetChannelKey(info.Channel))
	service.ApplyChannelAffinityRequestHeaders(c, req)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, err
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var openaiResp protocolkit.ChatCompletionsResponse
	if err := protocolkit.UnmarshalJSON(body, &openaiResp); err != nil {
		return nil, err
	}
	geminiResp := protocolkit.OpenAIResponseToGeminiResponse(&openaiResp)
	out, _ := protocolkit.MarshalJSON(geminiResp)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(out)
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
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
			b, _ := protocolkit.MarshalJSON(geminiChunk)
			_, _ = c.Writer.WriteString("data: " + string(b) + "\n\n")
			c.Writer.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
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
	b, _ := protocolkit.MarshalJSON(final)
	_, _ = c.Writer.WriteString("data: " + string(b) + "\n\n")
	c.Writer.Flush()

	if usage == nil && contentChars > 0 {
		usage = relaycommon.EstimateStreamUsage(0, contentChars)
	}
	return usage, nil
}
