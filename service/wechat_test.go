package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wechatMockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
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

	// Server failure response surfaces the message.
	srv := wechatMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":false,"message":"code invalid"}`))
	})
	t.Setenv("WECHAT_SERVER_ADDRESS", srv.URL)
	_, err = GetWeChatIdByCode("bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code invalid")

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

func TestWeChatAuthEnabledDefault(t *testing.T) {
	assert.False(t, WeChatAuthEnabled())
}
