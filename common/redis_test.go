package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitRedisKeepsExplicitNoRedisModeLocal(t *testing.T) {
	previousClient, previousEnabled, previousStore := RedisClient, RedisEnabled, Store
	RedisClient = nil
	t.Cleanup(func() {
		RedisClient, RedisEnabled, Store = previousClient, previousEnabled, previousStore
	})
	t.Setenv("REDIS_CONN_STRING", "")

	require.NoError(t, InitRedis())
	assert.False(t, RedisEnabled)
	assert.Nil(t, RedisClient)
	_, ok := Store.(*memoryStore)
	assert.True(t, ok)
}

func TestInitRedisFailsClosedForConfiguredInvalidEndpointWithoutLeakingIt(t *testing.T) {
	previousClient, previousEnabled, previousStore := RedisClient, RedisEnabled, Store
	RedisClient = nil
	t.Cleanup(func() {
		RedisClient, RedisEnabled, Store = previousClient, previousEnabled, previousStore
	})
	const secret = "redis-password-must-not-leak"
	t.Setenv("REDIS_CONN_STRING", "://:"+secret+"@cache.example.test/0")

	err := InitRedis()
	require.EqualError(t, err, "REDIS_CONN_STRING is invalid")
	assert.NotContains(t, err.Error(), secret)
	assert.False(t, RedisEnabled)
	assert.Nil(t, RedisClient)
	_, ok := Store.(*memoryStore)
	assert.True(t, ok)
}

func TestRedisPoolSizeIsStrictAndBoundedBeforeConnecting(t *testing.T) {
	previousClient, previousEnabled, previousStore := RedisClient, RedisEnabled, Store
	RedisClient = nil
	t.Cleanup(func() {
		RedisClient, RedisEnabled, Store = previousClient, previousEnabled, previousStore
	})
	t.Setenv("REDIS_CONN_STRING", "redis://127.0.0.1:6379/0")

	for _, value := range []string{"0", "01", "-1", "10001", "many"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("REDIS_POOL_SIZE", value)
			require.EqualError(t, InitRedis(), "REDIS_POOL_SIZE must be an integer from 1 to 10000")
			assert.False(t, RedisEnabled)
			assert.Nil(t, RedisClient)
		})
	}

	t.Setenv("REDIS_POOL_SIZE", "")
	poolSize, err := redisPoolSize()
	require.NoError(t, err)
	assert.Equal(t, defaultRedisPoolSize, poolSize)
	t.Setenv("REDIS_POOL_SIZE", "10000")
	poolSize, err = redisPoolSize()
	require.NoError(t, err)
	assert.Equal(t, maxRedisPoolSize, poolSize)
}
