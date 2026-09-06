package middleware

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rateLimitStoreProbe struct {
	cache.KVStore

	mu          sync.Mutex
	count       int64
	increments  int
	keys        []string
	expirations []time.Duration
	incrErr     error
}

func (s *rateLimitStoreProbe) Incr(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.increments++
	s.keys = append(s.keys, key)
	if s.incrErr != nil {
		return 0, s.incrErr
	}
	s.count++
	return s.count, nil
}

func (s *rateLimitStoreProbe) Expire(_ context.Context, _ string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expirations = append(s.expirations, ttl)
	return nil
}

func (s *rateLimitStoreProbe) snapshot() (int64, int, []string, []time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.increments, append([]string(nil), s.keys...), append([]time.Duration(nil), s.expirations...)
}

func useRateLimitStore(t *testing.T, store cache.KVStore) {
	t.Helper()
	previous := cache.Store
	cache.Store = store
	t.Cleanup(func() { cache.Store = previous })
}

func rateLimitTestRouter(handler gin.HandlerFunc, userID int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if userID != 0 {
		router.Use(func(c *gin.Context) {
			requestctx.SetUserId(c, userID)
			c.Next()
		})
	}
	router.Use(handler)
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return router
}

func requestRateLimitedRoute(router http.Handler) int {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}

func TestRateLimitConfigurationMatchesReferenceDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		for _, name := range []string{
			"GLOBAL_API_RATE_LIMIT_ENABLE",
			"GLOBAL_API_RATE_LIMIT",
			"GLOBAL_API_RATE_LIMIT_DURATION",
			"CRITICAL_RATE_LIMIT_ENABLE",
			"CRITICAL_RATE_LIMIT",
			"CRITICAL_RATE_LIMIT_DURATION",
			"SEARCH_RATE_LIMIT_ENABLE",
			"SEARCH_RATE_LIMIT",
			"SEARCH_RATE_LIMIT_DURATION",
		} {
			t.Setenv(name, "")
		}

		global := globalAPIRateLimitConfig()
		assert.True(t, global.enabled)
		assert.Equal(t, 360, global.limit)
		assert.Equal(t, 180*time.Second, global.window)

		critical := criticalRateLimitConfig()
		assert.True(t, critical.enabled)
		assert.Equal(t, 20, critical.limit)
		assert.Equal(t, 1200*time.Second, critical.window)

		search := searchRateLimitConfig()
		assert.True(t, search.enabled)
		assert.Equal(t, 10, search.limit)
		assert.Equal(t, 60*time.Second, search.window)
	})

	t.Run("configured", func(t *testing.T) {
		t.Setenv("GLOBAL_API_RATE_LIMIT_ENABLE", "false")
		t.Setenv("GLOBAL_API_RATE_LIMIT", "17")
		t.Setenv("GLOBAL_API_RATE_LIMIT_DURATION", "19")
		t.Setenv("CRITICAL_RATE_LIMIT_ENABLE", "false")
		t.Setenv("CRITICAL_RATE_LIMIT", "3")
		t.Setenv("CRITICAL_RATE_LIMIT_DURATION", "5")
		t.Setenv("SEARCH_RATE_LIMIT_ENABLE", "false")
		t.Setenv("SEARCH_RATE_LIMIT", "13")
		t.Setenv("SEARCH_RATE_LIMIT_DURATION", "17")

		global := globalAPIRateLimitConfig()
		assert.False(t, global.enabled)
		assert.Equal(t, 17, global.limit)
		assert.Equal(t, 19*time.Second, global.window)

		critical := criticalRateLimitConfig()
		assert.False(t, critical.enabled)
		assert.Equal(t, 3, critical.limit)
		assert.Equal(t, 5*time.Second, critical.window)

		search := searchRateLimitConfig()
		assert.False(t, search.enabled)
		assert.Equal(t, 13, search.limit)
		assert.Equal(t, 17*time.Second, search.window)
	})
}

func TestRateLimitConfigurationRejectsUnsafeValues(t *testing.T) {
	// The limiter adds a one-second cleanup cushion when setting the store TTL,
	// so the largest safe configured window must leave room for that addition.
	durationOverflow := strconv.FormatInt((math.MaxInt64-int64(time.Second))/int64(time.Second)+1, 10)
	tests := []struct {
		name       string
		enabled    string
		limit      string
		duration   string
		wantEnable bool
		wantLimit  int
		wantWindow time.Duration
	}{
		{name: "valid", enabled: "false", limit: "7", duration: "11", wantEnable: false, wantLimit: 7, wantWindow: 11 * time.Second},
		{name: "malformed", enabled: "not-a-boolean", limit: "many", duration: "later", wantEnable: true, wantLimit: 20, wantWindow: 1200 * time.Second},
		{name: "zero", enabled: "true", limit: "0", duration: "0", wantEnable: true, wantLimit: 20, wantWindow: 1200 * time.Second},
		{name: "negative", enabled: "true", limit: "-7", duration: "-11", wantEnable: true, wantLimit: 20, wantWindow: 1200 * time.Second},
		{name: "integer parse overflow", enabled: "true", limit: "9223372036854775808", duration: "9223372036854775808", wantEnable: true, wantLimit: 20, wantWindow: 1200 * time.Second},
		{name: "TTL cushion overflow", enabled: "true", limit: "7", duration: durationOverflow, wantEnable: true, wantLimit: 7, wantWindow: 1200 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_RATE_LIMIT_ENABLE", test.enabled)
			t.Setenv("TEST_RATE_LIMIT", test.limit)
			t.Setenv("TEST_RATE_LIMIT_DURATION", test.duration)

			config := loadRateLimitConfig(
				"TEST_RATE_LIMIT_ENABLE",
				"TEST_RATE_LIMIT",
				"TEST_RATE_LIMIT_DURATION",
				20,
				1200,
			)
			assert.Equal(t, test.wantEnable, config.enabled)
			assert.Equal(t, test.wantLimit, config.limit)
			assert.Equal(t, test.wantWindow, config.window)
		})
	}

	t.Run("search uses safe defaults for invalid values", func(t *testing.T) {
		t.Setenv("SEARCH_RATE_LIMIT_ENABLE", "true")
		t.Setenv("SEARCH_RATE_LIMIT", "-1")
		t.Setenv("SEARCH_RATE_LIMIT_DURATION", durationOverflow)

		config := searchRateLimitConfig()
		assert.True(t, config.enabled)
		assert.Equal(t, defaultSearchRateLimit, config.limit)
		assert.Equal(t, time.Duration(defaultSearchRateLimitDurationSeconds)*time.Second, config.window)
	})
}

func TestConfiguredRateLimitersEnforceLimitsAndSecondBasedExpiry(t *testing.T) {
	tests := []struct {
		name        string
		enableEnv   string
		limitEnv    string
		durationEnv string
		limitValue  string
		windowValue string
		wantLimit   int
		wantWindow  time.Duration
		wantKey     string
		userID      int
		factory     func() gin.HandlerFunc
	}{
		{
			name: "global defaults", enableEnv: "GLOBAL_API_RATE_LIMIT_ENABLE", limitEnv: "GLOBAL_API_RATE_LIMIT", durationEnv: "GLOBAL_API_RATE_LIMIT_DURATION",
			wantLimit: 360, wantWindow: 180 * time.Second, wantKey: "rate:global:192.0.2.10:", factory: GlobalRateLimit,
		},
		{
			name: "global configured", enableEnv: "GLOBAL_API_RATE_LIMIT_ENABLE", limitEnv: "GLOBAL_API_RATE_LIMIT", durationEnv: "GLOBAL_API_RATE_LIMIT_DURATION",
			limitValue: "3", windowValue: "2", wantLimit: 3, wantWindow: 2 * time.Second, wantKey: "rate:global:192.0.2.10:", factory: GlobalRateLimit,
		},
		{
			name: "critical defaults", enableEnv: "CRITICAL_RATE_LIMIT_ENABLE", limitEnv: "CRITICAL_RATE_LIMIT", durationEnv: "CRITICAL_RATE_LIMIT_DURATION",
			wantLimit: 20, wantWindow: 1200 * time.Second, wantKey: "rate:critical:192.0.2.10:", factory: CriticalRateLimit,
		},
		{
			name: "critical configured seconds", enableEnv: "CRITICAL_RATE_LIMIT_ENABLE", limitEnv: "CRITICAL_RATE_LIMIT", durationEnv: "CRITICAL_RATE_LIMIT_DURATION",
			limitValue: "3", windowValue: "2", wantLimit: 3, wantWindow: 2 * time.Second, wantKey: "rate:critical:192.0.2.10:", factory: CriticalRateLimit,
		},
		{
			name: "user critical defaults", enableEnv: "CRITICAL_RATE_LIMIT_ENABLE", limitEnv: "CRITICAL_RATE_LIMIT", durationEnv: "CRITICAL_RATE_LIMIT_DURATION",
			wantLimit: 20, wantWindow: 1200 * time.Second, wantKey: "rate:UC:test-scope:42:", userID: 42,
			factory: func() gin.HandlerFunc { return UserCriticalRateLimit("test-scope") },
		},
		{
			name: "user critical configured seconds", enableEnv: "CRITICAL_RATE_LIMIT_ENABLE", limitEnv: "CRITICAL_RATE_LIMIT", durationEnv: "CRITICAL_RATE_LIMIT_DURATION",
			limitValue: "3", windowValue: "2", wantLimit: 3, wantWindow: 2 * time.Second, wantKey: "rate:UC:test-scope:42:", userID: 42,
			factory: func() gin.HandlerFunc { return UserCriticalRateLimit("test-scope") },
		},
		{
			name: "search defaults", enableEnv: "SEARCH_RATE_LIMIT_ENABLE", limitEnv: "SEARCH_RATE_LIMIT", durationEnv: "SEARCH_RATE_LIMIT_DURATION",
			wantLimit: 10, wantWindow: 60 * time.Second, wantKey: "rate:SR:42:", userID: 42, factory: SearchRateLimit,
		},
		{
			name: "search configured seconds", enableEnv: "SEARCH_RATE_LIMIT_ENABLE", limitEnv: "SEARCH_RATE_LIMIT", durationEnv: "SEARCH_RATE_LIMIT_DURATION",
			limitValue: "3", windowValue: "2", wantLimit: 3, wantWindow: 2 * time.Second, wantKey: "rate:SR:42:", userID: 42, factory: SearchRateLimit,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.enableEnv, "true")
			t.Setenv(test.limitEnv, test.limitValue)
			t.Setenv(test.durationEnv, test.windowValue)
			store := &rateLimitStoreProbe{}
			useRateLimitStore(t, store)
			router := rateLimitTestRouter(test.factory(), test.userID)

			for request := 1; request <= test.wantLimit; request++ {
				assert.Equal(t, http.StatusNoContent, requestRateLimitedRoute(router), "request %d", request)
			}
			assert.Equal(t, http.StatusTooManyRequests, requestRateLimitedRoute(router))

			count, increments, keys, expirations := store.snapshot()
			assert.Equal(t, int64(test.wantLimit+1), count)
			assert.Equal(t, test.wantLimit+1, increments)
			require.NotEmpty(t, keys)
			assert.True(t, strings.HasPrefix(keys[0], test.wantKey), "unexpected rate-limit key %q", keys[0])
			// RateLimiter keeps the configured fixed window and adds a one-second
			// cleanup cushion to the backing-store TTL.
			require.Equal(t, []time.Duration{test.wantWindow + time.Second}, expirations)
		})
	}
}

func TestRateLimitEnableFlagsBypassTheStore(t *testing.T) {
	tests := []struct {
		name      string
		enableEnv string
		factory   func() gin.HandlerFunc
	}{
		{name: "global", enableEnv: "GLOBAL_API_RATE_LIMIT_ENABLE", factory: GlobalRateLimit},
		{name: "critical", enableEnv: "CRITICAL_RATE_LIMIT_ENABLE", factory: CriticalRateLimit},
		{name: "user critical", enableEnv: "CRITICAL_RATE_LIMIT_ENABLE", factory: func() gin.HandlerFunc { return UserCriticalRateLimit("disabled") }},
		{name: "search", enableEnv: "SEARCH_RATE_LIMIT_ENABLE", factory: SearchRateLimit},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.enableEnv, "false")
			store := &rateLimitStoreProbe{incrErr: errors.New("store must not be called")}
			useRateLimitStore(t, store)
			router := rateLimitTestRouter(test.factory(), 42)

			for request := 0; request < 3; request++ {
				assert.Equal(t, http.StatusNoContent, requestRateLimitedRoute(router))
			}
			_, increments, _, expirations := store.snapshot()
			assert.Zero(t, increments)
			assert.Empty(t, expirations)
		})
	}
}

func TestRateLimitStoreFailureStillFailsOpen(t *testing.T) {
	t.Setenv("GLOBAL_API_RATE_LIMIT_ENABLE", "true")
	t.Setenv("GLOBAL_API_RATE_LIMIT", "1")
	t.Setenv("GLOBAL_API_RATE_LIMIT_DURATION", "5")
	store := &rateLimitStoreProbe{incrErr: errors.New("store unavailable")}
	useRateLimitStore(t, store)
	router := rateLimitTestRouter(GlobalRateLimit(), 0)

	for request := 0; request < 3; request++ {
		assert.Equal(t, http.StatusNoContent, requestRateLimitedRoute(router))
	}
	_, increments, _, expirations := store.snapshot()
	assert.Equal(t, 3, increments)
	assert.Empty(t, expirations)
}

func TestGlobalRateLimitConcurrentAdmissionDoesNotExceedConfiguredLimit(t *testing.T) {
	const (
		limit    = 17
		requests = 128
	)
	t.Setenv("GLOBAL_API_RATE_LIMIT_ENABLE", "true")
	t.Setenv("GLOBAL_API_RATE_LIMIT", strconv.Itoa(limit))
	t.Setenv("GLOBAL_API_RATE_LIMIT_DURATION", "120")
	store := &rateLimitStoreProbe{}
	useRateLimitStore(t, store)
	router := rateLimitTestRouter(GlobalRateLimit(), 0)

	var allowed atomic.Int64
	var denied atomic.Int64
	var unexpected atomic.Int64
	var wait sync.WaitGroup
	for request := 0; request < requests; request++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			switch requestRateLimitedRoute(router) {
			case http.StatusNoContent:
				allowed.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	wait.Wait()

	assert.Equal(t, int64(limit), allowed.Load())
	assert.Equal(t, int64(requests-limit), denied.Load())
	assert.Zero(t, unexpected.Load())
	count, increments, _, expirations := store.snapshot()
	assert.Equal(t, int64(requests), count)
	assert.Equal(t, requests, increments)
	require.Equal(t, []time.Duration{121 * time.Second}, expirations)
}
