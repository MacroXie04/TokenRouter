package contract

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

var geminiSafetyCategories = []string{
	"HARM_CATEGORY_HARASSMENT",
	"HARM_CATEGORY_HATE_SPEECH",
	"HARM_CATEGORY_SEXUALLY_EXPLICIT",
	"HARM_CATEGORY_DANGEROUS_CONTENT",
}

// PrepareGeminiRequest applies one coherent model-policy snapshot to a typed
// request produced by protocol conversion. It returns the provider-visible
// model after any enabled virtual thinking suffix has been removed.
func PrepareGeminiRequest(request *protocolkit.GeminiChatRequest, originalModel, upstreamModel string, converted bool) string {
	policy := setting.GetModelPolicySetting().Gemini
	model, variant := geminiPolicyVariant(originalModel, upstreamModel, policy.ThinkingAdapterEnabled)
	if request == nil {
		return model
	}
	if converted {
		request.SafetySettings = request.SafetySettings[:0]
		for _, category := range geminiSafetyCategories {
			threshold := policy.SafetySettings[category]
			if threshold == "" {
				threshold = policy.SafetySettings["default"]
			}
			if threshold == "" {
				threshold = "OFF"
			}
			request.SafetySettings = append(request.SafetySettings, protocolkit.GeminiChatSafetySettings{
				Category: category, Threshold: threshold,
			})
		}
		if policy.FunctionCallThoughtSignatureEnabled {
			attachSyntheticGeminiThoughtSignatures(request)
		} else {
			removeSyntheticGeminiThoughtSignatures(request)
		}
		if geminiModelSupportsImagine(policy.SupportedImagineModels, model) {
			if request.GenerationConfig == nil {
				request.GenerationConfig = &protocolkit.GeminiChatGenerationConfig{}
			}
			request.GenerationConfig.ResponseModalities = []string{"TEXT", "IMAGE"}
		}
	}
	applyGeminiThinkingVariant(request, model, variant, policy.ThinkingAdapterBudgetTokensPercentage)
	return model
}

func geminiModelSupportsImagine(models []string, model string) bool {
	for _, supported := range models {
		if supported == model {
			return true
		}
	}
	return false
}

type thinkingVariant struct {
	kind   string
	budget int
	level  string
}

func geminiPolicyVariant(originalModel, upstreamModel string, enabled bool) (string, thinkingVariant) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if !enabled {
		return upstreamModel, thinkingVariant{}
	}
	if base, variant, ok := parseGeminiThinkingVariant(upstreamModel); ok {
		return base, variant
	}
	if _, variant, ok := parseGeminiThinkingVariant(strings.TrimSpace(originalModel)); ok {
		return upstreamModel, variant
	}
	return upstreamModel, thinkingVariant{}
}

func parseGeminiThinkingVariant(model string) (string, thinkingVariant, bool) {
	if strings.HasSuffix(model, "-nothinking") {
		base := strings.TrimSuffix(model, "-nothinking")
		return base, thinkingVariant{kind: "disabled"}, base != ""
	}
	if strings.HasSuffix(model, "-thinking") {
		base := strings.TrimSuffix(model, "-thinking")
		return base, thinkingVariant{kind: "enabled"}, base != ""
	}
	marker := strings.Index(model, "-thinking-")
	if marker <= 0 {
		for _, level := range []string{"max", "xhigh", "high", "medium", "low", "minimal"} {
			suffix := "-" + level
			if base := strings.TrimSuffix(model, suffix); base != model && base != "" {
				return base, thinkingVariant{kind: "level", level: level}, true
			}
		}
		return model, thinkingVariant{}, false
	}
	budget, err := strconv.Atoi(model[marker+len("-thinking-"):])
	if err != nil {
		return model[:marker], thinkingVariant{}, true
	}
	return model[:marker], thinkingVariant{kind: "budget", budget: budget}, true
}

func applyGeminiThinkingVariant(request *protocolkit.GeminiChatRequest, model string, variant thinkingVariant, ratio float64) {
	if variant.kind == "" || request == nil {
		return
	}
	if variant.kind == "disabled" && isGemini25ProThinkingModel(model) {
		return
	}
	if request.GenerationConfig == nil {
		request.GenerationConfig = &protocolkit.GeminiChatGenerationConfig{}
	}
	config := &protocolkit.GeminiThinkingConfig{}
	switch variant.kind {
	case "disabled":
		budget := 0
		config.ThinkingBudget = &budget
	case "budget":
		config.IncludeThoughts = true
		budget := clampGeminiThinkingBudget(model, variant.budget)
		config.ThinkingBudget = &budget
	case "enabled":
		config.IncludeThoughts = true
		if !geminiThinkingBudgetUnsupported(model) {
			maximum := request.GenerationConfig.MaxOutputTokens
			if maximum == nil || *maximum <= 0 {
				break
			}
			budget := int(math.Floor(float64(*maximum) * ratio))
			budget = clampGeminiThinkingBudget(model, budget)
			config.ThinkingBudget = &budget
		}
	case "level":
		config.IncludeThoughts = true
		config.ThinkingLevel = variant.level
	}
	request.GenerationConfig.ThinkingConfig = config
}

func isGemini25ProThinkingModel(model string) bool {
	return strings.HasPrefix(model, "gemini-2.5-pro") && !geminiThinkingBudgetUnsupported(model)
}

func geminiThinkingBudgetUnsupported(model string) bool {
	return strings.HasPrefix(model, "gemini-2.5-pro-preview-05-06") ||
		strings.HasPrefix(model, "gemini-2.5-pro-preview-03-25")
}

func clampGeminiThinkingBudget(model string, budget int) int {
	minimum, maximum := 0, 24_576
	if strings.HasPrefix(model, "gemini-2.5-flash-lite") {
		minimum = 512
	} else if isGemini25ProThinkingModel(model) {
		minimum, maximum = 128, 32_768
	}
	if budget < minimum {
		return minimum
	}
	if budget > maximum {
		return maximum
	}
	return budget
}

func attachSyntheticGeminiThoughtSignatures(request *protocolkit.GeminiChatRequest) {
	for contentIndex := range request.Contents {
		content := &request.Contents[contentIndex]
		if content.Role != "model" && content.Role != "assistant" {
			continue
		}
		attached := false
		firstText := -1
		for partIndex := range content.Parts {
			part := &content.Parts[partIndex]
			if part.ThoughtSignature == protocolkit.GeminiThoughtSignatureBypass {
				part.ThoughtSignature = ""
			}
			if firstText < 0 && part.Text != "" && part.ThoughtSignature == "" {
				firstText = partIndex
			}
			if !attached && part.FunctionCall != nil &&
				(strings.TrimSpace(part.FunctionCall.Name) != "" || len(part.FunctionCall.Args) > 0) && part.ThoughtSignature == "" {
				part.ThoughtSignature = protocolkit.GeminiThoughtSignatureBypass
				attached = true
			}
		}
		if !attached && firstText >= 0 {
			content.Parts[firstText].ThoughtSignature = protocolkit.GeminiThoughtSignatureBypass
		}
	}
}

func removeSyntheticGeminiThoughtSignatures(request *protocolkit.GeminiChatRequest) {
	for contentIndex := range request.Contents {
		for partIndex := range request.Contents[contentIndex].Parts {
			part := &request.Contents[contentIndex].Parts[partIndex]
			if part.ThoughtSignature == protocolkit.GeminiThoughtSignatureBypass {
				part.ThoughtSignature = ""
			}
		}
	}
}

// PrepareClaudeRequest applies one coherent Claude policy snapshot: configured
// output-token defaults plus the reference virtual-model thinking contracts.
// A missing or zero-valued request limit resolves through the configured
// model/default map; an explicitly configured zero remains zero for cache
// pre-warming, while 8192 is used only when neither configured key exists.
func PrepareClaudeRequest(request *protocolkit.ClaudeRequest, originalModel, upstreamModel string, limitProvided bool) string {
	policy := setting.GetModelPolicySetting().Claude
	model, variant := claudePolicyVariant(originalModel, upstreamModel, policy.ThinkingAdapterEnabled)
	if request == nil {
		return model
	}
	request.Model = model
	if !limitProvided || request.MaxTokens == 0 {
		request.MaxTokens = claudeDefaultMaxTokens(policy.DefaultMaxTokens, model)
	}
	switch variant.kind {
	case "effort":
		request.Thinking = &protocolkit.Thinking{Type: "adaptive"}
		request.OutputConfig = claudeEffortOutputConfig(variant.effort)
		if claudeUsesAdaptiveDisplay(model) {
			request.Thinking.Display = "summarized"
			request.Temperature = nil
			request.TopP = nil
			request.TopK = nil
		} else {
			one := 1.0
			request.Temperature = &one
			request.TopP = nil
		}
	case "thinking":
		if request.Thinking != nil {
			break
		}
		if claudeUsesAdaptiveDisplay(model) {
			request.Thinking = &protocolkit.Thinking{Type: "adaptive", Display: "summarized"}
			request.OutputConfig = claudeEffortOutputConfig("high")
			request.Temperature = nil
			request.TopP = nil
			request.TopK = nil
			break
		}
		if request.MaxTokens < 1280 {
			request.MaxTokens = 1280
		}
		budget := int(math.Floor(float64(request.MaxTokens) * policy.ThinkingAdapterBudgetTokensPercentage))
		request.Thinking = &protocolkit.Thinking{Type: "enabled", BudgetTokens: budget}
		one := 1.0
		request.Temperature = &one
		request.TopP = nil
	}
	return model
}

type claudeThinkingVariant struct {
	kind   string
	effort string
}

func claudePolicyVariant(originalModel, upstreamModel string, enabled bool) (string, claudeThinkingVariant) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if base, variant, ok := parseClaudeThinkingVariant(upstreamModel, enabled); ok {
		return base, variant
	}
	if _, variant, ok := parseClaudeThinkingVariant(strings.TrimSpace(originalModel), enabled); ok {
		return upstreamModel, variant
	}
	return upstreamModel, claudeThinkingVariant{}
}

func parseClaudeThinkingVariant(model string, enabled bool) (string, claudeThinkingVariant, bool) {
	for _, effort := range []string{"max", "xhigh", "high", "medium", "low", "minimal"} {
		suffix := "-" + effort
		base := strings.TrimSuffix(model, suffix)
		if base != model && (strings.HasPrefix(base, "claude-opus-4-6") ||
			strings.HasPrefix(base, "claude-opus-4-7") || strings.HasPrefix(base, "claude-opus-4-8")) {
			return base, claudeThinkingVariant{kind: "effort", effort: effort}, true
		}
	}
	if enabled && strings.HasSuffix(model, "-thinking") {
		base := strings.TrimSuffix(model, "-thinking")
		if base != "" {
			return base, claudeThinkingVariant{kind: "thinking"}, true
		}
	}
	return model, claudeThinkingVariant{}, false
}

func claudeUsesAdaptiveDisplay(model string) bool {
	return strings.HasPrefix(model, "claude-opus-4-7") || strings.HasPrefix(model, "claude-opus-4-8")
}

func claudeEffortOutputConfig(effort string) json.RawMessage {
	return json.RawMessage(`{"effort":"` + effort + `"}`)
}

func claudeDefaultMaxTokens(defaults map[string]int, model string) int {
	if tokens, exists := defaults[model]; exists {
		return tokens
	}
	if tokens, exists := defaults["default"]; exists {
		return tokens
	}
	return setting.DefaultClaudeMaxTokens
}

// PrepareGeminiNativeBody applies native-request policy by patching only the
// affected RawMessage branches. Provider-specific fields unknown to this
// binary remain intact.
func PrepareGeminiNativeBody(body []byte, originalModel, upstreamModel string) ([]byte, string, error) {
	policy := setting.GetModelPolicySetting().Gemini
	model, variant := geminiPolicyVariant(originalModel, upstreamModel, policy.ThinkingAdapterEnabled)
	var envelope map[string]json.RawMessage
	if err := protocolkit.UnmarshalJSON(body, &envelope); err != nil {
		return nil, model, err
	}
	if envelope == nil {
		return nil, model, errors.New("Gemini request must be a JSON object")
	}
	changed := false

	if variant.kind != "" {
		var typed protocolkit.GeminiChatRequest
		if err := protocolkit.UnmarshalJSON(body, &typed); err != nil {
			return nil, model, err
		}
		var originalThinking *protocolkit.GeminiThinkingConfig
		if typed.GenerationConfig != nil {
			originalThinking = typed.GenerationConfig.ThinkingConfig
		}
		applyGeminiThinkingVariant(&typed, model, variant, policy.ThinkingAdapterBudgetTokensPercentage)
		if typed.GenerationConfig != nil && typed.GenerationConfig.ThinkingConfig != nil &&
			typed.GenerationConfig.ThinkingConfig != originalThinking {
			generation := make(map[string]json.RawMessage)
			if rawGeneration, exists := envelope["generationConfig"]; exists {
				if err := protocolkit.UnmarshalJSON(rawGeneration, &generation); err != nil {
					return nil, model, err
				}
			}
			thinking, err := protocolkit.MarshalJSON(typed.GenerationConfig.ThinkingConfig)
			if err != nil {
				return nil, model, err
			}
			generation["thinkingConfig"] = thinking
			encoded, err := protocolkit.MarshalJSON(generation)
			if err != nil {
				return nil, model, err
			}
			envelope["generationConfig"] = encoded
			changed = true
		}
	}

	if !policy.RemoveFunctionResponseIDEnabled {
		if !changed {
			return body, model, nil
		}
		encoded, err := protocolkit.MarshalJSON(envelope)
		return encoded, model, err
	}
	rawContents, exists := envelope["contents"]
	if !exists {
		if !changed {
			return body, model, nil
		}
		encoded, err := protocolkit.MarshalJSON(envelope)
		return encoded, model, err
	}
	var contents []map[string]json.RawMessage
	if err := protocolkit.UnmarshalJSON(rawContents, &contents); err != nil {
		return nil, model, err
	}
	for contentIndex := range contents {
		rawParts, exists := contents[contentIndex]["parts"]
		if !exists {
			continue
		}
		var parts []map[string]json.RawMessage
		if err := protocolkit.UnmarshalJSON(rawParts, &parts); err != nil {
			return nil, model, err
		}
		partsChanged := false
		for partIndex := range parts {
			for _, field := range []string{"functionResponse", "function_response"} {
				rawResponse, exists := parts[partIndex][field]
				if !exists {
					continue
				}
				var response map[string]json.RawMessage
				if err := protocolkit.UnmarshalJSON(rawResponse, &response); err != nil {
					return nil, model, err
				}
				if _, exists := response["id"]; !exists {
					continue
				}
				delete(response, "id")
				encoded, err := protocolkit.MarshalJSON(response)
				if err != nil {
					return nil, model, err
				}
				parts[partIndex][field] = encoded
				partsChanged = true
				changed = true
			}
		}
		if partsChanged {
			encoded, err := protocolkit.MarshalJSON(parts)
			if err != nil {
				return nil, model, err
			}
			contents[contentIndex]["parts"] = encoded
		}
	}
	if !changed {
		return body, model, nil
	}
	encoded, err := protocolkit.MarshalJSON(contents)
	if err != nil {
		return nil, model, err
	}
	envelope["contents"] = encoded
	encoded, err = protocolkit.MarshalJSON(envelope)
	return encoded, model, err
}

// PatchGeminiNativeBody retains a small compatibility helper for callers that
// only need the function-response sanitization policy.
func PatchGeminiNativeBody(body []byte) ([]byte, error) {
	patched, _, err := PrepareGeminiNativeBody(body, "", "")
	return patched, err
}
