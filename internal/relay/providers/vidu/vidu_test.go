package vidu

import (
	"bytes"
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestViduModelCatalogDefaultsAndFourActions(t *testing.T) {
	assert.Equal(t, []string{"viduq2", "viduq1", "vidu2.0", "vidu1.5"}, ModelList)
	tests := []struct {
		name   string
		body   string
		action Action
		path   string
	}{
		{"text", `{"model":"viduq1","prompt":"move"}`, ActionTextGenerate, "/ent/v2/text2video"},
		{"image", `{"model":"viduq1","prompt":"move","images":["image-a"]}`, ActionGenerate, "/ent/v2/img2video"},
		{"first-tail", `{"model":"viduq1","prompt":"move","images":["image-a","image-b"]}`, ActionFirstTailGenerate, "/ent/v2/start-end2video"},
		{"reference", `{"model":"viduq1","prompt":"move","images":["a","b","c"]}`, ActionReferenceGenerate, "/ent/v2/reference2video"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, err := RequestedModel([]byte(test.body), "application/json")
			require.NoError(t, err)
			prepared, err := PrepareSubmit([]byte(test.body), "application/json", model, "mapped-model")
			require.NoError(t, err)
			assert.Equal(t, test.action, prepared.Action)
			path, err := prepared.Action.Path()
			require.NoError(t, err)
			assert.Equal(t, test.path, path)
			assert.Equal(t, DefaultDuration, prepared.Payload.Duration)
			assert.Equal(t, DefaultResolution, prepared.Payload.Resolution)
			assert.Equal(t, DefaultMovementAmplitude, prepared.Payload.MovementAmplitude)
			assert.Equal(t, "mapped-model", prepared.Payload.Model)
		})
	}
}

func TestViduMetadataOrderingAndMappedModelProtection(t *testing.T) {
	// The reference chooses its route before metadata overlays images. Preserve
	// that observable ordering while validating the final payload.
	raw := []byte(`{"model":"viduq2","prompt":"top","images":["one"],"metadata":{"images":["a","b","c"],"prompt":"metadata","duration":9,"model":"attacker"}}`)
	prepared, err := PrepareSubmit(raw, "application/json", "viduq2", "viduq2-pro")
	require.NoError(t, err)
	assert.Equal(t, ActionGenerate, prepared.Action)
	assert.Equal(t, []string{"a", "b", "c"}, prepared.Payload.Images)
	assert.Equal(t, "metadata", prepared.Payload.Prompt)
	assert.Equal(t, 9, prepared.Payload.Duration)
	assert.Equal(t, "viduq2-pro", prepared.Payload.Model)

	raw = []byte(`{"model":"viduq2","prompt":"x","metadata":{"action":"referenceGenerate","images":["a","b","c"],"model":"attacker"}}`)
	prepared, err = PrepareSubmit(raw, "application/json", "viduq2", "viduq2-turbo")
	require.NoError(t, err)
	assert.Equal(t, ActionReferenceGenerate, prepared.Action)
	assert.Equal(t, "viduq2", prepared.Payload.Model, "reference generation strips Vidu Q2 suffixes")
}

func TestViduRequestValidationIsBounded(t *testing.T) {
	_, err := RequestedModel([]byte(`{"model":"viduq1","model":"viduq2","prompt":"x"}`), "application/json")
	assert.ErrorContains(t, err, "duplicate")
	_, err = PrepareSubmit([]byte(`{"model":"viduq1","prompt":"x","metadata":{"prompt":""}}`),
		"application/json", "viduq1", "viduq1")
	assert.ErrorContains(t, err, "prompt is required")
	_, err = PrepareSubmit([]byte(`{"model":"viduq1","prompt":"x","metadata":{"duration":3601}}`),
		"application/json", "viduq1", "viduq1")
	assert.ErrorContains(t, err, "duration must be between")
	_, err = RequestedModel(bytes.Repeat([]byte{'x'}, int(MaxRequestBodyBytes)+1), "application/json")
	assert.ErrorIs(t, err, httpx.ErrBodyTooLarge)
}

func TestViduMultipartRequestUsesSameBoundedConversion(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "viduq1"))
	require.NoError(t, writer.WriteField("prompt", "multipart video"))
	require.NoError(t, writer.WriteField("images", "image-a"))
	require.NoError(t, writer.WriteField("images", "image-b"))
	require.NoError(t, writer.WriteField("duration", "7"))
	require.NoError(t, writer.Close())
	model, err := RequestedModel(body.Bytes(), writer.FormDataContentType())
	require.NoError(t, err)
	prepared, err := PrepareSubmit(body.Bytes(), writer.FormDataContentType(), model, "mapped-vidu")
	require.NoError(t, err)
	assert.Equal(t, ActionFirstTailGenerate, prepared.Action)
	assert.Equal(t, []string{"image-a", "image-b"}, prepared.Payload.Images)
	assert.Equal(t, 7, prepared.Payload.Duration)
	assert.Equal(t, "mapped-vidu", prepared.Payload.Model)
}

func TestViduClientUsesExactRoutesTokenAuthAndStatusCodec(t *testing.T) {
	var calls atomic.Int32
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "Token provider-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		switch calls.Add(1) {
		case 1:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "/prefix/ent/v2/start-end2video", request.URL.Path)
			return response(http.StatusOK, `{"task_id":"provider-1","state":"created","created_at":"now"}`), nil
		case 2:
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "/prefix/ent/v2/tasks/provider-1/creations", request.URL.Path)
			return response(http.StatusOK, `{"state":"success","credits":999,"payload":"opaque","creations":[{"id":"c1","url":"https://cdn.example/video.mp4","cover_url":"https://cdn.example/cover.jpg"}]}`), nil
		default:
			return nil, errors.New("unexpected request")
		}
	})}}
	prepared, err := PrepareSubmit([]byte(`{"model":"viduq1","prompt":"x","images":["a","b"]}`),
		"application/json", "viduq1", "mapped")
	require.NoError(t, err)
	submitted, _, err := client.Submit(context.Background(), "https://provider.example/prefix", "provider-key", prepared)
	require.NoError(t, err)
	assert.Equal(t, StatusSubmitted, submitted.Status)
	assert.Equal(t, "provider-1", submitted.ProviderTaskID)
	completed, _, err := client.Fetch(context.Background(), "https://provider.example/prefix", "provider-key", "provider-1")
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, completed.Status)
	assert.Equal(t, 999, completed.Credits)
	assert.Equal(t, "https://cdn.example/video.mp4", completed.ResultURL)
}

func TestViduStatusCodecMatchesReferenceStates(t *testing.T) {
	tests := []struct {
		state string
		want  TaskStatus
	}{
		{"created", StatusSubmitted},
		{"queueing", StatusSubmitted},
		{"processing", StatusProcessing},
		{"success", StatusSucceeded},
		{"failed", StatusFailed},
	}
	for _, test := range tests {
		status, err := parseStatus(test.state)
		require.NoError(t, err)
		assert.Equal(t, test.want, status)
	}
	_, err := parseStatus("unknown")
	assert.ErrorContains(t, err, "unknown Vidu task state")
}

func TestViduClientBoundsResponsesAndMarksDispatchAmbiguity(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset after write")
	})}}
	prepared, err := PrepareSubmit([]byte(`{"model":"viduq1","prompt":"x"}`),
		"application/json", "viduq1", "viduq1")
	require.NoError(t, err)
	_, _, err = client.Submit(context.Background(), "https://provider.example", "provider-key", prepared)
	assert.True(t, SubmitWasDispatched(err))

	client = &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, strings.Repeat("x", int(MaxResponseBodyBytes)+1)), nil
	})}}
	_, _, err = client.Fetch(context.Background(), "https://provider.example", "provider-key", "provider-1")
	assert.Error(t, err)

	client = &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"state":"failed"}`), nil
	})}}
	failed, _, err := client.Submit(context.Background(), "https://provider.example", "provider-key", prepared)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, failed.Status)
	assert.Empty(t, failed.ProviderTaskID)
}

func TestViduNoRedirectPolicy(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "https://provider.example/next", nil)
	response := &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://elsewhere.example"}}, Request: request}
	err := NewHTTPClient().CheckRedirect(request, []*http.Request{{URL: request.URL, Response: response}})
	assert.ErrorIs(t, err, http.ErrUseLastResponse)
}
