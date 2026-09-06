package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMidjourneyRoutesExposeExactReferenceSurface(t *testing.T) {
	router := SetUpRouter()
	registered := make(map[string]string, len(router.Routes()))
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = route.Handler
	}
	routes := []struct{ method, path, handler string }{
		{http.MethodGet, "/mj/image/:id", "controller.RelayMidjourneyImage"},
		{http.MethodPost, "/mj/submit/action", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/shorten", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/modal", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/imagine", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/change", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/simple-change", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/describe", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/blend", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/edits", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/video", "controller.RelayMidjourney"},
		{http.MethodGet, "/mj/task/:id/fetch", "controller.RelayMidjourney"},
		{http.MethodGet, "/mj/task/:id/image-seed", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/task/list-by-condition", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/insight-face/swap", "controller.RelayMidjourney"},
		{http.MethodPost, "/mj/submit/upload-discord-images", "controller.RelayMidjourney"},
		{http.MethodGet, "/:mode/mj/image/:id", "controller.RelayMidjourneyImage"},
		{http.MethodPost, "/:mode/mj/submit/action", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/shorten", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/modal", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/imagine", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/change", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/simple-change", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/describe", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/blend", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/edits", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/video", "controller.RelayMidjourney"},
		{http.MethodGet, "/:mode/mj/task/:id/fetch", "controller.RelayMidjourney"},
		{http.MethodGet, "/:mode/mj/task/:id/image-seed", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/task/list-by-condition", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/insight-face/swap", "controller.RelayMidjourney"},
		{http.MethodPost, "/:mode/mj/submit/upload-discord-images", "controller.RelayMidjourney"},
	}
	for _, route := range routes {
		key := route.method + " " + route.path
		handler, ok := registered[key]
		require.True(t, ok, "missing %s", key)
		assert.Contains(t, handler, route.handler, key)
		assert.NotContains(t, handler, "NotImplemented", key)
	}
	for _, forbidden := range []string{
		"POST /mj/notify", "POST /:mode/mj/notify", "GET /mj/task/:id", "POST /v1/mj/submit/imagine",
	} {
		_, ok := registered[forbidden]
		assert.False(t, ok, forbidden)
	}
}

func TestMidjourneyTaskRoutesRequireRelayAuthenticationButImagesRemainPublic(t *testing.T) {
	router := SetUpRouter()
	for _, prefix := range []string{"/mj", "/fast/mj"} {
		for _, route := range []struct{ method, suffix string }{
			{http.MethodPost, "/submit/action"},
			{http.MethodPost, "/submit/shorten"},
			{http.MethodPost, "/submit/modal"},
			{http.MethodPost, "/submit/imagine"},
			{http.MethodPost, "/submit/change"},
			{http.MethodPost, "/submit/simple-change"},
			{http.MethodPost, "/submit/describe"},
			{http.MethodPost, "/submit/blend"},
			{http.MethodPost, "/submit/edits"},
			{http.MethodPost, "/submit/video"},
			{http.MethodGet, "/task/task_0123456789abcdef0123456789abcdef/fetch"},
			{http.MethodGet, "/task/task_0123456789abcdef0123456789abcdef/image-seed"},
			{http.MethodPost, "/task/list-by-condition"},
			{http.MethodPost, "/insight-face/swap"},
			{http.MethodPost, "/submit/upload-discord-images"},
		} {
			request := httptest.NewRequest(route.method, prefix+route.suffix, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assert.Equal(t, http.StatusUnauthorized, recorder.Code, route.method+" "+prefix+route.suffix)
		}
		request := httptest.NewRequest(http.MethodGet, prefix+"/image/not-a-task", nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assert.NotEqual(t, http.StatusUnauthorized, recorder.Code, prefix+" image route must be public")
	}
}
