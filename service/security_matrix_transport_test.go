package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestChannelOutboundClientsUseDirectSSRFSafeDialing(t *testing.T) {
	for name, client := range map[string]*http.Client{
		"health-and-balance": healthHTTPClient,
		"model-catalog":      channelUpstreamHTTPClient,
		"user-notification":  defaultUserNotificationHTTPClient,
	} {
		t.Run(name, func(t *testing.T) {
			transport, ok := client.Transport.(*http.Transport)
			require.True(t, ok)
			assert.Nil(t, transport.Proxy, "channel destinations must not be delegated to an environment proxy")
			require.NotNil(t, transport.DialContext)
			assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
		})
	}
}

func TestChannelOutboundClientsRefuseCredentialedRedirects(t *testing.T) {
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()

	for name, client := range map[string]*http.Client{
		"health-and-balance": healthHTTPClient,
		"model-catalog":      channelUpstreamHTTPClient,
		"user-notification":  defaultUserNotificationHTTPClient,
	} {
		t.Run(name, func(t *testing.T) {
			destinationCalls := 0
			destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				destinationCalls++
			}))
			t.Cleanup(destination.Close)
			redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				http.Redirect(w, request, destination.URL, http.StatusTemporaryRedirect)
			}))
			t.Cleanup(redirector.Close)

			request, err := http.NewRequest(http.MethodGet, redirector.URL, nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer must-not-be-replayed")
			response, err := client.Do(request)
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })
			assert.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
			assert.Zero(t, destinationCalls)
		})
	}
}

func TestCredentialedOutboundClientsRefuseRedirects(t *testing.T) {
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "redirect.example", Path: "/next"}}
	via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "provider.example", Path: "/token"}}}
	for name, client := range map[string]*http.Client{
		"built-in OAuth": oauthHTTPClient,
		"custom OAuth":   customOAuthHTTPClient,
		"Turnstile":      turnstileHTTPClient,
		"WeChat":         weChatHTTPClient,
	} {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, client.CheckRedirect)
			assert.ErrorIs(t, client.CheckRedirect(request.Clone(request.Context()), via), http.ErrUseLastResponse)
		})
	}
}
