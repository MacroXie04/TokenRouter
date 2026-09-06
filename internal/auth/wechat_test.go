package auth

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func wechatMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	// SafeDialContext rejects loopback in production. These tests deliberately
	// use a local mock server, so opt out only for their lifetime.
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetWeChatIdByCodeBlocksUnsafeConfiguredDestinations(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "false")
	httpx.InitSSRF()
	t.Setenv("WECHAT_SERVER_ADDRESS", "http://127.0.0.1:65535")

	_, err := GetWeChatIdByCode("code")
	require.Error(t, err)
	assert.Equal(t, "微信登录服务地址无效", err.Error())
}

func TestWeChatHTTPClientCannotDelegateResolutionToEnvironmentProxy(t *testing.T) {
	transport, ok := weChatHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
}

func TestWeChatHTTPClientRefusesCredentialedRedirects(t *testing.T) {
	destinationCalls := 0
	destination := wechatMockServer(t, func(http.ResponseWriter, *http.Request) {
		destinationCalls++
	})
	redirector := wechatMockServer(t, func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, destination.URL, http.StatusTemporaryRedirect)
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", redirector.URL)
	t.Setenv("WECHAT_SERVER_TOKEN", "must-not-be-replayed")

	_, err := GetWeChatIdByCode("one-time-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "307")
	assert.Zero(t, destinationCalls)
}

func TestWeChatUserEndpointRejectsAmbiguousBaseURLs(t *testing.T) {
	for _, address := range []string{
		"file:///tmp/socket",
		"https://user:pass@example.com",
		"https://example.com?redirect=https://internal.example",
		"https://example.com/#fragment",
	} {
		_, err := weChatUserEndpoint(address, "code")
		assert.Error(t, err, address)
	}
}

func TestGetWeChatIdByCodeResponseIsBounded(t *testing.T) {
	srv := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxWeChatResponseBytes)+1)))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)

	_, err := GetWeChatIdByCode("code")
	require.Error(t, err)
	assert.True(t, errors.Is(err, httpx.ErrBodyTooLarge))
}

func TestGetWeChatIdByCode(t *testing.T) {
	var gotPath, gotAuth, gotCode string
	srv := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCode = r.URL.Query().Get("code")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"message":"","data":"openid-1"}`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)
	t.Setenv("WECHAT_SERVER_TOKEN", "tok-123")

	id, err := GetWeChatIdByCode("code-1")
	require.NoError(t, err)
	assert.Equal(t, "openid-1", id)
	assert.Equal(t, "/api/wechat/user", gotPath)
	assert.Equal(t, "tok-123", gotAuth)
	assert.Equal(t, "code-1", gotCode)
}

func TestGetWeChatIdByCodeFailures(t *testing.T) {
	// Empty code fails before any network call.
	_, err := GetWeChatIdByCode("")
	assert.Error(t, err)

	// Server failure responses are fixed and never surface upstream text.
	srv := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":false,"message":"code invalid"}`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)
	_, err = GetWeChatIdByCode("bad")
	require.Error(t, err)
	assert.Equal(t, "验证码错误或已过期", err.Error())
	assert.NotContains(t, err.Error(), "code invalid")

	// Success without data means the code expired.
	srv2 := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"message":"","data":""}`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv2.URL)
	_, err = GetWeChatIdByCode("expired")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "验证码错误或已过期")

	// Non-JSON response is rejected.
	srv3 := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>error</html>`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv3.URL)
	_, err = GetWeChatIdByCode("x")
	require.Error(t, err)

	// Unconfigured server address fails.
	t.Setenv("WECHAT_SERVER_ADDRESS", "")
	_, err = GetWeChatIdByCode("x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未配置")
}

func TestGetWeChatIdByCodeRejectsUnboundedOrUnsafeFields(t *testing.T) {
	for _, code := range []string{
		strings.Repeat("c", maxWeChatCodeBytes+1),
		"line\nbreak",
		"spoof\u202etext",
		string([]byte{0xff}),
	} {
		_, err := GetWeChatIdByCode(code)
		assert.EqualError(t, err, "无效的参数")
	}

	srv := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"` +
			strings.Repeat("m", maxWeChatMessageBytes+1) + `"}`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)
	_, err := GetWeChatIdByCode("code")
	assert.EqualError(t, err, "微信登录服务响应无效")

	srv2 := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":"` +
			strings.Repeat("i", maxWeChatProviderIDBytes+1) + `"}`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv2.URL)
	_, err = GetWeChatIdByCode("code")
	assert.EqualError(t, err, "验证码错误或已过期")

	t.Setenv("WECHAT_SERVER_TOKEN", "unsafe\ntoken")
	_, err = GetWeChatIdByCode("code")
	assert.EqualError(t, err, "微信登录服务令牌无效")
}

func TestGetWeChatIdByCodeRejectsAmbiguousResponseFields(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"success":false,"data":"openid-1"}`,
		`{"success":true,"data":"first","data":"second"}`,
		`{"success":"true","data":"openid-1"}`,
		`{"success":true,"data":123}`,
	} {
		srv := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)
		_, err := GetWeChatIdByCode("code")
		assert.EqualError(t, err, "微信登录服务响应无效", body)
	}
}

func TestWeChatAuthEnabledDefault(t *testing.T) {
	assert.False(t, WeChatAuthEnabled())
}
