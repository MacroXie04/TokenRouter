package minimax

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func miniMaxMeta(mode constant.RelayMode, format constant.RelayFormat, modelName string) *relaycommon.Meta {
	request := &protocolkit.GeneralOpenAIRequest{
		Model: modelName,
		Extra: map[string]any{"model": modelName},
	}
	return &relaycommon.Meta{
		Channel:   &model.Channel{Type: int(constant.ChannelTypeMiniMax)},
		Mode:      mode,
		Format:    format,
		ModelName: modelName,
		APIKey:    "sk-minimax-test",
		Request:   request,
	}
}

func miniMaxHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestMiniMaxURLsHeadersModesAndCatalog(t *testing.T) {
	tests := []struct {
		name   string
		mode   constant.RelayMode
		format constant.RelayFormat
		stream bool
		base   string
		want   string
	}{
		{name: "OpenAI chat default", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatOpenAI, want: "https://api.minimax.chat/v1/text/chatcompletion_v2"},
		{name: "OpenAI stream custom", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatOpenAI, stream: true, base: "https://minimax.example/gateway", want: "https://minimax.example/gateway/v1/text/chatcompletion_v2"},
		{name: "Anthropic messages", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatClaude, base: "https://minimax.example", want: "https://minimax.example/anthropic/v1/messages"},
		{name: "image", mode: constant.RelayModeImagesGenerations, format: constant.RelayFormatOpenAIImage, want: "https://api.minimax.chat/v1/image_generation"},
		{name: "speech", mode: constant.RelayModeAudioSpeech, format: constant.RelayFormatOpenAIAudio, want: "https://api.minimax.chat/v1/t2a_v2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := miniMaxMeta(test.mode, test.format, "MiniMax-M2.7")
			meta.BaseURL = test.base
			meta.IsStream = test.stream
			meta.Request.Stream = test.stream
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			got, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)

			req := httptest.NewRequest(http.MethodPost, got, nil)
			req.Header.Set("x-api-key", "client-key")
			req.Header.Set("anthropic-version", "client-version")
			require.NoError(t, adaptor.SetupRequestHeader(req, meta))
			assert.Equal(t, "Bearer sk-minimax-test", req.Header.Get("Authorization"))
			assert.Empty(t, req.Header.Get("x-api-key"))
			assert.Empty(t, req.Header.Get("anthropic-version"))
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			if test.stream {
				assert.Equal(t, "text/event-stream", req.Header.Get("Accept"))
			} else {
				assert.Equal(t, "application/json", req.Header.Get("Accept"))
			}
		})
	}

	for _, test := range []struct {
		name   string
		mode   constant.RelayMode
		format constant.RelayFormat
		stream bool
	}{
		{name: "embeddings", mode: constant.RelayModeEmbeddings, format: constant.RelayFormatEmbedding},
		{name: "image stream", mode: constant.RelayModeImagesGenerations, format: constant.RelayFormatOpenAIImage, stream: true},
		{name: "speech stream", mode: constant.RelayModeAudioSpeech, format: constant.RelayFormatOpenAIAudio, stream: true},
		{name: "image edit", mode: constant.RelayModeImagesEdits, format: constant.RelayFormatOpenAIImage},
		{name: "audio transcription", mode: constant.RelayModeAudioTranscription, format: constant.RelayFormatOpenAIAudio},
		{name: "gemini", mode: constant.RelayModeChatCompletions, format: constant.RelayFormatGemini},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			meta := miniMaxMeta(test.mode, test.format, "MiniMax-M2.7")
			meta.IsStream = test.stream
			meta.Request.Stream = test.stream
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			_, err := adaptor.GetRequestURL(meta)
			assert.Error(t, err)
		})
	}

	for _, rawBase := range []string{
		"ftp://minimax.example", "https://user:pass@minimax.example", "https:///missing", "https://minimax.example?secret=x", "https://minimax.example#fragment",
	} {
		meta := miniMaxMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI, "MiniMax-M2.7")
		meta.BaseURL = rawBase
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		assert.Error(t, err, rawBase)
	}

	meta := miniMaxMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI, "MiniMax-M2.7")
	meta.APIKey = "bad\r\nkey"
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	assert.Error(t, adaptor.SetupRequestHeader(httptest.NewRequest(http.MethodPost, "https://minimax.example", nil), meta))
	for _, invalidKey := range []string{"", strings.Repeat("k", maxMiniMaxCredentialBytes+1)} {
		meta.APIKey = invalidKey
		assert.Error(t, adaptor.SetupRequestHeader(httptest.NewRequest(http.MethodPost, "https://minimax.example", nil), meta))
	}
	meta.BaseURL = "https://" + strings.Repeat("x", maxMiniMaxBaseURLBytes)
	_, err := adaptor.GetRequestURL(meta)
	assert.Error(t, err)
	for _, invalidModel := range []string{" MiniMax-M2.7", "MiniMax\tM2.7", strings.Repeat("m", maxMiniMaxModelBytes+1)} {
		meta.BaseURL = "https://minimax.example"
		meta.ModelName = invalidModel
		meta.Request.Model = invalidModel
		_, err := adaptor.ConvertRequest(meta)
		assert.Error(t, err)
	}

	first := ModelList()
	require.Contains(t, first, "MiniMax-M2.7")
	require.Contains(t, first, "image-01")
	require.Contains(t, first, "speech-02-hd")
	first[0] = "mutated"
	assert.Equal(t, "abab6.5-chat", ModelList()[0])
}

func TestMiniMaxRequestConversionsUseMappedModelAndProviderFields(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		meta := miniMaxMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI, "MiniMax-M2.7-highspeed")
		meta.Request.Messages = []protocolkit.Message{{Role: "user", Content: "hello"}}
		meta.Request.Extra = map[string]any{
			"model": "client-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
			"provider_extension": "kept", "group": "dashboard-only",
		}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		body, err := adaptor.ConvertRequest(meta)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "MiniMax-M2.7-highspeed", payload["model"])
		assert.Equal(t, "kept", payload["provider_extension"])
		assert.NotContains(t, payload, "group")
	})

	t.Run("image", func(t *testing.T) {
		n := 2
		meta := miniMaxMeta(constant.RelayModeImagesGenerations, constant.RelayFormatOpenAIImage, "image-01-live")
		meta.Request.Prompt = "a fox in snowfall"
		meta.Request.N = &n
		meta.Request.Extra = map[string]any{
			"model": "client-image", "prompt": "a fox in snowfall", "n": float64(2),
			"size": "1536x1024", "response_format": "b64_json",
			"prompt_optimizer": true, "watermark": false,
		}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		body, err := adaptor.ConvertRequest(meta)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"model":"image-01-live","prompt":"a fox in snowfall","aspect_ratio":"3:2",
			"response_format":"base64","n":2,"prompt_optimizer":true,"aigc_watermark":false
		}`, string(body))
	})

	t.Run("speech with metadata", func(t *testing.T) {
		meta := miniMaxMeta(constant.RelayModeAudioSpeech, constant.RelayFormatOpenAIAudio, "speech-02-hd")
		meta.Request.Extra = map[string]any{
			"model": "client-speech", "input": "hello", "voice": "English_Graceful_Lady",
			"response_format": "wav", "speed": 1.25,
		}
		meta.Request.Metadata = map[string]any{
			"model": "must-not-override-mapping", "text": "must-not-override-input",
			"language_boost": "English", "output_format": "hex",
			"voice_setting": map[string]any{"voice_id": "custom-voice", "vol": 2.0},
			"audio_setting": map[string]any{"sample_rate": 32000},
		}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		body, err := adaptor.ConvertRequest(meta)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "speech-02-hd", payload["model"])
		assert.Equal(t, "hello", payload["text"])
		assert.Equal(t, "English", payload["language_boost"])
		assert.Equal(t, "hex", payload["output_format"])
		voice := payload["voice_setting"].(map[string]any)
		assert.Equal(t, "custom-voice", voice["voice_id"])
		assert.EqualValues(t, 1.25, voice["speed"])
		assert.EqualValues(t, 2, voice["vol"])
		audio := payload["audio_setting"].(map[string]any)
		assert.Equal(t, "wav", audio["format"])
		assert.EqualValues(t, 32000, audio["sample_rate"])
	})

	t.Run("invalid image and speech inputs", func(t *testing.T) {
		tooMany := maxMiniMaxImageCount + 1
		image := miniMaxMeta(constant.RelayModeImagesGenerations, constant.RelayFormatOpenAIImage, "image-01")
		image.Request.Prompt = "prompt"
		image.Request.N = &tooMany
		image.Request.Extra = map[string]any{"prompt": "prompt"}
		adaptor := &Adaptor{}
		adaptor.Init(image)
		_, err := adaptor.ConvertRequest(image)
		assert.Error(t, err)

		speech := miniMaxMeta(constant.RelayModeAudioSpeech, constant.RelayFormatOpenAIAudio, "speech-02-hd")
		speech.Request.Extra = map[string]any{"input": "hello", "voice": "voice", "speed": 9.0}
		adaptor.Init(speech)
		_, err = adaptor.ConvertRequest(speech)
		assert.Error(t, err)

		image.RawBody = bytes.Repeat([]byte("x"), maxMiniMaxRequestBodyBytes+1)
		image.Request.N = nil
		adaptor.Init(image)
		_, err = adaptor.ConvertRequest(image)
		assert.Error(t, err)
	})
}

func TestMiniMaxChatImageAndSpeechResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("chat usage", func(t *testing.T) {
		meta := miniMaxMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI, "MiniMax-M2.7")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		usage, err := adaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK,
			`{"id":"chat-1","object":"chat.completion","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`), meta)
		require.NoError(t, err)
		require.NotNil(t, usage)
		assert.Equal(t, 7, usage.PromptTokens)
		assert.Equal(t, 3, usage.CompletionTokens)
		assert.Equal(t, 10, usage.TotalTokens)
		assert.Contains(t, recorder.Body.String(), `"chat-1"`)
	})

	t.Run("image URL and base64 conversion", func(t *testing.T) {
		meta := miniMaxMeta(constant.RelayModeImagesGenerations, constant.RelayFormatOpenAIImage, "image-01")
		meta.PromptTokens = 5
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		imageBytes := base64.StdEncoding.EncodeToString([]byte("image"))
		usage, err := adaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK,
			`{"data":{"image_urls":["https://cdn.example/image.png?sig=x"],"image_base64":["`+imageBytes+`"]},"metadata":{"request_id":"r1"},"base_resp":{"status_code":0}}`), meta)
		require.NoError(t, err)
		assert.Equal(t, 5, usage.PromptTokens)
		assert.Equal(t, 2, usage.CompletionTokens)
		assert.Equal(t, 2, usage.CompletionTokensDetails.ImageTokens)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		data := payload["data"].([]any)
		require.Len(t, data, 2)
		assert.Equal(t, "https://cdn.example/image.png?sig=x", data[0].(map[string]any)["url"])
		assert.Equal(t, imageBytes, data[1].(map[string]any)["b64_json"])
		assert.Equal(t, "r1", payload["metadata"].(map[string]any)["request_id"])
		assert.NotContains(t, payload, "base_resp")
	})

	t.Run("speech hex", func(t *testing.T) {
		meta := miniMaxMeta(constant.RelayModeAudioSpeech, constant.RelayFormatOpenAIAudio, "speech-02-hd")
		meta.PromptTokens = 4
		meta.Request.Extra = map[string]any{"response_format": "wav"}
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		usage, err := adaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK,
			`{"data":{"audio":"68656c6c6f","status":0},"extra_info":{"usage_characters":11},"base_resp":{"status_code":0}}`), meta)
		require.NoError(t, err)
		assert.Equal(t, "hello", recorder.Body.String())
		assert.Equal(t, "audio/wav", recorder.Header().Get("Content-Type"))
		assert.Equal(t, 4, usage.PromptTokens)
		assert.Zero(t, usage.CompletionTokens)
		assert.Equal(t, 11, usage.TotalTokens)
	})

	t.Run("speech URL redirect", func(t *testing.T) {
		meta := miniMaxMeta(constant.RelayModeAudioSpeech, constant.RelayFormatOpenAIAudio, "speech-02-hd")
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		_, err := adaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK,
			`{"data":{"audio":"https://cdn.example/audio.mp3?sig=x"},"extra_info":{"usage_characters":5},"base_resp":{"status_code":0}}`), meta)
		require.NoError(t, err)
		assert.Equal(t, http.StatusFound, recorder.Code)
		assert.Equal(t, "https://cdn.example/audio.mp3?sig=x", recorder.Header().Get("Location"))
	})
}

func TestMiniMaxStreamUsageErrorsAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := miniMaxMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI, "MiniMax-M2.7")
	meta.IsStream = true
	meta.Request.Stream = true
	meta.PromptTokens = 5
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK, strings.Join([]string{
		`data: {"id":"stream-1","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`,
		`data: [DONE]`, "",
	}, "\n\n")), meta)
	require.NoError(t, err)
	assert.Equal(t, 8, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
	assert.Equal(t, 11, usage.TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"content":"hello"`)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))

	meta.Channel.StatusCodeMapping = `{"502":"503","400":"429"}`
	adaptor.Init(meta)
	errorRecorder := httptest.NewRecorder()
	errorContext, _ := gin.CreateTestContext(errorRecorder)
	partialUsage, err := adaptor.DoResponse(errorContext, miniMaxHTTPResponse(http.StatusOK,
		`data: {"usage":{"prompt_tokens":2,"completion_tokens":1},"choices":[]}`+"\n\n"+
			`data: {"error":{"message":"stream failed","type":"provider_error"}}`+"\n\n"), meta)
	require.Error(t, err)
	require.NotNil(t, partialUsage)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)

	adaptor.Init(meta)
	oversizedRecorder := httptest.NewRecorder()
	oversizedContext, _ := gin.CreateTestContext(oversizedRecorder)
	_, err = adaptor.DoResponse(oversizedContext, miniMaxHTTPResponse(http.StatusOK,
		"data: "+strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1)+"\n"), meta)
	assert.Error(t, err)
}

func TestMiniMaxProviderErrorsMalformedMediaAndResponseBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)

	imageMeta := miniMaxMeta(constant.RelayModeImagesGenerations, constant.RelayFormatOpenAIImage, "image-01")
	imageMeta.Channel.StatusCodeMapping = `{"400":"503"}`
	imageAdaptor := &Adaptor{}
	imageAdaptor.Init(imageMeta)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, err := imageAdaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK,
		`{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`), imageMeta)
	require.Error(t, err)
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
	assert.Contains(t, upstream.Body, "insufficient balance")

	for _, body := range []string{
		`{"data":{"image_urls":[]},"base_resp":{"status_code":0}}`,
		`{"data":{"image_urls":["file:///private/image"]},"base_resp":{"status_code":0}}`,
		`{"data":{"image_base64":["not-base64"]},"base_resp":{"status_code":0}}`,
	} {
		imageAdaptor.Init(imageMeta)
		context, _ = gin.CreateTestContext(httptest.NewRecorder())
		_, err = imageAdaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK, body), imageMeta)
		assert.Error(t, err, body)
	}

	speechMeta := miniMaxMeta(constant.RelayModeAudioSpeech, constant.RelayFormatOpenAIAudio, "speech-02-hd")
	speechAdaptor := &Adaptor{}
	for _, body := range []string{
		`{"data":{"audio":"javascript:alert(1)"},"base_resp":{"status_code":0}}`,
		`{"data":{"audio":"xyz"},"base_resp":{"status_code":0}}`,
		`{"data":{"audio":"00","status":2},"base_resp":{"status_code":0}}`,
		`{"data":{"audio":"00"},"extra_info":{"usage_characters":-1},"base_resp":{"status_code":0}}`,
	} {
		speechAdaptor.Init(speechMeta)
		context, _ = gin.CreateTestContext(httptest.NewRecorder())
		_, err = speechAdaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK, body), speechMeta)
		assert.Error(t, err, body)
	}

	chatMeta := miniMaxMeta(constant.RelayModeChatCompletions, constant.RelayFormatOpenAI, "MiniMax-M2.7")
	chatAdaptor := &Adaptor{}
	chatAdaptor.Init(chatMeta)
	context, _ = gin.CreateTestContext(httptest.NewRecorder())
	tooLarge := io.NopCloser(io.LimitReader(&repeatReader{value: ' '}, relaycommon.MaxUpstreamJSONBodyBytes+1))
	_, err = chatAdaptor.DoResponse(context, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: tooLarge}, chatMeta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)

	chatAdaptor.Init(chatMeta)
	context, _ = gin.CreateTestContext(httptest.NewRecorder())
	_, err = chatAdaptor.DoResponse(context, miniMaxHTTPResponse(http.StatusOK,
		`{"base_resp":{"status_code":1000,"status_msg":"chat rejected"}}`), chatMeta)
	assert.Error(t, err)

	var injected = errors.New("injected read failure")
	chatAdaptor.Init(chatMeta)
	context, _ = gin.CreateTestContext(httptest.NewRecorder())
	_, err = chatAdaptor.DoResponse(context, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(&errorReader{err: injected})}, chatMeta)
	assert.ErrorIs(t, err, injected)
}

type repeatReader struct{ value byte }

func (r *repeatReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = r.value
	}
	return len(buffer), nil
}

type errorReader struct{ err error }

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }
