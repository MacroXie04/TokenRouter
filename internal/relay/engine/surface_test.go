package engine_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
)

func surfaceRequest(t *testing.T, r http.Handler, method, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRelayRetrieveModel(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mock.Close()
	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	// Known model in the group returns the model entry.
	rec := surfaceRequest(t, r, http.MethodGet, "/v1/models/gpt-4", key)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var model struct {
		Id     string `json:"id"`
		Object string `json:"object"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &model))
	assert.Equal(t, "gpt-4", model.Id)
	assert.Equal(t, "model", model.Object)

	// Anthropic header flavor returns the Claude shape.
	req := httptest.NewRequest(http.MethodGet, "/v1/models/gpt-4", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("x-api-key", "sk-ant")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req)
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), `"type":"model"`)
	assert.Contains(t, rec2.Body.String(), `"display_name"`)

	// Unknown model → 404 model_not_found.
	rec3 := surfaceRequest(t, r, http.MethodGet, "/v1/models/no-such-model", key)
	require.Equal(t, http.StatusNotFound, rec3.Code)
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec3.Body.Bytes(), &e))
	assert.Equal(t, "model_not_found", e.Error.Code)
	assert.Contains(t, e.Error.Message, "no-such-model")
}

func TestRelayNotImplementedSurfaces(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mock.Close()
	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/images/variations"},
		{http.MethodGet, "/v1/files"},
		{http.MethodPost, "/v1/files"},
		{http.MethodDelete, "/v1/files/file-1"},
		{http.MethodGet, "/v1/files/file-1"},
		{http.MethodGet, "/v1/files/file-1/content"},
		{http.MethodPost, "/v1/fine-tunes"},
		{http.MethodGet, "/v1/fine-tunes"},
		{http.MethodGet, "/v1/fine-tunes/ft-1"},
		{http.MethodPost, "/v1/fine-tunes/ft-1/cancel"},
		{http.MethodGet, "/v1/fine-tunes/ft-1/events"},
		{http.MethodDelete, "/v1/models/gpt-4"},
	} {
		rec := surfaceRequest(t, r, tc.method, tc.path, key)
		require.Equal(t, http.StatusNotImplemented, rec.Code, "%s %s: %s", tc.method, tc.path, rec.Body.String())
		var body struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "%s %s", tc.method, tc.path)
		assert.Equal(t, "API not implemented", body.Error.Message)
		assert.Equal(t, "api_not_implemented", body.Error.Code)
	}
}

func TestRelayEditsPassthrough(t *testing.T) {
	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		resp := map[string]any{
			"object":  "edit",
			"created": 1,
			"choices": []map[string]any{{"text": "edited text", "index": 0}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()
	key, _ := setupRelayIntegration(t, mock.URL)
	r := router.SetUpRouter()

	body := `{"model":"gpt-4","input":"hello there","instruction":"make it formal"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/edits", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/v1/edits", gotPath)
	assert.Contains(t, rec.Body.String(), "edited text")
}
