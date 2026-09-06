package setting

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestModelPolicyDefaultsAndAtomicPublication(t *testing.T) {
	setupAffinitySettingTest(t)

	defaults := GetModelPolicySetting()
	assert.Equal(t, "OFF", GetGeminiSafetyThreshold("HARM_CATEGORY_HATE_SPEECH"))
	assert.Equal(t, "v1", GetGeminiAPIVersion("gemini-1.0-pro"))
	assert.Equal(t, "v1beta", GetGeminiAPIVersion("gemini-new"))
	assert.True(t, GeminiModelSupportsImagine("gemini-3-pro-image"))
	assert.False(t, defaults.Gemini.ThinkingAdapterEnabled)
	assert.True(t, defaults.Gemini.FunctionCallThoughtSignatureEnabled)
	assert.True(t, defaults.Gemini.RemoveFunctionResponseIDEnabled)
	assert.Equal(t, DefaultClaudeMaxTokens, GetClaudeDefaultMaxTokens("claude-new"))
	assert.True(t, defaults.Claude.ThinkingAdapterEnabled)

	require.NoError(t, UpdateOptions(map[string]string{
		GeminiSafetySettingsOption:           `{"default":"BLOCK_ONLY_HIGH","HARM_CATEGORY_HATE_SPEECH":"BLOCK_NONE"}`,
		GeminiVersionSettingsOption:          `{"default":"v1","gemini-preview":"v1beta"}`,
		GeminiSupportedImagineModelsOption:   `["gemini-image-custom"]`,
		GeminiThinkingAdapterEnabledOption:   "true",
		GeminiThinkingBudgetPercentageOption: "0.4",
		ClaudeDefaultMaxTokensOption:         `{"default":4096,"claude-large":16384}`,
		ClaudeThinkingAdapterEnabledOption:   "false",
		ClaudeThinkingBudgetPercentageOption: "0.7",
	}))

	assert.Equal(t, "BLOCK_NONE", GetGeminiSafetyThreshold("HARM_CATEGORY_HATE_SPEECH"))
	assert.Equal(t, "BLOCK_ONLY_HIGH", GetGeminiSafetyThreshold("HARM_CATEGORY_DANGEROUS_CONTENT"))
	assert.Equal(t, "v1beta", GetGeminiAPIVersion("gemini-preview"))
	assert.Equal(t, "v1", GetGeminiAPIVersion("gemini-other"))
	assert.True(t, GeminiModelSupportsImagine("gemini-image-custom"))
	assert.False(t, GeminiModelSupportsImagine("gemini-3-pro-image"))
	assert.Equal(t, 16384, GetClaudeDefaultMaxTokens("claude-large"))
	assert.Equal(t, 4096, GetClaudeDefaultMaxTokens("claude-other"))

	before := GetModelPolicySetting()
	for _, update := range []map[string]string{
		{GeminiSafetySettingsOption: `{"default":"BLOCK_SOME"}`},
		{GeminiSafetySettingsOption: `{"default":"OFF","default":"BLOCK_NONE"}`},
		{GeminiVersionSettingsOption: `{"default":"v2"}`},
		{GeminiSupportedImagineModelsOption: `["duplicate","duplicate"]`},
		{GeminiThinkingBudgetPercentageOption: "0.001"},
		{GeminiFunctionCallThoughtSignatureOption: "yes"},
		{ClaudeDefaultMaxTokensOption: `{"default":-1}`},
		{ClaudeThinkingBudgetPercentageOption: "NaN"},
		{ClaudeModelHeadersSettingsOption: `{"claude-large":{"Authorization":["Bearer attacker"]}}`},
		{ClaudeModelHeadersSettingsOption: `{"claude-large":{"Anthropic-Beta":["bad\r\nvalue"]}}`},
		{ClaudeModelHeadersSettingsOption: `{"claude-large":{"X-Bad:Name":["value"]}}`},
		{ClaudeModelHeadersSettingsOption: `{"claude-large":{"Anthropic-Beta":["one"],"Anthropic-Beta":["two"]}}`},
		{ClaudeModelHeadersSettingsOption: `{"claude-large":{"Anthropic-Beta":["one"],"anthropic-beta":["two"]}}`},
		{GeminiSafetySettingsOption: strings.Repeat("x", maxModelPolicyOptionBytes+1)},
	} {
		require.Error(t, UpdateOptions(update), update)
		assert.Equal(t, before, GetModelPolicySetting(), "failed update must not publish any field")
	}
}

func TestClaudeConfiguredZeroMaxTokensIsPreserved(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(ClaudeDefaultMaxTokensOption, `{"default":8192,"claude-cache":0}`))
	assert.Equal(t, 0, GetClaudeDefaultMaxTokens("claude-cache"))
	assert.Equal(t, 8192, GetClaudeDefaultMaxTokens("claude-other"))
}

func TestClaudeModelHeadersAreBoundedMergedAndDetached(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(ClaudeModelHeadersSettingsOption, `{
		"claude-thinking":{"Anthropic-Beta":["token-efficient-tools","computer-use"],"X-Trace-Mode":["safe"]}
	}`))

	headers := http.Header{}
	headers.Add("anthropic-beta", "existing, token-efficient-tools")
	ApplyClaudeModelHeaders("claude-thinking", headers)
	assert.Equal(t, "existing,token-efficient-tools,computer-use", headers.Get("Anthropic-Beta"))
	assert.Equal(t, "safe", headers.Get("X-Trace-Mode"))

	copyOf := GetModelPolicySetting()
	copyOf.Claude.ModelHeadersSettings["claude-thinking"]["Anthropic-Beta"][0] = "mutated"
	copyOf.Claude.DefaultMaxTokens["default"] = 1
	copyOf.Gemini.SafetySettings["default"] = "BLOCK_NONE"
	assert.Equal(t, "existing,token-efficient-tools,computer-use", headers.Get("Anthropic-Beta"))
	assert.Equal(t, DefaultClaudeMaxTokens, GetClaudeDefaultMaxTokens("unknown"))
	assert.Equal(t, "OFF", GetGeminiSafetyThreshold("unknown"))
}

func TestModelPolicyRemoteSyncRetainsLastValidSnapshot(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOption(GeminiThinkingBudgetPercentageOption, "0.5"))
	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", GeminiThinkingBudgetPercentageOption).Update("value", "Infinity").Error)
	require.Error(t, Sync())
	assert.Equal(t, 0.5, GetModelPolicySetting().Gemini.ThinkingAdapterBudgetTokensPercentage)
	assert.Equal(t, "0.5", GetOption(GeminiThinkingBudgetPercentageOption))
}
