package catalog

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type ratioSyncRoundTripFunc func(*http.Request) (*http.Response, error)

func (function ratioSyncRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func allowRatioSyncLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
}

func TestRatioSyncHTTPClientUsesDirectSSRFSafeTransportAndRefusesRedirects(t *testing.T) {
	transport, ok := ratioSyncHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	require.NotNil(t, transport.DialContext)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, ratioSyncHTTPClient.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://redirect.example/", nil)
	assert.ErrorIs(t, ratioSyncHTTPClient.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)
}

func TestRatioSyncFetchStandardFormatsAndDeterministicDifferences(t *testing.T) {
	allowRatioSyncLoopback(t)
	previousPrices := billingsvc.ExportedModelPrices()
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"same-model": {Prompt: 2, Completion: 6},
	})
	t.Cleanup(func() { billingsvc.SetModelPriceRegistry(previousPrices) })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/ratio":
			_, _ = writer.Write([]byte(`{
				"success":true,
				"data":{
					"model_ratio":{"same-model":1,"new-model":2},
					"completion_ratio":{"same-model":3,"new-model":4}
				}
			}`))
		case "/pricing":
			_, _ = writer.Write([]byte(`{
				"success":true,
				"data":[{
					"model_name":"same-model",
					"quota_type":0,
					"model_ratio":1.5,
					"completion_ratio":3,
					"cache_ratio":0.25
				}]
			}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	data, err := FetchRatioSyncData(t.Context(), RatioSyncRequest{
		Timeout: 2,
		Upstreams: []RatioSyncUpstream{
			{Name: "ratio", BaseURL: server.URL, Endpoint: "/ratio"},
			{Name: "pricing", BaseURL: server.URL, Endpoint: "/pricing"},
		},
	})
	require.NoError(t, err)
	require.Len(t, data.TestResults, 2)
	assert.Equal(t, "ratio", data.TestResults[0].Name)
	assert.Equal(t, "success", data.TestResults[0].Status)
	assert.Equal(t, "pricing", data.TestResults[1].Name)
	assert.Equal(t, "success", data.TestResults[1].Status)

	// The local $2/1M prompt price is one compatibility ratio unit. The first
	// upstream matches it, while the second reports 1.5.
	sameModel := data.Differences["same-model"]
	require.Contains(t, sameModel, "model_ratio")
	assert.Equal(t, float64(1), sameModel["model_ratio"].Current)
	assert.Equal(t, "same", sameModel["model_ratio"].Upstreams["ratio"])
	assert.Equal(t, 1.5, sameModel["model_ratio"].Upstreams["pricing"])

	newModel := data.Differences["new-model"]
	require.Contains(t, newModel, "model_ratio")
	assert.Nil(t, newModel["model_ratio"].Current)
	assert.Equal(t, float64(2), newModel["model_ratio"].Upstreams["ratio"])
	assert.Equal(t, "same", newModel["model_ratio"].Upstreams["pricing"])
	assert.True(t, newModel["model_ratio"].Confidence["ratio"])
}

func TestRatioSyncFetchReportsSanitizedUpstreamFailures(t *testing.T) {
	allowRatioSyncLoopback(t)
	secret := "do-not-reflect-this-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(secret))
	}))
	t.Cleanup(server.Close)

	data, err := FetchRatioSyncData(t.Context(), RatioSyncRequest{
		Upstreams: []RatioSyncUpstream{{
			Name: "broken", BaseURL: server.URL, Endpoint: "/pricing?credential=" + secret,
		}},
	})
	require.NoError(t, err)
	require.Len(t, data.TestResults, 1)
	assert.Equal(t, "error", data.TestResults[0].Status)
	assert.Equal(t, "upstream returned HTTP 500", data.TestResults[0].Error)
	encoded, marshalErr := jsonutil.Marshal(data)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(encoded), secret)
}

func TestRatioSyncFetchCancellationAndRequestBounds(t *testing.T) {
	previousClient := ratioSyncHTTPClient
	ratioSyncHTTPClient = &http.Client{Transport: ratioSyncRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	t.Cleanup(func() { ratioSyncHTTPClient = previousClient })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data, err := FetchRatioSyncData(ctx, RatioSyncRequest{
		Upstreams: []RatioSyncUpstream{{Name: "canceled", BaseURL: "https://example.com", Endpoint: "/pricing"}},
	})
	require.NoError(t, err)
	require.Len(t, data.TestResults, 1)
	assert.Equal(t, "error", data.TestResults[0].Status)
	assert.Equal(t, "request canceled", data.TestResults[0].Error)

	tooMany := make([]RatioSyncUpstream, ratioSyncMaxUpstreams+1)
	for index := range tooMany {
		tooMany[index] = RatioSyncUpstream{Name: "source-" + strconv.Itoa(index), BaseURL: "https://example.com"}
	}
	_, err = FetchRatioSyncData(t.Context(), RatioSyncRequest{Upstreams: tooMany})
	assert.ErrorIs(t, err, ErrRatioSyncInvalidRequest)

	_, err = FetchRatioSyncData(t.Context(), RatioSyncRequest{Upstreams: []RatioSyncUpstream{{
		Name: "bad", BaseURL: "http://user:password@example.com", Endpoint: "/pricing",
	}}})
	assert.ErrorIs(t, err, ErrRatioSyncInvalidRequest)
	assert.NotContains(t, err.Error(), "password")
}

func TestRatioSyncResponseBodyLimitIsExact(t *testing.T) {
	exact := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", int(ratioSyncMaxResponseBytes)))),
	}
	body, err := readRatioSyncResponse(exact)
	require.NoError(t, err)
	assert.Len(t, body, int(ratioSyncMaxResponseBytes))

	over := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", int(ratioSyncMaxResponseBytes)+1))),
	}
	_, err = readRatioSyncResponse(over)
	require.Error(t, err)
	assert.Equal(t, "upstream response too large", err.Error())
}

func TestRatioSyncFormatConverters(t *testing.T) {
	openRouter, err := parseOpenRouterRatioSync([]byte(`{
		"data":[
			{"id":"priced","pricing":{"prompt":"0.000002","completion":"0.000006","input_cache_read":"0.0000005"}},
			{"id":"free","pricing":{"prompt":"0","completion":"0"}},
			{"id":"sentinel","pricing":{"prompt":"-1","completion":"-1"}}
		]
	}`))
	require.NoError(t, err)
	assert.Equal(t, float64(1), ratioSyncValueMap(openRouter["model_ratio"])["priced"])
	assert.Equal(t, float64(3), ratioSyncValueMap(openRouter["completion_ratio"])["priced"])
	assert.Equal(t, float64(0.25), ratioSyncValueMap(openRouter["cache_ratio"])["priced"])
	assert.Equal(t, float64(0), ratioSyncValueMap(openRouter["model_ratio"])["free"])
	assert.NotContains(t, ratioSyncValueMap(openRouter["model_ratio"]), "sentinel")

	modelsDev, err := parseModelsDevRatioSync([]byte(`{
		"z-provider":{"models":{"shared":{"cost":{"input":5,"output":10}}}},
		"a-provider":{"models":{"shared":{"cost":{"input":2,"output":6,"cache_read":0.5}}}}
	}`))
	require.NoError(t, err)
	assert.Equal(t, float64(1), ratioSyncValueMap(modelsDev["model_ratio"])["shared"])
	assert.Equal(t, float64(3), ratioSyncValueMap(modelsDev["completion_ratio"])["shared"])
	assert.Equal(t, float64(0.25), ratioSyncValueMap(modelsDev["cache_ratio"])["shared"])

	_, err = parseStandardRatioSync([]byte(`{"success":true,"data":{"model_ratio":{"bad":-1}}}`))
	assert.Error(t, err)
	_, err = parseStandardRatioSync([]byte(`{"success":true,"data":{"model_ratio":{"bad":null}}}`))
	assert.Error(t, err)
	_, err = parseStandardRatioSync([]byte(`{"success":true,"data":{"billing_expr":{"bad":42}}}`))
	assert.Error(t, err)
	_, err = parseStandardRatioSync([]byte(`{"success":true,"data":{"model_ratio":{"bad":1e13}}}`))
	assert.Error(t, err)
}

func TestRatioSyncRetriesTransportFailuresWithoutLeakingDetails(t *testing.T) {
	var calls atomic.Int32
	previousClient := ratioSyncHTTPClient
	ratioSyncHTTPClient = &http.Client{Transport: ratioSyncRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("transport secret")
	})}
	t.Cleanup(func() { ratioSyncHTTPClient = previousClient })

	data, err := FetchRatioSyncData(t.Context(), RatioSyncRequest{Upstreams: []RatioSyncUpstream{{
		Name: "retry", BaseURL: "https://example.com", Endpoint: "/pricing",
	}}})
	require.NoError(t, err)
	assert.EqualValues(t, ratioSyncMaxRetries, calls.Load())
	assert.Equal(t, "upstream request failed", data.TestResults[0].Error)
	assert.NotContains(t, data.TestResults[0].Error, "transport secret")
}
