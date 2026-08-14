package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
)

func TestModelSyncFetchHonorsCancellation(t *testing.T) {
	t.Cleanup(common.InitSSRF)
	t.Setenv("SSRF_DISABLE", "true")
	t.Setenv("SYNC_HTTP_RETRY_COUNT", "3")
	common.InitSSRF()
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
