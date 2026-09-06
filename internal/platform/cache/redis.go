package cache

import (
	"context"
	"errors"
	"github.com/go-redis/redis/v8"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KVStore is the minimal cache/atomic-counter interface used across the
// gateway. Both Redis and the in-memory fallback implement it.
type KVStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Exists(ctx context.Context, keys ...string) (int64, error)
	Incr(ctx context.Context, key string) (int64, error)
	Decr(ctx context.Context, key string) (int64, error)
	IncrBy(ctx context.Context, key string, delta int64) (int64, error)
	ExpireAt(ctx context.Context, key string, at time.Time) error
	TTL(ctx context.Context, key string) (time.Duration, error)
}

// RedisClient is the configured Redis client, or nil when Redis is disabled.
var RedisClient *redis.Client

// RedisEnabled reports whether a Redis connection string is configured.
var RedisEnabled = false

// Store is the active key-value store: Redis when available, otherwise an
// in-memory fallback. Multi-node deployments must configure Redis so that
// cache invalidation and rate limiting are shared.
var Store KVStore = newMemoryStore()

const (
	defaultRedisPoolSize = 10
	maxRedisPoolSize     = 10_000
)

// InitRedis parses REDIS_CONN_STRING and connects if present. An omitted
// connection string intentionally selects the in-memory single-node store. A
// configured but invalid or unreachable Redis endpoint is an error: silently
// degrading in that case would split rate limits and coordination state across
// nodes while operators believe the shared store is active.
func InitRedis() error {
	resetRedisState()
	conn := strings.TrimSpace(env.GetEnv("REDIS_CONN_STRING", ""))
	if conn == "" {
		return nil
	}
	opt, err := redis.ParseURL(conn)
	if err != nil {
		return errors.New("REDIS_CONN_STRING is invalid")
	}
	poolSize, err := redisPoolSize()
	if err != nil {
		return err
	}
	opt.PoolSize = poolSize
	client := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return errors.New("configured Redis is unreachable")
	}
	RedisClient = client
	RedisEnabled = true
	Store = &redisStore{client: client}
	logging.Logger.Info("redis connected")
	return nil
}

func redisPoolSize() (int, error) {
	raw := strings.TrimSpace(env.GetEnv("REDIS_POOL_SIZE", ""))
	if raw == "" {
		return defaultRedisPoolSize, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(value) != raw || value < 1 || value > maxRedisPoolSize {
		return 0, errors.New("REDIS_POOL_SIZE must be an integer from 1 to 10000")
	}
	return value, nil
}

func resetRedisState() {
	if RedisClient != nil {
		_ = RedisClient.Close()
	}
	RedisClient = nil
	RedisEnabled = false
	Store = newMemoryStore()
}

// redisStore adapts go-redis v8 to KVStore.
type redisStore struct {
	client *redis.Client
}

func (r *redisStore) Get(ctx context.Context, key string) (string, error) {
	v, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

func (r *redisStore) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *redisStore) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

func (r *redisStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.Expire(ctx, key, ttl).Err()
}

func (r *redisStore) Exists(ctx context.Context, keys ...string) (int64, error) {
	return r.client.Exists(ctx, keys...).Result()
}

func (r *redisStore) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, key).Result()
}

func (r *redisStore) Decr(ctx context.Context, key string) (int64, error) {
	return r.client.Decr(ctx, key).Result()
}

func (r *redisStore) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	return r.client.IncrBy(ctx, key, delta).Result()
}

func (r *redisStore) ExpireAt(ctx context.Context, key string, at time.Time) error {
	return r.client.ExpireAt(ctx, key, at).Err()
}

func (r *redisStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	return r.client.TTL(ctx, key).Result()
}

// memEntry is a cached value with an optional expiry.
type memEntry struct {
	value  string
	expire time.Time // zero means no expiry
}

// memoryStore is a process-local KVStore with expiry semantics, used as the
// Redis fallback and in tests. It is not shared across nodes.
type memoryStore struct {
	mu    sync.RWMutex
	items map[string]*memEntry
}

func newMemoryStore() *memoryStore {
	return &memoryStore{items: make(map[string]*memEntry)}
}

func (m *memoryStore) get(ctx context.Context, key string) (string, bool) {
	m.mu.RLock()
	e, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	if !e.expire.IsZero() && time.Now().After(e.expire) {
		m.mu.Lock()
		delete(m.items, key)
		m.mu.Unlock()
		return "", false
	}
	return e.value, true
}

func (m *memoryStore) Get(ctx context.Context, key string) (string, error) {
	v, _ := m.get(ctx, key)
	return v, nil
}

func (m *memoryStore) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var expire time.Time
	if ttl > 0 {
		expire = time.Now().Add(ttl)
	}
	m.items[key] = &memEntry{value: toString(value), expire: expire}
	return nil
}

func (m *memoryStore) Del(ctx context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.items, k)
	}
	return nil
}

func (m *memoryStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return nil
	}
	e.expire = time.Now().Add(ttl)
	return nil
}

func (m *memoryStore) ExpireAt(ctx context.Context, key string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return nil
	}
	e.expire = at
	return nil
}

func (m *memoryStore) Exists(ctx context.Context, keys ...string) (int64, error) {
	var n int64
	for _, k := range keys {
		if _, ok := m.get(ctx, k); ok {
			n++
		}
	}
	return n, nil
}

func (m *memoryStore) Incr(ctx context.Context, key string) (int64, error) {
	return m.incrBy(ctx, key, 1)
}

func (m *memoryStore) Decr(ctx context.Context, key string) (int64, error) {
	return m.incrBy(ctx, key, -1)
}

func (m *memoryStore) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	return m.incrBy(ctx, key, delta)
}

func (m *memoryStore) incrBy(ctx context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	var cur int64
	if ok && !e.expired() {
		cur = parseInt64(e.value)
	} else if ok && e.expired() {
		delete(m.items, key)
	}
	cur += delta
	m.items[key] = &memEntry{value: itoa64(cur)}
	return cur, nil
}

func (m *memoryStore) TTL(ctx context.Context, key string) (time.Duration, error) {
	m.mu.RLock()
	e, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return -2 * time.Second, nil
	}
	if e.expire.IsZero() {
		return -1 * time.Second, nil
	}
	remaining := time.Until(e.expire)
	if remaining < 0 {
		return -2 * time.Second, nil
	}
	return remaining, nil
}

func (e *memEntry) expired() bool {
	return !e.expire.IsZero() && time.Now().After(e.expire)
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return textutil.Sprint(t)
	}
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			continue
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
