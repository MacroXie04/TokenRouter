package protocolkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeUsageToOpenAIUsage(t *testing.T) {
	u := &ClaudeUsage{
		InputTokens:              100,
		CacheCreationInputTokens: 200,
		CacheReadInputTokens:     300,
		OutputTokens:             50,
	}
	got := ClaudeUsageToOpenAIUsage(u)
	// prompt_tokens must include cache (OpenAI convention).
	assert.Equal(t, 600, got.PromptTokens)
	assert.Equal(t, 50, got.CompletionTokens)
	assert.Equal(t, 650, got.TotalTokens)
	assert.Equal(t, 300, got.PromptCacheHitTokens)
	assert.Equal(t, 200, got.PromptCacheMissTokens)
	require.NotNil(t, got.PromptTokensDetails)
	assert.Equal(t, 300, got.PromptTokensDetails.CachedTokens)
}

func TestGeminiUsageToOpenAIUsage(t *testing.T) {
	meta := &GeminiUsageMetadata{
		PromptTokenCount:        120,
		CandidatesTokenCount:    30,
		TotalTokenCount:         150,
		PromptTokensDetails:     []GeminiPromptTokensDetails{{Modality: "AUDIO", TokenCount: 20}},
		CandidatesTokensDetails: []GeminiCandidatesTokensDetails{{Modality: "IMAGE", TokenCount: 5}},
	}
	got := GeminiUsageToOpenAIUsage(meta)
	assert.Equal(t, 120, got.PromptTokens)
	assert.Equal(t, 30, got.CompletionTokens)
	assert.Equal(t, 150, got.TotalTokens)
	assert.Equal(t, 20, got.AudioTokens)
	require.NotNil(t, got.CompletionTokensDetails)
	assert.Equal(t, 5, got.CompletionTokensDetails.ImageTokens)
}

func TestClaudeResponseToOpenAIResponse(t *testing.T) {
	resp := &ClaudeResponse{
		Id:    "msg_1",
		Model: "claude-sonnet",
		Content: []ClaudeMediaMessage{
			{Type: "text", Text: "Hello"},
			{Type: "text", Text: " world"},
		},
		StopReason: "end_turn",
		Usage:      &ClaudeUsage{InputTokens: 10, OutputTokens: 2},
	}
	got := ClaudeResponseToOpenAIResponse(resp)
	assert.Equal(t, "chat.completion", got.Object)
	require.Len(t, got.Choices, 1)
	assert.Equal(t, "Hello world", got.Choices[0].Message.Content)
	assert.Equal(t, "stop", got.Choices[0].FinishReason)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 10, got.Usage.PromptTokens)
}

func TestGeminiResponseToOpenAIResponse(t *testing.T) {
	resp := &GeminiChatResponse{
		Candidates: []GeminiChatCandidate{{
			Content:      &GeminiChatContent{Parts: []GeminiPart{{Text: "Bonjour"}}},
			FinishReason: "STOP",
		}},
		UsageMetadata: &GeminiUsageMetadata{PromptTokenCount: 3, CandidatesTokenCount: 1, TotalTokenCount: 4},
	}
	got := GeminiResponseToOpenAIResponse(resp)
	require.Len(t, got.Choices, 1)
	assert.Equal(t, "Bonjour", got.Choices[0].Message.Content)
	assert.Equal(t, "stop", got.Choices[0].FinishReason)
	assert.Equal(t, 3, got.Usage.PromptTokens)
}

func TestOpenAIRequestToClaudeRequest(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Model:     "claude-sonnet",
		MaxTokens: intPtr(1024),
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi"},
		},
		Tools: []ToolCallRequest{{Function: &FunctionRequest{Name: "get_weather", Parameters: map[string]any{"type": "object"}}}},
	}
	got := OpenAIRequestToClaudeRequest(req)
	assert.Equal(t, "claude-sonnet", got.Model)
	assert.Equal(t, 1024, got.MaxTokens)
	assert.Equal(t, "You are helpful.", got.System)
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
	require.Len(t, got.Tools, 1)
	assert.Equal(t, "get_weather", got.Tools[0].Name)
}

func TestOpenAIRequestToGeminiRequest(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Model: "gemini-pro",
		Messages: []Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello"},
		},
	}
	got := OpenAIRequestToGeminiRequest(req)
	require.NotNil(t, got.SystemInstruction)
	require.Len(t, got.Contents, 2)
	assert.Equal(t, "user", got.Contents[0].Role)
	assert.Equal(t, "model", got.Contents[1].Role)
	assert.Equal(t, "Hello", got.Contents[1].Parts[0].Text)
}

func TestRoundTripMarshalPreservesExplicitZero(t *testing.T) {
	// An explicit 0 max_tokens must survive re-marshal (pointer scalar).
	req := &GeneralOpenAIRequest{Model: "m", MaxTokens: intPtr(0)}
	b, err := MarshalJSON(req)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(b, &raw))
	assert.Contains(t, raw, "max_tokens")
	assert.Equal(t, float64(0), raw["max_tokens"])
}

func TestOpenAIResponseToClaudeResponse(t *testing.T) {
	resp := &ChatCompletionsResponse{
		Id:    "chatcmpl-9",
		Model: "claude-3-5-sonnet-20241022",
		Choices: []ChatCompletionsChoice{
			{Index: 0, Message: &ChatResponseMessage{Role: "assistant", Content: "hello there"}, FinishReason: "stop"},
		},
		Usage: &Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}
	got := OpenAIResponseToClaudeResponse(resp)
	require.NotNil(t, got)
	assert.Equal(t, "message", got.Type)
	assert.Equal(t, "assistant", got.Role)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "hello there", got.Content[0].Text)
	assert.Equal(t, "end_turn", got.StopReason)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 5, got.Usage.InputTokens)
	assert.Equal(t, 2, got.Usage.OutputTokens)

	assert.Equal(t, "max_tokens", OpenAIFinishReasonToClaudeStopReason("length"))
	assert.Equal(t, "tool_use", OpenAIFinishReasonToClaudeStopReason("tool_calls"))
	assert.Equal(t, "refusal", OpenAIFinishReasonToClaudeStopReason("content_filter"))
}

func TestOpenAIResponseToGeminiResponse(t *testing.T) {
	resp := &ChatCompletionsResponse{
		Id:    "chatcmpl-9",
		Model: "gemini-2.0-flash",
		Choices: []ChatCompletionsChoice{
			{Index: 0, Message: &ChatResponseMessage{Role: "assistant", Content: "hello there"}, FinishReason: "stop"},
		},
		Usage: &Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}
	got := OpenAIResponseToGeminiResponse(resp)
	require.NotNil(t, got)
	require.Len(t, got.Candidates, 1)
	cand := got.Candidates[0]
	require.NotNil(t, cand.Content)
	assert.Equal(t, "model", cand.Content.Role)
	require.Len(t, cand.Content.Parts, 1)
	assert.Equal(t, "hello there", cand.Content.Parts[0].Text)
	assert.Equal(t, "STOP", cand.FinishReason)
	require.NotNil(t, got.UsageMetadata)
	assert.Equal(t, 5, got.UsageMetadata.PromptTokenCount)
	assert.Equal(t, 2, got.UsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 7, got.UsageMetadata.TotalTokenCount)
}

func TestOpenAIResponseToGeminiResponseToolCall(t *testing.T) {
	resp := &ChatCompletionsResponse{
		Choices: []ChatCompletionsChoice{{
			Index: 0,
			Message: &ChatResponseMessage{Role: "assistant",
				ToolCalls: []ToolCallResponse{{Type: "function", Function: &FunctionResponse{Name: "get_weather", Arguments: `{"city":"sf"}`}}},
			},
			FinishReason: "tool_calls",
		}},
	}
	got := OpenAIResponseToGeminiResponse(resp)
	require.Len(t, got.Candidates, 1)
	parts := got.Candidates[0].Content.Parts
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].FunctionCall)
	assert.Equal(t, "get_weather", parts[0].FunctionCall.Name)
	assert.Equal(t, "sf", parts[0].FunctionCall.Args["city"])
	assert.Equal(t, "STOP", got.Candidates[0].FinishReason)
}

func TestClaudeRequestToOpenAIRequestNilMetadata(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 128,
		Messages:  []ClaudeMessage{{Role: "user", Content: "hi"}},
	}
	got := ClaudeRequestToOpenAIRequest(req) // must not panic on nil Metadata
	require.NotNil(t, got)
	assert.Equal(t, "claude-3-5-sonnet-20241022", got.Model)
	require.Len(t, got.Messages, 1)
}
