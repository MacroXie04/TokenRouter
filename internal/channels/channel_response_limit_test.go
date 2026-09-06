package channels

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"strings"
	"testing"
)

type channelRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn channelRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func withChannelResponseBody(t *testing.T, body io.Reader) {
	t.Helper()
	previous := channelUpstreamHTTPClient
	channelUpstreamHTTPClient = &http.Client{Transport: channelRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(body),
		}, nil
	})}
	t.Cleanup(func() { channelUpstreamHTTPClient = previous })
}

func TestBalanceGETRejectsOversizedResponse(t *testing.T) {
	withChannelResponseBody(t, strings.NewReader(strings.Repeat("x", int(maxChannelUpstreamResponseBytes)+1)))
	_, err := balanceGET("https://example.com/balance", "key")
	require.Error(t, err)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

func TestBalanceGETAcceptsExactResponseLimit(t *testing.T) {
	withChannelResponseBody(t, strings.NewReader(strings.Repeat("x", int(maxChannelUpstreamResponseBytes))))
	body, err := balanceGET("https://example.com/balance", "key")
	require.NoError(t, err)
	assert.Len(t, body, int(maxChannelUpstreamResponseBytes))
}

func TestFetchUpstreamModelsRejectsOversizedResponse(t *testing.T) {
	withChannelResponseBody(t, strings.NewReader(strings.Repeat("x", int(maxChannelUpstreamResponseBytes)+1)))
	_, err := FetchUpstreamModelsForChannel(&model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), BaseURL: "https://example.com", Key: "key",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, httpx.ErrBodyTooLarge))
}
