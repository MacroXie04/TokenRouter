package doubao

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestExactReferenceCatalogAndPricing(t *testing.T) {
	expected := []string{
		"doubao-seedance-1-0-pro-250528",
		"doubao-seedance-1-0-lite-t2v",
		"doubao-seedance-1-0-lite-i2v",
		"doubao-seedance-1-5-pro-251215",
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
	}
	assert.Equal(t, "doubao-video", ChannelName)
	assert.Equal(t, expected, ModelList())
	models := ModelList()
	models[0] = "mutated"
	assert.Equal(t, expected, ModelList(), "callers must not mutate the provider catalog")

	tests := []struct {
		name, model, resolution string
		hasVideo                bool
		want                    float64
		configured              bool
	}{
		{"base text", expected[4], "", false, 1, true},
		{"base video", expected[4], "720p", true, 28.0 / 46.0, true},
		{"1080 text", expected[4], " 1080P ", false, 51.0 / 46.0, true},
		{"1080 video", expected[4], "1080p", true, 31.0 / 46.0, true},
		{"4k text", expected[4], "4K", false, 26.0 / 46.0, true},
		{"4k video", expected[4], "4k", true, 16.0 / 46.0, true},
		{"fast video", expected[5], "", true, 22.0 / 37.0, true},
		{"fast unsupported resolution", expected[5], "1080p", true, 1, true},
		{"model without table", expected[0], "1080p", false, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, configured := VideoInputRatio(test.model, test.resolution, test.hasVideo)
			assert.Equal(t, test.configured, configured)
			assert.InDelta(t, test.want, got, 1e-12)
		})
	}
}

func TestPrepareSubmitUsesReferenceConversionOrdering(t *testing.T) {
	raw := []byte(`{
		"model":"doubao-seedance-2-0-260128",
		"prompt":"final prompt",
		"images":["https://assets.example/top.png"],
		"seconds":"8",
		"metadata":{
			"model":"metadata-model",
			"content":[
				{"type":"text","text":"metadata prompt must be removed"},
				{"type":"video_url","video_url":{"url":"https://assets.example/input.mp4"}}
			],
			"resolution":"1080p",
			"duration":3,
			"generate_audio":true,
			"watermark":false
		}
	}`)
	prepared, err := PrepareSubmit(raw, "doubao-seedance-2-0-260128", "mapped-endpoint")
	require.NoError(t, err)
	assert.Equal(t, ActionGenerate, prepared.Action)
	assert.Equal(t, "doubao-seedance-2-0-260128", prepared.OriginModel)
	assert.Equal(t, "mapped-endpoint", prepared.UpstreamModel)
	assert.Equal(t, "mapped-endpoint", prepared.Payload.Model, "model mapping is applied last")
	require.NotNil(t, prepared.Payload.Duration)
	assert.Equal(t, 8, *prepared.Payload.Duration, "positive top-level seconds overrides metadata")
	assert.True(t, prepared.HasVideoInput)
	assert.InDelta(t, 31.0/46.0, prepared.PriceRatio, 1e-12)
	require.Len(t, prepared.Payload.Content, 2)
	assert.Equal(t, "video_url", prepared.Payload.Content[0].Type,
		"metadata overlay replaces the provisional top-level image content")
	assert.Equal(t, "https://assets.example/input.mp4", prepared.Payload.Content[0].VideoURL.URL)
	assert.Equal(t, ContentItem{Type: "text", Text: "final prompt"}, prepared.Payload.Content[1])
	assert.NotContains(t, string(prepared.Body), "metadata prompt must be removed")
	assert.Contains(t, string(prepared.Body), `"model":"mapped-endpoint"`)
}

func TestPrepareSubmitRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	model := "doubao-seedance-1-0-pro-250528"
	tests := []struct {
		name string
		raw  string
	}{
		{"duplicate top-level key", `{"model":"` + model + `","model":"` + model + `","prompt":"x"}`},
		{"unknown top-level key", `{"model":"` + model + `","prompt":"x","extra":true}`},
		{"unknown metadata key", `{"model":"` + model + `","prompt":"x","metadata":{"extra":true}}`},
		{"insecure top-level image", `{"model":"` + model + `","prompt":"x","images":["http://assets.example/x"]}`},
		{"insecure metadata media", `{"model":"` + model + `","prompt":"x","metadata":{"content":[{"type":"video_url","video_url":{"url":"file:///tmp/x"}}]}}`},
		{"zero seconds", `{"model":"` + model + `","prompt":"x","seconds":0}`},
		{"trailing JSON", `{"model":"` + model + `","prompt":"x"}{}`},
		{"unsupported model", `{"model":"not-doubao","prompt":"x"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RequestedModel([]byte(test.raw))
			if test.name == "unknown metadata key" || test.name == "insecure top-level image" ||
				test.name == "insecure metadata media" || test.name == "zero seconds" {
				require.NoError(t, err)
				_, err = PrepareSubmit([]byte(test.raw), model, model)
			}
			require.Error(t, err)
		})
	}
	_, err := PrepareSubmit([]byte(`{"model":"`+model+`","prompt":"`+
		strings.Repeat("x", MaxPromptRunes+1)+`"}`), model, model)
	require.Error(t, err)
}

func TestClientUsesExactEndpointsAuthAndTotalTokens(t *testing.T) {
	var calls int
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "Bearer provider-key", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		switch calls {
		case 1:
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "/root/api/v3/contents/generations/tasks", request.URL.Path)
			return response(http.StatusOK, `{"id":"provider-1"}`), nil
		case 2:
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "/root/api/v3/contents/generations/tasks/provider-1", request.URL.Path)
			return response(http.StatusOK, `{
				"id":"provider-1","status":"succeeded",
				"content":{"video_url":"https://cdn.example/video.mp4"},
				"usage":{"completion_tokens":999,"total_tokens":7},
				"created_at":11,"updated_at":12
			}`), nil
		default:
			t.Fatal("unexpected provider call")
			return nil, nil
		}
	})}}
	prepared := &PreparedRequest{Action: ActionGenerate, Body: []byte(`{"model":"mapped","content":[{"type":"text","text":"x"}]}`)}
	submitted, raw, err := client.Submit(context.Background(), "https://ark.example/root/", "provider-key", prepared)
	require.NoError(t, err)
	assert.Equal(t, "provider-1", submitted.ProviderTaskID)
	assert.NotEmpty(t, raw)
	fetched, raw, err := client.Fetch(context.Background(), "https://ark.example/root", "provider-key", "provider-1")
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, fetched.Status)
	assert.Equal(t, 7, fetched.CompletionUnits, "billing uses total_tokens, not completion_tokens")
	assert.Equal(t, "https://cdn.example/video.mp4", fetched.ResultURL)
	assert.Equal(t, int64(11), fetched.CreatedAt)
	assert.Equal(t, int64(12), fetched.UpdatedAt)
	assert.NotEmpty(t, raw)
}

func TestClientFailsClosedOnTransportStatusAndResponseAmbiguity(t *testing.T) {
	prepared := &PreparedRequest{Action: ActionGenerate, Body: []byte(`{"model":"x"}`)}
	transportErr := errors.New("connection reset after write")
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}}
	_, _, err := client.Submit(context.Background(), "https://ark.example", "key", prepared)
	require.ErrorIs(t, err, transportErr)
	assert.True(t, SubmitWasDispatched(err))

	_, _, err = client.Submit(context.Background(), "file:///tmp/provider", "key", prepared)
	require.Error(t, err)
	assert.False(t, SubmitWasDispatched(err), "validation failures occur before dispatch")

	client.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, `{"error":{"code":"bad\ncode","message":"bad\nrequest"}}`), nil
	})
	_, _, err = client.Submit(context.Background(), "https://ark.example", "key", prepared)
	var providerErr *ProviderError
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, http.StatusBadRequest, providerErr.StatusCode)
	assert.Equal(t, "provider_error", providerErr.Code)
	assert.Equal(t, "bad request", providerErr.Message)
	assert.True(t, SubmitWasDispatched(err))

	invalidFetchBodies := []string{
		`{"id":"other","status":"processing"}`,
		`{"id":"provider-1","status":"surprise"}`,
		`{"id":"provider-1","status":"succeeded","content":{"video_url":"http://private.invalid/x"}}`,
		`{"id":"provider-1","status":"processing","usage":{"total_tokens":-1}}`,
		`{"id":"provider-1","id":"provider-1","status":"processing"}`,
	}
	for _, body := range invalidFetchBodies {
		client.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, body), nil
		})
		_, _, err = client.Fetch(context.Background(), "https://ark.example", "key", "provider-1")
		require.Error(t, err, body)
	}

	client.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, strings.Repeat("x", int(MaxResponseBodyBytes)+1)), nil
	})
	_, _, err = client.Fetch(context.Background(), "https://ark.example", "key", "provider-1")
	require.Error(t, err)
}

func TestClientTransportIsNoRedirectSafeDialAndContextAware(t *testing.T) {
	client := NewHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
	assert.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocking := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}
	_, _, err := blocking.Fetch(ctx, "https://ark.example", "key", "provider-1")
	assert.ErrorIs(t, err, context.Canceled)

	common.InitSSRF()
	t.Cleanup(common.InitSSRF)
	for _, base := range []string{
		"http://ark.example", "file:///tmp/provider", "https://user:secret@ark.example",
		"https://ark.example?secret=1", "https://ark.example/#fragment",
	} {
		_, err := EffectiveBaseURL(base)
		require.Error(t, err, base)
	}
}
