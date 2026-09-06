package cache

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterEnforcesLimit(t *testing.T) {
	// Use a fresh in-memory store so the test is isolated.
	prev := Store
	Store = newMemoryStore()
	defer func() { Store = prev }()

	rl := NewRateLimiter(3, time.Minute, "test")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ok, err := rl.Allow(ctx, "key")
		require.NoError(t, err)
		assert.True(t, ok, "request %d should be allowed", i+1)
	}
	ok, err := rl.Allow(ctx, "key")
	require.NoError(t, err)
	assert.False(t, ok, "4th request in window must be denied")
}

func TestRateLimiterConcurrentDoesNotExceedLimit(t *testing.T) {
	prev := Store
	Store = newMemoryStore()
	defer func() { Store = prev }()

	rl := NewRateLimiter(100, time.Minute, "test")
	ctx := context.Background()

	const n = 500
	var wg sync.WaitGroup
	var allowed int64
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := rl.Allow(ctx, "ckey")
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, allowed, int64(100), "atomic counter must not exceed the limit")
	assert.Equal(t, int64(100), allowed, "exactly limit requests should be admitted")
}
