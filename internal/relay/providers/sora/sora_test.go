package sora

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type soraRoundTripFunc func(*http.Request) (*http.Response, error)

func (f soraRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func allowSoraLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
}

func TestPrepareSubmitJSONValidatesAndRewritesModel(t *testing.T) {
	raw := []byte(`{
		"model":"sora-2-pro",
		"prompt":"  a lighthouse in a storm  ",
		"duration":"8",
		"size":"1792x1024",
		"input_reference":"https://assets.example/reference.png"
	}`)
	prepared, err := PrepareSubmit(raw, "application/json", "sora-2-pro", "vendor-video-v2", false, 0, "")
	require.NoError(t, err)
	assert.Equal(t, "sora-2-pro", prepared.Model)
	assert.Equal(t, "a lighthouse in a storm", prepared.Prompt)
	assert.Equal(t, 8, prepared.Seconds)
	assert.Equal(t, "1792x1024", prepared.Size)
	assert.True(t, prepared.HasInputReference)

	var body map[string]any
	require.NoError(t, json.Unmarshal(prepared.Body, &body))
	assert.Equal(t, "vendor-video-v2", body["model"])
	assert.Equal(t, "8", body["duration"])

	_, err = PrepareSubmit(
		[]byte(`{"model":"sora-2","prompt":"scene","seconds":4,"size":"1792x1024"}`),
		"application/json", "sora-2", "mapped", false, 0, "",
	)
	assert.ErrorContains(t, err, "not supported")
	_, err = PrepareSubmit(
		[]byte(`{"model":"sora-2","prompt":"scene","seconds":3601}`),
		"application/json", "sora-2", "mapped", false, 0, "",
	)
	assert.ErrorContains(t, err, "between 1 and 3600")
	_, err = PrepareSubmit(
		[]byte(`{"model":"sora-2","prompt":"scene","seconds":4,"duration":8}`),
		"application/json", "sora-2", "mapped", false, 0, "",
	)
	assert.ErrorContains(t, err, "must not both be provided")
	_, err = PrepareSubmit(
		[]byte(`{"model":"sora-2","prompt":"scene"} trailing`),
		"application/json", "sora-2", "mapped", false, 0, "",
	)
	assert.ErrorContains(t, err, "trailing JSON")
}

func TestPrepareSubmitMultipartPreservesFilesAndRewritesModel(t *testing.T) {
	var input bytes.Buffer
	writer := multipart.NewWriter(&input)
	require.NoError(t, writer.WriteField("model", "sora-2"))
	require.NoError(t, writer.WriteField("prompt", "animate this drawing"))
	require.NoError(t, writer.WriteField("seconds", "6"))
	file, err := writer.CreateFormFile("input_reference", "../drawing.png")
	require.NoError(t, err)
	_, err = file.Write([]byte("image-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	prepared, err := PrepareSubmit(
		input.Bytes(), writer.FormDataContentType(), "sora-2", "mapped-sora", false, 0, "",
	)
	require.NoError(t, err)
	assert.Equal(t, 6, prepared.Seconds)
	assert.True(t, prepared.HasInputReference)

	mediaType, params, err := mime.ParseMediaType(prepared.ContentType)
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)
	reader := multipart.NewReader(bytes.NewReader(prepared.Body), params["boundary"])
	fields := map[string][]string{}
	files := map[string]string{}
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		data, readErr := io.ReadAll(part)
		require.NoError(t, readErr)
		fields[part.FormName()] = append(fields[part.FormName()], string(data))
		if part.FileName() != "" {
			files[part.FormName()] = part.FileName()
		}
		require.NoError(t, part.Close())
	}
	assert.Equal(t, []string{"mapped-sora"}, fields["model"])
	assert.Equal(t, []string{"animate this drawing"}, fields["prompt"])
	assert.Equal(t, "drawing.png", files["input_reference"])
	assert.Equal(t, []string{"image-bytes"}, fields["input_reference"])

	var duplicate bytes.Buffer
	duplicateWriter := multipart.NewWriter(&duplicate)
	require.NoError(t, duplicateWriter.WriteField("model", "sora-2"))
	require.NoError(t, duplicateWriter.WriteField("prompt", "ambiguous duration"))
	require.NoError(t, duplicateWriter.WriteField("seconds", "1"))
	require.NoError(t, duplicateWriter.WriteField("seconds", "3600"))
	require.NoError(t, duplicateWriter.Close())
	_, err = PrepareSubmit(
		duplicate.Bytes(), duplicateWriter.FormDataContentType(), "sora-2", "mapped-sora", false, 0, "",
	)
	assert.ErrorContains(t, err, "seconds must not be repeated")
}

func TestSoraClientTransportAndProtocolContract(t *testing.T) {
	allowSoraLoopback(t)
	defaultClient := NewHTTPClient()
	transport, ok := defaultClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, defaultClient.CheckRedirect)
	redirectRequest := httptest.NewRequest(http.MethodGet, "https://redirect.example", nil)
	assert.ErrorIs(t, defaultClient.CheckRedirect(redirectRequest, []*http.Request{redirectRequest}), http.ErrUseLastResponse)

	publicID := "task_" + strings.Repeat("a", 32)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		assert.Equal(t, "Bearer upstream-secret", request.Header.Get("Authorization"))
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/videos":
			assert.Equal(t, publicID, request.Header.Get("Idempotency-Key"))
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"provider-123","object":"video","status":"queued"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/videos/provider-origin/remix":
			assert.Equal(t, publicID, request.Header.Get("Idempotency-Key"))
			_, _ = writer.Write([]byte(`{"task_id":"provider-remix","status":"processing"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/videos/provider-123":
			_, _ = writer.Write([]byte(`{"id":"provider-123","status":"completed","progress":100}`))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/videos/provider-123/content":
			writer.Header().Set("Content-Type", "video/mp4")
			_, _ = writer.Write([]byte("video-content"))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	httpClient := server.Client()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := &Client{HTTPClient: httpClient}
	prepared, err := PrepareSubmit(
		[]byte(`{"model":"sora-2","prompt":"a calm lake"}`),
		"application/json", "sora-2", "sora-2", false, 0, "",
	)
	require.NoError(t, err)

	created, _, err := client.Submit(context.Background(), server.URL, "upstream-secret", publicID, "", prepared, false)
	require.NoError(t, err)
	assert.Equal(t, "provider-123", created.ID)
	remixed, _, err := client.Submit(context.Background(), server.URL, "upstream-secret", publicID, "provider-origin", prepared, true)
	require.NoError(t, err)
	assert.Equal(t, "provider-remix", remixed.ID)
	fetched, _, err := client.Fetch(context.Background(), server.URL, "upstream-secret", "provider-123")
	require.NoError(t, err)
	assert.Equal(t, "completed", fetched.Status)
	content, err := client.Content(context.Background(), server.URL, "upstream-secret", "provider-123")
	require.NoError(t, err)
	_, bounded := content.Body.(*boundedContentReadCloser)
	assert.True(t, bounded, "content responses must always use the streaming byte limit")
	data, err := io.ReadAll(content.Body)
	require.NoError(t, err)
	require.NoError(t, content.Body.Close())
	assert.Equal(t, "video/mp4", content.Header.Get("Content-Type"))
	assert.Equal(t, "video-content", string(data))
	assert.Equal(t, int32(4), requests.Load())
}

func TestSoraClientMapsBoundedProviderErrorsAsDispatched(t *testing.T) {
	allowSoraLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(server.Close)
	prepared, err := PrepareSubmit(
		[]byte(`{"model":"sora-2","prompt":"a calm lake"}`),
		"application/json", "sora-2", "sora-2", false, 0, "",
	)
	require.NoError(t, err)
	_, _, err = (&Client{HTTPClient: server.Client()}).Submit(
		context.Background(), server.URL, "secret", "task_"+strings.Repeat("b", 32), "", prepared, false,
	)
	require.Error(t, err)
	assert.True(t, SubmitWasDispatched(err))
	var upstream *relaycommon.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusTooManyRequests, upstream.StatusCode)
}

func TestSoraClientRejectsConflictingAndMismatchedProviderIDs(t *testing.T) {
	conflicting := httptest.NewRecorder()
	conflicting.WriteHeader(http.StatusOK)
	_, _ = conflicting.WriteString(`{"id":"provider-a","task_id":"provider-b","status":"queued"}`)
	_, _, err := parseTaskResponse(conflicting.Result())
	assert.ErrorContains(t, err, "conflicting ids")

	allowSoraLoopback(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/v1/videos/provider-expected", request.URL.Path)
		_, _ = writer.Write([]byte(`{"id":"provider-other","status":"failed"}`))
	}))
	t.Cleanup(server.Close)
	_, _, err = (&Client{HTTPClient: server.Client()}).Fetch(
		context.Background(), server.URL, "secret", "provider-expected",
	)
	assert.ErrorContains(t, err, "does not match")
}

func TestSoraContentBodyRejectsChunkedDataPastBound(t *testing.T) {
	body := &boundedContentReadCloser{
		reader:    strings.NewReader("1234"),
		closer:    io.NopCloser(strings.NewReader("")),
		remaining: 3,
	}
	data, err := io.ReadAll(body)
	assert.Equal(t, "123", string(data))
	assert.True(t, errors.Is(err, httpx.ErrBodyTooLarge), err)
}

func TestSoraContentRedactsProviderIDFromTransportError(t *testing.T) {
	const providerID = "provider-secret-task-id"
	client := &Client{HTTPClient: &http.Client{Transport: soraRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: request.Method, URL: request.URL.String(), Err: errors.New("dial failed")}
	})}}

	_, err := client.Content(context.Background(), "https://provider.example", "secret", providerID)
	require.Error(t, err)
	require.NotContains(t, err.Error(), providerID)
	require.NotContains(t, err.Error(), "provider.example")
}
