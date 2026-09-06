package sub2api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func TestAdaptorInheritsNewAPIGatewayContract(t *testing.T) {
	meta := &relaycommon.Meta{
		Channel: &model.Channel{Type: int(constant.ChannelTypeSub2API)},
		Mode:    constant.RelayModeResponsesCompact, RequestPath: "/v1/responses/compact",
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
