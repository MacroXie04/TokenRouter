package cloudflare

import (
	"bytes"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cloudflareMeta(mode channelcatalog.RelayMode, format channelcatalog.RelayFormat) *relaycommon.Meta {
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(channelcatalog.ChannelCloudflare), Other: "account_123"},
		Mode:      mode,
		Format:    format,
		ModelName: "@cf/meta/mapped-model",
		APIKey:    "cloudflare-secret",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model:    "client-model",
			Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
	}
}

func cloudflareContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	return context, recorder
}

func cloudflareResponse(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(body)}
}

func decodeCloudflareBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(body, &decoded))
	return decoded
}

func TestCloudflareURLsAuthenticationAndModeGates(t *testing.T) {
	tests := []struct {
		name     string
		mode     channelcatalog.RelayMode
		format   channelcatalog.RelayFormat
		base     string
		expected string
	}{
		{name: "chat", mode: channelcatalog.RelayModeChatCompletions, format: channelcatalog.RelayFormatOpenAI, expected: "https://api.cloudflare.com/client/v4/accounts/account_123/ai/v1/chat/completions"},
		{name: "embeddings", mode: channelcatalog.RelayModeEmbeddings, format: channelcatalog.RelayFormatEmbedding, expected: "https://api.cloudflare.com/client/v4/accounts/account_123/ai/v1/embeddings"},
		{name: "responses", mode: channelcatalog.RelayModeResponses, format: channelcatalog.RelayFormatOpenAIResponses, expected: "https://api.cloudflare.com/client/v4/accounts/account_123/ai/v1/responses"},
		{name: "completion", mode: channelcatalog.RelayModeCompletions, format: channelcatalog.RelayFormatOpenAI, expected: "https://api.cloudflare.com/client/v4/accounts/account_123/ai/run/@cf/meta/mapped-model"},
		{name: "transcription", mode: channelcatalog.RelayModeAudioTranscription, format: channelcatalog.RelayFormatOpenAIAudio, expected: "https://api.cloudflare.com/client/v4/accounts/account_123/ai/run/@cf/meta/mapped-model"},
		{name: "translation custom base", mode: channelcatalog.RelayModeAudioTranslation, format: channelcatalog.RelayFormatOpenAIAudio, base: "https://gateway.example/root/", expected: "https://gateway.example/root/client/v4/accounts/account_123/ai/run/@cf/meta/mapped-model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := cloudflareMeta(test.mode, test.format)
			meta.BaseURL = test.base
			meta.APIVersion = "client-controlled-account-must-not-win"
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			requestURL, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.expected, requestURL)

			request := httptest.NewRequest(http.MethodPost, requestURL, nil)
			request.Header.Set("x-api-key", "must-be-cleared")
			require.NoError(t, adaptor.SetupRequestHeader(request, meta))
			assert.Equal(t, "Bearer cloudflare-secret", request.Header.Get("Authorization"))
			assert.Empty(t, request.Header.Get("x-api-key"))
			if isCloudflareAudioMode(test.mode) {
				assert.Equal(t, "application/octet-stream", request.Header.Get("Content-Type"))
			} else {
				assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			}
			assert.Equal(t, "application/json", request.Header.Get("Accept"))
		})
	}

	stream := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	stream.IsStream = true
	adaptor := &Adaptor{}
	adaptor.Init(stream)
	request := httptest.NewRequest(http.MethodPost, defaultBaseURL, nil)
	require.NoError(t, adaptor.SetupRequestHeader(request, stream))
	assert.Equal(t, "text/event-stream", request.Header.Get("Accept"))

	for _, invalidBase := range []string{
		"ftp://api.cloudflare.test", "https://user:pass@api.cloudflare.test",
		"https://api.cloudflare.test?token=x", "https://api.cloudflare.test#fragment",
	} {
		meta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
		meta.BaseURL = invalidBase
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err, invalidBase)
	}

	for _, invalidAccount := range []string{"", "../other-account", "account/other", strings.Repeat("a", maxCloudflareAccountIDBytes+1)} {
		meta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
		meta.Channel.Other = invalidAccount
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err, invalidAccount)
	}

	invalidModel := cloudflareMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI)
	invalidModel.ModelName = "@cf/meta/../escape"
	adaptor.Init(invalidModel)
	_, err := adaptor.GetRequestURL(invalidModel)
	require.Error(t, err)

	missingKey := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	missingKey.APIKey = " "
	adaptor.Init(missingKey)
	require.Error(t, adaptor.SetupRequestHeader(request, missingKey))

	for _, invalid := range []*relaycommon.Meta{
		cloudflareMeta(channelcatalog.RelayModeRerank, channelcatalog.RelayFormatRerank),
		cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatClaude),
	} {
		adaptor.Init(invalid)
		_, urlErr := adaptor.GetRequestURL(invalid)
		_, convertErr := adaptor.ConvertRequest(invalid)
		assert.Error(t, urlErr)
		assert.Error(t, convertErr)
	}

	streamingEmbedding := cloudflareMeta(channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding)
	streamingEmbedding.IsStream = true
	adaptor.Init(streamingEmbedding)
	_, err = adaptor.GetRequestURL(streamingEmbedding)
	require.ErrorContains(t, err, "does not support streaming")
}

func TestCloudflareRequestConversionUsesMappedModelAndNativeRunShape(t *testing.T) {
	temperature := 0.25
	maximum := 17
	meta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	meta.IsStream = true
	meta.Request.Stream = true
	meta.Request.Temperature = &temperature
	meta.Request.MaxTokens = &maximum
	meta.Request.StreamOptions = &protocolkit.StreamOptions{IncludeUsage: true}
	meta.Request.Extra = map[string]any{"model": "client-model", "provider_extension": "kept", "group": "dashboard-only"}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded := decodeCloudflareBody(t, body)
	assert.Equal(t, "@cf/meta/mapped-model", decoded["model"])
	assert.Equal(t, "kept", decoded["provider_extension"])
	assert.NotContains(t, decoded, "group")
	assert.Equal(t, true, decoded["stream_options"].(map[string]any)["include_usage"])
	assert.EqualValues(t, 0.25, decoded["temperature"])

	zero := 0
	meta = cloudflareMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI)
	meta.Request.Prompt = "native prompt"
	meta.Request.MaxTokens = &maximum
	meta.Request.MaxCompletionTokens = &zero
	meta.Request.Temperature = &temperature
	meta.Request.Stream = true
	meta.IsStream = true
	adaptor.Init(meta)
	body, err = adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	decoded = decodeCloudflareBody(t, body)
	assert.Equal(t, "native prompt", decoded["prompt"])
	assert.EqualValues(t, 17, decoded["max_tokens"], "zero max_completion_tokens falls back to legacy max_tokens like the reference")
	assert.Equal(t, true, decoded["stream"])
	assert.EqualValues(t, 0.25, decoded["temperature"])
	assert.NotContains(t, decoded, "model", "the mapped model belongs in the /run URL")
	assert.NotContains(t, decoded, "messages")

	meta = cloudflareMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI)
	meta.Request.Prompt = []any{"not", "a string"}
	adaptor.Init(meta)
	_, err = adaptor.ConvertRequest(meta)
	require.ErrorContains(t, err, "must be a string")

	negative := -1
	meta.Request.Prompt = "prompt"
	meta.Request.MaxTokens = &negative
	_, err = adaptor.ConvertRequest(meta)
	require.ErrorContains(t, err, "outside the supported range")

	embedding := cloudflareMeta(channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding)
	embedding.Request.Extra = map[string]any{"model": "client-model", "input": []any{"one", "two"}, "group": "dashboard-only"}
	adaptor.Init(embedding)
	body, err = adaptor.ConvertRequest(embedding)
	require.NoError(t, err)
	decoded = decodeCloudflareBody(t, body)
	assert.Equal(t, "@cf/meta/mapped-model", decoded["model"])
	assert.Equal(t, []any{"one", "two"}, decoded["input"])
	assert.NotContains(t, decoded, "group")

	responses := cloudflareMeta(channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses)
	responses.Request.Extra = map[string]any{"model": "client-model", "input": "hello", "instructions": "brief"}
	adaptor.Init(responses)
	body, err = adaptor.ConvertRequest(responses)
	require.NoError(t, err)
	decoded = decodeCloudflareBody(t, body)
	assert.Equal(t, "@cf/meta/mapped-model", decoded["model"])
	assert.Equal(t, "hello", decoded["input"])
	assert.Equal(t, "brief", decoded["instructions"])

	tooLarge := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	tooLarge.Request.Messages[0].Content = strings.Repeat("x", maxCloudflareJSONRequestBytes)
	adaptor.Init(tooLarge)
	_, err = adaptor.ConvertRequest(tooLarge)
	require.ErrorContains(t, err, "request exceeds")
}

func TestCloudflareAudioRequestExtractsOneBoundedFile(t *testing.T) {
	makeMultipart := func(t *testing.T, files ...[]byte) ([]byte, string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for index, contents := range files {
			part, err := writer.CreateFormFile("file", "audio"+string(rune('a'+index))+".wav")
			require.NoError(t, err)
			_, err = part.Write(contents)
			require.NoError(t, err)
		}
		require.NoError(t, writer.WriteField("model", "client-model"))
		require.NoError(t, writer.Close())
		return body.Bytes(), writer.FormDataContentType()
	}

	meta := cloudflareMeta(channelcatalog.RelayModeAudioTranscription, channelcatalog.RelayFormatOpenAIAudio)
	meta.RawBody, meta.RequestContentType = makeMultipart(t, []byte("RIFF-audio-bytes"))
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	assert.Equal(t, []byte("RIFF-audio-bytes"), body)

	meta.RawBody, meta.RequestContentType = makeMultipart(t)
	_, err = adaptor.ConvertRequest(meta)
	require.ErrorContains(t, err, "file is required")

	meta.RawBody, meta.RequestContentType = makeMultipart(t, []byte("one"), []byte("two"))
	_, err = adaptor.ConvertRequest(meta)
	require.ErrorContains(t, err, "multiple file fields")

	meta.RawBody = bytes.Repeat([]byte{'x'}, maxCloudflareAudioRequestBytes+1)
	_, err = adaptor.ConvertRequest(meta)
	require.ErrorContains(t, err, "audio request exceeds")
}

func TestCloudflareNonStreamResponsesNormalizeUsageAndMappedModel(t *testing.T) {
	meta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	meta.PromptTokens = 7
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, recorder := cloudflareContext()
	usage, err := adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"id":"provider-id","model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello from Cloudflare"},"finish_reason":"stop"}],"usage":{"prompt_tokens":999,"completion_tokens":999,"total_tokens":1998}}`,
	)), meta)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, relaycommon.CountTokens("hello from Cloudflare"), usage.CompletionTokens)
	chat := decodeCloudflareBody(t, recorder.Body.Bytes())
	assert.Equal(t, "@cf/meta/mapped-model", chat["model"])
	assert.True(t, strings.HasPrefix(chat["id"].(string), "chatcmpl-"))
	assert.EqualValues(t, usage.TotalTokens, chat["usage"].(map[string]any)["total_tokens"])

	embedding := cloudflareMeta(channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding)
	embedding.PromptTokens = 13
	adaptor.Init(embedding)
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"input_tokens":5,"total_tokens":5}}`,
	)), embedding)
	require.NoError(t, err)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.Equal(t, 5, usage.TotalTokens)
	decodedEmbedding := decodeCloudflareBody(t, recorder.Body.Bytes())
	assert.Equal(t, "@cf/meta/mapped-model", decodedEmbedding["model"])
	assert.EqualValues(t, 5, decodedEmbedding["usage"].(map[string]any)["prompt_tokens"])

	embedding.PromptTokens = 13
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.3]}]}`,
	)), embedding)
	require.NoError(t, err)
	assert.Equal(t, &protocolkit.Usage{PromptTokens: 13, TotalTokens: 13}, usage)
	assert.Contains(t, recorder.Body.String(), `"prompt_tokens":13`)

	completion := cloudflareMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI)
	completion.PromptTokens = 3
	adaptor.Init(completion)
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"result":{"response":"native answer"},"success":true,"errors":[],"messages":[]}`,
	)), completion)
	require.NoError(t, err)
	assert.Equal(t, relaycommon.CountTokens("native answer"), usage.CompletionTokens)
	decodedCompletion := decodeCloudflareBody(t, recorder.Body.Bytes())
	assert.Equal(t, "text_completion", decodedCompletion["object"])
	assert.Equal(t, "native answer", decodedCompletion["choices"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "@cf/meta/mapped-model", decodedCompletion["model"])

	audio := cloudflareMeta(channelcatalog.RelayModeAudioTranscription, channelcatalog.RelayFormatOpenAIAudio)
	audio.PromptTokens = 2
	adaptor.Init(audio)
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"result":{"text":"transcribed words"},"success":true}`,
	)), audio)
	require.NoError(t, err)
	assert.Equal(t, relaycommon.CountTokens("transcribed words"), usage.CompletionTokens)
	assert.JSONEq(t, `{"text":"transcribed words"}`, recorder.Body.String())

	responses := cloudflareMeta(channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses)
	adaptor.Init(responses)
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"id":"resp_1","object":"response","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`,
	)), responses)
	require.NoError(t, err)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"resp_1"`)
}

func TestCloudflareStreamsNormalizeWireAndUsage(t *testing.T) {
	meta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	meta.IsStream = true
	meta.PromptTokens = 9
	meta.Request.StreamOptions = &protocolkit.StreamOptions{IncludeUsage: true}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	stream := strings.Join([]string{
		`: heartbeat`,
		`data: {"id":"provider-one","object":"chat.completion.chunk","created":1,"model":"provider-model","choices":[{"index":0,"delta":{"content":"hello "}}]}`,
		`data: {"id":"provider-two","object":"chat.completion.chunk","created":2,"model":"provider-model","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	context, recorder := cloudflareContext()
	usage, err := adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(stream)), meta)
	require.NoError(t, err)
	assert.Equal(t, 9, usage.PromptTokens)
	assert.Equal(t, relaycommon.CountTokens("hello world"), usage.CompletionTokens)
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
	assert.Contains(t, recorder.Body.String(), `"role":"assistant"`)
	assert.Contains(t, recorder.Body.String(), `"model":"@cf/meta/mapped-model"`)
	assert.Contains(t, recorder.Body.String(), `"prompt_tokens":9`)

	var ids []string
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		data, ok, done := cloudflareSSEData(line)
		if !ok || done {
			continue
		}
		var event map[string]any
		require.NoError(t, protocolkit.UnmarshalJSON(data, &event))
		if id, _ := event["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	require.GreaterOrEqual(t, len(ids), 3)
	for _, id := range ids[1:] {
		assert.Equal(t, ids[0], id)
	}

	completion := cloudflareMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI)
	completion.IsStream = true
	completion.PromptTokens = 4
	completion.Request.StreamOptions = &protocolkit.StreamOptions{IncludeUsage: true}
	adaptor.Init(completion)
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(strings.Join([]string{
		`data: {"response":"native "}`,
		`data: {"result":{"response":"stream"},"success":true}`,
		`data: [DONE]`,
		"",
	}, "\n\n"))), completion)
	require.NoError(t, err)
	assert.Equal(t, relaycommon.CountTokens("native stream"), usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"text":"native "`)
	assert.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
	assert.Contains(t, recorder.Body.String(), `"total_tokens":`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))

	responses := cloudflareMeta(channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses)
	responses.IsStream = true
	responses.Request.Stream = true
	responses.Request.Extra = map[string]any{"input": "hello", "stream": true}
	adaptor.Init(responses)
	context, recorder = cloudflareContext()
	usage, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"id":"resp_cf","object":"response","status":"completed","model":"@cf/meta/mapped-model","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`,
		`data: [DONE]`,
		"",
	}, "\n\n"))), responses)
	require.NoError(t, err)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Equal(t, 2, usage.CompletionTokens)
	assert.Contains(t, recorder.Body.String(), `"type":"response.completed"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
}

var errInjectedCloudflareRead = errors.New("injected Cloudflare read failure")

type cloudflarePartialErrorReader struct {
	sent bool
}

func (reader *cloudflarePartialErrorReader) Read(buffer []byte) (int, error) {
	if !reader.sent {
		reader.sent = true
		return copy(buffer, "{}"), nil
	}
	return 0, errInjectedCloudflareRead
}

func TestCloudflareErrorsAndResponseBounds(t *testing.T) {
	meta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	meta.Channel.StatusCodeMapping = `{"429":"503","502":504}`
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	context, recorder := cloudflareContext()
	_, err := adaptor.DoResponse(context, cloudflareResponse(http.StatusTooManyRequests, strings.NewReader(
		`{"error":{"message":"busy","type":"rate_limit_error"}}`,
	)), meta)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.Zero(t, recorder.Body.Len())

	completion := cloudflareMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI)
	completion.Channel.StatusCodeMapping = meta.Channel.StatusCodeMapping
	adaptor.Init(completion)
	context, recorder = cloudflareContext()
	_, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(
		`{"success":false,"errors":[{"code":10001,"message":"invalid account"}]}`,
	)), completion)
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusGatewayTimeout, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "invalid account")
	assert.Zero(t, recorder.Body.Len())

	adaptor.Init(meta)
	context, recorder = cloudflareContext()
	over := bytes.Repeat([]byte{' '}, int(relaycommon.MaxUpstreamJSONBodyBytes+1))
	copy(over, []byte("{}"))
	_, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, bytes.NewReader(over)), meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Zero(t, recorder.Body.Len())

	context, recorder = cloudflareContext()
	_, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, &cloudflarePartialErrorReader{}), meta)
	assert.ErrorIs(t, err, errInjectedCloudflareRead)
	assert.Zero(t, recorder.Body.Len())

	streamMeta := cloudflareMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI)
	streamMeta.IsStream = true
	adaptor.Init(streamMeta)
	context, _ = cloudflareContext()
	oversizedEvent := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	_, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusOK, strings.NewReader(oversizedEvent)), streamMeta)
	require.ErrorContains(t, err, "maximum event")

	context, _ = cloudflareContext()
	oversizedError := strings.Repeat("x", int(relaycommon.MaxUpstreamErrorBodyBytes+1))
	_, err = adaptor.DoResponse(context, cloudflareResponse(http.StatusBadGateway, strings.NewReader(oversizedError)), meta)
	require.ErrorAs(t, err, &upstream)
	assert.ErrorIs(t, upstream.Cause, relaycommon.ErrUpstreamResponseTooLarge)
}

func TestCloudflareModelListReturnsIndependentCopy(t *testing.T) {
	first := ModelList()
	second := ModelList()
	require.Len(t, first, 33)
	first[0] = "mutated"
	assert.NotEqual(t, first[0], second[0])
}
