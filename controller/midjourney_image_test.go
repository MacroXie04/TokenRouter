package controller_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func TestMidjourneyImageProxyContract(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("synthetic-image"))
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		case "/large":
			w.Header().Set("Content-Length", "33554433")
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("upstream-error"))
		}
	}))
	defer upstream.Close()

	handler, _, userID := setupTaskHistory(t, constant.RoleCommonUser)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: userID, MjId: "proxy-ok", ImageUrl: upstream.URL + "/image",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: userID, MjId: "proxy-error", ImageUrl: upstream.URL + "/error",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: userID, MjId: "proxy-html", ImageUrl: upstream.URL + "/html",
	}).Error)
	require.NoError(t, model.DB.Create(&model.Midjourney{
		UserId: userID, MjId: "proxy-large", ImageUrl: upstream.URL + "/large",
	}).Error)

	request := httptest.NewRequest(http.MethodGet, "/mj/image/proxy-ok", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, "image/png", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "synthetic-image", recorder.Body.String())

	request = httptest.NewRequest(http.MethodGet, "/mj/image/proxy-html", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "application/octet-stream", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "attachment", recorder.Header().Get("Content-Disposition"))

	request = httptest.NewRequest(http.MethodGet, "/mj/image/proxy-large", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	assert.Equal(t, "image_response_too_large", decodeBody(t, recorder)["error"])

	request = httptest.NewRequest(http.MethodGet, "/mj/image/proxy-error", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusTeapot, recorder.Code)
	assert.Equal(t, "upstream-error", decodeBody(t, recorder)["error"])

	request = httptest.NewRequest(http.MethodGet, "/mj/image/missing", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Equal(t, "midjourney_task_not_found", decodeBody(t, recorder)["error"])

	t.Setenv("SSRF_DISABLE", "false")
	common.InitSSRF()
	request = httptest.NewRequest(http.MethodGet, "/mj/image/proxy-ok", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, decodeBody(t, recorder)["error"], "request blocked")
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
}
