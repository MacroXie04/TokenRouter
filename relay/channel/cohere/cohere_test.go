package cohere

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func cohereTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	return ctx, recorder
}

func cohereTestResponse(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(body)}
}

func cohereTestMeta(mode constant.RelayMode) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(constant.ChannelTypeCohere)},
		Mode:      mode,
		ModelName: "command-r-plus",
		APIKey:    "cohere-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "client-model",
			Messages: []protocolkit.Message{{
				Role: "user", Content: "hello",
			}},
		},
		PromptTokens: 5,
	}
}

func decodeCohereBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(body, &decoded))
	return decoded
}

func TestCohereURLsHeadersModesAndModelCatalog(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       constant.RelayMode
		stream     bool
		base       string
		expected   string
		wantAccept string
	}{
		{name: "chat default", mode: constant.RelayModeChatCompletions, expected: "https://api.cohere.ai/v1/chat", wantAccept: "application/json"},
		{name: "chat stream custom path", mode: constant.RelayModeChatCompletions, stream: true, base: "https://cohere.example/gateway/", expected: "https://cohere.example/gateway/v1/chat", wantAccept: "text/event-stream"},
		{name: "rerank", mode: constant.RelayModeRerank, base: "https://cohere.example", expected: "https://cohere.example/v1/rerank", wantAccept: "application/json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			meta := cohereTestMeta(test.mode)
			meta.IsStream = test.stream
			meta.Request.Stream = test.stream
			meta.BaseURL = test.base
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.expected, requestURL)
			req, err := http.NewRequest(http.MethodPost, requestURL, nil)
			require.NoError(t, err)
			req.Header.Set("x-api-key", "must-remove")
			req.Header.Set("x-goog-api-key", "must-remove")
			require.NoError(t, adaptor.SetupRequestHeader(req, meta))
			assert.Equal(t, "Bearer cohere-secret", req.Header.Get("Authorization"))
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, test.wantAccept, req.Header.Get("Accept"))
			assert.Empty(t, req.Header.Get("x-api-key"))
			assert.Empty(t, req.Header.Get("x-goog-api-key"))
		})
	}

	unsupported := cohereTestMeta(constant.RelayModeEmbeddings)
	adaptor := &Adaptor{}
	adaptor.Init(unsupported)
	_, err := adaptor.GetRequestURL(unsupported)
	assert.ErrorContains(t, err, "does not support")
	_, err = adaptor.ConvertRequest(unsupported)
	assert.ErrorContains(t, err, "does not support")

	rerankStream := cohereTestMeta(constant.RelayModeRerank)
	rerankStream.IsStream = true
	adaptor.Init(rerankStream)
	_, err = adaptor.GetRequestURL(rerankStream)
	assert.ErrorContains(t, err, "does not support streaming")

	badBase := cohereTestMeta(constant.RelayModeChatCompletions)
	badBase.BaseURL = "https://user:password@cohere.example"
	adaptor.Init(badBase)
	_, err = adaptor.GetRequestURL(badBase)
	assert.ErrorContains(t, err, "must not contain credentials")

	req, err := http.NewRequest(http.MethodPost, "https://cohere.example/v1/chat", nil)
	require.NoError(t, err)
	badKey := cohereTestMeta(constant.RelayModeChatCompletions)
	badKey.APIKey = "\r\n"
	assert.Error(t, adaptor.SetupRequestHeader(req, badKey))
	badKey.APIKey = strings.Repeat("k", maxCohereCredentialBytes+1)
	assert.Error(t, adaptor.SetupRequestHeader(req, badKey))

	models := ModelList()
	require.Contains(t, models, "command-a-03-2025")
	models[0] = "mutated"
	assert.Equal(t, "command-a-03-2025", ModelList()[0])
}

func TestCohereChatRequestConversion(t *testing.T) {
	t.Setenv("COHERE_SAFETY_SETTING", "contextual")
	maxTokens := 7
	maxCompletionTokens := 9
	meta := cohereTestMeta(constant.RelayModeChatCompletions)
	meta.ModelName = "command-r-08-2024"
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.MaxTokens = &maxTokens
	meta.Request.MaxCompletionTokens = &maxCompletionTokens
	meta.Request.Messages = []protocolkit.Message{
		{Role: "system", Content: []any{map[string]any{"type": "text", "text": "rules"}}},
		{Role: "user", Content: "old user turn"},
		{Role: "assistant", Content: "old answer"},
		{Role: "tool", Content: "tool result"},
		{Role: "user", Content: []any{map[string]any{"type": "text", "text": "latest "}, map[string]any{"type": "text", "text": "question"}}},
	}
	meta.Request.Extra = map[string]any{"group": "dashboard-only", "provider_extension": "must-strip"}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeCohereBody(t, body)
	assert.Equal(t, "command-r-08-2024", decoded["model"])
	assert.Equal(t, "latest question", decoded["message"])
	assert.Equal(t, true, decoded["stream"])
	assert.EqualValues(t, 9, decoded["max_tokens"])
	assert.Equal(t, "CONTEXTUAL", decoded["safety_mode"])
	assert.NotContains(t, decoded, "group")
	assert.NotContains(t, decoded, "provider_extension")
	history := decoded["chat_history"].([]any)
	require.Len(t, history, 3)
	assert.Equal(t, map[string]any{"role": "SYSTEM", "message": "rules"}, history[0])
	assert.Equal(t, map[string]any{"role": "CHATBOT", "message": "old answer"}, history[1])
	assert.Equal(t, map[string]any{"role": "USER", "message": "tool result"}, history[2])
	assert.Equal(t, "client-model", meta.Request.Model)
	assert.Equal(t, "must-strip", meta.Request.Extra["provider_extension"], "conversion must not mutate the relay snapshot")

	t.Setenv("COHERE_SAFETY_SETTING", "NONE")
	zero := 0
	meta.Request.MaxCompletionTokens = &zero
	meta.Request.MaxTokens = &zero
	meta.Request.Stream = false
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded = decodeCohereBody(t, body)
	assert.EqualValues(t, defaultMaxTokens, decoded["max_tokens"])
	assert.NotContains(t, decoded, "safety_mode")

	negative := -1
	meta.Request.MaxTokens = &negative
	_, err = adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "outside the supported range")
	meta.Request.MaxTokens = nil
	t.Setenv("COHERE_SAFETY_SETTING", "unsafe")
	_, err = adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "COHERE_SAFETY_SETTING")
}

func TestCohereRerankRequestConversionAndBounds(t *testing.T) {
	meta := cohereTestMeta(constant.RelayModeRerank)
	meta.ModelName = "rerank-multilingual-v3.0"
	meta.Request.Extra = map[string]any{
		"model": "client-model", "query": "needle", "documents": []any{"one", map[string]any{"text": "two"}},
		"top_n": 0, "return_documents": false, "provider_extension": "must-strip",
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeCohereBody(t, body)
	assert.Equal(t, "rerank-multilingual-v3.0", decoded["model"])
	assert.Equal(t, "needle", decoded["query"])
	assert.Len(t, decoded["documents"], 2)
	assert.EqualValues(t, 1, decoded["top_n"])
	assert.Equal(t, true, decoded["return_documents"])
	assert.NotContains(t, decoded, "provider_extension")
	assert.Equal(t, false, meta.Request.Extra["return_documents"], "conversion must not mutate the relay snapshot")

	meta.Request.Extra["top_n"] = 2
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.EqualValues(t, 2, decodeCohereBody(t, body)["top_n"])
	estimated, err := estimateRerankPromptTokens(meta.Request)
	require.NoError(t, err)
	assert.Equal(t, relaycommon.CountTokens("one\nmap[text:two]\nneedle"), estimated)

	meta.Request.Extra["top_n"] = 1.5
	_, err = adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "decode OpenAI rerank request")
	meta.Request.Extra["top_n"] = 1
	meta.Request.Extra["documents"] = make([]any, maxCohereRerankDocuments+1)
	_, err = adaptor.ConvertRequest(meta)
	assert.ErrorContains(t, err, "exceed")

	chat := cohereTestMeta(constant.RelayModeChatCompletions)
	chat.Request.Messages[0].Content = strings.Repeat("x", maxCohereRequestBodyBytes)
	adaptor.Init(chat)
	_, err = adaptor.ConvertRequest(chat)
	assert.ErrorContains(t, err, "exceeds")
}

func TestCohereNonStreamResponses(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		meta := cohereTestMeta(constant.RelayModeChatCompletions)
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := cohereTestContext()
		response := `{"response_id":"cohere-1","finish_reason":"COMPLETE","text":"answer","meta":{"billed_units":{"input_tokens":4,"output_tokens":6}}}`
		usage, err := adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, &protocolkit.Usage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10}, usage)
		assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
		decoded := decodeCohereBody(t, recorder.Body.Bytes())
		assert.Equal(t, "cohere-1", decoded["id"])
		assert.Equal(t, "chat.completion", decoded["object"])
		assert.Equal(t, "command-r-plus", decoded["model"])
		choice := decoded["choices"].([]any)[0].(map[string]any)
		assert.Equal(t, "stop", choice["finish_reason"])
		assert.Equal(t, "answer", choice["message"].(map[string]any)["content"])
	})

	t.Run("rerank billed and fallback", func(t *testing.T) {
		meta := cohereTestMeta(constant.RelayModeRerank)
		meta.PromptTokens = 13
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		ctx, recorder := cohereTestContext()
		response := `{"results":[{"index":1,"relevance_score":0.92,"document":{"text":"two"},"ignored":"strip"}],"meta":{"billed_units":{"input_tokens":8,"output_tokens":1}}}`
		usage, err := adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, &protocolkit.Usage{PromptTokens: 8, CompletionTokens: 1, TotalTokens: 9}, usage)
		decoded := decodeCohereBody(t, recorder.Body.Bytes())
		result := decoded["results"].([]any)[0].(map[string]any)
		assert.NotContains(t, result, "ignored")
		assert.EqualValues(t, 9, decoded["usage"].(map[string]any)["total_tokens"])

		ctx, _ = cohereTestContext()
		response = `{"results":[],"meta":{"billed_units":{"input_tokens":0,"output_tokens":0}}}`
		usage, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(response)), meta)
		require.NoError(t, err)
		assert.Equal(t, &protocolkit.Usage{PromptTokens: 13, TotalTokens: 13}, usage)
	})
}

func TestCohereStreamConversionUsageFallbackAndBounds(t *testing.T) {
	meta := cohereTestMeta(constant.RelayModeChatCompletions)
	meta.IsStream = true
	meta.Request.Stream = true
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	ctx, recorder := cohereTestContext()
	stream := strings.Join([]string{
		`not-json`,
		`{"is_finished":false,"event_type":"text-generation","text":"hello"}`,
		`{"is_finished":true,"event_type":"stream-end","finish_reason":"MAX_TOKENS","response":{"response_id":"cohere-stream","text":"hello","meta":{"billed_units":{"input_tokens":5,"output_tokens":3}}}}`,
	}, "\r\n") + "\r\n"
	usage, err := adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(stream)), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}, usage)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"max_tokens"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))

	ctx, _ = cohereTestContext()
	fallbackStream := `{"is_finished":false,"event_type":"text-generation","text":"hello"}` + "\n"
	usage, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(fallbackStream)), meta)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, usage)

	ctx, _ = cohereTestContext()
	oversizedLine := strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	usage, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(oversizedLine)), meta)
	require.Error(t, err)
	assert.ErrorContains(t, err, "maximum event")
	require.NotNil(t, usage, "an accepted stream keeps estimated usage visible to settlement")
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, TotalTokens: 5}, usage)

	assert.EqualValues(t, maxCohereStreamTextBytes, mustAddCohereStreamTextBytes(t, maxCohereStreamTextBytes-1, 1))
	_, err = addCohereStreamTextBytes(maxCohereStreamTextBytes, 1)
	assert.ErrorContains(t, err, "exceeds")

	ctx, _ = cohereTestContext()
	errorStream := strings.Join([]string{
		`{"is_finished":false,"event_type":"text-generation","text":"hello"}`,
		`{"is_finished":false,"event_type":"error","message":"stream failed"}`,
	}, "\n") + "\n"
	usage, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(errorStream)), meta)
	require.Error(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, usage)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Contains(t, upstream.Body, `"code":"cohere_error"`)

	oversizedStreamError := cohereStreamError(strings.Repeat("x", int(relaycommon.MaxUpstreamErrorBodyBytes)+1))
	require.ErrorAs(t, oversizedStreamError, &upstream)
	assert.ErrorIs(t, upstream.Cause, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Empty(t, upstream.Body)
}

func mustAddCohereStreamTextBytes(t *testing.T, current int64, additional int) int64 {
	t.Helper()
	value, err := addCohereStreamTextBytes(current, additional)
	require.NoError(t, err)
	return value
}

func TestCohereResponseAndErrorBounds(t *testing.T) {
	meta := cohereTestMeta(constant.RelayModeChatCompletions)
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	ctx, recorder := cohereTestContext()
	oversized := strings.NewReader(strings.Repeat(" ", int(relaycommon.MaxUpstreamJSONBodyBytes)+1))
	usage, err := adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, oversized), meta)
	require.Error(t, err)
	assert.ErrorContains(t, err, "exceeds limit")
	assert.Nil(t, usage)
	assert.Zero(t, recorder.Body.Len())

	ctx, recorder = cohereTestContext()
	invalidUsage := `{"response_id":"cohere-1","text":"answer","meta":{"billed_units":{"input_tokens":2147483647,"output_tokens":1}}}`
	usage, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusOK, strings.NewReader(invalidUsage)), meta)
	require.Error(t, err)
	require.NotNil(t, usage, "accepted upstream work with invalid usage remains visible to the settlement lifecycle")
	assert.Equal(t, -1, usage.TotalTokens)
	assert.Zero(t, recorder.Body.Len())

	meta.Channel.StatusCodeMapping = `{"429":"503"}`
	ctx, _ = cohereTestContext()
	_, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusTooManyRequests,
		strings.NewReader(`{"message":"Cohere busy"}`)), meta)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.Contains(t, upstream.Body, `"code":"cohere_error"`)
	assert.Contains(t, upstream.Body, "Cohere busy")

	ctx, _ = cohereTestContext()
	_, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusFound, strings.NewReader(`{"message":"redirect refused"}`)), meta)
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusFound, upstream.StatusCode)

	ctx, _ = cohereTestContext()
	tooLargeError := strings.Repeat("x", int(relaycommon.MaxUpstreamErrorBodyBytes)+1)
	_, err = adaptor.DoResponse(ctx, cohereTestResponse(http.StatusBadRequest, strings.NewReader(tooLargeError)), meta)
	require.Error(t, err)
	require.ErrorAs(t, err, &upstream)
	assert.ErrorIs(t, upstream.Cause, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Empty(t, upstream.Body)
}
