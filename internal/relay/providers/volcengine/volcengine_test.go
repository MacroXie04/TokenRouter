package volcengine

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExactReferenceCatalog(t *testing.T) {
	expected := []string{
		"Doubao-pro-128k", "Doubao-pro-32k", "Doubao-pro-4k",
		"Doubao-lite-128k", "Doubao-lite-32k", "Doubao-lite-4k",
		"Doubao-embedding", "doubao-seedream-4-0-250828", "seedream-4-0-250828",
		"doubao-seedance-1-0-pro-250528", "seedance-1-0-pro-250528",
		"doubao-seed-1-6-thinking-250715", "seed-1-6-thinking-250715",
	}
	assert.Equal(t, "volcengine", ChannelName)
	assert.Equal(t, expected, ModelList())
	models := ModelList()
	models[0] = "mutated"
	assert.Equal(t, expected, ModelList())
}

func TestRequestURLContract(t *testing.T) {
	tests := []struct {
		name, base, model string
		mode              channelcatalog.RelayMode
		format            channelcatalog.RelayFormat
		want              string
	}{
		{"chat", "", "ep-1", channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "https://ark.cn-beijing.volces.com/api/v3/chat/completions"},
		{"bot chat", "https://ark.example/root", "bot-1", channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "https://ark.example/root/api/v3/bots/chat/completions"},
		{"embedding", "https://ark.example", "ep", channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding, "https://ark.example/api/v3/embeddings"},
		{"image generation", "https://ark.example", "ep", channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayFormatOpenAIImage, "https://ark.example/api/v3/images/generations"},
		{"image edit shares generations", "https://ark.example", "ep", channelcatalog.RelayModeImagesEdits, channelcatalog.RelayFormatOpenAIImage, "https://ark.example/api/v3/images/generations"},
		{"rerank", "https://ark.example", "ep", channelcatalog.RelayModeRerank, channelcatalog.RelayFormatRerank, "https://ark.example/api/v3/rerank"},
		{"responses", "https://ark.example", "ep", channelcatalog.RelayModeResponses, channelcatalog.RelayFormatOpenAIResponses, "https://ark.example/api/v3/responses"},
		{"default tts", "", "voice", channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio, defaultTTSURL},
		{"custom tts", "https://tts.example/root", "voice", channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio, "https://tts.example/root/v1/audio/speech"},
		{"coding openai", codingPlanAlias, "ep", channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions"},
		{"coding claude", codingPlanAlias, "ep", channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatClaude, "https://ark.cn-beijing.volces.com/api/coding/v1/messages"},
		{"standard claude conversion", "https://ark.example", "ep", channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatClaude, "https://ark.example/api/v3/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := volcMeta(test.mode, test.format, test.base, test.model)
			adaptor := &Adaptor{}
			adaptor.Init(meta)
			got, err := adaptor.GetRequestURL(meta)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestRequestURLRejectsUnsupportedOrUnsafeCoordinates(t *testing.T) {
	tests := []*relaycommon.Meta{
		volcMeta(channelcatalog.RelayModeCompletions, channelcatalog.RelayFormatOpenAI, "", "ep"),
		volcMeta(channelcatalog.RelayModeEmbeddings, channelcatalog.RelayFormatEmbedding, codingPlanAlias, "ep"),
		volcMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "file:///tmp/provider", "ep"),
		volcMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "https://user:pass@example.com", "ep"),
		volcMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "https://example.com?secret=1", "ep"),
		volcMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "", ""),
	}
	for _, meta := range tests {
		adaptor := &Adaptor{}
		adaptor.Init(meta)
		_, err := adaptor.GetRequestURL(meta)
		require.Error(t, err)
	}
}

func TestHeadersAndThinkingConversion(t *testing.T) {
	meta := volcMeta(channelcatalog.RelayModeChatCompletions, channelcatalog.RelayFormatOpenAI, "", "deepseek-r1-thinking")
	meta.APIKey = "ark-secret"
	meta.Request.Extra = map[string]any{"model": "client", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var converted map[string]any
	require.NoError(t, protocolkit.UnmarshalJSON(body, &converted))
	assert.Equal(t, "deepseek-r1", converted["model"])
	assert.Equal(t, "enabled", converted["thinking"].(map[string]any)["type"])

	request := httptest.NewRequest(http.MethodPost, "https://ark.example/api/v3/chat/completions", nil)
	require.NoError(t, adaptor.SetupRequestHeader(request, meta))
	assert.Equal(t, "Bearer ark-secret", request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))

	meta.APIKey = "bad\nkey"
	require.Error(t, adaptor.SetupRequestHeader(request, meta))
}

func TestTTSConversionExactDefaultsAndMetadataBounds(t *testing.T) {
	meta := volcMeta(channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio, "", "tts-model")
	meta.OriginalModelName = "client-tts"
	meta.APIKey = "app-123|token-456"
	meta.Request.Extra = map[string]any{
		"input": "hello", "voice": "alloy", "response_format": "opus", "speed": 1.25,
	}
	body, err := buildTTSRequest(meta)
	require.NoError(t, err)
	var payload ttsRequest
	require.NoError(t, decodeStrictTTSJSON(body, &payload))
	assert.Equal(t, "app-123", payload.App.AppID)
	assert.Equal(t, "token-456", payload.App.Token)
	assert.Equal(t, "volcano_tts", payload.App.Cluster)
	assert.Equal(t, "openai_relay_user", payload.User.UID)
	assert.Equal(t, "zh_male_M392_conversation_wvae_bigtts", payload.Audio.VoiceType)
	assert.Equal(t, "ogg_opus", payload.Audio.Encoding)
	assert.Equal(t, 1.25, payload.Audio.SpeedRatio)
	assert.Equal(t, 24000, payload.Audio.Rate)
	assert.Equal(t, "submit", payload.Request.Operation)
	assert.Equal(t, "client-tts", payload.Request.Model)
	assert.NotEmpty(t, payload.Request.RequestID)

	meta.Request.Extra["metadata"] = map[string]any{"audio": map[string]any{"rate": 48000}}
	body, err = buildTTSRequest(meta)
	require.NoError(t, err)
	require.NoError(t, decodeStrictTTSJSON(body, &payload))
	assert.Equal(t, 48000, payload.Audio.Rate)

	meta.Request.Extra["metadata"] = map[string]any{"unknown": true}
	_, err = buildTTSRequest(meta)
	require.Error(t, err)
	meta.Request.Extra["metadata"] = nil
	meta.Request.Extra["input"] = strings.Repeat("x", maxTTSInputBytes+1)
	_, err = buildTTSRequest(meta)
	require.Error(t, err)
}

func TestTTSHTTPResponseIsBoundedAndDecoded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	meta := volcMeta(channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio, "https://tts.example", "tts-model")
	meta.APIKey = "app|token"
	meta.PromptTokens = 7
	meta.Request.Extra = map[string]any{"input": "hello", "voice": "nova", "response_format": "wav"}
	audio := []byte("RIFF-audio")
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"reqid":"id","code":3000,"message":"ok","sequence":-1,"data":"` + base64.StdEncoding.EncodeToString(audio) + `"}`))}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	usage, err := (&Adaptor{}).DoResponse(context, response, meta)
	require.NoError(t, err)
	assert.Equal(t, audio, recorder.Body.Bytes())
	assert.Equal(t, "audio/wav", recorder.Header().Get("Content-Type"))
	assert.Equal(t, 7, usage.PromptTokens)
	assert.Equal(t, 7, usage.TotalTokens)

	oversized := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.LimitReader(
		strings.NewReader(strings.Repeat("x", 1024)), maxTTSJSONBytes+1))}
	_, err = (&Adaptor{}).DoResponse(context, oversized, meta)
	require.Error(t, err)
}

func TestBinaryTTSFrameCodec(t *testing.T) {
	payload := []byte(`{"request":"ok"}`)
	frame, err := marshalTTSClientFrame(payload)
	require.NoError(t, err)
	assert.Equal(t, []byte{0x11, 0x10, 0x10, 0}, frame[:4])
	assert.Equal(t, uint32(len(payload)), binary.BigEndian.Uint32(frame[4:8]))
	assert.Equal(t, payload, frame[8:])

	audioFrame := serverTTSFrame(0x0b, 3, -1, 0, []byte("audio"))
	parsed, err := parseTTSServerFrame(audioFrame)
	require.NoError(t, err)
	assert.Equal(t, ttsFrameAudio, parsed.kind)
	assert.Equal(t, int32(-1), parsed.sequence)
	assert.Equal(t, []byte("audio"), parsed.payload)

	errorFrame := serverTTSFrame(0x0f, 0, 0, 4501, []byte(`{"message":"rejected"}`))
	parsed, err = parseTTSServerFrame(errorFrame)
	require.NoError(t, err)
	assert.Equal(t, ttsFrameError, parsed.kind)
	assert.Equal(t, uint32(4501), parsed.errorCode)

	_, err = parseTTSServerFrame(audioFrame[:len(audioFrame)-1])
	require.Error(t, err)
}

func TestDirectTTSWebSocketUsesFixedEndpointAndBoundedProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer;token", request.Header.Get("Authorization"))
		connection, err := upgrader.Upgrade(writer, request, nil)
		require.NoError(t, err)
		defer connection.Close()
		kind, frame, err := connection.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, websocket.BinaryMessage, kind)
		assert.Equal(t, byte(0x11), frame[0])
		require.NoError(t, connection.WriteMessage(websocket.BinaryMessage,
			serverTTSFrame(0x0b, 3, -1, 0, []byte("audio"))))
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	adaptor := &Adaptor{DialContext: func(ctx context.Context, _ string, header http.Header) (*websocket.Conn, *http.Response, error) {
		return dialer.DialContext(ctx, wsURL, header)
	}}
	meta := volcMeta(channelcatalog.RelayModeAudioSpeech, channelcatalog.RelayFormatOpenAIAudio, "", "tts-model")
	meta.Context = context.Background()
	meta.APIKey = "app|token"
	meta.PromptTokens = 3
	meta.Request.Extra = map[string]any{"input": "hello", "voice": "echo", "response_format": "mp3"}
	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	usage, err := adaptor.DoDirectRequest(context, defaultTTSURL, body, meta)
	require.NoError(t, err)
	assert.Equal(t, "audio", recorder.Body.String())
	assert.Equal(t, "audio/mpeg", recorder.Header().Get("Content-Type"))
	assert.Equal(t, 3, usage.TotalTokens)
}

func volcMeta(mode channelcatalog.RelayMode, format channelcatalog.RelayFormat, base, modelName string) *relaycommon.Meta {
	request := &protocolkit.GeneralOpenAIRequest{Model: modelName, Extra: map[string]any{"model": modelName}}
	return &relaycommon.Meta{Context: context.Background(), Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeVolcEngine)},
		Mode: mode, Format: format, BaseURL: base, ModelName: modelName, OriginalModelName: modelName,
		Request: request, APIKey: "key"}
}

func serverTTSFrame(messageType, flags byte, sequence int32, errorCode uint32, payload []byte) []byte {
	extra := 0
	if (messageType == 0x0b || messageType == 0x09 || messageType == 0x0c) && (flags == 1 || flags == 3) {
		extra = 4
	}
	if messageType == 0x0f {
		extra = 4
	}
	frame := make([]byte, 8+extra+len(payload))
	frame[0], frame[1], frame[2], frame[3] = 0x11, messageType<<4|flags, 0x10, 0
	offset := 4
	if extra > 0 {
		if messageType == 0x0f {
			binary.BigEndian.PutUint32(frame[offset:offset+4], errorCode)
		} else {
			binary.BigEndian.PutUint32(frame[offset:offset+4], uint32(sequence))
		}
		offset += 4
	}
	binary.BigEndian.PutUint32(frame[offset:offset+4], uint32(len(payload)))
	copy(frame[offset+4:], payload)
	return frame
}
