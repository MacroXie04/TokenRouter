package common

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedisExternalSharedStoreLifecycle verifies the production Redis wiring
// against a real server: two independent clients must observe one atomic
// counter, TTL state, and deletion. Ordinary unit runs skip this exact gate.
func TestRedisExternalSharedStoreLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_REDIS_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_REDIS_DSN to an isolated Redis database")
	}

	previousClient, previousEnabled, previousStore := RedisClient, RedisEnabled, Store
	RedisClient = nil
	RedisEnabled = false
	Store = newMemoryStore()
	var testClient *redis.Client
	var peer *redis.Client
	var cleanupKeys []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		if peer != nil {
			if len(cleanupKeys) > 0 {
				_ = peer.Del(cleanupCtx, cleanupKeys...).Err()
			}
			_ = peer.Close()
		}
		if testClient != nil {
			_ = testClient.Close()
		}
		RedisClient, RedisEnabled, Store = previousClient, previousEnabled, previousStore
	})
	t.Setenv("REDIS_CONN_STRING", dsn)
	t.Setenv("REDIS_POOL_SIZE", "32")
	require.NoError(t, InitRedis())
	require.True(t, RedisEnabled)
	require.NotNil(t, RedisClient)
	testClient = RedisClient
	assert.Equal(t, 32, RedisClient.Options().PoolSize)

	options, err := redis.ParseURL(dsn)
	require.NoError(t, err)
	peer = redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, peer.Ping(ctx).Err())

	prefix := "tokenrouter:external-redis:" + BestEffortRandomAlphanumeric(20)
	counterKey := prefix + ":counter"
	ttlKey := prefix + ":ttl"
	cleanupKeys = []string{counterKey, ttlKey}
	require.NoError(t, RedisClient.Del(ctx, counterKey, ttlKey).Err())

	const workers = 24
	const incrementsPerWorker = 50
	errs := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for worker := range workers {
		go func(worker int) {
			defer group.Done()
			var incrementErr error
			for range incrementsPerWorker {
				if worker%2 == 0 {
					_, incrementErr = Store.Incr(ctx, counterKey)
				} else {
					incrementErr = peer.Incr(ctx, counterKey).Err()
				}
				if incrementErr != nil {
					break
				}
			}
			errs <- incrementErr
		}(worker)
	}
	group.Wait()
	close(errs)
	for incrementErr := range errs {
		require.NoError(t, incrementErr)
	}

	expected := int64(workers * incrementsPerWorker)
	value, err := Store.Get(ctx, counterKey)
	require.NoError(t, err)
	assert.Equal(t, strconv.FormatInt(expected, 10), value)
	peerValue, err := peer.Get(ctx, counterKey).Int64()
	require.NoError(t, err)
	assert.Equal(t, expected, peerValue, "independent clients must share one atomic counter")

	require.NoError(t, Store.Set(ctx, ttlKey, "shared", 30*time.Second))
	peerValueText, err := peer.Get(ctx, ttlKey).Result()
	require.NoError(t, err)
	assert.Equal(t, "shared", peerValueText)
	ttl, err := Store.TTL(ctx, ttlKey)
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, 30*time.Second)

	require.NoError(t, Store.Del(ctx, counterKey, ttlKey))
	exists, err := peer.Exists(ctx, counterKey, ttlKey).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
}
