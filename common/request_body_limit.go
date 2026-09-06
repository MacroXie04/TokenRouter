package common

import (
	"errors"
	"net/http"
)

// ErrRequestBodyTooLarge is returned when a request body exceeds its limit.
var ErrRequestBodyTooLarge = errors.New("request body too large")

// IsRequestBodyTooLargeError reports whether err indicates an oversized
// request body, either from our own limit or from a http.MaxBytesError.
func IsRequestBodyTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return true
	}
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

const (
	// defaultAnonymousRequestBodyLimitKB is the fallback limit for anonymous
	// request bodies (ANONYMOUS_REQUEST_BODY_LIMIT_KB).
	defaultAnonymousRequestBodyLimitKB = 512
	// Anonymous requests are also covered by the 16 MiB process-wide ceiling.
	// Bounding the configuration here prevents an overflowing KB-to-byte shift
	// from accidentally turning the narrower limiter off.
	maxAnonymousRequestBodyLimitKB = 16 << 10
)

// GetAnonymousRequestBodyLimitBytes returns the anonymous request-body limit
// in bytes. Values outside 0..16 MiB fall back to the default; zero disables
// the narrower limit (the process-wide request ceiling still applies).
func GetAnonymousRequestBodyLimitBytes() int64 {
	limitKB := GetEnvInt("ANONYMOUS_REQUEST_BODY_LIMIT_KB", defaultAnonymousRequestBodyLimitKB)
	if limitKB < 0 || limitKB > maxAnonymousRequestBodyLimitKB {
		limitKB = defaultAnonymousRequestBodyLimitKB
	}
	return int64(limitKB) * 1024
}
