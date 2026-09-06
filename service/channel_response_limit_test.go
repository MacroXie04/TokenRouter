package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
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
	assert.ErrorIs(t, err, common.ErrBodyTooLarge)
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
		Type: int(constant.ChannelTypeOpenAI), BaseURL: "https://example.com", Key: "key",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, common.ErrBodyTooLarge))
}
