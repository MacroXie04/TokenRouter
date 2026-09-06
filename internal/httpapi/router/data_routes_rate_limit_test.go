package router

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type dataRouteRateLimitProbe struct {
	cache.KVStore

	mu     sync.Mutex
	counts map[string]int64
}

func (s *dataRouteRateLimitProbe) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[key]++
	return s.counts[key], nil
}

func (s *dataRouteRateLimitProbe) Expire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (s *dataRouteRateLimitProbe) countForIP(ip string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, count := range s.counts {
		if strings.Contains(key, ":"+ip+":") {
			return count
		}
	}
	return 0
}

func requestDataRoute(handler http.Handler, path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestDataRoutesInheritGlobalRateLimitExactlyOnce(t *testing.T) {
	t.Setenv("GLOBAL_API_RATE_LIMIT_ENABLE", "true")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1")
	t.Setenv("GLOBAL_API_RATE_LIMIT_DURATION", "120")
	previousStore := cache.Store
	probe := &dataRouteRateLimitProbe{KVStore: previousStore, counts: make(map[string]int64)}
	cache.Store = probe
	t.Cleanup(func() { cache.Store = previousStore })

	handler := SetUpRouter()
	endpoints := []string{
		"/api/data/?start_timestamp=1&end_timestamp=1",
		"/api/data/users?start_timestamp=1&end_timestamp=1",
		"/api/data/self?start_timestamp=1&end_timestamp=1",
		"/api/data/flow?start_timestamp=1&end_timestamp=1",
		"/api/data/flow/self?start_timestamp=1&end_timestamp=1",
	}
	for index, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			ip := "192.0.2." + textutil.Int2Str(index+1)
			first := requestDataRoute(handler, endpoint, ip)
			require.Equal(t, http.StatusUnauthorized, first.Code, first.Body.String())
			assert.Equal(t, int64(1), probe.countForIP(ip), "first request must consume one global-rate-limit slot")

			second := requestDataRoute(handler, endpoint, ip)
			assert.Equal(t, http.StatusTooManyRequests, second.Code, second.Body.String())
			assert.Equal(t, int64(2), probe.countForIP(ip), "second request must be checked once and rejected")
		})
	}
}

func TestDashboardAdminRoutesInheritGlobalRateLimitExactlyOnce(t *testing.T) {
	t.Setenv("GLOBAL_API_RATE_LIMIT_ENABLE", "true")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1")
	t.Setenv("GLOBAL_API_RATE_LIMIT_DURATION", "120")
	previousStore := cache.Store
	probe := &dataRouteRateLimitProbe{KVStore: previousStore, counts: make(map[string]int64)}
	cache.Store = probe
	t.Cleanup(func() { cache.Store = previousStore })

	handler := SetUpRouter()
	endpoints := []string{
		"/api/channel",
		"/api/channel/update_balance",
		"/api/channel/update_balance/1",
		"/api/ability",
	}
	for index, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			ip := "198.51.100." + textutil.Int2Str(index+1)
			first := requestDataRoute(handler, endpoint, ip)
			require.Equal(t, http.StatusUnauthorized, first.Code, first.Body.String())
			assert.Equal(t, int64(1), probe.countForIP(ip), "first request must consume one global-rate-limit slot")

			second := requestDataRoute(handler, endpoint, ip)
			assert.Equal(t, http.StatusTooManyRequests, second.Code, second.Body.String())
			assert.Equal(t, int64(2), probe.countForIP(ip), "second request must be checked once and rejected")
		})
	}
}
