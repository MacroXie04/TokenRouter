package relaycommon

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

func setupModelPolicyTest(t *testing.T) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "policy.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Option{}))
	model.DB = database
	require.NoError(t, setting.Init())
}

func TestPrepareGeminiRequestAppliesConvertedDefaults(t *testing.T) {
	setupModelPolicyTest(t)
	maximum := 2048
	request := &protocolkit.GeminiChatRequest{
		Contents: []protocolkit.GeminiChatContent{{Role: "model", Parts: []protocolkit.GeminiPart{{
			FunctionCall: &protocolkit.FunctionCall{Name: "lookup"},
		}}}},
		GenerationConfig: &protocolkit.GeminiChatGenerationConfig{MaxOutputTokens: &maximum},
	}

	modelName := PrepareGeminiRequest(request, "gemini-3-pro-image", "gemini-3-pro-image", true)

	assert.Equal(t, "gemini-3-pro-image", modelName)
	require.Len(t, request.SafetySettings, 4)
	for _, safety := range request.SafetySettings {
		assert.Equal(t, "OFF", safety.Threshold)
	}
	assert.Equal(t, []string{"TEXT", "IMAGE"}, request.GenerationConfig.ResponseModalities)
	assert.Equal(t, protocolkit.GeminiThoughtSignatureBypass, request.Contents[0].Parts[0].ThoughtSignature)
}

func TestPrepareGeminiRequestAttachesOneSyntheticSignaturePerModelTurn(t *testing.T) {
	setupModelPolicyTest(t)
	request := &protocolkit.GeminiChatRequest{Contents: []protocolkit.GeminiChatContent{
		{Role: "model", Parts: []protocolkit.GeminiPart{
			{Text: "planning"},
			{FunctionCall: &protocolkit.FunctionCall{Name: "first"}, ThoughtSignature: protocolkit.GeminiThoughtSignatureBypass},
			{FunctionCall: &protocolkit.FunctionCall{Name: "second"}, ThoughtSignature: protocolkit.GeminiThoughtSignatureBypass},
		}},
		{Role: "assistant", Parts: []protocolkit.GeminiPart{{Text: "text only"}}},
		{Role: "user", Parts: []protocolkit.GeminiPart{{FunctionCall: &protocolkit.FunctionCall{Name: "ignored"}}}},
	}}

	PrepareGeminiRequest(request, "gemini-custom", "gemini-custom", true)

	assert.Empty(t, request.Contents[0].Parts[0].ThoughtSignature)
	assert.Equal(t, protocolkit.GeminiThoughtSignatureBypass, request.Contents[0].Parts[1].ThoughtSignature)
	assert.Empty(t, request.Contents[0].Parts[2].ThoughtSignature)
	assert.Equal(t, protocolkit.GeminiThoughtSignatureBypass, request.Contents[1].Parts[0].ThoughtSignature)
	assert.Empty(t, request.Contents[2].Parts[0].ThoughtSignature)
}

func TestPrepareGeminiNativeBodyPreservesUnknownFieldsAndAppliesPolicies(t *testing.T) {
	setupModelPolicyTest(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.GeminiThinkingAdapterEnabledOption:   "true",
		setting.GeminiThinkingBudgetPercentageOption: "0.5",
	}))
	body := []byte(`{
		"contents":[{"parts":[{"functionResponse":{"id":"call-1","name":"lookup","response":{"ok":true}},"futurePart":7}]}],
		"generationConfig":{"maxOutputTokens":2000,"futureGeneration":"kept"},
		"futureTopLevel":{"enabled":true}
	}`)

	patched, modelName, err := PrepareGeminiNativeBody(body, "gemini-custom-thinking", "gemini-custom-thinking")
	require.NoError(t, err)
	assert.Equal(t, "gemini-custom", modelName)
	assert.JSONEq(t, `{"enabled":true}`, string(extractRawJSON(t, patched, "futureTopLevel")))

	var decoded map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(patched, &decoded))
	generation := decoded["generationConfig"].(map[string]any)
	assert.Equal(t, "kept", generation["futureGeneration"])
	thinking := generation["thinkingConfig"].(map[string]any)
	assert.Equal(t, float64(1000), thinking["thinkingBudget"])
	assert.Equal(t, true, thinking["includeThoughts"])
	part := decoded["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
	assert.Equal(t, float64(7), part["futurePart"])
	assert.NotContains(t, part["functionResponse"].(map[string]any), "id")

	disabled, modelName, err := PrepareGeminiNativeBody(
		[]byte(`{"contents":[],"generationConfig":{"maxOutputTokens":2000}}`),
		"gemini-custom-nothinking", "gemini-custom-nothinking",
	)
	require.NoError(t, err)
	assert.Equal(t, "gemini-custom", modelName)
	require.NoError(t, protocolkit.UnmarshalJSON(disabled, &decoded))
	thinking = decoded["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	assert.Equal(t, float64(0), thinking["thinkingBudget"], "explicit thinking-off must survive omitempty encoding")
}

func TestPrepareClaudeRequestAppliesDefaultAndThinkingVariant(t *testing.T) {
	setupModelPolicyTest(t)
	require.NoError(t, setting.UpdateOption(setting.ClaudeDefaultMaxTokensOption,
		`{"default":8192,"claude-custom":4096}`))
	request := &protocolkit.ClaudeRequest{Model: "claude-custom-thinking"}

	modelName := PrepareClaudeRequest(request, "claude-custom-thinking", "claude-custom-thinking", false)

	assert.Equal(t, "claude-custom", modelName)
	assert.Equal(t, "claude-custom", request.Model)
	assert.Equal(t, 4096, request.MaxTokens)
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "enabled", request.Thinking.Type)
	assert.Equal(t, 3276, request.Thinking.BudgetTokens)
	require.NotNil(t, request.Temperature)
	assert.Equal(t, 1.0, *request.Temperature)
}

func TestPrepareClaudeRequestPreservesConfiguredZero(t *testing.T) {
	setupModelPolicyTest(t)
	require.NoError(t, setting.UpdateOption(setting.ClaudeDefaultMaxTokensOption,
		`{"default":8192,"claude-cache":0}`))
	request := &protocolkit.ClaudeRequest{Model: "claude-cache"}

	modelName := PrepareClaudeRequest(request, "claude-cache", "claude-cache", false)

	assert.Equal(t, "claude-cache", modelName)
	assert.Equal(t, 0, request.MaxTokens)
	assert.Nil(t, request.Thinking)
}

func TestPrepareClaudeRequestAppliesAdaptiveThinkingVariants(t *testing.T) {
	setupModelPolicyTest(t)
	topP := 0.7
	topK := 3
	temperature := 0.4
	request := &protocolkit.ClaudeRequest{
		Model: "claude-opus-4-8-high", MaxTokens: 2048,
		Temperature: &temperature, TopP: &topP, TopK: &topK,
	}

	modelName := PrepareClaudeRequest(request, request.Model, request.Model, true)

	assert.Equal(t, "claude-opus-4-8", modelName)
	assert.Equal(t, "claude-opus-4-8", request.Model)
	require.NotNil(t, request.Thinking)
	assert.Equal(t, "adaptive", request.Thinking.Type)
	assert.Equal(t, "summarized", request.Thinking.Display)
	assert.JSONEq(t, `{"effort":"high"}`, string(request.OutputConfig))
	assert.Nil(t, request.Temperature)
	assert.Nil(t, request.TopP)
	assert.Nil(t, request.TopK)
}

func extractRawJSON(t *testing.T, document []byte, key string) []byte {
	t.Helper()
	var decoded map[string]json.RawMessage
	require.NoError(t, protocolkit.UnmarshalJSON(document, &decoded))
	return decoded[key]
}
