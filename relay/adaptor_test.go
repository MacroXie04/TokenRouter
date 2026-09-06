package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

type adaptorRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn adaptorRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestUnsupportedProviderFailsBeforeCredentialedNetworkDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var contacted atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		contacted.Store(true)
	}))
	defer upstream.Close()

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &RelayInfo{
		Channel: &model.Channel{
			Type:    int(constant.ChannelTypeDummy),
			BaseURL: upstream.URL,
			Key:     "must-not-leave-process",
		},
		Mode:      constant.RelayModeChatCompletions,
		ModelName: "model",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model:    "model",
			Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
	}

	_, err := dispatchUpstream(context, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no implemented relay adapter")
	assert.False(t, contacted.Load(), "an unsupported provider key must never be sent through a fallback adapter")
}

func TestProviderAdaptorRegistryIsExplicit(t *testing.T) {
	for _, channelType := range []constant.ChannelType{
		constant.ChannelTypeOpenAI,
		constant.ChannelTypeAzure,
		constant.ChannelTypeOllama,
		constant.ChannelTypeAnthropic,
		constant.ChannelTypeAws,
		constant.ChannelTypeGemini,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeZhipu,
		constant.ChannelTypeZhipuV4,
		constant.ChannelTypeAli,
		constant.ChannelTypeBaidu,
		constant.ChannelTypeBaiduV2,
		constant.ChannelTypeXunfei,
		constant.ChannelTypeTencent,
		constant.ChannelTypeJimeng,
		constant.ChannelTypeVolcEngine,
		constant.ChannelTypeMoonshot,
		constant.ChannelCloudflare,
		constant.ChannelTypeCohere,
		constant.ChannelTypeCoze,
		constant.ChannelTypeDify,
		constant.ChannelTypeMiniMax,
		constant.ChannelTypeMokaAI,
		constant.ChannelTypePaLM,
		constant.ChannelTypeReplicate,
		constant.ChannelTypeOpenRouter,
		constant.ChannelTypeDeepSeek,
		constant.ChannelTypeMistral,
		constant.ChannelTypeXai,
		constant.ChannelTypeSiliconFlow,
		constant.ChannelTypeJina,
		constant.ChannelTypeSubmodel,
		constant.ChannelTypeCodex,
		constant.ChannelTypeAdvancedCustom,
		constant.ChannelTypeSub2API,
		constant.ChannelTypeNewAPI,
	} {
		assert.NotNil(t, GetAdaptor(channelType), "implemented channel type %d", channelType)
	}
	for _, channelType := range []constant.ChannelType{
		constant.ChannelTypeUnknown,
		constant.ChannelTypeDummy,
	} {
		assert.Nil(t, GetAdaptor(channelType), "unimplemented channel type %d", channelType)
	}
}

func TestDirectAdaptorSchemeGate(t *testing.T) {
	adaptor := GetAdaptor(constant.ChannelTypeVolcEngine)
	_, direct := adaptor.(interface {
		DoDirectRequest(*gin.Context, string, []byte, *relaycommon.Meta) (*protocolkit.Usage, error)
	})
	require.True(t, direct)
	assert.False(t, isDirectRelayURL("https://ark.cn-beijing.volces.com/api/v3/chat/completions"))
	assert.False(t, isDirectRelayURL("https://openspeech.bytedance.com/v1/audio/speech"))
	assert.True(t, isDirectRelayURL("wss://openspeech.bytedance.com/api/v1/tts/ws_binary"))
	assert.True(t, isDirectRelayURL("ws://provider.example/socket"))
	assert.False(t, isDirectRelayURL("wss-not-really://provider.example/socket"))
	assert.False(t, isDirectRelayURL("wss://user:secret@provider.example/socket"))
}

func TestVolcEngineDirectAdaptorHTTPFallsThroughOrdinarySafeTransport(t *testing.T) {
	productionClient := relayHTTPClient
	transport, ok := productionClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialContext)
	assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(),
		reflect.ValueOf(transport.DialContext).Pointer())
	assert.Nil(t, transport.Proxy)
	assert.ErrorIs(t, productionClient.CheckRedirect(nil, nil), http.ErrUseLastResponse)

	var ordinaryCalls atomic.Int32
	relayHTTPClient = &http.Client{Transport: adaptorRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		ordinaryCalls.Add(1)
		assert.Equal(t, "https://ark.example/api/v3/chat/completions", request.URL.String())
		assert.Equal(t, "Bearer provider-secret", request.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"chatcmpl-volc","object":"chat.completion","model":"endpoint",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { relayHTTPClient = productionClient })
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &RelayInfo{
		Channel: &model.Channel{Type: int(constant.ChannelTypeVolcEngine),
			BaseURL: "https://ark.example", Key: "provider-secret"},
		Mode: constant.RelayModeChatCompletions, ModelName: "endpoint",
		Request: &protocolkit.GeneralOpenAIRequest{
			Model: "endpoint", Messages: []protocolkit.Message{{Role: "user", Content: "hello"}},
		},
	}
	usage, err := dispatchUpstream(context, info)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 2, usage.TotalTokens)
	assert.Equal(t, int32(1), ordinaryCalls.Load(),
		"an HTTP URL from a DirectAdaptor must use the ordinary hardened client")
}
