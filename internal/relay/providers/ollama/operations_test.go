package ollama

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOllamaOperationClientIsDirectSSRFSafeAndRefusesRedirects(t *testing.T) {
	transport, ok := operationHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, operationHTTPClient.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://ollama.example.test/api/version", nil)
	redirect := httptest.NewRequest(http.MethodGet, "https://redirect.example.test/", nil)
	assert.ErrorIs(t, operationHTTPClient.CheckRedirect(redirect, []*http.Request{request}), http.ErrUseLastResponse)
}

func TestOllamaManagementOperationsWireContracts(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	type observation struct {
		method string
		path   string
		auth   string
		body   map[string]any
	}
	observed := make(chan observation, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&body)
		}
		observed <- observation{method: request.Method, path: request.URL.Path, auth: request.Header.Get("Authorization"), body: body}
		switch request.URL.Path {
		case "/api/pull":
			if stream, _ := body["stream"].(bool); stream {
				_, _ = io.WriteString(w, strings.Join([]string{
					`{"status":"pulling manifest"}`,
					`{"status":"downloading","digest":"sha256:abc","total":10,"completed":5}`,
					`{"status":"success","total":10,"completed":10}`,
				}, "\n"))
				return
			}
			_, _ = io.WriteString(w, `{"status":"success"}`)
		case "/api/delete":
			_, _ = io.WriteString(w, `{}`)
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.11.7"}`)
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"llama3.2:latest"},{"name":"nomic-embed-text"},{"name":"llama3.2:latest"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	require.NoError(t, PullModel(t.Context(), server.URL, "operation-secret", "llama3.2:latest"))
	progress := make([]PullProgress, 0, 3)
	require.NoError(t, PullModelStream(t.Context(), server.URL, "operation-secret", "llama3.2:latest", func(frame PullProgress) error {
		progress = append(progress, frame)
		return nil
	}))
	require.Len(t, progress, 3)
	assert.Equal(t, "success", progress[2].Status)
	require.NoError(t, DeleteModel(t.Context(), server.URL, "operation-secret", "llama3.2:latest"))
	version, err := FetchVersion(t.Context(), server.URL, "operation-secret")
	require.NoError(t, err)
	assert.Equal(t, "0.11.7", version)
	models, err := FetchModels(t.Context(), server.URL, "operation-secret")
	require.NoError(t, err)
	assert.Equal(t, []string{"llama3.2:latest", "nomic-embed-text"}, models)

	wires := make([]observation, 0, 5)
	for range 5 {
		wires = append(wires, <-observed)
	}
	assert.Equal(t, []string{"/api/pull", "/api/pull", "/api/delete", "/api/version", "/api/tags"}, []string{
		wires[0].path, wires[1].path, wires[2].path, wires[3].path, wires[4].path,
	})
	assert.Equal(t, []string{http.MethodPost, http.MethodPost, http.MethodDelete, http.MethodGet, http.MethodGet}, []string{
		wires[0].method, wires[1].method, wires[2].method, wires[3].method, wires[4].method,
	})
	for _, wire := range wires {
		assert.Equal(t, "Bearer operation-secret", wire.auth)
	}
	assert.Equal(t, "llama3.2:latest", wires[0].body["name"])
	assert.Equal(t, false, wires[0].body["stream"])
	assert.Equal(t, true, wires[1].body["stream"])
	assert.Equal(t, "llama3.2:latest", wires[2].body["name"])
}

func TestOllamaManagementOperationsFailClosed(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()

	var redirectContacted atomic.Bool
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectContacted.Store(true)
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", redirectTarget.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirectSource.Close()
	_, err := FetchVersion(t.Context(), redirectSource.URL, "must-not-redirect")
	require.Error(t, err)
	assert.False(t, redirectContacted.Load())
	assert.NotContains(t, err.Error(), "must-not-redirect")

	for _, test := range []struct {
		name string
		path string
		body string
		call func(string) error
		want string
	}{
		{
			name: "malformed version", path: "/api/version", body: `{"version":`,
			call: func(base string) error { _, err := FetchVersion(t.Context(), base, "secret"); return err }, want: "decode Ollama version",
		},
		{
			name: "invalid pull progress", path: "/api/pull", body: `{"status":"downloading","total":1,"completed":2}`,
			call: func(base string) error { return PullModel(t.Context(), base, "secret", "model") }, want: "invalid byte counts",
		},
		{
			name: "malformed stream frame", path: "/api/pull", body: "not-json\n",
			call: func(base string) error { return PullModelStream(t.Context(), base, "secret", "model", nil) }, want: "decode Ollama pull progress",
		},
		{
			name: "missing stream terminal", path: "/api/pull", body: `{"status":"downloading","total":2,"completed":1}`,
			call: func(base string) error { return PullModelStream(t.Context(), base, "secret", "model", nil) }, want: "without a success status",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				assert.Equal(t, test.path, request.URL.Path)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			require.ErrorContains(t, test.call(server.URL), test.want)
		})
	}

	var contacted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Store(true) }))
	defer server.Close()
	require.Error(t, PullModel(t.Context(), server.URL, "secret", strings.Repeat("m", maxModelNameBytes+1)))
	assert.False(t, contacted.Load())

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	blocking := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer blocking.Close()
	_, err = FetchVersion(ctx, blocking.URL, "secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
