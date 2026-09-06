package claude

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestConvertRequestPreservesConfiguredZeroDefaultMaxTokens(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "claude-policy.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Option{}))
	model.DB = database
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOption(setting.ClaudeDefaultMaxTokensOption,
		`{"default":8192,"claude-cache":0}`))

	request := &protocolkit.GeneralOpenAIRequest{
		Model: "claude-cache",
		Messages: []protocolkit.Message{{
			Role: "user", Content: "pre-warm",
		}},
	}
	meta := &relaycommon.Meta{
		Mode:              channelcatalog.RelayModeChatCompletions,
		Format:            channelcatalog.RelayFormatOpenAI,
		OriginalModelName: request.Model,
		ModelName:         request.Model,
		Request:           request,
	}
	adaptor := &Adaptor{}
	adaptor.Init(meta)

	body, err := adaptor.ConvertRequest(meta)
	require.NoError(t, err)
	var converted protocolkit.ClaudeRequest
	require.NoError(t, protocolkit.UnmarshalJSON(body, &converted))
	assert.Equal(t, "claude-cache", converted.Model)
	assert.Zero(t, converted.MaxTokens)
}
