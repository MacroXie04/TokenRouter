// Package gemini implements the Google Gemini (and Vertex AI) adapter.
package gemini

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
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
	if a.Mode == constant.RelayModeEmbeddings {
		// The embedding payload is always built batch-style (requests array),
		// matching the reference; use the batch endpoint.
		return relaycommon.JoinURL(base, "/v1beta/models/"+meta.ModelName+":batchEmbedContents"), nil
	}
	action := "generateContent"
	if meta.IsStream {
		action = "streamGenerateContent?alt=sse"
	}
	return relaycommon.JoinURL(base, "/v1beta/models/"+meta.ModelName+":"+action), nil
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
	return a.nonStreamResponse(c, resp)
}

// embeddingResponse converts the batch embedContent response into the OpenAI
// embeddings list shape. Embeddings are billed by prompt tokens.
func (a *Adaptor) embeddingResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
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
	_, _ = c.Writer.Write(b)
	return usage, nil
}

func (a *Adaptor) nonStreamResponse(c *gin.Context, resp *http.Response) (*protocolkit.Usage, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var geminiResp protocolkit.GeminiChatResponse
	if err := protocolkit.UnmarshalJSON(body, &geminiResp); err != nil {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	openaiResp := protocolkit.GeminiResponseToOpenAIResponse(&geminiResp)
	out, _ := protocolkit.MarshalJSON(openaiResp)
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(out)
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

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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
		if geminiResp.UsageMetadata != nil {
			usage = protocolkit.GeminiUsageToOpenAIUsage(geminiResp.UsageMetadata)
			promptTokens = usage.PromptTokens
		}
		if len(geminiResp.Candidates) > 0 && geminiResp.Candidates[0].Content != nil {
			for _, p := range geminiResp.Candidates[0].Content.Parts {
				if p.Text != "" {
					contentChars += len(p.Text)
					chunk := protocolkit.ChatCompletionsStreamResponse{
						Object: "chat.completion.chunk",
						Choices: []protocolkit.ChatCompletionsStreamResponseChoice{{
							Index: 0,
							Delta: protocolkit.ChatCompletionsStreamResponseChoiceDelta{Content: p.Text},
						}},
					}
					b, _ := protocolkit.MarshalJSON(chunk)
					_, _ = c.Writer.WriteString("data: " + string(b) + "\n\n")
					c.Writer.Flush()
				}
			}
		}
	}

	_, _ = c.Writer.WriteString("data: [DONE]\n\n")
	c.Writer.Flush()

	if usage == nil && contentChars > 0 {
		usage = relaycommon.EstimateStreamUsage(promptTokens, contentChars)
	}
	return usage, nil
}
