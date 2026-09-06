package vertex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	geminitask "github.com/tokenrouter/tokenrouter/relay/channel/task/gemini"
	vertexcore "github.com/tokenrouter/tokenrouter/relay/channel/vertex"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testConfig() Config {
	return Config{
		Snapshot: Snapshot{BaseURL: "https://vertex.example/v1", Region: "us-central1", CustomBase: true},
		Credential: `{"type":"service_account","project_id":"project-123","private_key":"unused",` +
			`"client_email":"vertex@project-123.iam.gserviceaccount.com"}`,
	}
}

func testPrepared(t *testing.T) *geminitask.PreparedRequest {
	t.Helper()
	raw := []byte(`{"model":"veo-3.1-generate-preview","prompt":"make a safe video","duration":8,"size":"1280x720"}`)
	prepared, err := geminitask.PrepareSubmit(raw, "application/json",
		"veo-3.1-generate-preview", "veo-3.1-generate-preview")
	require.NoError(t, err)
	return prepared
}

func testMP4() []byte {
	return []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
}

func response(status int, body string, request *http.Request) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestPrepareSnapshotUsesMappedModelRegionAndServiceAccounts(t *testing.T) {
	snapshot, err := PrepareSnapshot("", `{"veo-3.1-fast-generate-preview":"europe-west4","default":"global"}`,
		"", "veo-3.1-fast-generate-preview")
	require.NoError(t, err)
	assert.Equal(t, "europe-west4", snapshot.Region)
	assert.Equal(t, "https://europe-west4-aiplatform.googleapis.com/v1", snapshot.BaseURL)
	assert.False(t, snapshot.CustomBase)
	require.NoError(t, ValidateSnapshot(snapshot, "veo-3.1-fast-generate-preview"))

	custom, err := PrepareSnapshot("https://vertex.mock.example/root", "europe-west4", "",
		"veo-3.1-fast-generate-preview")
	require.NoError(t, err)
	assert.True(t, custom.CustomBase)
	assert.Equal(t, "https://vertex.mock.example/root/v1", custom.BaseURL)
	require.NoError(t, ValidateSnapshot(custom, "veo-3.1-fast-generate-preview"))

	_, err = PrepareSnapshot("", "global", `{"vertex_key_type":"api_key"}`, "veo-3.1-fast-generate-preview")
	require.EqualError(t, err, "Vertex AI Veo tasks require a service-account credential")
}

func TestClientSubmitAndFetchUseVertexLongRunningContract(t *testing.T) {
	operationName := "projects/project-123/locations/us-central1/publishers/google/models/veo-3.1-generate-preview/operations/op-123"
	videoBytes := testMP4()
	encodedVideo := base64.StdEncoding.EncodeToString(videoBytes)
	var calls atomic.Int32
	client := &Client{
		TokenProvider: func(ctx context.Context, raw string, credentials vertexcore.Credentials) (string, error) {
			assert.NoError(t, ctx.Err())
			assert.Contains(t, raw, "project-123")
			assert.Equal(t, "project-123", credentials.ProjectID)
			return "oauth-access-token", nil
		},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			call := calls.Add(1)
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "Bearer oauth-access-token", request.Header.Get("Authorization"))
			assert.Equal(t, "project-123", request.Header.Get("x-goog-user-project"))
			assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			if call == 1 {
				assert.Equal(t, "/v1/projects/project-123/locations/us-central1/publishers/google/models/veo-3.1-generate-preview:predictLongRunning", request.URL.Path)
				assert.Contains(t, string(body), `"sampleCount":1`)
				assert.NotContains(t, string(body), `"model"`)
				return response(http.StatusOK, `{"name":"`+operationName+`"}`, request), nil
			}
			assert.Equal(t, "/v1/projects/project-123/locations/us-central1/publishers/google/models/veo-3.1-generate-preview:fetchPredictOperation", request.URL.Path)
			assert.JSONEq(t, `{"operationName":"`+operationName+`"}`, string(body))
			return response(http.StatusOK, `{"name":"`+operationName+`","done":true,"response":{"videos":[{"mimeType":"video/mp4","bytesBase64Encoded":"`+encodedVideo+`"}]}}`, request), nil
		})},
	}

	providerTask, _, err := client.Submit(context.Background(), testConfig(), testPrepared(t))
	require.NoError(t, err)
	require.NotNil(t, providerTask)
	assert.Equal(t, StatusProcessing, providerTask.Status)
	assert.Equal(t, operationName, providerTask.ProviderTaskID)

	providerTask, _, err = client.Fetch(context.Background(), testConfig(), "veo-3.1-generate-preview", operationName)
	require.NoError(t, err)
	require.NotNil(t, providerTask.InlineVideo)
	assert.Equal(t, StatusSucceeded, providerTask.Status)
	assert.Equal(t, "video/mp4", providerTask.InlineVideo.MIMEType)
	assert.Equal(t, int64(len(videoBytes)), providerTask.InlineVideo.DecodedBytes)
	reader, err := providerTask.InlineVideo.Reader()
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, videoBytes, decoded)
	assert.Equal(t, int32(2), calls.Load())
}

func TestInlineVideoVariantsAreBoundedAndUnambiguous(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testMP4())
	for _, test := range []struct {
		name   string
		mutate func(*operationResponse)
	}{
		{"videos", func(operation *operationResponse) {
			operation.Response.Videos = []operationVideo{{MIMEType: "video/mp4", BytesBase64Encoded: encoded}}
		}},
		{"top-level bytes", func(operation *operationResponse) {
			operation.Response.BytesBase64Encoded, operation.Response.Encoding = encoded, "mp4"
		}},
		{"top-level video", func(operation *operationResponse) {
			operation.Response.Video = encoded
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var operation operationResponse
			test.mutate(&operation)
			video, err := extractInlineVideo(&operation)
			require.NoError(t, err)
			assert.Equal(t, "video/mp4", video.MIMEType)
		})
	}

	var ambiguous operationResponse
	ambiguous.Response.Video = encoded
	ambiguous.Response.BytesBase64Encoded = encoded
	_, err := extractInlineVideo(&ambiguous)
	require.Error(t, err)

	var invalid operationResponse
	invalid.Response.Video = base64.StdEncoding.EncodeToString([]byte("not a video"))
	_, err = extractInlineVideo(&invalid)
	require.Error(t, err)
}

func TestSubmitRejectsMalformedCredentialsBeforeProviderNetwork(t *testing.T) {
	config := testConfig()
	config.Credential = `{"type":"service_account","project_id":"project-123","private_key":"malformed",` +
		`"client_email":"secret@example.test"}`
	var calls atomic.Int32
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	})}}

	_, _, err := client.Submit(context.Background(), config, testPrepared(t))
	require.Error(t, err)
	assert.False(t, SubmitWasDispatched(err))
	assert.Zero(t, calls.Load())
	assert.NotContains(t, err.Error(), "secret@example.test")
	assert.NotContains(t, err.Error(), "malformed")
}

func TestSubmitRejectsExternalStorageOutputBeforeProviderNetwork(t *testing.T) {
	prepared := testPrepared(t)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(prepared.Body, &envelope))
	parameters := envelope["parameters"].(map[string]any)
	parameters["storageUri"] = "gs://caller-owned-bucket/output/"
	prepared.Body, _ = json.Marshal(envelope)
	var calls atomic.Int32
	client := &Client{
		TokenProvider: func(context.Context, string, vertexcore.Credentials) (string, error) {
			return "must-not-be-requested", nil
		},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("must not be called")
		})},
	}
	_, _, err := client.Submit(context.Background(), testConfig(), prepared)
	require.EqualError(t, err, "Vertex AI Veo storageUri output is unsupported")
	assert.False(t, SubmitWasDispatched(err))
	assert.Zero(t, calls.Load())
}

func TestSubmitClassifiesProviderDispatchAmbiguity(t *testing.T) {
	client := &Client{
		TokenProvider: func(context.Context, string, vertexcore.Credentials) (string, error) { return "token", nil },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			trace := httptrace.ContextClientTrace(request.Context())
			require.NotNil(t, trace)
			trace.WroteRequest(httptrace.WroteRequestInfo{})
			return nil, errors.New("connection reset after write")
		})},
	}
	_, _, err := client.Submit(context.Background(), testConfig(), testPrepared(t))
	require.Error(t, err)
	assert.True(t, SubmitWasDispatched(err))
	assert.NotContains(t, err.Error(), "project-123")
}

func TestFetchRejectsOperationProjectMismatchBeforeOAuthOrProviderNetwork(t *testing.T) {
	operationName := "projects/other-project/locations/us-central1/publishers/google/models/" +
		"veo-3.1-generate-preview/operations/op-123"
	var tokenCalls, providerCalls atomic.Int32
	client := &Client{
		TokenProvider: func(context.Context, string, vertexcore.Credentials) (string, error) {
			tokenCalls.Add(1)
			return "must-not-be-requested", nil
		},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			providerCalls.Add(1)
			return nil, errors.New("must not be called")
		})},
	}

	_, _, err := client.Fetch(context.Background(), testConfig(), "veo-3.1-generate-preview", operationName)
	require.EqualError(t, err, "Vertex AI operation project does not match the service account")
	assert.Zero(t, tokenCalls.Load())
	assert.Zero(t, providerCalls.Load())
}

func TestStrictOperationAndResponseValidation(t *testing.T) {
	valid := "projects/project-123/locations/us-central1/publishers/google/models/veo-3.0-generate-001/operations/op_123"
	identity, err := parseOperationName(valid)
	require.NoError(t, err)
	assert.Equal(t, "project-123", identity.Project)
	assert.Equal(t, "us-central1", identity.Region)
	assert.Equal(t, "veo-3.0-generate-001", identity.Model)
	require.NoError(t, ValidateOperationName(valid, "veo-3.0-generate-001"))
	require.NoError(t, ValidateOperationForSnapshot(valid,
		Snapshot{BaseURL: "https://us-central1-aiplatform.googleapis.com/v1", Region: "us-central1"},
		"veo-3.0-generate-001"))
	require.Error(t, ValidateOperationForSnapshot(valid,
		Snapshot{BaseURL: "https://europe-west4-aiplatform.googleapis.com/v1", Region: "europe-west4"},
		"veo-3.0-generate-001"))

	for _, invalid := range []string{
		"models/veo-3.0-generate-001/operations/op", valid + "?key=secret",
		strings.Replace(valid, "project-123", "other/project", 1),
	} {
		_, err := parseOperationName(invalid)
		require.Error(t, err)
	}

	var parsed operationResponse
	err = strictDecode([]byte(`{"name":"first","name":"second"}`), &parsed)
	require.Error(t, err)
	var raw map[string]json.RawMessage
	err = strictDecode([]byte(`{"done":false} trailing`), &raw)
	require.Error(t, err)
}
