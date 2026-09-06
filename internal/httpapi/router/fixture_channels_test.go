package router_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http/httptest"
	"testing"
)

func createSearchChannel(t *testing.T, name string, typ, status int, group, models, tag string, priority int64) model.Channel {
	t.Helper()
	ch := model.Channel{
		Type: typ, Key: "sk-secret-" + name, Name: name, Status: status, Group: group,
		Models: models, Tag: tag, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&ch).Error)
	return ch
}

func searchItems(t *testing.T, rec *httptest.ResponseRecorder) ([]map[string]any, map[string]any) {
	t.Helper()
	body := decodeBody(t, rec)
	require.Equal(t, true, body["success"], "body: %s", rec.Body.String())
	assert.Equal(t, "", body["message"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "data missing: %s", rec.Body.String())
	items, ok := data["items"].([]any)
	require.True(t, ok, "items missing: %s", rec.Body.String())
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		require.True(t, ok)
		out = append(out, m)
	}
	return out, data
}
