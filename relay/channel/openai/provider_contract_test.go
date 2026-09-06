package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func providerMeta(channelType constant.ChannelType, mode constant.RelayMode, modelName string) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(channelType), OpenAIOrganization: "must-not-leak"},
		Mode:      mode,
		ModelName: modelName,
		APIKey:    "provider-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model:    "client-model",
			Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
	}
}

func decodeProviderBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(body, &decoded))
	return decoded
}

func TestProviderDefaultURLsAndHeaders(t *testing.T) {
	for _, test := range []struct {
		name        string
		channelType constant.ChannelType
		expectedURL string
		openRouter  bool
	}{
		{name: "OpenRouter", channelType: constant.ChannelTypeOpenRouter, expectedURL: "https://openrouter.ai/api/v1/chat/completions", openRouter: true},
		{name: "DeepSeek", channelType: constant.ChannelTypeDeepSeek, expectedURL: "https://api.deepseek.com/v1/chat/completions"},
		{name: "Mistral", channelType: constant.ChannelTypeMistral, expectedURL: "https://api.mistral.ai/v1/chat/completions"},
		{name: "xAI", channelType: constant.ChannelTypeXai, expectedURL: "https://api.x.ai/v1/chat/completions"},
		{name: "SiliconFlow", channelType: constant.ChannelTypeSiliconFlow, expectedURL: "https://api.siliconflow.cn/v1/chat/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := providerMeta(test.channelType, constant.RelayModeChatCompletions, "upstream-model")
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.expectedURL, requestURL)

			req, err := http.NewRequest(http.MethodPost, requestURL, nil)
			require.NoError(t, err)
			require.NoError(t, adaptor.SetupRequestHeader(req, meta))
			assert.Equal(t, "Bearer provider-secret", req.Header.Get("Authorization"))
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Empty(t, req.Header.Get("OpenAI-Organization"))
			if test.openRouter {
				assert.Equal(t, openRouterReferer, req.Header.Get("HTTP-Referer"))
				assert.Equal(t, openRouterTitle, req.Header.Get("X-OpenRouter-Title"))
			} else {
				assert.Empty(t, req.Header.Get("HTTP-Referer"))
				assert.Empty(t, req.Header.Get("X-OpenRouter-Title"))
			}
		})
	}

	deepSeek := providerMeta(constant.ChannelTypeDeepSeek, constant.RelayModeCompletions, "deepseek-chat")
	adaptor := &Adaptor{}
	adaptor.Init(deepSeek)
	url, err := adaptor.GetRequestURL(deepSeek)
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepseek.com/beta/completions", url)
	deepSeek.BaseURL = "https://proxy.example/beta"
	url, err = adaptor.GetRequestURL(deepSeek)
	require.NoError(t, err)
	assert.Equal(t, "https://proxy.example/beta/completions", url)

	deepSeek.Mode = constant.RelayModeResponses
	adaptor.Init(deepSeek)
	deepSeek.BaseURL = "https://api.deepseek.com"
	url, err = adaptor.GetRequestURL(deepSeek)
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepseek.com/responses", url)

	customV1 := providerMeta(constant.ChannelTypeMistral, constant.RelayModeChatCompletions, "mistral-large")
	customV1.BaseURL = "https://proxy.example/v1/"
	adaptor.Init(customV1)
	url, err = adaptor.GetRequestURL(customV1)
	require.NoError(t, err)
	assert.Equal(t, "https://proxy.example/v1/chat/completions", url)

	customExact := providerMeta(constant.ChannelTypeCustom, constant.RelayModeChatCompletions, "tenant/deployment")
	customExact.BaseURL = "https://custom.example/invoke/{model}?version=stable"
	adaptor.Init(customExact)
	url, err = adaptor.GetRequestURL(customExact)
	require.NoError(t, err)
	assert.Equal(t, "https://custom.example/invoke/tenant/deployment?version=stable", url)
	customExact.BaseURL = ""
	_, err = adaptor.GetRequestURL(customExact)
	assert.Error(t, err)
}

func TestNarrowOpenAIProviderURLsAndModes(t *testing.T) {
	for _, test := range []struct {
		name        string
		channelType constant.ChannelType
		mode        constant.RelayMode
		expectedURL string
	}{
		{name: "Perplexity chat", channelType: constant.ChannelTypePerplexity, mode: constant.RelayModeChatCompletions, expectedURL: "https://api.perplexity.ai/chat/completions"},
		{name: "Perplexity responses", channelType: constant.ChannelTypePerplexity, mode: constant.RelayModeResponses, expectedURL: "https://api.perplexity.ai/v1/responses"},
		{name: "Jina rerank", channelType: constant.ChannelTypeJina, mode: constant.RelayModeRerank, expectedURL: "https://api.jina.ai/v1/rerank"},
		{name: "Jina embeddings", channelType: constant.ChannelTypeJina, mode: constant.RelayModeEmbeddings, expectedURL: "https://api.jina.ai/v1/embeddings"},
		{name: "Submodel chat", channelType: constant.ChannelTypeSubmodel, mode: constant.RelayModeChatCompletions, expectedURL: "https://llm.submodel.ai/v1/chat/completions"},
		{name: "Submodel completions", channelType: constant.ChannelTypeSubmodel, mode: constant.RelayModeCompletions, expectedURL: "https://llm.submodel.ai/v1/completions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := providerMeta(test.channelType, test.mode, "mapped-model")
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.expectedURL, requestURL)

			req, err := http.NewRequest(http.MethodPost, requestURL, nil)
			require.NoError(t, err)
			require.NoError(t, adaptor.SetupRequestHeader(req, meta))
			assert.Equal(t, "Bearer provider-secret", req.Header.Get("Authorization"))
			assert.Empty(t, req.Header.Get("OpenAI-Organization"))
		})
	}

	for _, test := range []struct {
		channelType constant.ChannelType
		mode        constant.RelayMode
	}{
		{channelType: constant.ChannelTypePerplexity, mode: constant.RelayModeEmbeddings},
		{channelType: constant.ChannelTypeJina, mode: constant.RelayModeChatCompletions},
		{channelType: constant.ChannelTypeSubmodel, mode: constant.RelayModeResponses},
	} {
		meta := providerMeta(test.channelType, test.mode, "mapped-model")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err)
		_, err = adaptor.ConvertRequest(meta)
		assert.Error(t, err)
	}
}

func TestPerplexityRequestConversion(t *testing.T) {
	meta := providerMeta(constant.ChannelTypePerplexity, constant.RelayModeChatCompletions, "sonar-pro")
	meta.IsStream = true
	meta.Request.Extra = map[string]any{
		"model": "client-model", "stream": true, "top_p": 1.0,
		"max_tokens": 17, "max_completion_tokens": 29,
		"messages": []any{map[string]any{
			"role": "user", "content": "hello", "name": "must-strip",
			"tool_calls": []any{map[string]any{"id": "must-strip"}},
		}},
		"search_domain_filter": []any{"example.test"}, "search_recency_filter": "week",
		"return_images": true, "return_related_questions": false, "search_mode": "academic",
		"tools": []any{map[string]any{"type": "function"}}, "provider_extension": "must-strip", "group": "dashboard-only",
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeProviderBody(t, body)
	assert.Equal(t, "sonar-pro", decoded["model"])
	assert.InDelta(t, 0.99, decoded["top_p"], 0.0001)
	assert.EqualValues(t, 29, decoded["max_tokens"])
	assert.NotContains(t, decoded, "max_completion_tokens")
	assert.Equal(t, []any{"example.test"}, decoded["search_domain_filter"])
	assert.Equal(t, "week", decoded["search_recency_filter"])
	assert.Equal(t, true, decoded["return_images"])
	assert.Equal(t, false, decoded["return_related_questions"])
	assert.Equal(t, "academic", decoded["search_mode"])
	assert.NotContains(t, decoded, "tools")
	assert.NotContains(t, decoded, "provider_extension")
	assert.NotContains(t, decoded, "group")
	messages := decoded["messages"].([]any)
	require.Len(t, messages, 1)
	assert.Equal(t, map[string]any{"role": "user", "content": "hello"}, messages[0])
	assert.EqualValues(t, 1, meta.Request.Extra["top_p"], "provider conversion must not mutate the retry/accounting snapshot")
	assert.Contains(t, meta.Request.Extra, "tools")
}

func TestJinaRequestConversion(t *testing.T) {
	t.Run("embeddings", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeJina, constant.RelayModeEmbeddings, "jina-embeddings-v3")
		meta.Request.Extra = map[string]any{
			"model": "client-model", "input": []any{"alpha", "beta"}, "encoding_format": "base64",
			"dimensions": 512, "user": "caller", "provider_extension": "must-strip", "group": "dashboard-only",
		}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		body, err := adaptor.ConvertRequest(meta)
		require.NoError(t, err)
		decoded := decodeProviderBody(t, body)
		assert.Equal(t, "jina-embeddings-v3", decoded["model"])
		assert.Equal(t, []any{"alpha", "beta"}, decoded["input"])
		assert.EqualValues(t, 512, decoded["dimensions"])
		assert.Equal(t, "caller", decoded["user"])
		assert.NotContains(t, decoded, "encoding_format")
		assert.NotContains(t, decoded, "provider_extension")
		assert.Equal(t, "base64", meta.Request.Extra["encoding_format"])
	})

	t.Run("rerank", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeJina, constant.RelayModeRerank, "jina-reranker-v2-base-multilingual")
		meta.Request.Extra = map[string]any{
			"model": "client-model", "query": "needle", "documents": []any{"one", "two"},
			"top_n": 1, "return_documents": true, "provider_extension": "must-strip",
		}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		body, err := adaptor.ConvertRequest(meta)
		require.NoError(t, err)
		decoded := decodeProviderBody(t, body)
		assert.Equal(t, "jina-reranker-v2-base-multilingual", decoded["model"])
		assert.Equal(t, "needle", decoded["query"])
		assert.Equal(t, []any{"one", "two"}, decoded["documents"])
		assert.NotContains(t, decoded, "provider_extension")
	})
}

func TestOpenAICompatibleRealtimeURLCarriesEncodedModel(t *testing.T) {
	meta := providerMeta(constant.ChannelTypeOpenAI, constant.RelayModeRealtime, "gpt realtime/preview")
	meta.BaseURL = "https://api.example.test"
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.test/v1/realtime?model=gpt+realtime%2Fpreview", requestURL)

	meta.ModelName = ""
	_, err = adaptor.GetRequestURL(meta)
	assert.Error(t, err)
}

func TestAzureOpenAIURLVersionAndAuthentication(t *testing.T) {
	t.Setenv("AZURE_DEFAULT_API_VERSION", "2025-04-01-preview")
	meta := providerMeta(constant.ChannelTypeAzure, constant.RelayModeChatCompletions, "deployment.name")
	meta.BaseURL = "https://resource.openai.azure.com"
	meta.Channel.CreatedTime = azurePreserveDeploymentDotsAfter
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://resource.openai.azure.com/openai/deployments/deployment.name/chat/completions?api-version=2025-04-01-preview", requestURL)
	req, err := http.NewRequest(http.MethodPost, requestURL, nil)
	require.NoError(t, err)
	require.NoError(t, adaptor.SetupRequestHeader(req, meta))
	assert.Equal(t, "provider-secret", req.Header.Get("api-key"))
	assert.Empty(t, req.Header.Get("Authorization"))

	meta.Channel.Other = "2024-10-21"
	meta.Mode = constant.RelayModeEmbeddings
	adaptor.Init(meta)
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://resource.openai.azure.com/openai/deployments/deployment.name/embeddings?api-version=2024-10-21", requestURL)

	meta.Mode = constant.RelayModeResponsesCompact
	adaptor.Init(meta)
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://resource.openai.azure.com/openai/v1/responses/compact?api-version=preview", requestURL)

	meta.Channel.OtherSettings = `{"azure_responses_version":"2025-01-01-preview"}`
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://resource.openai.azure.com/openai/v1/responses/compact?api-version=2025-01-01-preview", requestURL)

	meta.Channel.OtherSettings = ""
	meta.BaseURL = "https://legacy.cognitiveservices.azure.com"
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://legacy.cognitiveservices.azure.com/openai/responses/compact?api-version=2024-10-21", requestURL)

	meta.Mode = constant.RelayModeChatCompletions
	meta.BaseURL = "https://resource.openai.azure.com"
	meta.Channel.CreatedTime = azurePreserveDeploymentDotsAfter - 1
	meta.APIVersion = "2024-06-01"
	adaptor.Init(meta)
	requestURL, err = adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://resource.openai.azure.com/openai/deployments/deploymentname/chat/completions?api-version=2024-06-01", requestURL)

	meta.BaseURL = "https://resource.services.ai.azure.com"
	meta.APIVersion = ""
	meta.Channel.Other = "bad&injected=true"
	_, err = adaptor.GetRequestURL(meta)
	assert.Error(t, err)
}

func TestOpenRouterRequestConversion(t *testing.T) {
	meta := providerMeta(constant.ChannelTypeOpenRouter, constant.RelayModeChatCompletions, "anthropic/claude-sonnet-thinking")
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.StreamOptions = &protocolkit.StreamOptions{IncludeUsage: false}
	meta.Request.ReasoningEffort = "high"
	meta.Request.Extra = map[string]any{
		"model":            "client-model",
		"messages":         []any{map[string]any{"role": "user", "content": "hello"}},
		"vendor_extension": "preserved",
		"group":            "dashboard-only",
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeProviderBody(t, body)
	assert.Equal(t, "anthropic/claude-sonnet", decoded["model"])
	assert.Equal(t, "preserved", decoded["vendor_extension"])
	assert.NotContains(t, decoded, "group")
	assert.NotContains(t, decoded, "stream_options")
	assert.NotContains(t, decoded, "reasoning_effort")
	assert.Equal(t, true, decoded["usage"].(map[string]any)["include"])
	reasoning := decoded["reasoning"].(map[string]any)
	assert.Equal(t, true, reasoning["enabled"])
	assert.Equal(t, "high", reasoning["effort"])
	assert.Equal(t, "client-model", meta.Request.Extra["model"], "conversion must not mutate retry/billing input")
}

func TestNonChatRequestPreservesModeFieldsWithoutChatLeakage(t *testing.T) {
	meta := providerMeta(constant.ChannelTypeDeepSeek, constant.RelayModeEmbeddings, "deepseek-embedding")
	meta.Request.Extra = map[string]any{
		"model": "client-model",
		"input": []any{"alpha", "beta"},
		"group": "dashboard-only",
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeProviderBody(t, body)
	assert.Equal(t, "deepseek-embedding", decoded["model"])
	assert.Equal(t, []any{"alpha", "beta"}, decoded["input"])
	assert.NotContains(t, decoded, "group")
	assert.NotContains(t, decoded, "messages")
}

func TestDeepSeekRequestConversion(t *testing.T) {
	meta := providerMeta(constant.ChannelTypeDeepSeek, constant.RelayModeChatCompletions, "deepseek-v4-pro-max")
	meta.IsStream = true
	meta.Request.Stream = true
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeProviderBody(t, body)
	assert.Equal(t, "deepseek-v4-pro", decoded["model"])
	assert.Equal(t, "enabled", decoded["thinking"].(map[string]any)["type"])
	assert.Equal(t, "max", decoded["reasoning_effort"])
	assert.Equal(t, true, decoded["stream_options"].(map[string]any)["include_usage"])
}

func TestMistralRequestConversion(t *testing.T) {
	meta := providerMeta(constant.ChannelTypeMistral, constant.RelayModeChatCompletions, "mistral-large-latest")
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.Extra = map[string]any{
		"model":                 "client-model",
		"stream":                true,
		"stream_options":        map[string]any{"include_usage": true},
		"max_completion_tokens": 123,
		"messages": []any{
			map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []any{map[string]any{"id": "invalid-tool-id", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}},
			},
			map[string]any{"role": "tool", "tool_call_id": "invalid-tool-id", "content": "ok"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}}},
		},
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeProviderBody(t, body)
	assert.Equal(t, "mistral-large-latest", decoded["model"])
	assert.EqualValues(t, 123, decoded["max_tokens"])
	assert.NotContains(t, decoded, "max_completion_tokens")
	assert.NotContains(t, decoded, "stream_options")
	messages := decoded["messages"].([]any)
	callID := messages[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["id"].(string)
	assert.True(t, validMistralToolCallID(callID), callID)
	assert.Equal(t, callID, messages[1].(map[string]any)["tool_call_id"])
	assert.Empty(t, messages[0].(map[string]any)["content"].([]any))
	imageURL := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["image_url"]
	assert.Equal(t, "https://example.test/image.png", imageURL)
}

func TestXAIAndSiliconFlowRequestConversion(t *testing.T) {
	xai := providerMeta(constant.ChannelTypeXai, constant.RelayModeChatCompletions, "grok-3-mini-high")
	xai.IsStream = true
	xai.Request.Stream = true
	xai.Request.Extra = map[string]any{"model": "client-model", "max_tokens": 77, "stream": true}
	adaptor := &Adaptor{}
	adaptor.Init(xai)
	body, err := adaptor.ConvertRequest(xai)
	require.NoError(t, err)
	decoded := decodeProviderBody(t, body)
	assert.Equal(t, "grok-3-mini", decoded["model"])
	assert.Equal(t, "high", decoded["reasoning_effort"])
	assert.EqualValues(t, 77, decoded["max_completion_tokens"])
	assert.NotContains(t, decoded, "max_tokens")
	assert.Equal(t, true, decoded["stream_options"].(map[string]any)["include_usage"])

	xai.ModelName = "grok-3-search"
	xai.Request.Extra = map[string]any{"model": "client-model"}
	body, err = adaptor.ConvertRequest(xai)
	require.NoError(t, err)
	decoded = decodeProviderBody(t, body)
	assert.Equal(t, "grok-3", decoded["model"])
	assert.Equal(t, "on", decoded["search_parameters"].(map[string]any)["mode"])

	silicon := providerMeta(constant.ChannelTypeSiliconFlow, constant.RelayModeCompletions, "deepseek-ai/DeepSeek-V3")
	silicon.Request.Extra = map[string]any{"model": "client-model", "prefix": "func main()", "suffix": "}"}
	adaptor.Init(silicon)
	body, err = adaptor.ConvertRequest(silicon)
	require.NoError(t, err)
	decoded = decodeProviderBody(t, body)
	messages := decoded["messages"].([]any)
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].(map[string]any)["role"])

	silicon.Mode = constant.RelayModeImagesGenerations
	silicon.Request.Extra = map[string]any{"model": "client-model", "prompt": "cat", "size": "1024x1024", "n": 2}
	adaptor.Init(silicon)
	body, err = adaptor.ConvertRequest(silicon)
	require.NoError(t, err)
	decoded = decodeProviderBody(t, body)
	assert.Equal(t, "1024x1024", decoded["image_size"])
	assert.EqualValues(t, 2, decoded["batch_size"])
	assert.NotContains(t, decoded, "size")
	assert.NotContains(t, decoded, "n")
}

func TestProviderUsageNormalizationAndErrorMapping(t *testing.T) {
	t.Run("xAI non-stream", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeXai, constant.RelayModeChatCompletions, "grok-3")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := openAITestContext()
		response := `{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":999,"total_tokens":10,"completion_tokens_details":{"reasoning_tokens":2}}}`
		usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(response)), meta)
		require.NoError(t, err)
		require.NotNil(t, usage)
		assert.Equal(t, 6, usage.CompletionTokens)
		assert.Equal(t, 4, usage.CompletionTokensDetails.TextTokens)
		assert.Equal(t, 2, usage.ReasoningTokens)
		decoded := decodeProviderBody(t, recorder.Body.Bytes())
		assert.EqualValues(t, 6, decoded["usage"].(map[string]any)["completion_tokens"])
	})

	t.Run("DeepSeek cache usage", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeDeepSeek, constant.RelayModeChatCompletions, "deepseek-chat")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, _ := openAITestContext()
		response := `{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10,"prompt_cache_hit_tokens":3}}`
		usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(response)), meta)
		require.NoError(t, err)
		require.NotNil(t, usage.PromptTokensDetails)
		assert.Equal(t, 3, usage.PromptTokensDetails.CachedTokens)
	})

	t.Run("SiliconFlow rerank", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeSiliconFlow, constant.RelayModeRerank, "BAAI/bge-reranker-v2-m3")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := openAITestContext()
		response := `{"results":[{"index":0,"relevance_score":0.9}],"meta":{"tokens":{"input_tokens":11,"output_tokens":1}}}`
		usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, &protocolkit.Usage{PromptTokens: 11, CompletionTokens: 1, TotalTokens: 12}, usage)
		decoded := decodeProviderBody(t, recorder.Body.Bytes())
		assert.NotContains(t, decoded, "meta")
		assert.EqualValues(t, 12, decoded["usage"].(map[string]any)["total_tokens"])
	})

	t.Run("Jina rerank total usage", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeJina, constant.RelayModeRerank, "jina-reranker-v2-base-multilingual")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := openAITestContext()
		response := `{"results":[{"index":0,"relevance_score":0.91}],"usage":{"total_tokens":13}}`
		usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, &protocolkit.Usage{PromptTokens: 13, TotalTokens: 13}, usage)
		decoded := decodeProviderBody(t, recorder.Body.Bytes())
		assert.EqualValues(t, 13, decoded["usage"].(map[string]any)["prompt_tokens"])
	})

	t.Run("Perplexity citations and usage", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypePerplexity, constant.RelayModeChatCompletions, "sonar-pro")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := openAITestContext()
		response := `{"id":"pplx-1","choices":[],"citations":["https://example.test/source"],"search_results":[{"title":"source"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
		usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, &protocolkit.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}, usage)
		decoded := decodeProviderBody(t, recorder.Body.Bytes())
		assert.Equal(t, []any{"https://example.test/source"}, decoded["citations"])
		assert.Len(t, decoded["search_results"], 1)
	})

	t.Run("Responses usage", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeDeepSeek, constant.RelayModeResponses, "deepseek-chat")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, _ := openAITestContext()
		response := `{"id":"resp_1","object":"response","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":1}}}`
		usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, 5, usage.PromptTokens)
		assert.Equal(t, 2, usage.CompletionTokens)
		assert.Equal(t, 1, usage.PromptTokensDetails.CachedTokens)
		assert.Equal(t, 1, usage.ReasoningTokens)
	})

	t.Run("status mapping and bounded error", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeMistral, constant.RelayModeChatCompletions, "mistral-large")
		meta.Channel.StatusCodeMapping = `{"429":"503"}`
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := openAITestContext()
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"busy","type":"rate_limit"}}`))}
		_, err := adaptor.DoResponse(ctx, resp, meta)
		var upstream *relaycommon.UpstreamError
		require.ErrorAs(t, err, &upstream)
		assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
		assert.Zero(t, recorder.Body.Len())
	})

	t.Run("status mapping rejects non-integral codes", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeMistral, constant.RelayModeChatCompletions, "mistral-large")
		meta.Channel.StatusCodeMapping = `{"429":503.5}`
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, _ := openAITestContext()
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"busy"}}`))}
		_, err := adaptor.DoResponse(ctx, resp, meta)
		var upstream *relaycommon.UpstreamError
		require.ErrorAs(t, err, &upstream)
		assert.Equal(t, http.StatusTooManyRequests, upstream.StatusCode)
	})

	t.Run("success status error envelope fails closed", func(t *testing.T) {
		meta := providerMeta(constant.ChannelTypeOpenRouter, constant.RelayModeChatCompletions, "openai/gpt-4o")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := openAITestContext()
		_, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(`{"error":{"message":"provider rejected request","type":"invalid_request_error"}}`)), meta)
		var upstream *relaycommon.UpstreamError
		require.ErrorAs(t, err, &upstream)
		assert.Equal(t, http.StatusBadRequest, upstream.StatusCode)
		assert.Zero(t, recorder.Body.Len())
	})
}

func TestOpenRouterStreamUsageExtraction(t *testing.T) {
	meta := providerMeta(constant.ChannelTypeOpenRouter, constant.RelayModeChatCompletions, "openai/gpt-4o")
	meta.IsStream = true
	meta.PromptTokens = 99
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	ctx, recorder := openAITestContext()
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
	usage, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(stream)), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, usage)
	assert.Contains(t, recorder.Body.String(), "data: [DONE]")
}
