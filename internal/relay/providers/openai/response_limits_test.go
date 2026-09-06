package openai

import (
	"bytes"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errInjectedOpenAIRead = errors.New("injected OpenAI read failure")

type openAIPartialErrorReader struct {
	sent bool
}

func (r *openAIPartialErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "{}"), nil
	}
	return 0, errInjectedOpenAIRead
}

func openAITestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	return ctx, recorder
}

func openAITestResponse(reader io.Reader) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader)}
}

func paddedOpenAIJSON(size int64) []byte {
	body := bytes.Repeat([]byte{' '}, int(size))
	copy(body, "{}")
	return body
}

func TestOpenAIBufferedResponseBoundaryAndReadFailure(t *testing.T) {
	adaptor := &Adaptor{Mode: channelcatalog.RelayModeChatCompletions}
	meta := &relaycommon.Meta{}

	ctx, recorder := openAITestContext()
	exact := paddedOpenAIJSON(relaycommon.MaxUpstreamJSONBodyBytes)
	_, err := adaptor.DoResponse(ctx, openAITestResponse(bytes.NewReader(exact)), meta)
	require.NoError(t, err)
	assert.Len(t, recorder.Body.Bytes(), int(relaycommon.MaxUpstreamJSONBodyBytes))

	ctx, recorder = openAITestContext()
	over := paddedOpenAIJSON(relaycommon.MaxUpstreamJSONBodyBytes + 1)
	_, err = adaptor.DoResponse(ctx, openAITestResponse(bytes.NewReader(over)), meta)
	assert.ErrorIs(t, err, relaycommon.ErrUpstreamResponseTooLarge)
	assert.Zero(t, recorder.Body.Len())

	for _, mode := range []channelcatalog.RelayMode{
		channelcatalog.RelayModeChatCompletions,
		channelcatalog.RelayModeResponsesCompact,
		channelcatalog.RelayModeAlphaSearch,
	} {
		ctx, recorder = openAITestContext()
		adaptor.Mode = mode
		_, err = adaptor.DoResponse(ctx, openAITestResponse(&openAIPartialErrorReader{}), meta)
		assert.ErrorIs(t, err, errInjectedOpenAIRead, mode)
		assert.Zero(t, recorder.Body.Len(), mode)
	}
}

func TestOpenAIResponseLimitSelection(t *testing.T) {
	adaptor := &Adaptor{}
	for _, mode := range []channelcatalog.RelayMode{channelcatalog.RelayModeEmbeddings, channelcatalog.RelayModeImagesGenerations, channelcatalog.RelayModeImagesEdits} {
		adaptor.Mode = mode
		assert.Equal(t, relaycommon.MaxUpstreamLargeJSONBodyBytes, adaptor.bufferedResponseLimit())
	}
	adaptor.Mode = channelcatalog.RelayModeAudioSpeech
	assert.Equal(t, relaycommon.MaxUpstreamBinaryBodyBytes, adaptor.bufferedResponseLimit())
	adaptor.Mode = channelcatalog.RelayModeChatCompletions
	assert.Equal(t, relaycommon.MaxUpstreamJSONBodyBytes, adaptor.bufferedResponseLimit())
}

func TestOpenAIStreamRejectsOversizedEvent(t *testing.T) {
	adaptor := &Adaptor{Mode: channelcatalog.RelayModeChatCompletions}
	ctx, _ := openAITestContext()
	line := "data: " + strings.Repeat("x", relaycommon.MaxUpstreamSSEEventBytes+1) + "\n"
	_, err := adaptor.DoResponse(ctx, openAITestResponse(strings.NewReader(line)), &relaycommon.Meta{IsStream: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read OpenAI event stream")
}
