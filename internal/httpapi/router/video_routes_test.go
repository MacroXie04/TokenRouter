package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIVideoRoutesUseImplementedTaskHandlers(t *testing.T) {
	routes := SetUpRouter().Routes()
	registered := make(map[string]string, len(routes))
	for _, route := range routes {
		registered[route.Method+" "+route.Path] = route.Handler
	}
	expected := map[string]string{
		"POST /v1/video/generations":         "relay.RelayTask",
		"GET /v1/video/generations/:task_id": "relay.RelayTaskFetch",
		"POST /v1/videos/:video_id/remix":    "relay.RelayTask",
		"POST /v1/videos":                    "relay.RelayTask",
		"GET /v1/videos/:task_id":            "relay.RelayTaskFetch",
		"GET /v1/videos/:task_id/content":    "relay.VideoProxy",
	}
	for route, handler := range expected {
		actual, ok := registered[route]
		require.True(t, ok, "missing exact video route %s", route)
		assert.Contains(t, actual, handler, route)
		assert.NotContains(t, actual, "NotImplemented", route)
	}
}
