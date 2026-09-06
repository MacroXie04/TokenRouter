package gemini

import (
	"context"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func allowGeminiLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
}

func TestGeminiVeoModelCatalogIsExactAndOwned(t *testing.T) {
	want := []string{
		"veo-3.0-generate-001", "veo-3.0-fast-generate-001",
		"veo-3.1-generate-preview", "veo-3.1-fast-generate-preview",
	}
	models := ModelList()
	assert.Equal(t, want, models)
	for _, model := range want {
		assert.True(t, IsModel(model))
	}
	models[0] = "mutated"
	assert.Equal(t, want, ModelList())
	assert.False(t, IsModel("veo-unknown"))
}

func TestGeminiVeoDeclaredModelKeepsInvalidFamilyRequestsOnVeoPath(t *testing.T) {
	assert.Equal(t, "veo-3.1-generate-preview", DeclaredModel(
		[]byte(`{"model":"veo-3.1-generate-preview","unknown":"rejected later"}`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"veo-3.1-generate-preview","model":"veo-3.0-generate-001"}`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"veo-3.1-generate-preview"} trailing`), "application/json"))
	assert.Empty(t, DeclaredModel([]byte(`{"model":"veo-3.1-generate-preview"}`), "multipart/form-data"))
}

func TestGeminiVeoPrepareSubmitMapsRequestAndMetadata(t *testing.T) {
	raw := []byte(`{
		"model":"veo-3.1-generate-preview","prompt":"  a quiet forest  ","size":"1080x1920","duration":"6",
		"image":"data:image/png;base64,` + tinyPNG + `",
		"metadata":{"negativePrompt":"traffic","personGeneration":"allow_adult","seed":42,
			"generateAudio":true,"sampleCount":99}
	}`)
	model, err := RequestedModel(raw, "application/json; charset=utf-8")
	require.NoError(t, err)
	prepared, err := PrepareSubmit(raw, "application/json", model, model)
	require.NoError(t, err)
	assert.Equal(t, 6, prepared.Duration)
	assert.Equal(t, "1080p", prepared.Resolution)
	assert.Equal(t, "9:16", prepared.AspectRatio)
	assert.True(t, prepared.HasImage)
	assert.JSONEq(t, `{
		"instances":[{"prompt":"a quiet forest","image":{"bytesBase64Encoded":"`+tinyPNG+`","mimeType":"image/png"}}],
		"parameters":{"sampleCount":1,"durationSeconds":6,"aspectRatio":"9:16","resolution":"1080p",
			"negativePrompt":"traffic","personGeneration":"allow_adult","seed":42,"generateAudio":true}
	}`, string(prepared.Body))

	stringMetadata := []byte(`{"model":"veo-3.1-fast-generate-preview","prompt":"scene","metadata":"{\"resolution\":\"4K\",\"aspectRatio\":\"16:9\",\"durationSeconds\":8}"}`)
	prepared, err = PrepareSubmit(stringMetadata, "application/json", "veo-3.1-fast-generate-preview", "veo-3.1-fast-generate-preview")
	require.NoError(t, err)
	assert.Equal(t, "4k", prepared.Resolution)
	assert.Equal(t, 2.333333, ResolutionMultiplier(prepared.UpstreamModel, prepared.Resolution))
	assert.Equal(t, 1.5, ResolutionMultiplier("veo-3.1-generate-preview", "4k"))
	assert.Equal(t, float64(1), ResolutionMultiplier("veo-3.0-generate-001", "720p"))
}

func TestGeminiVeoPrepareSubmitRejectsAmbiguousAndUnboundedInput(t *testing.T) {
	valid := `{"model":"veo-3.0-generate-001","prompt":"scene"}`
	deep := `{"model":"veo-3.0-generate-001","prompt":"scene","metadata":` + strings.Repeat(`{"x":`, 34) + `null` + strings.Repeat(`}`, 34) + `}`
	tests := []struct {
		name        string
		body        string
		contentType string
		origin      string
		mapped      string
	}{
		{"duplicate", `{"model":"veo-3.0-generate-001","model":"veo-3.1-generate-preview","prompt":"scene"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"unknown field", `{"model":"veo-3.0-generate-001","prompt":"scene","key":"secret"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"wrong content", valid, "text/plain", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"missing prompt", `{"model":"veo-3.0-generate-001"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"both duration", `{"model":"veo-3.0-generate-001","prompt":"scene","duration":6,"seconds":8}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"bad duration", `{"model":"veo-3.0-generate-001","prompt":"scene","duration":5}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"fraction duration", `{"model":"veo-3.0-generate-001","prompt":"scene","duration":6.5}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"bad size", `{"model":"veo-3.0-generate-001","prompt":"scene","size":"1000x1000"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"4k with veo 3.0", `{"model":"veo-3.0-generate-001","prompt":"scene","size":"3840x2160"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"multiple images", `{"model":"veo-3.0-generate-001","prompt":"scene","image":"` + tinyPNG + `","images":["` + tinyPNG + `"]}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"remote image", `{"model":"veo-3.0-generate-001","prompt":"scene","image":"https://example.com/a.png"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"MIME mismatch", `{"model":"veo-3.0-generate-001","prompt":"scene","image":"data:image/jpeg;base64,` + tinyPNG + `"}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"metadata unknown", `{"model":"veo-3.0-generate-001","prompt":"scene","metadata":{"apiKey":"secret"}}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"metadata bad storage", `{"model":"veo-3.0-generate-001","prompt":"scene","metadata":{"storageUri":"file:///tmp/video"}}`, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
		{"mapped unknown", valid, "application/json", "veo-3.0-generate-001", "veo-unknown"},
		{"too deep", deep, "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareSubmit([]byte(test.body), test.contentType, test.origin, test.mapped)
			assert.Error(t, err)
		})
	}
	_, err := PrepareSubmit([]byte(valid+strings.Repeat(" ", MaxRequestBodyBytes)), "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001")
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

func TestGeminiVeoClientSubmitFetchAndContentWireContract(t *testing.T) {
	allowGeminiLoopback(t)
	database, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "veo-policy.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Option{}))
	model.DB = database
	require.NoError(t, setting.Init())
	require.NoError(t, setting.UpdateOption(setting.GeminiVersionSettingsOption,
		`{"default":"v1beta","veo-3.1-generate-preview":"v1"}`))
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		assert.Equal(t, "gemini-secret", request.Header.Get("x-goog-api-key"))
		switch call {
		case 1:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "/v1/models/veo-3.1-generate-preview:predictLongRunning", request.URL.Path)
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			_, _ = io.WriteString(w, `{"name":"models/veo-3.1-generate-preview/operations/op-1"}`)
		case 2:
			assert.Equal(t, "/v1beta/models/veo-3.1-generate-preview/operations/op-1", request.URL.Path)
			_, _ = io.WriteString(w, `{"name":"models/veo-3.1-generate-preview/operations/op-1","done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"`+server.URL+`/video.mp4"}}]}}}`)
		case 3:
			assert.Equal(t, "/video.mp4", request.URL.Path)
			assert.Equal(t, "video/*,application/octet-stream", request.Header.Get("Accept"))
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = io.WriteString(w, "video-bytes")
		default:
			t.Fatalf("unexpected request %d", call)
		}
	}))
	defer server.Close()
	prepared, err := PrepareSubmit([]byte(`{"model":"veo-3.1-generate-preview","prompt":"scene"}`), "application/json", "veo-3.1-generate-preview", "veo-3.1-generate-preview")
	require.NoError(t, err)
	client := &Client{HTTPClient: server.Client()}
	task, _, err := client.Submit(context.Background(), server.URL, "gemini-secret", prepared)
	require.NoError(t, err)
	assert.Equal(t, StatusProcessing, task.Status)
	assert.Equal(t, "models/veo-3.1-generate-preview/operations/op-1", task.ProviderTaskID)
	task, _, err = client.Fetch(context.Background(), server.URL, "gemini-secret", task.ProviderTaskID)
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, task.Status)
	response, err := client.Content(context.Background(), server.URL, "gemini-secret", task.ResultURL)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "video-bytes", string(body))
	assert.Equal(t, int32(3), calls.Load())
}

func TestGeminiVeoContentDoesNotForwardCredentialCrossOrigin(t *testing.T) {
	allowGeminiLoopback(t)
	resultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Empty(t, request.Header.Get("x-goog-api-key"))
		_, _ = io.WriteString(w, "result")
	}))
	defer resultServer.Close()
	baseServer := httptest.NewServer(http.NotFoundHandler())
	defer baseServer.Close()
	response, err := (&Client{HTTPClient: resultServer.Client()}).Content(context.Background(), baseServer.URL, "top-secret", resultServer.URL+"/video")
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	assert.NoError(t, err)
}

func TestGeminiVeoClientClassifiesSubmitAndFetchOutcomes(t *testing.T) {
	allowGeminiLoopback(t)
	prepared, err := PrepareSubmit([]byte(`{"model":"veo-3.0-generate-001","prompt":"scene"}`), "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001")
	require.NoError(t, err)
	tests := []struct {
		name       string
		statusCode int
		body       string
		definitive bool
	}{
		{"provider rejection", http.StatusBadRequest, `{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`, true},
		{"malformed client rejection", http.StatusUnprocessableEntity, `not-json`, false},
		{"request timeout remains ambiguous", http.StatusRequestTimeout, `{"error":{"code":408,"message":"timeout","status":"DEADLINE_EXCEEDED"}}`, false},
		{"conflict remains ambiguous", http.StatusConflict, `{"error":{"code":409,"message":"conflict","status":"ABORTED"}}`, false},
		{"structured server error remains ambiguous", http.StatusServiceUnavailable, `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`, false},
		{"gateway ambiguity", http.StatusBadGateway, `unavailable`, false},
		{"missing operation", http.StatusOK, `{}`, false},
		{"wrong model operation", http.StatusOK, `{"name":"models/veo-other/operations/op"}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			task, _, submitErr := (&Client{HTTPClient: server.Client()}).Submit(context.Background(), server.URL, "secret", prepared)
			if test.definitive {
				require.NoError(t, submitErr)
				require.NotNil(t, task)
				assert.Equal(t, StatusFailed, task.Status)
				assert.Equal(t, "INVALID_ARGUMENT", task.ErrorCode)
				return
			}
			assert.Error(t, submitErr)
			assert.True(t, SubmitWasDispatched(submitErr))
			if test.name == "malformed client rejection" {
				var upstream *relaycommon.UpstreamError
				require.ErrorAs(t, submitErr, &upstream)
				assert.Equal(t, http.StatusUnprocessableEntity, upstream.StatusCode)
			}
		})
	}

	operation := "models/veo-3.0-generate-001/operations/op-1"
	fetchCases := []string{
		`{"name":"models/veo-3.0-generate-001/operations/other","done":false}`,
		`{"name":"models/veo-3.0-generate-001/operations/op-1","done":true,"response":{}}`,
		`{"name":"models/veo-3.0-generate-001/operations/op-1","done":true,"response":{"generateVideoResponse":{"generatedVideos":[{"video":{"uri":"file:///tmp/video"}}]}}}`,
	}
	for _, body := range fetchCases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		_, _, err := (&Client{HTTPClient: server.Client()}).Fetch(context.Background(), server.URL, "secret", operation)
		server.Close()
		assert.Error(t, err)
	}
	authFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"expired credential","status":"UNAUTHENTICATED"}}`)
	}))
	_, _, err = (&Client{HTTPClient: authFailure.Client()}).Fetch(context.Background(), authFailure.URL, "secret", operation)
	authFailure.Close()
	assert.Error(t, err, "polling authentication errors must remain retryable instead of reversing accepted work")
}

func TestGeminiVeoTransportAndContentBounds(t *testing.T) {
	client := NewHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, client.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://redirect.example", nil)
	assert.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)
	_, err := EffectiveBaseURL("https://user:secret@example.com")
	assert.Error(t, err)
	_, err = EffectiveBaseURL("http://example.com")
	assert.Error(t, err)

	prepared, prepErr := PrepareSubmit([]byte(`{"model":"veo-3.0-generate-001","prompt":"scene"}`), "application/json", "veo-3.0-generate-001", "veo-3.0-generate-001")
	require.NoError(t, prepErr)
	networkErr := errors.New("network failed")
	_, _, err = (&Client{HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, networkErr
	})}}).Submit(context.Background(), DirectBaseURL, "secret", prepared)
	assert.ErrorIs(t, err, networkErr)
	assert.True(t, SubmitWasDispatched(err))

	body := &boundedReadCloser{reader: strings.NewReader("1234"), closer: io.NopCloser(strings.NewReader("")), remaining: 3}
	_, err = io.ReadAll(body)
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
