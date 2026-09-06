package ali

import (
	"context"
	"errors"
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
)

func allowAliLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
}

func TestAliModelCatalogIsExactAndOwned(t *testing.T) {
	want := []string{
		"wan2.7-i2v", "wan2.7-t2v", "wan2.5-i2v-preview", "wan2.2-i2v-flash",
		"wan2.2-i2v-plus", "wanx2.1-i2v-plus", "wanx2.1-i2v-turbo",
	}
	models := ModelList()
	assert.Equal(t, want, models)
	for _, model := range want {
		assert.True(t, IsModel(model))
	}
	models[0] = "mutated"
	assert.Equal(t, want, ModelList())
	assert.False(t, IsModel("wan2.7-unknown"))
}

func TestAliDeclaredModelKeepsInvalidFamilyRequestsOnAliPath(t *testing.T) {
	assert.Equal(t, "wan2.7-t2v", DeclaredModel(
		[]byte(`{"model":"wan2.7-t2v","unknown":"rejected during full validation"}`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"wan2.7-t2v","model":"wan2.7-i2v"}`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"wan2.7-t2v"} trailing`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"wan2.7-t2v"}`), "multipart/form-data"))
	assert.InDelta(t, 1/0.3, ResolutionMultiplier("wan2.5-i2v-preview", "1080P"), 0.000001)
	assert.Equal(t, float64(2), ResolutionMultiplier("wan2.2-i2v-flash", "1280*720"))
	assert.Equal(t, float64(1), ResolutionMultiplier("wan2.7-i2v", "720P"))
}

func TestAliPrepareSubmitMapsTextAndImageVideoRequests(t *testing.T) {
	t2v := []byte(`{"model":"wan2.7-t2v","prompt":"  sunrise over mountains  ","duration":"6"}`)
	model, err := RequestedModel(t2v, "application/json; charset=utf-8")
	require.NoError(t, err)
	assert.Equal(t, "wan2.7-t2v", model)
	prepared, err := PrepareSubmit(t2v, "application/json", model, model)
	require.NoError(t, err)
	assert.Equal(t, 6, prepared.Duration)
	assert.Equal(t, "1280*720", prepared.Size)
	assert.False(t, prepared.HasInputReference)
	assert.JSONEq(t, `{
		"model":"wan2.7-t2v","input":{"prompt":"sunrise over mountains"},
		"parameters":{"size":"1280*720","duration":6,"prompt_extend":true,"watermark":false}
	}`, string(prepared.Body))

	i2v := []byte(`{
		"model":"wan2.7-i2v","prompt":"interpolate","image":"https://assets.example/direct.png",
		"images":["https://assets.example/ignored.png","https://assets.example/last.png"],
		"size":"720p","seconds":10
	}`)
	prepared, err = PrepareSubmit(i2v, "application/json", "wan2.7-i2v", "wan2.7-i2v")
	require.NoError(t, err)
	assert.True(t, prepared.HasInputReference)
	assert.Equal(t, "720P", prepared.Resolution)
	assert.JSONEq(t, `{
		"model":"wan2.7-i2v","input":{"prompt":"interpolate","media":[
			{"type":"first_frame","url":"https://assets.example/direct.png"},
			{"type":"last_frame","url":"https://assets.example/last.png"}
		]},"parameters":{"resolution":"720P","duration":10,"prompt_extend":true,"watermark":false}
	}`, string(prepared.Body))
}

func TestAliPrepareSubmitAppliesStrictMetadataWithoutChangingModel(t *testing.T) {
	raw := []byte(`{
		"model":"wan2.7-i2v","prompt":"ignored by metadata","image":"https://assets.example/ignored.png",
		"metadata":{"model":"wan2.7-i2v","input":{"prompt":"continue the clip","media":[
			{"type":"first_clip","url":"https://assets.example/source.mp4"}
		]},"parameters":{"resolution":"1080P","duration":8,"prompt_extend":false,"watermark":true,"seed":42}}
	}`)
	prepared, err := PrepareSubmit(raw, "application/json", "wan2.7-i2v", "wan2.7-i2v")
	require.NoError(t, err)
	assert.Equal(t, "continue the clip", prepared.Prompt)
	assert.Equal(t, 8, prepared.Duration)
	assert.Equal(t, "1080P", prepared.Resolution)
	assert.JSONEq(t, `{
		"model":"wan2.7-i2v","input":{"prompt":"continue the clip","media":[
			{"type":"first_clip","url":"https://assets.example/source.mp4"}
		]},"parameters":{"resolution":"1080P","duration":8,"prompt_extend":false,"watermark":true,"seed":42}
	}`, string(prepared.Body))

	stringMetadata := []byte(`{
		"model":"wan2.5-i2v-preview","prompt":"animate","image":"https://assets.example/frame.png",
		"metadata":"{\"parameters\":{\"audio\":true}}"
	}`)
	prepared, err = PrepareSubmit(stringMetadata, "application/json", "wan2.5-i2v-preview", "wan2.5-i2v-preview")
	require.NoError(t, err)
	assert.Contains(t, string(prepared.Body), `"img_url":"https://assets.example/frame.png"`)
	assert.NotContains(t, string(prepared.Body), `"media"`)
	assert.Contains(t, string(prepared.Body), `"audio":true`)
}

func TestAliPrepareSubmitRejectsAmbiguousUnsupportedAndOversizedInputs(t *testing.T) {
	valid := `{"model":"wan2.7-t2v","prompt":"scene"}`
	deep := `{"model":"wan2.7-t2v","prompt":"scene","metadata":` + strings.Repeat(`{"input":`, 34) + `null` + strings.Repeat(`}`, 34) + `}`
	tests := []struct {
		name        string
		body        string
		contentType string
		origin      string
		mapped      string
	}{
		{"duplicate model", `{"model":"wan2.7-t2v","model":"wan2.7-i2v","prompt":"scene"}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"unknown field", `{"model":"wan2.7-t2v","prompt":"scene","api_key":"secret"}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"wrong media type", valid, "text/plain", "wan2.7-t2v", "wan2.7-t2v"},
		{"both duration fields", `{"model":"wan2.7-t2v","prompt":"scene","duration":5,"seconds":5}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"fraction duration", `{"model":"wan2.7-t2v","prompt":"scene","duration":5.5}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"duration too high", `{"model":"wan2.7-t2v","prompt":"scene","duration":11}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"text resolution instead of dimensions", `{"model":"wan2.7-t2v","prompt":"scene","size":"720p"}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"unsupported dimensions", `{"model":"wan2.7-t2v","prompt":"scene","size":"999*999"}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"mapped model unknown", valid, "application/json", "wan2.7-t2v", "wan9-t2v"},
		{"metadata changes model", `{"model":"wan2.7-t2v","prompt":"scene","metadata":{"model":"wan2.7-i2v"}}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"metadata unknown top field", `{"model":"wan2.7-t2v","prompt":"scene","metadata":{"credential":"secret"}}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"metadata unknown nested field", `{"model":"wan2.7-t2v","prompt":"scene","metadata":{"parameters":{"cost":2}}}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"size and resolution conflict", `{"model":"wan2.7-i2v","image":"https://assets.example/a.png","metadata":{"parameters":{"size":"1280*720","resolution":"720P"}}}`, "application/json", "wan2.7-i2v", "wan2.7-i2v"},
		{"missing text prompt", `{"model":"wan2.7-t2v"}`, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
		{"missing image", `{"model":"wan2.7-i2v","prompt":"scene"}`, "application/json", "wan2.7-i2v", "wan2.7-i2v"},
		{"invalid image scheme", `{"model":"wan2.7-i2v","image":"file:///tmp/a.png"}`, "application/json", "wan2.7-i2v", "wan2.7-i2v"},
		{"duplicate media type", `{"model":"wan2.7-i2v","metadata":{"input":{"media":[{"type":"first_frame","url":"https://a.example/1"},{"type":"first_frame","url":"https://a.example/2"}]}}}`, "application/json", "wan2.7-i2v", "wan2.7-i2v"},
		{"too deep", deep, "application/json", "wan2.7-t2v", "wan2.7-t2v"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareSubmit([]byte(test.body), test.contentType, test.origin, test.mapped)
			assert.Error(t, err)
		})
	}
	_, err := PrepareSubmit([]byte(valid+strings.Repeat(" ", MaxRequestBodyBytes)), "application/json", "wan2.7-t2v", "wan2.7-t2v")
	assert.ErrorIs(t, err, common.ErrBodyTooLarge)
}

func TestAliClientSubmitAndFetchWireContract(t *testing.T) {
	allowAliLoopback(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		assert.Equal(t, "Bearer dashscope-secret", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		switch call {
		case 1:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, SubmitPath, request.URL.Path)
			assert.Equal(t, "enable", request.Header.Get("X-DashScope-Async"))
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), `"model":"wan2.7-t2v"`)
			_, _ = io.WriteString(w, `{"output":{"task_id":"ali-task-1","task_status":"PENDING"},"request_id":"req-1"}`)
		case 2:
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, FetchPathPrefix+"ali-task-1", request.URL.Path)
			assert.Empty(t, request.Header.Get("X-DashScope-Async"))
			_, _ = io.WriteString(w, `{"output":{"task_id":"ali-task-1","task_status":"SUCCEEDED","video_url":"https://cdn.example/video.mp4"},"request_id":"req-2"}`)
		default:
			t.Fatalf("unexpected request %d", call)
		}
	}))
	defer server.Close()
	prepared, err := PrepareSubmit([]byte(`{"model":"wan2.7-t2v","prompt":"scene"}`), "application/json", "wan2.7-t2v", "wan2.7-t2v")
	require.NoError(t, err)
	client := &Client{HTTPClient: server.Client()}
	task, raw, err := client.Submit(context.Background(), server.URL, "dashscope-secret", prepared)
	require.NoError(t, err)
	assert.Equal(t, "ali-task-1", task.ProviderTaskID)
	assert.Equal(t, StatusSubmitted, task.Status)
	assert.Contains(t, string(raw), `"req-1"`)
	task, raw, err = client.Fetch(context.Background(), server.URL, "dashscope-secret", task.ProviderTaskID)
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, task.Status)
	assert.Equal(t, "https://cdn.example/video.mp4", task.ResultURL)
	assert.Contains(t, string(raw), `"req-2"`)
	assert.Equal(t, int32(2), calls.Load())
}

func TestAliClientClassifiesDefinitiveRejectionAndAmbiguousSubmit(t *testing.T) {
	allowAliLoopback(t)
	prepared, err := PrepareSubmit([]byte(`{"model":"wan2.7-t2v","prompt":"scene"}`), "application/json", "wan2.7-t2v", "wan2.7-t2v")
	require.NoError(t, err)
	tests := []struct {
		name       string
		statusCode int
		body       string
		failedTask bool
	}{
		{"structured provider rejection", http.StatusBadRequest, `{"code":"InvalidParameter","message":"bad request","output":{}}`, true},
		{"gateway ambiguity", http.StatusBadGateway, `upstream unavailable`, false},
		{"missing task id", http.StatusOK, `{"output":{"task_status":"PENDING"}}`, false},
		{"unknown task status", http.StatusOK, `{"output":{"task_id":"task-1","task_status":"MYSTERY"}}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			task, _, submitErr := (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "secret", prepared)
			if test.failedTask {
				require.NoError(t, submitErr)
				require.NotNil(t, task)
				assert.Equal(t, StatusFailed, task.Status)
				assert.Equal(t, "InvalidParameter", task.ErrorCode)
				return
			}
			assert.Error(t, submitErr)
			assert.True(t, SubmitWasDispatched(submitErr))
		})
	}
}

func TestAliClientFetchRejectsUnsafeOrInconsistentProviderData(t *testing.T) {
	allowAliLoopback(t)
	tests := []struct {
		name string
		body string
	}{
		{"task id mismatch", `{"output":{"task_id":"other","task_status":"RUNNING"}}`},
		{"unknown status", `{"output":{"task_id":"expected","task_status":"WAITING"}}`},
		{"unsafe result URL", `{"output":{"task_id":"expected","task_status":"SUCCEEDED","video_url":"file:///tmp/video"}}`},
		{"credential in result URL", `{"output":{"task_id":"expected","task_status":"SUCCEEDED","video_url":"https://user:secret@example.com/video"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, _, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "secret", "expected")
			assert.Error(t, err)
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"output":{"task_id":"expected","task_status":"FAILED","code":"ContentPolicy","message":"blocked"}}`)
	}))
	defer server.Close()
	task, _, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "secret", "expected")
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, task.Status)
	assert.Equal(t, "ContentPolicy", task.ErrorCode)
	assert.Equal(t, "blocked", task.ErrorMessage)
}

func TestAliClientTransportIsSSRFCheckedAndNoRedirect(t *testing.T) {
	client := NewHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(common.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, client.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://redirect.example", nil)
	assert.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)

	_, err := EffectiveBaseURL(DirectBaseURL)
	assert.NoError(t, err)
	_, err = EffectiveBaseURL("https://user:password@example.com")
	assert.Error(t, err)
	_, err = EffectiveBaseURL("https://example.com?key=secret")
	assert.Error(t, err)
	_, err = EffectiveBaseURL("http://example.com")
	assert.Error(t, err)

	transportError := errors.New("network failed")
	prepared, prepErr := PrepareSubmit([]byte(`{"model":"wan2.7-t2v","prompt":"scene"}`), "application/json", "wan2.7-t2v", "wan2.7-t2v")
	require.NoError(t, prepErr)
	_, _, err = (&Client{HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportError
	})}}).Submit(context.Background(), DirectBaseURL, "secret", prepared)
	assert.ErrorIs(t, err, transportError)
	assert.True(t, SubmitWasDispatched(err))
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
