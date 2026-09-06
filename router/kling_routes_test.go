package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireExactKlingRoute(t *testing.T, method, path, handler string) {
	t.Helper()
	for _, route := range SetUpRouter().Routes() {
		if route.Method == method && route.Path == path {
			assert.Contains(t, route.Handler, handler)
			assert.NotContains(t, route.Handler, "NotImplemented")
			return
		}
	}
	require.Failf(t, "missing exact Kling route", "%s %s", method, path)
}

func TestKlingRoutesUseExactImplementedSurface(t *testing.T) {
	routes := SetUpRouter().Routes()
	registered := make(map[string]string, len(routes))
	for _, route := range routes {
		registered[route.Method+" "+route.Path] = route.Handler
	}
	expected := map[string]string{
		"POST /kling/v1/videos/text2video":          "controller.RelayKlingTask",
		"POST /kling/v1/videos/image2video":         "controller.RelayKlingTask",
		"GET /kling/v1/videos/text2video/:task_id":  "controller.RelayKlingTaskFetch",
		"GET /kling/v1/videos/image2video/:task_id": "controller.RelayKlingTaskFetch",
	}
	for route, handler := range expected {
		actual, ok := registered[route]
		require.True(t, ok, "missing exact Kling route %s", route)
		assert.Contains(t, actual, handler, route)
		assert.NotContains(t, actual, "NotImplemented", route)
	}
	_, legacyAlias := registered["POST /v1/kling/videos/text2video"]
	assert.False(t, legacyAlias)
}

func TestKlingImageToVideoSubmitRouteContract(t *testing.T) {
	requireExactKlingRoute(t, http.MethodPost, "/kling/v1/videos/image2video", "controller.RelayKlingTask")
}

func TestKlingTextToVideoFetchRouteContract(t *testing.T) {
	requireExactKlingRoute(t, http.MethodGet, "/kling/v1/videos/text2video/:task_id", "controller.RelayKlingTaskFetch")
}

func TestKlingImageToVideoFetchRouteContract(t *testing.T) {
	requireExactKlingRoute(t, http.MethodGet, "/kling/v1/videos/image2video/:task_id", "controller.RelayKlingTaskFetch")
}
