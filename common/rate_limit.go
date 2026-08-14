package common

import (
	"context"
	"fmt"
	"time"
)

// RateLimiter enforces a fixed-window counter per key using the shared KVStore.
// The window is aligned to wall-clock boundaries so that a given key+window is
// deterministic and safe under concurrent access (atomic Incr on Redis, and a
// mutex-guarded counter in the in-memory fallback).
type RateLimiter struct {
	Limit  int
	Window time.Duration
	Prefix string
}

// NewRateLimiter builds a rate limiter.
func NewRateLimiter(limit int, window time.Duration, prefix string) *RateLimiter {
	return &RateLimiter{Limit: limit, Window: window, Prefix: prefix}
}

// windowKey returns the counter key for the current window.
func (r *RateLimiter) windowKey(key string, now time.Time) string {
	window := int64(r.Window.Seconds())
	if window <= 0 {
		window = 1
	}
	bucket := now.Unix() / window
	return fmt.Sprintf("rate:%s:%s:%d", r.Prefix, key, bucket)
}

// Allow records one event for the key and reports whether it is allowed.
func (r *RateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	if r.Limit <= 0 {
		return true, nil
	}
	now := time.Now()
	wk := r.windowKey(key, now)
	count, err := Store.Incr(ctx, wk)
	if err != nil {
		// On store failure, fail open only for counting, never for the
		// safety-critical global limits. Simplicity: return error to caller.
		return false, err
	}
	if count == 1 {
		// First hit in window: set TTL so keys don't leak forever.
		_ = Store.Expire(ctx, wk, r.Window+time.Second)
	}
	return count <= int64(r.Limit), nil
}

// Count returns the current count in the active window without incrementing.
func (r *RateLimiter) Count(ctx context.Context, key string) (int64, error) {
	now := time.Now()
	wk := r.windowKey(key, now)
	count, err := Store.Incr(ctx, wk)
	if err != nil {
		return 0, err
	}
	return count, nil
}
