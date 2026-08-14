// Package gemini implements the Google Gemini (and Vertex AI) adapter.
package gemini

import (
	"bufio"
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
		return relaycommon.JoinURL(base, "/v1beta/models/"+meta.ModelName+":embedContent"), nil
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
		// Keep the raw body for embeddings; parse content from the OpenAI request.
		return protocolkit.MarshalJSON(meta.Request)
	}
	geminiReq := protocolkit.OpenAIRequestToGeminiRequest(meta.Request)
	return protocolkit.MarshalJSON(geminiReq)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	if resp.StatusCode >= 400 {
		return nil, relaycommon.HandleErrorResponse(resp)
	}
	if meta.IsStream {
		return a.streamResponse(c, resp, meta)
	}
	return a.nonStreamResponse(c, resp)
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
