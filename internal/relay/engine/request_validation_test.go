package engine

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"testing"
)

func TestValidateAndNormalizeRelayRequestBoundsModeSpecificInputs(t *testing.T) {
	t.Run("image count defaults and is bounded", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "image", Extra: map[string]any{}}
		require.NoError(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeImagesGenerations, req, "application/json"))
		require.NotNil(t, req.N)
		assert.Equal(t, 1, *req.N)
		assert.Equal(t, 1, req.Extra["n"])

		negative := -1
		req.N = &negative
		assert.Error(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeImagesGenerations, req, "application/json"))
		tooMany := maxImageCount + 1
		req.N = &tooMany
		assert.Error(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeImagesGenerations, req, "application/json"))
	})

	t.Run("responses requires input", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "gpt", Extra: map[string]any{}}
		assert.Error(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeResponses, req, "application/json"))
		req.Extra["input"] = "hello"
		require.NoError(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeResponses, req, "application/json"))
	})

	t.Run("rerank requires query and documents", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "rerank", Extra: map[string]any{}}
		assert.Error(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeRerank, req, "application/json"))
		req.Extra["query"] = "search"
		assert.Error(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeRerank, req, "application/json"))
		req.Extra["documents"] = []any{"one"}
		require.NoError(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeRerank, req, "application/json"))
	})

	t.Run("multipart is limited to file endpoints", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "gpt", Extra: map[string]any{}}
		assert.Error(t, validateAndNormalizeRelayRequest(channelcatalog.RelayModeChatCompletions, req, "multipart/form-data; boundary=x"))
	})
}
