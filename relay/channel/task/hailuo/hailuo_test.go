package hailuo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
)

func allowHailuoLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
}

func TestHailuoModelCatalogIsExactAndOwned(t *testing.T) {
	want := []string{
		"MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3-Fast", "MiniMax-Hailuo-02",
		"T2V-01-Director", "T2V-01", "I2V-01-Director", "I2V-01-live", "I2V-01", "S2V-01",
	}
	models := ModelList()
	assert.Equal(t, want, models)
	for _, model := range want {
		assert.True(t, IsModel(model))
	}
	models[0] = "mutated"
	assert.Equal(t, want, ModelList())
	assert.False(t, IsModel("MiniMax-Hailuo-unknown"))
	assert.Equal(t, ActionGenerate, func() Action { action, _ := ParseAction("generate"); return action }())
	_, err := ParseAction("textGenerate")
	assert.Error(t, err)
}

func TestHailuoDeclaredModelKeepsInvalidFamilyRequestsOnHailuoPath(t *testing.T) {
	assert.Equal(t, "MiniMax-Hailuo-2.3", DeclaredModel(
		[]byte(`{"model":"MiniMax-Hailuo-2.3","unknown":"must be rejected later"}`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"MiniMax-Hailuo-2.3","model":"T2V-01"}`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"MiniMax-Hailuo-2.3"} trailing`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"MiniMax-Hailuo-2.3"}`), "multipart/form-data"))
}

func TestHailuoPrepareSubmitMapsOpenAIVideoRequestAndMetadata(t *testing.T) {
	raw := []byte(`{
		"model":"MiniMax-Hailuo-2.3","prompt":"  a lighthouse in a storm  ",
		"duration":"10","size":"1920x1080","input_reference":"https://assets.example/first.png",
		"metadata":{"prompt_optimizer":false,"fast_pretreatment":true,"aigc_watermark":true,
			"last_frame_image":"https://assets.example/last.png"}
	}`)
	model, err := RequestedModel(raw, "application/json; charset=utf-8")
	require.NoError(t, err)
	assert.Equal(t, "MiniMax-Hailuo-2.3", model)
	prepared, err := PrepareSubmit(raw, "application/json", model, "MiniMax-Hailuo-2.3")
	require.NoError(t, err)
	assert.Equal(t, "a lighthouse in a storm", prepared.Prompt)
	assert.Equal(t, 10, prepared.Duration)
	assert.Equal(t, Resolution1080P, prepared.Resolution)
	assert.True(t, prepared.HasInputReference)
	assert.JSONEq(t, `{
		"model":"MiniMax-Hailuo-2.3","prompt":"a lighthouse in a storm","duration":10,"resolution":"1080P",
		"prompt_optimizer":false,"fast_pretreatment":true,"aigc_watermark":true,
		"first_frame_image":"https://assets.example/first.png","last_frame_image":"https://assets.example/last.png"
	}`, string(prepared.Body))

	stringMetadata := []byte(`{"model":"I2V-01","prompt":"animate","image":"https://assets.example/a.png","metadata":"{\"resolution\":\"1080P\",\"prompt_optimizer\":true}"}`)
	prepared, err = PrepareSubmit(stringMetadata, "application/json", "I2V-01", "I2V-01")
	require.NoError(t, err)
	assert.Equal(t, DefaultDuration, prepared.Duration)
	assert.Equal(t, Resolution1080P, prepared.Resolution)
}

func TestHailuoPrepareSubmitRejectsAmbiguousUnsupportedAndOversizedInputs(t *testing.T) {
	valid := `{"model":"MiniMax-Hailuo-2.3","prompt":"scene"}`
	tests := []struct {
		name        string
		body        string
		contentType string
		origin      string
		mapped      string
	}{
		{"duplicate field", `{"model":"MiniMax-Hailuo-2.3","model":"T2V-01","prompt":"scene"}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"unknown field", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","credential":"secret"}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"both durations", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","duration":6,"seconds":"10"}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"fraction duration", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","duration":6.5}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"unsupported duration", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","duration":7}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"unsupported resolution", `{"model":"T2V-01","prompt":"scene","size":"1920x1080"}`, "application/json", "T2V-01", "T2V-01"},
		{"unsupported fast mode", `{"model":"T2V-01","prompt":"scene","metadata":{"fast_pretreatment":true}}`, "application/json", "T2V-01", "T2V-01"},
		{"unsafe callback", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","metadata":{"callback_url":"file:///tmp/result"}}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"bad subject", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","metadata":{"subject_reference":[{"type":"person","image":["https://assets.example/a.png"]}]}}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"too many frames", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","images":["https://assets.example/a","https://assets.example/b","https://assets.example/c"]}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"bad frame", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","image":"file:///tmp/a"}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"unknown metadata", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","metadata":{"access_token":"secret"}}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"duplicate metadata", `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","metadata":{"duration":6,"duration":10}}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"wrong origin", valid, "application/json", "T2V-01", "MiniMax-Hailuo-2.3"},
		{"wrong mapped", valid, "application/json", "MiniMax-Hailuo-2.3", "provider-alias"},
		{"empty prompt", `{"model":"MiniMax-Hailuo-2.3","prompt":"   "}`, "application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
		{"wrong content type", valid, "multipart/form-data; boundary=x", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareSubmit([]byte(test.body), test.contentType, test.origin, test.mapped)
			assert.Error(t, err)
		})
	}

	_, err := PrepareSubmit([]byte(strings.Repeat("x", MaxRequestBodyBytes+1)), "application/json",
		"MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3")
	assert.ErrorIs(t, err, common.ErrBodyTooLarge)
	deep := `{"model":"MiniMax-Hailuo-2.3","prompt":"scene","metadata":` + strings.Repeat(`{"x":`, 40) + `null` + strings.Repeat(`}`, 40) + `}`
	_, err = RequestedModel([]byte(deep), "application/json")
	assert.Error(t, err)
}

func TestHailuoClientSubmitFetchAndFileRetrievalContract(t *testing.T) {
	allowHailuoLoopback(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "Bearer hailuo-secret", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		switch {
		case request.Method == http.MethodPost && request.URL.Path == SubmitPath:
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, "MiniMax-Hailuo-2.3", body["model"])
			_, _ = io.WriteString(w, `{"task_id":"provider-task-1","base_resp":{"status_code":0,"status_msg":"success"}}`)
		case request.Method == http.MethodGet && request.URL.Path == FetchPath && request.URL.Query().Get("task_id") == "provider-task-1":
			_, _ = io.WriteString(w, `{"task_id":"provider-task-1","status":"Processing","base_resp":{"status_code":0}}`)
		case request.Method == http.MethodGet && request.URL.Path == FetchPath && request.URL.Query().Get("task_id") == "provider-task-2":
			_, _ = io.WriteString(w, `{"task_id":"provider-task-2","status":"Success","file_id":"file-9","video_width":1920,"video_height":1080,"base_resp":{"status_code":0}}`)
		case request.Method == http.MethodGet && request.URL.Path == RetrieveFilePath:
			assert.Equal(t, "file-9", request.URL.Query().Get("file_id"))
			_, _ = io.WriteString(w, `{"file":{"download_url":"https://cdn.example/video.mp4"},"base_resp":{"status_code":0}}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	prepared, err := PrepareSubmit([]byte(`{"model":"MiniMax-Hailuo-2.3","prompt":"scene"}`),
		"application/json", "MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3")
	require.NoError(t, err)
	client := &Client{HTTPClient: server.Client()}
	created, _, err := client.Submit(context.Background(), server.URL, "hailuo-secret", prepared)
	require.NoError(t, err)
	assert.Equal(t, "provider-task-1", created.ProviderTaskID)
	assert.Equal(t, StatusSubmitted, created.Status)
	processing, _, err := client.Fetch(context.Background(), server.URL, "hailuo-secret", "provider-task-1")
	require.NoError(t, err)
	assert.Equal(t, StatusProcessing, processing.Status)
	completed, _, err := client.Fetch(context.Background(), server.URL, "hailuo-secret", "provider-task-2")
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, completed.Status)
	assert.Equal(t, "file-9", completed.FileID)
	assert.Equal(t, "https://cdn.example/video.mp4", completed.ResultURL)
	assert.Equal(t, 1920, completed.VideoWidth)
	assert.Equal(t, int32(4), calls.Load())
}

func TestHailuoClientClassifiesDefinitiveAndAmbiguousSubmitOutcomes(t *testing.T) {
	allowHailuoLoopback(t)
	prepared, err := PrepareSubmit([]byte(`{"model":"T2V-01","prompt":"scene"}`),
		"application/json", "T2V-01", "T2V-01")
	require.NoError(t, err)
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		definite   bool
	}{
		{"business rejection", http.StatusOK, `{"base_resp":{"status_code":1004,"status_msg":"hailuo-secret denied"}}`, true},
		{"malformed accepted", http.StatusOK, `{`, false},
		{"missing task id", http.StatusOK, `{"base_resp":{"status_code":0}}`, false},
		{"HTTP rejection", http.StatusTooManyRequests, `{"error":{"message":"hailuo-secret limited"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			task, _, err := (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "hailuo-secret", prepared)
			if test.definite {
				require.NoError(t, err)
				require.NotNil(t, task)
				assert.Equal(t, StatusFailed, task.Status)
				assert.Equal(t, "1004", task.ErrorCode)
				return
			}
			require.Error(t, err)
			assert.Nil(t, task)
			assert.True(t, SubmitWasDispatched(err))
			if test.statusCode != http.StatusOK {
				var upstream *relaycommon.UpstreamError
				require.ErrorAs(t, err, &upstream)
				assert.Equal(t, test.statusCode, upstream.StatusCode)
			}
		})
	}

	_, _, err = (&Client{}).Submit(context.Background(), "file:///tmp/provider", "secret", prepared)
	require.Error(t, err)
	assert.False(t, SubmitWasDispatched(err))
	_, _, err = (&Client{}).Submit(context.Background(), DirectBaseURL, "bad\nkey", prepared)
	require.Error(t, err)
	assert.False(t, SubmitWasDispatched(err))
}

func TestHailuoFetchRejectsMismatchedMalformedAndUnsafeResults(t *testing.T) {
	allowHailuoLoopback(t)
	for _, test := range []struct {
		name string
		body string
	}{
		{"mismatched id", `{"task_id":"other","status":"Processing","base_resp":{"status_code":0}}`},
		{"unknown status", `{"task_id":"expected","status":"Mystery","base_resp":{"status_code":0}}`},
		{"missing file", `{"task_id":"expected","status":"Success","base_resp":{"status_code":0}}`},
		{"negative dimension", `{"task_id":"expected","status":"Processing","video_width":-1,"base_resp":{"status_code":0}}`},
		{"duplicate field", `{"task_id":"expected","status":"Processing","status":"Success","base_resp":{"status_code":0}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, _, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "secret", "expected")
			assert.Error(t, err)
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == FetchPath {
			_, _ = io.WriteString(w, `{"task_id":"expected","status":"Success","file_id":"file-1","base_resp":{"status_code":0}}`)
			return
		}
		_, _ = io.WriteString(w, `{"file":{"download_url":"file:///tmp/video"},"base_resp":{"status_code":0}}`)
	}))
	defer server.Close()
	_, _, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "secret", "expected")
	assert.Error(t, err)
}

func TestHailuoClientTransportIsSSRFCheckedAndNoRedirect(t *testing.T) {
	client := NewHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, client.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://redirect.example", nil)
	assert.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)

	assert.NoError(t, func() error { _, err := EffectiveBaseURL(DirectBaseURL); return err }())
	_, err := EffectiveBaseURL("https://user:password@example.com")
	assert.Error(t, err)
	_, err = EffectiveBaseURL("https://example.com?key=secret")
	assert.Error(t, err)
	_, err = EffectiveBaseURL("http://example.com")
	assert.Error(t, err)
}
