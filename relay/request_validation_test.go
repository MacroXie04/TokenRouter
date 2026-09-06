package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

func TestValidateAndNormalizeRelayRequestBoundsModeSpecificInputs(t *testing.T) {
	t.Run("image count defaults and is bounded", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "image", Extra: map[string]any{}}
		require.NoError(t, validateAndNormalizeRelayRequest(constant.RelayModeImagesGenerations, req, "application/json"))
		require.NotNil(t, req.N)
		assert.Equal(t, 1, *req.N)
		assert.Equal(t, 1, req.Extra["n"])

		negative := -1
		req.N = &negative
		assert.Error(t, validateAndNormalizeRelayRequest(constant.RelayModeImagesGenerations, req, "application/json"))
		tooMany := maxImageCount + 1
		req.N = &tooMany
		assert.Error(t, validateAndNormalizeRelayRequest(constant.RelayModeImagesGenerations, req, "application/json"))
	})

	t.Run("responses requires input", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "gpt", Extra: map[string]any{}}
		assert.Error(t, validateAndNormalizeRelayRequest(constant.RelayModeResponses, req, "application/json"))
		req.Extra["input"] = "hello"
		require.NoError(t, validateAndNormalizeRelayRequest(constant.RelayModeResponses, req, "application/json"))
	})

	t.Run("rerank requires query and documents", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "rerank", Extra: map[string]any{}}
		assert.Error(t, validateAndNormalizeRelayRequest(constant.RelayModeRerank, req, "application/json"))
		req.Extra["query"] = "search"
		assert.Error(t, validateAndNormalizeRelayRequest(constant.RelayModeRerank, req, "application/json"))
		req.Extra["documents"] = []any{"one"}
		require.NoError(t, validateAndNormalizeRelayRequest(constant.RelayModeRerank, req, "application/json"))
	})

	t.Run("multipart is limited to file endpoints", func(t *testing.T) {
		req := &protocolkit.GeneralOpenAIRequest{Model: "gpt", Extra: map[string]any{}}
		assert.Error(t, validateAndNormalizeRelayRequest(constant.RelayModeChatCompletions, req, "multipart/form-data; boundary=x"))
	})
}
