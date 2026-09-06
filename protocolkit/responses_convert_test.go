package protocolkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatRequestToResponsesMapPreservesInstructionsMediaAndTools(t *testing.T) {
	maxTokens := 128
	request := &GeneralOpenAIRequest{
		Model: "client-model", Stream: true, MaxCompletionTokens: &maxTokens,
		Messages: []Message{
			{Role: "system", Content: "system rules"},
			{Role: "developer", Content: "developer rules"},
			{Role: "user", Content: []MediaContent{
				{Type: ContentTypeText, Text: "look"},
				{Type: ContentTypeImageURL, ImageURL: &MessageImageUrl{Url: "https://example.test/image.png"}},
			}},
			{Role: "assistant", Content: nil, ToolCalls: []ToolCallRequest{{
				Id: "call-1", Type: "function", Function: &FunctionRequest{Name: "lookup", Arguments: `{"q":"x"}`},
			}}},
			{Role: "tool", ToolCallId: "call-1", Content: "result"},
		},
		Tools: []ToolCallRequest{{Type: "function", Function: &FunctionRequest{
			Name: "lookup", Description: "Lookup", Parameters: map[string]any{"type": "object"},
		}}},
		Extra: map[string]any{"group": "dashboard-only", "service_tier": "auto"},
	}
	body, err := OpenAIChatRequestToResponsesMap(request, "mapped-model")
	require.NoError(t, err)
	assert.Equal(t, "mapped-model", body["model"])
	assert.Equal(t, "system rules\n\ndeveloper rules", body["instructions"])
	assert.Equal(t, float64(128), genericValue(body["max_output_tokens"]))
	assert.NotContains(t, body, "group")
	assert.Equal(t, "auto", body["service_tier"])

	input := body["input"].([]any)
	require.Len(t, input, 3)
	user := input[0].(map[string]any)
	parts := user["content"].([]any)
	assert.Equal(t, "input_text", parts[0].(map[string]any)["type"])
	assert.Equal(t, "input_image", parts[1].(map[string]any)["type"])
	assert.Equal(t, "https://example.test/image.png", parts[1].(map[string]any)["image_url"])
	assert.Equal(t, "function_call", input[1].(map[string]any)["type"])
	assert.Equal(t, "function_call_output", input[2].(map[string]any)["type"])
}

func TestResponsesMapToOpenAIChatRequestPreservesFunctionsAndExplicitZero(t *testing.T) {
	source := map[string]any{
		"model": "client-model", "instructions": "be helpful", "stream": true, "max_output_tokens": float64(0),
		"input": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "look"},
				map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"},
			}},
			map[string]any{"type": "function_call", "call_id": "call-1", "name": "lookup", "arguments": `{"q":"x"}`},
			map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "result"},
		},
		"tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}},
	}
	request, err := ResponsesMapToOpenAIChatRequest(source, "mapped-model")
	require.NoError(t, err)
	assert.Equal(t, "mapped-model", request.Model)
	assert.True(t, request.Stream)
	require.NotNil(t, request.MaxCompletionTokens)
	assert.Zero(t, *request.MaxCompletionTokens)
	require.Len(t, request.Messages, 4)
	assert.Equal(t, "system", request.Messages[0].Role)
	assert.Equal(t, "user", request.Messages[1].Role)
	assert.Equal(t, "assistant", request.Messages[2].Role)
	require.Len(t, request.Messages[2].ToolCalls, 1)
	assert.Equal(t, "tool", request.Messages[3].Role)
	assert.Equal(t, "call-1", request.Messages[3].ToolCallId)
	require.Len(t, request.Tools, 1)
}

func TestResponsesAndChatResponseConversionPreservesUsageAndToolCalls(t *testing.T) {
	responses := &OpenAIResponsesResponse{
		Id: "resp-1", Object: "response", Status: "completed", Model: "model",
		Output: []ResponsesOutput{
			{Type: "message", Content: []ResponsesOutputContent{{Type: "output_text", Text: "answer"}}},
			{Type: "function_call", CallId: "call-1", Name: "lookup", Arguments: `{"q":"x"}`},
		},
		Usage: &ResponsesUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
	}
	chat := ResponsesResponseToOpenAIChat(responses)
	require.Len(t, chat.Choices, 1)
	assert.Equal(t, "tool_calls", chat.Choices[0].FinishReason)
	assert.Equal(t, "answer", chat.Choices[0].Message.Content)
	require.Len(t, chat.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, 7, chat.Usage.PromptTokens)
	assert.Equal(t, 3, chat.Usage.CompletionTokens)

	roundTrip := OpenAIChatResponseToResponses(chat)
	assert.Equal(t, "completed", roundTrip.Status)
	assert.Equal(t, 7, roundTrip.Usage.InputTokens)
	assert.Equal(t, 3, roundTrip.Usage.OutputTokens)
	assert.Len(t, roundTrip.Output, 2)
}

func TestResponsesToGeminiFunctionHistoryCarriesThoughtSignature(t *testing.T) {
	request, err := ResponsesMapToOpenAIChatRequest(map[string]any{
		"model": "gemini-test",
		"input": []any{
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"type": "function_call", "call_id": "call-1", "name": "lookup", "arguments": map[string]any{"q": "x"}},
			map[string]any{"type": "function_call_output", "call_id": "call-1", "output": map[string]any{"value": "ok"}},
		},
	}, "gemini-test")
	require.NoError(t, err)

	gemini := OpenAIRequestToGeminiRequest(request)
	require.Len(t, gemini.Contents, 3)
	require.Len(t, gemini.Contents[1].Parts, 1)
	require.NotNil(t, gemini.Contents[1].Parts[0].FunctionCall)
	assert.Equal(t, GeminiThoughtSignatureBypass, gemini.Contents[1].Parts[0].ThoughtSignature)
	require.Len(t, gemini.Contents[2].Parts, 1)
	require.NotNil(t, gemini.Contents[2].Parts[0].FunctionResponse)
	assert.Empty(t, gemini.Contents[2].Parts[0].ThoughtSignature)
}
