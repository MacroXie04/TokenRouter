package auth

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/url"
	"testing"
)

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
