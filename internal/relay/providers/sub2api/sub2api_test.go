package sub2api

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"testing"
)

func TestAdaptorInheritsNewAPIGatewayContract(t *testing.T) {
	meta := &relaycommon.Meta{
		Channel: &model.Channel{Type: int(channelcatalog.ChannelTypeSub2API)},
		Mode:    channelcatalog.RelayModeResponsesCompact, RequestPath: "/v1/responses/compact",
		BaseURL: "https://sub2api.example", APIKey: "secret", ModelName: "model",
		Request: &protocolkit.GeneralOpenAIRequest{Model: "model"},
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)
	requestURL, err := adaptor.GetRequestURL(meta)
	require.NoError(t, err)
	assert.Equal(t, "https://sub2api.example/v1/responses/compact", requestURL)
	assert.Equal(t, ChannelName, "sub2api")
	assert.Empty(t, ModelList())
}
