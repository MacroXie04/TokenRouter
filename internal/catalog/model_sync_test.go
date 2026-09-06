package catalog

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestModelSyncFetchHonorsCancellation(t *testing.T) {
	t.Cleanup(httpx.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "3")
	httpx.InitSSRF()
	requestStarted := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		<-request.Context().Done()
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fetchModelSyncBody(ctx, upstream.URL)
		result <- err
	}()
	select {
	case <-requestStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	select {
	case err := <-result:
		require.Error(t, err)
		require.True(t, errors.Is(err, context.Canceled), "unexpected error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("canceled upstream fetch did not return")
	}
}

func TestModelSyncHTTPClientAndPolicyAreBounded(t *testing.T) {
	t.Setenv("SYNC_HTTP_RETRY", "999")
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "2")
	t.Setenv("SYNC_HTTP_TIMEOUT_SECONDS", "999")
	t.Setenv("SYNC_HTTP_MAX_MB", "999")
	t.Setenv("SYNC_HTTP_MAX_BYTES", "2048")

	retries, timeout, maxBytes := modelSyncHTTPPolicy()
	require.Equal(t, maxModelSyncRetries, retries)
	require.Equal(t, time.Duration(maxModelSyncTimeoutSeconds)*time.Second, timeout)
	require.Equal(t, maxModelSyncMaxBytes, maxBytes)

	client := newModelSyncHTTPClient(timeout)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.Proxy)
	require.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.Equal(t, int64(modelSyncHeaderLimit), transport.MaxResponseHeaderBytes)
	require.NotNil(t, transport.TLSClientConfig)
	require.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	require.NotNil(t, client.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://example.com/catalog", nil)
	require.ErrorIs(t, client.CheckRedirect(request, []*http.Request{request}), http.ErrUseLastResponse)
}

func TestModelSyncHTTPPolicySupportsLegacyAliasesAndSafeMinimums(t *testing.T) {
	t.Setenv("SYNC_HTTP_RETRY", "")
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "0")
	t.Setenv("SYNC_HTTP_TIMEOUT_SECONDS", "-4")
	t.Setenv("SYNC_HTTP_MAX_MB", "")
	t.Setenv("SYNC_HTTP_MAX_BYTES", "12")

	retries, timeout, maxBytes := modelSyncHTTPPolicy()
	require.Equal(t, 1, retries)
	require.Equal(t, time.Second, timeout)
	require.Equal(t, int64(1024), maxBytes)
}

func TestValidateModelSyncURLRejectsRedirectHazardShapes(t *testing.T) {
	for _, raw := range []string{
		"", "file:///tmp/models.json", "https://user:secret@example.com/models.json",
		"https://example.com/models.json#fragment", "//example.com/models.json",
	} {
		require.Error(t, validateModelSyncURL(raw), raw)
	}
	require.NoError(t, validateModelSyncURL("https://example.com/models.json"))
}
