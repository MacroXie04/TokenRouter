// Package openai implements the OpenAI-compatible provider adapter. It serves
// as the shared adapter for all OpenAI-compatible channels (custom endpoints,
// Azure, DeepSeek, OpenRouter, etc.).
package openai

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

// Adaptor is the OpenAI-compatible adapter.
type Adaptor struct {
	ChannelType constant.ChannelType
	Format      constant.RelayFormat
	Mode        constant.RelayMode
}

func (a *Adaptor) Init(meta *relaycommon.Meta) {
	if meta.Channel != nil {
		a.ChannelType = constant.ChannelType(meta.Channel.Type)
	}
	a.Format = meta.Format
	a.Mode = meta.Mode
}

func (a *Adaptor) GetRequestURL(meta *relaycommon.Meta) (string, error) {
	base := meta.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	switch a.Mode {
	case constant.RelayModeChatCompletions:
		return relaycommon.JoinURL(base, "/v1/chat/completions"), nil
	case constant.RelayModeCompletions:
		return relaycommon.JoinURL(base, "/v1/completions"), nil
	case constant.RelayModeEmbeddings:
		return relaycommon.JoinURL(base, "/v1/embeddings"), nil
	case constant.RelayModeModerations:
		return relaycommon.JoinURL(base, "/v1/moderations"), nil
	case constant.RelayModeImagesGenerations:
		return relaycommon.JoinURL(base, "/v1/images/generations"), nil
	case constant.RelayModeImagesEdits:
		return relaycommon.JoinURL(base, "/v1/images/edits"), nil
	case constant.RelayModeAudioSpeech:
		return relaycommon.JoinURL(base, "/v1/audio/speech"), nil
	case constant.RelayModeAudioTranscription:
		return relaycommon.JoinURL(base, "/v1/audio/transcriptions"), nil
	case constant.RelayModeAudioTranslation:
		return relaycommon.JoinURL(base, "/v1/audio/translations"), nil
	case constant.RelayModeResponses, constant.RelayModeResponsesCompact:
		return relaycommon.JoinURL(base, "/v1/responses"), nil
	case constant.RelayModeRerank:
		return relaycommon.JoinURL(base, "/v1/rerank"), nil
	case constant.RelayModeRealtime:
		return relaycommon.JoinURL(base, "/v1/realtime"), nil
	default:
		return "", fmt.Errorf("unsupported relay mode %d for openai adapter", a.Mode)
	}
}

func (a *Adaptor) SetupRequestHeader(req *http.Request, meta *relaycommon.Meta) error {
	req.Header.Set("Content-Type", "application/json")
	if meta.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+meta.APIKey)
	}
	if meta.Channel != nil && meta.Channel.OpenAIOrganization != "" {
		req.Header.Set("OpenAI-Organization", meta.Channel.OpenAIOrganization)
	}
	return nil
}

func (a *Adaptor) ConvertRequest(meta *relaycommon.Meta) ([]byte, error) {
	// Chat/completions use the typed DTO. Other modes (embeddings, images,
	// audio, moderations, rerank, responses) pass through the original request
	// body with the mapped model name, so mode-specific fields (input, prompt,
	// n, voice, documents, …) are preserved.
	switch a.Mode {
	case constant.RelayModeChatCompletions, constant.RelayModeCompletions:
		return protocolkit.MarshalJSON(meta.Request)
	default:
		body := meta.Request.Extra
		if body == nil {
			body = map[string]any{}
		}
		body["model"] = meta.ModelName
		return protocolkit.MarshalJSON(body)
	}
}

// DoResponse converts the upstream OpenAI response to the client. For streams
// it proxies the SSE and extracts usage from the final chunk (or estimates).
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
	// Extract usage and rewrite the model name to the requested model.
	var parsed protocolkit.ChatCompletionsResponse
	var usage *protocolkit.Usage
	if err := protocolkit.UnmarshalJSON(body, &parsed); err == nil {
		if parsed.Usage != nil {
			usage = parsed.Usage
		}
		// For embedding/other shapes, try generic usage extraction.
	} else {
		usage = relaycommon.ExtractUsageFromBody(body)
	}

	c.Status(resp.StatusCode)
	c.Header("Content-Type", "application/json")
	_, _ = c.Writer.Write(body)
	return usage, nil
}

func (a *Adaptor) streamResponse(c *gin.Context, resp *http.Response, meta *relaycommon.Meta) (*protocolkit.Usage, error) {
	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()

	var usage *protocolkit.Usage
	var contentLength int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				_, _ = c.Writer.WriteString("data: [DONE]\n\n")
				c.Writer.Flush()
				continue
			}
			var chunk protocolkit.ChatCompletionsStreamResponse
			if err := protocolkit.UnmarshalJSON([]byte(data), &chunk); err == nil {
				if chunk.Usage != nil {
					usage = chunk.Usage
				}
				// Accumulate delta content for completion-token estimation.
				for _, ch := range chunk.Choices {
					contentLength += len(ch.Delta.Content)
				}
			}
			_, _ = c.Writer.WriteString(line + "\n\n")
			c.Writer.Flush()
		} else if line != "" {
			_, _ = c.Writer.WriteString(line + "\n")
			c.Writer.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if usage == nil && contentLength > 0 {
		usage = relaycommon.EstimateStreamUsage(meta.PromptTokens, contentLength)
	}
	return usage, nil
}
