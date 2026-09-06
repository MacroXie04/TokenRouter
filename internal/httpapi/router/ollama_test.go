package router_test

import (
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOllamaManagementRoutesUseNativeWireAndExactResponses(t *testing.T) {
	type observation struct {
		method string
		path   string
		auth   string
		body   map[string]any
	}
	observed := make(chan observation, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
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
					`{"status":"downloading","digest":"sha256:abc","total":20,"completed":10}`,
					`{"status":"success","total":20,"completed":20}`,
				}, "\n"))
			} else {
				_, _ = io.WriteString(w, `{"status":"success"}`)
			}
		case "/api/delete":
			_, _ = io.WriteString(w, `{}`)
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"0.11.7"}`)
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"llama3.2:latest"},{"name":"nomic-embed-text"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	channel := createSearchChannel(t, "ollama-ops", int(channelcatalog.ChannelTypeOllama), channelcatalog.ChannelStatusEnabled, "default", "llama3.2:latest", "", 0)
	require.NoError(t, model.DB.Model(&channel).Updates(map[string]any{
		"base_url": upstream.URL,
		"key":      "ollama-operation-secret",
	}).Error)

	payload := fmt.Sprintf(`{"channel_id":%d,"model_name":"llama3.2:latest"}`, channel.Id)
	pull := do(http.MethodPost, "/api/channel/ollama/pull", payload)
	require.Equal(t, http.StatusOK, pull.Code, pull.Body.String())
	pullBody := decodeBody(t, pull)
	assert.Equal(t, true, pullBody["success"])
	assert.Equal(t, "Model llama3.2:latest pulled successfully", pullBody["message"])

	stream := do(http.MethodPost, "/api/channel/ollama/pull/stream", payload)
	require.Equal(t, http.StatusOK, stream.Code, stream.Body.String())
	assert.Equal(t, "text/event-stream", stream.Header().Get("Content-Type"))
	assert.Contains(t, stream.Body.String(), `data: {"status":"pulling manifest"}`)
	assert.Contains(t, stream.Body.String(), `"completed":20`)
	assert.Contains(t, stream.Body.String(), `"message":"Model llama3.2:latest pulled successfully"`)
	assert.True(t, strings.HasSuffix(stream.Body.String(), "data: [DONE]\n\n"))

	deleted := do(http.MethodDelete, "/api/channel/ollama/delete", payload)
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	assert.Equal(t, true, decodeBody(t, deleted)["success"])

	version := do(http.MethodGet, fmt.Sprintf("/api/channel/ollama/version/%d", channel.Id), "")
	require.Equal(t, http.StatusOK, version.Code, version.Body.String())
	versionBody := decodeBody(t, version)
	assert.Equal(t, true, versionBody["success"])
	assert.Equal(t, "0.11.7", versionBody["data"].(map[string]any)["version"])

	models := do(http.MethodGet, fmt.Sprintf("/api/channel/fetch_models/%d", channel.Id), "")
	require.Equal(t, http.StatusOK, models.Code, models.Body.String())
	modelsBody := decodeBody(t, models)
	assert.Equal(t, true, modelsBody["success"])
	assert.Equal(t, []any{"llama3.2:latest", "nomic-embed-text"}, modelsBody["data"])

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
		assert.Equal(t, "Bearer ollama-operation-secret", wire.auth)
	}
	assert.Equal(t, false, wires[0].body["stream"])
	assert.Equal(t, true, wires[1].body["stream"])
	assert.Equal(t, "llama3.2:latest", wires[2].body["name"])
}

func TestOllamaManagementRoutesValidateChannelAndKeepCredentialsPrivate(t *testing.T) {
	var contacted atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contacted.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"ollama-operation-secret must stay private"}`)
	}))
	defer upstream.Close()

	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	ollamaChannel := createSearchChannel(t, "ollama-error", int(channelcatalog.ChannelTypeOllama), channelcatalog.ChannelStatusEnabled, "default", "model", "", 0)
	require.NoError(t, model.DB.Model(&ollamaChannel).Updates(map[string]any{
		"base_url": upstream.URL,
		"key":      "ollama-operation-secret",
	}).Error)
	wrongType := createSearchChannel(t, "not-ollama", int(channelcatalog.ChannelTypeOpenAI), channelcatalog.ChannelStatusEnabled, "default", "model", "", 0)

	badJSON := do(http.MethodPost, "/api/channel/ollama/pull", `{`)
	assert.Equal(t, http.StatusBadRequest, badJSON.Code)
	missingFields := do(http.MethodPost, "/api/channel/ollama/pull", `{}`)
	assert.Equal(t, http.StatusBadRequest, missingFields.Code)
	missingChannel := do(http.MethodPost, "/api/channel/ollama/pull", `{"channel_id":999999,"model_name":"model"}`)
	assert.Equal(t, http.StatusNotFound, missingChannel.Code)
	wrongChannel := do(http.MethodPost, "/api/channel/ollama/pull", fmt.Sprintf(`{"channel_id":%d,"model_name":"model"}`, wrongType.Id))
	assert.Equal(t, http.StatusBadRequest, wrongChannel.Code)
	invalidVersion := do(http.MethodGet, "/api/channel/ollama/version/not-a-number", "")
	assert.Equal(t, http.StatusBadRequest, invalidVersion.Code)
	assert.False(t, contacted.Load(), "validation failures must happen before network dispatch")

	upstreamFailure := do(http.MethodPost, "/api/channel/ollama/pull", fmt.Sprintf(
		`{"channel_id":%d,"model_name":"model"}`, ollamaChannel.Id,
	))
	assert.Equal(t, http.StatusBadGateway, upstreamFailure.Code)
	assert.NotContains(t, upstreamFailure.Body.String(), "ollama-operation-secret")
}

func TestOllamaManagementRoutesRequireSensitiveWritePermission(t *testing.T) {
	_, admin, plain := setupPermissionTest(t)
	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/channel/ollama/pull", `{"channel_id":1,"model_name":"model"}`},
		{http.MethodPost, "/api/channel/ollama/pull/stream", `{"channel_id":1,"model_name":"model"}`},
		{http.MethodDelete, "/api/channel/ollama/delete", `{"channel_id":1,"model_name":"model"}`},
		{http.MethodGet, "/api/channel/ollama/version/1", ""},
	} {
		assert.Equal(t, http.StatusForbidden, admin.do(request.method, request.path, request.body).Code)
		assert.Equal(t, http.StatusForbidden, plain.do(request.method, request.path, request.body).Code)
	}
}
