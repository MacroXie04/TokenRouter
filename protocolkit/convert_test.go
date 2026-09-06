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
		CacheCreation: &ClaudeCacheCreation{
			Ephemeral5mInputTokens: 40,
			Ephemeral1hInputTokens: 60,
		},
		OutputTokens: 50,
	}
	got := ClaudeUsageToOpenAIUsage(u)
	// prompt_tokens must include cache (OpenAI convention).
	assert.Equal(t, 600, got.PromptTokens)
	assert.Equal(t, 50, got.CompletionTokens)
	assert.Equal(t, 650, got.TotalTokens)
	assert.Equal(t, "anthropic", got.BillingSemantic)
	assert.Equal(t, 300, got.PromptCacheHitTokens)
	assert.Equal(t, 200, got.PromptCacheMissTokens)
	assert.Equal(t, 140, got.PromptCacheCreation5mTokens)
	assert.Equal(t, 60, got.PromptCacheCreation1hTokens)
	require.NotNil(t, got.PromptTokensDetails)
	assert.Equal(t, 300, got.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 140, got.PromptTokensDetails.CacheCreation5mTokens)
	assert.Equal(t, 60, got.PromptTokensDetails.CacheCreation1hTokens)
}

func TestClaudeUsageToOpenAIUsageUsesTTLBreakdownWhenLegacyTotalIsMissing(t *testing.T) {
	got := ClaudeUsageToOpenAIUsage(&ClaudeUsage{
		InputTokens: 11,
		CacheCreation: &ClaudeCacheCreation{
			Ephemeral5mInputTokens: 3,
			Ephemeral1hInputTokens: 7,
		},
		OutputTokens: 2,
	})

	assert.Equal(t, 21, got.PromptTokens)
	assert.Equal(t, 23, got.TotalTokens)
	assert.Equal(t, 10, got.PromptCacheCreationTokens)
	assert.Equal(t, 3, got.PromptCacheCreation5mTokens)
	assert.Equal(t, 7, got.PromptCacheCreation1hTokens)
}

func TestGeminiUsageToOpenAIUsage(t *testing.T) {
	meta := &GeminiUsageMetadata{
		PromptTokenCount:        120,
		ToolUsePromptTokenCount: 7,
		CandidatesTokenCount:    30,
		ThoughtsTokenCount:      3,
		CachedContentTokenCount: 11,
		TotalTokenCount:         160,
		PromptTokensDetails: []GeminiPromptTokensDetails{
			{Modality: "audio", TokenCount: 20},
			{Modality: "IMAGE", TokenCount: 13},
		},
		ToolUsePromptTokensDetails: []GeminiPromptTokensDetails{{Modality: "AUDIO", TokenCount: 7}},
		CandidatesTokensDetails: []GeminiCandidatesTokensDetails{
			{Modality: "IMAGE", TokenCount: 5},
			{Modality: "AUDIO", TokenCount: 9},
		},
	}
	got := GeminiUsageToOpenAIUsage(meta)
	assert.Equal(t, 127, got.PromptTokens)
	assert.Equal(t, 33, got.CompletionTokens)
	assert.Equal(t, 160, got.TotalTokens)
	assert.Equal(t, 27, got.AudioTokens)
	require.NotNil(t, got.PromptTokensDetails)
	assert.Equal(t, 11, got.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 13, got.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 27, got.PromptTokensDetails.AudioTokens)
	require.NotNil(t, got.CompletionTokensDetails)
	assert.Equal(t, 5, got.CompletionTokensDetails.ImageTokens)
	assert.Equal(t, 9, got.CompletionTokensDetails.AudioTokens)
	assert.Equal(t, 3, got.CompletionTokensDetails.ReasoningTokens)
}

func TestGeminiUsageToOpenAIUsageFailsClosedOnOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	got := GeminiUsageToOpenAIUsage(&GeminiUsageMetadata{
		PromptTokenCount: maxInt, ToolUsePromptTokenCount: 1,
		PromptTokensDetails: []GeminiPromptTokensDetails{
			{Modality: "IMAGE", TokenCount: maxInt},
			{Modality: "IMAGE", TokenCount: 1},
		},
	})
	assert.Equal(t, -1, got.PromptTokens)
	assert.Equal(t, -1, got.TotalTokens)
	require.NotNil(t, got.PromptTokensDetails)
	assert.Equal(t, -1, got.PromptTokensDetails.ImageTokens)
}

func TestNormalizeOpenAIUsageAliasesUsesEachBucketOnce(t *testing.T) {
	u := &Usage{
		PromptTokens: 10, CompletionTokens: 8,
		InputTokens: 99, OutputTokens: 77,
		PromptTokensDetails:     &InputTokenDetails{ImageTokens: 2},
		InputTokensDetails:      &InputTokenDetails{ImageTokens: 20, AudioTokens: 3},
		CompletionTokensDetails: &OutputTokenDetails{AudioTokens: 4},
		OutputTokensDetails:     &OutputTokenDetails{AudioTokens: 40, ImageTokens: 5},
	}
	got := NormalizeOpenAIUsageAliases(u)
	assert.Equal(t, 10, got.PromptTokens, "canonical aggregate wins over its alias")
	assert.Equal(t, 8, got.CompletionTokens, "canonical aggregate wins over its alias")
	assert.Equal(t, 18, got.TotalTokens)
	assert.Equal(t, 2, got.PromptTokensDetails.ImageTokens, "canonical detail is not added to its alias")
	assert.Equal(t, 3, got.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 4, got.CompletionTokensDetails.AudioTokens, "canonical detail is not added to its alias")
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

func TestClaudeToolResponseToOpenAIPreservesIdentityArgumentsAndText(t *testing.T) {
	resp := &ClaudeResponse{
		Id: "msg_tools", Model: "claude-sonnet", StopReason: "tool_use",
		Content: []ClaudeMediaMessage{
			{Type: "text", Text: "I will check."},
			{Type: "tool_use", ID: "toolu_1", Name: "get_weather", Input: map[string]any{"city": "sf"}},
		},
		Usage: &ClaudeUsage{InputTokens: 4, OutputTokens: 3},
	}
	got := ClaudeResponseToOpenAIResponse(resp)
	require.Len(t, got.Choices, 1)
	message := got.Choices[0].Message
	require.NotNil(t, message)
	assert.Equal(t, "I will check.", message.Content)
	require.Len(t, message.ToolCalls, 1)
	assert.Equal(t, "toolu_1", message.ToolCalls[0].Id)
	require.NotNil(t, message.ToolCalls[0].Function)
	assert.Equal(t, "get_weather", message.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"city":"sf"}`, message.ToolCalls[0].Function.Arguments)
	assert.Equal(t, "tool_calls", got.Choices[0].FinishReason)
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

func TestOpenAIRequestToClaudePreservesToolCallsResultsAndSchema(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Model: "claude-sonnet",
		Messages: []Message{
			{Role: "assistant", Content: "checking", ToolCalls: []ToolCallRequest{{
				Id: "toolu_1", Type: "function",
				Function: &FunctionRequest{Name: "get_weather", Arguments: `{"city":"sf"}`},
			}}},
			{Role: "tool", ToolCallId: "toolu_1", Content: `{"temperature":72}`},
		},
		Tools: []ToolCallRequest{{Type: "function", Function: &FunctionRequest{
			Name: "get_weather", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required": []any{"city"},
			},
		}}},
	}
	got := OpenAIRequestToClaudeRequest(req)
	require.Len(t, got.Messages, 2)
	assistantParts, ok := got.Messages[0].Content.([]any)
	require.True(t, ok)
	require.Len(t, assistantParts, 2)
	toolUse, ok := assistantParts[1].(ClaudeMediaMessage)
	require.True(t, ok)
	assert.Equal(t, "tool_use", toolUse.Type)
	assert.Equal(t, "toolu_1", toolUse.ID)
	assert.Equal(t, "get_weather", toolUse.Name)
	assert.Equal(t, map[string]any{"city": "sf"}, toolUse.Input)
	resultParts, ok := got.Messages[1].Content.([]any)
	require.True(t, ok)
	require.Len(t, resultParts, 1)
	toolResult, ok := resultParts[0].(ClaudeMediaMessage)
	require.True(t, ok)
	assert.Equal(t, "user", got.Messages[1].Role)
	assert.Equal(t, "tool_result", toolResult.Type)
	assert.Equal(t, "toolu_1", toolResult.ToolUseID)

	require.Len(t, got.Tools, 1)
	require.NotNil(t, got.Tools[0].InputSchema)
	assert.Equal(t, "object", got.Tools[0].InputSchema.Type)
	assert.Contains(t, got.Tools[0].InputSchema.Properties, "city")
	assert.Equal(t, []string{"city"}, got.Tools[0].InputSchema.Required)
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

func TestOpenAIRequestToGeminiPreservesToolCallAndResult(t *testing.T) {
	req := &GeneralOpenAIRequest{Model: "gemini-pro", Messages: []Message{
		{Role: "assistant", ToolCalls: []ToolCallRequest{{
			Id: "call_weather", Type: "function",
			Function: &FunctionRequest{Name: "get_weather", Arguments: `{"city":"sf"}`},
		}}},
		{Role: "tool", ToolCallId: "call_weather", Content: `{"temperature":72}`},
	}}
	got := OpenAIRequestToGeminiRequest(req)
	require.Len(t, got.Contents, 2)
	require.Len(t, got.Contents[0].Parts, 1)
	require.NotNil(t, got.Contents[0].Parts[0].FunctionCall)
	assert.Equal(t, "model", got.Contents[0].Role)
	assert.Equal(t, "get_weather", got.Contents[0].Parts[0].FunctionCall.Name)
	assert.Equal(t, "sf", got.Contents[0].Parts[0].FunctionCall.Args["city"])
	assert.Equal(t, GeminiThoughtSignatureBypass, got.Contents[0].Parts[0].ThoughtSignature)
	require.Len(t, got.Contents[1].Parts, 1)
	require.NotNil(t, got.Contents[1].Parts[0].FunctionResponse)
	assert.Equal(t, "user", got.Contents[1].Role)
	assert.Equal(t, "get_weather", got.Contents[1].Parts[0].FunctionResponse.Name)
}

func TestGeminiRequestToOpenAIPreservesToolCallAndResult(t *testing.T) {
	req := &GeminiChatRequest{Model: "gemini-pro", Contents: []GeminiChatContent{
		{
			Role: "model",
			Parts: []GeminiPart{{FunctionCall: &FunctionCall{
				Name: "get_weather", Args: map[string]any{"city": "sf"},
			}}},
		},
		{
			Role: "user",
			Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResponse{
				Name: "get_weather", Response: map[string]any{"temperature": 72},
			}}},
		},
	}}
	got := GeminiRequestToOpenAIRequest(req)
	require.Len(t, got.Messages, 2)
	require.Len(t, got.Messages[0].ToolCalls, 1)
	assert.Equal(t, "call_get_weather", got.Messages[0].ToolCalls[0].Id)
	assert.Equal(t, "get_weather", got.Messages[0].ToolCalls[0].Function.Name)
	assert.Equal(t, "tool", got.Messages[1].Role)
	assert.Equal(t, "call_get_weather", got.Messages[1].ToolCallId)
	assert.JSONEq(t, `{"temperature":72}`, got.Messages[1].Content.(string))
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
