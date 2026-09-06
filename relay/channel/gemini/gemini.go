// Package gemini implements the Google Gemini (and Vertex AI) adapter.
package gemini

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

// Adaptor is the Gemini adapter.
type Adaptor struct {
	Mode constant.RelayMode
}

func (a *Adaptor) Init(meta *relaycommon.Meta) { a.Mode = meta.Mode }

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	base := meta.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	model := relaycommon.PrepareGeminiRequest(nil, meta.OriginalModelName, meta.ModelName, false)
	version := setting.GetGeminiAPIVersion(model)
	if a.Mode == constant.RelayModeEmbeddings {
		// The embedding payload is always built batch-style (requests array),
		// matching the reference; use the batch endpoint.
		return relaycommon.JoinURL(base, "/"+version+"/models/"+model+":batchEmbedContents"), nil
	}
	action := "generateContent"
	if meta.IsStream {
		action = "streamGenerateContent?alt=sse"
	}
	return relaycommon.JoinURL(base, "/"+version+"/models/"+model+":"+action), nil
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", meta.APIKey)
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	if a.Mode == constant.RelayModeEmbeddings {
		return convertEmbeddingRequest(meta)
	}
	geminiReq := protocolkit.OpenAIRequestToGeminiRequest(meta.Request)
	_ = relaycommon.PrepareGeminiRequest(geminiReq, meta.OriginalModelName, meta.ModelName, true)
	// Gemini selects the mapped model from the request URL. Omitting the
	// OpenAI-facing model from the body prevents two conflicting model names
	// from being sent upstream.
	geminiReq.Model = ""
	return protocolkit.MarshalJSON(geminiReq)
}

// convertEmbeddingRequest converts an OpenAI embedding request (input as a
// string or array of strings) into the Gemini batch embedContent payload.
func convertEmbeddingRequest(meta *relaycommon.Meta) ([]byte, error) {
	inputs, err := embeddingInputs(meta)
	if err != nil {
		return nil, err
	}
	requests := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		requests = append(requests, map[string]any{
			"model": "models/" + meta.ModelName,
			"content": protocolkit.GeminiChatContent{
				Parts: []protocolkit.GeminiPart{{Text: input}},
			},
		})
	}
	return protocolkit.MarshalJSON(map[string]any{"requests": requests})
}

// embeddingInputs extracts the OpenAI embedding input field (string or
// []string) from the raw request body.
func embeddingInputs(meta *relaycommon.Meta) ([]string, error) {
	if meta.Request == nil || meta.Request.Extra == nil {
		return nil, fmt.Errorf("embedding request missing input")
	}
	switch in := meta.Request.Extra["input"].(type) {
	case string:
		if in == "" {
			return nil, fmt.Errorf("input is empty")
		}
		return []string{in}, nil
	case []any:
		out := make([]string, 0, len(in))
		for _, item := range in {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("input is empty")
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(in))
		for _, s := range in {
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("input is empty")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("input is required")
	}
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if a.Mode == constant.RelayModeEmbeddings {
		return a.embeddingResponse(c, resp, meta)
	}
	if meta.IsStream {
		return a.streamResponse(c, resp, meta)
	}
	return a.nonStreamResponse(c, resp, meta)
}

// embeddingResponse converts the batch embedContent response into the OpenAI
// embeddings list shape. Embeddings are billed by prompt tokens.
func (a *Adaptor) embeddingResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamLargeJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Gemini embedding response: %w", err)
	}
	var batch struct {
		Embeddings []protocolkit.ContentEmbedding `json:"embeddings"`
	}
	if err := protocolkit.UnmarshalJSON(body, &batch); err != nil {
		return nil, err
	}
	data := make([]gin.H, 0, len(batch.Embeddings))
	for i, emb := range batch.Embeddings {
		data = append(data, gin.H{"object": "embedding", "embedding": emb.Values, "index": i})
	}
	usage := &protocolkit.Usage{PromptTokens: meta.PromptTokens, TotalTokens: meta.PromptTokens}
	out := gin.H{"object": "list", "data": data, "model": meta.ModelName, "usage": usage}
	b, _ := protocolkit.MarshalJSON(out)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(b); err != nil {
		return usage, fmt.Errorf("write Gemini embedding response: %w", err)
	}
	return usage, nil
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := relaycommon.ReadUpstreamBody(resp.Body, relaycommon.MaxUpstreamJSONBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read Gemini response: %w", err)
	}
	var geminiResp protocolkit.GeminiChatResponse
	if err := protocolkit.UnmarshalJSON(body, &geminiResp); err != nil {
		return nil, fmt.Errorf("decode Gemini response: %w", err)
	}
	if err := observeGeminiGoogleSearch(meta.ToolHooks(), &geminiResp); err != nil {
		return nil, fmt.Errorf("observe Gemini Google Search usage: %w", err)
	}
	openaiResp := protocolkit.GeminiResponseToOpenAIResponse(&geminiResp)
	out, _ := protocolkit.MarshalJSON(openaiResp)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	if _, err := c.Writer.Write(out); err != nil {
		return openaiResp.Usage, fmt.Errorf("write Gemini response: %w", err)
	}
	return openaiResp.Usage, nil
}

func (a *Adaptor) streamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	var contentChars int
	var promptTokens int
	roleSent := false
	finishSent := false
	sawToolCall := false

	sendChunk := func(delta protocolkit.ChatCompletionsStreamResponseChoiceDelta, finishReason *string) {
		chunk := protocolkit.ChatCompletionsStreamResponse{
			Object: "chat.completion.chunk",
			Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
				Index: 0, Delta: delta, FinishReason: finishReason,
			}},
		}
		body, _ := protocolkit.MarshalJSON(chunk)
		_, _ = c.Writer.WriteString("data: " + string(body) + "\n\n")
		c.Writer.Flush()
	}

	scanner := relaycommon.NewUpstreamSSEScanner(resp.Body)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var geminiResp protocolkit.GeminiChatResponse
		if err := protocolkit.UnmarshalJSON([]byte(data), &geminiResp); err != nil {
			continue
		}
		if err := observeGeminiGoogleSearch(meta.ToolHooks(), &geminiResp); err != nil {
			return usage, fmt.Errorf("observe Gemini stream Google Search usage: %w", err)
		}
		if geminiResp.UsageMetadata != nil {
			usage = protocolkit.GeminiUsageToOpenAIUsage(geminiResp.UsageMetadata)
			promptTokens = usage.PromptTokens
		}
		if len(geminiResp.Candidates) > 0 {
			candidate := geminiResp.Candidates[0]
			if candidate.Content != nil {
				if !roleSent {
					sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"}, nil)
					roleSent = true
				}
				for partIndex, p := range candidate.Content.Parts {
					if p.Text != "" {
						contentChars += len(p.Text)
						delta := protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: p.Text}
						if p.Thought {
							delta.Content = ""
							delta.ReasoningContent = p.Text
						}
						sendChunk(delta, nil)
					}
					if p.FunctionCall != nil {
						sawToolCall = true
						sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []protocolkit.ToolCallResponse{{
							Index: partIndex, Id: geminiStreamToolCallID(p.FunctionCall.Name), Type: "function",
							Function: &protocolkit.FunctionResponse{
								Name: p.FunctionCall.Name, Arguments: protocolkit.ToJSONString(p.FunctionCall.Args),
							},
						}}}, nil)
					}
				}
			}
			if !finishSent {
				if reason := mapGeminiStreamFinishReason(candidate.FinishReason); reason != "" {
					if sawToolCall {
						reason = "tool_calls"
					}
					sendChunk(protocolkit.ChatCompletionsStreamResponseChoiceDelta{}, &reason)
					finishSent = true
				}
			}
		}
	}

	if usage == nil && contentChars > 0 {
		usage = relaycommon.EstimateStreamUsage(promptTokens, contentChars)
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read Gemini event stream (maximum event %d bytes): %w",
			relaycommon.MaxUpstreamSSEEventBytes, err)
	}
	_, _ = c.Writer.WriteString("data: [DONE]\n\n")
	c.Writer.Flush()
	return usage, nil
}

func observeGeminiGoogleSearch(hooks *relaycommon.ToolUsageHooks, response *protocolkit.GeminiChatResponse) error {
	if hooks == nil || hooks.MarkGeminiGoogleSearch == nil || response == nil {
		return nil
	}
	for _, candidate := range response.Candidates {
		if candidate.GroundingMetadata != nil && len(candidate.GroundingMetadata.WebSearchQueries) > 0 {
			return hooks.MarkGeminiGoogleSearch()
		}
	}
	return nil
}

func geminiStreamToolCallID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "call_gemini"
	}
	var id strings.Builder
	id.WriteString("call_")
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			id.WriteRune(r)
		} else {
			id.WriteByte('_')
		}
	}
	return id.String()
}

func mapGeminiStreamFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST":
		return "content_filter"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	default:
		return strings.ToLower(reason)
	}
}
